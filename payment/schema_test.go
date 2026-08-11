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
)

// TestTheLodgementMessagesThisSystemEmitsValidateAgainstTheSchema is the same
// check for the lodgement's two, and it exists for the sentence the admission
// test's doc ends on: a message definition added without this is a claim nobody
// checked.
//
// It matters more here than it did there, because these two schemas were not in
// the repository when the messages were written. camt.050.001.05.xsd and
// camt.025.001.05.xsd had to be downloaded before either could be validated at
// all, and until they were, both message files rested entirely on having been
// read carefully — which is the state camt.053 was in while every statement the
// system emitted was invalid.
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
// camt.025's Desc is Max140Text, and the refusals that reach it are Go error
// strings quoting a BIC, an asset and two account ids. An over-long one would
// produce a document xmllint rejects — and, worse, one the central bank's own
// handler would fail to build, so the member would be told nothing at all.
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
