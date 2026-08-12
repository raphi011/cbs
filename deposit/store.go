package deposit

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/product"
)

// Store owns the deposit layer's persistent state. Like ledger.Store it is
// declared here, by the consumer, and implemented by store/sqlite — so the
// store package imports the domain packages and never the reverse.
type Store interface {
	Update(ctx context.Context, fn func(context.Context, Tx) error) error
	View(ctx context.Context, fn func(context.Context, Tx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// Tx embeds product.Tx — and, through it, ledger.Tx — so one concrete
// transaction spans all three layers.
type Tx interface {
	product.Tx

	// The slot mapping, which the ledger keeps beside its own tables because
	// only a bank has one. Register opens an account's lines through it — see
	// Register.principalPosition — so a deposit unit of work has to carry it.
	ledger.SlotTx

	// Named with a Deposit prefix because ledger.Tx, embedded above, already has
	// PutAccount/GetAccount/ListAccounts for GL accounts.
	PutDepositAccount(ctx context.Context, book ledger.BookID, a Account) error
	GetDepositAccount(ctx context.Context, book ledger.BookID, id AccountID) (Account, error)
	ListDepositAccounts(ctx context.Context, book ledger.BookID) ([]Account, error)

	// ListDepositAccountsByIdentifier returns every account in the book holding
	// this (scheme, value) pair, compared with Identifier.Matches — the scheme
	// exactly, the value under that scheme's own rule.
	ListDepositAccountsByIdentifier(ctx context.Context, book ledger.BookID, ident Identifier) ([]Account, error)

	// NextAddressSerial issues the next account serial this book mints an IBAN
	// from: 1, 2, 3, …, dense and gap-free.
	NextAddressSerial(ctx context.Context, book ledger.BookID) (uint64, error)

	PutHold(ctx context.Context, book ledger.BookID, h Hold) error
	GetHold(ctx context.Context, book ledger.BookID, id HoldID) (Hold, error)
	ListHoldsForAccount(ctx context.Context, book ledger.BookID, id AccountID) ([]Hold, error)
	// ActiveHoldTotal sums active, non-expired holds as at `now`.
	ActiveHoldTotal(ctx context.Context, book ledger.BookID, id AccountID, now time.Time) (ledger.Amount, error)

	PutSnapshot(ctx context.Context, book ledger.BookID, s Snapshot) error
	GetSnapshot(ctx context.Context, book ledger.BookID, id AccountID, dateKey string) (Snapshot, error)
	// ListSnapshotsForAccount returns an account's snapshots ordered by business
	// date. Not in the brief's set, but Register.ListSnapshots — a public method
	// that predates the store — has no other way to enumerate them.
	ListSnapshotsForAccount(ctx context.Context, book ledger.BookID, id AccountID) ([]Snapshot, error)

	// The account's effective-dated terms timeline.
	PutOverdraftTerms(ctx context.Context, book ledger.BookID, t OverdraftTerms) error
	ListOverdraftTermsForAccount(ctx context.Context, book ledger.BookID, id AccountID) ([]OverdraftTerms, error)
	GetOverdraftTermsAsOf(ctx context.Context, book ledger.BookID, id AccountID, day time.Time) (OverdraftTerms, error)
}

// SnapshotDateKey is the business-date key a snapshot is stored under. A
// snapshot is identified by (account, date) at day granularity, so taking a
// second snapshot for the same business date overwrites the first.
func SnapshotDateKey(date time.Time) string { return date.Format("2006-01-02") }
