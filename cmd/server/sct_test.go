package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// The happy path, and the shape of every test in this package: submit, drain,
// assert. No sleeps, no Eventually, no retry — if any of those is needed, the
// Drain design has failed and that is the bug.
func TestCreditTransferReachesAcceptedThroughTheCSM(t *testing.T) {
	h := newHarness(t)

	p := h.submitCreditTransfer(t)
	if p.Status != payment.Initiated {
		t.Fatalf("submit returned %v, want Initiated — the creditor's bank has not answered yet", p.Status)
	}

	h.work(t)

	got := h.payment(t, p.ID)
	if got.Status != payment.Accepted {
		t.Fatalf("after draining, status = %v, want Accepted", got.Status)
	}
	if got.CycleID == "" {
		t.Error("accepted payment is in no cycle; the CSM adds it on ACCP")
	}
}

// The chain is ten files over one business day, and naming them in order is the
// point of the package. Nothing here is a function call across a boundary.
func TestTheCreditTransferChainIsTenFiles(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.day(t)

	want := []struct {
		from, to iso20022.BIC
		msgDef   string
	}{
		// The day opens with each member collecting the routing table, which is the
		// one file here that carries no message at all.
		{h.cfg.ClearingHouseBIC, h.debtorBIC, notAMessage},
		{h.cfg.ClearingHouseBIC, h.creditorBIC, notAMessage},

		{h.debtorBIC, h.cfg.ClearingHouseBIC, "pacs.008.001.08"},
		{h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC, "pacs.009.001.08"},
		{h.cfg.CentralBankBIC, h.cfg.ClearingHouseBIC, "pacs.002.001.10"},
		{h.cfg.CentralBankBIC, h.debtorBIC, "camt.053.001.08"},
		{h.cfg.ClearingHouseBIC, h.debtorBIC, "pacs.002.001.10"},
		{h.cfg.ClearingHouseBIC, h.debtorBIC, "pacs.002.001.10"},
		{h.cfg.CentralBankBIC, h.creditorBIC, "camt.053.001.08"},
		{h.cfg.ClearingHouseBIC, h.creditorBIC, "pacs.008.001.08"},
	}
	h.mu.Lock()
	seen := append([]tappedMessage(nil), h.seen...)
	h.mu.Unlock()

	if len(seen) != len(want) {
		t.Fatalf("the network carried %d files, want %d", len(seen), len(want))
	}
	for i, w := range want {
		if got := msgDefOf(seen[i].raw); seen[i].from != w.from || seen[i].to != w.to || got != w.msgDef {
			t.Errorf("hop %d is %s -> %s (%q), want %s -> %s (%q)",
				i, seen[i].from, seen[i].to, got, w.from, w.to, w.msgDef)
		}
	}
}

// The payee's bank is handed nothing at all until the cycle carrying the
// payment has settled, and this is the assertion that says so on its own.
func TestNothingReachesThePayeesBankBeforeTheCycleSettles(t *testing.T) {
	h := newHarness(t)
	p := h.submitCreditTransfer(t)
	h.work(t)

	if files := h.filesOfTypeTo(t, h.creditorBIC, "pacs.008.001.08"); len(files) != 0 {
		t.Fatalf("the payee's bank was handed %d instruction files before settlement, want 0", len(files))
	}
	if _, err := h.bank(h.creditorBIC).GetPayment(context.Background(), p.ID); err == nil {
		t.Error("the payee's bank holds a copy of a payment that has not settled")
	}
	// And the payer's bank HAS been told, which is what says the file was taken
	// in rather than lost: the clearing house validated it and put it in a cycle.
	if got := h.bankPayment(t, h.debtorBIC, p.ID); got.Status != payment.Accepted {
		t.Errorf("the payer's bank records %v before the cut-off, want Accepted", got.Status)
	}
}

