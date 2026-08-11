package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// A pull is the same skeleton with the roles swapped, and the assertion that
// matters is where the debtor leg is posted: by the DEBTOR's bank, on receipt,
// not by the submitter.
//
// # What this test can see, and what it cannot
//
// It sees the TIMING, which is half the claim: no leg when the submitting bank
// answers its own customer, a leg once the far side has been round. It cannot
// see WHICH actor posted, and that is not a fixable oversight in the assertion —
// postDebtorLegTx posts into the payment's own debtor bank's book whoever calls
// it, so the ledger looks the same either way, and under a shared store the only
// thing that differs is which goroutine's unit of work did it.
//
// TWO tests are what make the second half falsifiable, and they are named here
// rather than left to be found. Both were watched failing under both of the
// mutations that move the receiving half to the wrong bank — routing the
// submission to the payer's bank, and relaying the pacs.003 by CdtrAgt:
//
//   - TestTheDirectDebitChainIsFourMessages pins that the pacs.003 goes from the
//     payee's bank through the clearing house to the PAYER's bank, so the payer's
//     bank is the only actor that ran the receiving half at all.
//   - TestWhichBooksEachBankReachesInAPull measures, per actor, which books each
//     bank reached across the whole chain, and both mutations fail it — though
//     not by the same assertion, and not for the same reason. See the note on
//     that test for which one catches which, and for a two-phase variant that was
//     tried, was justified by a false claim, and was unsafe as written.
//
// TestDirectDebitAgainstAnUnfundedDebtorIsAM04 is NOT one of them, and the
// reason is recorded here because it reads like one. AM04 is the payer's
// BALANCE, and postDebtorLegTx's funds check reads it out of the payer's bank's
// register whoever runs the receiving half — so under both mutations above the
// payee's bank runs that half, the check still fails on the same account, and
// AM04 still comes back. It was measured passing under each. What it does pin is
// worth having and is a different claim: that the refusal travelled as a message
// rather than as a return value out of Submit.
func TestDirectDebitPostsTheDebtorLegAtTheDebtorsBank(t *testing.T) {
	h := newHarness(t)
	p := h.submitDirectDebit(t)
	if p.DebtorLegTx != "" {
		t.Fatal("the creditor's bank posted a debtor leg; it cannot see that account")
	}

	h.work(t)
	if got := h.payment(t, p.ID); got.Status != payment.Accepted {
		t.Fatalf("status = %v, want Accepted", got.Status)
	}
	// And STILL no leg, which is the half the release order added. The payer's
	// bank has not been handed the collection yet, so nothing in this system has
	// looked at the payer's account: a collection is validated by the clearing
	// house before it settles and executed by the payer's bank after.
	if _, err := h.bank(h.debtorBIC).GetPayment(context.Background(), p.ID); err == nil {
		t.Fatal("the payer's bank holds a copy of a collection that has not settled")
	}

	h.closeCycle(t)
	h.work(t)

	// The leg is read off the DEBTOR's bank's own copy, because that is the only
	// row in the network with a column to hold one. The clearing house's copy has
	// no leg columns at all — it posts nothing and holds no book of accounts — so
	// asking it would report a missing leg for a leg that was posted.
	if got := h.bankPayment(t, h.debtorBIC, p.ID); got.DebtorLegTx == "" {
		t.Error("no debtor leg after the payer's bank was handed the settled collection")
	}
}

// A payer's bank that cannot fund the collection RETURNS it, and this is the
// pull's half of the same rule AC01 states for a push.
//
// It is the clearest case there is for settling before releasing. The payer's
// bank is net-debited at the cut-off whatever its customer's balance turns out
// to be, so by the time it looks at the account the money has left its reserve —
// and AM04 is a shortfall no other institution in the system can see. Refusing
// would be refusing a payment nobody had paid for yet; what it does instead is
// stand in for the payer out of its own pocket and ask for the money back.
func TestDirectDebitAgainstAnUnfundedDebtorComesBackAsAnAM04Return(t *testing.T) {
	h := newHarnessWithAnUnfundedDebtor(t)
	p := h.submitDirectDebit(t)

	h.work(t)
	h.closeCycle(t)
	h.work(t)

	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Fatalf("status = %v after the cut-off, want Settled — a collection the payer cannot fund still settles", got.Status)
	}
	returns := h.filesOfTypeTo(t, h.cfg.ClearingHouseBIC, "pacs.004.001.09")
	if len(returns) != 1 {
		t.Fatalf("the payer's bank uploaded %d returns, want 1", len(returns))
	}
	rsn := returns[0].Document.(*iso20022.Pacs004).PmtRtr.TxInf[0].RtrRsnInf
	if rsn == nil || rsn.Rsn.Cd == nil || *rsn.Rsn.Cd != iso20022.ReturnReasonInsufficientFunds {
		t.Fatalf("the return carries %v, want AM04", rsn)
	}
	// The payer was never debited, which is the whole point of the refusal: the
	// bank that could not collect carried the money itself.
	if bal := h.balance(t, h.debtorPID, h.debtorAcct.ID); bal != 0 {
		t.Errorf("the payer's balance = %d, want 0 — the collection was forced through", bal)
	}

	h.work(t)
	if got := h.payment(t, p.ID); got.Status != payment.Returned {
		t.Fatalf("status = %v after the return, want Returned", got.Status)
	}
	// And the biller gave it back: it was credited at settlement, like every
	// other payee in this network, and the return clawed it back out again.
	if bal := h.balance(t, h.creditorPID, h.creditorAcct.ID); bal != 0 {
		t.Errorf("the biller's balance = %d, want 0 — the return did not claw the money back", bal)
	}
	for _, pid := range []payment.ParticipantID{h.debtorPID, h.creditorPID} {
		if bal := h.suspense(t, pid); bal != 0 {
			t.Errorf("%s's clearing suspense = %d after the return, want 0", pid, bal)
		}
	}
}

