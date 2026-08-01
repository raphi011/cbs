package iso20022

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// sample009 is one net settlement instruction: the clearing house telling the
// central bank to move reserves from one member to another.
func sample009() Envelope {
	amt, err := NewAmount(250000, 2, "EUR")
	if err != nil {
		panic(err)
	}
	when := time.Date(2026, 8, 1, 10, 30, 0, 0, time.UTC)
	return Envelope{
		AppHdr: AppHdr{
			Fr:        agent("CSMXFRPPXXX"),
			To:        agent("CBSEDEFFXXX"),
			BizMsgIdr: "CSM-SETTLE-1",
			MsgDefIdr: "pacs.009.001.08",
			CreDt:     ISODateTime{when},
		},
		Document: &Pacs009{
			FICdtTrf: FIToFIFinancialInstitutionCreditTransfer{
				GrpHdr: CreditTransferGroupHeader{
					MsgId:             "CSM-SETTLE-1",
					CreDtTm:           ISODateTime{when},
					NbOfTxs:           "1",
					TtlIntrBkSttlmAmt: &amt,
					IntrBkSttlmDt:     ISODate{when},
					SttlmInf:          SettlementInstruction{SttlmMtd: SettlementMethodClearing},
				},
				CdtTrfTxInf: []FinancialInstitutionCreditTransferTransaction{{
					PmtId:          PaymentIdentification{InstrId: "cyc_1:bank_1", EndToEndId: "cyc_1:bank_1", TxId: "cyc_1:bank_1"},
					IntrBkSttlmAmt: amt,
					IntrBkSttlmDt:  ISODate{when},
					Dbtr:           BranchAndFinancialInstitution{FinInstnId: FinancialInstitutionIdentification{BICFI: "AURODEFFXXX"}},
					Cdtr:           BranchAndFinancialInstitution{FinInstnId: FinancialInstitutionIdentification{BICFI: "VERDITMMXXX"}},
				}},
			},
		},
	}
}

// TestPacs009GoldenRoundTrip pins testdata/pacs009.xml the same way
// TestPacs008RoundTrip and its siblings pin their own golden files: this is
// what actually holds FinancialInstitutionCreditTransferTransaction's field
// order and Pacs009's namespace to the committed sample. Without it, nothing
// in this package's non-skipping test run ever reads pacs009.xml — the
// schema check skips without vendored XSDs, and the fuzz corpus only checks
// that Marshal succeeds on it, not that the bytes match.
func TestPacs009GoldenRoundTrip(t *testing.T) {
	env := assertGoldenRoundTrip(t, "pacs009.xml")

	doc, ok := env.Document.(*Pacs009)
	if !ok {
		t.Fatalf("Document is %T, want *Pacs009", env.Document)
	}
	tx := doc.FICdtTrf.CdtTrfTxInf
	if len(tx) != 1 {
		t.Fatalf("CdtTrfTxInf has %d entries, want 1", len(tx))
	}
	if got := tx[0].Dbtr.FinInstnId.BICFI; got != "AURODEFFXXX" {
		t.Fatalf("debtor agent = %q, want AURODEFFXXX", got)
	}
	if got := tx[0].Cdtr.FinInstnId.BICFI; got != "VERDITMMXXX" {
		t.Fatalf("creditor agent = %q, want VERDITMMXXX", got)
	}
	minor, err := tx[0].IntrBkSttlmAmt.Minor(2)
	if err != nil {
		t.Fatalf("Minor() error = %v", err)
	}
	if minor != 250000 {
		t.Fatalf("amount = %d minor units, want 250000", minor)
	}
}

func TestPacs009RoundTrips(t *testing.T) {
	raw, err := Marshal(sample009())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), "pacs.009.001.08") {
		t.Fatalf("marshalled document does not carry its namespace:\n%s", raw)
	}
	got, err := Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	doc, ok := got.Document.(*Pacs009)
	if !ok {
		t.Fatalf("Unmarshal returned %T, want *Pacs009", got.Document)
	}
	tx := doc.FICdtTrf.CdtTrfTxInf[0]
	if tx.Dbtr.FinInstnId.BICFI != "AURODEFFXXX" {
		t.Errorf("debtor agent = %q, want AURODEFFXXX", tx.Dbtr.FinInstnId.BICFI)
	}
	if tx.Cdtr.FinInstnId.BICFI != "VERDITMMXXX" {
		t.Errorf("creditor agent = %q, want VERDITMMXXX", tx.Cdtr.FinInstnId.BICFI)
	}
	if tx.IntrBkSttlmAmt.Value != "2500.00" {
		t.Errorf("amount = %q, want 2500.00", tx.IntrBkSttlmAmt.Value)
	}
}

func TestPacs009RequiresATransaction(t *testing.T) {
	env := sample009()
	env.Document.(*Pacs009).FICdtTrf.CdtTrfTxInf = nil
	if _, err := Marshal(env); !errors.Is(err, ErrMissingElement) {
		t.Fatalf("Marshal with no transaction = %v, want ErrMissingElement", err)
	}
}

func TestPacs009RequiresBothAgents(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*FinancialInstitutionCreditTransferTransaction)
	}{
		{"no debtor agent", func(tx *FinancialInstitutionCreditTransferTransaction) { tx.Dbtr.FinInstnId.BICFI = "" }},
		{"no creditor agent", func(tx *FinancialInstitutionCreditTransferTransaction) { tx.Cdtr.FinInstnId.BICFI = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := sample009()
			tc.mut(&env.Document.(*Pacs009).FICdtTrf.CdtTrfTxInf[0])
			if _, err := Marshal(env); err == nil {
				t.Fatal("Marshal accepted a transaction with a missing agent")
			}
		})
	}
}
