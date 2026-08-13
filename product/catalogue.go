package product

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/raphi011/cbs/internal/unit"
	"github.com/raphi011/cbs/ledger"
)

// Catalogue is the register over a book's products.
type Catalogue struct {
	// store owns all persistent state of this layer.
	store Store

	// gl is the general ledger this catalogue is scoped alongside.
	gl *ledger.Book

	// bookID is the book both layers are scoped to. Every store call carries
	// it, and every audit event this layer writes is stamped with it.
	bookID ledger.BookID

	// clock is the time source. Override in tests to control time.
	clock func() time.Time
}

// NewCatalogue creates a catalogue over the given store for one book.
func NewCatalogue(store Store, book *ledger.Book, id ledger.BookID, clock func() time.Time) *Catalogue {
	return &Catalogue{store: store, gl: book, bookID: id, clock: clock}
}

// Store returns the underlying store, so a caller that needs to span several
// layers in one unit of work can open the Update itself and then drive the …Tx
// methods of each layer with the resulting Tx.
func (c *Catalogue) Store() Store { return c.store }

// BookID returns the book this catalogue is scoped to.
func (c *Catalogue) BookID() ledger.BookID { return c.bookID }

func (c *Catalogue) now() time.Time { return c.clock() }

// today is the day publication is measured against. Day-granular, because
// EffectiveFrom is.
func (c *Catalogue) today() time.Time { return ledger.DayStart(c.now()) }

// appendAuditTx mirrors deposit.Register.appendAuditTx: the payload is
// marshalled now rather than held by reference, so a later mutation cannot
// rewrite history, and the event is stamped with this book's ID.
func (c *Catalogue) appendAuditTx(ctx context.Context, tx Tx, eventType, entityID string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("audit %s: marshal payload: %w", eventType, err)
	}
	id, err := tx.NextID(ctx, c.bookID, "evt")
	if err != nil {
		return err
	}
	return tx.AppendAudit(ctx, ledger.AuditEvent{
		ID:         id,
		BookID:     c.bookID,
		Scope:      ledger.ScopeProduct,
		Type:       eventType,
		EntityID:   entityID,
		Payload:    raw,
		OccurredAt: c.now(),
	})
}

// CreateProduct adds a catalogue entry. It has no price yet: DraftVersion and
// PublishVersion give it one, and until then no account can be opened from it.
func (c *Catalogue) CreateProduct(ctx context.Context, name string, kind Kind) (Product, error) {
	return unit.Run(ctx, c.store.Update, func(ctx context.Context, tx Tx) (Product, error) {
		return c.CreateProductTx(ctx, tx, name, kind)
	})
}

// CreateProductTx is CreateProduct within a caller-supplied unit of work, so a
// bank's onboarding can create its chart of accounts and its first product
// together — a bank with no product cannot open an account.
func (c *Catalogue) CreateProductTx(ctx context.Context, tx Tx, name string, kind Kind) (Product, error) {
	id, err := tx.NextID(ctx, c.bookID, "prd")
	if err != nil {
		return Product{}, err
	}
	p := Product{ID: ID(id), Name: name, Kind: kind, CreatedAt: c.now()}
	if err := p.Validate(); err != nil {
		return Product{}, err
	}
	if err := tx.PutProduct(ctx, c.bookID, p); err != nil {
		return Product{}, err
	}
	if err := c.appendAuditTx(ctx, tx, ledger.EventProductCreated, id, p); err != nil {
		return Product{}, err
	}
	return p, nil
}

// DraftVersion writes an unpublished version for one effective day, or replaces
// the draft already there.
func (c *Catalogue) DraftVersion(ctx context.Context, id ID, effectiveFrom time.Time, pricing OverdraftPricing) (Version, error) {
	return unit.Run(ctx, c.store.Update, func(ctx context.Context, tx Tx) (Version, error) {
		return c.DraftVersionTx(ctx, tx, id, effectiveFrom, pricing)
	})
}

// DraftVersionTx is DraftVersion within a caller-supplied unit of work.
func (c *Catalogue) DraftVersionTx(ctx context.Context, tx Tx, id ID, effectiveFrom time.Time, pricing OverdraftPricing) (Version, error) {
	if _, err := tx.GetProduct(ctx, c.bookID, id); err != nil {
		return Version{}, err
	}
	day := ledger.DayStart(effectiveFrom)

	existing, err := c.versionOnTx(ctx, tx, id, day)
	if err != nil {
		return Version{}, err
	}
	if existing.Published() {
		return Version{}, fmt.Errorf("%w: %s effective %s", ErrVersionPublished, id, VersionDayKey(day))
	}

	v := Version{
		ProductID:     id,
		EffectiveFrom: day,
		Overdraft:     pricing,
		CreatedAt:     c.now(),
	}
	if err := v.Validate(); err != nil {
		return Version{}, err
	}
	if err := tx.PutProductVersion(ctx, c.bookID, v); err != nil {
		return Version{}, err
	}
	if err := c.appendAuditTx(ctx, tx, ledger.EventProductVersionDrafted, string(id), v); err != nil {
		return Version{}, err
	}
	return v, nil
}

