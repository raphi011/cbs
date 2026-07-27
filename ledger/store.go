package ledger

import (
	"context"
	"time"
)

// Store owns all persistent state. Interfaces are declared here, by the
// consumer, and implemented by store/mem and store/pg — so the store packages
// import the domain packages and never the reverse.
type Store interface {
	// Update runs fn in one atomic unit of work, retrying on serialization
	// failure. mem takes the write lock; pg runs BEGIN … COMMIT.
	Update(ctx context.Context, fn func(context.Context, Tx) error) error

	// View runs fn in a read-only unit of work.
	View(ctx context.Context, fn func(context.Context, Tx) error) error

	// Reset discards all state: fresh maps for mem, TRUNCATE for pg.
	Reset(ctx context.Context) error

	Close() error
}

// Tx is one unit of work over the ledger layer. deposit.Tx and payment.Tx embed
// it, so a single concrete Tx spans all three layers and cross-layer operations
// commit or roll back together.
type Tx interface {
	// Identity allocation. Gap-free per book; rolls back with the transaction.
	NextID(ctx context.Context, book BookID, prefix string) (string, error)
	NextSubledgerBlock(ctx context.Context, book BookID) (int, error)
	NextAccountSeq(ctx context.Context, book BookID, typeBlock int, subledger SubledgerID) (int, error)

	// Assets are per book. PutAsset is an upsert, like every other Put here;
	// the duplicate check lives in Book.CreateAssetTx, so a store never has
	// to know the difference between registering and correcting an asset.
	PutAsset(ctx context.Context, book BookID, a AssetDef) error
	GetAsset(ctx context.Context, book BookID, code AssetCode) (AssetDef, error)
	ListAssets(ctx context.Context, book BookID) ([]AssetDef, error)

	PutLedger(ctx context.Context, book BookID, l Ledger) error
	GetLedger(ctx context.Context, book BookID, id LedgerID) (Ledger, error)
	ListLedgers(ctx context.Context, book BookID) ([]Ledger, error)

	PutSubledger(ctx context.Context, book BookID, sl Subledger) error
	GetSubledger(ctx context.Context, book BookID, id SubledgerID) (Subledger, error)
	ListSubledgers(ctx context.Context, book BookID) ([]Subledger, error)

	PutAccount(ctx context.Context, book BookID, a Account) error
	GetAccount(ctx context.Context, book BookID, id AccountID) (Account, error)
	ListAccounts(ctx context.Context, book BookID) ([]Account, error)

	// LockAccounts takes a write lock on the given accounts in a deterministic
	// order, so a balance check and the posting that follows it are one
	// serialized step. mem: a no-op under the write lock. pg: SELECT … FOR
	// UPDATE ordered by id, which also prevents deadlock between transactions
	// with overlapping account sets.
	LockAccounts(ctx context.Context, book BookID, ids []AccountID) error

	// PutTransaction stores a transaction, its ordered entries and its
	// idempotency claim. A non-empty key already held by a different
	// transaction fails with ErrDuplicateIdempotencyKey.
	//
	// That error is a documented answer rather than a broken unit of work: a
	// caller may handle it and go on using the same Tx. store/pg makes that
	// true by running the insert inside a SAVEPOINT, because in Postgres any
	// error otherwise aborts the whole transaction. Every sentinel a store
	// returns from a write carries the same promise.
	//
	// A Put is an upsert, so re-putting a transaction under a different key
	// RELEASES the old one: it is free for another transaction to claim.
	PutTransaction(ctx context.Context, book BookID, t Transaction) error
	GetTransaction(ctx context.Context, book BookID, id TransactionID) (Transaction, error)
	GetTransactionByIdempotencyKey(ctx context.Context, book BookID, key string) (Transaction, error)
	ListTransactions(ctx context.Context, book BookID) ([]Transaction, error)
	ListTransactionsForAccount(ctx context.Context, book BookID, id AccountID) ([]Transaction, error)

	// MarkReversed sets status to Reversed only if it is currently Posted, and
	// returns ErrTransactionAlreadyReversed otherwise. Conditional because a
	// read-compare-write would race once a mutex no longer covers it.
	MarkReversed(ctx context.Context, book BookID, id TransactionID) error

	// BookBalance aggregates entries rather than replaying them in Go. normal is
	// the account type's normal direction; entries in that direction add.
	BookBalance(ctx context.Context, book BookID, id AccountID, normal Direction) (Amount, error)

	AppendAudit(ctx context.Context, e AuditEvent) error
	ListAudit(ctx context.Context, f AuditFilter) ([]AuditEvent, error)

	Now() time.Time
}
