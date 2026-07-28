package lending_test

import (
	"context"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
)

// disbursedLoan is a €10,000 five-year annuity at 6%, paid out on 15 January.
func disbursedLoan(t *testing.T) (*lending.Portfolio, *ledger.Book, lending.Facility, ledger.AccountID) {
	t.Helper()
	ctx := context.Background()
	p, book, sub, customer := newTestPortfolio(t)

	loan, err := p.OpenTermLoan(ctx, sub, "Alice Home Loan", "EUR", 1_000_000, 60_000, interest.ACT365, lending.Annuity, 60)
	if err != nil {
		t.Fatalf("OpenTermLoan: %v", err)
	}
	if _, err := p.Disburse(ctx, loan.ID, customer, day(2025, time.February, 15), "advance"); err != nil {
		t.Fatalf("Disburse: %v", err)
	}
	after, err := p.GetFacility(ctx, loan.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	return p, book, after, customer
}

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestAccrue_PostsTheDeltaOfTheRoundedValue(t *testing.T) {
	ctx := context.Background()
	p, book, loan, _ := disbursedLoan(t)

	// €10,000 at 6% ACT/365 accrues 164.383561 cents a day.
	wantAccrued := []interest.Accrued{164_383_561, 328_767_122, 493_150_683}
	wantGL := []ledger.Amount{164, 329, 493}

	for i := 1; i <= 3; i++ {
		if err := p.Accrue(ctx, loan.ID, day(2025, time.January, 15+i)); err != nil {
			t.Fatalf("Accrue day %d: %v", i, err)
		}
		got, err := p.GetFacility(ctx, loan.ID)
		if err != nil {
			t.Fatalf("GetFacility: %v", err)
		}
		if got.Accrued != wantAccrued[i-1] {
			t.Errorf("day %d accrued = %d, want %d", i, got.Accrued, wantAccrued[i-1])
		}
		balance, err := book.BookBalance(ctx, got.InterestGL)
		if err != nil {
			t.Fatalf("BookBalance: %v", err)
		}
		if balance != wantGL[i-1] {
			t.Errorf("day %d receivable = %d, want %d", i, balance, wantGL[i-1])
		}
		// The invariant, restated every day: the ledger holds Minor() of the
		// record, so the daily posting is the change in the rounding — 164,
		// then 165, then 164.
		if balance != got.Accrued.Minor() {
			t.Fatalf("day %d: GL %d != Minor(accrued) %d", i, balance, got.Accrued.Minor())
		}
	}
}

func TestAccrue_IsIdempotentAndOnlyOnDrawnPrincipal(t *testing.T) {
	ctx := context.Background()
	p, _, loan, _ := disbursedLoan(t)

	if err := p.Accrue(ctx, loan.ID, day(2025, time.January, 16)); err != nil {
		t.Fatalf("Accrue: %v", err)
	}
	after, err := p.GetFacility(ctx, loan.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}

	// The same date again, and a date before it, are both no-ops.
	if err := p.Accrue(ctx, loan.ID, day(2025, time.January, 16)); err != nil {
		t.Fatalf("re-Accrue: %v", err)
	}
	if err := p.Accrue(ctx, loan.ID, day(2025, time.January, 15)); err != nil {
		t.Fatalf("backwards Accrue: %v", err)
	}
	again, err := p.GetFacility(ctx, loan.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	if again.Accrued != after.Accrued {
		t.Errorf("accrued after re-runs = %d, want %d", again.Accrued, after.Accrued)
	}
}

func TestAccrue_UndrawnFacilityAccruesNothing(t *testing.T) {
	ctx := context.Background()
	p, _, sub, _ := newTestPortfolio(t)

	// An open but undrawn commitment costs the borrower nothing.
	line, err := p.OpenRevolvingLine(ctx, sub, "Alice Line", "EUR", 250_000, 180_000, interest.ACT365, 20_000)
	if err != nil {
		t.Fatalf("OpenRevolvingLine: %v", err)
	}
	if err := p.Accrue(ctx, line.ID, day(2025, time.January, 16)); err != nil {
		t.Fatalf("Accrue: %v", err)
	}
	got, err := p.GetFacility(ctx, line.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	if got.Accrued != 0 {
		t.Errorf("undrawn accrual = %d, want 0", got.Accrued)
	}
}

func TestChargeInterest_CapitalizesAndBillsTheCycle(t *testing.T) {
	ctx := context.Background()
	p, book, sub, customer := newTestPortfolio(t)

	// €1,000 drawn at 18% ACT/365, minimum payment 2% of the balance.
	line, err := p.OpenRevolvingLine(ctx, sub, "Alice Line", "EUR", 250_000, 180_000, interest.ACT365, 20_000)
	if err != nil {
		t.Fatalf("OpenRevolvingLine: %v", err)
	}
	if _, err := p.Draw(ctx, line.ID, customer, 100_000, "First draw"); err != nil {
		t.Fatalf("Draw: %v", err)
	}

	for i := 1; i <= 30; i++ {
		if err := p.Accrue(ctx, line.ID, day(2025, time.January, 15).AddDate(0, 0, i)); err != nil {
			t.Fatalf("Accrue day %d: %v", i, err)
		}
	}

	before, err := p.GetFacility(ctx, line.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	// 30 × 49.315068 cents = 1479.45204, which rounds to 1479.
	if before.Accrued != 1_479_452_040 {
		t.Fatalf("accrued over 30 days = %d, want 1479452040", before.Accrued)
	}

	statement := day(2025, time.February, 14)
	txn, err := p.ChargeInterest(ctx, line.ID, statement)
	if err != nil {
		t.Fatalf("ChargeInterest: %v", err)
	}
	if len(txn.Entries) != 2 {
		t.Fatalf("charge posted %d entries, want 2", len(txn.Entries))
	}

	// Capitalization moves the receivable into principal: the interest is now
	// part of what is borrowed, which is what makes a revolving balance
	// compound.
	drawn, err := p.Drawn(ctx, line.ID)
	if err != nil {
		t.Fatalf("Drawn: %v", err)
	}
	if drawn != 101_479 {
		t.Errorf("drawn after capitalization = %d, want 101479", drawn)
	}
	after, err := p.GetFacility(ctx, line.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	receivable, err := book.BookBalance(ctx, after.InterestGL)
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	if receivable != 0 {
		t.Errorf("receivable after capitalization = %d, want 0", receivable)
	}
	// Charging the rounded figure (1479) is 0.45204 LESS than the exact accrual
	// (1479.45204): 1479.452 rounds down, not up. That leaves the record
	// positive by the unrounded remainder, still absorbed by Minor() as 0.
	if after.Accrued != 452_040 {
		t.Errorf("residue = %d, want 452040", after.Accrued)
	}
	if after.Accrued.Minor() != 0 {
		t.Errorf("residue rounds to %d, want 0", after.Accrued.Minor())
	}

	// The cycle is billed: a minimum payment of the interest charged plus 2%
	// of the new balance. This is how a revolving facility falls into arrears —
	// by missing a minimum payment, not an amortization instalment.
	schedule, err := p.Schedule(ctx, line.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(schedule) != 1 {
		t.Fatalf("instalments after one cycle = %d, want 1", len(schedule))
	}
	if schedule[0].Seq != 1 {
		t.Errorf("first cycle Seq = %d, want 1", schedule[0].Seq)
	}
	if schedule[0].Interest != 1_479 {
		t.Errorf("cycle interest = %d, want 1479", schedule[0].Interest)
	}
	// 2% of 101_479 is 2029.58, rounded to 2030.
	if schedule[0].Principal != 2_030 {
		t.Errorf("cycle principal share = %d, want 2030", schedule[0].Principal)
	}
	if !schedule[0].DueDate.Equal(lending.AddMonths(statement, 1)) {
		t.Errorf("cycle due date = %v, want %v", schedule[0].DueDate, lending.AddMonths(statement, 1))
	}

	// A second cycle appends rather than replacing.
	for i := 31; i <= 60; i++ {
		if err := p.Accrue(ctx, line.ID, day(2025, time.January, 15).AddDate(0, 0, i)); err != nil {
			t.Fatalf("Accrue day %d: %v", i, err)
		}
	}
	if _, err := p.ChargeInterest(ctx, line.ID, day(2025, time.March, 16)); err != nil {
		t.Fatalf("second ChargeInterest: %v", err)
	}
	schedule, err = p.Schedule(ctx, line.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(schedule) != 2 {
		t.Fatalf("instalments after two cycles = %d, want 2", len(schedule))
	}
	if schedule[1].Seq != 2 {
		t.Errorf("second cycle Seq = %d, want 2", schedule[1].Seq)
	}
}

func TestChargeInterest_TermLoansDoNotCapitalize(t *testing.T) {
	ctx := context.Background()
	p, _, loan, _ := disbursedLoan(t)

	// A term loan settles interest through its scheduled instalments. Rolling
	// it into principal would silently re-amortize a signed contract.
	_, err := p.ChargeInterest(ctx, loan.ID, day(2025, time.February, 15))
	assertErrorIs(t, err, lending.ErrWrongFacilityKind)
}

func TestChargeInterest_NothingAccruedBillsNothing(t *testing.T) {
	ctx := context.Background()
	p, _, sub, _ := newTestPortfolio(t)

	line, err := p.OpenRevolvingLine(ctx, sub, "Alice Line", "EUR", 250_000, 180_000, interest.ACT365, 20_000)
	if err != nil {
		t.Fatalf("OpenRevolvingLine: %v", err)
	}
	txn, err := p.ChargeInterest(ctx, line.ID, day(2025, time.February, 14))
	if err != nil {
		t.Fatalf("ChargeInterest: %v", err)
	}
	if txn.ID != "" {
		t.Errorf("charging an undrawn line posted transaction %s", txn.ID)
	}
	schedule, err := p.Schedule(ctx, line.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(schedule) != 0 {
		t.Errorf("an undrawn line was billed %d instalments, want 0", len(schedule))
	}
}
