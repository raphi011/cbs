// Package mem is the in-process implementation of ledger.Store and
// deposit.Store.
//
// It is the reference implementation: everything the system does works here
// first, with maps and a mutex, and store/pg then has to match it — a match the
// shared conformance suite in store/storetest enforces.
//
// # Books
//
// Every map is keyed by ledger.BookID at the outer level. Chart-of-accounts IDs
// (subledger blocks, account numbers) are unique within a book, not globally,
// so two banks in the same store can both hold account "200.100.001" without
// colliding. The ID counters are per book for the same reason; the audit
// sequence is not, because it is a total order over the whole store.
//
// # One transaction, several layers
//
// A single *tx implements ledger.Tx and deposit.Tx, so a unit of work spans both
// layers and a deposit capture commits its hold write together with its GL
// posting. Because Go cannot give one Store two Update methods with different
// callback types, the deposit-shaped view of the store is a thin adapter,
// (*Store).Deposit; both views share the same state and the same lock.
package mem

import (
	"context"
	"errors"
	"maps"
	"sync"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
)

// ErrReadOnly is returned when a write is attempted inside View. Update is the
// only unit of work that may mutate; View holds nothing but a read lock, so a
// write through it would be a data race rather than a rollback-safe change.
var ErrReadOnly = errors.New("mem: write attempted in a read-only transaction")

// ErrNestedTransaction is returned when a unit of work is opened inside another
// one on the same store.
//
// The store's mutex is not reentrant, so nesting would deadlock the process with
// no diagnostic at all. Failing loudly instead turns the single most likely
// mistake in this codebase — calling PostTransaction where PostTransactionTx was
// meant — into an error message naming the problem.
var ErrNestedTransaction = errors.New("mem: a unit of work is already open on this store (call the …Tx method, not its Update-wrapping sibling)")

// inUnitOfWork is the context key Update and View stamp into the context they
// pass to their callback. Its value is the *Store, so two independent stores can
// still be driven from one context — only re-entering the same store is refused.
type inUnitOfWork struct{}

// Store is an in-memory ledger.Store. The zero value is not usable; call New.
//
// One RWMutex guards everything. Update takes it exclusively and View takes it
// shared, which makes each unit of work atomic and isolated by construction.
// The mutex is not reentrant, so a unit of work must never open another one:
// inside Update, drive the …Tx methods rather than their Update-wrapping
// siblings.
type Store struct {
	mu    sync.RWMutex
	clock func() time.Time
	data  *state
}

// New creates an empty in-memory store reading time from clock.
func New(clock func() time.Time) *Store {
	return &Store{clock: clock, data: newState()}
}

