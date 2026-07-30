package deposit_test

// Effective-dated overdraft terms, from the accrual's side: what a timeline of
// terms rows buys that mutable columns on the account could not.
//
// Every figure in this file is derived with expectedFromTimeline — a day-by-day
// sum stated by the test — rather than copied out of what the implementation
// prints. See its doc comment in register_test.go, where the helpers these
// tests share also live.

import (
	"context"
	"testing"
	"time"

	. "github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// Back-value across a repricing trues up. Priced at R1 from day 0, repriced to
// R2 effective day 30. On day 45 a transaction lands value-dated day 10: the
// next run re-derives days 10-30 AT R1, with the new balance in place, and
// posts the delta.
//
// Nothing happened before this change, because the window started at day 30 and
// the days the posting takes effect over are behind it — a line the customer
// cannot see and did not agree to. This test is the reason effective-dated
// terms exist.
func TestBackValueAcrossARepricingTruesUp(t *testing.T) {
	ctx := context.Background()

	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, book, sub := newTestRegisterOn(t, clock.now)

	const (
		r1      interest.Rate = 120_000 // 12%
		r2      interest.Rate = 180_000 // 18%
		opening ledger.Amount = 100_000 // EUR 1,000 drawn from day 0
		extra   ledger.Amount = 100_000 // a second EUR 1,000, value-dated day 10
	)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 0)
	assertNoError(t, err)
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, r1, 0, interest.ACT365, day(0))
	assertNoError(t, err)

	overdrawValueDated(t, book, sub, acct, opening, day(0), day(0))

	// Thirty days at R1 on the opening draw, run as one end-of-day.
	clock.set(day(30))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(30)))

	// Repriced from day 30, entered on day 30 — an ordinary forward repricing.
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, r2, 0, interest.ACT365, day(30))
	assertNoError(t, err)

	clock.set(day(45))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(45)))

	// The back-dated posting: it reaches the ledger on day 45 and takes
	// economic effect on day 10, which is twenty days BEHIND the repricing.
	overdrawValueDated(t, book, sub, acct, extra, day(45), day(10))

	clock.set(day(46))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(46)))

	// Spans 0-8 on 1,000 at R1; spans 9-29 on 2,000 at R1 — the stretch that
	// was silently never corrected; spans 30-45 on 2,000 at R2.
	//
	// The balance boundary is span 9 rather than span 10 because the extra draw
	// is value-dated day 10 and a movement takes effect on the day ENDING on
	// its value date — interest.AccrueSeries ends the preceding run at day 9.
	// The rate boundary is span 30, because terms are resolved on the day a
	// span BEGINS. The two conventions are not the same one and the test says
	// which is which rather than tuning a number until it agrees.
	want := expectedFromTimeline(day(0), 46, interest.ACT365,
		func(d int) ledger.Amount {
			if d < 9 {
				return opening
			}
			return opening + extra
		},
		func(d int) interest.Rate {
			if d < 30 {
				return r1
			}
			return r2
		})

	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued after the back-dated posting", got.Accrued, want)

	// The receivable holds Minor() of the record, which is the invariant every
	// caller in this package maintains — and the true-up was POSTED, not just
	// recorded on the row.
	receivable, err := book.BookBalance(ctx, got.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable after the true-up", receivable, want.Minor())
}

