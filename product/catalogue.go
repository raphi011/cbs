package product

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Catalogue is the register over a book's products. It owns no state: products
// and versions live in a Store, exactly as the ledger's rows do, and the
// Catalogue keeps only the store handle, the book it audits through, the BookID
// both are scoped to, and its clock.
//
// # What it refuses, and why the refusals are here
//
// A store accepts any write. The rules that make a catalogue trustworthy — a
// published version is frozen, publication is forward-only, an unknown product
// has no versions — are all enforced here, in the domain layer. None of them is
// a constraint a schema could carry honestly: the first is about a row's
// PREVIOUS state, which a CHECK cannot see; the second is about today, which a
// schema has no idea of; and the third is "the parent must exist", which the
// schema refuses for the reason it refuses every other one. It is the same
// position the schema takes about text and about parent references.
type Catalogue struct {
	// store owns all persistent state of this layer.
	store Store

	// gl is the general ledger this catalogue is scoped alongside. The
	// catalogue posts nothing — a price is not a movement of money — so it is
	// held for the same reason deposit.Register holds it: one book, one
	// identity, and a later kind of product that does post has it to hand.
	gl *ledger.Book

	// bookID is the book both layers are scoped to. Every store call carries
	// it, and every audit event this layer writes is stamped with it.
	bookID ledger.BookID

	// clock is the time source. Override in tests to control time.
	clock func() time.Time
}

// NewCatalogue creates a catalogue over the given store for one book.
//
// id must be book.ID(): the catalogue's rows and the book's audit events are
// scoped by the same BookID. Share the clock with the book so that audit
// timestamps and effective dates line up across layers.
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
//
// Returns ErrNameRequired for an unnamed product, ledger.ErrInvalidText for an
// unusable name and ErrKindMismatch for an unknown kind.
func (c *Catalogue) CreateProduct(ctx context.Context, name string, kind Kind) (Product, error) {
	var out Product
	err := c.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = c.CreateProductTx(ctx, tx, name, kind)
		return err
	})
	return out, err
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
//
// A draft prices nothing: resolution skips it, so the published version before
// it stays in force through its day. That is what stops immutability from
// meaning a typo in a rate is permanent — and a rule worked around with a
// manual UPDATE is the thing this package defends against.
//
// effectiveFrom is truncated to a day. It may be in the past: only PUBLICATION
// is forward-only, and refusing a backdated draft would only mean the refusal
// arrived at a less useful moment.
//
// Returns ErrProductNotFound, ErrVersionPublished if that day is already
// published, and ErrInvalidRate.
func (c *Catalogue) DraftVersion(ctx context.Context, id ID, effectiveFrom time.Time, pricing OverdraftPricing) (Version, error) {
	var out Version
	err := c.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = c.DraftVersionTx(ctx, tx, id, effectiveFrom, pricing)
		return err
	})
	return out, err
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
// hash. From then on it prices every account bound to the product whose own row
// carries no overlay, for every day from effectiveFrom until the next published
// version.
//
// # Forward-only
//
// A version effective before today is refused with ErrRetroactivePublish. It
// would move interest already charged on every account bound to the product at
// once, and the audit log would be the only control on it; maker-checker is the
// real answer and this system has none. Retroactive repricing stays where its
// blast radius is one customer: deposit's per-account pricing overlay, which
// may be backdated and whose delta the accrual posts as ordinary correction
// interest. Correcting a mispublished rate is therefore laborious, and should
// be — it is a set of individual decisions about money already taken from named
// customers.
//
// Returns ErrProductNotFound, ErrVersionNotFound if that day has no draft,
// ErrVersionPublished if it is already published, and ErrRetroactivePublish.
func (c *Catalogue) PublishVersion(ctx context.Context, id ID, effectiveFrom time.Time) (Version, error) {
	var out Version
	err := c.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = c.PublishVersionTx(ctx, tx, id, effectiveFrom)
		return err
	})
	return out, err
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
//
// It does not unprice anything. The accounts already sold from a withdrawn
// product keep resolving against its versions for as long as they live, which
// is why Retired is checked at OpenAccount and never at resolution.
//
// Returns ErrProductNotFound.
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
//
// Returns ErrProductNotFound.
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
//
// Returns ErrProductNotFound, ErrVersionNotFound for a day the product had no
// published price on, and ErrHashMismatch for a row edited in the database.
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
//
// It reads the timeline rather than using GetProductVersionAsOf, because
// as-of is the wrong question here twice over: it skips drafts, and it returns
// the row in force on the day rather than the row FOR the day. A draft for the
// 20th must be found when the 1st is published, and must not be mistaken for
// one when the 20th is asked about.
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
