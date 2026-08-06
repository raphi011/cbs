package payment

import (
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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
// It covers the three admission messages. The rest of translate.go's builders —
// the pacs family and the camt.053 — have the same gap and closing it for them is
// a sweep of its own; what was not acceptable was adding three message definitions
// and leaving the claim unchecked on the day it was made.
//
// The lodgement's two are checked by
// TestTheLodgementMessagesThisSystemEmitsValidateAgainstTheSchema, in this file,
// held to the same standard on the same reasoning.
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
			validateEmitted(t, bin, tc.name, tc.schema, tc.env)
		})
	}
}

// TestTheLodgementMessagesThisSystemEmitsValidateAgainstTheSchema is the same
// check for the lodgement's two, and it exists for the sentence the admission
// test's doc ends on: a message definition added without this is a claim nobody
// checked.
//
// It matters more here than it did there, because these two schemas were not in
// the repository when the messages were written. camt.050.001.05.xsd and
// camt.025.001.05.xsd had to be downloaded before either could be validated at
// all, and until they were, both message files rested entirely on having been
// read carefully — which is the state camt.053 was in for a whole sub-project
// while every statement the system emitted was invalid.
//
// The REFUSING receipt is the case worth having, and it is the second subtest
// rather than a variation of the first. An accepted receipt carries no Desc at
// all, so a document with the element populated is not exercised by the happy
// path — and Desc is the element carrying a truncated Go error string, which is
// the one thing here most likely to produce something a schema rejects.
func TestTheLodgementMessagesThisSystemEmitsValidateAgainstTheSchema(t *testing.T) {
	bin, err := exec.LookPath("xmllint")
	if err != nil {
		skipUnlessSchemasRequired(t, "xmllint not installed; see iso20022/testdata/README.md")
	}

	mc := MessageContext{From: "NORDSESSXXX", To: "CBXXDEFFXXX", MsgID: "nord-lodge-1", Now: readNow}
	request, err := LodgementMessage(LodgementInstruction{
		BIC:     "NORDSESSXXX",
		Agent:   "CBXXDEFFXXX",
		Account: "acc_cb_reserve_nord_eur",
		Asset:   "EUR",
		Amount:  500_000,
		Ref:     "nord-lodge-1",
	}, mc)
	if err != nil {
		t.Fatalf("LodgementMessage: %v", err)
	}

	answered := MessageContext{From: "CBXXDEFFXXX", To: "NORDSESSXXX", MsgID: "cb-rcpt-1", Now: readNow}
	accepted, err := LodgementReceiptMessage(LodgementReceipt{
		Ref:    "nord-lodge-1",
		Status: iso20022.TransactionStatusSettlementCompleted,
	}, answered)
	if err != nil {
		t.Fatalf("LodgementReceiptMessage (accepted): %v", err)
	}
	// A reason LONGER than Max140Text, so that the truncation is what gets
	// validated rather than a short string that would have fitted anyway. The
	// refusals that reach this element are Go error strings quoting a BIC, an
	// asset and two account ids, which really do exceed 140 characters.
	refused, err := LodgementReceiptMessage(LodgementReceipt{
		Ref:    "nord-lodge-1",
		Status: iso20022.TransactionStatusRejected,
		Reason: strings.Repeat("this agent holds no settlement account for NORDSESSXXX in EUR. ", 5),
	}, answered)
	if err != nil {
		t.Fatalf("LodgementReceiptMessage (refused): %v", err)
	}

	for _, tc := range []struct {
		name   string
		schema string
		env    iso20022.Envelope
	}{
		{"camt.050", "camt.050.001.05.xsd", request},
		{"camt.025-accepted", "camt.025.001.05.xsd", accepted},
		{"camt.025-refused", "camt.025.001.05.xsd", refused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validateEmitted(t, bin, tc.name, tc.schema, tc.env)
		})
	}
}

// TestARefusalReasonIsTruncatedToFitTheElement pins the narrowing
// LodgementReceiptMessage makes, and it is about a document that would not
// marshal rather than about tidiness.
//
// camt.025's Desc is Max140Text and acmt.011's RjctnRsn is Max350Text, so a
// refusal reason that fits an admission may not fit a receipt. An over-long one
// would produce a document xmllint rejects — and, worse, one the central bank's
// own handler would fail to build, so the member would be told nothing at all.
//
// Three claims, because the obvious implementation gets the first two wrong: the
// result fits, the ellipsis is INSIDE the limit rather than added to it, and the
// cut lands on a rune boundary so the result is still valid UTF-8.
func TestARefusalReasonIsTruncatedToFitTheElement(t *testing.T) {
	long := strings.Repeat("é", 400)
	got := truncateTo(long, 140)
	if n := len([]rune(got)); n != 140 {
		t.Errorf("truncateTo returned %d characters, want exactly 140; an ellipsis added to the "+
			"limit rather than fitted inside it produces the invalid document this exists to prevent", n)
	}
	if !utf8.ValidString(got) {
		t.Error("truncateTo cut through a multi-byte rune and returned invalid UTF-8")
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("a shortened reason does not say it was shortened")
	}
	if short := "no account"; truncateTo(short, 140) != short {
		t.Error("truncateTo altered a reason that already fitted")
	}
}

// validateEmitted marshals one document this package built and runs xmllint over
// it against its own schema.
//
// It is shared by the two tests above rather than copied, because what it encodes
// is the awkward part: the DOCUMENT alone is validated, without the Envelope
// wrapper, since that wrapper is this repository's own invention and appears in no
// XSD.
func validateEmitted(t *testing.T, bin, name, schema string, env iso20022.Envelope) {
	t.Helper()
	// iso20022/testdata/xsd, reached from here, because the schemas are
	// downloaded once into the package that owns the codec rather than
	// copied per caller. They are gitignored, so this is a path that
	// exists on a machine somebody has prepared and nowhere else.
	schemaPath := filepath.Join("..", "iso20022", "testdata", "xsd", schema)
	if _, err := os.Stat(schemaPath); err != nil {
		skipUnlessSchemasRequired(t, "%s not present; see iso20022/testdata/README.md", schemaPath)
	}
	body, err := xml.MarshalIndent(env.Document, "", "  ")
	if err != nil {
		t.Fatalf("marshalling the %s: %v", name, err)
	}
	file := filepath.Join(t.TempDir(), name+".xml")
	if err := os.WriteFile(file, append([]byte(xml.Header), body...), 0o600); err != nil {
		t.Fatalf("writing %s: %v", file, err)
	}
	if out, err := exec.Command(bin, "--noout", "--schema", schemaPath, file).CombinedOutput(); err != nil {
		t.Fatalf("xmllint rejected the %s this system emits:\n%s", name, out)
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
