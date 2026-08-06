package payment

import (
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// TestTheAdmissionMessagesThisSystemEmitsValidateAgainstTheSchema is the only
// check in this repository that a document THIS package builds is schema-valid.
//
// # Why it exists here and not in iso20022
//
// iso20022's TestGoldenFilesValidateAgainstTheSchema validates hand-written
// golden files: it says the STRUCTS can express a valid document. It does not
// say that what a builder in this package fills them with is one, and the two
// are different claims — a mandatory element left empty by a builder produces a
// document that fails the schema and passes every test in that package. That is
// not hypothetical in this repository: camt.053's field order was wrong for a
// whole sub-project underneath a comment asserting it was right, and every
// statement the system emitted was invalid.
//
// It covers the three admission messages and no others. The rest of
// translate.go's builders — the pacs family and the camt.053 — have the same gap
// and closing it for them is a sweep of its own; what was not acceptable was
// adding three message definitions and leaving the claim unchecked on the day it
// was made.
//
// # It skips, and a skip is not a pass
//
// Same convention as iso20022's check and for the same reason: there is no
// usable pure-Go XSD validator, the schemas are ISO's to redistribute rather
// than this repository's to vendor, and shelling out to xmllint is what is left.
// Set ISO20022_REQUIRE_SCHEMAS — or run `make test-schemas`, which runs this
// test as well as the golden one — and every skip becomes a failure.
//
// The DOCUMENT alone is validated, without the Envelope wrapper, because that
// wrapper is this repository's own invention and appears in no XSD. The header
// is head.001.001.02 and is covered by the golden check, which builds it from
// the same MessageContext.header this file's builders use.
func TestTheAdmissionMessagesThisSystemEmitsValidateAgainstTheSchema(t *testing.T) {
	bin, err := exec.LookPath("xmllint")
	if err != nil {
		skipUnlessSchemasRequired(t, "xmllint not installed; see iso20022/testdata/README.md")
	}

	mc := MessageContext{From: "NORDSESSXXX", To: "CSMXFRPPXXX", MsgID: "nord-1", Now: readNow}
	request, err := AdmissionMessage(
		AdmissionRequest{Name: "Nordhaven Bank", BIC: "NORDSESSXXX", Asset: "EUR", Ref: "adm-1"},
		"CBXXDEFFXXX", mc)
	if err != nil {
		t.Fatalf("AdmissionMessage: %v", err)
	}
	// TWO accounts, because the acknowledgement is the asymmetric half: one
	// acmt.007 asks for one currency and one acmt.010 lists every account the
	// servicer holds. A single-account fixture would leave the repeated element
	// unvalidated.
	acknowledgement, err := AdmissionAcknowledgementMessage(AdmissionAcknowledgement{
		BIC:      "NORDSESSXXX",
		Ref:      "adm-1",
		Accounts: map[ledger.AssetCode]ledger.AccountID{"EUR": "acc_eur", "USD": "acc_usd"},
	}, MessageContext{From: "CBXXDEFFXXX", To: "CSMXFRPPXXX", MsgID: "cb-1", Now: readNow})
	if err != nil {
		t.Fatalf("AdmissionAcknowledgementMessage: %v", err)
	}
	rejection, err := AdmissionRejectionMessage(
		AdmissionRequest{Name: "Nordhaven Bank", BIC: "NORDSESSXXX", Asset: "EUR", Ref: "adm-1"},
		iso20022.MessageIdentification{Id: "nord-1", CreDtTm: iso20022.ISODateTime{Time: readNow}},
		"NORDSESSXXX is admitted under another admission",
		MessageContext{From: "CSMXFRPPXXX", To: "NORDSESSXXX", MsgID: "csm-1", Now: readNow})
	if err != nil {
		t.Fatalf("AdmissionRejectionMessage: %v", err)
	}

	for _, tc := range []struct {
		name   string
		schema string
		env    iso20022.Envelope
	}{
		{"acmt.007", "acmt.007.001.03.xsd", request},
		{"acmt.010", "acmt.010.001.03.xsd", acknowledgement},
		{"acmt.011", "acmt.011.001.03.xsd", rejection},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// iso20022/testdata/xsd, reached from here, because the schemas are
			// downloaded once into the package that owns the codec rather than
			// copied per caller. They are gitignored, so this is a path that
			// exists on a machine somebody has prepared and nowhere else.
			schemaPath := filepath.Join("..", "iso20022", "testdata", "xsd", tc.schema)
			if _, err := os.Stat(schemaPath); err != nil {
				skipUnlessSchemasRequired(t, "%s not present; see iso20022/testdata/README.md", schemaPath)
			}
			body, err := xml.MarshalIndent(tc.env.Document, "", "  ")
			if err != nil {
				t.Fatalf("marshalling the %s: %v", tc.name, err)
			}
			file := filepath.Join(t.TempDir(), tc.name+".xml")
			if err := os.WriteFile(file, append([]byte(xml.Header), body...), 0o600); err != nil {
				t.Fatalf("writing %s: %v", file, err)
			}
			if out, err := exec.Command(bin, "--noout", "--schema", schemaPath, file).CombinedOutput(); err != nil {
				t.Fatalf("xmllint rejected the %s this system emits:\n%s", tc.name, out)
			}
		})
	}
}

// skipUnlessSchemasRequired skips the calling test, or fails it when the schema
// check has been made mandatory.
//
// It repeats iso20022's own switch rather than importing it, because that one
// is unexported and test-only. The two must agree on the variable's NAME, which
// is what `make test-schemas` sets, and on nothing else.
func skipUnlessSchemasRequired(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("ISO20022_REQUIRE_SCHEMAS") != "" {
		t.Fatalf("ISO20022_REQUIRE_SCHEMAS is set, so this is a failure and not a skip: "+format, args...)
	}
	t.Skipf(format, args...)
}