// Whole-life accrual equals the sum of its periods: days 0-30 at R1 plus days
// 30-60 at R2, computed in ONE run, equals the two computed separately. This
// pins per-day resolution rather than one rate applied across the window.
func TestWholeLifeAccrualEqualsTheSumOfItsPeriods(t *testing.T) {
	ctx := context.Background()

	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	const (
		r1    interest.Rate = 120_000
		r2    interest.Rate = 180_000
		drawn ledger.Amount = 100_000
	)

	// build opens a priced, drawn account on its own register, sharing only the
	// clock and the timeline. Two registers rather than two accounts in one
	// book, so that neither run can be influenced by the other's postings.
	build := func(clock *mutableClock) (*Register, *ledger.Book, Account) {
		t.Helper()
		reg, book, sub := newTestRegisterOn(t, clock.now)
		acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 0)
		assertNoError(t, err)
		_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, r1, 0, interest.ACT365, day(0))
		assertNoError(t, err)
		_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, r2, 0, interest.ACT365, day(30))
		assertNoError(t, err)
		overdrawValueDated(t, book, sub, acct, drawn, day(0), day(0))
		return reg, book, acct
	}

	oneClock := &mutableClock{at: day(0)}
	oneReg, oneBook, one := build(oneClock)
	oneClock.set(day(60))
	assertNoError(t, oneReg.AccrueOverdraft(ctx, one.ID, day(60)))

	twoClock := &mutableClock{at: day(0)}
	twoReg, twoBook, two := build(twoClock)
	twoClock.set(day(30))
	assertNoError(t, twoReg.AccrueOverdraft(ctx, two.ID, day(30)))
	twoClock.set(day(60))
	assertNoError(t, twoReg.AccrueOverdraft(ctx, two.ID, day(60)))

	want := expectedFromTimeline(day(0), 60, interest.ACT365,
		func(int) ledger.Amount { return drawn },
		func(d int) interest.Rate {
			if d < 30 {
				return r1
			}
			return r2
		})

	inOneRun, err := oneReg.GetAccount(ctx, one.ID)
	assertNoError(t, err)
	inTwoRuns, err := twoReg.GetAccount(ctx, two.ID)
	assertNoError(t, err)

	assertEqual(t, "one run over sixty days", inOneRun.Accrued, want)
	assertEqual(t, "two runs over the same sixty days", inTwoRuns.Accrued, want)

	oneRecv, err := oneBook.BookBalance(ctx, inOneRun.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable after one run", oneRecv, want.Minor())
	twoRecv, err := twoBook.BookBalance(ctx, inTwoRuns.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable after two runs", twoRecv, want.Minor())
}

// A backdated terms row posts a delta and rewrites nothing.
//
// The rate change is entered on the 45th and takes effect on the 30th. The next
// run re-derives days 30-44 at the new rate and posts the difference — and that
// is all it does: no earlier accrual is amended, no posting is reversed, and
// the timeline gains a row rather than losing one.
func TestABackdatedTermsRowPostsADeltaAndRewritesNothing(t *testing.T) {
	ctx := context.Background()

	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, book, sub := newTestRegisterOn(t, clock.now)

	const (
		r1    interest.Rate = 120_000
		r2    interest.Rate = 180_000
		drawn ledger.Amount = 100_000
	)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 0)
	assertNoError(t, err)
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, r1, 0, interest.ACT365, day(0))
	assertNoError(t, err)
	overdrawValueDated(t, book, sub, acct, drawn, day(0), day(0))

	clock.set(day(45))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(45)))

	// Entered on the 45th, effective on the 30th. The pair of dates is the
	// whole point: CreatedAt is when the bank was told, EffectiveFrom is when
	// the customer's product changed.
	backdated, err := reg.SetOverdraftTerms(ctx, acct.ID, 500_000, r2, 0, interest.ACT365, day(30))
	assertNoError(t, err)
	if !backdated.EffectiveFrom.Equal(day(30)) {
		t.Errorf("effective from %s, want %s", backdated.EffectiveFrom, day(30))
	}
	if !backdated.CreatedAt.Equal(day(45)) {
		t.Errorf("created at %s, want %s", backdated.CreatedAt, day(45))
	}

	clock.set(day(46))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(46)))

	want := expectedFromTimeline(day(0), 46, interest.ACT365,
		func(int) ledger.Amount { return drawn },
		func(d int) interest.Rate {
			if d < 30 {
				return r1
			}
			return r2
		})

	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued after a retroactive repricing", got.Accrued, want)
	receivable, err := book.BookBalance(ctx, got.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable after a retroactive repricing", receivable, want.Minor())

	// Nothing was rewritten. Two repricings were entered and the log holds two
	// events for this account; the timeline holds three rows — the opening one
	// from OpenAccount, R1 on day 0... except that R1 landed on the SAME day as
	// the opening row and therefore replaced it. Two rows, and the audit log is
	// the record of how many times someone changed them.
	events, err := reg.GetAuditLog(ctx)
	assertNoError(t, err)
	var termsSet int
	for _, e := range events {
		if e.Type == ledger.EventOverdraftTermsSet && e.EntityID == string(acct.ID) {
			termsSet++
		}
	}
	assertEqual(t, "overdraft-terms-set events", termsSet, 2)

	history, err := reg.OverdraftTermsHistory(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "timeline length", len(history), 2)
	assertEqual(t, "first row rate", history[0].Rate, r1)
	assertEqual(t, "second row rate", history[1].Rate, r2)
	if !history[1].EffectiveFrom.Equal(day(30)) {
		t.Errorf("second row effective from %s, want %s", history[1].EffectiveFrom, day(30))
	}
	if !history[1].CreatedAt.Equal(day(45)) {
		t.Errorf("second row created at %s, want %s", history[1].CreatedAt, day(45))
	}
}

