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
const requireSchemasEnv = "ISO20022_REQUIRE_SCHEMAS"

// schemaCheckRequired reports whether an absent xmllint or an absent schema is
// a failure rather than a reason to skip.
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
		"camt053.xml": "camt.053.001.08.xsd",
		"camt050.xml": "camt.050.001.05.xsd",
		"camt025.xml": "camt.025.001.05.xsd",
	}

	// validate writes one fragment to a temporary file and runs xmllint over it.
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
