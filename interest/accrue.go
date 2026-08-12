package interest

import (
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Accrue returns the interest earned on balance between two dates.
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
