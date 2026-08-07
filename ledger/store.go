package ledger

import (
	"context"
	"time"
)

// Store owns all persistent state. Interfaces are declared here, by the
// consumer, and implemented by store/sqlite — so the store package imports the
// domain packages and never the reverse.
type Store interface {
	// Update runs fn in one atomic unit of work, retrying on serialization
	// failure: BEGIN … COMMIT, and the callback re-run from the top when the
	// database refuses a write because another unit of work holds the lock.
	Update(ctx context.Context, fn func(context.Context, Tx) error) error

	// View runs fn in a read-only unit of work.
	View(ctx context.Context, fn func(context.Context, Tx) error) error

	// Reset discards all state, leaving a store that behaves like a freshly
	// migrated one.
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

	// Assets are not here. An asset definition is a fact about the world, not
	// per-book state, so it lives in code (see LookupAsset) and a store never
	// sees one. What a store does persist is every row denominated in an
	// asset: Account.Asset, deposit.Account.Asset, and a participant's
	// per-asset plumbing accounts.

	PutLedger(ctx context.Context, book BookID, l Ledger) error
	GetLedger(ctx context.Context, book BookID, id LedgerID) (Ledger, error)
	ListLedgers(ctx context.Context, book BookID) ([]Ledger, error)

	PutSubledger(ctx context.Context, book BookID, sl Subledger) error
	GetSubledger(ctx context.Context, book BookID, id SubledgerID) (Subledger, error)
	ListSubledgers(ctx context.Context, book BookID) ([]Subledger, error)

	PutAccount(ctx context.Context, book BookID, a Account) error
	GetAccount(ctx context.Context, book BookID, id AccountID) (Account, error)
	ListAccounts(ctx context.Context, book BookID) ([]Account, error)

	// LockAccounts asks that a balance check and the posting that follows it be
	// one serialized step, taking the accounts in a deterministic order so that
	// two callers over overlapping sets cannot each hold what the other needs.
	//
	// It states the requirement rather than a mechanism, and a store may already
	// meet it without doing anything: store/sqlite implements it as a no-op,
	// because SQLite admits one writer and refuses a transaction that read at
	// one snapshot and then writes after somebody else has committed — which
	// Store.Update answers by re-running the unit of work with fresh reads. The
	// deterministic order is what a store buying this with real locks needs; see
	// store/sqlite.LockAccounts, which measured what a locking version bought
	// there.
	LockAccounts(ctx context.Context, book BookID, ids []AccountID) error

	// PutTransaction stores a transaction, its ordered entries and its
	// idempotency claim. A non-empty key already held by a different
	// transaction fails with ErrDuplicateIdempotencyKey.
	//
	// That error is a documented answer rather than a broken unit of work: a caller
	// may handle it and go on using the same Tx. Every sentinel a store returns
	// from a write carries the same promise, and keeping it is the store's job
	// however its database behaves.
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

	// ValueDateBalance is BookBalance restricted to entries that take economic
	// effect before the bound: it aggregates entries whose value date is
	// strictly less than before.
	//
	// before is an exclusive UTC-midnight bound the caller has already snapped
	// with NextDay. Stores compare raw timestamps against it and do no
	// truncation of their own — see DayStart.
	//
	// It obeys BookBalance's two rules. Reversed transactions count, because a
	// reversal posts its own mirrored entries and those are what cancel the
	// original. An account with no entries is 0, including one that does not
	// exist; callers wanting ErrAccountNotFound read the account first.
	//
	// An entry with a zero ValueDate is excluded from every bound, not included in
	// all of them: it has not been assigned economic effect, so it cannot be said
	// to have taken effect before any date. Book resolves every entry it posts, so
	// no entry written through the ledger is ever in this case — it can only arise
	// from a Tx caller constructing an Entry directly, as store/storetest's
	// fixtures do.
	ValueDateBalance(ctx context.Context, book BookID, id AccountID, normal Direction, before time.Time) (Amount, error)

	// ValueDatedSeries returns the balance carried into from, plus the net
	// movement on each value date in [from, to) that had any.
	//
	// from and to are UTC midnights the caller has already snapped, to
	// exclusive. Movements are ascending and days with no movement are omitted.
	// Opening is exactly ValueDateBalance at from, and the same two rules apply:
	// reversed transactions count, and an unknown account is empty rather than
	// an error.
	//
	// Like ValueDateBalance, an entry with a zero ValueDate is not value-dated
	// and takes no day. It is excluded from every bound and from every bucket,
	// rather than bucketed onto time.Time{}'s day — year 1 — which is where a
	// naive "before the bound" test would put it. store/sqlite gets both halves
	// from storing a zero date as NULL: NULL fails every comparison and falls
	// out of every grouping.
	ValueDatedSeries(ctx context.Context, book BookID, id AccountID, normal Direction, from, to time.Time) (Series, error)

	AppendAudit(ctx context.Context, e AuditEvent) error
	ListAudit(ctx context.Context, f AuditFilter) ([]AuditEvent, error)

	Now() time.Time
}
