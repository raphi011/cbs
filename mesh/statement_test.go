package mesh

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
//
// They exist because nothing else in this package injected one. Every other
// settlement test drives a cut-off and reads the state it leaves, which walks the
// statement path but never puts a statement into a bank's inbox on its own terms
// — so none of receiveStatement's own guards had a witness, and neither did the
// two no-ops the domain makes for a message it has already acted on. All four
// were verified by breaking them and watching exactly these tests fail; the
// reviewer who found them replaced two early returns with panics and ran the
// whole suite green.

// reserveOf is a member's reserve asset in its OWN book, in euro.
//
// Its own, not the central bank's view of it: the whole point of a mirror leg is
// that the two are separate postings in separate books, and a test about booking
// twice has to read the one the second booking would land in. h.advice reads the
// row beside it; ReserveBalance reads the other side.
func (h *meshHarness) reserveOf(t *testing.T, id payment.ParticipantID) ledger.Amount {
	t.Helper()
	ctx := context.Background()
	p, err := h.net.GetParticipant(ctx, id)
	if err != nil {
		t.Fatalf("GetParticipant %s: %v", id, err)
	}
	accts, err := p.AccountsFor("EUR")
	if err != nil {
		t.Fatalf("AccountsFor EUR: %v", err)
	}
	bal, err := p.Ledger.BookBalance(ctx, accts.Reserve)
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	return bal
}

// settlementAccountOf is a member's reserve account id as the CENTRAL BANK names
// it — the account a camt.053 to that member is about.
func (h *meshHarness) settlementAccountOf(t *testing.T, id payment.ParticipantID) ledger.AccountID {
	t.Helper()
	p, err := h.net.GetParticipant(context.Background(), id)
	if err != nil {
		t.Fatalf("GetParticipant %s: %v", id, err)
	}
	accts, err := p.AccountsFor("EUR")
	if err != nil {
		t.Fatalf("AccountsFor EUR: %v", err)
	}
	return accts.Settlement
}

