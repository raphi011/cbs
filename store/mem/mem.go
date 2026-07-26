// Package mem is the in-process implementation of ledger.Store.
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
package mem

import (
	"context"
	"errors"
	"maps"
	"sync"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// ErrReadOnly is returned when a write is attempted inside View. Update is the
// only unit of work that may mutate; View holds nothing but a read lock, so a
// write through it would be a data race rather than a rollback-safe change.
var ErrReadOnly = errors.New("mem: write attempted in a read-only transaction")

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

	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot := s.data.clone()
	if err := fn(ctx, &tx{store: s, state: s.data}); err != nil {
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

	s.mu.RLock()
	defer s.mu.RUnlock()

	return fn(ctx, &tx{store: s, state: s.data, readOnly: true})
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
}

func newState() *state {
	return &state{
		ledgers:      make(map[ledger.BookID]map[ledger.LedgerID]ledger.Ledger),
		subledgers:   make(map[ledger.BookID]map[ledger.SubledgerID]ledger.Subledger),
		accounts:     make(map[ledger.BookID]map[ledger.AccountID]ledger.Account),
		transactions: make(map[ledger.BookID]map[ledger.TransactionID]ledger.Transaction),
		idempotency:  make(map[ledger.BookID]map[string]ledger.TransactionID),
		idCounter:    make(map[ledger.BookID]int64),
		subledgerSeq: make(map[ledger.BookID]int),
		accountSeq:   make(map[ledger.BookID]map[string]int),
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
		ledgers:      cloneNested(s.ledgers),
		subledgers:   cloneNested(s.subledgers),
		accounts:     cloneNested(s.accounts),
		transactions: cloneNested(s.transactions),
		idempotency:  cloneNested(s.idempotency),
		audit:        s.audit,
		auditSeq:     s.auditSeq,
		idCounter:    maps.Clone(s.idCounter),
		subledgerSeq: maps.Clone(s.subledgerSeq),
		accountSeq:   cloneNested(s.accountSeq),
	}
}

func cloneNested[B, K comparable, V any](m map[B]map[K]V) map[B]map[K]V {
	out := make(map[B]map[K]V, len(m))
	for book, inner := range m {
		out[book] = maps.Clone(inner)
	}
	return out
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
