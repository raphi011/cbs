package lending_test

// Effective-dated facility terms, from the accrual's side: what a timeline of
// terms rows buys that mutable columns on the facility could not, and what the
// window opening at ORIGINATION rather than at the first advance changes.

import (
	"context"
	"iter"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
)

// expectedFromTimeline sums what a facility SHOULD have accrued, day by day,
// from functions the test states rather than from the implementation under
// test. drawnOn and rateOn are the two things a timeline decides.
func expectedFromTimeline(start time.Time, days int, dc interest.DayCount,
	drawnOn func(n int) ledger.Amount, rateOn func(n int) interest.Rate) interest.Accrued {
	var total interest.Accrued
	for n := 1; n <= days; n++ {
		from := start.AddDate(0, 0, n-1)
		total += interest.Accrue(drawnOn(n), rateOn(n), dc, from, from.AddDate(0, 0, 1))
	}
	return total
}

// A revolving line repriced mid-life accrues each day at the rate in force on
// it, and back-value across the repricing trues up.
func TestARevolvingLineRepricedMidLifeAccruesPerDay(t *testing.T) {
	ctx := context.Background()

	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	const (
		r1      interest.Rate = 120_000 // 12%
		r2      interest.Rate = 180_000 // 18%
		opening ledger.Amount = 100_000 // EUR 1,000 drawn from day 0
		extra   ledger.Amount = 100_000 // a second EUR 1,000, value-dated day 10
	)

	clock := &mutableClock{at: day(0)}
	p, book, sub, customer := newTestPortfolioOn(t, clock.now)

	line, err := p.OpenRevolvingLine(ctx, "Bruno Line", "EUR", 500_000, r1, interest.ACT365, 20_000)
	assertNoError(t, err)
	_, err = p.Draw(ctx, line.ID, customer, opening, "First draw")
	assertNoError(t, err)

	// Thirty days at R1 on the opening draw, run as one end-of-day.
	clock.set(day(30))
	assertNoError(t, p.Accrue(ctx, line.ID, day(30)))

	// Repriced from day 30, entered on day 30 — an ordinary forward repricing.
	repriced, err := p.SetFacilityTerms(ctx, line.ID, r2, interest.ACT365, day(30))
	assertNoError(t, err)
	assertDate(t, "the new row's effective date", repriced.EffectiveFrom, day(30))
	assertDate(t, "the new row's entry date", repriced.CreatedAt, day(30))

	clock.set(day(45))
	assertNoError(t, p.Accrue(ctx, line.ID, day(45)))

	// The back-dated posting: it reaches the ledger on day 45 and takes economic
	// effect on day 10, which is twenty days BEHIND the repricing.
	postTo(t, book, sub, positions(t, p, line.ID).Principal, "EUR", extra, ledger.Debit, day(10))

	clock.set(day(46))
	assertNoError(t, p.Accrue(ctx, line.ID, day(46)))

	// Days 1-9 on 1,000 at R1; days 10-29 on 2,000 at R1 — the stretch that was
	// silently never corrected; days 30-46 on 2,000 at R2.
	want := expectedFromTimeline(day(0), 46, interest.ACT365,
		func(n int) ledger.Amount {
			if n < 10 {
				return opening
			}
			return opening + extra
		},
		func(n int) interest.Rate {
			if n < 30 {
				return r1
			}
			return r2
		})

	got := facility(t, p, line.ID)
	assertEqual(t, "accrued after the back-dated draw", got.Accrued, want)
	// Nothing has been settled off the record — no repayment, no capitalisation —
	// so the whole-life gross and the record agree.
	assertEqual(t, "gross after the back-dated draw", got.AccruedGross, want)

	// The receivable holds Minor() of the record, which is the invariant every
	// caller in this package maintains — and the true-up was POSTED, not just
	// recorded on the row.
	assertEqual(t, "receivable after the true-up", bookBalance(t, book, positions(t, p, got.ID).Receivable), want.Minor())

	// The timeline gained a row and lost nothing: the opening row from
	// OpenRevolvingLine, and the repricing.
	rows, err := p.FacilityTermsHistory(ctx, line.ID)
	assertNoError(t, err)
	assertEqual(t, "timeline length", len(rows), 2)
	assertEqual(t, "first row rate", rows[0].Rate, r1)
	assertEqual(t, "second row rate", rows[1].Rate, r2)
}

