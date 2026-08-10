package lending_test

import (
	"context"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
)

// This is the spec's worked example, and the point of it is the €49.32.
//
// The schedule says the first month's interest is €50.00 — one twelfth of 6% on
// €10,000. Thirty days of ACT/365 accrual is €49.32. Allocation settles what
// ACTUALLY accrued, not what the plan projected, and the principal portion
// absorbs the difference. That is why the 30/360 convention exists.
func TestRepay_AllocatesAgainstAccruedNotTheSchedule(t *testing.T) {
	ctx := context.Background()
	p, book, loan, customer := disbursedLoan(t)

	for i := 1; i <= 30; i++ {
		if err := p.Accrue(ctx, loan.ID, day(2025, time.January, 15).AddDate(0, 0, i)); err != nil {
			t.Fatalf("Accrue day %d: %v", i, err)
		}
	}

	schedule, err := p.Schedule(ctx, loan.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if schedule[0].Interest != 5_000 {
		t.Fatalf("the schedule projects %d, want 5000", schedule[0].Interest)
	}
	accrued, err := p.AccruedInterest(ctx, loan.ID)
	if err != nil {
		t.Fatalf("AccruedInterest: %v", err)
	}
	if accrued != 4_932 {
		t.Fatalf("accrued over 30 days = %d, want 4932", accrued)
	}

	// Pay the scheduled instalment of €193.33.
	txn, err := p.Repay(ctx, loan.ID, customer, 19_333, day(2025, time.February, 14), "Instalment 1")
	if err != nil {
		t.Fatalf("Repay: %v", err)
	}
	if len(txn.Entries) != 3 {
		t.Fatalf("repayment posted %d entries, want 3", len(txn.Entries))
	}

	// Dr the customer's account for the whole payment; Cr the receivable for
	// what accrued and the principal for the rest.
	drawn, err := p.Drawn(ctx, loan.ID)
	if err != nil {
		t.Fatalf("Drawn: %v", err)
	}
	if want := ledger.Amount(1_000_000 - 14_401); drawn != want {
		t.Errorf("principal after repayment = %d, want %d", drawn, want)
	}
	receivable, err := p.AccruedInterest(ctx, loan.ID)
	if err != nil {
		t.Fatalf("AccruedInterest: %v", err)
	}
	if receivable != 0 {
		t.Errorf("receivable after repayment = %d, want 0", receivable)
	}
	glReceivable, err := book.BookBalance(ctx, loan.InterestGL.Total())
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	if glReceivable != 0 {
		t.Errorf("receivable account = %d, want 0", glReceivable)
	}

	// The instalment is marked satisfied, so arrears will not see it.
	schedule, err = p.Schedule(ctx, loan.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if schedule[0].Outstanding() != 0 {
		t.Errorf("instalment 1 outstanding = %d, want 0", schedule[0].Outstanding())
	}
	if schedule[1].Outstanding() != schedule[1].Total() {
		t.Errorf("instalment 2 was touched: outstanding %d, total %d", schedule[1].Outstanding(), schedule[1].Total())
	}
}

func TestRepay_InterestBeforePrincipalOnAPartialPayment(t *testing.T) {
	ctx := context.Background()
	p, _, loan, customer := disbursedLoan(t)

	for i := 1; i <= 30; i++ {
		if err := p.Accrue(ctx, loan.ID, day(2025, time.January, 15).AddDate(0, 0, i)); err != nil {
			t.Fatalf("Accrue day %d: %v", i, err)
		}
	}

	// Less than the accrued interest: none of it reaches principal.
	if _, err := p.Repay(ctx, loan.ID, customer, 2_000, day(2025, time.February, 14), "Part payment"); err != nil {
		t.Fatalf("Repay: %v", err)
	}
	drawn, err := p.Drawn(ctx, loan.ID)
	if err != nil {
		t.Fatalf("Drawn: %v", err)
	}
	if drawn != 1_000_000 {
		t.Errorf("principal after a part payment = %d, want 1000000", drawn)
	}
	accrued, err := p.AccruedInterest(ctx, loan.ID)
	if err != nil {
		t.Fatalf("AccruedInterest: %v", err)
	}
	if accrued != 2_932 {
		t.Errorf("receivable after a part payment = %d, want 2932", accrued)
	}

	// The instalment is partly satisfied, and arrears will still see it.
	schedule, err := p.Schedule(ctx, loan.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if schedule[0].Outstanding() != 19_333-2_000 {
		t.Errorf("instalment 1 outstanding = %d, want %d", schedule[0].Outstanding(), 19_333-2_000)
	}
}

func TestRepay_Rejects(t *testing.T) {
	ctx := context.Background()
	p, _, loan, customer := disbursedLoan(t)

	_, err := p.Repay(ctx, loan.ID, customer, 0, day(2025, time.February, 14), "nothing")
	assertErrorIs(t, err, lending.ErrInvalidAmount)

	_, err = p.Repay(ctx, loan.ID, customer, -1, day(2025, time.February, 14), "negative")
	assertErrorIs(t, err, lending.ErrInvalidAmount)

	// More than is owed. Overpaying a loan is a refund, not a repayment, and
	// letting it through would drive the principal account negative — which
	// checkSufficientBalance refuses anyway, but with an error naming the
	// ledger rather than the mistake.
	_, err = p.Repay(ctx, loan.ID, customer, 2_000_000, day(2025, time.February, 14), "too much")
	assertErrorIs(t, err, lending.ErrInvalidAmount)
}

func TestRepayAndClose_FullSettlement(t *testing.T) {
	ctx := context.Background()
	p, book, sub, customer := newTestPortfolio(t)

	line, err := p.OpenRevolvingLine(ctx, sub, "Alice Line", "EUR", 250_000, 180_000, interest.ACT365, 20_000)
	if err != nil {
		t.Fatalf("OpenRevolvingLine: %v", err)
	}
	if _, err := p.Draw(ctx, line.ID, customer, 100_000, "Draw"); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	for i := 1; i <= 30; i++ {
		if err := p.Accrue(ctx, line.ID, day(2025, time.January, 15).AddDate(0, 0, i)); err != nil {
			t.Fatalf("Accrue: %v", err)
		}
	}

	// Closing with a balance outstanding is refused, the same rule
	// deposit.Close already applies to an account.
	assertErrorIs(t, p.Close(ctx, line.ID), lending.ErrFacilityNotEmpty)

	outstanding, err := p.Outstanding(ctx, line.ID)
	if err != nil {
		t.Fatalf("Outstanding: %v", err)
	}
	if outstanding != 100_000+1_479 {
		t.Errorf("outstanding = %d, want 101479", outstanding)
	}

	if _, err := p.Repay(ctx, line.ID, customer, outstanding, day(2025, time.February, 14), "Settle in full"); err != nil {
		t.Fatalf("Repay: %v", err)
	}

	drawn, err := p.Drawn(ctx, line.ID)
	if err != nil {
		t.Fatalf("Drawn: %v", err)
	}
	if drawn != 0 {
		t.Errorf("principal after settlement = %d, want 0", drawn)
	}

	if err := p.Close(ctx, line.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed, err := p.GetFacility(ctx, line.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	if closed.Status != lending.Closed {
		t.Errorf("status = %s, want Closed", closed.Status)
	}

	// Closed is terminal for advancing and for repaying.
	_, err = p.Draw(ctx, line.ID, customer, 1_000, "after closing")
	assertErrorIs(t, err, lending.ErrFacilityClosed)
	_, err = p.Repay(ctx, line.ID, customer, 1, day(2025, time.February, 15), "after closing")
	assertErrorIs(t, err, lending.ErrFacilityClosed)

	// The customer paid for the loan out of their own account, so the two
	// sides net to nothing: they borrowed 100_000 and repaid 101_479.
	balance, err := book.BookBalance(ctx, customer.Total())
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	if balance != 100_000-101_479 {
		t.Errorf("customer balance = %d, want %d", balance, 100_000-101_479)
	}
}

func TestClose_RefusesAnUnsettledReceivable(t *testing.T) {
	ctx := context.Background()
	p, _, loan, customer := disbursedLoan(t)

	// Repay all the principal but none of the interest.
	for i := 1; i <= 30; i++ {
		if err := p.Accrue(ctx, loan.ID, day(2025, time.January, 15).AddDate(0, 0, i)); err != nil {
			t.Fatalf("Accrue: %v", err)
		}
	}
	// Interest first, so settling exactly the principal still leaves the
	// receivable behind — the payment clears interest before it touches it.
	if _, err := p.Repay(ctx, loan.ID, customer, 4_932, day(2025, time.February, 14), "interest only"); err != nil {
		t.Fatalf("Repay: %v", err)
	}
	if _, err := p.Repay(ctx, loan.ID, customer, 1_000_000, day(2025, time.February, 14), "principal"); err != nil {
		t.Fatalf("Repay: %v", err)
	}

	// One more day of interest, and the facility is not empty.
	if err := p.Accrue(ctx, loan.ID, day(2025, time.February, 15)); err != nil {
		t.Fatalf("Accrue: %v", err)
	}
	// Nothing accrues on a zero principal, so this one closes.
	if err := p.Close(ctx, loan.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestClose_SucceedsOnAnExactHalfMinorUnitResidue pins the one residue
// Accrued.Minor() cannot represent as settled: exactly half a minor unit.
// Minor() rounds half AWAY from zero, so Minor(500_000) is 1 and
// Minor(-500_000) is -1 — never 0 — even though the receivable itself is
// fully cleared. If CloseTx tested the record instead of the receivable's own
// ledger balance, a facility that ever lands on this exact residue could
// never be closed again: once drawn principal is zero, further accrual adds
// nothing and the residue never resolves.
//
// €18.25 drawn at 10% (ACT/365), for exactly one day:
//
//	1_825 × 100_000 × 1 / 365 = 500_000 micro-minor-units, exactly half a cent.
//
// Minor(500_000) = 1, so settling the loan credits 1 cent to the receivable
// (clearing it, since that is exactly its book balance) and leaves the record at
// 500_000 − 1_000_000 = −500_000. Minor(−500_000) = −1, which is nonzero — and
// the receivable is back to zero.
func TestClose_SucceedsOnAnExactHalfMinorUnitResidue(t *testing.T) {
	ctx := context.Background()
	p, book, sub, customer := newTestPortfolio(t)

	loan, err := p.OpenTermLoan(ctx, sub, "Exact Half Loan", "EUR", 1_825, 100_000, interest.ACT365, lending.Annuity, 12)
	if err != nil {
		t.Fatalf("OpenTermLoan: %v", err)
	}
	// The recompute window opens at ORIGINATION, which the fixed clock puts on
	// the same day as this disbursement, 15 January — so the run below is one
	// ACT/365 day whichever of the two you measure from.
	if _, err := p.Disburse(ctx, loan.ID, customer, day(2025, time.April, 15), "advance"); err != nil {
		t.Fatalf("Disburse: %v", err)
	}
	if err := p.Accrue(ctx, loan.ID, day(2025, time.January, 16)); err != nil {
		t.Fatalf("Accrue: %v", err)
	}

	before, err := p.GetFacility(ctx, loan.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	if before.Accrued != 500_000 {
		t.Fatalf("accrued after one day = %d, want 500000", before.Accrued)
	}

	// Settle in full: 1,825 principal plus the capitalized 1-cent receivable.
	if _, err := p.Repay(ctx, loan.ID, customer, 1_826, day(2025, time.January, 16), "settle in full"); err != nil {
		t.Fatalf("Repay: %v", err)
	}

	after, err := p.GetFacility(ctx, loan.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	if after.Accrued != -500_000 {
		t.Errorf("residue = %d, want -500000", after.Accrued)
	}
	if got := after.Accrued.Minor(); got != -1 {
		t.Errorf("residue rounds to %d, want -1 (AWAY from zero, not to it)", got)
	}

	drawn, err := p.Drawn(ctx, loan.ID)
	if err != nil {
		t.Fatalf("Drawn: %v", err)
	}
	if drawn != 0 {
		t.Fatalf("drawn principal = %d, want 0", drawn)
	}
	receivable, err := book.BookBalance(ctx, after.InterestGL.Total())
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	if receivable != 0 {
		t.Fatalf("receivable account = %d, want 0 despite the nonzero residue", receivable)
	}

	// A guard on Accrued.Minor() != 0 would refuse this close forever. Reading the
	// receivable's own ledger balance instead gives zero, and lets it through.
	if err := p.Close(ctx, loan.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// DisburseTx and DrawTx both guard ErrFacilityClosed, and Close is what makes
// either reachable.
func TestDisburseAndDraw_RefuseAClosedFacility(t *testing.T) {
	ctx := context.Background()
	p, _, sub, customer := newTestPortfolio(t)

	// A term loan, disbursed, repaid in full, and closed.
	loan, err := p.OpenTermLoan(ctx, sub, "Alice Home Loan", "EUR", 1_000_000, 60_000, interest.ACT365, lending.Annuity, 60)
	if err != nil {
		t.Fatalf("OpenTermLoan: %v", err)
	}
	if _, err := p.Disburse(ctx, loan.ID, customer, day(2025, time.February, 15), "advance"); err != nil {
		t.Fatalf("Disburse: %v", err)
	}
	if _, err := p.Repay(ctx, loan.ID, customer, 1_000_000, day(2025, time.February, 15), "settle in full"); err != nil {
		t.Fatalf("Repay: %v", err)
	}
	if err := p.Close(ctx, loan.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A second disbursement on a closed term loan is refused for being closed,
	// not merely for being already disbursed.
	_, err = p.Disburse(ctx, loan.ID, customer, day(2025, time.February, 15), "again")
	assertErrorIs(t, err, lending.ErrFacilityClosed)

	// A revolving line, drawn, repaid in full, and closed.
	line, err := p.OpenRevolvingLine(ctx, sub, "Alice Line", "EUR", 250_000, 180_000, interest.ACT365, 20_000)
	if err != nil {
		t.Fatalf("OpenRevolvingLine: %v", err)
	}
	if _, err := p.Draw(ctx, line.ID, customer, 100_000, "draw"); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	if _, err := p.Repay(ctx, line.ID, customer, 100_000, day(2025, time.February, 15), "settle in full"); err != nil {
		t.Fatalf("Repay: %v", err)
	}
	if err := p.Close(ctx, line.ID); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = p.Draw(ctx, line.ID, customer, 1_000, "after closing")
	assertErrorIs(t, err, lending.ErrFacilityClosed)
}
