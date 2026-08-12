package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// The camt.053's own tests.

// reserveOf is a member's reserve asset in its OWN book, in euro.
func (h *harness) reserveOf(t *testing.T, id payment.ParticipantID) ledger.Amount {
	t.Helper()
	ctx := context.Background()
	p, err := h.bank(iso20022.BIC(id)).GetBank(ctx, id)
	if err != nil {
		t.Fatalf("GetBank %s: %v", id, err)
	}
	accts, err := p.AccountsFor("EUR")
	if err != nil {
		t.Fatalf("AccountsFor EUR: %v", err)
	}
	bal, err := p.Ledger.BookBalance(ctx, accts.Reserve.Total())
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	return bal
}

// settlementAccountOf is a member's reserve account id as the CENTRAL BANK names
// it — the account a camt.053 to that member is about.
func (h *harness) settlementAccountOf(t *testing.T, id payment.ParticipantID) ledger.AccountID {
	t.Helper()
	p, err := h.bank(iso20022.BIC(id)).GetBank(context.Background(), id)
	if err != nil {
		t.Fatalf("GetBank %s: %v", id, err)
	}
	accts, err := p.AccountsFor("EUR")
	if err != nil {
		t.Fatalf("AccountsFor EUR: %v", err)
	}
	return accts.Settlement
}

// statementTo is the camt.053 one bank was actually sent, parsed, with its own
// document handed back for editing.
func statementTo(t *testing.T, h *harness, to iso20022.BIC) (iso20022.Envelope, *iso20022.Camt053) {
	t.Helper()
	env, err := iso20022.Unmarshal(h.lastMessageOfTypeTo(t, to, "camt.053.001.08"))
	if err != nil {
		t.Fatalf("unmarshalling the statement sent to %s: %v", to, err)
	}
	doc, ok := env.Document.(*iso20022.Camt053)
	if !ok {
		t.Fatalf("the camt.053 sent to %s parsed as %T", to, env.Document)
	}
	return env, doc
}

// injectStatement marshals an edited statement back into a bank's download queue, as the
// settlement agent.
func injectStatement(t *testing.T, h *harness, to iso20022.BIC, env iso20022.Envelope) {
	t.Helper()
	raw, err := iso20022.Marshal(env)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	h.injectRaw(t, h.cfg.CentralBankBIC, to, raw)
}

// TestARedeliveredStatementBooksTheMirrorLegOnce is the witness on
// PostSettlementAdviceTx's first guard: an advice already AdvicePosted is
// returned as it stands and nothing is posted.
func TestARedeliveredStatementBooksTheMirrorLegOnce(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.work(t)
	h.closeCycle(t)
	h.work(t)

	cyc := h.creditTransferCycle(t)
	before := h.advice(t, h.creditorPID, string(cyc.ID))
	if before.Status != payment.AdvicePosted {
		t.Fatalf("the payee's bank holds an advice that is %v; there is no redelivery to test otherwise", before.Status)
	}
	reserveBefore := h.reserveOf(t, h.creditorPID)
	suspenseBefore := h.suspense(t, h.creditorPID)

	// The statement that bank already booked, replayed verbatim into its queue.
	raw := h.lastMessageOfTypeTo(t, h.creditorBIC, "camt.053.001.08")
	h.injectRaw(t, h.cfg.CentralBankBIC, h.creditorBIC, raw)
	h.work(t)

	after := h.advice(t, h.creditorPID, string(cyc.ID))
	if after != before {
		t.Errorf("the advice row is %+v after a replayed statement, want the unchanged %+v", after, before)
	}
	if got := h.reserveOf(t, h.creditorPID); got != reserveBefore {
		t.Errorf("the payee's bank holds a reserve of %d after a replayed statement, want the unchanged %d", got, reserveBefore)
	}
	if got := h.suspense(t, h.creditorPID); got != suspenseBefore {
		t.Errorf("the payee's bank's suspense is %d after a replayed statement, want the unchanged %d", got, suspenseBefore)
	}
}

// TestARedeliveredSettlementStatusPaysThePayeeOnce is the witness on
// SettleAtBankTx's Settled guard.
func TestARedeliveredSettlementStatusPaysThePayeeOnce(t *testing.T) {
	h := newHarness(t)
	p := h.settledCollection(t)

	before := h.bankPayment(t, h.creditorBIC, p.ID)
	if before.Status != payment.Settled {
		t.Fatalf("status = %v, want Settled; there is no redelivery to test otherwise", before.Status)
	}
	if before.CreditorLegTx == "" {
		t.Fatal("the payee's bank posted no creditor leg, so a replay has nothing to double")
	}
	payeeBefore := h.balance(t, h.creditorPID, h.creditorAcct.ID)
	suspenseBefore := h.suspense(t, h.creditorPID)

	raw := h.lastMessageOfTypeTo(t, h.creditorBIC, "pacs.002.001.10")
	h.injectRaw(t, h.cfg.ClearingHouseBIC, h.creditorBIC, raw)
	h.work(t)

	if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != payeeBefore {
		t.Errorf("the payee holds %d after a replayed ACSC, want the unchanged %d", got, payeeBefore)
	}
	if got := h.suspense(t, h.creditorPID); got != suspenseBefore {
		t.Errorf("the payee's bank's suspense is %d after a replayed ACSC, want the unchanged %d", got, suspenseBefore)
	}
	after := h.bankPayment(t, h.creditorBIC, p.ID)
	if after.CreditorLegTx != before.CreditorLegTx {
		t.Errorf("the creditor leg is now %q, want the original %q", after.CreditorLegTx, before.CreditorLegTx)
	}
	if after.Status != payment.Settled {
		t.Errorf("status = %v after a replayed ACSC, want Settled", after.Status)
	}
}