// A facility accrues nothing before its first advance, WITHOUT needing a "first
// advance opens the window" state.
func TestAFacilityAccruesNothingBeforeItsFirstAdvance(t *testing.T) {
	ctx := context.Background()

	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	const (
		rate  interest.Rate = 120_000
		drawn ledger.Amount = 100_000
	)

	clock := &mutableClock{at: day(0)}
	p, book, _, customer := newTestPortfolioOn(t, clock.now)

	line, err := p.OpenRevolvingLine(ctx, "Bruno Line", "EUR", 250_000, rate, interest.ACT365, 20_000)
	assertNoError(t, err)

	// Sixty days of an open, priced, undrawn commitment. It costs the borrower
	// nothing, and the facility is still Pending: an advance is what activates
	// it, and no run does.
	clock.set(day(60))
	assertNoError(t, p.Accrue(ctx, line.ID, day(60)))
	idle := facility(t, p, line.ID)
	assertEqual(t, "accrued on an undrawn commitment", idle.Accrued, interest.Accrued(0))
	assertEqual(t, "gross on an undrawn commitment", idle.AccruedGross, interest.Accrued(0))
	assertEqual(t, "status of an undrawn commitment", idle.Status, lending.Pending)
	assertEqual(t, "receivable on an undrawn commitment", bookBalance(t, book, positions(t, p, idle.ID).Receivable), ledger.Amount(0))

	// Drawn on day 60, and accrued through day 90.
	_, err = p.Draw(ctx, line.ID, customer, drawn, "First draw")
	assertNoError(t, err)
	clock.set(day(90))
	assertNoError(t, p.Accrue(ctx, line.ID, day(90)))

	// The whole 90-day life is stated, and the undrawn stretch falls out at zero
	// rather than being excluded by hand — because that is what the code does:
	// every day is re-derived on every run, and days 1-59 contribute nothing
	// because drawn is zero across them.
	want := expectedFromTimeline(day(0), 90, interest.ACT365,
		func(n int) ledger.Amount {
			if n < 60 {
				return 0
			}
			return drawn
		},
		func(int) interest.Rate { return rate })

	got := facility(t, p, line.ID)
	assertEqual(t, "accrued only from the day it was drawn", got.Accrued, want)
	assertEqual(t, "status after the first draw", got.Status, lending.Active)
	assertEqual(t, "receivable", bookBalance(t, book, positions(t, p, got.ID).Receivable), want.Minor())
}

// A facility unpriced then priced accrues only afterwards.
func TestAFacilityUnpricedThenPricedAccruesOnlyAfterwards(t *testing.T) {
	ctx := context.Background()

	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	const (
		rate  interest.Rate = 120_000
		drawn ledger.Amount = 100_000
	)

	clock := &mutableClock{at: day(0)}
	p, book, _, customer := newTestPortfolioOn(t, clock.now)

	// Opened interest-free: the only row is the opening one, at a zero rate.
	line, err := p.OpenRevolvingLine(ctx, "Bruno Line", "EUR", 250_000, 0, interest.ACT365, 20_000)
	assertNoError(t, err)
	_, err = p.Draw(ctx, line.ID, customer, drawn, "First draw")
	assertNoError(t, err)

	clock.set(day(365))
	assertNoError(t, p.Accrue(ctx, line.ID, day(365)))
	free := facility(t, p, line.ID)
	assertEqual(t, "a year of a free facility, drawn", free.Accrued, interest.Accrued(0))

	// Priced from day 365.
	_, err = p.SetFacilityTerms(ctx, line.ID, rate, interest.ACT365, day(365))
	assertNoError(t, err)

	clock.set(day(400))
	assertNoError(t, p.Accrue(ctx, line.ID, day(400)))

	// Days 365-400 only — the whole 400-day life is stated, and the free year
	// falls out at zero rather than being excluded by hand, because that is what
	// the code does: every day is re-derived on every run, and days 1-364
	// contribute nothing because the row in force on them carries a zero rate.
	want := expectedFromTimeline(day(0), 400, interest.ACT365,
		func(int) ledger.Amount { return drawn },
		func(n int) interest.Rate {
			if n < 365 {
				return 0
			}
			return rate
		})

	got := facility(t, p, line.ID)
	assertEqual(t, "accrued only from the day it was priced", got.Accrued, want)
	assertEqual(t, "receivable", bookBalance(t, book, positions(t, p, got.ID).Receivable), want.Minor())
}

