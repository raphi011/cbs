package iso20022

import (
	"errors"
	"strings"
	"testing"
)

// The two samples are ONE lodgement, told in two messages: Aurora Bank asking
// its central bank to move EUR 500,000.00 of vault cash onto its reserve
// account, and the central bank saying it did.
const lodgementMsgID = "AURODEFFXXX-LODGE-1"

// sampleCamt050 is the request: the bank to its central bank, naming the reserve
// account to credit and the amount, and naming its own vault account nowhere.
func sampleCamt050() Envelope {
	return Envelope{
		AppHdr: AppHdr{
			Fr:        NewAgent("AURODEFFXXX"),
			To:        NewAgent("CBSEDEFFXXX"),
			BizMsgIdr: lodgementMsgID,
			MsgDefIdr: "camt.050.001.05",
			CreDt:     ISODateTime{testTime},
		},
		Document: &Camt050{LqdtyCdtTrf: LiquidityCreditTransferV5{
			MsgHdr: MessageHeader{MsgId: lodgementMsgID, CreDtTm: ISODateTime{testTime}},
			LqdtyCdtTrf: LiquidityTransfer{
				LqdtyTrfId: LiquidityTransferIdentification{EndToEndId: lodgementMsgID},
				Cdtr:       BranchAndFinancialInstitution{FinInstnId: FinancialInstitutionIdentification{BICFI: "CBSEDEFFXXX"}},
				CdtrAcct: CashAccount{Id: AccountIdentification4Choice{
					Othr: &GenericAccountIdentification{Id: "acc_cb_reserve_bank_1_eur"},
				}},
				TrfdAmt: TransferredAmount{AmtWthCcy: ActiveCurrencyAndAmount{Ccy: "EUR", Value: "500000.00"}},
				Dbtr:    BranchAndFinancialInstitution{FinInstnId: FinancialInstitutionIdentification{BICFI: "AURODEFFXXX"}},
			},
		}},
	}
}

// sampleCamt025 is the answer: the central bank acknowledging the request by its
// identifier, and saying what it did with it.
func sampleCamt025() Envelope {
	return Envelope{
		AppHdr: AppHdr{
			Fr:        NewAgent("CBSEDEFFXXX"),
			To:        NewAgent("AURODEFFXXX"),
			BizMsgIdr: "CBSEDEFFXXX-RCPT-1",
			MsgDefIdr: "camt.025.001.05",
			CreDt:     ISODateTime{testTime},
		},
		Document: &Camt025{Rct: ReceiptV5{
			MsgHdr: ReceiptMessageHeader{MsgId: "CBSEDEFFXXX-RCPT-1", CreDtTm: ISODateTime{testTime}},
			RctDtls: []ReceiptDetails{{
				OrgnlMsgId: OriginalMessageAndIssuer{MsgId: lodgementMsgID, MsgNmId: "camt.050.001.05"},
				ReqHdlg:    []RequestHandling{{StsCd: string(TransactionStatusSettlementCompleted)}},
			}},
		}},
	}
}

// TestCamt050GoldenRoundTrip pins testdata/camt050.xml.
func TestCamt050GoldenRoundTrip(t *testing.T) {
	env := assertGoldenRoundTrip(t, "camt050.xml")

	doc, ok := env.Document.(*Camt050)
	if !ok {
		t.Fatalf("Document is %T, want *Camt050", env.Document)
	}
	trf := doc.LqdtyCdtTrf.LqdtyCdtTrf
	if got := trf.Cdtr.FinInstnId.BICFI; got != "CBSEDEFFXXX" {
		t.Errorf("Cdtr/FinInstnId/BICFI = %q, want the central bank CBSEDEFFXXX", got)
	}
	if got := trf.Dbtr.FinInstnId.BICFI; got != "AURODEFFXXX" {
		t.Errorf("Dbtr/FinInstnId/BICFI = %q, want the lodging bank AURODEFFXXX", got)
	}
	if got := trf.TrfdAmt.AmtWthCcy.Ccy; got != "EUR" {
		t.Errorf("TrfdAmt/AmtWthCcy/@Ccy = %q, want EUR", got)
	}
	if got := trf.TrfdAmt.AmtWthCcy.Value; got != "500000.00" {
		t.Errorf("TrfdAmt/AmtWthCcy = %q, want 500000.00", got)
	}
}

// TestTheLodgementNamesTheAccountItCreditsAndNotTheOneItDebits is the asymmetry
// in LiquidityTransfer, asserted rather than left to the doc comment.
func TestTheLodgementNamesTheAccountItCreditsAndNotTheOneItDebits(t *testing.T) {
	raw, err := Marshal(sampleCamt050())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), "<CdtrAcct>") {
		t.Errorf("a lodgement must name the reserve account it credits:\n%s", raw)
	}
	if strings.Contains(string(raw), "<DbtrAcct>") {
		t.Errorf("a lodgement must NOT name the vault account it debits — that account is in the "+
			"lodging bank's own book, which the central bank may not read:\n%s", raw)
	}
}

