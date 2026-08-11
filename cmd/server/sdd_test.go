package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/raphi011/cbs/ebics"
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
	// The leg is read off the DEBTOR's bank's own copy, because that is the only
	// row in the network with a column to hold one. The clearing house's copy has
	// no leg columns at all — it posts nothing and holds no book of accounts — so
	// asking it would report a missing leg for a leg that was posted.
	if got := h.bankPayment(t, h.debtorBIC, p.ID); got.DebtorLegTx == "" {
		t.Error("no debtor leg after the debtor's bank accepted the collection")
	}
}

func TestDirectDebitAgainstAnUnfundedDebtorIsAM04(t *testing.T) {
	h := newHarnessWithAnUnfundedDebtor(t)
	p := h.submitDirectDebit(t)
	h.work(t)

	got := h.payment(t, p.ID)
	if got.RejectCode != iso20022.StatusReasonInsufficientFunds {
		t.Errorf("reject code = %q, want AM04", got.RejectCode)
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

// The chain is four messages, and it is the one place in this package that
// answers "which bank submits a direct debit" without prose.
//
// Read the first hop against TestTheCreditTransferChainIsFourMessages and the
// whole difference between a push and a pull is there: the payee's bank starts
// the conversation, the payer's bank answers it. Everything after that is the
// same clearing house doing the same job.
func TestTheDirectDebitChainIsFourMessages(t *testing.T) {
	h := newHarness(t)
	h.submitDirectDebit(t)
	h.work(t)

	want := []struct {
		from, to iso20022.BIC
		msgDef   string
	}{
		{h.creditorBIC, h.cfg.ClearingHouseBIC, "pacs.003.001.08"},
		{h.cfg.ClearingHouseBIC, h.debtorBIC, "pacs.003.001.08"},
		{h.debtorBIC, h.cfg.ClearingHouseBIC, "pacs.002.001.10"},
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
// answer to an instruction, and it goes back to whoever gave it. The debtor's
// bank is the one that DECIDED here, and it is the last party that needs telling.
func TestARejectedCollectionIsAnsweredToThePayeesBank(t *testing.T) {
	h := newHarnessWithAnUnfundedDebtor(t)
	h.submitDirectDebit(t)
	h.work(t)

	h.assertLastStatusTo(t, h.creditorBIC, iso20022.StatusReasonInsufficientFunds)
	// And ONE answer, not two. The clearing house tells the payer's bank as well
	// only when that bank is holding money against the rejected payment, and this
	// collection was refused before any was taken — the funds check is what
	// refused it, and it runs before the leg is posted. A second pacs.002 here
	// would be the clearing house instructing a bank to give back what it never
	// took. See csm.tell.
	if got := h.messagesSeen(); got != 4 {
		t.Errorf("a refused collection carried %d messages, want 4", got)
	}
}

// A collection the clearing house cannot clear is TM01, and the payer's money
// comes back — from the bank that took it, which is not the bank that submitted.
//
// This is the pull flow's one genuinely new seam, and it does not exist in a
// push. By the time the clearing house refuses, the DEBTOR's bank has already
// posted the debtor leg: that is what accepting a collection means. So the
// rejection has two recipients and two reasons — the payee's bank, because it
// asked, and the payer's bank, because it is holding money against a payment
// this network has just rejected. See csm.receiveStatus.
func TestDirectDebitWithNoOpenCycleIsTM01(t *testing.T) {
	h := newHarnessWithNoOpenCycle(t)
	p := h.submitDirectDebit(t)
	h.work(t)

	got := h.payment(t, p.ID)
	if got.RejectCode != iso20022.StatusReasonInvalidCutOffTime {
		t.Errorf("reject code = %q, want TM01 — no cycle open IS an invalid cut-off time", got.RejectCode)
	}
	// The debtor's bank really did take the money before the clearing house
	// refused — otherwise there would be nothing for the reversal to prove. Its
	// own copy is where the leg is; see harness.bankPayment.
	if h.bankPayment(t, h.debtorBIC, p.ID).DebtorLegTx == "" {
		t.Fatal("no debtor leg, so this test cannot show the reversal it exists for")
	}
	if bal := h.suspense(t, h.debtorPID); bal != 0 {
		t.Errorf("payer's clearing suspense = %d after a TM01, want 0 — the collection was never reversed", bal)
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
// The clearing house cannot fail to KNOW where a bank is, only fail to REACH it.
func TestTheRefundIsAttemptedEvenWhenTheSubmitterCannotBeAddressed(t *testing.T) {
	h := newHarness(t)
	p := h.submitDirectDebit(t)
	h.work(t)

	got := h.payment(t, p.ID)
	if h.bankPayment(t, h.debtorBIC, p.ID).DebtorLegTx == "" {
		t.Fatal("no debtor leg, so there is no refund for this test to be about")
	}
	if h.suspense(t, h.debtorPID) != harnessAmount {
		t.Fatalf("the payer's bank is not holding the money, so this test asserts nothing")
	}

	// The clearing house's own half of a rejection, run directly: the payment
	// leaves its cycle and becomes Rejected, and NOBODY is told. That is what
	// csm.reject does before it calls tell, and doing it here is what leaves
	// tell as the only thing under test.
	rejected, err := h.net.RejectAtCSM(context.Background(), got.ID,
		iso20022.StatusReasonNotSpecifiedAgentGenerated, "operator")
	if err != nil {
		t.Fatalf("RejectAtCSM: %v", err)
	}

	// The submitter is made unreachable by moving it to an address no subscriber
	// is enrolled under, so the clearing house has no download queue to put its
	// message in and the enqueue is refused.
	//
	// On the local COPY of the payment, which is all tell is given and all it
	// reads. Nothing is written, so the payer's bank is still the real one and
	// still the bank the refund has to reach — which is the ordering under test.
	rejected.CreditorDetails.Agent = "NOSUCHBKXXX"
	relay := h.dep.ClearingHouse()
	before := h.statusesSentTo(h.debtorBIC)
	// payerAccepted is true: this collection was answered ACCP by the payer's
	// bank, which is what posted the debtor leg asserted above. It is an argument
	// now rather than a read of p.DebtorLegTx, because that column is the bank's
	// and is not in the clearing house's schema — see csm.tell.
	err = relay.tell(context.Background(), rejected,
		payment.OriginalMessage{MsgID: notProvided, MsgDefIdr: notProvided},
		payment.TransactionStatusReport{
			EndToEndID: endToEndOf(rejected),
			TxID:       string(rejected.ID),
			Status:     iso20022.TransactionStatusRejected,
			Code:       iso20022.StatusReasonNotSpecifiedAgentGenerated,
			Text:       "operator",
		}, true)

	// The failure is reported — it is not swallowed to make the refund look
	// clean.
	if err == nil || ebics.CodeOf(err) != ebics.InvalidUserOrUserState {
		t.Fatalf("tell = %v, want the submitter's queueing reported", err)
	}

	// And the refund went out anyway. Counted after the day is carried, because
	// the tap records a file when it crosses rather than when it is queued.
	h.work(t)
	if after := h.statusesSentTo(h.debtorBIC); after != before+1 {
		t.Fatalf("the payer's bank was sent %d statuses, want 1 — the refund was skipped because the other message failed",
			after-before)
	}
	// The money is what says it worked. A status the payer's bank read as an
	// acceptance would leave the suspense exactly where it is.
	if bal := h.suspense(t, h.debtorPID); bal != 0 {
		t.Fatalf("payer's clearing suspense = %d after the refund message, want 0", bal)
	}
}

// There is no stub for a roster entry that cannot be read: csmOps has no
// GetRosterEntry, because a status is addressed from the payment's own agent
// BICs. The failure this test is about is the SEND. See
// TestTheRefundIsAttemptedEvenWhenTheSubmitterCannotBeAddressed.

// A second copy of a collection the debtor's bank has already answered is
// dead-lettered, not answered again — and above all not answered TWICE with two
// different answers.
//
// This is the transport end of the guard payment.AcceptInboundTx documents. Through a
// completed chain the redelivery meets the STATUS guard first: the clearing
// house has taken the payment into a cycle, so it is no longer Initiated and
// ErrInvalidStateTransition comes back. That sentinel carries the empty code in
// payment's reasonTable precisely so that it never reaches a counterparty —
// ReasonFor would turn it into MS03 and reject, on the wire, a collection this
// bank in fact accepted.
//
// The narrower witness inside that half — DebtorLegTx, which catches a
// redelivery arriving while the payment is still Initiated — is covered on both
// stores by payment's TestAcceptInboundIgnoresARedeliveredCollection. It is not
// reachable deterministically from here, because reaching it means racing the
// clearing house's acceptance, and no test in this package waits on a duration
// to decide who won.
func TestARedeliveredCollectionIsReportedAtTheDebtorsBank(t *testing.T) {
	h := newHarness(t)
	p := h.submitDirectDebit(t)
	h.work(t)

	// The pacs.003 the clearing house relayed, sent a second time.
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
	if got := h.payment(t, p.ID); got.Status != payment.Accepted {
		t.Errorf("the redelivery moved the payment to %v; it was already Accepted", got.Status)
	}
	// And the payer was debited once. This is the last of three guards and not
	// the first, which is worth stating rather than implying: it fires only after
	// the status guard, AcceptInboundTx's DebtorLegTx witness and the ledger's
	// idempotency key on the leg have all gone. It was checked that it fires at
	// all — with those three removed the suspense doubles and this is the line
	// that says so, in the terms that matter to a payer.
	if bal := h.suspense(t, h.debtorPID); bal != harnessAmount {
		t.Errorf("payer's clearing suspense = %d, want %d — the redelivery collected again", bal, harnessAmount)
	}
}
