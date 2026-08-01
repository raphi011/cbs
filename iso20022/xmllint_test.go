package iso20022

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGoldenFilesValidateAgainstTheSchema is the only check that this package's
// output is really schema-valid rather than merely round-trip-stable.
//
// It cannot be a required test. There is no usable pure-Go XSD validator, and
// taking a cgo dependency on libxml2 would cost this repository its "no
// dependencies beyond pgx" property for a check that runs on four files. So it
// shells out to xmllint and skips when the tool or the schemas are absent —
// see testdata/README.md for how to obtain them.
//
// A skip is not a pass. When this test is skipped, the golden files rest
// entirely on having been written carefully.
//
// Both halves of each envelope are checked, not just the message. The header is
// a standard element with a schema of its own, and validating only the Document
// would leave testdata/README.md telling a reader to download
// head.001.001.02.xsd for a check that never opened it.
func TestGoldenFilesValidateAgainstTheSchema(t *testing.T) {
	bin, err := exec.LookPath("xmllint")
	if err != nil {
		t.Skip("xmllint not installed; see testdata/README.md")
	}

	files := map[string]string{
		"pacs008.xml": "pacs.008.001.08.xsd",
		"pacs003.xml": "pacs.003.001.08.xsd",
		"pacs002.xml": "pacs.002.001.10.xsd",
		"pacs004.xml": "pacs.004.001.09.xsd",
	}

	// validate writes one fragment to a temporary file and runs xmllint over
	// it. It skips rather than fails when the schema is absent, because an
	// absent schema means the check was never enabled, not that the fragment
	// is wrong.
	validate := func(t *testing.T, name, schema string, body []byte) {
		t.Helper()
		schemaPath := filepath.Join("testdata", "xsd", schema)
		if _, err := os.Stat(schemaPath); err != nil {
			t.Skipf("%s not present; see testdata/README.md", schemaPath)
		}
		tmp := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(tmp, body, 0o600); err != nil {
			t.Fatalf("writing %s: %v", tmp, err)
		}
		out, err := exec.Command(bin, "--noout", "--schema", schemaPath, tmp).CombinedOutput()
		if err != nil {
			t.Fatalf("xmllint rejected %s:\n%s", name, out)
		}
	}

	for doc, schema := range files {
		t.Run(doc, func(t *testing.T) {
			golden, err := os.ReadFile(filepath.Join("testdata", doc))
			if err != nil {
				t.Fatalf("reading %s: %v", doc, err)
			}
			env, err := Unmarshal(golden)
			if err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", doc, err)
			}

			t.Run("Document", func(t *testing.T) {
				body, err := marshalDocumentOnly(env)
				if err != nil {
					t.Fatalf("marshalling document of %s: %v", doc, err)
				}
				validate(t, doc, schema, body)
			})
			t.Run("AppHdr", func(t *testing.T) {
				body, err := marshalHeaderOnly(env)
				if err != nil {
					t.Fatalf("marshalling header of %s: %v", doc, err)
				}
				validate(t, "hdr-"+doc, "head.001.001.02.xsd", body)
			})
		})
	}
}

// marshalDocumentOnly renders just the message, without the Envelope wrapper.
//
// It exists for the schema check: the wrapper is this repository's own
// invention and appears in no XSD, so validating the whole envelope would fail
// on the one element the standard has nothing to say about.
func marshalDocumentOnly(env Envelope) ([]byte, error) {
	if env.Document == nil {
		return nil, fmt.Errorf("%w: Document", ErrMissingElement)
	}
	if err := env.Document.validate(); err != nil {
		return nil, err
	}
	body, err := xml.MarshalIndent(env.Document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}

// marshalHeaderOnly renders just the business application header, for the same
// reason and against head.001.001.02.
func marshalHeaderOnly(env Envelope) ([]byte, error) {
	if err := env.AppHdr.validate(); err != nil {
		return nil, err
	}
	body, err := xml.MarshalIndent(env.AppHdr, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}