// A payee's bank that cannot find the payee RETURNS the payment, and this is
// the single largest consequence of settling before releasing.
func TestCreditTransferToAnUnknownAccountComesBackAsAnAC01Return(t *testing.T) {
	h := newHarness(t)
	p := h.submitCreditTransferTo(t, unknownIBANAt(h.creditor))

	h.work(t)
	h.closeCycle(t)
	// The cut-off releases the file, and the payee's bank asks for the payment
	// back in the same phase it discovers it cannot apply it.
	h.work(t)
	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Fatalf("status = %v after the cut-off, want Settled — a payment the payee's bank cannot apply still settles", got.Status)
	}
	if got := h.bankPayment(t, h.creditorBIC, p.ID); got.Status != payment.Settled {
		t.Fatalf("the payee's bank records %v; it holds money it cannot pay out", got.Status)
	}
	returns := h.filesOfTypeTo(t, h.cfg.ClearingHouseBIC, "pacs.004.001.09")
	if len(returns) != 1 {
		t.Fatalf("the payee's bank uploaded %d returns, want 1", len(returns))
	}

	// The return's own trip: up to the settlement agent, back down to both banks.
	h.work(t)
	if got := h.payment(t, p.ID); got.Status != payment.Returned {
		t.Fatalf("status = %v after the return, want Returned", got.Status)
	}

	// The payer has their money back and neither bank is holding any of it.
	if bal := h.balance(t, h.debtorPID, h.debtorAcct.ID); bal != harnessFunding {
		t.Errorf("the payer's balance = %d, want %d — the return did not reach them", bal, harnessFunding)
	}
	for _, pid := range []payment.ParticipantID{h.debtorPID, h.creditorPID} {
		if bal := h.suspense(t, pid); bal != 0 {
			t.Errorf("%s's clearing suspense = %d after the return, want 0", pid, bal)
		}
	}
}

// The return carries AC01 and it reaches the payer's bank as a MESSAGE, which is
// what makes the refund happen. A reason invented at the far end would be a
// second rendering of one intent; both banks read this one off the same bytes.
func TestTheReturnOfAnUnpayableTransferCarriesAC01(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransferTo(t, unknownIBANAt(h.creditor))
	h.work(t)
	h.closeCycle(t)
	h.work(t)

	returns := h.filesOfTypeTo(t, h.cfg.ClearingHouseBIC, "pacs.004.001.09")
	if len(returns) != 1 {
		t.Fatalf("the payee's bank uploaded %d returns, want 1", len(returns))
	}
	rsn := returns[0].Document.(*iso20022.Pacs004).PmtRtr.TxInf[0].RtrRsnInf
	if rsn == nil || rsn.Rsn.Cd == nil || *rsn.Rsn.Cd != iso20022.ReturnReasonIncorrectAccountNumber {
		t.Fatalf("the return carries %v, want AC01", rsn)
	}
}

// The rejection reaches the payer's bank as a message, not as a return value.
// It is the pacs.002 that makes the money come back, so the payer's bank has to
// receive one carrying the code.
func TestARejectedCreditTransferIsAnsweredToThePayersBank(t *testing.T) {
	h := newHarnessWithNoOpenCycle(t)
	h.submitCreditTransfer(t)
	h.work(t)

	h.assertLastStatusTo(t, h.debtorBIC, iso20022.StatusReasonInvalidCutOffTime)
}

func TestCreditTransferWithNoOpenCycleIsTM01(t *testing.T) {
	h := newHarnessWithNoOpenCycle(t)
	p := h.submitCreditTransfer(t)
	h.work(t)

	got := h.payment(t, p.ID)
	if got.RejectCode != iso20022.StatusReasonInvalidCutOffTime {
		t.Errorf("reject code = %q, want TM01 — no cycle open IS an invalid cut-off time", got.RejectCode)
	}
	// The refusal is the clearing house's, and it comes after the payee's bank
	// has already said yes: no bank refuses its own customer's instruction
	// because the CSM has no window open. The payer still gets their money back.
	if bal := h.suspense(t, h.debtorPID); bal != 0 {
		t.Errorf("clearing suspense = %d after a TM01, want 0", bal)
	}
}

// FF01 must be reachable AND answerable. An unparseable envelope hides its own
// header, but the queue carries the sender beside the bytes, so the receiver
// can still reply.
func TestAMalformedEnvelopeIsAnsweredWithFF01(t *testing.T) {
	h := newHarness(t)
	h.injectRaw(t, h.debtorBIC, h.cfg.ClearingHouseBIC, []byte("<Envelope><nonsense/>"))
	h.work(t)
	// The CSM could not parse it, so there is no payment to reject; what it
	// can do is tell the sender the file was invalid. Assert the debtor bank
	// received a pacs.002 carrying FF01.
	h.assertLastStatusTo(t, h.debtorBIC, iso20022.StatusReasonInvalidFileFormat)
}

