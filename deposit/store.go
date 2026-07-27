package deposit

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Store owns the deposit layer's persistent state. Like ledger.Store it is
// declared here, by the consumer, and implemented by store/mem and store/pg —
// so the store packages import the domain packages and never the reverse.
type Store interface {
	Update(ctx context.Context, fn func(context.Context, Tx) error) error
	View(ctx context.Context, fn func(context.Context, Tx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// Tx embeds ledger.Tx so one concrete transaction spans both layers. That is
// what makes CaptureHold — a hold write plus a GL posting — a single unit of
// work rather than two that can half-fail.
type Tx interface {
	ledger.Tx

	// Named with a Deposit prefix because ledger.Tx, embedded above, already has
	// PutAccount/GetAccount/ListAccounts for GL accounts. Go rejects an interface
	// carrying two methods of the same name and different signatures, so the
	// ledger keeps the bare names and deposit prefixes.
	PutDepositAccount(ctx context.Context, book ledger.BookID, a Account) error
	GetDepositAccount(ctx context.Context, book ledger.BookID, id AccountID) (Account, error)
	ListDepositAccounts(ctx context.Context, book ledger.BookID) ([]Account, error)

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
}

// SnapshotDateKey is the business-date key a snapshot is stored under. A
// snapshot is identified by (account, date) at day granularity, so taking a
// second snapshot for the same business date overwrites the first.
//
// Store implementations must derive the key of a snapshot passed to PutSnapshot
// with this function, so that GetSnapshot finds it: mem uses it as a map key,
// pg as the value of the snapshots.date_key column.
func SnapshotDateKey(date time.Time) string { return date.Format("2006-01-02") }

// Contract notes for implementers — storetest.RunDeposit pins all of these:
//
//   - GetDepositAccount returns ErrAccountNotFound, GetHold returns
//     ErrHoldNotFound and GetSnapshot returns ErrSnapshotNotFound when the row
//     is missing. Wrapping is fine; errors.Is is what the suite checks.
//   - Every method is scoped by book: the same AccountID in two books is two
//     different accounts, exactly as in ledger.Tx.
//   - ListDepositAccounts, ListHoldsForAccount: CreatedAt ascending, ties broken
//     by the row's monotonic insertion sequence — ORDER BY created_at, seq, the
//     same rule every ledger listing follows. Never break ties on the ID: IDs are
//     counter-derived strings, so "dep_10" sorts before "dep_8" and the list
//     reorders itself the moment a counter crosses a power of ten.
//     ListSnapshotsForAccount orders by business date ascending.
//   - ActiveHoldTotal counts only holds whose Status is HoldActive and whose
//     ExpiresAt is either zero (never expires) or not before now. It is an
//     aggregate like BookBalance: an unknown account is 0, not an error.
//   - PutHold and PutDepositAccount are upserts keyed by ID; PutSnapshot is an
//     upsert keyed by (account, SnapshotDateKey(s.Date)).
//   - Writes roll back with the surrounding Update, deposit rows and ledger rows
//     together — that is the whole point of Tx embedding ledger.Tx.
