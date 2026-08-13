package ledger

import (
	"context"
	"iter"
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

// BankStore is a BANK's ledger store: its unit of work carries the slot mapping
// as well as the ledger, because slot_accounts is in the bank schema alone.
type BankStore interface {
	Update(ctx context.Context, fn func(context.Context, BankTx) error) error
	View(ctx context.Context, fn func(context.Context, BankTx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// CommonTx is what EVERY institution's unit of work carries, whatever tables it
// has: an id allocator and the audit trail. id_sequences and audit_events are
// in all three schemas, so nothing here can be a crossing.
type CommonTx interface {
	// NextID allocates an identity. Gap-free per book; rolls back with the
	// transaction.
	NextID(ctx context.Context, book BookID, prefix string) (string, error)

	AppendAudit(ctx context.Context, e AuditEvent) error
	ListAudit(ctx context.Context, f AuditFilter) ([]AuditEvent, error)
}

// SlotTx is the slot mapping: which account a posting flow writes to.
type SlotTx interface {
	PutSlotAccount(ctx context.Context, book BookID, row SlotAccount) error
	GetSlotAccount(ctx context.Context, book BookID, product, slot string, asset AssetCode) (AccountID, error)
	ListSlotAccounts(ctx context.Context, book BookID) ([]SlotAccount, error)
}

// BankTx is the ledger as a BANK holds it: the ledger proper plus the slot
// mapping.
type BankTx interface {
	Tx
	SlotTx
}

// Tx is one unit of work over the ledger layer. deposit.Tx and payment.BankTx
// embed it, so a single concrete Tx spans all three layers and cross-layer
// operations commit or roll back together.
type Tx interface {
	CommonTx

	NextSubledgerBlock(ctx context.Context, book BookID) (int, error)
	NextAccountSeq(ctx context.Context, book BookID, typeBlock int, subledger SubledgerID) (int, error)

	// Assets are not here. An asset definition is a fact about the world, not
	// per-book state, so it lives in code (see LookupAsset) and a store never sees
	// one.

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
	LockAccounts(ctx context.Context, book BookID, ids []AccountID) error

	// PutTransaction stores a transaction, its ordered entries and its idempotency
	// claim. A non-empty key already held by a different transaction fails with
	// ErrDuplicateIdempotencyKey.
	PutTransaction(ctx context.Context, book BookID, t Transaction) error
	GetTransaction(ctx context.Context, book BookID, id TransactionID) (Transaction, error)
	GetTransactionByIdempotencyKey(ctx context.Context, book BookID, key string) (Transaction, error)
	ListTransactions(ctx context.Context, book BookID) ([]Transaction, error)

	// ListTransactionsForPosition returns every transaction with a leg on the
	// position: on the account, or on one subsidiary within it. A transaction is
	// returned whole — all of its legs, not only the matching ones.
	ListTransactionsForPosition(ctx context.Context, book BookID, pos Position) ([]Transaction, error)

	// MarkReversed sets status to Reversed only if it is currently Posted, and
	// returns ErrTransactionAlreadyReversed otherwise. Conditional because no mutex
	// covers it and a read-compare-write would race.
	MarkReversed(ctx context.Context, book BookID, id TransactionID) error

	// ScanEntries streams the position's entries, narrowed by the filter, in no
	// promised order. A balance is the fold over it, and BookBalance is that fold
	// — the store yields rows and the domain does the arithmetic.
	ScanEntries(ctx context.Context, book BookID, pos Position, f EntryFilter) iter.Seq2[Entry, error]

	// ValueDateBalance is BookBalance restricted to entries that take economic
	// effect before the bound: it aggregates entries whose value date is strictly
	// less than before.
	ValueDateBalance(ctx context.Context, book BookID, pos Position, normal Direction, before time.Time) (Amount, error)

	// SubsidiaryBalances is the balance of every subsidiary under an account, in
	// one query rather than one BookBalance per subsidiary: the caller does not
	// know the subsidiaries before it asks, which is the whole difference between
	// this and reading a Position.
	SubsidiaryBalances(ctx context.Context, book BookID, account AccountID, normal Direction) ([]SubsidiaryBalance, error)

	// ValueDatedSeries returns the balance carried into from, plus the net
	// movement on each value date in [from, to) that had any.
	ValueDatedSeries(ctx context.Context, book BookID, pos Position, normal Direction, from, to time.Time) (Series, error)
}