// statementTo is the camt.053 one bank was actually sent, parsed, with its own
// document handed back for editing.
//
// The real message rather than a hand-built one, because the two tests below
// provoke a receiver's guard by CHANGING one thing about a document the receiver
// would otherwise accept. A statement built from scratch could fail for some
// other reason and still read like a pass.
func statementTo(t *testing.T, h *meshHarness, to iso20022.BIC) (iso20022.Envelope, *iso20022.Camt053) {
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

// injectStatement marshals an edited statement back into a bank's inbox, as the
// settlement agent.
func injectStatement(t *testing.T, h *meshHarness, to iso20022.BIC, env iso20022.Envelope) {
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
//
// A queue can hand the same message over twice and the receiver cannot tell a
// duplicate from a retry, so a redelivered camt.053 is an ORDINARY event and not
// an error — which is why this drains clean rather than dead-lettering, and why
// the guard returns the existing row instead of refusing.
//
// The money assertion is the substance: the mirror leg moved the reserve once.
// Behind the guard the posting carries the idempotency key
// "<cycle>:reserve:<participant>", so a defect that got past it could not post
// twice either — the guard's own job is to stop the bank TRYING, and its
// observable form is the unchanged advice row that comes back.
func TestARedeliveredStatementBooksTheMirrorLegOnce(t *testing.T) {
	h := newMeshHarness(t)
	h.submitCreditTransfer(t)
	h.drain(t)
	h.closeCycle(t)
	h.drain(t)

	cyc := h.creditTransferCycle(t)
	before := h.advice(t, h.creditorPID, cyc.ID)
	if before.Status != payment.AdvicePosted {
		t.Fatalf("the payee's bank holds an advice that is %v; there is no redelivery to test otherwise", before.Status)
	}
	reserveBefore := h.reserveOf(t, h.creditorPID)
	suspenseBefore := h.suspense(t, h.creditorPID)

	// The statement that bank already booked, replayed verbatim into its inbox.
	raw := h.lastMessageOfTypeTo(t, h.creditorBIC, "camt.053.001.08")
	h.injectRaw(t, h.cfg.CentralBankBIC, h.creditorBIC, raw)
	h.drain(t)

	after := h.advice(t, h.creditorPID, cyc.ID)
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
// PostCreditorLegTx's Settled guard.
//
// Same shape as the statement above and a different message: the clearing house's
// per-payment ACSC, replayed. csm.tellSettled's dedup rationale leans on this
// guard — it argues that a fan-out sending the same advice twice would still
// leave the money right, and only the conversation wrong — so the guard it leans
// on needs a witness of its own.
//
// It returns the payment rather than refusing for a reason the comment there
// gives: transitioning twice is what ErrInvalidStateTransition reports, and that
// would tell a handler that did nothing wrong that it failed.
func TestARedeliveredSettlementStatusPaysThePayeeOnce(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitCreditTransfer(t)
	h.drain(t)
	h.closeCycle(t)
	h.drain(t)

	before := h.payment(t, p.ID)
	if before.Status != payment.Settled {
		t.Fatalf("status = %v, want Settled; there is no redelivery to test otherwise", before.Status)
	}
	payeeBefore := h.balance(t, h.creditorPID, h.creditorAcct.ID)
	suspenseBefore := h.suspense(t, h.creditorPID)

	raw := h.lastMessageOfTypeTo(t, h.creditorBIC, "pacs.002.001.10")
	h.injectRaw(t, h.cfg.ClearingHouseBIC, h.creditorBIC, raw)
	h.drain(t)

	if got := h.balance(t, h.creditorPID, h.creditorAcct.ID); got != payeeBefore {
		t.Errorf("the payee holds %d after a replayed ACSC, want the unchanged %d", got, payeeBefore)
	}
	if got := h.suspense(t, h.creditorPID); got != suspenseBefore {
		t.Errorf("the payee's bank's suspense is %d after a replayed ACSC, want the unchanged %d", got, suspenseBefore)
	}
	after := h.payment(t, p.ID)
	if after.CreditorLegTx != before.CreditorLegTx {
		t.Errorf("the creditor leg is now %q, want the original %q", after.CreditorLegTx, before.CreditorLegTx)
	}
	if after.Status != payment.Settled {
		t.Errorf("status = %v after a replayed ACSC, want Settled", after.Status)
	}
}

// TestAStatementAboutAnotherBanksAccountIsRefused is
// ErrStatementNotForThisBank's witness, and it had none.
//
// Its sibling ErrNotThisBanksPayment is pinned by TestOnlyThePayeesBankPaysThePayee
// and this one — described in its own doc as guarding "the failure that has no
// second reader to catch it" — was referenced by five comments and no test at
// all.
//
// The failure is worth stating precisely: a bank that booked whatever camt.053
// arrived would move its OWN reserve mirror by ANOTHER member's position, in its
// own book, and under isolation there is nobody else who can see both books to
// notice. Nothing in this mesh misroutes a statement, so the misrouting is
// injected — the payer's bank's own statement, delivered to the payee's bank.
//
// Both layers, because they refuse for different reasons. The domain refuses
// because the account named is not this participant's; the handler dead-letters
// because a message it cannot act on has no answer to send — a statement is not
// an instruction, so there is nothing to reject back to the sender.
func TestAStatementAboutAnotherBanksAccountIsRefused(t *testing.T) {
	h := newMeshHarness(t)
	h.submitCreditTransfer(t)
	h.drain(t)
	h.closeCycle(t)
	h.drain(t)

	cyc := h.creditTransferCycle(t)
	reserveBefore := h.reserveOf(t, h.creditorPID)

	// The domain's refusal, made where it lives. The movement is the payer
	// bank's own, named by the payer bank's own reserve account, and the payee's
	// bank is the one asking to book it.
	_, err := h.net.PostSettlementAdvice(context.Background(), h.creditorPID, payment.AdvisedMovement{
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
	drained := h.drainErr(t)
	if drained == nil || !strings.Contains(drained.Error(), "could not book the settlement") {
		t.Fatalf("draining after a misrouted statement = %v, want a dead letter", drained)
	}
	if !errors.Is(drained, payment.ErrStatementNotForThisBank) {
		t.Errorf("the dead letter is %v, want it to carry ErrStatementNotForThisBank", drained)
	}

	// The money is what could not be retried: the payee's bank's reserve did not
	// move on somebody else's position.
	if got := h.reserveOf(t, h.creditorPID); got != reserveBefore {
		t.Errorf("the payee's bank holds a reserve of %d after a misrouted statement, want the unchanged %d", got, reserveBefore)
	}
}

// TestAStatementCarryingTwoAccountsIsRefused is the witness on
// bank.receiveStatement's own guard, which is not the domain's.
//
// The standard permits several Stmt elements in one camt.053 and this system
// never builds one — StatementMessage sends one member per message, because a
// second statement would be about an account the recipient does not hold. The
// handler states that as a requirement it CHECKS rather than an assumption it
// makes, and this is what walks the check: booking the first and dropping the
// second would move a reserve mirror against a message half of which nothing
// ever acted on.
//
// It is provoked by DUPLICATING the statement this bank was really sent, so the
// only thing wrong with the document is the count.
func TestAStatementCarryingTwoAccountsIsRefused(t *testing.T) {
	h := newMeshHarness(t)
	h.submitCreditTransfer(t)
	h.drain(t)
	h.closeCycle(t)
	h.drain(t)

	env, doc := statementTo(t, h, h.creditorBIC)
	doc.BkToCstmrStmt.Stmt = append(doc.BkToCstmrStmt.Stmt, doc.BkToCstmrStmt.Stmt[0])
	injectStatement(t, h, h.creditorBIC, env)

	drained := h.drainErr(t)
	if drained == nil || !strings.Contains(drained.Error(), "carrying 2 accounts") {
		t.Fatalf("draining after a two-account statement = %v, want a dead letter naming the count", drained)
	}
}

// TestAnUnreadableStatementIsNotBooked is the other half of the handler, and it
// is a different failure from the one above: not a document this reader has no
// rule for, but one it cannot read at all.
//
// Two entries in ONE statement is payment.ReadStatement's refusal, and its
// reason is the sharper of the two — this system's central bank posts exactly one
// netting movement per member per cycle, so posting the first and dropping the
// second would move the bank's reserve mirror by the WRONG AMOUNT with nothing
// anywhere recording that it had.
//
// A statement is answered by nothing, so the only trace is a dead letter. That
// is not a gap: the central bank has already settled and is final, and there is
// no decision left for this bank to accept or refuse.
func TestAnUnreadableStatementIsNotBooked(t *testing.T) {
	h := newMeshHarness(t)
	h.submitCreditTransfer(t)
	h.drain(t)
	h.closeCycle(t)
	h.drain(t)

	cyc := h.creditTransferCycle(t)
	before := h.advice(t, h.creditorPID, cyc.ID)
	reserveBefore := h.reserveOf(t, h.creditorPID)

	env, doc := statementTo(t, h, h.creditorBIC)
	doc.BkToCstmrStmt.Stmt[0].Ntry = append(doc.BkToCstmrStmt.Stmt[0].Ntry, doc.BkToCstmrStmt.Stmt[0].Ntry[0])
	injectStatement(t, h, h.creditorBIC, env)

	drained := h.drainErr(t)
	if drained == nil || !strings.Contains(drained.Error(), "could not read the statement") {
		t.Fatalf("draining after an unreadable statement = %v, want a dead letter", drained)
	}
	if after := h.advice(t, h.creditorPID, cyc.ID); after != before {
		t.Errorf("the advice row is %+v after an unreadable statement, want the unchanged %+v", after, before)
	}
	if got := h.reserveOf(t, h.creditorPID); got != reserveBefore {
		t.Errorf("the payee's bank holds a reserve of %d after an unreadable statement, want the unchanged %d", got, reserveBefore)
	}
}
