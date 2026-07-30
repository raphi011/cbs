package lending_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
)

// overpaidLoan is the fixture the whole file needs: a facility the bank owes
// 4932 of interest back on.
//
// Producing it takes the full round trip, because there is no other way in — the
// payable is only ever credited by a backdated correction. Thirty days accrue,
// the borrower pays that interest in cash, and only then does a posting
// backdated to the drawdown reveal the loan was repaid on day one and no
// interest was ever owed. The receivable has nothing left to give back (the cash
// settled it) and neither has principal (the backdated posting cleared it), so
// the whole 4932 lands in the payable. It is
// TestAccrue_CorrectionClampsToWhatTheFacilityOwes' setup, kept here so this
// file's tests start from the state they are about.
func overpaidLoan(t *testing.T) (*lending.Portfolio, *ledger.Book, ledger.SubledgerID, lending.Facility, ledger.AccountID) {
	t.Helper()
	ctx := context.Background()
	p, book, sub, loan, customer := disbursedLoanIn(t)

	accrueDays(t, p, loan.ID, 30)
	settled, err := p.AccruedInterest(ctx, loan.ID)
	if err != nil {
		t.Fatalf("AccruedInterest: %v", err)
	}
	if settled != 4_932 {
		t.Fatalf("interest settled after 30 days = %d, want 4932", settled)
	}
	if _, err := p.Repay(ctx, loan.ID, customer, settled, drawdown.AddDate(0, 0, 30), "Interest"); err != nil {
		t.Fatalf("Repay: %v", err)
	}
	postTo(t, book, sub, loan.PrincipalGL, loan.Asset, 1_000_000, ledger.Credit, drawdown)
	if err := p.Accrue(ctx, loan.ID, drawdown.AddDate(0, 0, 31)); err != nil {
		t.Fatalf("Accrue: %v", err)
	}

	owed, err := p.RefundPayableFor(ctx, loan.ID)
	if err != nil {
		t.Fatalf("RefundPayableFor: %v", err)
	}
	if owed != 4_932 {
		t.Fatalf("refund payable = %d, want 4932; the fixture must leave a debt to discharge", owed)
	}
	return p, book, sub, facility(t, p, loan.ID), customer
}

// refundDate is a day after the correction that produced the payable.
var refundDate = drawdown.AddDate(0, 0, 40)

// TestRefundPayableFor_IsZeroWithoutAnAccount pins the read path's
// short-circuit. Almost every facility has no refunds-payable account at all —
// it is created only by a correction that overshoots — and reporting 0 for one
// must not mean resolving an account by name, because resolving it is what
// CREATES it. A read that materialises a Liability account on the bank's chart
// of accounts is not a read.
func TestRefundPayableFor_IsZeroWithoutAnAccount(t *testing.T) {
	ctx := context.Background()
	p, book, loan, _ := disbursedLoan(t)

	if got := facility(t, p, loan.ID).RefundGL; got != "" {
		t.Errorf("RefundGL = %q on an ordinary loan, want empty", got)
	}
	owed, err := p.RefundPayableFor(ctx, loan.ID)
	if err != nil {
		t.Fatalf("RefundPayableFor: %v", err)
	}
	if owed != 0 {
		t.Errorf("refund payable = %d, want 0", owed)
	}

	// And no Payables folder was conjured up by asking.
	ledgers, err := book.ListLedgers(ctx)
	if err != nil {
		t.Fatalf("ListLedgers: %v", err)
	}
	for _, l := range ledgers {
		subs, err := book.ListSubledgers(ctx, l.ID)
		if err != nil {
			t.Fatalf("ListSubledgers: %v", err)
		}
		for _, s := range subs {
			if s.Name == "Payables" {
				t.Error("reading a refund payable created the Payables subledger; a read must not write")
			}
		}
	}
}

