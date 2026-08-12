// Package payment_test's half of the translator's tests: the ones that need a whole
// network.
//
// They are separated from translate_test.go, which tests the same file, for a
// mechanical reason. These build a Network, which means a store, which means
// store/testenv and store/storetest, and both of those reach payment — so an
// in-package test file importing either back is an import cycle. translate_test.go's
// reason-table tests need reasonTable, which is unexported and stays that way.
// Neither half can move to the other's package, and Go allows both packages in one
// directory.
package payment_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	. "github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/storetest"
)

// readOne is the single instruction a file of one carries. A read that succeeded
// always carries at least one transaction — iso20022 refuses a pacs.008 or pacs.003
// with none — so the empty case is only reached behind an error the caller checks.
func readOne(txs []InboundTransaction, err error) (InitiatePaymentRequest, error) {
	if err != nil || len(txs) == 0 {
		return InitiatePaymentRequest{}, err
	}
	return txs[0].Request, nil
}

// readOneTx is readOne for the tests that are about the REFUSAL rather than the
// instruction: an address the receiving bank cannot resolve is that
// transaction's own answer and not the file's error, so it is on the
// InboundTransaction and readOne throws it away.
func readOneTx(txs []InboundTransaction, err error) (InboundTransaction, error) {
	if err != nil || len(txs) == 0 {
		return InboundTransaction{}, err
	}
	return txs[0], nil
}

// messageNow is the instant the message tests build their headers at, deliberately
// NOT the network's clock: a message is created when it is sent, not when the payment
// it carries was booked, and one instant for both could not tell a translator that
// confused the two apart from one that did not.
var messageNow = time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

// addressedBanks builds the two banks these tests address each other by: real,
// distinct BICs, and one customer account at each carrying a real IBAN. It does not
// reuse setupTwoBanks, whose banks sit at placeholder BICs — a test asserting which
// bank reached the wire wants two a reader can tell apart at sight, and their
// countries agreeing with the addresses their registers mint is what makes the
// documents below readable as real ones.
func addressedBanks(t *testing.T) (sys *testSystem, aurora, verde *Bank, alice, bruno deposit.Account) {
	t.Helper()
	ctx := context.Background()
	sys = testNetwork(t)

	aurora, err := storetest.Admit(ctx, sys.nets, "Aurora Bank", "AURODEFFXXX", euroOnly)
	assertNoError(t, err)
	verde, err = storetest.Admit(ctx, sys.nets, "Banca Verde", "VERDITMMXXX", euroOnly)
	assertNoError(t, err)

	alice = openCustomer(t, ctx, aurora, "Aurora Customer")
	bruno = openCustomer(t, ctx, verde, "Verde Customer")
	fundAccount(t, ctx, sys, aurora, alice, 500000)
	return sys, aurora, verde, alice, bruno
}

// networkWithOnePayment is the credit-transfer fixture: the two addressed banks
// and one accepted SEPA credit transfer from Aurora's customer to Verde's.
func networkWithOnePayment(t *testing.T) (*testSystem, Payment) {
	t.Helper()
	ctx := context.Background()
	sys, aurora, verde, alice, bruno := addressedBanks(t)

	openCycle(t, ctx, sys, SchemeSEPACT)
	p, err := initiate(ctx, sys, InitiatePaymentRequest{
		Scheme:      SchemeSEPACT,
		Debtor:      PartyRef{Account: alice.ID, Identifier: alice.Identifiers[0]},
		Creditor:    PartyRef{Account: bruno.ID, Identifier: bruno.Identifiers[0]},
		Amount:      250000,
		EndToEndID:  "e2e-1",
		Description: "invoice 42",
		// Push: the creditor is the counterparty, so the request must name it — the
		// NAME and the BIC. Nothing derives the second: the row it would be derived
		// from is the counterparty's own. See payment.SubmitPaymentTx.
		CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
		DebtorDetails:   PartyDetails{Agent: aurora.BIC}})
	assertNoError(t, err)
	return sys, p
}

// networkWithOneCollection is the direct-debit fixture: the same two banks, a
// mandate Verde's customer holds over Aurora's, and one accepted collection
// under it.
func networkWithOneCollection(t *testing.T) (*testSystem, Payment, Mandate) {
	t.Helper()
	ctx := context.Background()
	sys, aurora, verde, alice, bruno := addressedBanks(t)

	debtor := PartyRef{Account: alice.ID, Identifier: alice.Identifiers[0]}
	creditor := PartyRef{Account: bruno.ID, Identifier: bruno.Identifiers[0]}
	m, err := sys.bank(verde.BIC).CreateMandate(ctx, aurora.BIC, debtor, creditor, 0)
	assertNoError(t, err)

	openCycle(t, ctx, sys, SchemeSEPADD)
	p, err := initiate(ctx, sys, InitiatePaymentRequest{
		Scheme:      SchemeSEPADD,
		Debtor:      debtor,
		Creditor:    creditor,
		Amount:      120000,
		MandateID:   m.ID,
		EndToEndID:  "e2e-dd-1",
		Description: "electricity, August",
		// Pull: the debtor is the counterparty, so the request must name it. The
		// COLLECTOR's agent is named too, which is the fixture's own need rather than
		// the domain's: it selects which bank's database this collection is submitted at.
		DebtorDetails:   PartyDetails{Agent: aurora.BIC, Name: alice.Name},
		CreditorDetails: PartyDetails{Agent: verde.BIC}})
	assertNoError(t, err)
	return sys, p, m
}

func TestCreditTransferMessageCarriesBothAgentsAndBothIBANs(t *testing.T) {
	n, p := networkWithOnePayment(t)

	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{
		From:  "AURODEFFXXX",
		To:    "CSMXFRPPXXX",
		MsgID: "AURO-1",
		Now:   messageNow,
	})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	// Marshal is the real assertion. It validates the whole tree, so a message
	// that survives it is EPC-valid — which is exactly why this test does not
	// re-check field by field what iso20022.validate already checks.
	raw, err := iso20022.Marshal(env)
	if err != nil {
		t.Fatalf("the produced message is not valid: %v", err)
	}

	doc := env.Document.(*iso20022.Pacs008)
	tx := doc.FIToFICstmrCdtTrf.CdtTrfTxInf[0]
	if tx.DbtrAgt.FinInstnId.BICFI != "AURODEFFXXX" {
		t.Errorf("DbtrAgt = %q, want the debtor bank's BIC", tx.DbtrAgt.FinInstnId.BICFI)
	}
	if tx.CdtrAgt.FinInstnId.BICFI != "VERDITMMXXX" {
		t.Errorf("CdtrAgt = %q, want the creditor bank's BIC", tx.CdtrAgt.FinInstnId.BICFI)
	}
	// The agents are the BANKS, and the header's To is the CSM. A message that
	// addressed the creditor bank in the header would be a bank talking
	// directly to a bank, which is the topology this system does not have.
	if env.AppHdr.To.FIId.FinInstnId.BICFI != "CSMXFRPPXXX" {
		t.Errorf("header To = %q, want the CSM", env.AppHdr.To.FIId.FinInstnId.BICFI)
	}
	if !strings.Contains(string(raw), string(p.Debtor.Identifier.Value)) {
		t.Errorf("the debtor's quoted address is not in the message:\n%s", raw)
	}
	// The names come from the deposit accounts, not from the banks: EPC AT-P001
	// and AT-E001 name the CUSTOMERS, and a translator that reached for the
	// participant name instead would still produce a valid document.
	if tx.Dbtr.Nm != "Aurora Customer" || tx.Cdtr.Nm != "Verde Customer" {
		t.Errorf("Dbtr/Cdtr = %q/%q, want the two account holders", tx.Dbtr.Nm, tx.Cdtr.Nm)
	}
}

func TestCreditTransferMessageRefusesAPaymentWithNoAddress(t *testing.T) {
	n, p := networkWithOnePayment(t)
	p.Creditor.Identifier = deposit.Identifier{}
	if _, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: time.Now()}); err == nil {
		t.Fatal("built a pacs.008 for a payment with no creditor address")
	}
}

// An address that is present but not a legal IBAN is refused HERE, by the
// translator, and not later by Marshal. ErrUnaddressableAccount names an account this
// system could not address, a fact about the payment, where an iso20022 error names
// an element in a document. FuzzTranslate found this one.
func TestCreditTransferMessageRefusesAMalformedIBAN(t *testing.T) {
	n, p := networkWithOnePayment(t)
	p.Creditor.Identifier = deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "not an iban"}
	_, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: time.Now()})
	if !errors.Is(err, ErrUnaddressableAccount) {
		t.Fatalf("CreditTransferMessage on a malformed IBAN = %v, want ErrUnaddressableAccount", err)
	}
}

// EndToEndId is 1..1 in the schema and this system's own end-to-end id is
// optional, so the gap has to be filled with something. NOTPROVIDED is what the
// EPC guidelines say to fill it with, and it is a value a receiver recognises
// as "the sender had none" rather than one it mistakes for a reference.
func TestCreditTransferMessageWithNoEndToEndIDSaysNOTPROVIDED(t *testing.T) {
	n, p := networkWithOnePayment(t)
	p.EndToEndID = ""
	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	if _, err := iso20022.Marshal(env); err != nil {
		t.Fatalf("a payment with no end-to-end id produced an invalid message: %v", err)
	}
	got := env.Document.(*iso20022.Pacs008).FIToFICstmrCdtTrf.CdtTrfTxInf[0].PmtId.EndToEndId
	if got != "NOTPROVIDED" {
		t.Errorf("EndToEndId = %q, want NOTPROVIDED", got)
	}
}