// Submit runs the submitting bank's half synchronously, so a submission that
// fails inside its unit of work fails at the caller — and, because the send
// happens after the commit, it sends nothing.
func TestARolledBackSubmitSendsNothing(t *testing.T) {
	h := newHarness(t)
	// A submission that fails inside its unit of work — an amount of zero.
	req := h.creditTransferRequest(t)
	req.Amount = 0
	if _, err := h.dep.Submit(context.Background(), req); err == nil {
		t.Fatal("Submit accepted a zero amount")
	}
	h.work(t)
	if got := h.messagesSeen(); got != 0 {
		t.Fatalf("a rolled-back submission sent %d messages, want 0", got)
	}
}

// A message type an institution has no handler for is REPORTED and not shrugged
// off.
func TestAFileAnInstitutionHasNoHandlerForIsReported(t *testing.T) {
	h := newHarness(t)
	env, err := payment.SettlementMessage(
		[]payment.SettlementLeg{{
			From: h.debtorBIC, To: h.cfg.CentralBankBIC,
			Amount: harnessAmount, Asset: "EUR", Reference: "cyc_never",
		}},
		payment.MessageContext{From: h.cfg.ClearingHouseBIC, To: h.debtorBIC, MsgID: "stl-1", Now: testTime})
	if err != nil {
		t.Fatalf("SettlementMessage: %v", err)
	}
	h.upload(t, h.cfg.ClearingHouseBIC, h.debtorBIC, env)

	err = h.workErr(t)
	if err == nil {
		t.Fatal("the day was clean; the bank swallowed a message it has no handler for")
	}
	if !strings.Contains(err.Error(), "pacs.009") {
		t.Errorf("the reported problem %q does not name the message the bank could not handle", err)
	}
}

// A status report about a payment whose payer banks elsewhere is refused.
func TestABankRefusesAStatusAboutAnotherBanksPayment(t *testing.T) {
	h := newHarnessWithNoOpenCycle(t)
	p := h.submitCreditTransfer(t)
	h.work(t)
	if got := h.payment(t, p.ID); got.Status != payment.Rejected {
		t.Fatalf("the fixture payment is %v, want Rejected", got.Status)
	}
	h.rec.reset()

	// The same rejection the clearing house sent the payer's bank, sent to the
	// payee's bank instead.
	env, err := payment.StatusMessage(
		payment.OriginalMessage{MsgID: "orig-1", MsgDefIdr: "pacs.008.001.08"},
		[]payment.TransactionStatusReport{{
			TxID:   string(p.ID),
			Status: iso20022.TransactionStatusRejected,
			Code:   iso20022.StatusReasonIncorrectAccountNumber,
			Text:   "misrouted",
		}},
		payment.MessageContext{From: h.cfg.ClearingHouseBIC, To: h.creditorBIC, MsgID: "sts-x", Now: testTime})
	if err != nil {
		t.Fatalf("StatusMessage: %v", err)
	}
	h.upload(t, h.cfg.ClearingHouseBIC, h.creditorBIC, env)

	err = h.workErr(t)
	if err == nil {
		t.Fatal("the day was clean; the payee's bank acted on a rejection of somebody else's customer's debit")
	}
	// Named, and not merely non-nil.
	if !strings.Contains(err.Error(), string(h.creditorBIC)) || !strings.Contains(err.Error(), string(h.debtorBIC)) {
		t.Errorf("the reported problem %q does not say which bank refused it, and whose payment it was about", err)
	}
	// And it reached nobody's ledger while refusing.
	assertBooksTouched(t, "the payee's bank, refusing a misrouted rejection",
		h.booksTouchedBy(h.creditorBIC), nil)
}

// A bank reverses a debit once, and never after the money has moved between
// banks. Those are the two refusals it still has, and this measures the second.
func TestABankRefusesToReverseAPaymentThatIsNotRejected(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)
	h.rec.reset()

	env, err := payment.StatusMessage(
		payment.OriginalMessage{MsgID: "orig-1", MsgDefIdr: "pacs.008.001.08"},
		[]payment.TransactionStatusReport{{
			TxID:   string(p.ID),
			Status: iso20022.TransactionStatusRejected,
			Code:   iso20022.StatusReasonIncorrectAccountNumber,
			Text:   "a decision that arrived after finality",
		}},
		payment.MessageContext{From: h.cfg.ClearingHouseBIC, To: h.debtorBIC, MsgID: "sts-y", Now: testTime})
	if err != nil {
		t.Fatalf("StatusMessage: %v", err)
	}
	h.upload(t, h.cfg.ClearingHouseBIC, h.debtorBIC, env)

	err = h.workErr(t)
	if err == nil {
		t.Fatal("the day was clean; the payer's bank reversed the debit of a payment that has settled")
	}
	// Named, and not merely non-nil. The refusal has to be the STATE one and not
	// the ledger's own refusal to reverse a transaction twice, which would mean
	// the bank had already reached its customer's account to find out.
	if !strings.Contains(err.Error(), "records as Settled") || !strings.Contains(err.Error(), string(h.debtorBIC)) {
		t.Errorf("the reported problem %q is not the payer's bank refusing to reverse a settled payment", err)
	}
	if bal := h.balance(t, h.debtorPID, h.debtorAcct.ID); bal != harnessFunding-harnessAmount {
		t.Errorf("payer's balance = %d, want %d — the settled debit was reversed anyway",
			bal, harnessFunding-harnessAmount)
	}
	assertBooksTouched(t, "the payer's bank, refusing a rejection that arrived after finality",
		h.booksTouchedBy(h.debtorBIC), nil)
}

