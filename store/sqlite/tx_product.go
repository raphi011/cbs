package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/product"
)

// ---------------------------------------------------------------------------
// The product catalogue
// ---------------------------------------------------------------------------

// One *tx spans the catalogue and the ledger, so catalogue rows and GL rows
// commit or roll back together — which under a real BEGIN/COMMIT is a claim
// about this code rather than a side effect of one process-wide lock.
var _ product.Tx = (*tx)(nil)

func (t *tx) PutProduct(ctx context.Context, book ledger.BookID, p product.Product) error {
	if err := t.own(book); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	// Retired is a Go bool against an INTEGER column, which is what the schema's
	// BOOLEAN was underneath: the driver binds it as 0 or 1 and reads it back as
	// a bool. See products.retired.
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO products (book_id, id, name, kind, retired, created_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, `+nextRowSeq("products")+`)
		ON CONFLICT (book_id, id) DO UPDATE SET
			name       = EXCLUDED.name,
			kind       = EXCLUDED.kind,
			retired    = EXCLUDED.retired,
			created_at = EXCLUDED.created_at`,
		string(book), string(p.ID), p.Name, int64(p.Kind), p.Retired, nullTime{p.CreatedAt})
	if err != nil {
		return fmt.Errorf("sqlite: put product %s: %w", p.ID, err)
	}
	return nil
}

const productColumns = `id, name, kind, retired, created_at`

func scanProduct(row interface{ Scan(...any) error }) (product.Product, error) {
	var (
		out     product.Product
		kind    int64
		created nullTime
	)
	if err := row.Scan(&out.ID, &out.Name, &kind, &out.Retired, &created); err != nil {
		return product.Product{}, err
	}
	out.Kind = product.Kind(kind)
	out.CreatedAt = created.Time
	return out, nil
}

func (t *tx) GetProduct(ctx context.Context, book ledger.BookID, id product.ID) (product.Product, error) {
	if err := t.own(book); err != nil {
		return product.Product{}, err
	}
	row := t.tx.QueryRowContext(ctx, `
		SELECT `+productColumns+` FROM products WHERE book_id = ? AND id = ?`,
		string(book), string(id))
	out, err := scanProduct(row)
	if errors.Is(err, sql.ErrNoRows) {
		return product.Product{}, product.ErrProductNotFound
	}
	if err != nil {
		return product.Product{}, fmt.Errorf("sqlite: get product %s: %w", id, err)
	}
	return out, nil
}

func (t *tx) ListProducts(ctx context.Context, book ledger.BookID) ([]product.Product, error) {
	if err := t.own(book); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx, `
		SELECT `+productColumns+` FROM products WHERE book_id = ?
		ORDER BY created_at ASC NULLS FIRST, seq`, string(book))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list products: %w", err)
	}
	defer rows.Close()

	out := make([]product.Product, 0)
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list products: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PutProductVersion upserts under (product, effective day). The day key is
// derived with product.VersionDayKey — the same function GetProductVersionAsOf
// compares against.
//
// It does not refuse a write to a published row, and could not: what makes such
// a write illegal is the row's PREVIOUS state, which neither this statement nor
// a CHECK can see. product.Catalogue refuses it, and the version hash is the one
// control that survives a direct UPDATE — see the schema, on
// product_versions.published_at.
func (t *tx) PutProductVersion(ctx context.Context, book ledger.BookID, v product.Version) error {
	if err := t.own(book); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	if err := t.ensureBook(ctx, book); err != nil {
		return err
	}
	_, err := t.tx.ExecContext(ctx, `
		INSERT INTO product_versions (
			book_id, product_id, day_key, effective_from,
			rate, unarranged_rate, day_count, hash, published_at, created_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+nextRowSeq("product_versions")+`)
		ON CONFLICT (book_id, product_id, day_key) DO UPDATE SET
			effective_from  = EXCLUDED.effective_from,
			rate            = EXCLUDED.rate,
			unarranged_rate = EXCLUDED.unarranged_rate,
			day_count       = EXCLUDED.day_count,
			hash            = EXCLUDED.hash,
			published_at    = EXCLUDED.published_at,
			created_at      = EXCLUDED.created_at`,
		string(book), string(v.ProductID), product.VersionDayKey(v.EffectiveFrom),
		nullTime{v.EffectiveFrom},
		int64(v.Overdraft.Rate), int64(v.Overdraft.UnarrangedRate), int64(v.Overdraft.DayCount),
		v.Hash, nullTime{v.PublishedAt}, nullTime{v.CreatedAt})
	if err != nil {
		return fmt.Errorf("sqlite: put product version %s/%s: %w",
			v.ProductID, product.VersionDayKey(v.EffectiveFrom), err)
	}
	return nil
}

// productVersionColumns is the select list both readers use, for the reason
// overdraftTermsColumns exists: it stops the two from scanning different column
// sets, which is a whole class of "it round-trips one way".
const productVersionColumns = `
	product_id, effective_from, rate, unarranged_rate, day_count,
	hash, published_at, created_at`

func scanProductVersion(row interface{ Scan(...any) error }) (product.Version, error) {
	var (
		out                             product.Version
		rate, unarranged                int64
		dayCount                        int64
		effective, publishedAt, created nullTime
	)
	if err := row.Scan(&out.ProductID, &effective, &rate, &unarranged, &dayCount,
		&out.Hash, &publishedAt, &created); err != nil {
		return product.Version{}, err
	}
	out.Overdraft = product.OverdraftPricing{
		Rate:           interest.Rate(rate),
		UnarrangedRate: interest.Rate(unarranged),
		DayCount:       interest.DayCount(dayCount),
	}
	out.EffectiveFrom = effective.Time
	out.PublishedAt = publishedAt.Time
	out.CreatedAt = created.Time
	return out, nil
}

// ListProductVersions returns the whole timeline, drafts included, ascending by
// day_key — an ISO day and therefore lexicographically ordered. Ascending is
// load-bearing: product.VersionAt binary-searches the slice this returns.
func (t *tx) ListProductVersions(ctx context.Context, book ledger.BookID, id product.ID) ([]product.Version, error) {
	if err := t.own(book); err != nil {
		return nil, err
	}
	rows, err := t.tx.QueryContext(ctx, `
		SELECT `+productVersionColumns+`
		FROM product_versions WHERE book_id = ? AND product_id = ?
		ORDER BY day_key ASC, seq`, string(book), string(id))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list product versions: %w", err)
	}
	defer rows.Close()

	out := make([]product.Version, 0)
	for rows.Next() {
		v, err := scanProductVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("sqlite: list product versions: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetProductVersionAsOf is the published version in force on a day. It compares
// day_key rather than effective_from so that the bound is a DAY on both sides,
// and filters on published_at so a draft is invisible — the row before it stays
// in force through it.
func (t *tx) GetProductVersionAsOf(ctx context.Context, book ledger.BookID, id product.ID, day time.Time) (product.Version, error) {
	if err := t.own(book); err != nil {
		return product.Version{}, err
	}
	row := t.tx.QueryRowContext(ctx, `
		SELECT `+productVersionColumns+`
		FROM product_versions
		WHERE book_id = ? AND product_id = ? AND day_key <= ? AND published_at IS NOT NULL
		ORDER BY day_key DESC
		LIMIT 1`, string(book), string(id), product.VersionDayKey(day))
	out, err := scanProductVersion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return product.Version{}, product.ErrVersionNotFound
	}
	if err != nil {
		return product.Version{}, fmt.Errorf("sqlite: product version for %s as of %s: %w",
			id, product.VersionDayKey(day), err)
	}
	return out, nil
}
