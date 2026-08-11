package lending_test

import (
	"context"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
)

// loanRate and loanDayCount are the terms disbursedLoan opens at. They are
// constants here rather than fields read back off the facility, because a
// facility no longer carries either: they live in its FacilityTerms timeline,
// and a test that wants the rate a figure was derived at is better off stating
// it than resolving it.
const (
	loanRate     interest.Rate     = 60_000 // 6%
	loanDayCount interest.DayCount = interest.ACT365
)

// disbursedLoan is a €10,000 five-year annuity at 6%, paid out on 15 January.
func disbursedLoan(t *testing.T) (*lending.Portfolio, *ledger.Book, lending.Facility, ledger.Position) {
	t.Helper()
	p, book, _, loan, customer := disbursedLoanIn(t)
	return p, book, loan, customer
}

// disbursedLoanIn is disbursedLoan plus the subledger the loan was filed in.
// postTo has to create its counterparty accounts somewhere, and a fixture that
// kept the subledger to itself would make every backdating test below build its
// own loan by hand.
func disbursedLoanIn(t *testing.T) (*lending.Portfolio, *ledger.Book, ledger.SubledgerID, lending.Facility, ledger.Position) {
	t.Helper()
	ctx := context.Background()
	p, book, sub, customer := newTestPortfolio(t)

	loan, err := p.OpenTermLoan(ctx, "Alice Home Loan", "EUR", 1_000_000, loanRate, loanDayCount, lending.Annuity, 60)
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
	return p, book, sub, after, customer
}

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// drawdown is the day disbursedLoan's money reaches the borrower: the test
// portfolio's clock reads 15 January and never moves, so the loan is opened and
// disbursed on the same day. That is also the day its recompute window opens —
// the window opens at ORIGINATION now, and here the two coincide.
var drawdown = day(2025, time.January, 15)

// postTo posts a movement onto one of a facility's GL accounts against a
// throwaway counterparty, value-dated as given.
//
// It stands in for a posting that reached the ledger without passing through
// this layer — a repayment keyed in late and backdated to the day it took
// effect, an advance value-dated forward — which is the only way to produce the
// case the recompute exists for. deposit's test package has the same helper for
// the same reason; the two are not shared because neither package imports the
// other.
func postTo(t *testing.T, book *ledger.Book, sub ledger.SubledgerID, pos ledger.Position, asset ledger.AssetCode, amount ledger.Amount, dir ledger.Direction, value time.Time) {
	t.Helper()
	ctx := context.Background()
	// The contra is picked so that it absorbs the movement rather than being
	// driven below zero, which the ledger refuses for Asset and Expense
	// accounts: a Liability is credited, an Asset debited.
	contraType, other := ledger.Liability, ledger.Credit
	if dir == ledger.Credit {
		contraType, other = ledger.Asset, ledger.Debit
	}
	contra, err := book.CreateAccount(ctx, sub, "Counterparty "+pos.String(), contraType, asset)
	if err != nil {
		t.Fatalf("counterparty: %v", err)
	}
	if _, err := book.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "test movement",
		ValueDate:   value,
		Entries: []ledger.Entry{
			{AccountID: pos.Account, Subsidiary: pos.Subsidiary, Amount: amount, Direction: dir},
			{AccountID: contra.ID, Amount: amount, Direction: other},
		},
	}); err != nil {
		t.Fatalf("post: %v", err)
	}
}

// accrueDays runs the daily accrual for days 1..n after drawdown, the way an
// end-of-day batch would.
func accrueDays(t *testing.T, p *lending.Portfolio, id lending.FacilityID, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 1; i <= n; i++ {
		if err := p.Accrue(ctx, id, drawdown.AddDate(0, 0, i)); err != nil {
			t.Fatalf("Accrue day %d: %v", i, err)
		}
	}
}

func facility(t *testing.T, p *lending.Portfolio, id lending.FacilityID) lending.Facility {
	t.Helper()
	f, err := p.GetFacility(context.Background(), id)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	return f
}

