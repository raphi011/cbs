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

// The chain is eight files over one business day, and naming them in order is
// the point of the package. Nothing here is a function call across a boundary.
//
// The order is the whole of what task 8 is about. Read the middle four: the
// clearing house instructs settlement, the settlement agent discharges it and
// advises both members — and only THEN does the payee's bank receive the
// instruction it is to act on. The money is final before the instruction to
// apply it exists at the bank that will apply it, which is what makes a receiving
// bank unable to credit a customer against a cut-off that might still fail.
//
// It is asserted as a count and a route rather than as an exact byte sequence,
// because the identifiers each hop assigns are the sender's own and pinning them
// would be pinning the id scheme, not the flow.
//
// Two pacs.002 reach the payer's bank and they say different things: the first
// is the clearing house taking the payment into a cycle, the second is that
// cycle settling. Both are collected in the same download, which is what a
// deployment running one cut-off a day looks like from the bank's end.
func TestTheCreditTransferChainIsEightFiles(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.day(t)

	want := []struct {
		from, to iso20022.BIC
		msgDef   string
	}{
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
		env, err := iso20022.Unmarshal(seen[i].raw)
		if err != nil {
			t.Fatalf("message %d does not parse: %v", i, err)
		}
		if seen[i].from != w.from || seen[i].to != w.to || env.AppHdr.MsgDefIdr != w.msgDef {
			t.Errorf("hop %d is %s -> %s (%s), want %s -> %s (%s)",
				i, seen[i].from, seen[i].to, env.AppHdr.MsgDefIdr, w.from, w.to, w.msgDef)
		}
	}
}

// The payee's bank is handed nothing at all until the cycle carrying the payment
// has settled, and this is the assertion that says so on its own.
//
// It is the negative half of the chain above and worth its own test, because the
// chain measures an ORDER and this measures an ABSENCE: run every phase that
// carries a file, stop short of the cut-off, and the bank that is to be paid has
// still never heard of the payment.
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
//
// The same instruction was a REJECT while the clearing house relayed before the
// cut-off: the payee's bank looked at it first, said AC01, and no money had
// moved. Now it is handed the file only after finality, so the money is already
// in its clearing suspense and the only honest answer is a pacs.004 that sends
// it back. That is what a real SEPA bank does, and it is why AC01 is a member of
// the return code set as well as of the rejection one.
//
// Three days' worth of phases, because a return is a conversation: the first
// carries and settles the payment, the second is where the payee's bank finds
// out and asks for it back, and the third is where the settlement agent reverses
// the reserves and the payer's bank pays its customer.
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
//
// The refusal under test is the CLEARING HOUSE's, because after task 8 that is
// the only institution that rejects anything: a bank's own refusal comes too
// late to be one and is a return instead. TM01 is the cheapest of them to build
// — a scheme with no cut-off window open — and what it measures is the message,
// not the code.
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

// A message type an institution has no handler for is REPORTED and not shrugged off.
//
// A pacs.004 is not usable as the example: it travels to a bank after finality
// and that bank posts its own leg from it, so it is a message every bank in this
// system has a handler for.
//
// A pacs.009 takes its place, and it is the same kind of thing rather than a
// substitute chosen for convenience. A settlement instruction is addressed to
// the SETTLEMENT AGENT and to nobody else: it names net positions between
// members and the central bank, and a member bank has nothing it could do with
// one. So one arriving at a bank is a bug in whoever sent it, and swallowing it
// would make a half this system does not have look like one it does.
//
// Every message definition this system's actors emit now has an arm at a member
// bank except this one, which is why it is the last example available. A future
// task that gives banks a pacs.009 arm has to find another — or conclude that
// this assertion has run out of subject and say so.
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
//
// ReverseDebtorLegTx posts in the book of the payment's OWN debtor bank, whoever
// runs it — so a bank that acted on a misrouted rejection would reverse a debit
// in another bank's ledger. Nothing in the flow produces one, because the
// clearing house addresses a status to the payment's debtor bank and no other;
// this is the guard that makes that a property of the receiver too, rather than
// only of the router.
//
// The payment it names really is Rejected, so the other guard in that handler —
// a bank reverses only what this network's record calls rejected — cannot be
// what refuses it. See TestABankRefusesToReverseAPaymentThatIsNotRejected.
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
	// Named, and not merely non-nil. Without the guard the reversal is attempted
	// and fails on the ledger's own refusal to reverse a transaction twice, so a
	// test that asked only whether the day reported anything would pass on a handler
	// that had already reached into the payer's book to find out.
	if !strings.Contains(err.Error(), string(h.creditorBIC)) || !strings.Contains(err.Error(), string(h.debtorBIC)) {
		t.Errorf("the reported problem %q does not say which bank refused it, and whose payment it was about", err)
	}
	// And it reached nobody's ledger while refusing.
	assertBooksTouched(t, "the payee's bank, refusing a misrouted rejection",
		h.booksTouchedBy(h.creditorBIC), nil)
}

