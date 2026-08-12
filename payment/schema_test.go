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
	// validated rather than a short string that would have fitted anyway.
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

// validateEmitted marshals one document this package built and runs xmllint
// over it against its own schema.
func validateEmitted(t *testing.T, bin, name, schema string, env iso20022.Envelope) {
	t.Helper()
	// iso20022/testdata/xsd, reached from here, because the schemas are downloaded
	// once into the package that owns the codec rather than copied per caller.
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
func skipUnlessSchemasRequired(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("ISO20022_REQUIRE_SCHEMAS") != "" {
		t.Fatalf("ISO20022_REQUIRE_SCHEMAS is set, so this is a failure and not a skip: "+format, args...)
	}
	t.Skipf(format, args...)
}