// TestCamt050RoundTrips is what catches an element whose XML tag does not match
// the field it was written to.
func TestCamt050RoundTrips(t *testing.T) {
	raw, err := Marshal(sampleCamt050())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), camt050Namespace) {
		t.Fatalf("marshalled document does not carry its namespace:\n%s", raw)
	}
	env, err := Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got, ok := env.Document.(*Camt050)
	if !ok {
		t.Fatalf("Unmarshal gave %T, want *Camt050", env.Document)
	}
	if got.LqdtyCdtTrf.MsgHdr.MsgId != lodgementMsgID {
		t.Errorf("MsgHdr/MsgId round-tripped as %q", got.LqdtyCdtTrf.MsgHdr.MsgId)
	}
	if got.LqdtyCdtTrf.LqdtyCdtTrf.LqdtyTrfId.EndToEndId != lodgementMsgID {
		t.Errorf("LqdtyTrfId/EndToEndId round-tripped as %q", got.LqdtyCdtTrf.LqdtyCdtTrf.LqdtyTrfId.EndToEndId)
	}
	if got.LqdtyCdtTrf.LqdtyCdtTrf.CdtrAcct.Id.Othr == nil {
		t.Fatal("CdtrAcct/Id/Othr round-tripped as nil; the reserve account is what the message is for")
	}
	if id := got.LqdtyCdtTrf.LqdtyCdtTrf.CdtrAcct.Id.Othr.Id; id != "acc_cb_reserve_bank_1_eur" {
		t.Errorf("CdtrAcct/Id/Othr/Id round-tripped as %q", id)
	}
}

// TestCamt050Validate covers what this package requires of a lodgement, which
// is four elements more than the schema does.
func TestCamt050Validate(t *testing.T) {
	valid := func() *Camt050 { return sampleCamt050().Document.(*Camt050) }

	for _, tc := range []struct {
		name string
		mut  func(*LiquidityCreditTransferV5)
	}{
		{"no message id", func(m *LiquidityCreditTransferV5) { m.MsgHdr.MsgId = "" }},
		{"no message creation instant", func(m *LiquidityCreditTransferV5) { m.MsgHdr.CreDtTm = ISODateTime{} }},
		{"no end-to-end reference", func(m *LiquidityCreditTransferV5) { m.LqdtyCdtTrf.LqdtyTrfId.EndToEndId = "" }},
		{"no creditor", func(m *LiquidityCreditTransferV5) { m.LqdtyCdtTrf.Cdtr.FinInstnId.BICFI = "" }},
		{"no creditor account", func(m *LiquidityCreditTransferV5) {
			m.LqdtyCdtTrf.CdtrAcct.Id.Othr = nil
		}},
		{"no debtor", func(m *LiquidityCreditTransferV5) { m.LqdtyCdtTrf.Dbtr.FinInstnId.BICFI = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := valid()
			tc.mut(&d.LqdtyCdtTrf)
			if err := d.validate(); err == nil {
				t.Fatal("validate() = nil, want a refusal")
			}
		})
	}
}

// TestTheLodgementAmountCarriesItsCurrency is the narrowing TransferredAmount
// makes, and it is about money rather than about tidiness.
func TestTheLodgementAmountCarriesItsCurrency(t *testing.T) {
	raw, err := Marshal(sampleCamt050())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `<AmtWthCcy Ccy="EUR">`) {
		t.Errorf("the amount must state its currency:\n%s", raw)
	}
	if strings.Contains(string(raw), "AmtWthtCcy") {
		t.Errorf("this package does not model the implied-currency arm:\n%s", raw)
	}

	env := sampleCamt050()
	env.Document.(*Camt050).LqdtyCdtTrf.LqdtyCdtTrf.TrfdAmt.AmtWthCcy.Ccy = ""
	if _, err := Marshal(env); err == nil {
		t.Fatal("Marshal of an amount with no currency = nil, want a refusal")
	}
}

// TestCamt025GoldenRoundTrip pins testdata/camt025.xml.
func TestCamt025GoldenRoundTrip(t *testing.T) {
	env := assertGoldenRoundTrip(t, "camt025.xml")

	doc, ok := env.Document.(*Camt025)
	if !ok {
		t.Fatalf("Document is %T, want *Camt025", env.Document)
	}
	if n := len(doc.Rct.RctDtls); n != 1 {
		t.Fatalf("RctDtls carries %d receipts, want 1: this system answers one request per receipt", n)
	}
	d := doc.Rct.RctDtls[0]
	if got := d.ReqHdlg[0].StsCd; got != string(TransactionStatusSettlementCompleted) {
		t.Errorf("ReqHdlg/StsCd = %q, want %q", got, TransactionStatusSettlementCompleted)
	}
	if d.ReqHdlg[0].Desc != "" {
		t.Errorf("ReqHdlg/Desc = %q, want it absent on the accepting arm", d.ReqHdlg[0].Desc)
	}
}

