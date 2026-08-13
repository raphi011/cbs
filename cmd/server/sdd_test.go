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
	// And STILL no leg, which is the half the release order added.
	if _, err := h.bank(h.debtorBIC).GetPayment(context.Background(), p.ID); err == nil {
		t.Fatal("the payer's bank holds a copy of a collection that has not settled")
	}

	h.closeCycle(t)
	h.work(t)

	// The leg is read off the DEBTOR's bank's own copy, because that is the only
	// row in the network with a column to hold one.
	if got := h.bankPayment(t, h.debtorBIC, p.ID); got.DebtorLegTx == "" {
		t.Error("no debtor leg after the payer's bank was handed the settled collection")
	}
}

// A payer's bank that cannot fund the collection RETURNS it, and this is the
// pull's half of the same rule AC01 states for a push.
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

// The chain is ten files, and it is the one place in this package that answers
// "which bank submits a direct debit" without prose.
func TestTheDirectDebitChainIsTenFiles(t *testing.T) {
	h := newHarness(t)
	h.submitDirectDebit(t)
	h.day(t)

	want := []struct {
		from, to iso20022.BIC
		msgDef   string
	}{
		// The day opens with each member collecting the routing table, which is the
		// one file here that carries no message at all.
		{h.cfg.ClearingHouseBIC, h.debtorBIC, notAMessage},
		{h.cfg.ClearingHouseBIC, h.creditorBIC, notAMessage},

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
		if got := msgDefOf(seen[i].raw); seen[i].from != w.from || seen[i].to != w.to || got != w.msgDef {
			t.Errorf("hop %d is %s -> %s (%q), want %s -> %s (%q)",
				i, seen[i].from, seen[i].to, got, w.from, w.to, w.msgDef)
		}
	}
}

// The refusal reaches the bank that SUBMITTED the collection, which for a pull
// is the payee's — not the payer's, as it is for a push.
func TestARejectedCollectionIsAnsweredToThePayeesBank(t *testing.T) {
	h := newHarnessWithNoOpenCycle(t)
	h.submitDirectDebit(t)
	h.work(t)

	h.assertLastStatusTo(t, h.creditorBIC, iso20022.StatusReasonInvalidCutOffTime)
	// ONE answer, and the payer's bank is told nothing at all. It has never heard
	// of this collection: nothing is released before a cycle settles, and this one
	// never reached a cycle.
	if got := h.messagesSeen(); got != 2 {
		t.Errorf("a refused collection carried %d messages, want 2", got)
	}
}

// A collection the clearing house cannot clear is TM01, and NOBODY is holding
// any money when it says so.
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

// A second copy of a released collection is REPORTED at the debtor's bank, not
// executed again — and above all not collected from the payer twice.
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
	// And the payer was debited once.
	if bal := h.balance(t, h.debtorPID, h.debtorAcct.ID); bal != harnessFunding-harnessAmount {
		t.Errorf("the payer's balance = %d, want %d — the redelivery collected again",
			bal, harnessFunding-harnessAmount)
	}
}