// A member bank's pacs.002 is REPORTED at the clearing house and acted on by
// nothing, and what it costs is money nobody can move.
func TestAFileAPayeesBankCannotReadIsReportedAtTheClearingHouse(t *testing.T) {
	h := newHarness(t)
	h.injectRaw(t, h.cfg.ClearingHouseBIC, h.creditorBIC, []byte("<Envelope><nonsense/>"))
	// The bank answers FF01 up its own connection, which is the whole of what it
	// can do; the clearing house works through it on the next pass.
	h.work(t)

	err := h.workErr(t)
	if err == nil || !strings.Contains(err.Error(), "FF01") {
		t.Fatalf("the day reported %v, want the payee's bank's FF01 recorded as a problem", err)
	}
	if !strings.Contains(err.Error(), string(h.creditorBIC)) {
		t.Errorf("the report %q does not name the bank that could not read the file", err)
	}
}

// A payment addressed to a bank the clearing house cannot route to is RC01, and
// it is the clearing house that says so — the only party that holds the routing
// table.
func TestACreditTransferForABankTheClearingHouseCannotRouteToIsRC01(t *testing.T) {
	h := newHarness(t)
	p := h.submitCreditTransfer(t)
	h.work(t)

	env, err := h.net.CreditTransferMessage([]payment.Payment{p},
		payment.MessageContext{From: h.debtorBIC, To: h.cfg.ClearingHouseBIC, MsgID: "ct-x", Now: testTime})
	if err != nil {
		t.Fatalf("CreditTransferMessage: %v", err)
	}
	const forged = payment.PaymentID("pay_AURODEFFXXX_9001")
	tx := &env.Document.(*iso20022.Pacs008).FIToFICstmrCdtTrf.CdtTrfTxInf[0]
	tx.PmtId.TxId = string(forged)
	// A BIC no actor answers to.
	tx.CdtrAgt = iso20022.BranchAndFinancialInstitution{
		FinInstnId: iso20022.FinancialInstitutionIdentification{BICFI: "NOSUCHFFXXX"}}
	h.upload(t, h.debtorBIC, h.cfg.ClearingHouseBIC, env)
	err = h.workErr(t)
	if err == nil {
		t.Fatal("the day was clean; the payer's bank acted on a rejection of a payment it never sent")
	}
	if !strings.Contains(err.Error(), string(forged)) || !strings.Contains(err.Error(), string(h.debtorBIC)) {
		t.Errorf("the reported problem %q is not the payer's bank refusing a rejection of a payment it holds no row for", err)
	}

	h.assertLastStatusTo(t, h.debtorBIC, iso20022.StatusReasonBankIdentifierIncorrect)
	// The clearing house's own copy says what its message said.
	if got := h.payment(t, forged); got.Status != payment.Rejected {
		t.Errorf("the clearing house records the unroutable payment as %v; it answered RC01 about it", got.Status)
	}
	// And the real payment, whose id the forgery is derived from, is untouched.
	if got := h.payment(t, p.ID); got.Status != payment.Accepted {
		t.Errorf("%s is %v after a forged message about another payment, want Accepted", p.ID, got.Status)
	}
	if bal := h.suspense(t, h.debtorPID); bal != harnessAmount {
		t.Errorf("payer's clearing suspense = %d, want %d", bal, harnessAmount)
	}
}

