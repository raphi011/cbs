package mem

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// tx is one unit of work over the in-memory state. It holds no buffered writes:
// the Store's lock is already held for its whole lifetime, so it writes
// straight through, and Update handles rollback by restoring its snapshot.
type tx struct {
	store    *Store
	state    *state
	readOnly bool
}

// compile-time check that tx satisfies the interface it is written against.
var _ ledger.Tx = (*tx)(nil)

// write reports whether this transaction may mutate.
func (t *tx) write() error {
	if t.readOnly {
		return ErrReadOnly
	}
	return nil
}

// Now returns the store's current time.
func (t *tx) Now() time.Time { return t.store.clock() }

// ---------------------------------------------------------------------------
// Identity allocation
// ---------------------------------------------------------------------------

// NextID issues the next ID with the given prefix. One counter is shared by all
// prefixes within a book, so ldg_1, tx_2 and ent_3 can never collide and the
// sequence doubles as a creation order.
func (t *tx) NextID(ctx context.Context, book ledger.BookID, prefix string) (string, error) {
	if err := t.write(); err != nil {
		return "", err
	}
	t.state.idCounter[book]++
	return fmt.Sprintf("%s_%d", prefix, t.state.idCounter[book]), nil
}

// NextSubledgerBlock issues the next chart-of-accounts block for a book: 100,
// 200, 300, … Blocks are per book, so every book's first subledger is "100".
func (t *tx) NextSubledgerBlock(ctx context.Context, book ledger.BookID) (int, error) {
	if err := t.write(); err != nil {
		return 0, err
	}
	t.state.subledgerSeq[book] += 100
	return t.state.subledgerSeq[book], nil
}

// NextAccountSeq issues the next account number within one
// "<typeBlock>.<subledgerID>" branch of a book. Numbering restarts at 1 for
// each (type, subledger) pair.
func (t *tx) NextAccountSeq(ctx context.Context, book ledger.BookID, typeBlock int, subledger ledger.SubledgerID) (int, error) {
	if err := t.write(); err != nil {
		return 0, err
	}
	branch := bucket(t.state.accountSeq, book)
	key := fmt.Sprintf("%d.%s", typeBlock, subledger)
	branch[key]++
	return branch[key], nil
}

// ---------------------------------------------------------------------------
// Ledgers
// ---------------------------------------------------------------------------

func (t *tx) PutLedger(ctx context.Context, book ledger.BookID, l ledger.Ledger) error {
	if err := t.write(); err != nil {
		return err
	}
	t.state.insertSeq(book, kindLedger, string(l.ID))
	bucket(t.state.ledgers, book)[l.ID] = l
	return nil
}

func (t *tx) GetLedger(ctx context.Context, book ledger.BookID, id ledger.LedgerID) (ledger.Ledger, error) {
	l, ok := t.state.ledgers[book][id]
	if !ok {
		return ledger.Ledger{}, ledger.ErrLedgerNotFound
	}
	return l, nil
}

func (t *tx) ListLedgers(ctx context.Context, book ledger.BookID) ([]ledger.Ledger, error) {
	out := make([]ledger.Ledger, 0, len(t.state.ledgers[book]))
	for _, l := range t.state.ledgers[book] {
		out = append(out, l)
	}
	sortRows(t.state, out, book, kindLedger, func(l ledger.Ledger) (time.Time, string) { return l.CreatedAt, string(l.ID) })
	return out, nil
}

// ---------------------------------------------------------------------------
// Subledgers
// ---------------------------------------------------------------------------

func (t *tx) PutSubledger(ctx context.Context, book ledger.BookID, sl ledger.Subledger) error {
	if err := t.write(); err != nil {
		return err
	}
	t.state.insertSeq(book, kindSubledger, string(sl.ID))
	bucket(t.state.subledgers, book)[sl.ID] = sl
	return nil
}

func (t *tx) GetSubledger(ctx context.Context, book ledger.BookID, id ledger.SubledgerID) (ledger.Subledger, error) {
	sl, ok := t.state.subledgers[book][id]
	if !ok {
		return ledger.Subledger{}, ledger.ErrSubledgerNotFound
	}
	return sl, nil
}

func (t *tx) ListSubledgers(ctx context.Context, book ledger.BookID) ([]ledger.Subledger, error) {
	out := make([]ledger.Subledger, 0, len(t.state.subledgers[book]))
	for _, sl := range t.state.subledgers[book] {
		out = append(out, sl)
	}
	sortRows(t.state, out, book, kindSubledger, func(sl ledger.Subledger) (time.Time, string) { return sl.CreatedAt, string(sl.ID) })
	return out, nil
}

// ---------------------------------------------------------------------------
// Accounts
// ---------------------------------------------------------------------------

