package deposit

import (
	"sort"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// OverdraftTerms is what an account's arranged overdraft cost from one day
// onwards. It is a ROW, one per repricing, never overwritten — which is what
// makes "what did this account's product say on 15 July 2027?" a question with
// a stable answer, and what lets the accrual re-derive a past day at the terms
// that were actually in force on it.
//
// The four fields used to be mutable columns on Account. An accrual posted six
// months ago could not then be reproduced from stored state: the inputs it used
// were gone, overwritten by the current ones. Worse, it bounded the recompute
// window at the last repricing. A repricing closed the old window out and
// opened a new one at itself, so a backdated posting landing behind it was
// trued up only from the repricing forward — the window's opening balance is
// value-dated, so the posting did move it — while the days between where it
// took economic effect and the repricing kept the interest computed without it,
// permanently. The repricing was a line the correction stopped at.
//
// # Two dates, and what the pair means
//
// This is the booking-date/value-date distinction applied to configuration.
// CreatedAt is when the repricing was entered; EffectiveFrom is when it takes
// economic effect. They are different questions for exactly the reasons the
// README already gives for money, and both are kept.
//
// EffectiveFrom is day-granular: ledger.DayStart-truncated by the caller before
// it reaches a store, on the same day axis accrual already uses. Accrual
// iterates whole UTC days — interest.perDay is explicit about it — so terms
// changing part-way through a day would have no well-defined meaning: the day
// is the unit the arithmetic is expressed in.
type OverdraftTerms struct {
	AccountID     AccountID
	EffectiveFrom time.Time // day-truncated; the first day these terms apply

	// OverdraftLimit is the positive amount the balance may go below zero by;
	// 0 means none. It is here rather than on Account because the available
	// balance a customer was quoted last March is as much a fact about that
	// March as the rate they were charged.
	OverdraftLimit ledger.Amount
	// Rate is the annual rate on the drawn balance up to OverdraftLimit. Zero
	// makes the WHOLE overdraft interest-free, which is a real product.
	Rate interest.Rate
	// UnarrangedRate applies to any balance drawn beyond OverdraftLimit. It is
	// an optional SURCHARGE, not a switch: zero does not mean the excess is
	// free, it means Rate applies throughout. See Validate for the one
	// combination that is refused.
	UnarrangedRate interest.Rate
	DayCount       interest.DayCount

	CreatedAt time.Time // when the row was entered, not when it takes effect
}

// TermsDayKey is the day a terms row is identified by within its account.
//
// A terms row is identified by (account, day), so a second row entered for the
// same effective day replaces the first — which makes "the terms in force on
// day D" unique by construction rather than by a validation rule.
//
// Store implementations must derive the key of a row passed to
// PutOverdraftTerms with this function, so that GetOverdraftTermsAsOf finds it:
// mem uses it as a map key, pg as the value of the overdraft_terms.day_key
// column and as the column the as-of lookup compares on. It is the same
// discipline SnapshotDateKey follows, for the same reason — a lexicographically
// ordered ISO day is a total order the two stores cannot disagree about.
func TermsDayKey(day time.Time) string { return ledger.DayStart(day).Format("2006-01-02") }

// Validate reports whether these terms are a product. The rules are the ones
// SetOverdraftTerms used to apply to its arguments, moved onto the row so that
// a store round trip and a register call are held to one standard.
//
// The refused combination is an unarranged rate with no arranged one: it would
// price only the excess, making the money drawn beyond the limit dearer than
// nothing while leaving the facility inside it free. That is not a product, it
// is a mistake.
func (t OverdraftTerms) Validate() error {
	if t.OverdraftLimit < 0 {
		return ErrInvalidAmount
	}
	if t.Rate < 0 || t.UnarrangedRate < 0 {
		return ErrInvalidRate
	}
	if t.Rate == 0 && t.UnarrangedRate > 0 {
		return ErrInvalidRate
	}
	return nil
}

// termsAt is the row in force on a day: the last one whose EffectiveFrom is not
// after it. rows must be ascending by EffectiveFrom, which is what
// ListOverdraftTermsForAccount returns.
//
// Binary search rather than a cursor advanced alongside the accrual walk. The
// runs and the days within them are both ascending today, so a cursor would
// work — and would silently break the first time anything called the Period
// closure out of order. Rows are loaded once per accrual run, so this is
// O(days log rows) of arithmetic on top of a walk that is already O(days), and
// no extra I/O at all.
//
// false means the day precedes the account's opening row, which for an account
// opened through OpenAccount means a day before the account existed.
func termsAt(rows []OverdraftTerms, day time.Time) (OverdraftTerms, bool) {
	d := ledger.DayStart(day)
	// The first index whose EffectiveFrom is strictly after d; the row in force
	// is the one before it.
	i := sort.Search(len(rows), func(i int) bool {
		return rows[i].EffectiveFrom.After(d)
	})
	if i == 0 {
		return OverdraftTerms{}, false
	}
	return rows[i-1], true
}

// anyPriced reports whether any row in the timeline carries a non-zero rate.
//
// It is what keeps a never-priced account from reading a value-dated series
// every night. A zero rate is now a property of a DAY rather than of an
// account, so the old `Rate <= 0` early return cannot survive as an
// account-level guard: an account unpriced for its first year and priced
// thereafter is a case the previous model could not express at all. Skipping
// the run entirely is only safe when NO day in the timeline is priced.
func anyPriced(rows []OverdraftTerms) bool {
	for _, r := range rows {
		if r.Rate > 0 {
			return true
		}
	}
	return false
}