// A future-dated terms row is inert until its date, then applies. Scheduled
// repricing, for free.
func TestAFutureDatedTermsRowIsInertUntilItsDate(t *testing.T) {
	ctx := context.Background()

	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, book, sub := newTestRegisterOn(t, clock.now)

	const (
		r1    interest.Rate = 120_000
		r2    interest.Rate = 180_000
		drawn ledger.Amount = 100_000
	)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 0)
	assertNoError(t, err)
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, r1, 0, interest.ACT365, day(0))
	assertNoError(t, err)
	overdrawValueDated(t, book, sub, acct, drawn, day(0), day(0))

	// Agreed on day 10, effective day 30.
	clock.set(day(10))
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, r2, 0, interest.ACT365, day(30))
	assertNoError(t, err)

	clock.set(day(20))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(20)))

	atR1 := expectedFromTimeline(day(0), 20, interest.ACT365,
		func(int) ledger.Amount { return drawn },
		func(int) interest.Rate { return r1 })

	twenty, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued through day 20, all at the old rate", twenty.Accrued, atR1)

	clock.set(day(40))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(40)))

	want := expectedFromTimeline(day(0), 40, interest.ACT365,
		func(int) ledger.Amount { return drawn },
		func(d int) interest.Rate {
			if d < 30 {
				return r1
			}
			return r2
		})

	forty, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued through day 40, priced across the switch", forty.Accrued, want)
	receivable, err := book.BookBalance(ctx, forty.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable", receivable, want.Minor())
}

// An account unpriced then priced accrues only afterwards.
//
// The previous model could not express this at all: Rate was per-account, so
// pricing it would have re-derived the free year at the new rate or, with the
// window reset, thrown that year away.
func TestAnAccountUnpricedThenPricedAccruesOnlyAfterwards(t *testing.T) {
	ctx := context.Background()

	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, book, sub := newTestRegisterOn(t, clock.now)

	const (
		r1    interest.Rate = 120_000
		drawn ledger.Amount = 100_000
	)

	// A facility with no price: the only row is the opening one, at a zero rate.
	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 500_000)
	assertNoError(t, err)
	overdrawValueDated(t, book, sub, acct, drawn, day(0), day(0))

	clock.set(day(365))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(365)))

	free, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "a year of a free facility, used", free.Accrued, interest.Accrued(0))
	assertEqual(t, "and no receivable was opened for it", string(free.InterestGL), "")

	// Priced from day 365.
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, r1, 0, interest.ACT365, day(365))
	assertNoError(t, err)

	clock.set(day(400))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(400)))

	// Days 365-399 only. The free year is re-derived on every run, as every
	// other day is, and contributes nothing because the rate in force on those
	// days was zero.
	want := expectedFromTimeline(day(365), 35, interest.ACT365,
		func(int) ledger.Amount { return drawn },
		func(int) interest.Rate { return r1 })

	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued only from the day it was priced", got.Accrued, want)
	receivable, err := book.BookBalance(ctx, got.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable", receivable, want.Minor())
}

