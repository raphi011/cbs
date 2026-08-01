// Package payment_test's half of the translator's tests: the ones that need a
// whole network.
//
// They are separated from translate_test.go, which tests the same file, for a
// reason that is mechanical and worth stating rather than rediscovering. These
// build a Network, which means a store, which means store/testenv — and
// store/mem imports payment, so an in-package test file importing it back is an
// import cycle. translate_test.go's reason-table tests need reasonTable and
// reasonFor, which are unexported and are meant to stay that way. Neither half
// can move to the other's package, and a test-only export to bridge them would
// be API surface outliving the reason for it. Go allows both packages in one
// directory; this is what that facility is for.
package payment_test

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	. "github.com/raphi011/cbs/payment"
)

// messageNow is the instant the message tests build their headers at.
//
// It is deliberately NOT the network's clock (fixedTime, 2025-01-15): a
// message is created when it is sent, not when the payment it carries was
// booked, and a fixture that used one instant for both could not tell a
// translator that confused the two apart from one that did not.
var messageNow = time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

// addressedBanks builds the two banks these tests address each other by: real,
// distinct BICs, and one customer account at each carrying a real IBAN.
//
// It reuses system_test.go's harness — testNetwork, openCustomer, fundAccount,
// assertNoError — rather than standing up a second one. What it does NOT reuse
// is setupTwoBanks, and for a reason that is the whole subject here: that
// fixture gives both banks the same placeholder BIC and addresses its accounts
// by readable identifiers like SE89-BANKA-0001. A test asserting which bank and
// which address reached the wire cannot use a fixture where both banks are the
// same bank.
func addressedBanks(t *testing.T) (sys *Network, aurora, verde *Participant, alice, bruno deposit.Account) {
	t.Helper()
	ctx := context.Background()
	sys = testNetwork(t)

	aurora, err := sys.AddParticipant(ctx, "Aurora Bank", "AURODEFFXXX", euroOnly)
	assertNoError(t, err)
	verde, err = sys.AddParticipant(ctx, "Banca Verde", "VERDITMMXXX", euroOnly)
	assertNoError(t, err)

	alice = openCustomer(t, ctx, aurora, "Aurora Customer", "DE89370400440532013000")
	bruno = openCustomer(t, ctx, verde, "Verde Customer", "IT60X0542811101000000123456")
	fundAccount(t, ctx, sys, aurora, alice, 500000)
	return sys, aurora, verde, alice, bruno
}

// networkWithOnePayment is the credit-transfer fixture: the two addressed banks
// and one accepted SEPA credit transfer from Aurora's customer to Verde's.
func networkWithOnePayment(t *testing.T) (*Network, Payment) {
	t.Helper()
	ctx := context.Background()
	sys, aurora, verde, alice, bruno := addressedBanks(t)

	openCycle(t, ctx, sys, SchemeSEPACT)
	p, err := sys.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme:      SchemeSEPACT,
		Debtor:      PartyRef{Participant: aurora.ID, Account: alice.ID, Identifier: alice.Identifiers[0]},
		Creditor:    PartyRef{Participant: verde.ID, Account: bruno.ID, Identifier: bruno.Identifiers[0]},
		Amount:      250000,
		EndToEndID:  "e2e-1",
		Description: "invoice 42",
	})
	assertNoError(t, err)
	return sys, p
}

// networkWithOneCollection is the direct-debit fixture: the same two banks, a
// mandate Verde's customer holds over Aurora's, and one accepted collection
// under it.
func networkWithOneCollection(t *testing.T) (*Network, Payment, Mandate) {
	t.Helper()
	ctx := context.Background()
	sys, aurora, verde, alice, bruno := addressedBanks(t)

	debtor := PartyRef{Participant: aurora.ID, Account: alice.ID, Identifier: alice.Identifiers[0]}
	creditor := PartyRef{Participant: verde.ID, Account: bruno.ID, Identifier: bruno.Identifiers[0]}
	m, err := sys.CreateMandate(ctx, debtor, creditor, 0)
	assertNoError(t, err)

	openCycle(t, ctx, sys, SchemeSEPADD)
	p, err := sys.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme:      SchemeSEPADD,
		Debtor:      debtor,
		Creditor:    creditor,
		Amount:      120000,
		MandateID:   m.ID,
		EndToEndID:  "e2e-dd-1",
		Description: "electricity, August",
	})
	assertNoError(t, err)
	return sys, p, m
}

