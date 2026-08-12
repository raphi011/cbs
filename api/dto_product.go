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
	// Retired takes the product off sale.
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