// TestAStatementAboutAnotherBanksAccountIsRefused is
// ErrStatementNotForThisBank's witness, and it had none.
func TestAStatementAboutAnotherBanksAccountIsRefused(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.work(t)
	h.closeCycle(t)
	h.work(t)

	cyc := h.creditTransferCycle(t)
	reserveBefore := h.reserveOf(t, h.creditorPID)

	// The domain's refusal, made where it lives. The movement is the payer
	// bank's own, named by the payer bank's own reserve account, and the payee's
	// bank is the one asking to book it.
	_, err := h.bank(h.creditorBIC).PostSettlementAdvice(context.Background(), payment.AdvisedMovement{
		Account:        h.settlementAccountOf(t, h.debtorPID),
		Asset:          "EUR",
		Movement:       -harnessAmount,
		ClosingBalance: 0,
		Reference:      string(cyc.ID),
	})
	if !errors.Is(err, payment.ErrStatementNotForThisBank) {
		t.Errorf("booking another member's statement got %v, want ErrStatementNotForThisBank", err)
	}

	// And the same thing as a MESSAGE: the statement the payer's bank was sent,
	// delivered to the payee's bank instead.
	h.injectRaw(t, h.cfg.CentralBankBIC, h.creditorBIC,
		h.lastMessageOfTypeTo(t, h.debtorBIC, "camt.053.001.08"))
	drained := h.workErr(t)
	if drained == nil || !strings.Contains(drained.Error(), "could not book the settlement") {
		t.Fatalf("the day after a misrouted statement = %v, want a reported problem", drained)
	}
	// Matched on the TEXT and not with errors.Is, for the reason
	// TestARedeliveredReturnIsReportedAndNotAnswered gives: a day's report is
	// prose an operator reads, not a wrapped error.
	if drained == nil || !strings.Contains(drained.Error(), payment.ErrStatementNotForThisBank.Error()) {
		t.Errorf("the day reported %v, want it to name ErrStatementNotForThisBank", drained)
	}

	// The money is what could not be retried: the payee's bank's reserve did not
	// move on somebody else's position.
	if got := h.reserveOf(t, h.creditorPID); got != reserveBefore {
		t.Errorf("the payee's bank holds a reserve of %d after a misrouted statement, want the unchanged %d", got, reserveBefore)
	}
}

// TestAStatementCarryingTwoAccountsIsRefused is the witness on
// bank.receiveStatement's own guard, which is not the domain's.
func TestAStatementCarryingTwoAccountsIsRefused(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.work(t)
	h.closeCycle(t)
	h.work(t)

	env, doc := statementTo(t, h, h.creditorBIC)
	doc.BkToCstmrStmt.Stmt = append(doc.BkToCstmrStmt.Stmt, doc.BkToCstmrStmt.Stmt[0])
	injectStatement(t, h, h.creditorBIC, env)

	drained := h.workErr(t)
	if drained == nil || !strings.Contains(drained.Error(), "carrying 2 accounts") {
		t.Fatalf("the day after a two-account statement = %v, want a reported problem naming the count", drained)
	}
}

// TestAnUnreadableStatementIsNotBooked is the other half of the handler, and it
// is a different failure from the one above: not a document this reader has no
// rule for, but one it cannot read at all.
func TestAnUnreadableStatementIsNotBooked(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.work(t)
	h.closeCycle(t)
	h.work(t)

	cyc := h.creditTransferCycle(t)
	before := h.advice(t, h.creditorPID, string(cyc.ID))
	reserveBefore := h.reserveOf(t, h.creditorPID)

	env, doc := statementTo(t, h, h.creditorBIC)
	doc.BkToCstmrStmt.Stmt[0].Ntry = append(doc.BkToCstmrStmt.Stmt[0].Ntry, doc.BkToCstmrStmt.Stmt[0].Ntry[0])
	injectStatement(t, h, h.creditorBIC, env)

	drained := h.workErr(t)
	if drained == nil || !strings.Contains(drained.Error(), "could not read the statement") {
		t.Fatalf("the day after an unreadable statement = %v, want a reported problem", drained)
	}
	if after := h.advice(t, h.creditorPID, string(cyc.ID)); after != before {
		t.Errorf("the advice row is %+v after an unreadable statement, want the unchanged %+v", after, before)
	}
	if got := h.reserveOf(t, h.creditorPID); got != reserveBefore {
		t.Errorf("the payee's bank holds a reserve of %d after an unreadable statement, want the unchanged %d", got, reserveBefore)
	}
}