// accountNamed finds one of the bank's own accounts by the name the lending
// layer creates it under, walking the book because those accounts are
// materialised lazily and no caller is handed their ID.
// chartSize is how many rows the whole chart of accounts holds. It is what a
// test asserts on when the claim is that a borrower does not add one.
func chartSize(t *testing.T, book *ledger.Book) int {
	t.Helper()
	ctx := context.Background()
	ledgers, err := book.ListLedgers(ctx)
	if err != nil {
		t.Fatalf("ListLedgers: %v", err)
	}
	n := 0
	for _, l := range ledgers {
		subs, err := book.ListSubledgers(ctx, l.ID)
		if err != nil {
			t.Fatalf("ListSubledgers: %v", err)
		}
		for _, sub := range subs {
			accounts, err := book.ListAccounts(ctx, sub.ID)
			if err != nil {
				t.Fatalf("ListAccounts: %v", err)
			}
			n += len(accounts)
		}
	}
	return n
}

func accountNamed(t *testing.T, book *ledger.Book, name string) ledger.AccountID {
	t.Helper()
	ctx := context.Background()
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
			accounts, err := book.ListAccounts(ctx, s.ID)
			if err != nil {
				t.Fatalf("ListAccounts: %v", err)
			}
			for _, a := range accounts {
				if a.Name == name {
					return a.ID
				}
			}
		}
	}
	t.Fatalf("no account named %q in the book", name)
	return ""
}

// positions is where a facility's money is — the three control accounts, each
// under the facility's own id. Resolved through the portfolio rather than
// rebuilt here, so a test asserts against the same answer the posting used.
func positions(t *testing.T, p *lending.Portfolio, id lending.FacilityID) lending.FacilityPositions {
	t.Helper()
	at, err := p.Positions(context.Background(), id)
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	return at
}