// The advancement guard resolves its day count on `date` — meaning on the ROW
// in force on the day being accrued, not on the row the recompute window opens
// at.
func TestTheAdvancementGuardResolvesItsDayCountOnTheAccrualDate(t *testing.T) {
	ctx := context.Background()
	jan1 := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	the30th := time.Date(2025, time.January, 30, 0, 0, 0, 0, time.UTC)
	the31st := time.Date(2025, time.January, 31, 0, 0, 0, 0, time.UTC)

	const (
		rate  interest.Rate = 120_000
		drawn ledger.Amount = 100_000
	)

	// open puts a drawn, priced line on its own portfolio, so a case can add rows
	// of its own before running any days.
	open := func(dc interest.DayCount) (*lending.Portfolio, lending.Facility, *mutableClock) {
		t.Helper()
		clock := &mutableClock{at: jan1}
		p, _, _, customer := newTestPortfolioOn(t, clock.now)
		line, err := p.OpenRevolvingLine(ctx, "Bruno Line", "EUR", 500_000, rate, dc, 20_000)
		assertNoError(t, err)
		_, err = p.Draw(ctx, line.ID, customer, drawn, "First draw")
		assertNoError(t, err)
		return p, line, clock
	}

	// movedOnThe31st runs the 30th and then the 31st, and reports what the second
	// run added.
	movedOnThe31st := func(p *lending.Portfolio, line lending.Facility, clock *mutableClock) interest.Accrued {
		t.Helper()
		clock.set(the30th)
		assertNoError(t, p.Accrue(ctx, line.ID, the30th))
		through30 := facility(t, p, line.ID).AccruedGross

		clock.set(the31st)
		assertNoError(t, p.Accrue(ctx, line.ID, the31st))
		return facility(t, p, line.ID).AccruedGross - through30
	}

	// One row, Thirty360 throughout: the 31st is not a day.
	if moved := movedOnThe31st(open(interest.Thirty360)); moved != 0 {
		t.Errorf("Thirty360 accrued %d on the 31st, want 0: the 31st collapses onto the 30th", moved)
	}
	// One row, ACT365 throughout: it is.
	if moved := movedOnThe31st(open(interest.ACT365)); moved <= 0 {
		t.Errorf("ACT365 accrued %d on the 31st, want a real day", moved)
	}

	// Two rows, same rate, differing ONLY in convention: Thirty360 from 1 January,
	// ACT365 from the 31st.
	p, line, clock := open(interest.Thirty360)
	_, err := p.SetFacilityTerms(ctx, line.ID, rate, interest.ACT365, the31st)
	assertNoError(t, err)

	// Exactly one ACT/365 day on the drawn principal, and no more: the 31st is the
	// span [30th, 31st), which resolves to the row effective the 31st. Derived
	// from the timeline, not read off the run.
	want := interest.Accrue(drawn, rate, interest.ACT365, the30th, the31st)
	assertEqual(t, "the 31st, priced by the row in force on it",
		movedOnThe31st(p, line, clock), want)
}

