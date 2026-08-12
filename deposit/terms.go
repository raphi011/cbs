package deposit

import (
	"fmt"
	"sort"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/product"
)

// OverdraftTerms is what an account's arranged overdraft cost from one day
// onwards.
type OverdraftTerms struct {
	AccountID     AccountID
	EffectiveFrom time.Time // day-truncated; the first day these terms apply

	// ProductID is the catalogue entry this account is on from this day.
	ProductID product.ID

	// OverdraftLimit is the positive amount the balance may go below zero by; 0
	// means none.
	OverdraftLimit ledger.Amount

	// Pricing is this customer's NEGOTIATED price, or nil to float with the
	// product version in force on the day.
	Pricing *product.OverdraftPricing

	CreatedAt time.Time // when the row was entered, not when it takes effect
}

// TermsDayKey is the day a terms row is identified by within its account.
func TermsDayKey(day time.Time) string { return ledger.DayStart(day).Format("2006-01-02") }

// Validate reports whether these terms are a product. The rules are held on the
// row so that a store round trip and a register call are checked against one
// standard.
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
type EffectiveTerms struct {
	// ProductID is which catalogue entry priced this day. It is what makes
	// "what did this account's product say on 15 July 2027?" answerable rather
	// than merely recoverable.
	ProductID product.ID
	Limit     ledger.Amount
	Pricing   product.OverdraftPricing

	// Negotiated says the pricing above came from the account's own overlay rather
	// than from the product version in force.
	Negotiated bool
}

// Resolve merges the timelines for one day.
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
