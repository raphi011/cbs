package lending

import (
	"math"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// BuildSchedule generates a term loan's amortization schedule.
func BuildSchedule(f Facility, principal ledger.Amount, rate interest.Rate, firstDue time.Time) []Installment {
	if f.Kind != TermLoan || f.TermMonths <= 0 || principal <= 0 {
		return nil
	}

	out := make([]Installment, 0, f.TermMonths)
	outstanding := principal
	payment := annuityPayment(principal, rate, f.TermMonths)
	flat := principal / ledger.Amount(f.TermMonths)

	for seq := 1; seq <= f.TermMonths; seq++ {
		scheduled := monthlyInterest(outstanding, rate)

		var portion ledger.Amount
		switch {
		case seq == f.TermMonths:
			// The last instalment repays whatever is left, which is what makes
			// the scheduled principal sum to the disbursed principal exactly
			// however the rounding fell on the way.
			portion = outstanding
		case f.Method == EqualPrincipal:
			portion = flat
		default:
			portion = payment - scheduled
			// A rate high enough that the scheduled interest exceeds the annuity payment
			// would amortize backwards.
			if portion < 0 {
				portion = 0
			}
			if portion > outstanding {
				portion = outstanding
			}
		}

		out = append(out, Installment{
			FacilityID: f.ID,
			Seq:        seq,
			DueDate:    AddMonths(firstDue, seq-1),
			Principal:  portion,
			Interest:   scheduled,
		})
		outstanding -= portion
	}
	return out
}

// annuityPayment is the level instalment that repays principal over n months at
// an annual rate: P × i / (1 − (1+i)^−n), where i is a twelfth of the rate.
func annuityPayment(principal ledger.Amount, r interest.Rate, n int) ledger.Amount {
	if n <= 0 {
		return 0
	}
	if r <= 0 {
		return roundDiv(principal, n)
	}
	i := float64(r) / (12 * float64(interest.RateScale))
	factor := i / (1 - math.Pow(1+i, -float64(n)))
	return ledger.Amount(math.Round(float64(principal) * factor))
}

// monthlyInterest is one month's scheduled interest on an outstanding balance:
// outstanding × rate / 12, rounded half up.
func monthlyInterest(outstanding ledger.Amount, r interest.Rate) ledger.Amount {
	if outstanding <= 0 || r <= 0 {
		return 0
	}
	const den = int64(12 * interest.RateScale)
	return ledger.Amount((int64(outstanding)*int64(r) + den/2) / den)
}

// roundDiv divides an amount into n parts, rounding half up.
func roundDiv(a ledger.Amount, n int) ledger.Amount {
	if n <= 0 {
		return 0
	}
	return ledger.Amount((int64(a) + int64(n)/2) / int64(n))
}

// AddMonths adds n calendar months, clamping to the last day of the target
// month.
func AddMonths(t time.Time, n int) time.Time {
	y, m, d := t.UTC().Date()
	target := time.Date(y, m+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	lastDay := target.AddDate(0, 1, -1).Day()
	if d > lastDay {
		d = lastDay
	}
	return time.Date(target.Year(), target.Month(), d, 0, 0, 0, 0, time.UTC)
}