func (t *tx) PutAccount(ctx context.Context, book ledger.BookID, a ledger.Account) error {
	if err := t.write(); err != nil {
		return err
	}
	t.state.insertSeq(book, kindAccount, string(a.ID))
	bucket(t.state.accounts, book)[a.ID] = a
	return nil
}

func (t *tx) GetAccount(ctx context.Context, book ledger.BookID, id ledger.AccountID) (ledger.Account, error) {
	a, ok := t.state.accounts[book][id]
	if !ok {
		return ledger.Account{}, ledger.ErrAccountNotFound
	}
	return a, nil
}

func (t *tx) ListAccounts(ctx context.Context, book ledger.BookID) ([]ledger.Account, error) {
	out := make([]ledger.Account, 0, len(t.state.accounts[book]))
	for _, a := range t.state.accounts[book] {
		out = append(out, a)
	}
	sortRows(t.state, out, book, kindAccount, func(a ledger.Account) (time.Time, string) { return a.CreatedAt, string(a.ID) })
	return out, nil
}

// LockAccounts is a no-op in memory: Update already holds the store's write
// lock for the whole unit of work, so the accounts are exclusively held from
// the moment the transaction opened — there is nothing finer to lock. store/pg
// needs the real thing (SELECT … FOR UPDATE ordered by id) because its
// transactions run concurrently.
func (t *tx) LockAccounts(ctx context.Context, book ledger.BookID, ids []ledger.AccountID) error {
	return nil
}

// ---------------------------------------------------------------------------
// Transactions
// ---------------------------------------------------------------------------

// PutTransaction stores a transaction and claims its idempotency key.
//
// An empty key is not an identity, so empty keys are never indexed and never
// deduplicated. A non-empty key already claimed by a different transaction
// fails with ledger.ErrDuplicateIdempotencyKey: the check lives here, under the
// same lock as the write, so it cannot be lost to a race the way a
// read-then-write in the caller could be.
func (t *tx) PutTransaction(ctx context.Context, book ledger.BookID, txn ledger.Transaction) error {
	if err := t.write(); err != nil {
		return err
	}
	if txn.IdempotencyKey != "" {
		if existing, ok := t.state.idempotency[book][txn.IdempotencyKey]; ok && existing != txn.ID {
			return ledger.ErrDuplicateIdempotencyKey
		}
	}
	t.state.insertSeq(book, kindTransaction, string(txn.ID))
	bucket(t.state.transactions, book)[txn.ID] = copyTransaction(txn)
	if txn.IdempotencyKey != "" {
		bucket(t.state.idempotency, book)[txn.IdempotencyKey] = txn.ID
	}
	return nil
}

func (t *tx) GetTransaction(ctx context.Context, book ledger.BookID, id ledger.TransactionID) (ledger.Transaction, error) {
	txn, ok := t.state.transactions[book][id]
	if !ok {
		return ledger.Transaction{}, ledger.ErrTransactionNotFound
	}
	return copyTransaction(txn), nil
}

func (t *tx) GetTransactionByIdempotencyKey(ctx context.Context, book ledger.BookID, key string) (ledger.Transaction, error) {
	if key == "" {
		return ledger.Transaction{}, ledger.ErrTransactionNotFound
	}
	id, ok := t.state.idempotency[book][key]
	if !ok {
		return ledger.Transaction{}, ledger.ErrTransactionNotFound
	}
	return t.GetTransaction(ctx, book, id)
}

func (t *tx) ListTransactions(ctx context.Context, book ledger.BookID) ([]ledger.Transaction, error) {
	out := make([]ledger.Transaction, 0, len(t.state.transactions[book]))
	for _, txn := range t.state.transactions[book] {
		out = append(out, copyTransaction(txn))
	}
	sortRows(t.state, out, book, kindTransaction, func(txn ledger.Transaction) (time.Time, string) { return txn.CreatedAt, string(txn.ID) })
	return out, nil
}

func (t *tx) ListTransactionsForAccount(ctx context.Context, book ledger.BookID, id ledger.AccountID) ([]ledger.Transaction, error) {
	out := make([]ledger.Transaction, 0)
	for _, txn := range t.state.transactions[book] {
		for _, e := range txn.Entries {
			if e.AccountID == id {
				out = append(out, copyTransaction(txn))
				break
			}
		}
	}
	sortRows(t.state, out, book, kindTransaction, func(txn ledger.Transaction) (time.Time, string) { return txn.CreatedAt, string(txn.ID) })
	return out, nil
}

// MarkReversed flips a transaction from Posted to Reversed, or fails.
//
// It is conditional rather than a read-compare-write in the caller so that two
// concurrent reversals of the same transaction cannot both succeed once a
// single process-wide mutex no longer covers the whole operation.
func (t *tx) MarkReversed(ctx context.Context, book ledger.BookID, id ledger.TransactionID) error {
	if err := t.write(); err != nil {
		return err
	}
	txn, ok := t.state.transactions[book][id]
	if !ok {
		return ledger.ErrTransactionNotFound
	}
	if txn.Status != ledger.Posted {
		return ledger.ErrTransactionAlreadyReversed
	}
	txn.Status = ledger.Reversed
	t.state.transactions[book][id] = txn
	return nil
}

