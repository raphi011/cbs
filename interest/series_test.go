package interest_test

import (
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// flat is the Period both product layers pass: one rate, one convention.
func flat(rate interest.Rate, dc interest.DayCount) interest.Period {
	return func(balance ledger.Amount, from, to time.Time) interest.Accrued {
		return interest.Accrue(balance, rate, dc, from, to)
	}
}

func TestAccrueSeriesConstantBalanceMatchesAccrue(t *testing.T) {
	from, to := day(2026, time.January, 1), day(2026, time.January, 31)
	s := ledger.Series{Opening: 100_000}

	got := interest.AccrueSeries(s, from, to, flat(50_000, interest.ACT365))
	want := interest.Accrue(100_000, 50_000, interest.ACT365, from, to)
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d (a series with no movement is one run)", got, want)
	}
}

func TestAccrueSeriesSingleDayUsesClosingBalance(t *testing.T) {
	// The daily-run case: one slot, at the balance including that day's movement.
	from, to := day(2026, time.January, 10), day(2026, time.January, 11)
	s := ledger.Series{
		Opening:   100_000,
		Movements: []ledger.DayMovement{{Day: to, Amount: 50_000}},
	}

	got := interest.AccrueSeries(s, from, to, flat(50_000, interest.ACT365))
	want := interest.Accrue(150_000, 50_000, interest.ACT365, from, to)
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d (day 11 accrues on day 11's closing balance)", got, want)
	}
}

func TestAccrueSeriesSplitsAtAMovement(t *testing.T) {
	from, to := day(2026, time.January, 1), day(2026, time.January, 11)
	// Balance is 100_000 for slots 2..5, then 150_000 for slots 6..11.
	s := ledger.Series{
		Opening:   100_000,
		Movements: []ledger.DayMovement{{Day: day(2026, time.January, 6), Amount: 50_000}},
	}

	p := flat(50_000, interest.ACT365)
	got := interest.AccrueSeries(s, from, to, p)
	want := p(100_000, day(2026, time.January, 2), day(2026, time.January, 6)) +
		p(150_000, day(2026, time.January, 6), day(2026, time.January, 12))
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d", got, want)
	}
}

func TestAccrueSeriesFoldsMovementsAtOrBeforeFromIntoOpening(t *testing.T) {
	from, to := day(2026, time.January, 10), day(2026, time.January, 12)
	s := ledger.Series{
		Opening:   100_000,
		Movements: []ledger.DayMovement{{Day: from, Amount: 50_000}},
	}

	got := interest.AccrueSeries(s, from, to, flat(50_000, interest.ACT365))
	want := interest.Accrue(150_000, 50_000, interest.ACT365, from, to)
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d (a movement on `from` is already in force for every slot)", got, want)
	}
}

func TestAccrueSeriesHandlesNegativeBalances(t *testing.T) {
	// Every overdraft case: the deposit layer negates the balance itself, but a
	// Period may legitimately be handed a negative one.
	from, to := day(2026, time.January, 1), day(2026, time.January, 11)
	s := ledger.Series{Opening: -100_000}

	got := interest.AccrueSeries(s, from, to, func(balance ledger.Amount, f, t2 time.Time) interest.Accrued {
		if balance >= 0 {
			return 0
		}
		return interest.Accrue(-balance, 150_000, interest.ACT365, f, t2)
	})
	want := interest.Accrue(100_000, 150_000, interest.ACT365, from, to)
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d", got, want)
	}
}

func TestAccrueSeriesEmptyWindowAccruesNothing(t *testing.T) {
	d := day(2026, time.January, 10)
	s := ledger.Series{Opening: 100_000}

	if got := interest.AccrueSeries(s, d, d, flat(50_000, interest.ACT365)); got != 0 {
		t.Errorf("AccrueSeries over an empty window = %d, want 0", got)
	}
	if got := interest.AccrueSeries(s, d, d.AddDate(0, 0, -1), flat(50_000, interest.ACT365)); got != 0 {
		t.Errorf("AccrueSeries over a backwards window = %d, want 0", got)
	}
	// Same day, different time of day: still a zero-length window, not a
	// one-slot one. The emptiness check must compare day boundaries, not raw
	// instants.
	sameDayLater := time.Date(2026, time.January, 10, 6, 0, 0, 0, time.UTC)
	if got := interest.AccrueSeries(s, d, sameDayLater, flat(50_000, interest.ACT365)); got != 0 {
		t.Errorf("AccrueSeries over a same-day window with differing times of day = %d, want 0", got)
	}
}

func TestAccrueSeriesMovementTimeOfDayIsIgnoredAtTo(t *testing.T) {
	// DayMovement.Day is documented as UTC midnight, but the comparisons here
	// must not depend on that: a movement timestamped later in the day than
	// `to` must still be treated as landing on `to`'s slot, not dropped by a
	// raw (non-truncated) instant comparison.
	from, to := day(2026, time.January, 10), day(2026, time.January, 11)
	s := ledger.Series{
		Opening:   100_000,
		Movements: []ledger.DayMovement{{Day: time.Date(2026, time.January, 11, 13, 0, 0, 0, time.UTC), Amount: 50_000}},
	}

	got := interest.AccrueSeries(s, from, to, flat(50_000, interest.ACT365))
	want := interest.Accrue(150_000, 50_000, interest.ACT365, from, to)
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d (a movement timestamped later in the day than `to` must still land on `to`)", got, want)
	}
}