func TestCreditTransferMessageCarriesBothAgentsAndBothIBANs(t *testing.T) {
	n, p := networkWithOnePayment(t)
	ctx := context.Background()

	env, err := n.CreditTransferMessage(ctx, p, MessageContext{
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
	ctx := context.Background()
	p.Creditor.Identifier = deposit.Identifier{}
	if _, err := n.CreditTransferMessage(ctx, p, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: time.Now()}); err == nil {
		t.Fatal("built a pacs.008 for a payment with no creditor address")
	}
}

// An address that is present but not a legal IBAN is refused HERE, by the
// translator, and not later by Marshal. The difference is what the error says:
// ErrUnaddressableAccount names an account this system could not address, which
// is a fact about the payment, where an iso20022 error names an element in a
// document. FuzzTranslate found this one.
func TestCreditTransferMessageRefusesAMalformedIBAN(t *testing.T) {
	n, p := networkWithOnePayment(t)
	ctx := context.Background()
	p.Creditor.Identifier = deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "not an iban"}
	_, err := n.CreditTransferMessage(ctx, p, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: time.Now()})
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
	ctx := context.Background()
	p.EndToEndID = ""
	env, err := n.CreditTransferMessage(ctx, p, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
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
	ctx := context.Background()
	env, err := n.CreditTransferMessage(ctx, p, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
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
	ctx := context.Background()
	p.ValueDate = time.Time{}
	env, err := n.CreditTransferMessage(ctx, p, MessageContext{From: "AURODEFFXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	got := env.Document.(*iso20022.Pacs008).FIToFICstmrCdtTrf.GrpHdr.IntrBkSttlmDt
	if !got.Equal(messageNow) {
		t.Errorf("IntrBkSttlmDt = %s, want the message's creation date %s", got.Time, messageNow)
	}
}

// DtOfSgntr is the mandate's CreatedAt, which is the elision translate.go
// documents: this system creates a mandate at the moment it is authorised, so
// the two are one fact.
//
// The test is what makes that a claim rather than a hope. The network's clock
// is a year and a half behind the message's, so a translator that reached for
// mc.Now — the obvious wrong answer, and a date that would always look
// plausible — fails here.
func TestDirectDebitMessageDatesTheSignatureFromTheMandate(t *testing.T) {
	n, p, m := networkWithOneCollection(t)
	ctx := context.Background()

	env, err := n.DirectDebitMessage(ctx, p, m, MessageContext{
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
	ctx := context.Background()
	env, err := n.DirectDebitMessage(ctx, p, m, MessageContext{From: "VERDITMMXXX", To: "CSMXFRPPXXX", MsgID: "x", Now: messageNow})
	if err != nil {
		t.Fatalf("DirectDebitMessage: %v", err)
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

// NbOfTxs is what the SENDER asserts, not a derivation a receiver would
// recompute — and this is the test that makes the difference visible. Truncate
// the transaction list after the message is built, exactly as a lost packet or
// a half-written file would, and the count still says two. A receiver that
// recomputed it instead of checking it would read this document as a complete
// one-transaction settlement and never notice the missing leg, which is the
// whole reason the element exists.
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

	if got := doc.FICdtTrf.GrpHdr.NbOfTxs; got != "2" {
		t.Fatalf("NbOfTxs = %q after truncation, want the sender's original assertion of 2", got)
	}
	// And the truncated document is still well-formed: nothing in the codec
	// cross-checks the count, so the discrepancy is the RECEIVER's to catch.
	// That is the division of labour the element encodes.
	if _, err := iso20022.Marshal(env); err != nil {
		t.Fatalf("Marshal refused the truncated document: %v", err)
	}
}

// The amount's scale comes from the asset definition, never from a two-decimal
// constant — and the proof is that BITCOIN IS REFUSED.
//
// A translator carrying a hardcoded 2 would render 250000 minor units of BTC as
// "2500.00" and send it: a message a counterparty accepts, describing a sum six
// orders of magnitude off. Taking the scale from ledger.LookupAsset produces
// "0.00250000" instead, which the standard cannot carry at all — its
// ActiveCurrencyAndAmount permits five fraction digits for any currency — so the
// translator refuses to build the message. Refusal is the correct behaviour AND
// the evidence: only a translator that consulted the asset can fail this way.
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

// The other end of the same bound: a euro amount above roughly 9.2e15 minor
// units renders as nineteen significant digits, one past the standard's
// eighteen, and is refused by the translator rather than by a counterparty's
// parser.
func TestSettlementMessageRefusesAnAmountTooLargeForTheStandard(t *testing.T) {
	_, err := SettlementMessage([]SettlementLeg{
		{From: "AURODEFFXXX", To: "VERDITMMXXX", Amount: math.MaxInt64, Asset: "EUR", Reference: "r"},
	}, MessageContext{From: "CSMXFRPPXXX", To: "CBSEDEFFXXX", MsgID: "CSM-1", Now: messageNow})
	if !errors.Is(err, iso20022.ErrAmountFormat) {
		t.Fatalf("SettlementMessage of MaxInt64 = %v, want ErrAmountFormat", err)
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
