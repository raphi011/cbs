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
// once per run, so this function's own cost is the number of days the balance
// moved rather than the number of days in the window. Whether the whole
// accrual costs that is up to the Period: every production caller reaches this
// through Recompute, which re-decomposes each run into its days and is O(days)
// by choice, because the per-call truncation below is not something an engine
// replacing an existing day-by-day increment can absorb. See perDay.
//
// This per-slot decomposition — one call per run, at that run's own
// endpoints — is authoritative: it is what makes a daily run identical to the
// single-balance call production already makes for that one day. It comes at
// a real, convention-dependent cost against one call over the whole window:
//
//   - Under ACT365/ACT360, a split costs at most one Accrued unit per split
//     point, to Accrue's per-call integer-division truncation, and only ever
//     loses relative to the unsplit total — the days themselves add up
//     exactly, so only the rounding differs.
//   - Under Thirty360, that bound does not hold. 30/360-US day counts are
//     not additive across a run boundary that lands on the 31st, or on the
//     1st of the month after a 31-day one: Days(a, 31st) + Days(31st, b) can
//     differ from Days(a, b) by a whole day, in either direction. A split at
//     such a boundary can therefore gain or lose a full day's interest, not
//     a truncation unit — and a Thirty360 window's total is consequently
//     not invariant to where it is split. The per-slot answer this function
//     computes is still the correct one, because it is the one that agrees
//     with production day by day; it is the whole-window total that is not
//     a stable reference point under this convention.
//
// AccrueSeries also assumes s.Movements is ascending by Day, as documented
// on ledger.Series; it does not sort or validate that itself.
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
