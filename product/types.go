package product

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// ID identifies a product within a book. A defined type, not an alias, so the
// compiler prevents passing a deposit.AccountID where a product is expected.
type ID string

// Kind is what sort of instance a product may be bound to. It is on the product
// rather than on each version because a product does not change kind: a
// timeline that is a current account in March and a term loan in April is not a
// product, it is two.
type Kind int

const (
	// CurrentAccount prices a deposit account's arranged overdraft. It is the
	// only kind implemented; the credit kinds bind nothing yet, and a term loan
	// never will float — see the design doc.
	CurrentAccount Kind = iota
)

func (k Kind) String() string {
	switch k {
	case CurrentAccount:
		return "CurrentAccount"
	default:
		return "Unknown"
	}
}

// OverdraftPricing is the FLOATING parameter group: what a product charges on a
// drawn overdraft.
//
// There is no limit field here, and the absence is the design rather than an
// omission. A limit is an underwriting decision about one customer's
// creditworthiness; a rate is a price the bank publishes. Making the catalogue
// UNABLE to express a limit means "the limit does not float" is a fact the
// compiler checks, instead of a rule a later caller has to remember.
//
// The same three fields, as a pointer, are a deposit account's negotiated
// overlay. That is not duplication to be tidied away: an overlay is by
// definition "this customer's price instead of the product's", so it is the
// product's price type or it is a second type that must be kept in step.
type OverdraftPricing struct {
	// Rate is the annual rate on the drawn balance up to the account's limit.
	// Zero makes the whole overdraft interest-free, which is a real product.
	Rate interest.Rate
	// UnarrangedRate applies beyond the limit. It is an optional SURCHARGE, not
	// a switch: zero means Rate applies throughout, never that the excess is
	// free. See Validate for the one refused combination.
	UnarrangedRate interest.Rate
	// DayCount is a product parameter, not a code path chosen by product type.
	// It floats with the price because it is part of the price: 12% on ACT/365
	// and 12% on 30/360 are different products.
	DayCount interest.DayCount
}

// Validate reports whether this is a price. It is deposit.OverdraftTerms's rule
// about the same three numbers, so a catalogue version and an account overlay
// are held to one standard.
func (p OverdraftPricing) Validate() error {
	if p.Rate < 0 || p.UnarrangedRate < 0 {
		return ErrInvalidRate
	}
	if p.Rate == 0 && p.UnarrangedRate > 0 {
		return ErrInvalidRate
	}
	return nil
}

// Product is a catalogue entry: a named thing accounts are opened from.
//
// It is separate from its versions because a product needs a name before it has
// a price, and because listing the catalogue should not mean grouping a version
// table by product.
type Product struct {
	ID   ID
	Name string
	Kind Kind

	// Retired takes the product off sale: no new account may be opened from it
	// and no account may be migrated to it. It does not affect resolution — see
	// ErrProductRetired.
	Retired bool

	CreatedAt time.Time
}

// Validate checks the fields a store cannot.
func (p Product) Validate() error {
	if p.Name == "" {
		return ErrNameRequired
	}
	if err := ledger.ValidateText("name", p.Name); err != nil {
		return err
	}
	if p.Kind.String() == "Unknown" {
		return fmt.Errorf("%w: kind %d", ErrKindMismatch, p.Kind)
	}
	return nil
}