// TestRefundInterest_DischargesThePayable is the gap this closes: before it,
// the payable could be credited and nothing could ever settle it.
func TestRefundInterest_DischargesThePayable(t *testing.T) {
	ctx := context.Background()
	p, book, _, loan, customer := overpaidLoan(t)

	before := bookBalance(t, book, customer)
	tx, err := p.RefundInterest(ctx, loan.ID, customer, 4_932, refundDate, "")
	if err != nil {
		t.Fatalf("RefundInterest: %v", err)
	}

	if got := bookBalance(t, book, loan.RefundGL); got != 0 {
		t.Errorf("refund payable = %d, want 0; the obligation must be discharged", got)
	}
	if got := bookBalance(t, book, customer) - before; got != 4_932 {
		t.Errorf("customer credited %d, want 4932", got)
	}
	if len(tx.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(tx.Entries))
	}
	if tx.Description != "Interest refund: Alice Home Loan" {
		t.Errorf("description = %q, want the defaulted one", tx.Description)
	}
	if !tx.ValueDate.Equal(refundDate) {
		t.Errorf("value date = %s, want %s", tx.ValueDate, refundDate)
	}

	// It settles a debt that has no components and no schedule, so it must not
	// touch the loan: the correction already credited principal as far as
	// principal could absorb, and only the overflow reached the payable.
	// Allocating any of this back onto the loan would hand the borrower money
	// the correction had already used to reduce what they owed.
	after := facility(t, p, loan.ID)
	if got := bookBalance(t, book, loan.PrincipalGL); got != 0 {
		t.Errorf("principal = %d after a refund, want 0 — a refund is not a repayment", got)
	}
	if got := bookBalance(t, book, loan.InterestGL); got != 0 {
		t.Errorf("receivable = %d after a refund, want 0", got)
	}
	if after.Accrued != loan.Accrued {
		t.Errorf("accrued moved from %d to %d; the record mirrors the receivable, which did not change",
			loan.Accrued, after.Accrued)
	}

	// One event, under its own type. A refund logged as EventFacilityRepaid
	// would net two opposite movements into one figure, since a repayment runs
	// the other way.
	events := refundEvents(t, p)
	if len(events) != 1 {
		t.Fatalf("wrote %d facility.interest_refunded events, want 1", len(events))
	}
	if got := events[0].EntityID; got != string(loan.ID) {
		t.Errorf("event entity = %q, want the facility %q", got, loan.ID)
	}
	var payload struct {
		Amount        int64  `json:"amount"`
		Remaining     int64  `json:"remaining"`
		TransactionID string `json:"transaction_id"`
	}
	if err := json.Unmarshal(events[0].Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if payload.Amount != 4_932 || payload.Remaining != 0 {
		t.Errorf("payload amount/remaining = %d/%d, want 4932/0", payload.Amount, payload.Remaining)
	}
	if payload.TransactionID != string(tx.ID) {
		t.Errorf("payload transaction_id = %q, want %q", payload.TransactionID, tx.ID)
	}
}

// refundEvents reads the lending-scope refund events. There is no HTTP route for
// ScopeLending audit yet — none of the facility events have one — so the log is
// read through the store, as arrears_test.go does.
func refundEvents(t *testing.T, p *lending.Portfolio) []ledger.AuditEvent {
	t.Helper()
	ctx := context.Background()
	var out []ledger.AuditEvent
	err := p.Store().View(ctx, func(ctx context.Context, tx lending.Tx) error {
		var err error
		out, err = tx.ListAudit(ctx, ledger.AuditFilter{
			Scope: ledger.ScopeLending, Type: ledger.EventFacilityInterestRefunded,
		})
		return err
	})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	return out
}

// TestRefundInterest_PaysPartially covers an obligation settled in instalments,
// which is ordinary: the payable is drawn down and the rest stays owed.
func TestRefundInterest_PaysPartially(t *testing.T) {
	ctx := context.Background()
	p, book, _, loan, customer := overpaidLoan(t)

	if _, err := p.RefundInterest(ctx, loan.ID, customer, 2_000, refundDate, ""); err != nil {
		t.Fatalf("RefundInterest: %v", err)
	}
	if got := bookBalance(t, book, loan.RefundGL); got != 2_932 {
		t.Errorf("refund payable = %d, want 2932 still owed", got)
	}
	if _, err := p.RefundInterest(ctx, loan.ID, customer, 2_932, refundDate, ""); err != nil {
		t.Fatalf("RefundInterest, second: %v", err)
	}
	if got := bookBalance(t, book, loan.RefundGL); got != 0 {
		t.Errorf("refund payable = %d, want 0", got)
	}
	// And now there is nothing left to pay.
	if _, err := p.RefundInterest(ctx, loan.ID, customer, 1, refundDate, ""); !errors.Is(err, lending.ErrNoRefundOutstanding) {
		t.Errorf("third refund err = %v, want ErrNoRefundOutstanding", err)
	}
}

// TestRefundInterest_RefusesMoreThanIsOwed is the guard the ledger cannot
// supply. Its sufficiency check covers Asset and Expense accounts only, so an
// over-refund out of a Liability posts cleanly and leaves the payable NEGATIVE:
// an account asserting the borrower owes the bank a refund, which is not a
// thing.
func TestRefundInterest_RefusesMoreThanIsOwed(t *testing.T) {
	ctx := context.Background()
	p, book, _, loan, customer := overpaidLoan(t)

	if _, err := p.RefundInterest(ctx, loan.ID, customer, 4_933, refundDate, ""); !errors.Is(err, lending.ErrInvalidAmount) {
		t.Errorf("over-refund err = %v, want ErrInvalidAmount", err)
	}
	if got := bookBalance(t, book, loan.RefundGL); got != 4_932 {
		t.Errorf("refund payable = %d after a refused refund, want 4932 untouched", got)
	}
	for _, amount := range []ledger.Amount{0, -1} {
		if _, err := p.RefundInterest(ctx, loan.ID, customer, amount, refundDate, ""); !errors.Is(err, lending.ErrInvalidAmount) {
			t.Errorf("refund of %d err = %v, want ErrInvalidAmount", amount, err)
		}
	}
}

