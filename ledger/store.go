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

// BankStore is a BANK's ledger store: its unit of work carries the slot mapping
// as well as the ledger, because slot_accounts is in the bank schema alone.
//
// Store and this are the two institutions that keep a book of accounts, and the
// difference between them is exactly those three methods. Book takes the
// narrower one, so the settlement agent gets a general ledger without a mapping
// it has no table for.
type BankStore interface {
	Update(ctx context.Context, fn func(context.Context, BankTx) error) error
	View(ctx context.Context, fn func(context.Context, BankTx) error) error
	Reset(ctx context.Context) error
	Close() error
}

// CommonTx is what EVERY institution's unit of work carries, whatever tables it
// has: an id allocator, a clock, and the audit trail. id_sequences and
// audit_events are in all three schemas, so nothing here can be a crossing.
//
// It is separate from Tx because the clearing house keeps an audit trail and has
// no book of accounts at all. A transaction type that reached the audit through
// the ledger would hand the clearing house a ledger to go with it, which is the
// crossing the store split exists to refuse.
type CommonTx interface {
	// NextID allocates an identity. Gap-free per book; rolls back with the
	// transaction.
	NextID(ctx context.Context, book BookID, prefix string) (string, error)

	AppendAudit(ctx context.Context, e AuditEvent) error
	ListAudit(ctx context.Context, f AuditFilter) ([]AuditEvent, error)

	Now() time.Time
}

// SlotTx is the slot mapping: which account a posting flow writes to.
//
// It is a BANK's and no other institution's — slot_accounts is created by the
// bank schema alone, because a slot is a line in a chart of accounts that sells
// products, and the settlement agent's ledger holds reserve accounts and
// nothing that posts through a product.
//
// PutSlotAccount is an upsert keyed by (book, product, slot, asset) —
// repointing a slot is the same act as pointing it, and a store that appended
// instead would leave two answers to a question that has one.
//
// GetSlotAccount returns ErrSlotNotMapped for a triple with no row. It does NOT
// fall back to the bank-wide row: the fallback is Book.SlotAccountTx's rule,
// stated once there, and a store that also implemented it would make "this
// product has its own line" unaskable.
//
// ListSlotAccounts orders by slot, then product, then asset — a stated order
// rather than insertion, because the mapping is read as a configuration listing
// and nothing about when a row was written belongs in it.
type SlotTx interface {
	PutSlotAccount(ctx context.Context, book BookID, row SlotAccount) error
	GetSlotAccount(ctx context.Context, book BookID, product, slot string, asset AssetCode) (AccountID, error)
	ListSlotAccounts(ctx context.Context, book BookID) ([]SlotAccount, error)
}

// BankTx is the ledger as a BANK holds it: the ledger proper plus the slot
// mapping. It is what Book's slot methods take, since pointing a slot at an
// account reads that account, writes the mapping row and audits the change in
// one unit of work.
type BankTx interface {
	Tx
	SlotTx
}

// Tx is one unit of work over the ledger layer. deposit.Tx and payment.BankTx
// embed it, so a single concrete Tx spans all three layers and cross-layer
// operations commit or roll back together.
//
// It is the ledger a bank and the settlement agent BOTH hold. What only a bank
// has is beside it rather than in it: see SlotTx.
type Tx interface {
	CommonTx

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

	// ListTransactionsForPosition returns every transaction with a leg on the
	// position: on the account, or on one subsidiary within it. A transaction is
	// returned whole — all of its legs, not only the matching ones.
	ListTransactionsForPosition(ctx context.Context, book BookID, pos Position) ([]Transaction, error)

	// MarkReversed sets status to Reversed only if it is currently Posted, and
	// returns ErrTransactionAlreadyReversed otherwise. Conditional because a
	// read-compare-write would race once a mutex no longer covers it.
	MarkReversed(ctx context.Context, book BookID, id TransactionID) error

	// BookBalance aggregates entries rather than replaying them in Go. normal is
	// the account type's normal direction; entries in that direction add.
	//
	// A Position with an empty Subsidiary is the WHOLE account and not the
	// subsidiary named by the empty string: it aggregates every entry against the
	// account, which on a control account is the control figure. The two
	// readings cannot both apply, because PostTransactionTx leaves no account
	// holding qualified and unqualified entries at once — so a store adds the
	// subsidiary to the predicate only when it is non-empty, and Σ(detail) ==
	// control follows from the SQL rather than from a reconciliation.
	BookBalance(ctx context.Context, book BookID, pos Position, normal Direction) (Amount, error)

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
	ValueDateBalance(ctx context.Context, book BookID, pos Position, normal Direction, before time.Time) (Amount, error)

	// SubsidiaryBalances is the balance of every subsidiary under an account, in
	// one query rather than one BookBalance per subsidiary: the caller does not
	// know the subsidiaries before it asks, which is the whole difference between
	// this and reading a Position.
	//
	// Ordered by subsidiary, and subsidiaries netting to zero are omitted — a customer
	// who has repaid is not a row in what the bank owes. normal is the account
	// type's normal direction, as it is for BookBalance.
	SubsidiaryBalances(ctx context.Context, book BookID, account AccountID, normal Direction) ([]SubsidiaryBalance, error)

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
	ValueDatedSeries(ctx context.Context, book BookID, pos Position, normal Direction, from, to time.Time) (Series, error)
}