// Version is what a product cost from one day onwards: a ROW, one per
// repricing, never changed once published.
//
// # Two dates, and what the pair means
//
// CreatedAt is when the version was drafted; EffectiveFrom is the first day it
// prices. PublishedAt is a third and it is a lifecycle fact rather than a date
// about money: a version with no PublishedAt is a draft, editable and invisible
// to resolution, which is what stops "immutable" from meaning "a typo in a rate
// is permanent" — and a rule worked around with a manual UPDATE is the thing
// being defended against.
//
// EffectiveFrom is day-granular, ledger.DayStart-truncated by the caller, on
// the same day axis accrual and deposit.OverdraftTerms already use.
type Version struct {
	ProductID     ID
	EffectiveFrom time.Time // day-truncated; the first day this version prices

	Overdraft OverdraftPricing

	// Hash is over the identity and the pricing, computed at publication and
	// VERIFIED at resolution. Storing a hash nothing reads would be theatre;
	// the verification is what makes it a control on the one hole a
	// domain-layer refusal cannot cover — a direct UPDATE to a published row.
	Hash string

	PublishedAt time.Time // zero == draft
	CreatedAt   time.Time
}

// Published reports whether this version prices anything. A draft never does.
func (v Version) Published() bool { return !v.PublishedAt.IsZero() }

// ComputeHash is the content hash: the identity and the pricing, and
// deliberately NOT PublishedAt or CreatedAt.
//
// Publication stamps a time onto a row whose content did not change, so a hash
// covering it would differ before and after publishing and could never be
// verified. The day KEY is hashed rather than the instant, for the same reason
// the row is keyed by day: two rows differing only within a day are the same
// row, and an upsert must not look like tampering.
//
// The "v1|" prefix is a format version. A future field changes the canonical
// form, and every stored hash would then fail to verify; the prefix is what
// lets that be a migration rather than a mystery.
func (v Version) ComputeHash() string {
	canonical := fmt.Sprintf("v1|%s|%s|%d|%d|%d",
		v.ProductID, VersionDayKey(v.EffectiveFrom),
		int64(v.Overdraft.Rate), int64(v.Overdraft.UnarrangedRate), int64(v.Overdraft.DayCount))
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// VerifyHash reports whether this row still holds the content it was published
// with. An unhashed row (a draft) verifies as a mismatch, which is correct:
// only a published version is ever verified, and a published one always has a
// hash.
func (v Version) VerifyHash() error {
	if v.Hash != v.ComputeHash() {
		return fmt.Errorf("%w: product %s effective %s",
			ErrHashMismatch, v.ProductID, VersionDayKey(v.EffectiveFrom))
	}
	return nil
}

// Validate checks the fields a store cannot.
func (v Version) Validate() error {
	if v.ProductID == "" {
		return ErrProductNotFound
	}
	return v.Overdraft.Validate()
}

// VersionDayKey is the day a version is identified by within its product.
//
// A version is identified by (product, day), so a second version drafted for
// the same effective day replaces the first — which makes "the version in force
// on day D" unique by construction rather than by a validation rule, and leaves
// the book's non-overlapping-interval exclusion constraint with nothing to
// enforce. It is deposit.TermsDayKey's twin, and a store must derive its key
// with it: it is the value of product_versions.day_key and the column the as-of
// lookup compares on.
func VersionDayKey(day time.Time) string { return ledger.DayStart(day).Format("2006-01-02") }

// VersionAt is the published version in force on a day: the last one whose
// EffectiveFrom is not after it, with its hash verified.
//
// rows must be ascending by EffectiveFrom, which is what
// ListProductVersions returns.
//
// Drafts are skipped rather than treated as boundaries. That distinction
// matters: an operator drafting next quarter's price must not change what today
// costs, so the row BEFORE a draft stays in force through it.
//
// It returns an error rather than a bool because the failures are now several
// and an operator needs to know which — a day before the product had any price
// is not the same event as a row that was edited in the database.
func VersionAt(rows []Version, day time.Time) (Version, error) {
	d := ledger.DayStart(day)
	// The first index effective strictly after d; the candidates are before it.
	i := sort.Search(len(rows), func(i int) bool {
		return rows[i].EffectiveFrom.After(d)
	})
	for ; i > 0; i-- {
		v := rows[i-1]
		if !v.Published() {
			continue
		}
		if err := v.VerifyHash(); err != nil {
			return Version{}, err
		}
		return v, nil
	}
	return Version{}, ErrVersionNotFound
}
