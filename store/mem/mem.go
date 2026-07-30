// Package mem is the in-process implementation of ledger.Store, deposit.Store,
// payment.Store and lending.Store.
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
// A single *tx implements ledger.Tx, deposit.Tx, payment.Tx and lending.Tx, so a
// unit of work spans all four layers: a deposit capture commits its hold write
// together with its GL posting, a settlement commits postings across every
// participant's book, the central bank's, and its own record of the
// settlement, and a disbursement commits a facility write with its GL posting.
// Because Go cannot give one Store four Update methods with different callback
// types, the deposit-, payment- and lending-shaped views of the store are thin
// adapters, (*Store).Deposit, (*Store).Payment and (*Store).Lending; all four
// views share the same state and the same lock.
package mem

import (
	"context"
	"errors"
	"maps"
	"sync"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
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

	// overdraftTerms is the effective-dated terms timeline, keyed by (account,
	// effective day) rather than nested per account, so an upsert is a single
	// map assignment — the same shape snapshots has, for the same reason.
	overdraftTerms map[ledger.BookID]map[termsKey]deposit.OverdraftTerms

	// The payment layer's state. These maps are NOT nested per book: the
	// entities are network-scoped — a payment belongs to no single bank — so
	// they are sequenced under ledger.NetworkBook and keyed by their ID alone.
	participants map[payment.ParticipantID]payment.Participant
	payments     map[payment.PaymentID]payment.Payment
	mandates     map[payment.MandateID]payment.Mandate
	cycles       map[payment.CycleID]payment.ClearingCycle
	settlements  map[payment.SettlementID]payment.Settlement

	// endToEnd maps end-to-end ids to payment IDs, the payment layer's
	// equivalent of the ledger's idempotency index. It is what lets the store
	// reject a duplicate client reference.
	endToEnd map[string]payment.PaymentID

	// The lending layer's own state, in the same store and under the same lock
	// as the ledger's, which is what lets one Tx write both atomically — a
	// disbursement's facility write and its GL posting commit or roll back
	// together.
	facilities map[ledger.BookID]map[lending.FacilityID]lending.Facility
	// installments is keyed by (facility, seq) rather than nested per facility,
	// so an upsert is a single map assignment — the same identity store/pg gets
	// from a primary key on (book_id, facility_id, seq).
	installments map[ledger.BookID]map[installmentKey]lending.Installment

	// facilityTerms is the effective-dated terms timeline, keyed by (facility,
	// effective day) rather than nested per facility, so an upsert is a single
	// map assignment — the same shape overdraftTerms has, for the same reason.
	facilityTerms map[ledger.BookID]map[facilityTermsKey]lending.FacilityTerms

	// rowSeq is the insertion order of every book-scoped row, and the tie-break
	// every List* uses when two rows share a CreatedAt.
	//
	// Ordering must never fall back to the ID: IDs are counter-derived strings,
	// so "dep_10" sorts before "dep_8" and a chart of accounts or a customer
	// list silently reorders itself the moment a counter crosses a power of ten.
	// A monotonic sequence has no such cliff. It is the same mechanism the audit
	// log already uses for AuditEvent.Seq, so the two agree rather than each
	// inventing an ordering rule.
	//
	// Assigned on first insert only: an upsert (a status change, say) keeps the
	// row where it was. store/pg gets this from a BIGSERIAL column per table.
	rowSeq map[rowKey]int64

	// rowSeqCounter is the last sequence issued, one counter per (book, kind) —
	// mirroring pg, where each table has its own BIGSERIAL.
	rowSeqCounter map[rowCounterKey]int64
}

// snapshotKey identifies one end-of-day snapshot within a book.
type snapshotKey struct {
	account deposit.AccountID
	dateKey string
}

// termsKey identifies one overdraft terms row within a book: the account and
// the effective DAY, which is the same identity store/pg gets from a primary
// key on (book_id, account_id, day_key).
type termsKey struct {
	account deposit.AccountID
	dayKey  string
}

// installmentKey is an instalment's composite identity: its facility and its
// position in that facility's schedule.
type installmentKey struct {
	facility lending.FacilityID
	seq      int
}

// facilityTermsKey identifies one facility terms row within a book: the
// facility and the effective DAY, which is the same identity store/pg gets
// from a primary key on (book_id, facility_id, day_key).
type facilityTermsKey struct {
	facility lending.FacilityID
	dayKey   string
}

// rowKind names the table a row belongs to, so sequences are per table.
type rowKind string

const (
	kindLedger         rowKind = "ledger"
	kindSubledger      rowKind = "subledger"
	kindAccount        rowKind = "account"
	kindTransaction    rowKind = "transaction"
	kindDepositAccount rowKind = "deposit_account"
	kindHold           rowKind = "hold"
	kindSnapshot       rowKind = "snapshot"
	kindOverdraftTerms rowKind = "overdraft_terms"
	kindParticipant    rowKind = "participant"
	kindPayment        rowKind = "payment"
	kindMandate        rowKind = "mandate"
	kindCycle          rowKind = "cycle"
	kindSettlement     rowKind = "settlement"
	kindFacility       rowKind = "facility"
	kindInstallment    rowKind = "installment"
	kindFacilityTerms  rowKind = "facility_terms"
)

