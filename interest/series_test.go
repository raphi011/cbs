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
}

func TestAccrueSeriesThirty360TotalsAcrossAMonthEnd(t *testing.T) {
	// 30/360 collapses the 31st. Splitting a month into runs must not change
	// the month's total, only which slot the collapse lands on.
	from, to := day(2026, time.January, 1), day(2026, time.February, 1)
	p := flat(60_000, interest.Thirty360)

	whole := interest.AccrueSeries(ledger.Series{Opening: 1_000_000}, from, to, p)
	split := interest.AccrueSeries(ledger.Series{
		Opening:   1_000_000,
		Movements: []ledger.DayMovement{{Day: day(2026, time.January, 15), Amount: 0}},
	}, from, to, p)

	if whole != split {
		t.Errorf("split series = %d, whole = %d; a zero movement must not change the total", split, whole)
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