// PublishVersion freezes the draft for one effective day and stamps its content
// hash.
func (c *Catalogue) PublishVersion(ctx context.Context, id ID, effectiveFrom time.Time) (Version, error) {
	return unit.Run(ctx, c.store.Update, func(ctx context.Context, tx Tx) (Version, error) {
		return c.PublishVersionTx(ctx, tx, id, effectiveFrom)
	})
}

// PublishVersionTx is PublishVersion within a caller-supplied unit of work.
func (c *Catalogue) PublishVersionTx(ctx context.Context, tx Tx, id ID, effectiveFrom time.Time) (Version, error) {
	if _, err := tx.GetProduct(ctx, c.bookID, id); err != nil {
		return Version{}, err
	}
	day := ledger.DayStart(effectiveFrom)
	if day.Before(c.today()) {
		return Version{}, fmt.Errorf("%w: %s effective %s, today is %s",
			ErrRetroactivePublish, id, VersionDayKey(day), VersionDayKey(c.today()))
	}

	v, err := c.versionOnTx(ctx, tx, id, day)
	if err != nil {
		return Version{}, err
	}
	if v.ProductID == "" {
		return Version{}, fmt.Errorf("%w: %s effective %s", ErrVersionNotFound, id, VersionDayKey(day))
	}
	if v.Published() {
		return Version{}, fmt.Errorf("%w: %s effective %s", ErrVersionPublished, id, VersionDayKey(day))
	}

	v.PublishedAt = c.now()
	v.Hash = v.ComputeHash()
	if err := tx.PutProductVersion(ctx, c.bookID, v); err != nil {
		return Version{}, err
	}
	if err := c.appendAuditTx(ctx, tx, ledger.EventProductVersionPublished, string(id), v); err != nil {
		return Version{}, err
	}
	return v, nil
}

// RetireProduct takes a product off sale: no new account may be opened from it
// and no account may be migrated to it.
func (c *Catalogue) RetireProduct(ctx context.Context, id ID) (Product, error) {
	var out Product
	err := c.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		p, err := tx.GetProduct(ctx, c.bookID, id)
		if err != nil {
			return err
		}
		p.Retired = true
		if err := tx.PutProduct(ctx, c.bookID, p); err != nil {
			return err
		}
		if err := c.appendAuditTx(ctx, tx, ledger.EventProductRetired, string(id), p); err != nil {
			return err
		}
		out = p
		return nil
	})
	return out, err
}

// GetProduct returns one catalogue entry. Returns ErrProductNotFound.
func (c *Catalogue) GetProduct(ctx context.Context, id ID) (Product, error) {
	var out Product
	err := c.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetProduct(ctx, c.bookID, id)
		return err
	})
	return out, err
}

// ListProducts returns the whole catalogue, retired entries included: a
// withdrawn product is still the product some accounts are on.
func (c *Catalogue) ListProducts(ctx context.Context) ([]Product, error) {
	var out []Product
	err := c.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListProducts(ctx, c.bookID)
		return err
	})
	return out, err
}

// Versions returns a product's whole timeline, oldest first, drafts included.
// It is the point of making the catalogue effective-dated: the history is
// inspectable rather than merely recoverable by replaying the audit log.
func (c *Catalogue) Versions(ctx context.Context, id ID) ([]Version, error) {
	var out []Version
	err := c.store.View(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.GetProduct(ctx, c.bookID, id); err != nil {
			return err
		}
		var err error
		out, err = tx.ListProductVersions(ctx, c.bookID, id)
		return err
	})
	return out, err
}

// VersionInForce is the published version pricing a day, with its hash
// verified.
func (c *Catalogue) VersionInForce(ctx context.Context, id ID, day time.Time) (Version, error) {
	var out Version
	err := c.store.View(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.GetProduct(ctx, c.bookID, id); err != nil {
			return err
		}
		v, err := tx.GetProductVersionAsOf(ctx, c.bookID, id, ledger.DayStart(day))
		if err != nil {
			return err
		}
		if err := v.VerifyHash(); err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}

// versionOnTx is the version stored for exactly one effective day, published or
// draft, or a zero Version if that day has none.
func (c *Catalogue) versionOnTx(ctx context.Context, tx Tx, id ID, day time.Time) (Version, error) {
	rows, err := tx.ListProductVersions(ctx, c.bookID, id)
	if err != nil {
		return Version{}, err
	}
	want := VersionDayKey(day)
	for _, v := range rows {
		if VersionDayKey(v.EffectiveFrom) == want {
			return v, nil
		}
	}
	return Version{}, nil
}
