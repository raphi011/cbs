package api

import (
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/product"
)

// Wire format for the product catalogue: entries, their effective-dated
// versions, and the requests that create them.

// ProductDTO is a catalogue entry. Kind crosses as a string for the reason
// FacilityDTO's does: an integer whose meaning a client learns from
// documentation is an integer a client renders wrong.
type ProductDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Retired takes the product off sale. It does NOT unprice the accounts
	// already sold from it, which keep resolving against its versions for as
	// long as they live — so a client must not hide a retired product from an
	// account that is on it, only from the list it offers.
	Retired   bool      `json:"retired"`
	CreatedAt time.Time `json:"createdAt"`
}

func ToProductDTO(p product.Product) ProductDTO {
	return ProductDTO{
		ID:        string(p.ID),
		Name:      p.Name,
		Kind:      p.Kind.String(),
		Retired:   p.Retired,
		CreatedAt: p.CreatedAt,
	}
}

// ProductVersionDTO is one row of a product's effective-dated timeline.
//
// It carries the publication state explicitly rather than leaving a client to
// infer it from a zero timestamp, and carries the hash because a client showing
// an operator "this is what was published" should be able to show what it is
// pinned to. A draft has neither: publishing is what stamps both.
//
// effectiveFrom is the day the version starts pricing and createdAt is when it
// was drafted — the booking-date/value-date distinction applied to a price.
type ProductVersionDTO struct {
	ProductID      string     `json:"productId"`
	EffectiveFrom  time.Time  `json:"effectiveFrom"`
	Rate           int64      `json:"rate"`
	UnarrangedRate int64      `json:"unarrangedRate"`
	RateScale      int64      `json:"rateScale"`
	DayCount       string     `json:"dayCount"`
	Published      bool       `json:"published"`
	PublishedAt    *time.Time `json:"publishedAt,omitempty"`
	Hash           string     `json:"hash"`
	CreatedAt      time.Time  `json:"createdAt"`
}

func ToProductVersionDTO(v product.Version) ProductVersionDTO {
	out := ProductVersionDTO{
		ProductID:      string(v.ProductID),
		EffectiveFrom:  v.EffectiveFrom,
		Rate:           int64(v.Overdraft.Rate),
		UnarrangedRate: int64(v.Overdraft.UnarrangedRate),
		RateScale:      interest.RateScale,
		DayCount:       v.Overdraft.DayCount.String(),
		Published:      v.Published(),
		Hash:           v.Hash,
		CreatedAt:      v.CreatedAt,
	}
	if v.Published() {
		published := v.PublishedAt
		out.PublishedAt = &published
	}
	return out
}

// CreateProductRequest names a product. It has no price: DraftVersion and
// PublishVersion give it one, and until then no account can be opened from it.
type CreateProductRequest struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// DraftVersionRequest is a price for one effective day, unpublished.
//
// effectiveFrom may point into the past — only PUBLICATION is forward-only, and
// refusing a backdated draft would only mean the refusal arrived at a less
// useful moment. There is no limit field, and the absence is the design: a limit
// is an underwriting decision about one customer, so product.OverdraftPricing
// cannot express one.
type DraftVersionRequest struct {
	EffectiveFrom  time.Time `json:"effectiveFrom"`
	Rate           int64     `json:"rate"`
	UnarrangedRate int64     `json:"unarrangedRate"`
	DayCount       string    `json:"dayCount"`
}

// ---------------------------------------------------------------------------
// Enum parsing
// ---------------------------------------------------------------------------

// KindFromString parses a product kind, so an unknown one is a 400 naming the
// field rather than a silent CurrentAccount — the same reason
// FacilityKindFromString exists.
func KindFromString(s string) (product.Kind, error) {
	switch s {
	case "CurrentAccount":
		return product.CurrentAccount, nil
	default:
		return 0, BadRequest("invalid product kind %q (want CurrentAccount)", s)
	}
}