func bookBalance(t *testing.T, book *ledger.Book, pos ledger.Position) ledger.Amount {
	t.Helper()
	bal, err := book.BookBalance(context.Background(), pos)
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	return bal
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
		balance, err := book.BookBalance(ctx, positions(t, p, got.ID).Receivable)
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
	p, _, _, _ := newTestPortfolio(t)

	// An open but undrawn commitment costs the borrower nothing.
	line, err := p.OpenRevolvingLine(ctx, "Alice Line", "EUR", 250_000, 180_000, interest.ACT365, 20_000)
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
	p, book, _, customer := newTestPortfolio(t)

	// €1,000 drawn at 18% ACT/365, minimum payment 2% of the balance.
	line, err := p.OpenRevolvingLine(ctx, "Alice Line", "EUR", 250_000, 180_000, interest.ACT365, 20_000)
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
	charge, err := p.ChargeInterest(ctx, line.ID, statement)
	if err != nil {
		t.Fatalf("ChargeInterest: %v", err)
	}
	if len(charge.Transaction.Entries) != 2 {
		t.Fatalf("charge posted %d entries, want 2", len(charge.Transaction.Entries))
	}
	if !charge.Billed() || charge.Installment.Seq != 1 {
		t.Fatalf("charge billed %+v, want the first cycle", charge.Installment)
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
	receivable, err := book.BookBalance(ctx, positions(t, p, after.ID).Receivable)
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

// TestChargeInterest_NegativeResidueStaysInStepWithTheLedger covers the other
// side of the rounding from TestChargeInterest_CapitalizesAndBillsTheCycle: a
// cycle whose fractional accrual is ABOVE half a minor unit, so Minor() rounds
// UP and charging it leaves the record negative rather than positive. The
// invariant under test is the same either way — Minor() of the residue is
// still 0, so the GL and the record stay in step — but a clamp-to-zero
// "cleanup" of the residue would only be caught by exercising this branch too.
func TestChargeInterest_NegativeResidueStaysInStepWithTheLedger(t *testing.T) {
	ctx := context.Background()
	p, book, _, customer := newTestPortfolio(t)

	// €1,000 drawn at 20% ACT/365, minimum payment 2% of the balance.
	line, err := p.OpenRevolvingLine(ctx, "Alice Line", "EUR", 250_000, 200_000, interest.ACT365, 20_000)
	if err != nil {
		t.Fatalf("OpenRevolvingLine: %v", err)
	}
	if _, err := p.Draw(ctx, line.ID, customer, 100_000, "First draw"); err != nil {
		t.Fatalf("Draw: %v", err)
	}

	// One day at 20% ACT/365: 100,000 × 200,000 × 1 / 365 = 20,000,000,000 /
	// 365 = 54,794,520 (integer division truncates the remaining .5479... of
	// a micro-minor-unit away) — i.e. 54.794520 minor units, a fraction ABOVE
	// one half, unlike the 30-day, 18%-rate cycle in the capitalization test
	// above (which lands on 0.45204, below one half).
	if err := p.Accrue(ctx, line.ID, day(2025, time.January, 16)); err != nil {
		t.Fatalf("Accrue: %v", err)
	}
	before, err := p.GetFacility(ctx, line.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	if before.Accrued != 54_794_520 {
		t.Fatalf("accrued after one day = %d, want 54794520", before.Accrued)
	}

	// Minor(54,794,520): half away from zero rounds 54.794520 up to 55.
	if _, err := p.ChargeInterest(ctx, line.ID, day(2025, time.January, 16)); err != nil {
		t.Fatalf("ChargeInterest: %v", err)
	}

	// FromMinor(55) = 55,000,000, which is 205,480 MORE than the 54,794,520
	// actually earned: the bank charged 0.205480 minor units more than it had
	// earned, so the record goes negative by exactly that amount.
	after, err := p.GetFacility(ctx, line.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	if after.Accrued != -205_480 {
		t.Errorf("residue = %d, want -205480", after.Accrued)
	}
	if after.Accrued.Minor() != 0 {
		t.Errorf("residue rounds to %d, want 0", after.Accrued.Minor())
	}

	// The GL receivable must still equal Minor() of the (now negative) record
	// — 0 — which is the whole point: the invariant holds on both sides of
	// the rounding threshold, not just the one the capitalization test above
	// happens to land on.
	receivable, err := book.BookBalance(ctx, positions(t, p, after.ID).Receivable)
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	if receivable != 0 {
		t.Errorf("receivable after capitalization = %d, want 0", receivable)
	}

	// Capitalization still moved 55 into principal: drawn = 100,000 + 55.
	drawn, err := p.Drawn(ctx, line.ID)
	if err != nil {
		t.Fatalf("Drawn: %v", err)
	}
	if drawn != 100_055 {
		t.Errorf("drawn after capitalization = %d, want 100055", drawn)
	}
	schedule, err := p.Schedule(ctx, line.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(schedule) != 1 {
		t.Fatalf("instalments after one cycle = %d, want 1", len(schedule))
	}
	if schedule[0].Interest != 55 {
		t.Errorf("cycle interest = %d, want 55", schedule[0].Interest)
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
	p, _, _, _ := newTestPortfolio(t)

	line, err := p.OpenRevolvingLine(ctx, "Alice Line", "EUR", 250_000, 180_000, interest.ACT365, 20_000)
	if err != nil {
		t.Fatalf("OpenRevolvingLine: %v", err)
	}
	charge, err := p.ChargeInterest(ctx, line.ID, day(2025, time.February, 14))
	if err != nil {
		t.Fatalf("ChargeInterest: %v", err)
	}
	if charge.Posted() {
		t.Errorf("charging an undrawn line posted transaction %s", charge.Transaction.ID)
	}
	if charge.Billed() {
		t.Errorf("charging an undrawn line billed cycle %+v", charge.Installment)
	}
	schedule, err := p.Schedule(ctx, line.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(schedule) != 0 {
		t.Errorf("an undrawn line was billed %d instalments, want 0", len(schedule))
	}
}

// TestChargeInterest_ADrawnLineWithNoInterestStillBillsACycle covers the third
// outcome, between the two the name "charge interest" suggests: a line that is
// drawn but whose accrual has not yet reached a whole minor unit posts NOTHING
// and still bills a cycle. It is reachable by drawing and charging before the
// first end-of-day, and it is why a bare transaction cannot describe the
// result — a caller seeing no transaction would report that nothing happened
// while the schedule below it gained a row.
func TestChargeInterest_ADrawnLineWithNoInterestStillBillsACycle(t *testing.T) {
	ctx := context.Background()
	p, book, _, customer := newTestPortfolio(t)

	line, err := p.OpenRevolvingLine(ctx, "Alice Line", "EUR", 250_000, 180_000, interest.ACT365, 20_000)
	if err != nil {
		t.Fatalf("OpenRevolvingLine: %v", err)
	}
	if _, err := p.Draw(ctx, line.ID, customer, 100_000, "First draw"); err != nil {
		t.Fatalf("Draw: %v", err)
	}

	// Charged the same day it was drawn: nothing has accrued yet.
	charge, err := p.ChargeInterest(ctx, line.ID, day(2025, time.January, 15))
	if err != nil {
		t.Fatalf("ChargeInterest: %v", err)
	}
	if charge.Posted() {
		t.Errorf("capitalized %s with nothing accrued", charge.Transaction.ID)
	}
	if !charge.Billed() {
		t.Fatal("a drawn line billed no cycle")
	}
	if charge.Installment.Interest != 0 {
		t.Errorf("cycle interest = %d, want 0", charge.Installment.Interest)
	}
	// The minimum payment is still 2% of the €1,000 drawn.
	if charge.Installment.Principal != 2_000 {
		t.Errorf("cycle minimum = %d, want 2000", charge.Installment.Principal)
	}

	schedule, err := p.Schedule(ctx, line.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(schedule) != 1 {
		t.Fatalf("instalments after the cycle = %d, want 1", len(schedule))
	}

	// Principal is untouched: there was nothing to capitalize into it.
	drawn, err := p.Drawn(ctx, line.ID)
	if err != nil {
		t.Fatalf("Drawn: %v", err)
	}
	if drawn != 100_000 {
		t.Errorf("drawn = %d, want 100000 (nothing capitalized)", drawn)
	}
	after, err := p.GetFacility(ctx, line.ID)
	if err != nil {
		t.Fatalf("GetFacility: %v", err)
	}
	receivable, err := book.BookBalance(ctx, positions(t, p, after.ID).Receivable)
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	if receivable != 0 {
		t.Errorf("receivable = %d, want 0", receivable)
	}
}

// TestChargeInterest_RefusesACycleAlreadyBilled pins idempotency. Unlike
// accrual, which LastAccrualDate makes a no-op on a re-run, billing appends a
// row and capitalizes the receivable — so a retried request (a proxy retry, a
// double-submitted form) would otherwise leave the borrower owing two minimum
// payments for one cycle and drifting into arrears from an infrastructure
// event.
func TestChargeInterest_RefusesACycleAlreadyBilled(t *testing.T) {
	ctx := context.Background()
	p, _, _, customer := newTestPortfolio(t)

	line, err := p.OpenRevolvingLine(ctx, "Alice Line", "EUR", 250_000, 180_000, interest.ACT365, 20_000)
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

	statement := day(2025, time.February, 14)
	if _, err := p.ChargeInterest(ctx, line.ID, statement); err != nil {
		t.Fatalf("ChargeInterest: %v", err)
	}
	drawn, err := p.Drawn(ctx, line.ID)
	if err != nil {
		t.Fatalf("Drawn: %v", err)
	}

	_, err = p.ChargeInterest(ctx, line.ID, statement)
	assertErrorIs(t, err, lending.ErrCycleAlreadyBilled)

	// Nothing moved on the retry: no second cycle, and no second
	// capitalization into principal.
	schedule, err := p.Schedule(ctx, line.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(schedule) != 1 {
		t.Fatalf("instalments after the retry = %d, want 1", len(schedule))
	}
	again, err := p.Drawn(ctx, line.ID)
	if err != nil {
		t.Fatalf("Drawn: %v", err)
	}
	if again != drawn {
		t.Errorf("drawn after the retry = %d, want %d", again, drawn)
	}

	// A DIFFERENT cycle on the same line is still billable, including a
	// backdated one: charging out of chronological order is supported, and
	// only the due date already on the schedule is refused.
	if _, err := p.ChargeInterest(ctx, line.ID, day(2025, time.January, 14)); err != nil {
		t.Fatalf("ChargeInterest for an earlier cycle: %v", err)
	}
	schedule, err = p.Schedule(ctx, line.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(schedule) != 2 {
		t.Fatalf("instalments after a backdated cycle = %d, want 2", len(schedule))
	}
}

// ---------------------------------------------------------------------------
// The recompute: backdated postings, and the corrections they produce
// ---------------------------------------------------------------------------

func TestAccrue_CorrectsABackdatedRepayment(t *testing.T) {
	ctx := context.Background()
	p, book, sub, loan, _ := disbursedLoanIn(t)

	accrueDays(t, p, loan.ID, 10)
	ten := facility(t, p, loan.ID)
	// €10,000 at 6% ACT/365 is 164.383561 cents a day.
	if ten.Accrued != 1_643_835_610 {
		t.Fatalf("accrued over ten drawn days = %d, want 1643835610", ten.Accrued)
	}
	if got := bookBalance(t, book, positions(t, p, loan.ID).Receivable); got != 1644 {
		t.Fatalf("receivable after ten days = %d, want 1644", got)
	}

	// Half the principal was repaid on day 3 and only reaches the ledger now:
	// from day 3 onward less was owed than the accrual assumed.
	postTo(t, book, sub, positions(t, p, loan.ID).Principal, loan.Asset, 500_000, ledger.Credit, drawdown.AddDate(0, 0, 3))

	if err := p.Accrue(ctx, loan.ID, drawdown.AddDate(0, 0, 11)); err != nil {
		t.Fatalf("Accrue: %v", err)
	}
	got := facility(t, p, loan.ID)
	if got.Accrued >= ten.Accrued {
		t.Errorf("accrued after the backdated repayment = %d, want less than %d", got.Accrued, ten.Accrued)
	}

	// "Less than before" is not enough on its own: the whole window is
	// re-derived at the balance each day actually carried, and only a recompute
	// reaches this figure.
	//
	//	days 1-2   at 1_000_000  2 × 164_383_561 =   328_767_122
	//	days 3-11  at   500_000  9 ×  82_191_780 =   739_726_020
	//	                                         = 1_068_493_142 -> 1068 cents
	if got.Accrued != 1_068_493_142 {
		t.Errorf("accrued after the backdated repayment = %d, want 1068493142", got.Accrued)
	}
	// AccruedGross is that whole window total. Accrued equals it here only
	// because nothing has been settled off the record yet.
	if got.AccruedGross != 1_068_493_142 {
		t.Errorf("gross = %d, want 1068493142", got.AccruedGross)
	}
	// The correction credited the receivable, which still holds Minor() of the
	// record — the invariant every caller in this package depends on.
	if recv := bookBalance(t, book, positions(t, p, loan.ID).Receivable); recv != got.Accrued.Minor() {
		t.Errorf("receivable = %d, accrued = %d; the two must agree", recv, got.Accrued.Minor())
	}
	if recv := bookBalance(t, book, positions(t, p, loan.ID).Receivable); recv != 1068 {
		t.Errorf("receivable = %d, want 1068", recv)
	}
}

func TestAccrue_IgnoresAForwardValueDatedAdvance(t *testing.T) {
	ctx := context.Background()
	p, book, sub, loan, _ := disbursedLoanIn(t)

	accrueDays(t, p, loan.ID, 1)
	baseline := facility(t, p, loan.ID).Accrued

	// A second advance, booked today but value-dated five days out. It has not
	// taken economic effect, so it is not part of any day being accrued.
	postTo(t, book, sub, positions(t, p, loan.ID).Principal, loan.Asset, 1_000_000, ledger.Debit, drawdown.AddDate(0, 0, 5))

	if err := p.Accrue(ctx, loan.ID, drawdown.AddDate(0, 0, 2)); err != nil {
		t.Fatalf("Accrue: %v", err)
	}
	oneMoreDay := facility(t, p, loan.ID).Accrued

	// Day 2 accrued on the original €10,000, so its increment matches day 1's.
	if got, want := oneMoreDay-baseline, baseline; got != want {
		t.Errorf("second day accrued %d, want %d; the forward-dated advance has not taken effect", got, want)
	}
}

// TestAccrue_CorrectionRefundsToPrincipal covers the case the receivable cannot
// absorb: interest the borrower has already paid, which a backdated repayment
// then shows was never owed. It is credited to principal — they owe less — and
// that is what makes the following day accrue on a smaller basis.
func TestAccrue_CorrectionRefundsToPrincipal(t *testing.T) {
	ctx := context.Background()
	p, book, sub, loan, customer := disbursedLoanIn(t)

	accrueDays(t, p, loan.ID, 30)
	// Settle the accrued interest, emptying the receivable.
	settled, err := p.AccruedInterest(ctx, loan.ID)
	if err != nil {
		t.Fatalf("AccruedInterest: %v", err)
	}
	if _, err := p.Repay(ctx, loan.ID, customer, settled, drawdown.AddDate(0, 0, 30), "Interest"); err != nil {
		t.Fatalf("Repay: %v", err)
	}
	if recv := bookBalance(t, book, positions(t, p, loan.ID).Receivable); recv != 0 {
		t.Fatalf("receivable after repayment = %d, want 0", recv)
	}
	principalBefore := bookBalance(t, book, positions(t, p, loan.ID).Principal)

	// All but €1,000 of the loan was repaid on day one, backdated.
	postTo(t, book, sub, positions(t, p, loan.ID).Principal, loan.Asset, 900_000, ledger.Credit, drawdown)
	if err := p.Accrue(ctx, loan.ID, drawdown.AddDate(0, 0, 31)); err != nil {
		t.Fatalf("Accrue: %v", err)
	}

	recv := bookBalance(t, book, positions(t, p, loan.ID).Receivable)
	if recv < 0 {
		t.Errorf("receivable = %d; the correction must clamp rather than drive an Asset negative", recv)
	}
	principalAfter := bookBalance(t, book, positions(t, p, loan.ID).Principal)
	// 31 days on €1,000 is 509_589_036, against 4_931_506_830 charged on
	// €10,000 — 4422 cents of it already paid, so 4422 comes back off what is
	// still owed.
	if want := principalBefore - 900_000 - 4_422; principalAfter != want {
		t.Errorf("principal %d -> %d, want %d; the unabsorbed correction must reduce what the borrower owes",
			principalBefore, principalAfter, want)
	}
	// The refunded part has been settled in cash, so it comes back on to the
	// record: leaving the record negative by it would suppress the next 4422
	// cents of interest genuinely owed and hand the borrower the money twice.
	got := facility(t, p, loan.ID)
	if recv != got.Accrued.Minor() {
		t.Errorf("receivable = %d, accrued = %d; a refund left on the record is money given back twice",
			recv, got.Accrued.Minor())
	}
}

// TestAccrue_CorrectionClampsToWhatTheFacilityOwes is the far end of the same
// case. Principal is an Asset too, so a correction bigger than everything still
// outstanding cannot go there either: the borrower has overpaid the bank
// outright. It must clamp rather than refuse — a refusal inside an end-of-day
// batch takes the whole book's run down — and what is left over is a debt the
// bank owes a customer, so it lands in the interest-refunds-payable account. It
// must not simply be dropped, which leaves income overstated by money the bank
// is not owed and no account anywhere recording the obligation.
func TestAccrue_CorrectionClampsToWhatTheFacilityOwes(t *testing.T) {
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
	// The bank has recognised the whole 4932 as income at this point.
	income := accountNamed(t, book, "Interest Income (EUR)")
	if got := bookBalance(t, book, income.Total()); got != 4_932 {
		t.Fatalf("interest income before the correction = %d, want 4932", got)
	}

	// The whole loan was repaid on day one, backdated: no interest was ever
	// owed, and neither the receivable nor principal has anything left to take
	// the correction.
	postTo(t, book, sub, positions(t, p, loan.ID).Principal, loan.Asset, 1_000_000, ledger.Credit, drawdown)
	if err := p.Accrue(ctx, loan.ID, drawdown.AddDate(0, 0, 31)); err != nil {
		t.Fatalf("Accrue: %v", err)
	}

	if drawn := bookBalance(t, book, positions(t, p, loan.ID).Principal); drawn != 0 {
		t.Errorf("principal = %d, want 0; the correction must not drive it negative", drawn)
	}
	recv := bookBalance(t, book, positions(t, p, loan.ID).Receivable)
	if recv != 0 {
		t.Errorf("receivable = %d, want 0", recv)
	}
	if got := facility(t, p, loan.ID); got.Accrued.Minor() != recv {
		t.Errorf("receivable = %d, accrued = %d; the two must agree wherever the correction was settled",
			recv, got.Accrued.Minor())
	}

	// The whole 4932 the borrower paid and never owed is now a debt the bank
	// records against itself, and income is back to nothing earned. It is
	// recorded UNDER THIS FACILITY on the bank's one refunds-payable line — a
	// balance pooled with no subsidiary could not say which borrower is owed the
	// 4932, and it is the subsidiary that makes the pool answerable.
	payable := accountNamed(t, book, "Interest Refunds Payable (EUR)")
	owed := positions(t, p, loan.ID).Payable
	if owed.Account != payable {
		t.Errorf("payable position = %s, want a subsidiary under %s", owed, payable)
	}
	if got := bookBalance(t, book, owed); got != 4_932 {
		t.Errorf("interest refunds payable = %d, want 4932; the overpayment must be recorded, not kept", got)
	}
	if got := bookBalance(t, book, income.Total()); got != 0 {
		t.Errorf("interest income after the correction = %d, want 0; no interest was ever owed", got)
	}
	// And nothing is stranded on the facility: it can be closed.
	if err := p.Close(ctx, loan.ID); err != nil {
		t.Errorf("Close after a clamped correction: %v", err)
	}
}

// TestAccrue_RefundFeedsTheFollowingDaysBasis pins the one feedback loop in
// this design: a refund credits PrincipalGL, which IS the accrual basis, so the
// days after it accrue on less.
func TestAccrue_RefundFeedsTheFollowingDaysBasis(t *testing.T) {
	ctx := context.Background()
	p, book, sub, loan, customer := disbursedLoanIn(t)

	accrueDays(t, p, loan.ID, 30)
	settled, err := p.AccruedInterest(ctx, loan.ID)
	if err != nil {
		t.Fatalf("AccruedInterest: %v", err)
	}
	if _, err := p.Repay(ctx, loan.ID, customer, settled, drawdown.AddDate(0, 0, 30), "Interest"); err != nil {
		t.Fatalf("Repay: %v", err)
	}

	postTo(t, book, sub, positions(t, p, loan.ID).Principal, loan.Asset, 900_000, ledger.Credit, drawdown)
	if err := p.Accrue(ctx, loan.ID, drawdown.AddDate(0, 0, 31)); err != nil {
		t.Fatalf("Accrue day 31: %v", err)
	}
	drawnAfterCorrection := bookBalance(t, book, positions(t, p, loan.ID).Principal)
	if drawnAfterCorrection >= 100_000 {
		t.Fatalf("principal after the correction = %d, want less than 100000", drawnAfterCorrection)
	}

	// Day 32 is the run that absorbs the correction's own value date. The
	// refund is value-dated day 31, and a movement value-dated on a day is in
	// force for the day ENDING on it (ledger.Series and interest.AccrueSeries
	// are both explicit about this), so day 31 — already accrued at the old
	// basis — is re-derived here too. That is ordinary behaviour for any
	// posting made after a day's end-of-day, not something the correction does
	// specially, and it is why the clean single-day comparison is day 33.
	if err := p.Accrue(ctx, loan.ID, drawdown.AddDate(0, 0, 32)); err != nil {
		t.Fatalf("Accrue day 32: %v", err)
	}
	before := facility(t, p, loan.ID).Accrued
	if err := p.Accrue(ctx, loan.ID, drawdown.AddDate(0, 0, 33)); err != nil {
		t.Fatalf("Accrue day 33: %v", err)
	}
	after := facility(t, p, loan.ID).Accrued

	want := interest.Accrue(drawnAfterCorrection, loanRate, loanDayCount,
		drawdown.AddDate(0, 0, 32), drawdown.AddDate(0, 0, 33))
	if got := after - before; got != want {
		t.Errorf("day 33 accrued %d, want %d on the post-refund principal %d", got, want, drawnAfterCorrection)
	}
	// And that is strictly less than a day on the basis before the refund.
	full := interest.Accrue(100_000, loanRate, loanDayCount,
		drawdown.AddDate(0, 0, 32), drawdown.AddDate(0, 0, 33))
	if want >= full {
		t.Errorf("post-refund day = %d, want less than %d; the refund must reduce the basis", want, full)
	}
}