// Idempotence. The guard is still what refuses the re-run, but with a
// whole-life recompute the same date would produce the same gross and therefore
// a zero delta anyway — same invariant, one fewer reason behind it.
func TestRerunningEndOfDayForTheSameDatePostsNothing(t *testing.T) {
	ctx := context.Background()

	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, book, sub := newTestRegisterOn(t, clock.now)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 0)
	assertNoError(t, err)
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, 120_000, 0, interest.ACT365, day(0))
	assertNoError(t, err)
	overdrawValueDated(t, book, sub, acct, 100_000, day(0), day(0))

	clock.set(day(30))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(30)))

	before, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	beforeRecv, err := book.BookBalance(ctx, before.InterestGL)
	assertNoError(t, err)

	want := expectedFromTimeline(day(0), 30, interest.ACT365,
		func(int) ledger.Amount { return 100_000 },
		func(int) interest.Rate { return 120_000 })
	assertEqual(t, "accrued through day 30", before.Accrued, want)

	for i := 0; i < 3; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(30)))
	}

	after, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	afterRecv, err := book.BookBalance(ctx, after.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "accrued after three re-runs", after.Accrued, before.Accrued)
	assertEqual(t, "receivable after three re-runs", afterRecv, beforeRecv)
}

// A never-priced account reads no series. Every row in its timeline carries a
// zero rate, so the run is skipped BEFORE any store read rather than merely
// producing zero. Asserted on the store, because "the balance did not move"
// would pass even if the loop ran.
func TestANeverPricedAccountReadsNoSeries(t *testing.T) {
	ctx := context.Background()

	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, book, sub := newTestRegisterOn(t, clock.now)

	acct, err := reg.OpenAccount(ctx, sub, "Bella", testAsset, 500_000)
	assertNoError(t, err)
	overdrawValueDated(t, book, sub, acct, 100_000, day(0), day(0))

	clock.set(day(30))
	var reads int
	assertNoError(t, reg.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		return reg.AccrueOverdraftTx(ctx, countingTx{Tx: tx, series: &reads}, acct.ID, day(30))
	}))
	assertEqual(t, "series reads on a never-priced account", reads, 0)

	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued on a never-priced account", got.Accrued, interest.Accrued(0))
}

// The guard still holds: an unadvanced window is refused BEFORE a series is
// read, per interest.Recompute's documented precondition — "a caller that
// recomputes an empty window over a non-zero prior state is told to give the
// whole record back". The precondition survives this change and this is what
// pins that neither layer has started violating it.
func TestAnUnadvancedWindowIsRefusedBeforeReadingASeries(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, book, sub := newTestRegisterOn(t, clock.now)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 0)
	assertNoError(t, err)
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, 120_000, 0, interest.ACT365, day(0))
	assertNoError(t, err)
	overdrawValueDated(t, book, sub, acct, 100_000, day(0), day(0))

	clock.set(day(30))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(30)))
	before, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)

	// Now re-run for a date the window has ALREADY reached. The record must
	// come back untouched, and no series must be read at all.
	var reads int
	assertNoError(t, reg.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		return reg.AccrueOverdraftTx(ctx, countingTx{Tx: tx, series: &reads}, acct.ID, day(30))
	}))
	assertEqual(t, "series reads on an unadvanced window", reads, 0)

	after, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued after an unadvanced re-run", after.Accrued, before.Accrued)
	receivable, err := book.BookBalance(ctx, after.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable after an unadvanced re-run", receivable, before.Accrued.Minor())
}