// The interbank settlement date is the payment's VALUE date — the date the
// money takes economic effect — and not the date the message was written.
// SEPA CT is T+1, so under the test clock the two are more than a year apart
// and a translator that used Now would fail here rather than in production.
func TestCreditTransferMessageSettlesOnTheValueDate(t *testing.T) {
	n, p := networkWithOnePayment(t)
	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	got := env.Document.(*iso20022.Pacs008).FIToFICstmrCdtTrf.GrpHdr.IntrBkSttlmDt
	if !got.Equal(p.ValueDate) {
		t.Errorf("IntrBkSttlmDt = %s, want the payment's value date %s", got.Time, p.ValueDate)
	}
}

// A payment carrying no value date at all gets the message's own creation date
// and not the zero time. The zero time marshals as 0001-01-01, a schema-valid
// date asserting settlement in the first century — the same fabrication
// AppHdr.CreDt's validation exists to stop, arriving by a different door.
func TestCreditTransferMessageNeverAssertsTheZeroSettlementDate(t *testing.T) {
	n, p := networkWithOnePayment(t)
	p.ValueDate = time.Time{}
	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	got := env.Document.(*iso20022.Pacs008).FIToFICstmrCdtTrf.GrpHdr.IntrBkSttlmDt
	if !got.Equal(messageNow) {
		t.Errorf("IntrBkSttlmDt = %s, want the message's creation date %s", got.Time, messageNow)
	}
}

// DtOfSgntr is the mandate's CreatedAt, which is the elision translate.go documents:
// this system creates a mandate at the moment it is authorised, so the two are one
// fact. The network's clock is a year and a half behind the message's, so a
// translator that reached for mc.Now — the obvious wrong answer, and a date that
// would always look plausible — fails here.
func TestDirectDebitMessageDatesTheSignatureFromTheMandate(t *testing.T) {
	n, p, m := networkWithOneCollection(t)

	env, err := n.DirectDebitMessage([]Collection{{Payment: p, Mandate: m}}, MessageContext{
		From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "VERDE-1", Now: messageNow,
	})
	if err != nil {
		t.Fatalf("DirectDebitMessage: %v", err)
	}
	if _, err := iso20022.Marshal(env); err != nil {
		t.Fatalf("the produced collection is not valid: %v", err)
	}

	tx := env.Document.(*iso20022.Pacs003).FIToFICstmrDrctDbt.DrctDbtTxInf[0]
	if got := tx.DrctDbtTx.MndtRltdInf.MndtId; got != string(m.ID) {
		t.Errorf("MndtId = %q, want the mandate's id %q", got, m.ID)
	}
	if got := tx.DrctDbtTx.MndtRltdInf.DtOfSgntr; !got.Equal(m.CreatedAt) {
		t.Errorf("DtOfSgntr = %s, want the mandate's creation date %s", got.Time, m.CreatedAt)
	}
	if got := tx.DrctDbtTx.MndtRltdInf.DtOfSgntr; got.Equal(messageNow) {
		t.Error("DtOfSgntr is the message's creation date; it must be the mandate's")
	}
	// The collection travels from the creditor's bank, so the creditor comes
	// first — see DirectDebitTransactionInformation's field order.
	if tx.CdtrAgt.FinInstnId.BICFI != "VERDITMMXXX" || tx.DbtrAgt.FinInstnId.BICFI != "AURODEFFXXX" {
		t.Errorf("agents = %q/%q, want Verde collecting from Aurora",
			tx.CdtrAgt.FinInstnId.BICFI, tx.DbtrAgt.FinInstnId.BICFI)
	}
	if tx.PmtTpInf.SeqTp == nil || *tx.PmtTpInf.SeqTp != iso20022.SequenceTypeRecurring {
		t.Errorf("SeqTp = %v, want RCUR", tx.PmtTpInf.SeqTp)
	}
	if tx.PmtTpInf.LclInstrm == nil || tx.PmtTpInf.LclInstrm.Cd == nil || *tx.PmtTpInf.LclInstrm.Cd != iso20022.LocalInstrumentCore {
		t.Errorf("LclInstrm = %v, want the code CORE", tx.PmtTpInf.LclInstrm)
	}
}