// The mandate is refused at the creditor's own bank, synchronously. MD01 never
// reaches the wire in this system, and that is a modelling limit worth stating:
// a real debtor's bank holds mandate records of its own and can reject with it.
func TestARevokedMandateIsRefusedSynchronously(t *testing.T) {
	h := newHarnessWithARevokedMandate(t)
	if _, err := h.dep.Submit(context.Background(), h.directDebitRequest(t)); !errors.Is(err, payment.ErrMandateRevoked) {
		t.Fatalf("Submit = %v, want ErrMandateRevoked", err)
	}
	h.work(t)
	if got := h.messagesSeen(); got != 0 {
		t.Errorf("a refused collection sent %d messages, want 0", got)
	}
}

// The chain is eight files, and it is the one place in this package that answers
// "which bank submits a direct debit" without prose.
//
// Read the first hop against TestTheCreditTransferChainIsEightFiles and the whole
// difference between a push and a pull is there: the payee's bank starts the
// conversation, the payer's bank is handed the collection to execute. Everything
// after that is the same clearing house doing the same job — and, as on a push,
// the instruction reaches the bank that acts on it only after the cycle has
// settled.
func TestTheDirectDebitChainIsEightFiles(t *testing.T) {
	h := newHarness(t)
	h.submitDirectDebit(t)
	h.day(t)

	want := []struct {
		from, to iso20022.BIC
		msgDef   string
	}{
		{h.creditorBIC, h.cfg.ClearingHouseBIC, "pacs.003.001.08"},
		{h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC, "pacs.009.001.08"},
		{h.cfg.CentralBankBIC, h.cfg.ClearingHouseBIC, "pacs.002.001.10"},
		{h.cfg.CentralBankBIC, h.debtorBIC, "camt.053.001.08"},
		{h.cfg.ClearingHouseBIC, h.debtorBIC, "pacs.003.001.08"},
		{h.cfg.CentralBankBIC, h.creditorBIC, "camt.053.001.08"},
		{h.cfg.ClearingHouseBIC, h.creditorBIC, "pacs.002.001.10"},
		{h.cfg.ClearingHouseBIC, h.creditorBIC, "pacs.002.001.10"},
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

// The refusal reaches the bank that SUBMITTED the collection, which for a pull
// is the payee's — not the payer's, as it is for a push.
//
// It is the same rule stated twice over, once per direction: a pacs.002 is the
// answer to an instruction, and it goes back to whoever gave it. TM01 is the
// refusal available, because after task 8 the clearing house is the only
// institution that rejects anything and a scheme with no open cut-off window is
// the cheapest of its refusals to build.
func TestARejectedCollectionIsAnsweredToThePayeesBank(t *testing.T) {
	h := newHarnessWithNoOpenCycle(t)
	h.submitDirectDebit(t)
	h.work(t)

	h.assertLastStatusTo(t, h.creditorBIC, iso20022.StatusReasonInvalidCutOffTime)
	// ONE answer, and the payer's bank is told nothing at all. It has never heard
	// of this collection: nothing is released before a cycle settles, and this one
	// never reached a cycle. Two files crossed — the collection up and the refusal
	// back — and that is the whole of a rejected pull.
	if got := h.messagesSeen(); got != 2 {
		t.Errorf("a refused collection carried %d messages, want 2", got)
	}
}

// A collection the clearing house cannot clear is TM01, and NOBODY is holding
// any money when it says so.
//
// That is the seam settling before releasing closed. The payer's bank used to
// have posted the debtor leg by this point — accepting a collection is what
// posting it means — so a rejection had to reach two banks and one of them had a
// customer to refund. Now the payer's bank is handed the collection only after
// finality, so a refusal before finality finds nothing to give back: the payer's
// money never left, and the only bank that ever heard of the payment is the one
// that asked.
func TestDirectDebitWithNoOpenCycleIsTM01(t *testing.T) {
	h := newHarnessWithNoOpenCycle(t)
	p := h.submitDirectDebit(t)
	h.work(t)

	got := h.payment(t, p.ID)
	if got.RejectCode != iso20022.StatusReasonInvalidCutOffTime {
		t.Errorf("reject code = %q, want TM01 — no cycle open IS an invalid cut-off time", got.RejectCode)
	}
	if _, err := h.bank(h.debtorBIC).GetPayment(context.Background(), p.ID); err == nil {
		t.Error("the payer's bank holds a copy of a collection that was refused before it settled")
	}
	if bal := h.balance(t, h.debtorPID, h.debtorAcct.ID); bal != harnessFunding {
		t.Errorf("the payer's balance = %d, want %d — money moved for a collection nobody cleared", bal, harnessFunding)
	}
	if bal := h.suspense(t, h.debtorPID); bal != 0 {
		t.Errorf("payer's clearing suspense = %d after a TM01, want 0", bal)
	}
	// And the bank that asked was told, with the clearing house's own reason.
	h.assertLastStatusTo(t, h.creditorBIC, iso20022.StatusReasonInvalidCutOffTime)
}

// TestTheRefundIsAttemptedEvenWhenTheSubmitterCannotBeAddressed is about which
// of a rejected collection's two messages the clearing house tries first.
//
// One of them is news and one of them is money. The payee's bank asked and is
// owed an answer; the payer's bank is holding a posted debtor leg and its
// message is what makes it hand a customer their money back. Sending the news
// first and returning on its failure — which is what this did — left the refund
// unattempted, and the payer's money in a suspense against a payment the network
// records as Rejected, with nobody left to notice. The failure is reachable: the
// send is refused with ErrUnknownBIC through Reset's ForgetBanks/JoinRoster
// window, and the lookup fails on a store error.
//
// The submitter is made unaddressable rather than the payer's bank, because
// that is the direction the ordering has to survive. Both are attempted and both
// errors come back joined, so the dead letter still names what went wrong.
//
// # How the submitter is made unaddressable, and what stopped working
//
// By taking its ACTOR away, so the send is refused with ErrUnknownBIC. It used
// to be by taking its ROSTER ENTRY away, through a csmOps that failed the lookup
// for one participant — and there is no lookup left: tell reads both banks'
// addresses off the payment itself, because a payment names its parties by BIC
// (see payment.PartyRef). That stand-in also covered a store failure, which is
// now equally unreachable on this path, and Reset's ForgetBanks/JoinRoster window
// — which is exactly what removing the actor reproduces.
//
// There is no stub for a roster entry that cannot be read: csmOps has no
// GetRosterEntry, because a status is addressed from the payment's own agent
// BICs. The failure this test is about is the SEND. See
// TestTheRefundIsAttemptedEvenWhenTheSubmitterCannotBeAddressed.

// A second copy of a released collection is REPORTED at the debtor's bank, not
// executed again — and above all not collected from the payer twice.
//
// This is the transport end of the guard payment.AcceptInboundTx documents.
// Through a completed chain the redelivery meets the STATUS guard first: this
// bank's own copy is Settled, so it is no longer Initiated and
// ErrInvalidStateTransition comes back. That sentinel carries the empty code in
// payment's reasonTable precisely so that it never reaches a counterparty, and
// payment.Answerable is what keeps it out of a pacs.004 — a bank that RETURNED a
// redelivery would send back a collection it had in fact executed.
//
// The narrower witness inside that half — DebtorLegTx, which catches a
// redelivery arriving while the payment is still Initiated — is covered on both
// stores by payment's TestAcceptInboundIgnoresARedeliveredCollection. It is not
// reachable deterministically from here, because reaching it means racing the
// clearing house's acceptance, and no test in this package waits on a duration
// to decide who won.
func TestARedeliveredCollectionIsReportedAtTheDebtorsBank(t *testing.T) {
	h := newHarness(t)
	p := h.settledCollection(t)

	// The pacs.003 the clearing house released, sent a second time.
	var relayed []byte
	h.mu.Lock()
	for _, m := range h.seen {
		if m.to == h.debtorBIC {
			relayed = m.raw
		}
	}
	h.mu.Unlock()
	if relayed == nil {
		t.Fatal("the payer's bank was never sent a pacs.003")
	}
	h.injectRaw(t, h.cfg.ClearingHouseBIC, h.debtorBIC, relayed)

	err := h.workErr(t)
	// Matched on the TEXT and not with errors.Is: a day's report is prose an
	// operator reads, and Problem.Detail does not wrap the sentinel.
	if err == nil || !strings.Contains(err.Error(), payment.ErrInvalidStateTransition.Error()) {
		t.Fatalf("the day reported %v, want the illegal transition as a problem", err)
	}
	if got := h.bankPayment(t, h.debtorBIC, p.ID); got.Status != payment.Settled {
		t.Errorf("the redelivery moved the payer's bank's copy to %v; it was already Settled", got.Status)
	}
	// And the payer was debited once. This is the last of three guards and not
	// the first, which is worth stating rather than implying: it fires only after
	// the status guard, AcceptInboundTx's DebtorLegTx witness and the ledger's
	// idempotency key on the leg have all gone. It was checked that it fires at
	// all — with those three removed the payer is collected from twice and this
	// is the line that says so, in the terms that matter to a payer.
	if bal := h.balance(t, h.debtorPID, h.debtorAcct.ID); bal != harnessFunding-harnessAmount {
		t.Errorf("the payer's balance = %d, want %d — the redelivery collected again",
			bal, harnessFunding-harnessAmount)
	}
}
