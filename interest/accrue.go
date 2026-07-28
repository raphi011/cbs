package interest

import (
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Accrue returns the interest earned on balance between two dates.
//
// # Why this is exact in int64
//
// The natural formula is
//
//	balance × (Rate / RateScale) × days / yearDays
//
// expressed in Accrued's units, which are minor units × AccruedScale. Writing
// that out, the ×AccruedScale and the ÷RateScale cancel because both are 1e6,
// and what is left needs no intermediate rescaling at all:
//
//	Accrued = balance × Rate × days / yearDays
//
// The largest intermediate is balance × Rate × days. Callers accrue a business
// day at a time, so days is 1 in the hot path; at a €10 billion balance and a
// 100% rate that is 1e18, inside int64's 9.22e18.
//
// A non-positive balance or a span that does not run forwards accrues nothing.
// Both are the caller's guard as well — the overdraft base is a magnitude, and
// an end-of-day run for a date already covered is a no-op — but a function that
// silently un-accrues would turn either mistake into a wrong balance rather
// than a missing posting.
func Accrue(balance ledger.Amount, r Rate, dc DayCount, from, to time.Time) Accrued {
	if balance <= 0 || r <= 0 {
		return 0
	}
	days := dc.Days(from, to)
	if days <= 0 {
		return 0
	}
	return Accrued(int64(balance) * int64(r) * int64(days) / int64(dc.YearDays()))
}

// Minor rounds an accrued amount to whole minor units, half away from zero.
//
// This is what the general ledger holds: the invariant every caller maintains
// is that the accrued-interest account's book balance equals Minor() of the
// record, and the daily posting is the change in that value.
func (a Accrued) Minor() ledger.Amount {
	if a >= 0 {
		return ledger.Amount((int64(a) + AccruedScale/2) / AccruedScale)
	}
	return ledger.Amount((int64(a) - AccruedScale/2) / AccruedScale)
}

// FromMinor is the inverse of Minor for a whole number of minor units. It is
// what a capitalization subtracts from the record after charging the rounded
// figure to the customer.
func FromMinor(a ledger.Amount) Accrued { return Accrued(int64(a) * AccruedScale) }

// Of applies a dimensionless fraction to an amount, rounding half away from
// zero. It is how a revolving line's minimum payment takes its share of drawn
// principal.
func (f Fraction) Of(a ledger.Amount) ledger.Amount {
	product := int64(f) * int64(a)
	if product >= 0 {
		return ledger.Amount((product + FractionScale/2) / FractionScale)
	}
	return ledger.Amount((product - FractionScale/2) / FractionScale)
}