// Update runs fn in one atomic unit of work under the write lock.
//
// There is no serialization failure to retry here — the write lock already
// serializes writers. Atomicity comes from a snapshot: the whole state is
// cloned before fn runs and restored if fn returns an error, so a failed
// operation leaves behind neither entities nor burned ID counters. store/pg
// gets the same guarantee from BEGIN … ROLLBACK.
func (s *Store) Update(ctx context.Context, fn func(context.Context, ledger.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkNotNested(ctx); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot := s.data.clone()
	if err := fn(s.mark(ctx), &tx{store: s, state: s.data}); err != nil {
		s.data = snapshot
		return err
	}
	return nil
}

// View runs fn in a read-only unit of work under the read lock. Writes through
// the Tx it provides fail with ErrReadOnly.
func (s *Store) View(ctx context.Context, fn func(context.Context, ledger.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkNotNested(ctx); err != nil {
		return err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	return fn(s.mark(ctx), &tx{store: s, state: s.data, readOnly: true})
}

// mark stamps the context handed to a callback so that a unit of work opened
// from inside it can be recognised.
func (s *Store) mark(ctx context.Context) context.Context {
	return context.WithValue(ctx, inUnitOfWork{}, s)
}

// checkNotNested refuses to open a unit of work inside another one on this
// store. A read lock taken while this goroutine already holds the write lock —
// or while another goroutine is waiting for it — deadlocks, and a deadlock with
// no error is far harder to diagnose than ErrNestedTransaction.
func (s *Store) checkNotNested(ctx context.Context) error {
	if ctx.Value(inUnitOfWork{}) == s {
		return ErrNestedTransaction
	}
	return nil
}

// Reset discards all state, including the ID counters and the audit log, so a
// store reset behaves exactly like a freshly constructed one.
func (s *Store) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.data = newState()
	return nil
}

// Close releases the store's resources. There are none to release in memory.
func (s *Store) Close() error { return nil }

// state is the whole contents of the store. It is only ever reachable while a
// caller holds s.mu, and is replaced wholesale by Reset and by an Update that
// rolls back.
type state struct {
	ledgers      map[ledger.BookID]map[ledger.LedgerID]ledger.Ledger
	subledgers   map[ledger.BookID]map[ledger.SubledgerID]ledger.Subledger
	accounts     map[ledger.BookID]map[ledger.AccountID]ledger.Account
	transactions map[ledger.BookID]map[ledger.TransactionID]ledger.Transaction

	// idempotency maps idempotency keys to transaction IDs, per book. This is
	// what lets the store detect and reject duplicate postings.
	idempotency map[ledger.BookID]map[string]ledger.TransactionID

	// audit is the append-only log, in Seq order because Seq is assigned on
	// append. Entries are never modified or removed once written.
	audit    []ledger.AuditEvent
	auditSeq int64

	// idCounter is the shared monotonic counter behind ldg_/tx_/ent_/evt_ IDs,
	// one per book.
	idCounter map[ledger.BookID]int64

	// subledgerSeq is the last chart-of-accounts block issued to a subledger
	// (100, 200, …) in each book.
	subledgerSeq map[ledger.BookID]int

	// accountSeq is the last account sequence issued within each
	// "<typeBlock>.<subledgerID>" branch of a book. Account numbers reset per
	// (type, subledger) — the type-first chart-of-accounts convention.
	accountSeq map[ledger.BookID]map[string]int

	// The deposit layer's own state, in the same store and under the same lock
	// as the ledger's, which is what lets one Tx write both atomically.
	depositAccounts map[ledger.BookID]map[deposit.AccountID]deposit.Account
	holds           map[ledger.BookID]map[deposit.HoldID]deposit.Hold

	// snapshots is keyed by (account, business date) rather than nested per
	// account, so an upsert is a single map assignment — the same identity
	// store/pg gets from a primary key on (book_id, account_id, date_key).
	snapshots map[ledger.BookID]map[snapshotKey]deposit.Snapshot
}

// snapshotKey identifies one end-of-day snapshot within a book.
type snapshotKey struct {
	account deposit.AccountID
	dateKey string
}

func newState() *state {
	return &state{
		ledgers:         make(map[ledger.BookID]map[ledger.LedgerID]ledger.Ledger),
		subledgers:      make(map[ledger.BookID]map[ledger.SubledgerID]ledger.Subledger),
		accounts:        make(map[ledger.BookID]map[ledger.AccountID]ledger.Account),
		transactions:    make(map[ledger.BookID]map[ledger.TransactionID]ledger.Transaction),
		idempotency:     make(map[ledger.BookID]map[string]ledger.TransactionID),
		idCounter:       make(map[ledger.BookID]int64),
		subledgerSeq:    make(map[ledger.BookID]int),
		accountSeq:      make(map[ledger.BookID]map[string]int),
		depositAccounts: make(map[ledger.BookID]map[deposit.AccountID]deposit.Account),
		holds:           make(map[ledger.BookID]map[deposit.HoldID]deposit.Hold),
		snapshots:       make(map[ledger.BookID]map[snapshotKey]deposit.Snapshot),
	}
}

// clone takes the rollback snapshot for Update.
//
// The copy is one level deep on purpose. Stored entities are values, and the
// store only ever replaces a map entry, never mutates one in place — Put
// deep-copies on the way in and Get deep-copies on the way out — so no cloned
// entry can be changed behind the snapshot's back. The audit log is likewise
// append-only: copying the slice header pins its length, and anything appended
// past that length is invisible to the snapshot.
func (s *state) clone() *state {
	return &state{
		ledgers:         cloneNested(s.ledgers),
		subledgers:      cloneNested(s.subledgers),
		accounts:        cloneNested(s.accounts),
		transactions:    cloneNested(s.transactions),
		idempotency:     cloneNested(s.idempotency),
		audit:           s.audit,
		auditSeq:        s.auditSeq,
		idCounter:       maps.Clone(s.idCounter),
		subledgerSeq:    maps.Clone(s.subledgerSeq),
		accountSeq:      cloneNested(s.accountSeq),
		depositAccounts: cloneNested(s.depositAccounts),
		holds:           cloneNested(s.holds),
		snapshots:       cloneNested(s.snapshots),
	}
}

func cloneNested[B, K comparable, V any](m map[B]map[K]V) map[B]map[K]V {
	out := make(map[B]map[K]V, len(m))
	for book, inner := range m {
		out[book] = maps.Clone(inner)
	}
	return out
}

// ---------------------------------------------------------------------------
// The deposit-shaped view of the same store
// ---------------------------------------------------------------------------

// Deposit returns this store as a deposit.Store.
//
// It is an adapter rather than a second implementation because Go allows one
// Update method per type, and ledger.Store and deposit.Store declare Update with
// different callback types. The adapter holds no state: it hands the callback
// the very same *tx, which implements both interfaces, so a Register and a Book
// built over one mem.Store share its lock, its rollback snapshot and its ID
// counters — and a deposit capture's hold write and GL posting land in one unit
// of work.
func (s *Store) Deposit() deposit.Store { return depositStore{s} }

// depositStore re-types Store's Update and View; Reset and Close are promoted
// unchanged from the embedded *Store.
type depositStore struct{ *Store }

// compile-time check that the adapter satisfies the interface it exists for.
var _ deposit.Store = depositStore{}

func (d depositStore) Update(ctx context.Context, fn func(context.Context, deposit.Tx) error) error {
	return d.Store.Update(ctx, func(ctx context.Context, t ledger.Tx) error {
		return fn(ctx, t.(deposit.Tx))
	})
}

func (d depositStore) View(ctx context.Context, fn func(context.Context, deposit.Tx) error) error {
	return d.Store.View(ctx, func(ctx context.Context, t ledger.Tx) error {
		return fn(ctx, t.(deposit.Tx))
	})
}

// bucket returns the per-book map for a book, creating it on first use.
func bucket[B, K comparable, V any](m map[B]map[K]V, book B) map[K]V {
	inner, ok := m[book]
	if !ok {
		inner = make(map[K]V)
		m[book] = inner
	}
	return inner
}