// TestRefundInterest_RefusesAFacilityOwedNothing covers the ordinary loan: no
// correction has overshot on it, so there is no account and nothing to pay.
func TestRefundInterest_RefusesAFacilityOwedNothing(t *testing.T) {
	ctx := context.Background()
	p, _, loan, customer := disbursedLoan(t)

	_, err := p.RefundInterest(ctx, loan.ID, customer, 100, refundDate, "")
	if !errors.Is(err, lending.ErrNoRefundOutstanding) {
		t.Errorf("err = %v, want ErrNoRefundOutstanding", err)
	}
	_, err = p.RefundInterest(ctx, "fac_nope", customer, 100, refundDate, "")
	if !errors.Is(err, lending.ErrFacilityNotFound) {
		t.Errorf("err = %v, want ErrFacilityNotFound", err)
	}
}

// TestRefundInterest_SurvivesTheFacilityClosing is the asymmetry with Repay,
// which refuses ErrFacilityClosed.
//
// Closed means the BORROWER owes nothing and no more will be lent. It says
// nothing about what the bank owes: a bank that discovers it overcharged
// interest on a loan settled last year still owes that money. Refusing to pay it
// because the contract is over would strand the obligation in a Liability
// account with nothing left that could ever discharge it — the facility accrues
// no more, is never billed again, and takes no more repayments.
func TestRefundInterest_SurvivesTheFacilityClosing(t *testing.T) {
	ctx := context.Background()
	p, book, _, loan, customer := overpaidLoan(t)

	// The loan is fully repaid, so it closes — and closing does not consider the
	// payable, because that is the bank's debt and not the borrower's.
	if err := p.Close(ctx, loan.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := facility(t, p, loan.ID).Status; got != lending.Closed {
		t.Fatalf("status = %s, want Closed", got)
	}

	if _, err := p.RefundInterest(ctx, loan.ID, customer, 4_932, refundDate, ""); err != nil {
		t.Fatalf("RefundInterest on a closed facility: %v", err)
	}
	if got := bookBalance(t, book, loan.RefundGL); got != 0 {
		t.Errorf("refund payable = %d, want 0", got)
	}
}

// TestListRefundsPayable_ListsOnlyWhatIsOwed pins the listing, including the
// closed-facility case a naive "walk the active portfolio" implementation would
// drop — which is exactly the set of obligations nothing else surfaces.
func TestListRefundsPayable_ListsOnlyWhatIsOwed(t *testing.T) {
	ctx := context.Background()
	p, _, sub, loan, customer := overpaidLoan(t)

	// A second, ordinary facility in the same book. It must not appear: it has
	// no refunds-payable account, and a pooled per-asset account would have made
	// these two indistinguishable.
	if _, err := p.OpenRevolvingLine(ctx, sub, "Bob Line", "EUR", 500_000, 120_000, loanDayCount, 20_000); err != nil {
		t.Fatalf("OpenRevolvingLine: %v", err)
	}

	list, err := p.ListRefundsPayable(ctx)
	if err != nil {
		t.Fatalf("ListRefundsPayable: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("listed %d refunds, want 1: %+v", len(list), list)
	}
	got := list[0]
	if got.FacilityID != loan.ID {
		t.Errorf("facility = %q, want %q", got.FacilityID, loan.ID)
	}
	if got.Amount != 4_932 {
		t.Errorf("amount = %d, want 4932", got.Amount)
	}
	if got.Name != "Alice Home Loan" || got.Asset != "EUR" {
		t.Errorf("name/asset = %q/%q, want Alice Home Loan/EUR", got.Name, got.Asset)
	}
	if got.Account != loan.RefundGL {
		t.Errorf("account = %q, want %q", got.Account, loan.RefundGL)
	}
	if got.FacilityStatus != lending.Active {
		t.Errorf("facility status = %s, want Active", got.FacilityStatus)
	}

	// Closing the loan must not hide the debt.
	if err := p.Close(ctx, loan.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	list, err = p.ListRefundsPayable(ctx)
	if err != nil {
		t.Fatalf("ListRefundsPayable after close: %v", err)
	}
	if len(list) != 1 || list[0].Amount != 4_932 {
		t.Fatalf("listed %+v after closing, want the 4932 still owed", list)
	}
	if list[0].FacilityStatus != lending.Closed {
		t.Errorf("facility status = %s, want Closed", list[0].FacilityStatus)
	}

	// Paying it drops the row rather than leaving a zero one: an operator's
	// worklist of "who do we still owe" must not need filtering.
	if _, err := p.RefundInterest(ctx, loan.ID, customer, 4_932, refundDate, ""); err != nil {
		t.Fatalf("RefundInterest: %v", err)
	}
	list, err = p.ListRefundsPayable(ctx)
	if err != nil {
		t.Fatalf("ListRefundsPayable after refund: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("listed %+v after settling, want nothing owed", list)
	}
}
