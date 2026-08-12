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
type Tx interface {
	ledger.Tx

	PutProduct(ctx context.Context, book ledger.BookID, p Product) error
	GetProduct(ctx context.Context, book ledger.BookID, id ID) (Product, error)
	ListProducts(ctx context.Context, book ledger.BookID) ([]Product, error)

	// The product's version timeline.
	PutProductVersion(ctx context.Context, book ledger.BookID, v Version) error
	ListProductVersions(ctx context.Context, book ledger.BookID, id ID) ([]Version, error)
	GetProductVersionAsOf(ctx context.Context, book ledger.BookID, id ID, day time.Time) (Version, error)
}
