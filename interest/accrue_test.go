package interest

import (
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
)

func TestAccrue(t *testing.T) {
	tests := []struct {
		name     string
		balance  ledger.Amount
		rate     Rate
		dc       DayCount
		from, to time.Time
		want     Accrued
	}{
		// €50 (5_000 cents) at 15% for one day under ACT/365:
		//   5000 × 150_000 × 1 / 365 = 2_054_794 micro-cents = 2.054794 cents.
		{"overdraft one day", 5_000, 150_000, ACT365,
			date(2025, time.January, 15), date(2025, time.January, 16), 2_054_794},

		// €10,000 (1_000_000 cents) at 6% for one day:
		//   1_000_000 × 60_000 × 1 / 365 = 164_383_561 = 164.383561 cents.
		{"loan one day", 1_000_000, 60_000, ACT365,
			date(2025, time.January, 15), date(2025, time.January, 16), 164_383_561},

		// The same loan over a 30-day span accrues 30 times that, to within
		// the truncation of a single division.
		{"loan thirty days", 1_000_000, 60_000, ACT365,
			date(2025, time.January, 15), date(2025, time.February, 14), 4_931_506_849},

		// Under 30/360 a calendar month is exactly a twelfth of a year, so the
		// same loan accrues precisely the scheduled €50.00.
		{"loan one month at 30/360", 1_000_000, 60_000, Thirty360,
			date(2025, time.January, 15), date(2025, time.February, 15), 5_000_000_000},

		// ACT/360 over the same actual span is 365/360 of the ACT/365 figure.
		{"one day at ACT/360", 1_000_000, 60_000, ACT360,
			date(2025, time.January, 15), date(2025, time.January, 16), 166_666_666},

		{"zero rate accrues nothing", 1_000_000, 0, ACT365,
			date(2025, time.January, 15), date(2025, time.January, 16), 0},
		{"zero balance accrues nothing", 0, 60_000, ACT365,
			date(2025, time.January, 15), date(2025, time.January, 16), 0},
		{"same day accrues nothing", 1_000_000, 60_000, ACT365,
			date(2025, time.January, 15), date(2025, time.January, 15), 0},

		// A span that runs backwards accrues nothing rather than un-accruing:
		// an end-of-day run for a date already accrued must be a no-op, and
		// the caller should not have to guard the sign itself.
		{"backwards accrues nothing", 1_000_000, 60_000, ACT365,
			date(2025, time.January, 16), date(2025, time.January, 15), 0},

		// A negative balance is not a negative accrual. Callers pass the
		// overdrawn magnitude; a signed balance reaching here is a bug, and
		// zero is the safe answer.
		{"negative balance accrues nothing", -5_000, 150_000, ACT365,
			date(2025, time.January, 15), date(2025, time.January, 16), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Accrue(tt.balance, tt.rate, tt.dc, tt.from, tt.to); got != tt.want {
				t.Errorf("Accrue = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAccrued_Minor(t *testing.T) {
	tests := []struct {
		name string
		a    Accrued
		want ledger.Amount
	}{
		{"below half rounds down", 2_054_794, 2},
		{"cumulative two days", 4_109_588, 4},
		{"cumulative three days", 6_164_382, 6},
		// 30.82 cents is where the daily delta first posts a different amount.
		{"cumulative fifteen days", 30_821_910, 31},
		{"exactly half rounds up", 500_000, 1},
		{"just below half rounds down", 499_999, 0},
		{"zero", 0, 0},
		// A capitalization charges the rounded figure, which can exceed what
		// was earned and leave the record negative. round(−0.36) is still 0,
		// so the GL and the record stay in step.
		{"small negative residue rounds to zero", -360_000, 0},
		{"negative half rounds away from zero", -500_000, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Minor(); got != tt.want {
				t.Errorf("Accrued(%d).Minor() = %d, want %d", tt.a, got, tt.want)
			}
		})
	}
}

// A year of day-by-day accrual must come to the annual figure, because that is
// the whole reason the record holds sub-minor-unit precision. Each day's
// division truncates, so the loss is bounded by one micro-unit per day — well
// inside a single minor unit over a year.
func TestAccrue_YearOfDaysMatchesTheAnnualFigure(t *testing.T) {
	const balance ledger.Amount = 1_000_000 // €10,000
	const rate Rate = 60_000                // 6%

	var total Accrued
	day := date(2025, time.January, 1)
	for range 365 {
		next := day.AddDate(0, 0, 1)
		total += Accrue(balance, rate, ACT365, day, next)
		day = next
	}

	// 6% of €10,000 is €600.00 = 60_000 cents.
	const want ledger.Amount = 60_000
	if got := total.Minor(); got != want {
		t.Errorf("a year of daily accrual = %d minor units, want %d", got, want)
	}
}

// The largest intermediate is balance × Rate. At a €10bn balance and a 100%
// rate that is 1e18, inside int64's 9.22e18 — so the arithmetic is exact rather
// than merely usually-exact.
func TestAccrue_DoesNotOverflowAtTheLargestPlausibleBalance(t *testing.T) {
	const huge ledger.Amount = 1_000_000_000_000 // €10 billion in cents
	got := Accrue(huge, RateScale, ACT365, date(2025, time.January, 15), date(2025, time.January, 16))
	if got <= 0 {
		t.Fatalf("Accrue overflowed or underflowed: got %d", got)
	}
	// 1e12 × 1e6 × 1 / 365 = 2_739_726_027_397_260.
	if want := Accrued(2_739_726_027_397_260); got != want {
		t.Errorf("Accrue = %d, want %d", got, want)
	}
}

func TestFraction_Of(t *testing.T) {
	for _, tt := range []struct {
		name   string
		f      Fraction
		amount ledger.Amount
		want   ledger.Amount
	}{
		{"two percent of 10000", 20_000, 10_000, 200},
		{"rounds half up", 20_000, 10_050, 201},
		{"zero fraction", 0, 10_000, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.f.Of(tt.amount); got != tt.want {
				t.Errorf("Fraction(%d).Of(%d) = %d, want %d", tt.f, tt.amount, got, tt.want)
			}
		})
	}
}