// The Creditor Identifier this system has no register for is the creditor's own
// IBAN, said out loud rather than invented. See creditorSchemeIdentification.
func TestDirectDebitMessageStandsTheCreditorsIBANInForTheCreditorIdentifier(t *testing.T) {
	n, p, m := networkWithOneCollection(t)
	env, err := n.DirectDebitMessage([]Collection{{Payment: p, Mandate: m}}, MessageContext{From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("DirectDebitMessage: %v", err)
	}
	// This is the test that is ABOUT the substitution, and CdtrSchmeId is the deepest
	// subtree it invents a value for. The test making the claim should be the one
	// asserting the document is valid, rather than leaning on another that happens to
	// marshal a pacs.003.
	if _, err := iso20022.Marshal(env); err != nil {
		t.Fatalf("the collection carrying the substituted creditor identifier is not valid: %v", err)
	}
	tx := env.Document.(*iso20022.Pacs003).FIToFICstmrDrctDbt.DrctDbtTxInf[0]
	id := tx.DrctDbtTx.CdtrSchmeId.Id.PrvtId.Othr
	if id.Id != string(p.Creditor.Identifier.Value) {
		t.Errorf("CdtrSchmeId = %q, want the creditor's IBAN %q", id.Id, p.Creditor.Identifier.Value)
	}
	if id.SchmeNm.Prtry != "SEPA" {
		t.Errorf("CdtrSchmeId SchmeNm/Prtry = %q, want SEPA", id.SchmeNm.Prtry)
	}
}

func TestStatusMessageCarriesTheCodeAndTheText(t *testing.T) {
	env, err := StatusMessage(
		OriginalMessage{MsgID: "AURO-1", MsgDefIdr: "pacs.008.001.08", CreDtTm: messageNow},
		[]TransactionStatusReport{{
			EndToEndID: "e2e-1",
			TxID:       "pay_1",
			Status:     iso20022.TransactionStatusRejected,
			Code:       iso20022.StatusReasonClosedAccountNumber,
			Text:       "creditor account is closed",
		}},
		MessageContext{From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "VERDE-1", Now: time.Now()},
	)
	if err != nil {
		t.Fatalf("StatusMessage: %v", err)
	}
	if _, err := iso20022.Marshal(env); err != nil {
		t.Fatalf("the produced status report is not valid: %v", err)
	}

	sts := env.Document.(*iso20022.Pacs002).FIToFIPmtStsRpt.TxInfAndSts[0]
	if sts.StsRsnInf == nil || sts.StsRsnInf.Rsn.Cd == nil {
		t.Fatalf("a rejection reached the wire with no reason code: %+v", sts)
	}
	if *sts.StsRsnInf.Rsn.Cd != iso20022.StatusReasonClosedAccountNumber {
		t.Errorf("Rsn/Cd = %q, want AC04", *sts.StsRsnInf.Rsn.Cd)
	}
	// The code is for machines and the text is for the person reading the
	// exception queue. Losing either one is a silent loss.
	if sts.StsRsnInf.AddtlInf != "creditor account is closed" {
		t.Errorf("AddtlInf = %q, want the free text beside the code", sts.StsRsnInf.AddtlInf)
	}
	// Orgtr is WHO decided, and it is the sender rather than the CSM the
	// message is addressed to — see StatusReasonInformation.
	if sts.StsRsnInf.Orgtr == nil || sts.StsRsnInf.Orgtr.Id == nil || sts.StsRsnInf.Orgtr.Id.OrgId.AnyBIC != "VERDITMMXXX" {
		t.Errorf("Orgtr = %+v, want the deciding bank's BIC", sts.StsRsnInf.Orgtr)
	}
}

// GrpSts is derived from the transactions because a message is a BULK: a file
// with one acceptance and one rejection is neither accepted nor rejected, and
// PART is the only truthful answer.
func TestStatusMessageGroupStatusDescribesTheWholeBulk(t *testing.T) {
	accepted := TransactionStatusReport{EndToEndID: "a", Status: iso20022.TransactionStatusAccepted}
	rejected := TransactionStatusReport{
		EndToEndID: "r",
		Status:     iso20022.TransactionStatusRejected,
		Code:       iso20022.StatusReasonInsufficientFunds,
	}
	for _, tc := range []struct {
		name string
		sts  []TransactionStatusReport
		want iso20022.GroupStatus
	}{
		{"all accepted", []TransactionStatusReport{accepted, accepted}, iso20022.GroupStatusAccepted},
		{"all rejected", []TransactionStatusReport{rejected, rejected}, iso20022.GroupStatusRejected},
		{"mixed", []TransactionStatusReport{accepted, rejected}, iso20022.GroupStatusPartiallyAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := StatusMessage(
				OriginalMessage{MsgID: "AURO-1", MsgDefIdr: "pacs.008.001.08", CreDtTm: messageNow},
				tc.sts,
				MessageContext{From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "VERDE-1", Now: messageNow},
			)
			if err != nil {
				t.Fatalf("StatusMessage: %v", err)
			}
			if _, err := iso20022.Marshal(env); err != nil {
				t.Fatalf("the produced status report is not valid: %v", err)
			}
			if got := env.Document.(*iso20022.Pacs002).FIToFIPmtStsRpt.OrgnlGrpInfAndSts.GrpSts; got != tc.want {
				t.Errorf("GrpSts = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReturnMessageCarriesTheReturnReason(t *testing.T) {
	n, p := networkWithOnePayment(t)
	env, err := n.ReturnMessage(p, iso20022.ReturnReasonClosedAccountNumber, "creditor account closed before the credit was applied",
		MessageContext{From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "VERDE-R1", Now: messageNow})
	if err != nil {
		t.Fatalf("ReturnMessage: %v", err)
	}
	if _, err := iso20022.Marshal(env); err != nil {
		t.Fatalf("the produced return is not valid: %v", err)
	}
	tx := env.Document.(*iso20022.Pacs004).PmtRtr.TxInf[0]
	if tx.RtrRsnInf == nil || tx.RtrRsnInf.Rsn.Cd == nil || *tx.RtrRsnInf.Rsn.Cd != iso20022.ReturnReasonClosedAccountNumber {
		t.Fatalf("Rsn = %+v, want AC04 from the RETURN code set", tx.RtrRsnInf)
	}
	// This system's returns are whole, so the two amounts are equal — but they
	// are two elements because the standard is shaped for partial returns. See
	// ReturnTransaction.
	if tx.OrgnlIntrBkSttlmAmt != tx.RtrdIntrBkSttlmAmt {
		t.Errorf("returned %v of an original %v; this system returns the whole payment",
			tx.RtrdIntrBkSttlmAmt, tx.OrgnlIntrBkSttlmAmt)
	}
	if tx.OrgnlEndToEndId != p.EndToEndID {
		t.Errorf("OrgnlEndToEndId = %q, want the returned payment's %q", tx.OrgnlEndToEndId, p.EndToEndID)
	}
}

// TestAReturnCarriesTheTwoAgentsItsSettlementNeeds is why OrgnlTxRef is not
// omitted. A settlement agent with no payment rows learns whose reserves to move
// from this element or from nothing.
func TestAReturnCarriesTheTwoAgentsItsSettlementNeeds(t *testing.T) {
	n, p := networkWithOnePayment(t)
	env, err := n.ReturnMessage(p, iso20022.ReturnReasonClosedAccountNumber, "account closed",
		MessageContext{From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "VERDE-R2", Now: messageNow})
	if err != nil {
		t.Fatalf("ReturnMessage: %v", err)
	}
	ins, err := ReadReturn(env.Document.(*iso20022.Pacs004))
	if err != nil {
		t.Fatalf("ReadReturn: %v", err)
	}
	if len(ins) != 1 {
		t.Fatalf("got %d instructions, want 1", len(ins))
	}
	got := ins[0]
	if got.DebtorAgent != p.DebtorDetails.Agent || got.CreditorAgent != p.CreditorDetails.Agent {
		t.Errorf("agents came back %s/%s, want %s/%s",
			got.DebtorAgent, got.CreditorAgent, p.DebtorDetails.Agent, p.CreditorDetails.Agent)
	}
	if got.PaymentID != p.ID || got.Amount != p.Amount {
		t.Errorf("got %s for %d, want %s for %d", got.PaymentID, got.Amount, p.ID, p.Amount)
	}
}

func TestSettlementMessageIsOneLegPerBank(t *testing.T) {
	env, err := SettlementMessage([]SettlementLeg{
		{From: "AURODEFFXXX", To: "VERDITMMXXX", Amount: 250000, Asset: "EUR", Reference: "cyc_1:bank_1"},
		{From: "NORDSESSXXX", To: "VERDITMMXXX", Amount: 100000, Asset: "EUR", Reference: "cyc_1:bank_3"},
	}, MessageContext{From: "CSMXFRPPXXX", To: "CBSEDEFFXXX", MsgID: "CSM-1", Now: time.Now()})
	if err != nil {
		t.Fatalf("SettlementMessage: %v", err)
	}
	if _, err := iso20022.Marshal(env); err != nil {
		t.Fatalf("the produced settlement instruction is not valid: %v", err)
	}
	doc := env.Document.(*iso20022.Pacs009)
	if got := len(doc.FICdtTrf.CdtTrfTxInf); got != 2 {
		t.Fatalf("got %d transactions, want one per leg", got)
	}
	if got := doc.FICdtTrf.GrpHdr.NbOfTxs; got != "2" {
		t.Errorf("NbOfTxs = %q, want 2 — the sender's own assertion of what it sent", got)
	}
	// No total. The legs of one message may be denominated in different assets,
	// and a sum across assets is not a number — see settlementInstruction.
	if doc.FICdtTrf.GrpHdr.TtlIntrBkSttlmAmt != nil {
		t.Errorf("TtlIntrBkSttlmAmt = %+v, want it absent", doc.FICdtTrf.GrpHdr.TtlIntrBkSttlmAmt)
	}
}

// NbOfTxs is what the SENDER asserts, not a derivation a receiver would recompute.
// Truncate the transaction list after the message is built, exactly as a lost packet
// or a half-written file would, and the count still says two. A receiver that
// recomputed it would read this as a complete one-transaction settlement and never
// notice the missing leg.
func TestSettlementMessageNbOfTxsSurvivesATruncatedFile(t *testing.T) {
	env, err := SettlementMessage([]SettlementLeg{
		{From: "AURODEFFXXX", To: "VERDITMMXXX", Amount: 250000, Asset: "EUR", Reference: "cyc_1:bank_1"},
		{From: "NORDSESSXXX", To: "VERDITMMXXX", Amount: 100000, Asset: "EUR", Reference: "cyc_1:bank_3"},
	}, MessageContext{From: "CSMXFRPPXXX", To: "CBSEDEFFXXX", MsgID: "CSM-1", Now: messageNow})
	if err != nil {
		t.Fatalf("SettlementMessage: %v", err)
	}
	doc := env.Document.(*iso20022.Pacs009)
	doc.FICdtTrf.CdtTrfTxInf = doc.FICdtTrf.CdtTrfTxInf[:1]

	// The truncated document is still well-formed: nothing in the codec
	// cross-checks the count, so the discrepancy is the RECEIVER's to catch.
	// That is the division of labour the element encodes.
	raw, err := iso20022.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal refused the truncated document: %v", err)
	}

	// Read it back off the wire rather than off the struct just truncated. Asserting
	// NbOfTxs on the in-memory document would be a tautology, and the claim is about
	// what a RECEIVER sees — which is where an encoder that "helpfully" recomputed the
	// count on the way out would be caught.
	back, err := iso20022.Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal refused the truncated document: %v", err)
	}
	got := back.Document.(*iso20022.Pacs009).FICdtTrf
	if got.GrpHdr.NbOfTxs != "2" {
		t.Errorf("NbOfTxs off the wire = %q, want the sender's original assertion of 2", got.GrpHdr.NbOfTxs)
	}
	if len(got.CdtTrfTxInf) != 1 {
		t.Fatalf("got %d transactions off the wire, want the 1 that survived truncation", len(got.CdtTrfTxInf))
	}
	// The two disagree, the codec accepted it, and that disagreement is the
	// only evidence a leg went missing. A receiver deriving the count from the
	// slice would have destroyed it.
}

// The amount's scale comes from the asset definition, never from a two-decimal
// constant — and the proof is that BITCOIN IS REFUSED. A translator carrying a
// hardcoded 2 would render 250000 minor units of BTC as "2500.00": a message a
// counterparty accepts, describing a sum six orders of magnitude off. The asset's own
// scale gives "0.00250000", which ActiveCurrencyAndAmount cannot carry, so the
// translator refuses. Refusal is the correct behaviour AND the evidence.
func TestSettlementMessageTakesItsScaleFromTheAsset(t *testing.T) {
	settle := func(asset ledger.AssetCode) (iso20022.ActiveCurrencyAndAmount, error) {
		env, err := SettlementMessage([]SettlementLeg{
			{From: "AURODEFFXXX", To: "VERDITMMXXX", Amount: 250000, Asset: asset, Reference: "cyc_1:bank_1"},
		}, MessageContext{From: "CSMXFRPPXXX", To: "CBSEDEFFXXX", MsgID: "CSM-1", Now: messageNow})
		if err != nil {
			return iso20022.ActiveCurrencyAndAmount{}, err
		}
		return env.Document.(*iso20022.Pacs009).FICdtTrf.CdtTrfTxInf[0].IntrBkSttlmAmt, nil
	}

	got, err := settle("EUR")
	if err != nil {
		t.Fatalf("SettlementMessage in EUR: %v", err)
	}
	if got.Value != "2500.00" || got.Ccy != "EUR" {
		t.Errorf("250000 minor units of EUR rendered as %s %s, want EUR 2500.00", got.Ccy, got.Value)
	}

	// An eight-decimal asset the ledger holds and the standard's element cannot
	// express. A hardcoded scale would have produced 2500.00 here and passed.
	if _, err := settle("BTC"); !errors.Is(err, iso20022.ErrAmountScale) {
		t.Errorf("SettlementMessage in BTC = %v, want ErrAmountScale: the standard caps this element at five fraction digits", err)
	}
}

// The magnitude bound, pinned at the exact minor unit where it flips.
//
// A test at math.MaxInt64 proves nothing: it is orders of magnitude past every
// candidate threshold, so it passes whatever the real bound is. A number in prose
// needs a test that would fail if the number were wrong, which means testing the pair
// astride it. 9,223,372,036,854,775 is math.MaxInt64 / 1000, which is where the
// validator's padding into minor units overflows int64. The seventeen-digit case is a
// separate limit and the codec's own, not the standard's.
func TestSettlementMessageAmountBound(t *testing.T) {
	settle := func(minor ledger.Amount) error {
		_, err := SettlementMessage([]SettlementLeg{
			{From: "AURODEFFXXX", To: "VERDITMMXXX", Amount: minor, Asset: "EUR", Reference: "r"},
		}, MessageContext{From: "CSMXFRPPXXX", To: "CBSEDEFFXXX", MsgID: "CSM-1", Now: messageNow})
		return err
	}

	// Sixteen rendered digits: 92233720368547.75. The last amount that fits.
	if err := settle(9223372036854775); err != nil {
		t.Errorf("MaxInt64/1000 minor units of EUR refused: %v", err)
	}
	// One minor unit more. Same digit count, same shape, over the int64 bound
	// the validator's padding imposes.
	if err := settle(9223372036854776); !errors.Is(err, iso20022.ErrAmountFormat) {
		t.Errorf("one minor unit past the bound = %v, want ErrAmountFormat", err)
	}
	// Seventeen digits: 999999999999999.99. Legal under the standard's
	// eighteen-digit ceiling, representable in ledger.Amount, refused here. The
	// bound is the codec's implementation, not the standard.
	if err := settle(99999999999999999); !errors.Is(err, iso20022.ErrAmountFormat) {
		t.Errorf("a legal 17-digit amount = %v, want the codec's ErrAmountFormat refusal", err)
	}
	// And the far end.
	if err := settle(math.MaxInt64); !errors.Is(err, iso20022.ErrAmountFormat) {
		t.Errorf("MaxInt64 = %v, want ErrAmountFormat", err)
	}
}

// An asset the ledger has never heard of is refused rather than rendered at
// some default scale, because there is no default that is right.
func TestSettlementMessageRefusesAnUnknownAsset(t *testing.T) {
	_, err := SettlementMessage([]SettlementLeg{
		{From: "AURODEFFXXX", To: "VERDITMMXXX", Amount: 1, Asset: "XYZ", Reference: "r"},
	}, MessageContext{From: "CSMXFRPPXXX", To: "CBSEDEFFXXX", MsgID: "CSM-1", Now: messageNow})
	if err == nil {
		t.Fatal("rendered an amount in an asset the ledger does not define")
	}
}

// ---------------------------------------------------------------------------
// Refusals
// ---------------------------------------------------------------------------
//
// The one error that must NOT become a wire reason code — a store failure or a
// cancelled context mistranslated into a fact about the counterparty's message — has
// no outbound test here, because building an outbound message reads no store. Its one
// pin is inbound, under "Inbound: a received message becomes a request".

// failingTx is a Tx whose directory lookups fail with an error of the caller's
// choosing. Everything else is the real transaction, promoted by embedding, so a
// participant still resolves and the failure lands on exactly the call under test.
//
// ListDepositAccountsByIdentifier is the one override: it is what the inbound
// resolution in ResolveIdentifierTx calls, reached through a View. There is no
// outbound counterpart, partiesOf reading nothing to build an outbound message.
type failingTx struct {
	BankTx
	err error
}

func (t failingTx) ListDepositAccountsByIdentifier(ctx context.Context, book ledger.BookID, ident deposit.Identifier) ([]deposit.Account, error) {
	return nil, t.err
}

// failingStore wraps a real Store and hands every view a failingTx. There is no other
// way to reach an inbound resolution failure on demand: the store checks ctx.Err()
// before it opens a transaction, so a cancelled context never gets as far as the
// closure, and a real database failure cannot be provoked from a test.
type failingStore struct {
	BankStore
	err error
}

func (s failingStore) View(ctx context.Context, fn func(context.Context, BankTx) error) error {
	return s.BankStore.View(ctx, func(ctx context.Context, tx BankTx) error {
		return fn(ctx, failingTx{BankTx: tx, err: s.err})
	})
}

// There is no OUTBOUND equivalent of the refusal tests below. partiesOf reads no
// participant and no account building an outbound message — both sides are already on
// the Payment, resolved once at submission — so there is no store call left to fail,
// no context left to cancel, and no directory lookup that could come back not-found.
//
// The property — a store failure or a cancellation must not be misreported as a fact
// about the counterparty's message — holds on the INBOUND side, where
// addressedPartyTx and ResolveIdentifierTx read the store, and
// TestCreditTransferRequestDoesNotBlameTheCounterpartyForAStoreFailure pins it there.

// A payment whose scheme is not registered has no asset, and therefore no scale
// to render its amount at. Guessing euro is the multi-asset mistake amountOf
// exists to prevent, so it is refused instead.
func TestCreditTransferMessageRefusesAnUnregisteredScheme(t *testing.T) {
	n, p := networkWithOnePayment(t)
	p.Scheme = "sepa.invented"

	_, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if !errors.Is(err, ErrSchemeNotFound) {
		t.Fatalf("CreditTransferMessage = %v, want ErrSchemeNotFound", err)
	}
}

// A collection with no mandate is refused before the message is built. MndtId is
// EPC-mandatory and a pacs.003 without it is invalid, but the reason to catch it
// here is stronger than validity: a direct debit with no mandate is not a
// badly-formed message, it is an unauthorised claim on someone's account.
func TestDirectDebitMessageRefusesACollectionWithNoMandate(t *testing.T) {
	n, p, _ := networkWithOneCollection(t)

	_, err := n.DirectDebitMessage([]Collection{{Payment: p, Mandate: Mandate{}}}, MessageContext{From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if !errors.Is(err, ErrMandateRequired) {
		t.Fatalf("DirectDebitMessage = %v, want ErrMandateRequired", err)
	}
}

// A return with no reason is refused. RtrRsnInf is EPC-mandatory and
// ReturnReasonChoice needs exactly one arm, so there is nothing to put in the
// element — and a return that does not say why it came back is the one thing the
// creditor's bank cannot act on.
func TestReturnMessageRefusesAReturnWithNoReason(t *testing.T) {
	n, p := networkWithOnePayment(t)

	_, err := n.ReturnMessage(p, "", "no code given",
		MessageContext{From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if !errors.Is(err, iso20022.ErrMissingElement) {
		t.Fatalf("ReturnMessage with no reason = %v, want ErrMissingElement", err)
	}
}

// A settlement instruction with no legs is refused rather than emitted empty.
// CdtTrfTxInf is 1..n, so the document would be invalid — but the reason to
// refuse is that an instruction to settle nothing is not a thing anyone means to
// send.
func TestSettlementMessageRefusesAnEmptyInstruction(t *testing.T) {
	_, err := SettlementMessage(nil, MessageContext{From: "CSMXFRPPXXX", To: "CBSEDEFFXXX", MsgID: "CSM-1", Now: messageNow})
	if !errors.Is(err, iso20022.ErrMissingElement) {
		t.Fatalf("SettlementMessage with no legs = %v, want ErrMissingElement", err)
	}
}

// ---------------------------------------------------------------------------
// Inbound: a received message becomes a request
// ---------------------------------------------------------------------------

// The round trip is the assertion that matters: a message this system produced
// must come back as the payment it was built from. Anything less is a
// translator that is self-consistent and wrong.
func TestCreditTransferRoundTripsThroughTheWire(t *testing.T) {
	n, p := networkWithOnePayment(t)
	ctx := context.Background()
	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{
		From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "AURO-1", Now: messageNow,
	})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	raw, err := iso20022.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := iso20022.Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	got, err := readOne(n.bank(p.CreditorDetails.Agent).CreditTransferRequest(ctx, back.Document.(*iso20022.Pacs008)))
	if err != nil {
		t.Fatalf("CreditTransferRequest: %v", err)
	}
	if got.Scheme != SchemeSEPACT {
		t.Errorf("scheme = %q, want %q", got.Scheme, SchemeSEPACT)
	}
	if got.Amount != p.Amount {
		t.Errorf("amount = %d, want %d", got.Amount, p.Amount)
	}
	if got.EndToEndID != p.EndToEndID {
		t.Errorf("end-to-end id = %q, want %q", got.EndToEndID, p.EndToEndID)
	}
	if got.Description != p.Description {
		t.Errorf("description = %q, want %q", got.Description, p.Description)
	}
	// The CREDITOR is this bank's own customer on a push, so it is resolved by
	// ADDRESS and not by an id the message never carried. This would catch a
	// translator quietly threading internal account ids through a message with no
	// element for them.
	if !got.Creditor.SameParty(p.Creditor) {
		t.Errorf("creditor resolved to %+v, want %+v", got.Creditor, p.Creditor)
	}
	// The address comes back too, and it is the one the message quoted. It is what
	// submission checks against the account it resolves to, so losing it here turns
	// "the payment records the address it was sent to" into "the payment records
	// whatever address the account happens to hold".
	if got.Creditor.Identifier != p.Creditor.Identifier {
		t.Errorf("creditor address = %+v, want the quoted %+v", got.Creditor.Identifier, p.Creditor.Identifier)
	}
	// The DEBTOR is the SENDING bank's customer. CreditTransferRequest resolves this
	// bank's own side only, so the debtor comes back with the address the message
	// quoted and NO ACCOUNT: an account id is this bank's own key for its own register.
	//
	// The AGENT is not part of that claim and is not empty. It is DbtrAgt, read
	// straight off the message and asserted rather than derived.
	if got.Debtor.Account != "" {
		t.Errorf("debtor resolved to %+v, want it recorded rather than resolved", got.Debtor)
	}
	if got.Debtor.Identifier != p.Debtor.Identifier {
		t.Errorf("debtor address = %+v, want the quoted %+v", got.Debtor.Identifier, p.Debtor.Identifier)
	}
	// What the message says about EACH party — its bank and the name on the
	// account — comes back too, read off Dbtr/DbtrAgt and Cdtr/CdtrAgt directly
	// rather than resolved from either directory.
	if got.DebtorDetails != p.DebtorDetails {
		t.Errorf("debtor details = %+v, want %+v", got.DebtorDetails, p.DebtorDetails)
	}
	if got.CreditorDetails != p.CreditorDetails {
		t.Errorf("creditor details = %+v, want %+v", got.CreditorDetails, p.CreditorDetails)
	}
}

// The collection round trip, and the element a credit transfer has no
// counterpart for: the mandate the debtor's bank has to check the claim
// against.
func TestDirectDebitRoundTripsThroughTheWire(t *testing.T) {
	n, p, m := networkWithOneCollection(t)
	ctx := context.Background()
	env, err := n.DirectDebitMessage([]Collection{{Payment: p, Mandate: m}}, MessageContext{
		From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "VERDE-1", Now: messageNow,
	})
	if err != nil {
		t.Fatalf("DirectDebitMessage: %v", err)
	}
	raw, err := iso20022.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := iso20022.Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	got, err := readOne(n.bank(p.DebtorDetails.Agent).DirectDebitRequest(ctx, back.Document.(*iso20022.Pacs003)))
	if err != nil {
		t.Fatalf("DirectDebitRequest: %v", err)
	}
	if got.Scheme != SchemeSEPADD {
		t.Errorf("scheme = %q, want %q", got.Scheme, SchemeSEPADD)
	}
	if got.Amount != p.Amount {
		t.Errorf("amount = %d, want %d", got.Amount, p.Amount)
	}
	if got.MandateID != m.ID {
		t.Errorf("mandate = %q, want %q", got.MandateID, m.ID)
	}
	// A pull message travels against the money, so the party that SENT it is the one
	// being paid, and it is routed here by DbtrAgt: this bank is the DEBTOR's, and the
	// DEBTOR is the side it has standing to resolve. A translator reading the two
	// account elements in pacs.008 order would point the collection the wrong way and
	// still resolve successfully.
	if !got.Debtor.SameParty(p.Debtor) {
		t.Errorf("debtor resolved to %+v, want %+v", got.Debtor, p.Debtor)
	}
	if got.Debtor.Identifier != p.Debtor.Identifier {
		t.Errorf("debtor address = %+v, want the quoted %+v", got.Debtor.Identifier, p.Debtor.Identifier)
	}
	// The CREDITOR is the SENDING bank's customer here — the mirror of
	// receiveCreditTransfer's debtor — and comes back recorded rather than
	// resolved: no account, only the address the message quoted. The AGENT is
	// CdtrAgt and is on the message; see the push's mirror of this note.
	if got.Creditor.Account != "" {
		t.Errorf("creditor resolved to %+v, want it recorded rather than resolved", got.Creditor)
	}
	if got.Creditor.Identifier != p.Creditor.Identifier {
		t.Errorf("creditor address = %+v, want the quoted %+v", got.Creditor.Identifier, p.Creditor.Identifier)
	}
	if got.DebtorDetails != p.DebtorDetails {
		t.Errorf("debtor details = %+v, want %+v", got.DebtorDetails, p.DebtorDetails)
	}
	if got.CreditorDetails != p.CreditorDetails {
		t.Errorf("creditor details = %+v, want %+v", got.CreditorDetails, p.CreditorDetails)
	}
}

// TestAReceivingBankDoesNotResolveTheSendersCustomer: a pacs.008 quoting a debtor
// IBAN this bank does not hold is NOT refused AC01. The debtor is the SENDING bank's
// customer, and a receiving bank resolves only its own side of the message — a
// message whose creditor it can find is accepted whatever the debtor's address says.
func TestAReceivingBankDoesNotResolveTheSendersCustomer(t *testing.T) {
	n, p := networkWithOnePayment(t)
	ctx := context.Background()
	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	doc := env.Document.(*iso20022.Pacs008)
	// An IBAN in a well-formed country/checksum shape, held by nobody in this
	// network — no participant here has ever opened an account with it.
	unknown := iso20022.IBAN("SE00000000000000000000")
	doc.FIToFICstmrCdtTrf.CdtTrfTxInf[0].DbtrAcct.Id.IBAN = &unknown

	req, err := readOne(n.bank(p.CreditorDetails.Agent).CreditTransferRequest(ctx, doc))
	if err != nil {
		t.Fatalf("CreditTransferRequest: %v — a receiving bank does not check the sender's customer", err)
	}
	if req.CreditorDetails.Agent == "" {
		t.Error("the receiving bank did not resolve its own customer")
	}
}

// TestAReceivingBankDoesNotResolveTheSendersCustomerOnAPull is the direct-debit
// mirror: on a pacs.003 this bank is the DEBTOR's, and the party it must not check
// is the CREDITOR. Getting the direction backwards would resolve successfully and be
// wrong, which is why both directions are pinned.
func TestAReceivingBankDoesNotResolveTheSendersCustomerOnAPull(t *testing.T) {
	n, p, m := networkWithOneCollection(t)
	ctx := context.Background()
	env, err := n.DirectDebitMessage([]Collection{{Payment: p, Mandate: m}}, MessageContext{From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("DirectDebitMessage: %v", err)
	}
	doc := env.Document.(*iso20022.Pacs003)
	unknown := iso20022.IBAN("IT00000000000000000000")
	doc.FIToFICstmrDrctDbt.DrctDbtTxInf[0].CdtrAcct.Id.IBAN = &unknown

	req, err := readOne(n.bank(p.DebtorDetails.Agent).DirectDebitRequest(ctx, doc))
	if err != nil {
		t.Fatalf("DirectDebitRequest: %v — a receiving bank does not check the sender's customer", err)
	}
	if req.DebtorDetails.Agent == "" {
		t.Error("the receiving bank did not resolve its own customer")
	}
}

func TestCreditTransferRequestRefusesAnUnknownIBAN(t *testing.T) {
	n, p := networkWithOnePayment(t)
	ctx := context.Background()
	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	doc := env.Document.(*iso20022.Pacs008)

	// An address in the RECEIVING bank's own range that no account of its holds. It
	// has to be well-formed and at that bank: a value failing its check digits is
	// refused for that instead, and another bank's code is a different refusal again.
	verde, err := n.bank(p.CreditorDetails.Agent).GetBank(ctx, ParticipantID(p.CreditorDetails.Agent))
	assertNoError(t, err)
	unknown := iso20022.IBAN(mintAt(t, verde, 999_999).Value)
	doc.FIToFICstmrCdtTrf.CdtTrfTxInf[0].CdtrAcct.Id.IBAN = &unknown

	// The refusal is the TRANSACTION's and not the file's, which is what lets a
	// bulk file of a thousand be answered per payee. See InboundTransaction.
	tx, err := readOneTx(n.bank(p.CreditorDetails.Agent).CreditTransferRequest(ctx, doc))
	if err != nil {
		t.Fatalf("CreditTransferRequest refused the whole file: %v", err)
	}
	if tx.Refusal == nil {
		t.Fatal("resolved an IBAN no account holds")
	}
	// ErrAccountNotInParticipant is what becomes AC01 "incorrect account number" on
	// the wire. The mapping is pinned by TestReasonForKnownErrors in the other package
	// in this directory — that test drives the unexported reasonTable, this one needs
	// testenv, and no one package can reach both.
	if !errors.Is(tx.Refusal, ErrAccountNotInParticipant) {
		t.Errorf("CreditTransferRequest on an unknown IBAN = %v, want ErrAccountNotInParticipant", tx.Refusal)
	}
}

// An account element that carries no IBAN at all is refused as unaddressable, not
// dereferenced. AccountIdentification4Choice's arms are pointers because encoding/xml
// cannot express an xsd:choice, and INBOUND is where a nil one arrives: the Othr arm
// is legal in the schema, so a counterparty is free to send it. Outbound never
// produces one, so a missing check here is a panic in the actor that reads them.
func TestCreditTransferRequestRefusesAnAccountThatIsNotAnIBAN(t *testing.T) {
	n, p := networkWithOnePayment(t)
	ctx := context.Background()
	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	doc := env.Document.(*iso20022.Pacs008)
	doc.FIToFICstmrCdtTrf.CdtTrfTxInf[0].CdtrAcct.Id = iso20022.AccountIdentification4Choice{
		Othr: &iso20022.GenericAccountIdentification{Id: "4111111111111111"},
	}

	_, err = n.bank(p.CreditorDetails.Agent).CreditTransferRequest(ctx, doc)
	if !errors.Is(err, ErrUnaddressableAccount) {
		t.Fatalf("CreditTransferRequest on a non-IBAN account = %v, want ErrUnaddressableAccount", err)
	}
}

// An address two accounts both claim is refused, and — the part that matters — NOT
// as an incorrect account number.
//
// ResolveIdentifierTx returns deposit.ErrIdentifierAmbiguous rather than the first
// hit, and this translator passes that through unchanged. AC01 would tell the sending
// bank its customer quoted a bad IBAN, which is a false statement about someone
// else's data: the IBAN is fine, and it is this bank's own register that cannot say
// which of its accounts is meant. An error with no code falls to MS03, which is true.
//
// A THIRD BANK claiming the address is not provokable: resolution is narrowed to the
// receiving bank's own register. What survives is the collision a register CAN see.
func TestCreditTransferRequestRefusesAnAddressTwoOfItsOwnAccountsClaim(t *testing.T) {
	n, p := networkWithOnePayment(t)
	ctx := context.Background()
	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}

	// A second account AT THE RECEIVING BANK holding the creditor's IBAN, written
	// straight through the store past the register's write-time check, because that is
	// the only way it arises. Through that bank's OWN network: a bank row is in the
	// bank shape and the clearing house's has no such table.
	verde, err := n.bank(p.CreditorDetails.Agent).GetBank(ctx, ParticipantID(p.CreditorDetails.Agent))
	assertNoError(t, err)
	impostor := openCustomer(t, ctx, verde, "Impostor")
	assertNoError(t, verde.Deposit.Store().Update(ctx, func(ctx context.Context, tx deposit.Tx) error {
		a, err := tx.GetDepositAccount(ctx, verde.BookID, impostor.ID)
		if err != nil {
			return err
		}
		a.Identifiers = append(a.Identifiers, p.Creditor.Identifier)
		return tx.PutDepositAccount(ctx, verde.BookID, a)
	}))

	tx, err := readOneTx(n.bank(p.CreditorDetails.Agent).CreditTransferRequest(ctx, env.Document.(*iso20022.Pacs008)))
	if err != nil {
		t.Fatalf("CreditTransferRequest refused the whole file: %v", err)
	}
	if !errors.Is(tx.Refusal, deposit.ErrIdentifierAmbiguous) {
		t.Fatalf("CreditTransferRequest on a contested address = %v, want ErrIdentifierAmbiguous", tx.Refusal)
	}
	if errors.Is(tx.Refusal, ErrAccountNotInParticipant) {
		t.Error("a contested address surfaced as ErrAccountNotInParticipant, which reaches the sender as AC01 \"incorrect account number\" — the number is not the problem")
	}
}

// A store failure is not a defect in the counterparty's message and must not be
// reported as one. It is a live hazard here because the obvious way to write the
// resolver is `if err != nil { return ErrAccountNotInParticipant }`, a not-found being
// the expected failure of a directory lookup — a shape that turns this system's
// outage into AC01 and sends another bank hunting a fault in an address that was
// never wrong. Injected at the seam, for the reason failingStore documents.
func TestCreditTransferRequestDoesNotBlameTheCounterpartyForAStoreFailure(t *testing.T) {
	n, p := networkWithOnePayment(t)
	ctx := context.Background()
	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}

	dropped := errors.New("connection reset by peer")
	// The store made to fail is the RECEIVING BANK's, the one this act reads: it
	// resolves the payee in that bank's own register. Wrapping the clearing house's
	// reports a missing table rather than the injected failure.
	broken := NewBankNetwork(failingStore{BankStore: n.bank(p.CreditorDetails.Agent).Store(), err: dropped},
		func() time.Time { return fixedTime }, ParticipantID(p.CreditorDetails.Agent))

	_, err = broken.CreditTransferRequest(ctx, env.Document.(*iso20022.Pacs008))
	if err == nil {
		t.Fatal("resolved a party while the store was failing")
	}
	if !errors.Is(err, dropped) {
		t.Errorf("error = %v, want it to carry the store's own failure", err)
	}
	if errors.Is(err, ErrAccountNotInParticipant) {
		t.Errorf("a store failure surfaced as ErrAccountNotInParticipant, which reaches a counterparty as AC01 \"incorrect account number\"")
	}
}

// The scale an inbound amount is read at comes from the ASSET the message names,
// never from a two-decimal constant.
//
// A pacs.008 quoting BTC is refused as an asset mismatch, because SEPA credit
// transfer settles in euro. The evidence that the scale was looked up is WHICH
// refusal arrives: at the asset's own scale of 8, "0.00250000" is a well-formed
// 250000 satoshi and the only thing wrong is the currency. A hardcoded 2 would refuse
// it as ErrAmountScale and never reach the question that decides the message.
func TestCreditTransferRequestRefusesAMessageInAnotherAsset(t *testing.T) {
	n, p := networkWithOnePayment(t)
	ctx := context.Background()
	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	doc := env.Document.(*iso20022.Pacs008)
	doc.FIToFICstmrCdtTrf.CdtTrfTxInf[0].IntrBkSttlmAmt = iso20022.ActiveCurrencyAndAmount{
		Ccy: "BTC", Value: "0.00250000",
	}

	_, err = n.bank(p.CreditorDetails.Agent).CreditTransferRequest(ctx, doc)
	if !errors.Is(err, ErrAssetMismatch) {
		t.Fatalf("CreditTransferRequest on a BTC pacs.008 = %v, want ErrAssetMismatch", err)
	}
}

// dollarPush and secondEuroPush are two push schemes registered only by the two
// tests below. Both embed SCT, so a method added to Scheme reaches them without
// a compile error that says nothing about what they are for.
type dollarPush struct{ SCT }

func (dollarPush) ID() SchemeID            { return "usd.ct" }
func (dollarPush) Asset() ledger.AssetCode { return "USD" }

type secondEuroPush struct{ SCT }

func (secondEuroPush) ID() SchemeID { return "sepa.ct.instant" }

// TestCreditTransferRequestReadsTheSchemeFromTheCurrency is the positive half of the
// rule schemeSettling states: a pacs.008 says PUSH and nothing more, and which push
// scheme it travelled under is decided by the currency, this network registering one
// scheme per direction and asset. With a single push scheme the two answers are the
// same one and the distinction cannot be observed at all.
func TestCreditTransferRequestReadsTheSchemeFromTheCurrency(t *testing.T) {
	n, p := networkWithOnePayment(t)
	n.RegisterScheme(dollarPush{})
	ctx := context.Background()

	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	assertNoError(t, err)
	doc := env.Document.(*iso20022.Pacs008)

	// The euro message still resolves to the euro scheme, which is the half that
	// must not change.
	req, err := readOne(n.bank(p.CreditorDetails.Agent).CreditTransferRequest(ctx, doc))
	assertNoError(t, err)
	assertEqual(t, "scheme of a euro pacs.008", req.Scheme, SchemeSEPACT)

	// The same message in dollars resolves to the dollar scheme rather than
	// being refused as a mismatch.
	doc.FIToFICstmrCdtTrf.CdtTrfTxInf[0].IntrBkSttlmAmt = iso20022.ActiveCurrencyAndAmount{Ccy: "USD", Value: "2500.00"}
	req, err = readOne(n.bank(p.CreditorDetails.Agent).CreditTransferRequest(ctx, doc))
	assertNoError(t, err)
	assertEqual(t, "scheme of a dollar pacs.008", req.Scheme, SchemeID("usd.ct"))
	assertEqual(t, "amount in minor units", req.Amount, ledger.Amount(250000))
}

// TestCreditTransferRequestRefusesAnAmbiguousScheme is the other half, a refusal
// because there is nothing in the message to break the tie. Two push schemes settling
// in the same asset is a network whose rulebook a pacs.008 cannot name; picking one
// would resolve every euro message under a scheme chosen by alphabet, and the two
// could differ in settlement model, value date and whether returns are allowed.
func TestCreditTransferRequestRefusesAnAmbiguousScheme(t *testing.T) {
	n, p := networkWithOnePayment(t)
	n.RegisterScheme(secondEuroPush{})
	ctx := context.Background()

	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	assertNoError(t, err)

	_, err = n.bank(p.CreditorDetails.Agent).CreditTransferRequest(ctx, env.Document.(*iso20022.Pacs008))
	if !errors.Is(err, ErrAssetMismatch) {
		t.Fatalf("CreditTransferRequest with two euro push schemes = %v, want ErrAssetMismatch", err)
	}
	if !strings.Contains(err.Error(), "sepa.ct.instant") {
		t.Errorf("the refusal does not name the schemes it could not choose between: %v", err)
	}
}

// A currency the ledger has never heard of is refused rather than read at some
// default scale, because there is no default that is right — the same refusal
// the outbound direction makes in TestSettlementMessageRefusesAnUnknownAsset.
func TestCreditTransferRequestRefusesAnUnknownCurrency(t *testing.T) {
	n, p := networkWithOnePayment(t)
	ctx := context.Background()
	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	doc := env.Document.(*iso20022.Pacs008)
	doc.FIToFICstmrCdtTrf.CdtTrfTxInf[0].IntrBkSttlmAmt = iso20022.ActiveCurrencyAndAmount{Ccy: "XYZ", Value: "25.00"}

	// ledger.ErrAssetNotFound and not merely "an error": the refusal must be
	// the ledger saying it does not define this asset, not some later
	// complaint about the number's shape at a scale that was guessed.
	if _, err := n.bank(p.CreditorDetails.Agent).CreditTransferRequest(ctx, doc); !errors.Is(err, ledger.ErrAssetNotFound) {
		t.Fatalf("CreditTransferRequest in an undefined currency = %v, want ledger.ErrAssetNotFound", err)
	}
}

// NOTPROVIDED comes back as NO reference, the inverse of what endToEndOf writes on
// the way out, and getting it wrong is not cosmetic.
//
// EndToEndID is deduplicated: submission refuses a second payment carrying a
// reference it has seen, and an empty one is never an identity. Storing the literal
// string would give every reference-less payment the same reference, and the SECOND
// to arrive would be rejected as a duplicate of the first.
func TestCreditTransferRequestReadsNOTPROVIDEDBackAsNoReference(t *testing.T) {
	n, p := networkWithOnePayment(t)
	ctx := context.Background()
	p.EndToEndID = ""
	p.Amount = 1000
	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	if got := env.Document.(*iso20022.Pacs008).FIToFICstmrCdtTrf.CdtTrfTxInf[0].PmtId.EndToEndId; got != "NOTPROVIDED" {
		t.Fatalf("the fixture message carries EndToEndId %q, want NOTPROVIDED", got)
	}

	req, err := readOne(n.bank(p.CreditorDetails.Agent).CreditTransferRequest(ctx, env.Document.(*iso20022.Pacs008)))
	if err != nil {
		t.Fatalf("CreditTransferRequest: %v", err)
	}
	if req.EndToEndID != "" {
		t.Errorf("end-to-end id = %q, want it empty: NOTPROVIDED means the sender had none", req.EndToEndID)
	}
	// CreditorDetails now comes off the message itself — agentIn/nameIn reading
	// Cdtr/CdtrAgt — rather than being carried forward from the payment this
	// fixture already knows about. Asserted, not assigned: this is the real
	// translator's own output.
	if req.CreditorDetails != p.CreditorDetails {
		t.Errorf("creditor details = %+v, want %+v", req.CreditorDetails, p.CreditorDetails)
	}
	// The debtor's participant and account are filled in directly rather than left as
	// CreditTransferRequest returned them: the debtor is not resolved at all, so
	// req.Debtor holds only the address the message quoted. What is under test is
	// EndToEndID deduplication, which needs a resolvable debtor to reach.
	req.DebtorDetails.Agent = p.DebtorDetails.Agent
	req.Debtor.Account = p.Debtor.Account
	if _, err := initiate(ctx, n, req); err != nil {
		t.Fatalf("initiating a reference-less payment: %v", err)
	}
	if _, err := initiate(ctx, n, req); err != nil {
		t.Fatalf("a second reference-less payment was refused: %v", err)
	}
}

// NbOfTxs is the sender's own assertion of what it sent, and THIS is the receiver
// that checks it. TestSettlementMessageNbOfTxsSurvivesATruncatedFile pins the other
// half — neither the encoder nor the decoder recomputes it, so the discrepancy
// survives the wire intact. A message that says two and carries one is a truncated
// file.
func TestCreditTransferRequestRefusesATruncatedFile(t *testing.T) {
	n, p := networkWithOnePayment(t)
	ctx := context.Background()
	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	doc := env.Document.(*iso20022.Pacs008)
	doc.FIToFICstmrCdtTrf.GrpHdr.NbOfTxs = "2"

	_, err = n.bank(p.CreditorDetails.Agent).CreditTransferRequest(ctx, doc)
	if err == nil {
		t.Fatal("read a message that declared two transactions and carried one")
	}
	if !strings.Contains(err.Error(), "NbOfTxs") {
		t.Errorf("error = %v, want it to name the count the sender asserted", err)
	}
}

// A file carrying two transactions is read as two instructions, in the file's own
// order. A bank accumulates behind a cut-off and sends one message carrying
// everything it has, so a reader that took CdtTrfTxInf[0] and stopped would drop
// every payment after the first — arrived, acknowledged at the message level, and
// never happened.
func TestCreditTransferRequestReadsEveryTransactionInAFile(t *testing.T) {
	n, p := networkWithOnePayment(t)
	ctx := context.Background()
	env, err := n.CreditTransferMessage([]Payment{p}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	body := &env.Document.(*iso20022.Pacs008).FIToFICstmrCdtTrf
	second := body.CdtTrfTxInf[0]
	second.PmtId = iso20022.PaymentIdentification{TxId: "pay-second", EndToEndId: "e2e-2"}
	body.CdtTrfTxInf = append(body.CdtTrfTxInf, second)
	body.GrpHdr.NbOfTxs = "2"

	txs, err := n.bank(p.CreditorDetails.Agent).CreditTransferRequest(ctx, env.Document.(*iso20022.Pacs008))
	if err != nil {
		t.Fatalf("CreditTransferRequest over a file of two: %v", err)
	}
	if len(txs) != 2 {
		t.Fatalf("read %d instructions out of a file of two", len(txs))
	}
	if txs[0].ID != p.ID || txs[1].ID != "pay-second" {
		t.Errorf("ids are %s and %s, want %s and pay-second in the file's order", txs[0].ID, txs[1].ID, p.ID)
	}
	if txs[1].Request.EndToEndID != "e2e-2" {
		t.Errorf("the second instruction's reference is %q, want e2e-2", txs[1].Request.EndToEndID)
	}
	// Resolution runs on every transaction, not only the one a single-payment
	// reader would have looked at: an unresolved creditor is an account id the
	// receiving bank never filled in.
	if txs[0].Request.Creditor.Account == "" || txs[1].Request.Creditor.Account == "" {
		t.Errorf("creditors resolved to %q and %q, want this bank's own account on both",
			txs[0].Request.Creditor.Account, txs[1].Request.Creditor.Account)
	}
}

// A file carries every payment given to it, and says how many.
func TestCreditTransferMessageCarriesEveryPaymentInTheFile(t *testing.T) {
	n, p := networkWithOnePayment(t)
	second := p
	second.ID = "pay-second"
	second.EndToEndID = "e2e-2"

	env, err := n.CreditTransferMessage([]Payment{p, second},
		MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	body := env.Document.(*iso20022.Pacs008).FIToFICstmrCdtTrf
	if len(body.CdtTrfTxInf) != 2 {
		t.Fatalf("the file carries %d transactions, want 2", len(body.CdtTrfTxInf))
	}
	if body.GrpHdr.NbOfTxs != "2" {
		t.Errorf("NbOfTxs is %q, want 2 — the count the sender asserts is the count it sent", body.GrpHdr.NbOfTxs)
	}
	if got := body.CdtTrfTxInf[1].PmtId.TxId; got != "pay-second" {
		t.Errorf("the second transaction is %s, want pay-second in the order given", got)
	}
}

// One file, one interbank settlement date. IntrBkSttlmDt is the group's element
// and there is one of it, so a file whose payments settle on different days
// would assert a date that is false for some of them.
func TestAFileCannotAssertTwoSettlementDates(t *testing.T) {
	n, p := networkWithOnePayment(t)
	tomorrow := p
	tomorrow.ID = "pay-second"
	tomorrow.ValueDate = p.ValueDate.AddDate(0, 0, 1)

	_, err := n.CreditTransferMessage([]Payment{p, tomorrow},
		MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err == nil {
		t.Fatal("built one file asserting two settlement dates")
	}
	if !strings.Contains(err.Error(), "settlement dates") {
		t.Errorf("error = %v, want it to name the two dates that disagreed", err)
	}
}

func TestAFileWithNoTransactionsIsNotAMessage(t *testing.T) {
	n, _ := networkWithOnePayment(t)

	if _, err := n.CreditTransferMessage(nil, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow}); err == nil {
		t.Error("built a pacs.008 carrying nothing")
	}
	if _, err := n.DirectDebitMessage(nil, MessageContext{From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow}); err == nil {
		t.Error("built a pacs.003 carrying nothing")
	}
}

// Each collection in a file travels with its OWN mandate: one debtor's standing
// authority says nothing about the next one's, so a builder that carried the
// file's first mandate on every transaction would put an authority on the wire
// that its debtor never gave.
func TestEachCollectionInAFileCarriesItsOwnMandate(t *testing.T) {
	n, p, m := networkWithOneCollection(t)
	second := p
	second.ID = "pay-second"
	other := m
	other.ID = "mandate-second"

	env, err := n.DirectDebitMessage([]Collection{{Payment: p, Mandate: m}, {Payment: second, Mandate: other}},
		MessageContext{From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("DirectDebitMessage: %v", err)
	}
	txs := env.Document.(*iso20022.Pacs003).FIToFICstmrDrctDbt.DrctDbtTxInf
	if len(txs) != 2 {
		t.Fatalf("the file carries %d collections, want 2", len(txs))
	}
	if got := txs[0].DrctDbtTx.MndtRltdInf.MndtId; got != string(m.ID) {
		t.Errorf("the first collection quotes mandate %s, want %s", got, m.ID)
	}
	if got := txs[1].DrctDbtTx.MndtRltdInf.MndtId; got != "mandate-second" {
		t.Errorf("the second collection quotes mandate %s, want mandate-second", got)
	}
}

// A collection with no mandate is refused before it becomes a request. The
// outbound direction refuses the same thing for the same reason, and the reason
// is stronger inbound: this is another bank's claim on this bank's customer's
// account, and the mandate is the only thing that makes it authorised.
func TestDirectDebitRequestRefusesACollectionWithNoMandate(t *testing.T) {
	n, p, m := networkWithOneCollection(t)
	ctx := context.Background()
	env, err := n.DirectDebitMessage([]Collection{{Payment: p, Mandate: m}}, MessageContext{From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("DirectDebitMessage: %v", err)
	}
	doc := env.Document.(*iso20022.Pacs003)
	doc.FIToFICstmrDrctDbt.DrctDbtTxInf[0].DrctDbtTx.MndtRltdInf.MndtId = ""

	if _, err := n.bank(p.DebtorDetails.Agent).DirectDebitRequest(ctx, doc); !errors.Is(err, ErrMandateRequired) {
		t.Fatalf("DirectDebitRequest with no mandate = %v, want ErrMandateRequired", err)
	}
}

// An address a PERSON typed reaches the account a bank stored.
//
// A register that stored a readable form while its messages carried a compact one
// would let a bank emit an address it could not then resolve — compaction does not
// run backwards. There is ONE stored form, minted canonical, so the assertion is that
// the message carries the stored string VERBATIM.
//
// A statement prints an IBAN grouped in fours and a keyboard produces whatever case
// the typist was in. Both are the same address, and deposit.Identifier.MatchValue is
// what makes them so — reached by two different comparisons, which is why one test
// drives both: resolution, which finds an account FROM an address, and addressFor's
// Matches, which checks an address QUOTED against an account already named. A fix
// that taught only one would resolve a typed address and still refuse it as a quote.
func TestATypedAddressReachesTheStoredOne(t *testing.T) {
	ctx := context.Background()
	n := testNetwork(t)
	aurora, err := storetest.Admit(ctx, n.nets, "Aurora Bank", "AURODEFFXXX", euroOnly)
	assertNoError(t, err)
	verde, err := storetest.Admit(ctx, n.nets, "Banca Verde", "VERDITMMXXX", euroOnly)
	assertNoError(t, err)

	alice := openCustomer(t, ctx, aurora, "Alice")
	bruno := openCustomer(t, ctx, verde, "Bruno")
	fundAccount(t, ctx, n, aurora, alice, 500000)
	openCycle(t, ctx, n, SchemeSEPACT)

	alicesStored := addressOf(t, alice)
	brunosStored := addressOf(t, bruno)

	// Off a statement, and off a keyboard. Neither can travel on a message — the
	// schema's pattern admits no separators and an upper-case country code only —
	// so both arrive the way a person's does: through a request.
	grouped := deposit.Identifier{
		Scheme: deposit.IdentifierIBAN,
		Value:  iban.IBAN(brunosStored.Value).Grouped(),
	}
	lowered := deposit.Identifier{
		Scheme: deposit.IdentifierIBAN,
		Value:  strings.ToLower(alicesStored.Value),
	}

	// Resolution: Verde finds its own customer from the address as printed.
	ref, err := n.bank(verde.BIC).ResolveIdentifier(ctx, grouped)
	if err != nil {
		t.Fatalf("resolving Bruno's address as a statement prints it: %v", err)
	}
	if ref.Account != bruno.ID {
		t.Errorf("resolved %s, want %s", ref.Account, bruno.ID)
	}

	// Quoting: Alice names her own address in the case she typed it in.
	p, err := initiate(ctx, n, InitiatePaymentRequest{
		Scheme: SchemeSEPACT,
		Debtor: PartyRef{Account: alice.ID, Identifier: lowered},
		// The payee's address as the payer typed it, which is the only thing the
		// payer has: Aurora cannot reach Verde's register to normalise it, so the
		// grouped form is what its copy records and what the codec compacts on
		// the way out.
		Creditor:        PartyRef{Account: bruno.ID, Identifier: grouped},
		Amount:          250000,
		CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
		DebtorDetails:   PartyDetails{Agent: aurora.BIC}})
	assertNoError(t, err)

	// What the payment RECORDS is the account's stored form and not the typed
	// one: a payment's recorded address is one the account actually holds. See
	// addressFor.
	atAurora := mustGetPaymentAt(t, ctx, n.bank(aurora.BIC), p.ID)
	assertEqual(t, "the debtor address on the payer's bank's copy", atAurora.Debtor.Identifier, alicesStored)

	// And the message carries that same string, character for character. One
	// form, so there is nothing for the wire to normalise.
	env, err := n.CreditTransferMessage([]Payment{atAurora}, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	raw, err := iso20022.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(raw), alicesStored.Value) {
		t.Errorf("the message does not carry the debtor's stored address %q:\n%s", alicesStored.Value, raw)
	}

	// The whole way round: the message resolves at Verde against the same stored
	// address the grouped form reached above.
	back, err := iso20022.Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	got, err := readOne(n.bank(verde.BIC).CreditTransferRequest(ctx, back.Document.(*iso20022.Pacs008)))
	if err != nil {
		t.Fatalf("CreditTransferRequest: %v", err)
	}
	if got.Creditor.Account != bruno.ID {
		t.Errorf("the creditor resolved to %s, want %s", got.Creditor.Account, bruno.ID)
	}
	assertEqual(t, "the debtor address the message carried", got.Debtor.Identifier, alicesStored)
}

// A status report about no transactions leaves GrpSts empty and the document is still
// valid, which is the claim groupStatusOf's comment makes. The element is optional in
// the standard precisely so a report can decline to characterise a group it says
// nothing about, and a future GrpSts check in OriginalGroupHeader.validate would
// break this silently.
func TestStatusMessageWithNoTransactionsCharacterisesNoGroup(t *testing.T) {
	env, err := StatusMessage(
		OriginalMessage{MsgID: "AURO-1", MsgDefIdr: "pacs.008.001.08", CreDtTm: messageNow},
		nil,
		MessageContext{From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "VERDE-1", Now: messageNow},
	)
	if err != nil {
		t.Fatalf("StatusMessage: %v", err)
	}
	if _, err := iso20022.Marshal(env); err != nil {
		t.Fatalf("a report with no transaction statuses is not valid: %v", err)
	}
	rpt := env.Document.(*iso20022.Pacs002).FIToFIPmtStsRpt
	if got := rpt.OrgnlGrpInfAndSts.GrpSts; got != "" {
		t.Errorf("GrpSts = %q, want it absent — there is nothing to characterise", got)
	}
	if len(rpt.TxInfAndSts) != 0 {
		t.Errorf("got %d transaction statuses, want none", len(rpt.TxInfAndSts))
	}
}

// TestStatementMessageRoundTripsThroughTheWire pins that what the central bank
// captured inside its unit of work is what the member bank can act on: the movement,
// the closing balance, the asset and the cycle. Both directions in one test, because
// a builder and a reader that agree with each other and not with the wire is the
// failure this catches.
func TestStatementMessageRoundTripsThroughTheWire(t *testing.T) {
	st := SettlementStatement{
		Agent:          "BRVODEFFXXX",
		Account:        "acc_cb_reserve_bank_2",
		Asset:          "EUR",
		Reference:      "cyc_1",
		StatementRef:   "set_1",
		Movement:       250000,
		ClosingBalance: 300000,
		ValueDate:      messageNow,
	}
	env, err := StatementMessage(st, MessageContext{
		From: "CBSEDEFFXXX", To: st.Agent, MsgID: "msg_7", Now: messageNow,
	})
	if err != nil {
		t.Fatalf("StatementMessage: %v", err)
	}

	raw, err := iso20022.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	back, err := iso20022.Unmarshal(raw)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	moves, err := ReadStatement(back.Document.(*iso20022.Camt053))
	if err != nil {
		t.Fatalf("ReadStatement: %v", err)
	}
	if len(moves) != 1 {
		t.Fatalf("movements = %d, want 1", len(moves))
	}
	want := AdvisedMovement{
		Account:        st.Account,
		Asset:          st.Asset,
		Movement:       st.Movement,
		ClosingBalance: st.ClosingBalance,
		Reference:      st.Reference,
		ValueDate:      ledger.DayStart(messageNow),
	}
	if moves[0] != want {
		t.Errorf("movement round-tripped as %+v, want %+v", moves[0], want)
	}
}

// TestStatementMessageCarriesTheDirectionInWordsAndTheAmountAsAMagnitude pins
// the one place a sign could be lost.
//
// ActiveCurrencyAndAmount cannot be negative — NewAmount refuses one — so a net
// PAYER's movement travels as a positive amount with CdtDbtInd = DBIT, and the
// sign is reconstructed on the way in. A builder that emitted the magnitude and
// dropped the indicator would produce a statement that reads as a credit, which
// would make a bank post its mirror leg BACKWARDS: reserve up when it went down.
func TestStatementMessageCarriesTheDirectionInWordsAndTheAmountAsAMagnitude(t *testing.T) {
	st := SettlementStatement{
		Agent: "BRVODEFFXXX", Account: "acc_1", Asset: "EUR",
		Reference: "cyc_1", StatementRef: "set_1",
		Movement: -250000, ClosingBalance: -1, ValueDate: messageNow,
	}
	env, err := StatementMessage(st, MessageContext{
		From: "CBSEDEFFXXX", To: st.Agent, MsgID: "msg_7", Now: messageNow,
	})
	if err != nil {
		t.Fatalf("StatementMessage: %v", err)
	}
	doc := env.Document.(*iso20022.Camt053)
	entry := doc.BkToCstmrStmt.Stmt[0].Ntry[0]
	if got, want := entry.CdtDbtInd, iso20022.CreditDebitDebit; got != want {
		t.Errorf("entry indicator = %q, want %q", got, want)
	}
	if got := entry.Amt.Value; got != "2500.00" {
		t.Errorf("entry amount = %q, want the magnitude 2500.00", got)
	}
	if got, want := doc.BkToCstmrStmt.Stmt[0].Bal[0].CdtDbtInd, iso20022.CreditDebitDebit; got != want {
		t.Errorf("balance indicator = %q, want %q — a negative reserve is a DEBIT balance", got, want)
	}

	moves, err := ReadStatement(doc)
	if err != nil {
		t.Fatalf("ReadStatement: %v", err)
	}
	if moves[0].Movement != -250000 {
		t.Errorf("movement read back as %d, want -250000", moves[0].Movement)
	}
	if moves[0].ClosingBalance != -1 {
		t.Errorf("closing balance read back as %d, want -1", moves[0].ClosingBalance)
	}
}
