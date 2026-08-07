package deposit

import (
	"fmt"
	"sort"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/product"
)

// OverdraftTerms is what an account's arranged overdraft cost from one day
// onwards. It is a ROW, one per repricing: a repricing APPENDS rather than
// editing what an earlier day already said — which is what makes "what did this
// account's product say on 15 July 2027?" a question with a stable answer, and
// what lets the accrual re-derive a past day at the terms that were actually in
// force on it. Re-entering the SAME effective day does replace that day's row,
// and only that day's; see TermsDayKey for why the day is the identity.
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

	// ProductID is the catalogue entry this account is on from this day. It is
	// on the ROW rather than on Account because it varies over the account's
	// life: migrating between products is an ordinary forward-dated row, and a
	// column on Account would contradict the timeline the moment a future-dated
	// migration was entered — the Account.Rate mistake with a new name.
	ProductID product.ID

	// OverdraftLimit is the positive amount the balance may go below zero by;
	// 0 means none. It is here rather than on Account because the available
	// balance a customer was quoted last March is as much a fact about that
	// March as the rate they were charged.
	//
	// It is PINNED: it is on this row for every day of the account's life and
	// never resolves from the product, because a limit is an underwriting
	// decision about one customer rather than a price the bank publishes.
	// product.OverdraftPricing cannot express one at all, which is what makes
	// this a fact about the types rather than a rule to remember.
	OverdraftLimit ledger.Amount

	// Pricing is this customer's NEGOTIATED price, or nil to float with the
	// product version in force on the day.
	//
	// nil means float and specifically NOT interest-free: a zero-rate overlay
	// is a real interest-free product and a different, deliberate statement.
	// Validate refuses the states in between, and the schema says the same thing
	// inside the CREATE TABLE that holds the three nullable columns
	// (overdraft_terms.rate in store/sqlite/schema/bank/0001_init.sql), because the
	// difference between "NULL means free" and "NULL means ask the product" is
	// invisible in a schema dump.
	Pricing *product.OverdraftPricing

	CreatedAt time.Time // when the row was entered, not when it takes effect
}

// TermsDayKey is the day a terms row is identified by within its account.
//
// A terms row is identified by (account, day), so a second row entered for the
// same effective day replaces the first — which makes "the terms in force on
// day D" unique by construction rather than by a validation rule.
//
// A store must derive the key of a row passed to PutOverdraftTerms with this
// function, so that GetOverdraftTermsAsOf finds it: it is the value of the
// overdraft_terms.day_key column, and the column the as-of lookup compares on.
// It is the same discipline SnapshotDateKey follows, for the same reason — a
// lexicographically ordered ISO day is a total order, so "the row in force on
// day D" is settled by a string comparison and by no date arithmetic at all.
// Which day an instant falls in is a domain question and is answered here (see
// ledger.DayStart) rather than by the store.
func TermsDayKey(day time.Time) string { return ledger.DayStart(day).Format("2006-01-02") }

// Validate reports whether these terms are a product. The rules are held on the
// row so that a store round trip and a register call are checked against one
// standard.
//
// The pricing rules are product.OverdraftPricing's, because an overlay and a
// catalogue version are the same three numbers and must be judged the same way.
// The refused combination is an unarranged rate with no arranged one: it would
// price only the excess, making the money drawn beyond the limit dearer than
// nothing while leaving the facility inside it free. That is not a product, it
// is a mistake. The error is re-wrapped in this layer's sentinel so that a
// caller of the deposit layer — and the api's status mapping — has one thing to
// check.
func (t OverdraftTerms) Validate() error {
	if t.ProductID == "" {
		return ErrProductRequired
	}
	if t.OverdraftLimit < 0 {
		return ErrInvalidAmount
	}
	if t.Pricing == nil {
		return nil
	}
	if err := t.Pricing.Validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRate, err)
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

// EffectiveTerms is what an account's overdraft actually costs on one day: the
// merge of its own row and the product version in force.
//
// It is in-memory only and is never stored. That is the rule the terms timeline
// already follows one level down — current terms are resolved, never cached on
// the row — for the same reason: a second copy of a derivable fact is a second
// place it can be wrong.
type EffectiveTerms struct {
	// ProductID is which catalogue entry priced this day. It is what makes
	// "what did this account's product say on 15 July 2027?" answerable rather
	// than merely recoverable.
	ProductID product.ID
	Limit     ledger.Amount
	Pricing   product.OverdraftPricing

	// Negotiated says the pricing above came from the account's own overlay
	// rather than from the product version in force.
	//
	// It is on the resolved value rather than left for a caller to re-derive
	// because the merge is the only place that knows, and because a client that
	// cannot tell a list price from a promised one cannot answer the single
	// question a negotiated rate generates: why did my rate not move when the
	// product was repriced?
	Negotiated bool
}

// Resolve merges the timelines for one day.
//
// Order of precedence: the account's own row supplies the limit, always; the
// account's row supplies the pricing if it carries an overlay; otherwise the
// product version in force on that day does.
//
// versions is keyed by product because an account's life can span several — a
// ChangeProduct is a forward-dated row like any other — and a day resolves
// against the product in force on THAT day. A single slice would silently price
// pre-migration days from the new product, which is the class of bug this whole
// design exists to prevent. Extra keys are harmless: an accrual run loads one
// entry per distinct product across every account it touches and hands the same
// map to every call.
//
// rows must be ascending by EffectiveFrom, and so must each slice of versions.
//
// The three failures are worth telling apart, which is why this returns an error
// where termsAt returns a bool: ErrTermsNotFound for a day before the account's
// first row, product.ErrVersionNotFound for a floating day whose product had no
// published price, and product.ErrHashMismatch for a version edited in the
// database.
func Resolve(rows []OverdraftTerms, versions map[product.ID][]product.Version, day time.Time) (EffectiveTerms, error) {
	row, ok := termsAt(rows, day)
	if !ok {
		return EffectiveTerms{}, ErrTermsNotFound
	}
	out := EffectiveTerms{ProductID: row.ProductID, Limit: row.OverdraftLimit}
	if row.Pricing != nil {
		out.Pricing = *row.Pricing
		out.Negotiated = true
		return out, nil
	}
	v, err := product.VersionAt(versions[row.ProductID], day)
	if err != nil {
		return EffectiveTerms{}, err
	}
	out.Pricing = v.Overdraft
	return out, nil
}

// anyPriced reports whether any day in the account's life could carry a
// non-zero rate. It is what keeps a never-priced account from reading a
// value-dated series every night.
//
// A row with an overlay is judged on the overlay alone: a zero-rate overlay is
// a definite statement that those days are free. A FLOATING row has to look
// through to its product, which is the case the old account-level `Rate <= 0`
// guard could not express at all.
//
// It errs towards running. A product version outside the account's life still
// counts, because bounding each row's span against the next row's day would be
// a second, subtler copy of Resolve — and the cost of being wrong this way is
// one unnecessary series read, not a wrong number.
func anyPriced(rows []OverdraftTerms, versions map[product.ID][]product.Version) bool {
	for _, r := range rows {
		if r.Pricing != nil {
			if r.Pricing.Rate > 0 {
				return true
			}
			continue
		}
		for _, v := range versions[r.ProductID] {
			if v.Published() && v.Overdraft.Rate > 0 {
				return true
			}
		}
	}
	return false
}
