package interest

import (
	"time"

	"github.com/raphi011/cbs/ledger"
)

// State is the accrual state a product keeps on its own record: what is still
// owed and not yet settled, and the whole terms window's gross as last
// computed. deposit.Account and lending.Facility each carry exactly this pair,
// as Accrued and AccruedGross.
type State struct {
	// Accrued is interest earned and not yet settled. The invariant every
	// caller maintains is that the accrued-interest account's book balance
	// equals Accrued.Minor().
	Accrued Accrued

	// Gross is the entire window's accrual as of the last recomputation.
	Gross Accrued
}

// Recompute re-derives a whole terms window from the balance history it accrued
// over, and reports the state that follows plus the rounded change to post.
func Recompute(s ledger.Series, from, to time.Time, prior State, daily Period) (State, ledger.Amount) {
	gross := AccrueSeries(s, from, to, perDay(daily))

	next := State{
		Accrued: prior.Accrued + gross - prior.Gross,
		Gross:   gross,
	}
	return next, next.Accrued.Minor() - prior.Accrued.Minor()
}

// perDay turns a one-day Period into one that AccrueSeries can be handed a
// whole run of constant balance, by walking the run's days.
func perDay(p Period) Period {
	return func(balance ledger.Amount, from, to time.Time) Accrued {
		var total Accrued
		end := ledger.DayStart(to)
		for d := ledger.DayStart(from); d.Before(end); d = d.AddDate(0, 0, 1) {
			total += p(balance, d, d.AddDate(0, 0, 1))
		}
		return total
	}
}
