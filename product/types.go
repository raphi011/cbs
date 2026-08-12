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

// Kind is what sort of instance a product may be bound to.
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
type Version struct {
	ProductID     ID
	EffectiveFrom time.Time // day-truncated; the first day this version prices

	Overdraft OverdraftPricing

	// Hash is over the identity and the pricing, computed at publication and
	// VERIFIED at resolution.
	Hash string

	PublishedAt time.Time // zero == draft
	CreatedAt   time.Time
}

// Published reports whether this version prices anything. A draft never does.
func (v Version) Published() bool { return !v.PublishedAt.IsZero() }

// ComputeHash is the content hash: the identity and the pricing, and
// deliberately NOT PublishedAt or CreatedAt.
func (v Version) ComputeHash() string {
	canonical := fmt.Sprintf("v1|%s|%s|%d|%d|%d",
		v.ProductID, VersionDayKey(v.EffectiveFrom),
		int64(v.Overdraft.Rate), int64(v.Overdraft.UnarrangedRate), int64(v.Overdraft.DayCount))
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// VerifyHash reports whether this row still holds the content it was published
// with.
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
func VersionDayKey(day time.Time) string { return ledger.DayStart(day).Format("2006-01-02") }

// VersionAt is the published version in force on a day: the last one whose
// EffectiveFrom is not after it, with its hash verified.
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
