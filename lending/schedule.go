package lending

import (
	"math"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// BuildSchedule generates a term loan's amortization schedule.
//
// The schedule is generated once, at disbursement, and stored. That is a
// deliberate exception to this codebase's rule of deriving rather than storing:
// it is a contract artifact, and the moment a loan is rescheduled or partly
// prepaid it depends on its own history rather than on the loan's terms, so it
// could no longer be recomputed from them.
//
// # The schedule is a plan, not a statement of fact
//
// Its Interest column is the SCHEDULED interest — the outstanding balance times
// a twelfth of the annual rate. What a repayment actually settles is the
// interest that has ACCRUED, which under ACT/365 differs: a 30-day month
// accrues less than a scheduled twelfth, and the principal portion absorbs the
// difference. See Portfolio.Repay.
//
// That difference is precisely why the 30/360 convention exists. Under it every
// month is exactly a twelfth of a year and the two agree to the cent.
//
// # Why this may use floating point and the rest of the system may not
//
// The annuity factor is i / (1 − (1+i)^−n), which has no exact integer form.
// Nothing here reaches a posting: every value is rounded to whole minor units
// before it leaves, the actual interest charged is computed by integer accrual
// in the interest package, and the final instalment absorbs the rounding
// residue so the scheduled principal sums to the disbursed principal exactly —
// which assertScheduleRepaysExactly holds it to. A float64 carries about 16
// significant digits, so the factor for any term a human would sign is exact
// far beyond a cent.
//
// # The rate is an argument, and that is the point
//
// Taking it explicitly rather than reading it off the facility makes visible
// what would otherwise be implicit: the schedule is a PLAN PINNED TO THE TERMS
// IN FORCE AT ACTIVATION. Interest follows an effective-dated timeline and the
// schedule does not, which is why repricing a term loan that already has one is
// refused — see ErrScheduleWouldDiverge.
//
// Returns nil for a revolving line, which has no schedule to generate: it
// appends one instalment per billing cycle instead, when interest is charged.
// Also nil for a non-positive term or principal, so a caller that has validated
// its input gets a schedule and one that has not gets nothing rather than a
// schedule that repays a negative amount.
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
			// A rate high enough that the scheduled interest exceeds the
			// annuity payment would amortize backwards. Clamping to zero keeps
			// the schedule monotonic; the loan simply does not amortize, which
			// is the honest depiction of that contract.
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
//
// A zero rate has no annuity factor — the formula divides by zero — and a
// zero-rate loan is a real product, so it repays principal in equal parts.
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
//
// It divides by twelve here rather than pre-dividing the rate into a monthly
// fraction, because a rate like 3.375% — 33_750 millionths — does not divide by
// twelve into a whole number of millionths, and truncating it first would lose
// half a cent a month on every schedule.
//
// The largest intermediate is outstanding × rate, which at a €10 billion
// balance and a 100% rate is 1e18, inside int64.
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
//
// time.Time.AddDate normalizes instead of clamping, so 31 January plus one
// month is 3 March — which as a schedule of monthly instalments means a loan
// that skips February and drifts further every year. Clamping to 28 February is
// the convention every lender uses.
func AddMonths(t time.Time, n int) time.Time {
	y, m, d := t.UTC().Date()
	target := time.Date(y, m+time.Month(n), 1, 0, 0, 0, 0, time.UTC)
	lastDay := target.AddDate(0, 1, -1).Day()
	if d > lastDay {
		d = lastDay
	}
	return time.Date(target.Year(), target.Month(), d, 0, 0, 0, 0, time.UTC)
}