// BookBalance aggregates an account's entries: those in the account's normal
// direction add, the others subtract.
//
// ALL transactions are included, including those marked Reversed. The Reversed
// status is informational — a reversal posts its own mirrored entries, and it
// is those that cancel the original's balance impact. Excluding reversed
// transactions here would double-count the correction and lose the audit trail.
// An account with no entries, including one that does not exist, is zero;
// callers that need ErrAccountNotFound read the account first.
func (t *tx) BookBalance(ctx context.Context, book ledger.BookID, id ledger.AccountID, normal ledger.Direction) (ledger.Amount, error) {
	var balance ledger.Amount
	for _, txn := range t.state.transactions[book] {
		for _, e := range txn.Entries {
			if e.AccountID != id {
				continue
			}
			if e.Direction == normal {
				balance += e.Amount
			} else {
				balance -= e.Amount
			}
		}
	}
	return balance, nil
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// AppendAudit appends an event, assigning its Seq. Any Seq the caller set is
// overwritten: the sequence is a total order over the store, so only the store
// can issue it.
func (t *tx) AppendAudit(ctx context.Context, e ledger.AuditEvent) error {
	if err := t.write(); err != nil {
		return err
	}
	t.state.auditSeq++
	e.Seq = t.state.auditSeq
	t.state.audit = append(t.state.audit, copyAudit(e))
	return nil
}

// ListAudit returns the events matching f in ascending Seq order. See
// ledger.AuditFilter for how Before and Limit page backwards.
func (t *tx) ListAudit(ctx context.Context, f ledger.AuditFilter) ([]ledger.AuditEvent, error) {
	out := make([]ledger.AuditEvent, 0)
	// t.state.audit is in append order, which is Seq order.
	for _, e := range t.state.audit {
		if f.BookID != "" && e.BookID != f.BookID {
			continue
		}
		if f.Scope != "" && e.Scope != f.Scope {
			continue
		}
		if f.Type != "" && e.Type != f.Type {
			continue
		}
		if f.EntityID != "" && e.EntityID != f.EntityID {
			continue
		}
		if f.Before > 0 && e.Seq >= f.Before {
			continue
		}
		out = append(out, copyAudit(e))
	}
	// Keep the newest matches, still oldest-first: Before names the page's
	// exclusive upper bound and Limit its size.
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[len(out)-f.Limit:]
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Copying and ordering
// ---------------------------------------------------------------------------

// copyTransaction returns a deep copy of a Transaction, including its Entries
// slice and Metadata map. Transactions cross the store boundary in both
// directions, and neither side may end up sharing mutable structure with the
// other.
func copyTransaction(txn ledger.Transaction) ledger.Transaction {
	cp := txn
	cp.Entries = make([]ledger.Entry, len(txn.Entries))
	copy(cp.Entries, txn.Entries)
	if txn.Metadata != nil {
		cp.Metadata = make(map[string]string, len(txn.Metadata))
		for k, v := range txn.Metadata {
			cp.Metadata[k] = v
		}
	}
	return cp
}

// copyAudit returns a deep copy of an AuditEvent. The log is immutable, so the
// raw payload in particular must never be shared with a caller.
func copyAudit(e ledger.AuditEvent) ledger.AuditEvent {
	cp := e
	if e.Payload != nil {
		cp.Payload = make([]byte, len(e.Payload))
		copy(cp.Payload, e.Payload)
	}
	if e.Metadata != nil {
		cp.Metadata = make(map[string]string, len(e.Metadata))
		for k, v := range e.Metadata {
			cp.Metadata[k] = v
		}
	}
	return cp
}

// sortRows orders a listing by creation time, breaking ties by the row's
// insertion sequence, so that the unordered maps produce a stable order across
// calls — and the same order store/pg produces with ORDER BY created_at, seq.
//
// The tie-break is deliberately NOT the ID. IDs are counter-derived strings, so
// "tx_10" sorts before "tx_8" and a listing silently reorders itself the moment
// a counter crosses a power of ten; the sequence is monotonic and has no such
// cliff. See state.rowSeq.
//
// It takes the state explicitly because Go methods cannot have type parameters,
// and looking a sequence up needs the state.
func sortRows[T any](st *state, items []T, book ledger.BookID, kind rowKind, key func(T) (time.Time, string)) {
	sort.Slice(items, func(i, j int) bool {
		ti, idi := key(items[i])
		tj, idj := key(items[j])
		if ti.Equal(tj) {
			return st.seqOf(book, kind, idi) < st.seqOf(book, kind, idj)
		}
		return ti.Before(tj)
	})
}