func TestAccrueSeriesIgnoresMovementsAfterTo(t *testing.T) {
	from, to := day(2026, time.January, 1), day(2026, time.January, 10)
	s := ledger.Series{
		Opening:   100_000,
		Movements: []ledger.DayMovement{{Day: day(2026, time.January, 20), Amount: 50_000}},
	}

	p := flat(50_000, interest.ACT365)
	got := interest.AccrueSeries(s, from, to, p)
	want := p(100_000, from, to)
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d (a movement after `to` must not affect this window)", got, want)
	}
}

func TestAccrueSeriesMultipleMovementsAdvanceThroughEachRun(t *testing.T) {
	from, to := day(2026, time.January, 1), day(2026, time.January, 20)
	s := ledger.Series{
		Opening: 100_000,
		Movements: []ledger.DayMovement{
			{Day: day(2026, time.January, 6), Amount: 50_000},
			{Day: day(2026, time.January, 12), Amount: 25_000},
		},
	}

	p := flat(50_000, interest.ACT365)
	got := interest.AccrueSeries(s, from, to, p)
	want := p(100_000, from, day(2026, time.January, 5)) +
		p(150_000, day(2026, time.January, 5), day(2026, time.January, 11)) +
		p(175_000, day(2026, time.January, 11), to)
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d (the cursor must advance through both runs, not just the first)", got, want)
	}
}

func TestAccrueSeriesBalanceCrossingZeroStopsAccruingOnTheZeroSide(t *testing.T) {
	// TestAccrueSeriesHandlesNegativeBalances above holds a constant negative
	// opening, so the Period's balance>=0 guard never actually fires there.
	// Here the balance clears to zero mid-window, which is the only way that
	// branch runs.
	from, to := day(2026, time.January, 1), day(2026, time.January, 11)
	s := ledger.Series{
		Opening:   -100_000,
		Movements: []ledger.DayMovement{{Day: day(2026, time.January, 6), Amount: 100_000}},
	}

	p := func(balance ledger.Amount, f, t2 time.Time) interest.Accrued {
		if balance >= 0 {
			return 0
		}
		return interest.Accrue(-balance, 150_000, interest.ACT365, f, t2)
	}

	got := interest.AccrueSeries(s, from, to, p)
	want := interest.Accrue(100_000, 150_000, interest.ACT365, from, day(2026, time.January, 5))
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d (the run after the balance clears to zero must accrue nothing)", got, want)
	}
}

func TestAccrueSeriesThirty360NearlyTotalsAcrossAMonthEnd(t *testing.T) {
	// 30/360 collapses the 31st. Splitting a month into runs must not change
	// the month's total beyond the per-call truncation AccrueSeries documents:
	// one Accrued unit — 1e-6 of a minor unit — per split point, never more.
	// This test has exactly one split point, hence the bound of 1; a second
	// movement would need the bound widened to 2.
	from, to := day(2026, time.January, 1), day(2026, time.February, 1)
	p := flat(60_000, interest.Thirty360)

	whole := interest.AccrueSeries(ledger.Series{Opening: 1_000_000}, from, to, p)
	split := interest.AccrueSeries(ledger.Series{
		Opening:   1_000_000,
		Movements: []ledger.DayMovement{{Day: day(2026, time.January, 15), Amount: 0}},
	}, from, to, p)

	// Splitting can only lose to truncation, never gain, so the bound is
	// directional: diff < 0 would mean a split over-accrued, which is a real
	// bug, not a rounding artifact, and must fail loudly rather than being
	// hidden behind an absolute-value check.
	if diff := whole - split; diff < 0 || diff > 1 {
		t.Errorf("split series = %d, whole = %d; a zero movement may cost at most "+
			"one unit of truncation, not %d", split, whole, diff)
	}
}

func TestAccrueSeriesThirty360ConstantBalanceMatchesAccrue(t *testing.T) {
	// The no-movement case must be exactly Accrue's own answer under every
	// convention, Thirty360 included. Thirty360's day count depends on which
	// day of the month a date falls on, not just the gap between two dates,
	// so this is the test that would catch a run expressed in the wrong
	// coordinate (e.g. one shifted by NextDay/PrevDay against `from`/`to`).
	from, to := day(2026, time.January, 1), day(2026, time.January, 31)
	s := ledger.Series{Opening: 100_000}

	got := interest.AccrueSeries(s, from, to, flat(60_000, interest.Thirty360))
	want := interest.Accrue(100_000, 60_000, interest.Thirty360, from, to)
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d (a series with no movement is one run, exact under every convention)", got, want)
	}
}

func TestAccrueSeriesThirty360DailyRunAcrossMonthEnd(t *testing.T) {
	// The day the 31st collapses onto the 30th under 30/360: production's
	// daily run charges zero days for Jan 31 alone. AccrueSeries must land
	// that collapse on the same day, not shift it under the hood.
	from, to := day(2026, time.January, 30), day(2026, time.January, 31)
	s := ledger.Series{Opening: 100_000}

	got := interest.AccrueSeries(s, from, to, flat(60_000, interest.Thirty360))
	want := interest.Accrue(100_000, 60_000, interest.Thirty360, from, to)
	if got != want {
		t.Errorf("AccrueSeries = %d, want %d (Jan 31 alone must accrue what Accrue does, zero under 30/360)", got, want)
	}
}

func TestAccrueSeriesLeapDayIsADay(t *testing.T) {
	from, to := day(2028, time.February, 27), day(2028, time.March, 1)
	s := ledger.Series{Opening: 1_000_000}

	got := interest.AccrueSeries(s, from, to, flat(365_000, interest.ACT365))
	want := interest.Accrue(1_000_000, 365_000, interest.ACT365, from, to)
	if got != want {
		t.Errorf("AccrueSeries across Feb 29 = %d, want %d", got, want)
	}
}
