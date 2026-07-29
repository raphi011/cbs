package interest

import (
	"time"

	"github.com/raphi011/cbs/ledger"
)

// State is the accrual state a product keeps on its own record: what is still
// owed and not yet settled, and the whole terms window's gross as last
// computed. deposit.Account and lending.Facility each carry exactly this pair,
// as Accrued and AccruedGross.
//
// Gross exists so that an accrual can be a recomputation rather than an
// increment. Without it a run could only add what it thinks today earned, and a
// posting that lands backdated — after interest has already been charged over
// the days it takes effect on — would have nothing to true up against.
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
//
// This is a recomputation, not an increment: every run re-derives [from, to]
// from the series and posts the change in the ROUNDED value. On an ordinary day
// that is the same delta an incremental engine would post, arrived at
// differently. The difference shows when a posting lands backdated — the days it
// takes effect over are re-derived with it in place, gross moves, and the delta
// trues up what was charged on the old figure. No accrual is reversed and no
// date is rewound: the original posting was a correct statement of what the
// ledger knew at the time.
//
// A negative delta is that true-up, and it means interest was charged that is
// not owed. What to do about it is the caller's: which accounts settle it, and
// how much of it comes back off the record, depend on the product. Recompute
// only says how much.
//
// Posting the change in the rounded value — rather than rounding the change —
// is what keeps the record and the receivable in step. The two differ whenever
// a window's sub-unit remainder crosses a rounding boundary, and only the
// former lands on the figure Accrued.Minor() will report afterwards.
//
// daily is the product's interest over ONE day, and Recompute applies it a day
// at a time across each run of constant balance. See perDay: this is the single
// most load-bearing convention in the accrual engine, and it is not a detail a
// caller may choose differently.
//
// # Precondition: the window must have advanced
//
// [from, to] must cover at least one day. An empty or backwards window accrues a
// gross of zero, and since the returned Accrued is prior.Accrued + gross -
// prior.Gross, a caller that recomputes an empty window over a non-zero prior
// state is told to give the whole record back. That is arithmetic, not a
// judgement about what is owed.
//
// Every caller therefore guards first — deposit and lending both refuse when
// DayCount.Days(LastAccrualDate, date) <= 0, before reading a series at all —
// and this function deliberately does not second-guess them: a silent
// short-circuit here would hide a caller that had lost track of its own window.
// TestRecomputeOnAnUnadvancedWindowGivesTheRecordBack pins the consequence.
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
//
// AccrueSeries hands a Period a whole run at once, and interest over an N-day
// span is NOT the sum of its N single days: Accrue divides once per call, so one
// call over N days truncates once where N calls truncate N times. 30 days on
// €1,000 at 18% is 1_479_452_040 day by day against 1_479_452_054 in one call.
//
// That difference is the whole reason this walk exists. Interest here has always
// been accrued a business day at a time, and every receivable in every book was
// built up that way; recomputing a window with one long call would restate the
// record of every account and every facility the first night it ran. Walking the
// days is what makes a recompute reproduce the increment it replaced, which is
// the only thing that made it a safe swap.
//
// It matters more than truncation under Thirty360, where a day is not always a
// day: the 31st collapses onto the 30th and accrues nothing. What "the interest
// over this window" comes to there depends on how the window is cut, so the only
// answer worth reproducing is the one that was actually charged — day by day.
// That is also why this cannot be shortened to one call times the day count.
//
// The cost is arithmetic, not I/O: AccrueSeries still reads the window in one
// query, and this loop adds no round trips.
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
