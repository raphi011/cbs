package lending_test

// Effective-dated facility terms, from the accrual's side: what a timeline of
// terms rows buys that mutable columns on the facility could not, and what the
// window opening at ORIGINATION rather than at the first advance changes.
//
// Every figure here is derived with expectedFromTimeline — a day-by-day sum
// stated by the test — rather than copied out of what the implementation prints.
// The fixtures and the small assert helpers these tests share live in
// portfolio_test.go and accrual_test.go, which is also where the frozen-clock
// fixtures are; these tests run on a clock they move, because when an operation
// happened is part of what is under test.

import (
	"context"
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
)

// expectedFromTimeline sums what a facility SHOULD have accrued, day by day,
// from functions the test states rather than from the implementation under test.
// drawnOn and rateOn are the two things a timeline decides.
//
// It is deliberately the same shape as interest.perDay: one Accrue call per day,
// so the per-call integer truncation lands identically. Interest over an N-day
// span is NOT the sum of its N single days — Accrue divides once per call — so a
// span-at-a-time expectation would disagree with the engine by a few
// micro-minor-units and nobody would know which was right.
//
// Both closures are indexed by the same thing, on the one day axis the engine
// uses: a span is NAMED BY ITS END DATE, so span n is [start+n-1, start+n) and n
// runs 1..days. interest.AccrueSeries is what fixes that — a movement
// value-dated V ends the preceding run at V-1 and so first bites on [V-1, V) —
// and lending's accrual resolves termsAt on the same `to`. So both closures read
// straight off the dates a test states:
//
//   - drawnOn(n) is the drawn principal a movement value-dated start+n puts in
//     force.
//   - rateOn(n) is the rate a terms row effective from start+n puts in force.
//
// deposit's test package has the same helper for the same reason; the two are
// not shared because neither package imports the other.
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
// it, and back-value across the repricing trues up. It is the lending mirror of
// deposit's TestBackValueAcrossARepricingTruesUp, and it is the reason
// effective-dated terms exist on this side too.
//
// Priced at R1 from day 0 and repriced to R2 effective day 30 (allowed: a line
// has no schedule to diverge from). On day 45 a further draw lands value-dated
// day 10: the next run re-derives days 10-29 AT R1, with the larger balance in
// place, and posts the delta. Under the mutable-columns model the window started
// at the last repricing, so those days were behind it and were silently never
// corrected.
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
	// silently never corrected; days 30-46 on 2,000 at R2. Both boundaries read
	// straight off the dates above, because a day is named by the date its span
	// ends on and both the value date and the effective date are on that axis:
	//
	//	 9 × 100_000 × 120_000 / 365 =  9 × 32_876_712 =   295_890_408
	//	20 × 200_000 × 120_000 / 365 = 20 × 65_753_424 = 1_315_068_480
	//	17 × 200_000 × 180_000 / 365 = 17 × 98_630_136 = 1_676_712_312
	//	                                                 3_287_671_200
	//
	// Truncation is per day, not per span-group, which is why this is computed
	// rather than written down: Accrue divides once per call.
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
//
// That state was an optimisation expressed as a guard: the drawn series is zero
// across every day before the money went out, so those days re-derive to zero on
// their own. Removing it is what let the window open at origination, which is
// what makes a backdated posting correctable wherever it lands.
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
	//
	// The drawn stretch is 31 days, not 30: a day is named by the date its span
	// ends on, so the draw value-dated day 60 is in force for the day named 60 —
	// the span [59, 60) — and the last day accrued is day 90.
	//
	//	31 × 100_000 × 120_000 / 365 = 31 × 32_876_712 = 1_019_178_072
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
//
// The previous model could not express this at all: Rate was per-facility, so
// pricing it would have re-derived the free year at the new rate or, with the
// window reset, thrown that year away. A zero rate is a real product — an
// interest-free staff loan, a promotional line — and it is now a property of a
// DAY rather than of the facility.
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
	//
	// The priced stretch is 36 days, not 35: a day is named by the date its span
	// ends on, so the row effective day 365 prices day 365 — the span [364, 365)
	// — and the last day accrued is day 400.
	//
	//	36 × 100_000 × 120_000 / 365 = 36 × 32_876_712 = 1_183_561_632
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