// The advancement guard resolves its day count on `date`.
//
// There is no single DayCount to ask any more — it is a terms field — and the
// conventions genuinely disagree about whether a window advanced. Under
// Thirty360 the 31st collapses onto the 30th, so Days(30th, 31st) is zero while
// ACT365 says one: a run on the 31st is a no-op under one convention and a real
// day under the other. Resolving on the day being accrued is the right answer,
// and it is otherwise invisible.
func TestTheAdvancementGuardResolvesItsDayCountOnTheAccrualDate(t *testing.T) {
	ctx := context.Background()
	the30th := time.Date(2025, time.January, 30, 0, 0, 0, 0, time.UTC)
	the31st := time.Date(2025, time.January, 31, 0, 0, 0, 0, time.UTC)

	run := func(dc interest.DayCount) interest.Accrued {
		t.Helper()
		clock := &mutableClock{at: time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)}
		reg, book, sub := newTestRegisterOn(t, clock.now)
		acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 0)
		assertNoError(t, err)
		_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, 120_000, 0, dc, clock.now())
		assertNoError(t, err)
		overdrawValueDated(t, book, sub, acct, 100_000, clock.now(), clock.now())

		clock.set(the30th)
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, the30th))
		through30, err := reg.GetAccount(ctx, acct.ID)
		assertNoError(t, err)

		clock.set(the31st)
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, the31st))
		through31, err := reg.GetAccount(ctx, acct.ID)
		assertNoError(t, err)
		return through31.Accrued - through30.Accrued
	}

	if moved := run(interest.Thirty360); moved != 0 {
		t.Errorf("Thirty360 accrued %d on the 31st, want 0: the 31st collapses onto the 30th", moved)
	}
	if moved := run(interest.ACT365); moved <= 0 {
		t.Errorf("ACT365 accrued %d on the 31st, want a real day", moved)
	}
}

// Thirty360, terms effective on a 31st: the row takes effect on the 1st,
// because the 31st accrues nothing under the convention.
//
// That follows from the convention rather than from this change, and it is
// pinned rather than asserted away: it is surprising enough that someone will
// read it as a bug, and a test saying "this is correct and here is why" is
// cheaper than the investigation.
func TestUnderThirty360ATermsRowEffectiveOnA31stTakesEffectOnThe1st(t *testing.T) {
	ctx := context.Background()
	clock := &mutableClock{at: time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)}
	reg, book, sub := newTestRegisterOn(t, clock.now)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 0)
	assertNoError(t, err)
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, 120_000, 0, interest.Thirty360, clock.now())
	assertNoError(t, err)
	overdrawValueDated(t, book, sub, acct, 100_000, clock.now(), clock.now())

	the30th := time.Date(2025, time.January, 30, 0, 0, 0, 0, time.UTC)
	the31st := time.Date(2025, time.January, 31, 0, 0, 0, 0, time.UTC)
	feb1 := time.Date(2025, time.February, 1, 0, 0, 0, 0, time.UTC)

	clock.set(the30th)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, the30th))

	// A repricing effective on the 31st, entered on the 30th.
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, 240_000, 0, interest.Thirty360, the31st)
	assertNoError(t, err)

	through30, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)

	clock.set(feb1)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, feb1))
	throughFeb1, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)

	// One day of 30/360 accrued between the 30th and 1 February — the 31st
	// collapsed onto the 30th and accrued nothing — and that one day is the
	// 31st's, charged at the NEW rate, because the row was in force on it.
	want := interest.Accrue(100_000, 240_000, interest.Thirty360, the31st, feb1)
	assertEqual(t, "the 31st accrues one 30/360 day at the new rate",
		throughFeb1.Accrued-through30.Accrued, want)

	receivable, err := book.BookBalance(ctx, throughFeb1.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable", receivable, throughFeb1.Accrued.Minor())
}

