package interest

import (
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Period is the interest a product's terms earn on a constant balance over
// [from, to). It is the shape both product layers already have — lending's is
// Accrue with the facility's rate and convention, deposit's is the tiered
// arranged/unarranged split — so AccrueSeries can drive either without knowing
// anything about pricing.
type Period func(balance ledger.Amount, from, to time.Time) Accrued

// AccrueSeries is interest over a window whose balance changed inside it.
//
// The window [from, to] covers the day slots from+1 … to — exactly
// DayCount.Days(from, to) of them — and slot d accrues on B(d), the balance
// including everything value-dated on d itself. A day's interest is earned on
// that day's closing balance, which is both the conventional rule and what the
// single-balance code it replaces computes when the window is one day long.
//
// The series is folded into runs of constant balance and Period is called once
// per run, so the cost is the number of days the balance moved rather than the
// number of days in the window. Splitting a window into runs changes the result
// slightly against one call over the whole of it, because Accrue truncates its
// integer division per call; the difference is in micro-minor-units, and it is
// the more accurate answer, since the balance genuinely differed across the
// runs.
func AccrueSeries(s ledger.Series, from, to time.Time, p Period) Accrued {
	if !to.After(from) {
		return 0
	}

	balance := s.Opening
	// The first slot is the day after from. A movement on from itself — or
	// before it, which a well-formed Series does not contain — is already in
	// force for every slot in the window.
	cursor := ledger.NextDay(from)
	i := 0
	for ; i < len(s.Movements) && !s.Movements[i].Day.After(from); i++ {
		balance += s.Movements[i].Amount
	}

	var total Accrued
	for ; i < len(s.Movements); i++ {
		m := s.Movements[i]
		if m.Day.After(to) {
			break
		}
		if m.Day.After(cursor) {
			total += p(balance, cursor, m.Day)
			cursor = m.Day
		}
		balance += m.Amount
	}
	return total + p(balance, cursor, ledger.NextDay(to))
}