// The advancement guard resolves its day count on `date` — meaning on the ROW in
// force on the day being accrued, not on the row the recompute window opens at.
// It is deposit's TestTheAdvancementGuardResolvesItsDayCountOnTheAccrualDate,
// applied to a facility's drawn principal, and it exists for the same reason.
//
// There is no single DayCount to ask any more: it is a terms field, one per row,
// and the conventions genuinely disagree about whether a window advanced. Under
// Thirty360 the 31st collapses onto the 30th, so Days(30th, 31st) is zero while
// ACT365 says one — a run on the 31st is a no-op under one convention and a real
// day under the other.
//
// Three cases, because the first two only pin half the claim. They hold the
// timeline to a SINGLE row, so they discriminate which CONVENTION the guard
// applies but not which row supplies it — with only those two, taking the day
// count from rows[0] instead of from the accrual date's row passes. The third
// gives one facility two rows differing in nothing but their convention, so the
// row choice is the only thing that can move the figure.
//
// Only one direction of that third case is observable, which is worth knowing
// before someone adds its mirror image expecting symmetry. When the guard wrongly
// REFUSES a run the day is never accrued and the difference shows; when it
// wrongly ALLOWS one, the per-day closure resolves the row the guard should have
// used and prices that span at zero days anyway, so the delta is zero and the
// mistake hides. The guard's row choice matters exactly where a real day would
// otherwise be silently dropped.
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
	// of its own before running any days. A revolving line rather than a term
	// loan, because a term loan cannot be repriced once it has a schedule and the
	// third case below adds a row after the money is out.
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
	// run added. It reads AccruedGross rather than Accrued so that nothing but
	// the arithmetic can move it — no capitalisation happens here, but the two
	// are the same figure only for as long as that stays true.
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
	// ACT365 from the 31st. The window opens on the Thirty360 row and the day
	// being accrued belongs to the ACT365 one, so the two candidate readings of
	// the guard disagree about whether the 31st is a day at all:
	//
	//	rows[0]        Thirty360  Days(30th, 31st) = 0  -> the run is refused
	//	termsAt(date)  ACT365     Days(30th, 31st) = 1  -> the run happens
	//
	// A convention change mid-life is a sharp fixture rather than a common product
	// event; it is the cleanest way to make the row choice the only variable,
	// since everything else about the two rows is identical.
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

// A never-priced facility reads no drawn series. Every row in its timeline
// carries a zero rate, so the run is skipped BEFORE any store read rather than
// merely producing zero — which is what keeps a portfolio of interest-free
// facilities from walking a series each per night. Asserted on the store, because
// "the balance did not move" would pass even if the loop ran.
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
	var reads int
	assertNoError(t, p.Store().Update(ctx, func(ctx context.Context, tx lending.Tx) error {
		return p.AccrueTx(ctx, countingTx{Tx: tx, series: &reads}, line.ID, day(30))
	}))
	assertEqual(t, "series reads on a never-priced facility", reads, 0)
	assertEqual(t, "accrued on a never-priced facility", facility(t, p, line.ID).Accrued, interest.Accrued(0))
}

// countingTx wraps a Tx and counts value-dated series reads, so a test can assert
// that a run was skipped BEFORE any I/O rather than that it happened to produce
// zero. The two are indistinguishable from the balance alone, and only one of them
// is the guard doing its job.
type countingTx struct {
	lending.Tx
	series *int
}

func (t countingTx) ValueDatedSeries(ctx context.Context, book ledger.BookID, pos ledger.Position, normal ledger.Direction, from, to time.Time) (ledger.Series, error) {
	*t.series++
	return t.Tx.ValueDatedSeries(ctx, book, pos, normal, from, to)
}

// Re-disbursement charges the span between a full repayment and the new
// advance's predecessor: those are drawn days, so they are owed.
//
// The sequence: disburse at day 0, accrue to L (day 10), repay in full at R (day
// 15), re-disburse at D (day 40), accrue to day 41.
//
// This is a README *Next Work* bullet, closed as a side effect. Principal WAS
// drawn across days 11-14, so real interest accrued there, and the clamp moved
// LastAccrualDate and the window forward to D and zeroed AccruedGross — so those
// days were never charged at all, the mirror image of the double-charge the clamp
// was added to prevent. A whole-life recompute over the value-dated drawn series
// includes them naturally, while the idle span R->D accrues nothing because drawn
// is zero across it. No special case, and no clamp: a general fix subsuming a
// special one.
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
	// accrue-to-L, repay-at-R sequence, accrued through R and then on to D
	// WITHOUT being re-disbursed.
	assertEqual(t, "an idle span accrues nothing",
		accrueControlThroughIdleSpan(t, day), interest.Accrued(0))
}

// accrueControlThroughIdleSpan drives a second loan through the same
// disburse/accrue/repay sequence as the test above, accrues it through R — which
// is what separates the DRAWN L->R span from the idle one — and then on to day 40
// without re-disbursing. It returns what that last run added, which must be zero.
//
// Accruing through R first is the whole reason this is a separate facility: on the
// facility under test the two spans are added by one run, and only running them
// separately says which of them was worth nothing.
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