// rowKey identifies one row for sequence purposes: its book, its table and its
// primary key rendered as a string.
type rowKey struct {
	book ledger.BookID
	kind rowKind
	id   string
}

type rowCounterKey struct {
	book ledger.BookID
	kind rowKind
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
		overdraftTerms:  make(map[ledger.BookID]map[termsKey]deposit.OverdraftTerms),
		participants:    make(map[payment.ParticipantID]payment.Participant),
		payments:        make(map[payment.PaymentID]payment.Payment),
		mandates:        make(map[payment.MandateID]payment.Mandate),
		cycles:          make(map[payment.CycleID]payment.ClearingCycle),
		settlements:     make(map[payment.SettlementID]payment.Settlement),
		endToEnd:        make(map[string]payment.PaymentID),
		facilities:      make(map[ledger.BookID]map[lending.FacilityID]lending.Facility),
		installments:    make(map[ledger.BookID]map[installmentKey]lending.Installment),
		facilityTerms:   make(map[ledger.BookID]map[facilityTermsKey]lending.FacilityTerms),
		rowSeq:          make(map[rowKey]int64),
		rowSeqCounter:   make(map[rowCounterKey]int64),
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
		overdraftTerms:  cloneNested(s.overdraftTerms),
		participants:    maps.Clone(s.participants),
		payments:        maps.Clone(s.payments),
		mandates:        maps.Clone(s.mandates),
		cycles:          maps.Clone(s.cycles),
		settlements:     maps.Clone(s.settlements),
		endToEnd:        maps.Clone(s.endToEnd),
		facilities:      cloneNested(s.facilities),
		installments:    cloneNested(s.installments),
		facilityTerms:   cloneNested(s.facilityTerms),
		rowSeq:          maps.Clone(s.rowSeq),
		rowSeqCounter:   maps.Clone(s.rowSeqCounter),
	}
}

// insertSeq records a row's position in its table's insertion order and returns
// it. Calling it again for the same row is a no-op: an upsert keeps the position
// the row was first given, so editing a customer's status does not move them to
// the bottom of the list.
func (s *state) insertSeq(book ledger.BookID, kind rowKind, id string) int64 {
	k := rowKey{book: book, kind: kind, id: id}
	if seq, ok := s.rowSeq[k]; ok {
		return seq
	}
	counter := rowCounterKey{book: book, kind: kind}
	s.rowSeqCounter[counter]++
	s.rowSeq[k] = s.rowSeqCounter[counter]
	return s.rowSeq[k]
}

// seqOf returns a row's insertion sequence, or 0 if it has none.
func (s *state) seqOf(book ledger.BookID, kind rowKind, id string) int64 {
	return s.rowSeq[rowKey{book: book, kind: kind, id: id}]
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

// ---------------------------------------------------------------------------
// The payment-shaped view of the same store
// ---------------------------------------------------------------------------

// Payment returns this store as a payment.Store.
//
// It is the store handle a payment.Network takes, and the only one it needs:
// the Network derives its own ledger.Store and deposit.Store views from it, so
// there is no way to hand it two stores that disagree. Like Deposit, this is an
// adapter over the same state, the same lock and the same *tx.
func (s *Store) Payment() payment.Store { return paymentStore{s} }

// paymentStore re-types Store's Update and View; Reset and Close are promoted
// unchanged from the embedded *Store.
type paymentStore struct{ *Store }

// compile-time check that the adapter satisfies the interface it exists for.
var _ payment.Store = paymentStore{}

func (p paymentStore) Update(ctx context.Context, fn func(context.Context, payment.Tx) error) error {
	return p.Store.Update(ctx, func(ctx context.Context, t ledger.Tx) error {
		return fn(ctx, t.(payment.Tx))
	})
}

func (p paymentStore) View(ctx context.Context, fn func(context.Context, payment.Tx) error) error {
	return p.Store.View(ctx, func(ctx context.Context, t ledger.Tx) error {
		return fn(ctx, t.(payment.Tx))
	})
}

// ---------------------------------------------------------------------------
// The lending-shaped view of the same store
// ---------------------------------------------------------------------------

// Lending returns this store as a lending.Store.
//
// It is an adapter over the same state, the same lock and the same *tx as
// Deposit and Payment, for the same reason: Go allows one Update method per
// type, and lending.Store declares Update with its own callback type. Sharing
// the *tx is what lets a disbursement's facility write and its GL posting land
// in one unit of work.
func (s *Store) Lending() lending.Store { return lendingStore{s} }

// lendingStore re-types Store's Update and View; Reset and Close are promoted
// unchanged from the embedded *Store.
type lendingStore struct{ *Store }

// compile-time check that the adapter satisfies the interface it exists for.
var _ lending.Store = lendingStore{}

func (l lendingStore) Update(ctx context.Context, fn func(context.Context, lending.Tx) error) error {
	return l.Store.Update(ctx, func(ctx context.Context, t ledger.Tx) error {
		return fn(ctx, t.(lending.Tx))
	})
}

func (l lendingStore) View(ctx context.Context, fn func(context.Context, lending.Tx) error) error {
	return l.Store.View(ctx, func(ctx context.Context, t ledger.Tx) error {
		return fn(ctx, t.(lending.Tx))
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
