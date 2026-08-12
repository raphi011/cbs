package interest

import (
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Period is the interest a product's terms earn on a constant balance held over
// one run of a series.
type Period func(balance ledger.Amount, from, to time.Time) Accrued

// AccrueSeries is interest over a window whose balance changed inside it.
func AccrueSeries(s ledger.Series, from, to time.Time, p Period) Accrued {
	from = ledger.DayStart(from)
	to = ledger.DayStart(to)
	if !to.After(from) {
		return 0
	}

	balance := s.Opening
	// A movement on from itself — or before it, which a well-formed Series
	// does not contain — is already in force for every slot in the window,
	// so the first run starts at from itself.
	cursor := from
	i := 0
	for ; i < len(s.Movements) && !ledger.DayStart(s.Movements[i].Day).After(from); i++ {
		balance += s.Movements[i].Amount
	}

	var total Accrued
	for ; i < len(s.Movements); i++ {
		m := s.Movements[i]
		day := ledger.DayStart(m.Day)
		if day.After(to) {
			break
		}
		// m takes effect from slot day onward, so the run it ends covers
		// through the day before.
		prev := day.AddDate(0, 0, -1)
		if prev.After(cursor) {
			total += p(balance, cursor, prev)
			cursor = prev
		}
		balance += m.Amount
	}
	return total + p(balance, cursor, to)
}
