package product

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Store owns the catalogue's persistent state. Declared here, by the consumer,
// and implemented by store/sqlite — so the store package imports the domain
// packages and never the reverse.
type Store interface {
	Update(ctx context.Context, fn func(context.Context, Tx) error) error
	View(ctx context.Context, fn func(context.Context, Tx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// Tx embeds ledger.Tx so that one concrete transaction spans the catalogue and
// the ledger — and, because deposit.Tx in turn embeds this one, so that opening
// an account can validate its product and write its first terms row in a single
// unit of work.
//
// Products are book-scoped like every other entity here: the same ID in two
// books is two products, because a catalogue belongs to a bank.
type Tx interface {
	ledger.Tx

	PutProduct(ctx context.Context, book ledger.BookID, p Product) error
	GetProduct(ctx context.Context, book ledger.BookID, id ID) (Product, error)
	ListProducts(ctx context.Context, book ledger.BookID) ([]Product, error)

	// The product's version timeline. Two readers rather than one, because the
	// two callers want different things: the accrual walk wants the whole
	// timeline in one read and resolves per day in Go, while an operator view
	// wants the single version in force now and should not pay for history.
	// It is the same split deposit's terms methods make, for the same reason.
	PutProductVersion(ctx context.Context, book ledger.BookID, v Version) error
	ListProductVersions(ctx context.Context, book ledger.BookID, id ID) ([]Version, error)
	GetProductVersionAsOf(ctx context.Context, book ledger.BookID, id ID, day time.Time) (Version, error)
}

// Contract notes for implementers — storetest.RunProduct pins all of these:
//
//   - PutProduct is an upsert keyed by (book, ID). PutProductVersion is an
//     upsert keyed by (book, ProductID, VersionDayKey(v.EffectiveFrom)): two
//     versions drafted for the same product and the same effective day are the
//     same row, and the later one wins.
//   - A store NEVER refuses a write on PublishedAt grounds. Freezing a
//     published version is the Catalogue's job, in the domain layer, and a
//     CHECK could not do it anyway: what makes the write illegal is the row's
//     PREVIOUS state, which a constraint on the row being written cannot see.
//     Version.Hash is the one control that survives a direct UPDATE.
//   - GetProduct returns ErrProductNotFound and GetProductVersionAsOf returns
//     ErrVersionNotFound when the row is missing. Wrapping is fine; errors.Is
//     is what the suite checks.
//   - GetProductVersionAsOf considers PUBLISHED rows only, and returns the
//     greatest effective day not after `day`. Unlike ActiveHoldTotal it is not
//     an aggregate: a product with no published version has no price, and
//     reporting a zero row would read as a real interest-free product.
//   - Neither reader verifies the hash. VersionAt does, in the domain, so that
//     one rule lives in one place and a store cannot disagree with it.
//   - ListProducts orders by CreatedAt ascending, ties broken by the row's
//     monotonic insertion sequence — ORDER BY created_at, seq, the rule every
//     listing here follows. Never break ties on the ID.
//   - ListProductVersions orders by effective day ASCENDING and includes
//     drafts. Ascending is not a convenience: VersionAt binary-searches the
//     slice it is handed. Drafts are included because the domain decides what a
//     draft means, and because an operator view needs to see one.
//   - The store truncates nothing. Callers pass an already-DayStart-ed instant
//     and the store keys on VersionDayKey of it.
//   - Writes roll back with the surrounding Update, catalogue rows and ledger
//     rows together — that is what Tx embedding ledger.Tx exists for.
