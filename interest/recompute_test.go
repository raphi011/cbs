package interest_test

import (
	"testing"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// TestRecomputeWalksTheWindowDayByDay is the property the whole engine rests on.
//
// Interest over an N-day span is not the sum of its N single days, because
// Accrue divides once per call. Recompute must reproduce the day-by-day figure —
// the one every receivable in every book was actually built up from — not the
// cheaper one-call-over-the-window figure. The two constants here are the
// example the day-walk's own doc comment cites.
func TestRecomputeWalksTheWindowDayByDay(t *testing.T) {
	from, to := day(2026, time.January, 1), day(2026, time.January, 31)
	rate, dc := interest.Rate(180_000), interest.ACT365 // 18%
	s := ledger.Series{Opening: 1_000_00}               // €1,000 in minor units

	daily := flat(rate, dc)

	next, delta := interest.Recompute(s, from, to, interest.State{}, daily)

	const dayByDay = 1_479_452_040
	const oneCall = 1_479_452_054

	if got := int64(next.Gross); got != dayByDay {
		t.Errorf("gross = %d, want %d (30 days walked one at a time)", got, dayByDay)
	}
	if int64(interest.AccrueSeries(s, from, to, daily)) != oneCall {
		t.Fatalf("guard: the unwalked figure is no longer %d, so this test proves nothing", oneCall)
	}

	// From a zero prior state the delta is simply the rounded gross.
	if want := next.Gross.Minor(); delta != want {
		t.Errorf("delta = %d, want %d", delta, want)
	}
}

// TestRecomputePostsTheChangeInTheRoundedValue pins the rule that keeps the
// record and the receivable in step: the delta is the change in Minor(), not the
// rounding of the change. Here a second day's accrual leaves the sub-unit
// remainder below the next boundary, so nothing is owed in minor units yet even
// though gross moved.
func TestRecomputePostsTheChangeInTheRoundedValue(t *testing.T) {
	from := day(2026, time.January, 1)
	// A rate low enough that a day earns well under half a minor unit.
	daily := flat(interest.Rate(100), interest.ACT365)
	s := ledger.Series{Opening: 1_000_00}

	oneDay, delta1 := interest.Recompute(s, from, from.AddDate(0, 0, 1), interest.State{}, daily)
	if delta1 != 0 {
		t.Fatalf("first day delta = %d, want 0 (gross has not reached a minor unit)", delta1)
	}
	if oneDay.Gross == 0 {
		t.Fatal("first day accrued nothing at all; pick a rate that accrues something")
	}

	// Recomputing a longer window from the state the first run left behind.
	twoDay, delta2 := interest.Recompute(s, from, from.AddDate(0, 0, 2), oneDay, daily)
	if delta2 != 0 {
		t.Errorf("second day delta = %d, want 0 (still below a minor unit)", delta2)
	}
	if twoDay.Gross <= oneDay.Gross {
		t.Errorf("gross did not advance: %d then %d", oneDay.Gross, twoDay.Gross)
	}
	if twoDay.Accrued != twoDay.Gross {
		t.Errorf("Accrued = %d, want %d: from a zero start the two track each other",
			twoDay.Accrued, twoDay.Gross)
	}
}

// TestRecomputeTruesUpABackdatedMovement is the case the gross field exists for.
//
// The first run accrues over a window with nothing in it. A repayment then
// arrives value-dated INSIDE that window, so the second run re-derives the same
// days on the smaller balance, gross falls, and the delta is negative — interest
// that was charged and is not owed. An incremental engine has no way to express
// this.
func TestRecomputeTruesUpABackdatedMovement(t *testing.T) {
	from, to := day(2026, time.January, 1), day(2026, time.January, 31)
	daily := flat(interest.Rate(180_000), interest.ACT365)

	before := ledger.Series{Opening: 1_000_00}
	first, chargedDelta := interest.Recompute(before, from, to, interest.State{}, daily)
	if chargedDelta <= 0 {
		t.Fatalf("first run posted %d, want a positive charge", chargedDelta)
	}

	// The same window, now with a repayment value-dated halfway through it.
	after := ledger.Series{
		Opening: 1_000_00,
		Movements: []ledger.DayMovement{
			{Day: day(2026, time.January, 15), Amount: -500_00},
		},
	}
	second, trueUp := interest.Recompute(after, from, to, first, daily)

	if trueUp >= 0 {
		t.Errorf("true-up delta = %d, want negative (half the balance was repaid mid-window)", trueUp)
	}
	if second.Gross >= first.Gross {
		t.Errorf("gross did not fall: %d then %d", first.Gross, second.Gross)
	}
	// The record still mirrors what is owed: the whole window recomputed, not
	// two runs added together.
	if want := second.Gross; second.Accrued != want {
		t.Errorf("Accrued = %d, want %d (no prior settlement, so the record is the gross)",
			second.Accrued, want)
	}
	// And the two runs together charged exactly the recomputed window.
	if got := chargedDelta + trueUp; got != second.Gross.Minor() {
		t.Errorf("total posted = %d, want %d", got, second.Gross.Minor())
	}
}

// TestRecomputeKeepsSettledInterestOffTheRecord pins the one asymmetry between
// Accrued and Gross: capitalisation moves Accrued and leaves Gross alone, so the
// next recompute must not re-charge what was already settled.
func TestRecomputeKeepsSettledInterestOffTheRecord(t *testing.T) {
	from := day(2026, time.January, 1)
	daily := flat(interest.Rate(180_000), interest.ACT365)
	s := ledger.Series{Opening: 1_000_00}

	first, charged := interest.Recompute(s, from, from.AddDate(0, 0, 30), interest.State{}, daily)

	// Capitalisation: the receivable is cleared, so Accrued drops by what was
	// settled while Gross — the window's history — stays where it is.
	settled := first
	settled.Accrued -= interest.FromMinor(charged)

	second, delta := interest.Recompute(s, from, from.AddDate(0, 0, 31), settled, daily)

	if delta < 0 {
		t.Errorf("delta = %d, want non-negative: day 31 must not re-charge settled interest", delta)
	}
	if second.Gross <= first.Gross {
		t.Errorf("gross did not advance over the extra day: %d then %d", first.Gross, second.Gross)
	}
	if second.Accrued >= second.Gross {
		t.Errorf("Accrued = %d should sit below Gross = %d once interest has been settled",
			second.Accrued, second.Gross)
	}
}

// TestRecomputeOnAnUnadvancedWindowGivesTheRecordBack pins the precondition
// Recompute documents rather than enforces, so that anyone who removes a
// caller's guard finds out here.
//
// An empty window accrues a gross of zero, and the returned Accrued is
// prior.Accrued + gross - prior.Gross — so recomputing a window that has not
// advanced hands the whole record back as a correction. It is not a no-op, and it
// is why both product layers refuse when Days(LastAccrualDate, date) <= 0 before
// reading a series at all.
func TestRecomputeOnAnUnadvancedWindowGivesTheRecordBack(t *testing.T) {
	d := day(2026, time.January, 10)
	prior := interest.State{Accrued: 5_000_000, Gross: 5_000_000}
	daily := flat(interest.Rate(180_000), interest.ACT365)
	s := ledger.Series{Opening: 1_000_00}

	for _, c := range []struct {
		name     string
		from, to time.Time
	}{
		{"empty window", d, d},
		{"backwards window", d, d.AddDate(0, 0, -1)},
	} {
		next, delta := interest.Recompute(s, c.from, c.to, prior, daily)

		if next.Gross != 0 {
			t.Errorf("%s: gross = %d, want 0", c.name, next.Gross)
		}
		if want := prior.Accrued - prior.Gross; next.Accrued != want {
			t.Errorf("%s: Accrued = %d, want %d", c.name, next.Accrued, want)
		}
		if want := -prior.Accrued.Minor(); delta != want {
			t.Errorf("%s: delta = %d, want %d (the whole record handed back)", c.name, delta, want)
		}
	}
}