// A retroactive rate CUT across a long history drives a large negative delta
// through the refund path. Its bounds hold — the credit to the receivable is
// clamped by what the receivable holds, and the rest is refunded in cash rather
// than driving an Asset balance negative, which the ledger would refuse inside
// an end-of-day batch and take the whole book's run down with it — but this
// change widens the input range from one terms window to the account's whole
// life, so the test says so.
func TestARetroactiveRateCutRefundsWithoutDrivingTheReceivableNegative(t *testing.T) {
	ctx := context.Background()

	origin := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, book, sub := newTestRegisterOn(t, clock.now)

	const (
		dear  interest.Rate = 240_000 // 24%
		cheap interest.Rate = 20_000  // 2%
		drawn ledger.Amount = 100_000
	)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 0)
	assertNoError(t, err)
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, dear, 0, interest.ACT365, day(0))
	assertNoError(t, err)
	overdrawValueDated(t, book, sub, acct, drawn, day(0), day(0))

	clock.set(day(365))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(365)))

	// A year at 24%, day by day: days 0-364.
	atDear := expectedFromTimeline(day(0), 365, interest.ACT365,
		func(int) ledger.Amount { return drawn },
		func(int) interest.Rate { return dear })

	year, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued over a year at 24%", year.Accrued, atDear)

	// CAPITALISE, so the receivable is emptied and the interest has genuinely
	// been charged to the customer. That is what makes the correction below
	// unabsorbable and forces it down the refund path.
	charge := atDear.Minor()
	_, err = reg.ChargeOverdraftInterest(ctx, acct.ID, day(365))
	assertNoError(t, err)
	emptied, err := book.BookBalance(ctx, year.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable after capitalisation", emptied, ledger.Amount(0))
	customerAfterCharge, err := book.BookBalance(ctx, acct.GLAccount)
	assertNoError(t, err)
	assertEqual(t, "customer owes principal plus the capitalised interest",
		customerAfterCharge, -(drawn + charge))

	// Now the whole year was mispriced: 2%, effective from day 0.
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 500_000, cheap, 0, interest.ACT365, day(0))
	assertNoError(t, err)

	clock.set(day(366))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(366)))

	// The whole life re-derived at 2%. The capitalisation is value-dated day
	// 365, and a movement takes effect on the day ENDING on its value date, so
	// spans 0-363 are on the principal alone and spans 364-365 include the
	// capitalised charge. That is also why capitalisation compounds a day early
	// — ChargeOverdraftInterestTx's own doc says so and calls value-dating to
	// the next day the fix.
	atCheap := expectedFromTimeline(day(0), 366, interest.ACT365,
		func(d int) ledger.Amount {
			if d < 364 {
				return drawn
			}
			return drawn + charge
		},
		func(int) interest.Rate { return cheap })

	// Recompute's arithmetic, stated rather than observed. Prior state is the
	// capitalisation residue over the old gross; the new state is the new gross
	// less what capitalisation took off the record.
	priorAccrued := atDear - interest.FromMinor(charge)
	nextAccrued := priorAccrued + atCheap - atDear
	delta := nextAccrued.Minor() - priorAccrued.Minor()
	if delta >= 0 {
		t.Fatalf("delta = %d, want a large negative correction", delta)
	}
	// The receivable was emptied by the capitalisation, so it absorbs nothing
	// and the whole correction is refunded in cash — and a refund leaves the
	// record, because it has been settled.
	refund := -delta
	want := nextAccrued + interest.FromMinor(refund)

	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued after the retroactive cut", got.Accrued, want)

	// The invariant at risk is that the record and the ledger agree, so both
	// are read rather than one restated from the other.
	receivable, err := book.BookBalance(ctx, got.InterestGL)
	assertNoError(t, err)
	if receivable < 0 {
		t.Errorf("receivable = %d; the correction must clamp rather than drive an Asset negative", receivable)
	}
	assertEqual(t, "receivable equals Minor() of the record", receivable, got.Accrued.Minor())

	customer, err := book.BookBalance(ctx, acct.GLAccount)
	assertNoError(t, err)
	assertEqual(t, "the customer was credited what the receivable could not absorb",
		customer, customerAfterCharge+refund)
}