// TestTheReceiptNamesTheRequestItAnswers is the correlation the conversation
// rests on, and the one thing neither sample can assert alone.
func TestTheReceiptNamesTheRequestItAnswers(t *testing.T) {
	req := sampleCamt050()
	rcpt := sampleCamt025()

	sent := req.Document.(*Camt050).LqdtyCdtTrf.MsgHdr.MsgId
	quoted := rcpt.Document.(*Camt025).Rct.RctDtls[0].OrgnlMsgId

	if quoted.MsgId != sent {
		t.Errorf("the receipt quotes %q and the request was %q; a bank holding two lodgements "+
			"cannot tell which one this answers", quoted.MsgId, sent)
	}
	if quoted.MsgNmId != req.AppHdr.MsgDefIdr {
		t.Errorf("OrgnlMsgId/MsgNmId = %q, want the request's message definition %q",
			quoted.MsgNmId, req.AppHdr.MsgDefIdr)
	}
	// The receipt must NOT reuse the request's identifier as its own: they are two
	// messages, and a reader matching on the wrong one would match a message
	// against itself.
	if got := rcpt.Document.(*Camt025).Rct.MsgHdr.MsgId; got == sent {
		t.Errorf("the receipt's own MsgId is %q, the same as the request's; two messages need two ids", got)
	}
}

// TestCamt025RoundTrips is the round-trip for the answering half.
func TestCamt025RoundTrips(t *testing.T) {
	raw, err := Marshal(sampleCamt025())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), camt025Namespace) {
		t.Fatalf("marshalled document does not carry its namespace:\n%s", raw)
	}
	env, err := Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got, ok := env.Document.(*Camt025)
	if !ok {
		t.Fatalf("Unmarshal gave %T, want *Camt025", env.Document)
	}
	if got.Rct.RctDtls[0].OrgnlMsgId.MsgId != lodgementMsgID {
		t.Errorf("OrgnlMsgId/MsgId round-tripped as %q", got.Rct.RctDtls[0].OrgnlMsgId.MsgId)
	}
	if got.Rct.RctDtls[0].ReqHdlg[0].StsCd != string(TransactionStatusSettlementCompleted) {
		t.Errorf("ReqHdlg/StsCd round-tripped as %q", got.Rct.RctDtls[0].ReqHdlg[0].StsCd)
	}
}

// TestARefusingReceiptCarriesItsReasonAsProse is the loss this family imposes,
// pinned so that a reader does not go looking for a code.
func TestARefusingReceiptCarriesItsReasonAsProse(t *testing.T) {
	env := sampleCamt025()
	env.Document.(*Camt025).Rct.RctDtls[0].ReqHdlg = []RequestHandling{{
		StsCd: string(TransactionStatusRejected),
		Desc:  "no settlement account is held for AURODEFFXXX in USD",
	}}
	raw, err := Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), "<StsCd>RJCT</StsCd>") {
		t.Errorf("a refusing receipt must carry RJCT:\n%s", raw)
	}
	if !strings.Contains(string(raw), "<Desc>no settlement account is held for AURODEFFXXX in USD</Desc>") {
		t.Errorf("a refusing receipt must carry its reason:\n%s", raw)
	}
}

// TestCamt025Validate covers what this package requires of a receipt.
func TestCamt025Validate(t *testing.T) {
	valid := func() *Camt025 { return sampleCamt025().Document.(*Camt025) }

	for _, tc := range []struct {
		name string
		mut  func(*ReceiptV5)
	}{
		{"no message id", func(r *ReceiptV5) { r.MsgHdr.MsgId = "" }},
		{"no message creation instant", func(r *ReceiptV5) { r.MsgHdr.CreDtTm = ISODateTime{} }},
		{"no receipt details", func(r *ReceiptV5) { r.RctDtls = nil }},
		{"no original message id", func(r *ReceiptV5) { r.RctDtls[0].OrgnlMsgId.MsgId = "" }},
		{"no request handling", func(r *ReceiptV5) { r.RctDtls[0].ReqHdlg = nil }},
		{"no status code", func(r *ReceiptV5) { r.RctDtls[0].ReqHdlg[0].StsCd = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := valid()
			tc.mut(&d.Rct)
			if err := d.validate(); !errors.Is(err, ErrMissingElement) {
				t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
			}
		})
	}
}

// TestTheReceiptCarriesNoAmount is the absence that decides the whole
// conversation's shape, asserted so that it is a fact rather than a claim in a
// doc comment.
func TestTheReceiptCarriesNoAmount(t *testing.T) {
	raw, err := Marshal(sampleCamt025())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, forbidden := range []string{"Amt", "Ccy"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("a camt.025 carries no amount, and this one mentions %q:\n%s\n"+
				"If an amount has been added here, the argument in Camt025 for posting "+
				"before the send no longer holds and has to be rewritten.", forbidden, raw)
		}
	}
}
