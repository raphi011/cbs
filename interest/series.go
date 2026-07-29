package interest

import (
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Period is the interest a product's terms earn on a constant balance held
// over one run of a series. AccrueSeries calls it once per run with that
// run's own [from, to] — the same closed calendar-date interval Accrue
// itself takes — and the balance is whatever held throughout that run,
// including everything value-dated on to: the cursor only ever advances to
// the day before the next movement, never past it.
type Period func(balance ledger.Amount, from, to time.Time) Accrued

// AccrueSeries is interest over a window whose balance changed inside it.
//
// A run covering slots a+1 … b is charged p(balance, a, b) — literally the
// same call production makes for that span. So with no movements in
// [from, to], AccrueSeries is exactly one call, p(s.Opening, from, to),
// byte-identical to Accrue(..., from, to) under every DayCount, Thirty360
// included. That identity matters because Thirty360's day count depends on
// which day of the month a date falls on, not just the gap between two
// dates: expressing each run's endpoints as the literal calendar dates the
// caller passed in — rather than as day-count-derived slot numbers, which
// would shift under Thirty360 — is what keeps a split series landing the
// 31st's collapse on the same day as an unsplit one.
//
// The series is folded into runs of constant balance and Period is called
// once per run, so the cost is the number of days the balance moved rather
// than the number of days in the window. Splitting a window into runs can
// still differ from one call over the whole of it by up to one Accrued unit
// per split point, because Accrue truncates its integer division per call;
// that is the more accurate answer, since the balance genuinely differed
// across the runs.
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