// A second copy of a released output file is REPORTED at the payee's bank, not
// acted on again.
func TestARedeliveredCreditTransferIsReportedAtThePayeesBank(t *testing.T) {
	h := newHarness(t)
	p := h.settledPayment(t)

	// The pacs.008 the clearing house released, sent a second time.
	var relayed []byte
	h.mu.Lock()
	for _, m := range h.seen {
		if m.to == h.creditorBIC {
			relayed = m.raw
		}
	}
	h.mu.Unlock()
	if relayed == nil {
		t.Fatal("the payee's bank was never sent a pacs.008")
	}
	h.injectRaw(t, h.cfg.ClearingHouseBIC, h.creditorBIC, relayed)

	err := h.workErr(t)
	// Matched on the TEXT and not with errors.Is: a day's report is prose an
	// operator reads, and Problem.Detail does not wrap the sentinel.
	if err == nil || !strings.Contains(err.Error(), payment.ErrInvalidStateTransition.Error()) {
		t.Fatalf("the day reported %v, want the illegal transition as a problem", err)
	}
	if got := h.bankPayment(t, h.creditorBIC, p.ID); got.Status != payment.Settled {
		t.Errorf("the redelivery moved the payee's bank's copy to %v; it was already Settled", got.Status)
	}
	// And the payee was paid once. A redelivery answered rather than reported
	// would have credited them twice or sent the money back.
	if bal := h.balance(t, h.creditorPID, h.creditorAcct.ID); bal != harnessAmount {
		t.Errorf("the payee's balance = %d, want %d", bal, harnessAmount)
	}
}

// TestAnOnUsPaymentIsRefusedBeforeItReachesAClearingHouse is the boundary this
// this transport does not have, and the thing it refuses is not an error in the
// message.
func TestAnOnUsPaymentIsRefusedBeforeItReachesAClearingHouse(t *testing.T) {
	// Both directions, because the submitting bank differs — a push is submitted
	// by the payer's bank and a collection by the payee's — and on-us is the one
	// arrangement in which those are the same institution.
	for _, tc := range []struct {
		name   string
		scheme payment.SchemeID
	}{
		{"a credit transfer", payment.SchemeSEPACT},
		{"a collection", payment.SchemeSEPADD},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			// A second customer at the PAYER's bank, so both parties are that
			// bank's.
			other := h.openCustomer(t, h.debtor, "Carla", "EUR", 0)
			otherRef := payment.PartyRef{Account: other.ID, Identifier: other.Identifiers[0]}
			// A mandate, so that a collection is refused for being on-us and not for
			// being unauthorised. Without it SDD.ValidateMandate would refuse first and
			// this test would pass on a network with no boundary at all.
			mandate, err := h.bank(h.debtorBIC).CreateMandate(ctx, h.debtorBIC, h.debtorRef(), otherRef, 0)
			if err != nil {
				t.Fatalf("CreateMandate: %v", err)
			}

			before := h.messagesSeen()
			_, err = h.dep.Submit(ctx, payment.InitiatePaymentRequest{
				Scheme:          tc.scheme,
				MandateID:       mandate.ID,
				Debtor:          h.debtorRef(),
				Creditor:        otherRef,
				Amount:          harnessAmount,
				Description:     "one bank, both customers",
				CreditorDetails: payment.PartyDetails{Agent: h.debtorBIC, Name: other.Name},
				DebtorDetails:   payment.PartyDetails{Agent: h.debtorBIC, Name: h.debtorAcct.Name},
			})
			if !errors.Is(err, payment.ErrOnUsPayment) {
				t.Fatalf("Submit = %v, want it refused as an on-us payment", err)
			}
			// The refusal names the bank, because a caller holding several
			// participants has to be told which one is both ends.
			if !strings.Contains(err.Error(), string(h.debtorPID)) {
				t.Errorf("the refusal reads %q and does not name the bank that is both parties", err)
			}

			// Refused BEFORE anything happened, not unwound afterwards: no payment row,
			// no debit, no message. The submitting bank's half runs synchronously inside
			// Submit, so a guard placed after it would have left a row behind.
			payments, err := h.net.ListPayments(ctx)
			if err != nil {
				t.Fatalf("ListPayments: %v", err)
			}
			if len(payments) != 0 {
				t.Errorf("the refused on-us submission left %d payments behind, want none", len(payments))
			}
			if got := h.balance(t, h.debtorPID, h.debtorAcct.ID); got != harnessFunding {
				t.Errorf("the payer holds %d after the refused submission, want the %d they started with", got, harnessFunding)
			}
			if got := len(h.messagesFrom(before)); got != 0 {
				t.Errorf("the refused on-us submission put %d messages on the wire, want none", got)
			}
		})
	}
}