// A never-priced facility reads no drawn series.
func TestANeverPricedFacilityReadsNoSeries(t *testing.T) {
	ctx := context.Background()

	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	p, _, _, customer := newTestPortfolioOn(t, clock.now)

	line, err := p.OpenRevolvingLine(ctx, "Bella Line", "EUR", 250_000, 0, interest.ACT365, 20_000)
	assertNoError(t, err)
	_, err = p.Draw(ctx, line.ID, customer, 100_000, "First draw")
	assertNoError(t, err)

	clock.set(day(30))
	var scans int
	assertNoError(t, p.Store().Update(ctx, func(ctx context.Context, tx lending.Tx) error {
		return p.AccrueTx(ctx, countingTx{Tx: tx, scans: &scans}, line.ID, day(30))
	}))
	assertEqual(t, "entry scans on a never-priced facility", scans, 0)
	assertEqual(t, "accrued on a never-priced facility", facility(t, p, line.ID).Accrued, interest.Accrued(0))
}

// countingTx wraps a Tx and counts entry scans, so a test can assert that a run
// was skipped BEFORE any I/O rather than that it happened to produce zero.
type countingTx struct {
	lending.Tx
	scans *int
}

func (t countingTx) ScanEntries(ctx context.Context, book ledger.BookID, pos ledger.Position, f ledger.EntryFilter) iter.Seq2[ledger.Entry, error] {
	*t.scans++
	return t.Tx.ScanEntries(ctx, book, pos, f)
}

// Re-disbursement charges the span between a full repayment and the new
// advance's predecessor: those are drawn days, so they are owed.
func TestReDisbursementChargesTheSpanBeforeTheRepayment(t *testing.T) {
	ctx := context.Background()

	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	const principal ledger.Amount = 1_000_000

	clock := &mutableClock{at: day(0)}
	p, book, _, customer := newTestPortfolioOn(t, clock.now)

	loan, err := p.OpenTermLoan(ctx, "Alice Home Loan", "EUR",
		principal, loanRate, loanDayCount, lending.Annuity, 60)
	assertNoError(t, err)
	_, err = p.Disburse(ctx, loan.ID, customer, lending.AddMonths(day(0), 1), "advance")
	assertNoError(t, err)

	// L: accrue through day 10 with the loan fully drawn.
	clock.set(day(10))
	assertNoError(t, p.Accrue(ctx, loan.ID, day(10)))
	atL := facility(t, p, loan.ID)
	grossAtL := expectedFromTimeline(day(0), 10, loanDayCount,
		func(int) ledger.Amount { return principal },
		func(int) interest.Rate { return loanRate })
	assertEqual(t, "gross at L", atL.AccruedGross, grossAtL)

	// R: repay principal AND accrued interest in full on day 15, so drawn goes to
	// zero from the day named 15 onwards.
	clock.set(day(15))
	repayInFull(t, p, loan.ID, customer, day(15))
	drawn, err := p.Drawn(ctx, loan.ID)
	assertNoError(t, err)
	assertEqual(t, "drawn after repaying in full", drawn, ledger.Amount(0))
	atR := facility(t, p, loan.ID)

	// D: re-disburse on day 40. The facility was never closed, so this is
	// allowed — ErrAlreadyDisbursed is guarded on drawn principal, not on status.
	clock.set(day(40))
	_, err = p.Disburse(ctx, loan.ID, customer, lending.AddMonths(day(40), 1), "Re-disbursement")
	assertNoError(t, err)
	beforeReaccrual := facility(t, p, loan.ID)
	assertEqual(t, "the re-disbursement moved no accrual figure",
		beforeReaccrual.AccruedGross, atR.AccruedGross)

	clock.set(day(41))
	assertNoError(t, p.Accrue(ctx, loan.ID, day(41)))
	after := facility(t, p, loan.ID)

	// The whole life re-derived: days 1-14 drawn (the repayment is value-dated day
	// 15, so day 14 is the last day at the old balance), days 15-39 at zero, days
	// 40-41 drawn again (the advance is value-dated day 40, so the day NAMED 40 is
	// the first at the new balance).
	want := expectedFromTimeline(day(0), 41, loanDayCount,
		func(n int) ledger.Amount {
			if n < 15 || n >= 40 {
				return principal
			}
			return 0
		},
		func(int) interest.Rate { return loanRate })
	assertEqual(t, "gross after the re-disbursement's first run", after.AccruedGross, want)

	// Six days were added by that run, and which six is the point: 11, 12, 13 and
	// 14 — drawn, between L and R, and skipped entirely under the clamp — plus 40
	// and 41. The twenty-five days of the idle span R->D add nothing.
	sixDays := want - grossAtL
	assertEqual(t, "the run added six days and no more", sixDays,
		6*interest.Accrue(principal, loanRate, loanDayCount, day(0), day(1)))
	// Recompute's arithmetic, stated rather than observed: Accrued moves by the
	// change in gross, over whatever the repayment left on the record.
	assertEqual(t, "accrued after the re-disbursement's first run",
		after.Accrued, atR.Accrued+sixDays)

	// The receivable still holds Minor() of the record, which is the invariant a
	// whole-life recompute must not break.
	assertEqual(t, "receivable", bookBalance(t, book, positions(t, p, after.ID).Receivable), after.Accrued.Minor())
	assertDate(t, "LastAccrualDate", after.LastAccrualDate, day(41))

	// And the idle span accrues nothing, asserted directly rather than inferred
	// from the six days above: a second loan driven through the same disburse,
	// accrue-to-L, repay-at-R sequence, accrued through R and then on to D WITHOUT
	// being re-disbursed.
	assertEqual(t, "an idle span accrues nothing",
		accrueControlThroughIdleSpan(t, day), interest.Accrued(0))
}

