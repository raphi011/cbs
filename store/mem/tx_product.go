package mem

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/product"
)

// ---------------------------------------------------------------------------
// The product catalogue
// ---------------------------------------------------------------------------

// The same *tx serves the catalogue and the ledger, which is what lets opening
// an account validate its product and write its first terms row in one unit of
// work. Asserted here rather than left to the adapter, whose type assertion is
// a runtime panic rather than a compile error.
var _ product.Tx = (*tx)(nil)

func (t *tx) PutProduct(ctx context.Context, book ledger.BookID, p product.Product) error {
	if err := t.write(); err != nil {
		return err
	}
	t.state.insertSeq(book, kindProduct, string(p.ID))
	bucket(t.state.products, book)[p.ID] = p
	return nil
}

func (t *tx) GetProduct(ctx context.Context, book ledger.BookID, id product.ID) (product.Product, error) {
	p, ok := t.state.products[book][id]
	if !ok {
		return product.Product{}, product.ErrProductNotFound
	}
	return p, nil
}

// ListProducts orders by CreatedAt then insertion sequence, the rule every
// listing here follows.
func (t *tx) ListProducts(ctx context.Context, book ledger.BookID) ([]product.Product, error) {
	out := make([]product.Product, 0, len(t.state.products[book]))
	for _, p := range t.state.products[book] {
		out = append(out, p)
	}
	sortRows(t.state, out, book, kindProduct, func(p product.Product) (time.Time, string) {
		return p.CreatedAt, string(p.ID)
	})
	return out, nil
}

// PutProductVersion upserts under (product, effective day): a second version
// drafted for the same day replaces the first, which is what makes "the version
// in force on day D" unique by construction rather than by a validation rule.
//
// It does NOT refuse a write to a published row. Freezing a published version
// is product.Catalogue's job: a store that refused would accept a different set
// of writes from store/pg, which is the one divergence this package must never
// introduce.
func (t *tx) PutProductVersion(ctx context.Context, book ledger.BookID, v product.Version) error {
	if err := t.write(); err != nil {
		return err
	}
	key := versionKey{productID: v.ProductID, dayKey: product.VersionDayKey(v.EffectiveFrom)}
	t.state.insertSeq(book, kindProductVersion, versionSeqID(key))
	bucket(t.state.productVersions, book)[key] = v
	return nil
}

// ListProductVersions returns a product's whole timeline, drafts included,
// ascending by effective day. Ascending is load-bearing: product.VersionAt
// binary-searches the slice this returns.
func (t *tx) ListProductVersions(ctx context.Context, book ledger.BookID, id product.ID) ([]product.Version, error) {
	out := make([]product.Version, 0)
	for key, v := range t.state.productVersions[book] {
		if key.productID == id {
			out = append(out, v)
		}
	}
	// The effective day is already a total order within a product, so the
	// insertion sequence is the tie-break only for form.
	sortRows(t.state, out, book, kindProductVersion, func(v product.Version) (time.Time, string) {
		return v.EffectiveFrom, versionSeqID(versionKey{
			productID: v.ProductID, dayKey: product.VersionDayKey(v.EffectiveFrom),
		})
	})
	return out, nil
}

// GetProductVersionAsOf is the PUBLISHED version in force on a day: the
// greatest effective day not after it, skipping drafts.
//
// It scans rather than binary-searching, because the map is unordered and
// building the ordered slice to search would cost more than the scan. A product
// with no published version before the day has no price, which is
// ErrVersionNotFound rather than a zero row that would read as a real
// interest-free product.
//
// It does not verify the hash. product.VersionAt does, in the domain, so that
// one rule lives in one place.
func (t *tx) GetProductVersionAsOf(ctx context.Context, book ledger.BookID, id product.ID, day time.Time) (product.Version, error) {
	want := product.VersionDayKey(day)
	var (
		best  product.Version
		found bool
	)
	for key, v := range t.state.productVersions[book] {
		if key.productID != id || key.dayKey > want || !v.Published() {
			continue
		}
		if !found || key.dayKey > product.VersionDayKey(best.EffectiveFrom) {
			best, found = v, true
		}
	}
	if !found {
		return product.Version{}, product.ErrVersionNotFound
	}
	return best, nil
}

// versionSeqID renders a version's composite key as the string sortRows and
// insertSeq identify rows by.
func versionSeqID(k versionKey) string { return string(k.productID) + "/" + k.dayKey }