// A bank reverses a debit once, and never after the money has moved between
// banks. Those are the two refusals it still has, and this measures the second.
//
// ReverseDebtorLegTx does not load the payment and does not look at its status —
// its own doc says so, and says the caller establishes the decision. The caller
// is payment.RejectAtBankTx now, and what it establishes the decision against is
// THIS BANK's own copy: a rejection of a payment already recorded as Settled or
// Rejected is refused, because the first would take back a debit that has
// already funded a reserve movement and the second would reverse a leg that is
// already reversed.
//
// # A forged rejection cannot be told from a real one
//
// Each institution holds a copy nobody else can write, so "does this network
// call it rejected" is not a question a bank can ask — the decider's own record
// is in another database. A genuine post-acceptance rejection and a forged one
// are the same bytes from the same address about a payment in the same state.
//
// Refusing on Accepted was tried and is wrong on the domain as well as on the
// data. A rejection is a pre-SETTLEMENT act, not a pre-acceptance one: a cut-off
// that finds a participant short of its position rejects payments that have been
// in a cycle for hours, and a bank that answered "I was already told ACCP" would
// be refusing the ordinary case. See payment.Network.transition.
//
// What replaces it is not here, because it cannot be: a real network answers
// forgery with the CHANNEL — an authenticated message, non-repudiable, on a
// closed network — and with reconciliation against the cycle reports afterwards.
// What this system CAN do it does one hop earlier, at the institution that would
// have to be lying: the clearing house never sends an RJCT about a payment it has
// not rejected. See ClearingHouse.refuse, which rejects its own copy in the same
// breath as the code it answers with.
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
//
// It is the only status a member uploads now: a receiving bank makes no
// judgement about an instruction, because the instruction reaches it after the
// cycle carrying it is final. So the one thing left for it to say is that a file
// would not parse — and by then the payments inside that file have settled, the
// money is in that bank's clearing suspense, and it can neither apply them nor
// name them to return them.
//
// There is nothing for the clearing house to do about it, which is exactly why
// it is a line in the day's report: the failure is real, it is unrecoverable
// from inside the flow, and payment/recon is what makes the stranded suspense
// visible afterwards.
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

// A payment addressed to a bank the clearing house cannot route to is RC01, and it is the
// clearing house that says so — the only party that holds the routing table.
//
// # The message names a payment nobody has seen, and it has to
//
// A doctored copy of a status the payer's bank really sent, id and all, would
// make the RC01 an answer about a payment already ACCEPTED and sitting in an open
// cycle — the shape of defect the clearing house refuses to produce, because a
// bank acting on such a status reverses a debit that is funding a settlement and
// no state its own copy could be in would let it tell that message from a
// rejection this network really made (payment.RejectAtBankTx). The replay is
// refused as a DUPLICATE before anything goes on the wire — see
// the clearing house's own record — so keeping the original id would measure that refusal
// instead of the routing table.
//
// So the transaction id is doctored too, and the file is then what a forged
// instruction is: a well-formed pacs.008 for a payment no institution holds. The
// clearing house records it, cannot route it, answers RC01, and marks ITS OWN
// copy rejected — which is the property this test gained, because a clearing
// house whose row said Initiated while its message said RJCT is the same
// inconsistency one layer up.
//
// The payer's bank then REPORTS the answer, and that is correct rather than
// incidental: it is being told about a payment it never submitted and holds no
// row for. It is asserted rather than discarded so that a failure anywhere else
// in the chain cannot hide underneath the assertions below.
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
//
// The redelivery arrives after the payment has settled and been applied, so
// AcceptInboundTx refuses it with ErrInvalidStateTransition — a sentinel
// payment's reasonTable classifies with the EMPTY code precisely because it
// describes a defect in this system rather than a judgement about the sender's
// instruction. payment.Answerable is what keeps it out of a pacs.004: a bank
// that RETURNED a redelivery would send back, across the network, a payment it
// had in fact applied.
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
// this transport does not have, and the thing it refuses is not an error in the message.
//
// # Why an on-us payment is not a clearing payment
//
// Both customers bank at the same institution, so paying one from the other is
// that bank moving money between two of its own deposit accounts. Nothing leaves
// it. No reserves move, because a reserve is what one bank owes another through
// the central bank and this bank owes itself nothing; there is nothing for a
// clearing house to net, nothing for a settlement agent to settle, and no
// camt.053 that could tell a bank about its own book. A real scheme never sees
// one: the payer's bank recognises the beneficiary as its own and books the
// transfer internally.
//
// Each of the three institutions does something incoherent with an on-us
// payment. The clearing house nets a position of zero and drops the payment out
// of the settlement instruction, so a cycle holding nothing but an on-us payment
// strands at Cleared for ever. The settlement agent's return path sends the SAME
// bank two camt.053s about the SAME account under the same reference, differing
// only in sign, and the second is swallowed by the advice row the first wrote —
// so that bank's reserve mirror moves by the full amount while the central
// bank's record of it does not, and its clearing suspense goes permanently
// negative. The returning bank is both parties, so it holds both legs and
// refuses its own customer's unconditional refund.
//
// Every one of those is a symptom of a payment that should never have been
// submitted to clearing, which is why the refusal is here — at the one door
// every submission comes through — and not three patches further in.
//
// # What it does NOT refuse
//
// A book transfer between two customers of one bank is a real product and this
// system offers it — deposit.Register.TransferTx, which is the register's own
// act and reaches no transport at all. The refusal is a statement about the wrong
// ROUTE and not about the payment being illegitimate, so a caller reading it
// knows which of the two to ask for instead.
func TestAnOnUsPaymentIsRefusedBeforeItReachesAClearingHouse(t *testing.T) {
	// Both directions, because the submitting bank differs — a push is submitted
	// by the payer's bank and a collection by the payee's — and on-us is the one
	// arrangement in which those are the same institution. A guard that read the
	// submitter rather than the two parties would pass one of these.
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
			// A mandate, so that a collection is refused for being on-us and not
			// for being unauthorised. Without it SDD.ValidateMandate would refuse
			// first and this test would pass on a network with no boundary at all.
			// Both parties are the payer's bank's, so it is also the creditor's
			// bank and the one that records the mandate.
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

			// Refused BEFORE anything happened, not unwound afterwards: no
			// payment row, no debit, no message. The submitting bank's half runs
			// synchronously inside Submit, so a guard placed after it would have
			// left a row behind.
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