// accrueControlThroughIdleSpan drives a second loan through the same
// disburse/accrue/repay sequence as the test above, accrues it through R —
// which is what separates the DRAWN L->R span from the idle one — and then on
// to day 40 without re-disbursing.
func accrueControlThroughIdleSpan(t *testing.T, day func(int) time.Time) interest.Accrued {
	t.Helper()
	ctx := context.Background()

	clock := &mutableClock{at: day(0)}
	p, _, _, customer := newTestPortfolioOn(t, clock.now)

	loan, err := p.OpenTermLoan(ctx, "Control Loan", "EUR",
		1_000_000, loanRate, loanDayCount, lending.Annuity, 60)
	assertNoError(t, err)
	_, err = p.Disburse(ctx, loan.ID, customer, lending.AddMonths(day(0), 1), "advance")
	assertNoError(t, err)

	clock.set(day(10))
	assertNoError(t, p.Accrue(ctx, loan.ID, day(10)))
	clock.set(day(15))
	repayInFull(t, p, loan.ID, customer, day(15))

	// Through R: days 11-14 are drawn and are charged here, so what the next run
	// adds is the idle span alone.
	assertNoError(t, p.Accrue(ctx, loan.ID, day(15)))
	throughR := facility(t, p, loan.ID).AccruedGross

	clock.set(day(40))
	assertNoError(t, p.Accrue(ctx, loan.ID, day(40)))
	return facility(t, p, loan.ID).AccruedGross - throughR
}

// repayInFull settles everything a facility owes — the receivable first, then the
// principal, which is Repay's own allocation — value-dated on the given day.
func repayInFull(t *testing.T, p *lending.Portfolio, id lending.FacilityID, counterparty ledger.Position, date time.Time) {
	t.Helper()
	ctx := context.Background()
	owed, err := p.Outstanding(ctx, id)
	assertNoError(t, err)
	_, err = p.Repay(ctx, id, counterparty, owed, date, "settle in full")
	assertNoError(t, err)
}
