package iso20022

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireSchemasEnv, when set to any non-empty value, turns every skip in the
// schema check into a failure.
//
// It is what makes "a skip is not a pass" a thing somebody can enforce rather
// than a sentence in a comment. Without it the check had exactly one outcome
// available to it on a machine without the schemas — skip — and the parent
// test still printed PASS, so nothing would ever have noticed the schemas
// going missing from a machine or a CI job that HAD them. `make test-schemas`
// is the command that sets it.
const requireSchemasEnv = "ISO20022_REQUIRE_SCHEMAS"

// schemaCheckRequired reports whether an absent xmllint or an absent schema is
// a failure rather than a reason to skip.
//
// Any non-empty value counts, including "0": this is a switch, and a developer
// who set it to anything at all meant to turn it on. See
// TestSchemaCheckIsRequiredOnlyWhenAsked, which pins that.
func schemaCheckRequired() bool {
	return os.Getenv(requireSchemasEnv) != ""
}

// skipUnlessRequired skips the calling test, or fails it when the schema check
// has been made mandatory.
func skipUnlessRequired(t *testing.T, format string, args ...any) {
	t.Helper()
	if schemaCheckRequired() {
		t.Fatalf("%s is set, so this is a failure and not a skip: %s",
			requireSchemasEnv, fmt.Sprintf(format, args...))
	}
	t.Skipf(format, args...)
}

// TestSchemaCheckIsRequiredOnlyWhenAsked pins the switch itself, because the
// comment above asserts a behaviour of this file's code and everything else
// here can only be exercised on a machine that has the schemas.
func TestSchemaCheckIsRequiredOnlyWhenAsked(t *testing.T) {
	t.Setenv(requireSchemasEnv, "")
	if schemaCheckRequired() {
		t.Fatalf("%s unset: schemaCheckRequired() = true, want the check to stay skippable", requireSchemasEnv)
	}
	for _, value := range []string{"1", "0", "yes"} {
		t.Setenv(requireSchemasEnv, value)
		if !schemaCheckRequired() {
			t.Fatalf("%s=%q: schemaCheckRequired() = false, want any non-empty value to make the check mandatory",
				requireSchemasEnv, value)
		}
	}
}

// TestGoldenFilesValidateAgainstTheSchema is the only check that this package's
// output is really schema-valid rather than merely round-trip-stable.
//
// It cannot be a required test by default. There is no usable pure-Go XSD
// validator, and taking a cgo dependency on libxml2 would cost this repository
// its "no dependencies beyond pgx" property for a check that runs on five
// files. So it shells out to xmllint and skips when the tool or the schemas are
// absent — see testdata/README.md for how to obtain them.
//
// A skip is not a pass. When this test is skipped, the golden files rest
// entirely on having been written carefully — so set ISO20022_REQUIRE_SCHEMAS
// (or run `make test-schemas`) and every skip below becomes a failure. That is
// the difference between a check nobody can be held to and one a CI job can.
//
// Both halves of each envelope are checked, not just the message. The header is
// a standard element with a schema of its own, and validating only the Document
// would leave testdata/README.md telling a reader to download
// head.001.001.02.xsd for a check that never opened it.
func TestGoldenFilesValidateAgainstTheSchema(t *testing.T) {
	bin, err := exec.LookPath("xmllint")
	if err != nil {
		skipUnlessRequired(t, "xmllint not installed; see testdata/README.md")
	}

	files := map[string]string{
		"pacs008.xml": "pacs.008.001.08.xsd",
		"pacs003.xml": "pacs.003.001.08.xsd",
		"pacs002.xml": "pacs.002.001.10.xsd",
		"pacs004.xml": "pacs.004.001.09.xsd",
		"pacs009.xml": "pacs.009.001.08.xsd",
	}

	// validate writes one fragment to a temporary file and runs xmllint over
	// it. It skips rather than fails when the schema is absent, because an
	// absent schema means the check was never enabled, not that the fragment
	// is wrong — unless ISO20022_REQUIRE_SCHEMAS says the caller expected it
	// to be enabled, in which case an absent schema is the failure.
	validate := func(t *testing.T, name, schema string, body []byte) {
		t.Helper()
		schemaPath := filepath.Join("testdata", "xsd", schema)
		if _, err := os.Stat(schemaPath); err != nil {
			skipUnlessRequired(t, "%s not present; see testdata/README.md", schemaPath)
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
