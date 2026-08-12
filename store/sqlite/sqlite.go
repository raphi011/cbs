// Package sqlite is the SQLite implementation of every store the domain
// declares — ledger's, deposit's, product's, lending's, ebics's, and one per
// institution at the payment layer — on modernc.org/sqlite, real SQLite
// transpiled to Go, so the module gains Go dependencies and loses every
// external one.
package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	sqlite3 "modernc.org/sqlite"
	sqlite3lib "modernc.org/sqlite/lib"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
)

// ---------------------------------------------------------------------------
// Shapes
// ---------------------------------------------------------------------------

// Shape is which of the three schemas a store carries.
type Shape struct {
	// dir is the directory under schema/ holding this shape's migrations.
	dir string

	// holds is every table this shape's schema creates. It is what Reset empties,
	// and it is written out rather than read from sqlite_master so that a table
	// added to a schema and forgotten here is not possible.
	holds map[string]struct{}

	// paymentLegs and paymentCycle are the two places where two shapes hold the
	// SAME table with different columns, and they are the only such places.
	paymentLegs  bool
	paymentCycle bool
}

// String names the shape, for the migration errors.
func (s Shape) String() string { return s.dir }

// The three shapes. See Shape.
var (
	// Bank is one member bank: a ledger, a deposit register, products, lending,
	// its own single row in banks, its copy of the scheme's routing directory, the
	// mandates it holds as creditor bank, its own copy of each payment it is a
	// party to, and the advices it was sent.
	Bank = shape("bank",
		"books", "ledgers", "subledgers", "accounts", "slot_accounts", "transactions", "entries",
		"deposit_accounts", "deposit_account_identifiers", "holds", "snapshots", "overdraft_terms",
		"products", "product_versions",
		"facilities", "installments", "facility_terms",
		"banks", "bank_assets", "routing_directory", "mandates", "payments", "settlement_advices",
		"audit_events", "id_sequences").withPaymentLegs()

	// CSM is the clearing house: a roster, cycles, its own copy of each payment
	// it carries, the files and returns it has taken in and not yet handed over,
	// the transport every member dials, and no book of accounts of any kind.
	CSM = shape("csm",
		"roster_entries", "roster_entry_assets",
		"payments", "cycles", "cycle_payments",
		"held_files", "held_file_transactions", "held_returns",
		"ebics_queue", "ebics_orders",
		"audit_events", "id_sequences").withPaymentCycle()

	// CentralBank is the settlement agent: a ledger holding the members' reserve
	// accounts, its own member register, the register of bank codes it allocates
	// as four national registries in one, the settlements it discharged, the
	// transport the clearing house and every member dial, and no customers and no
	// payments.
	CentralBank = shape("centralbank",
		"books", "ledgers", "subledgers", "accounts", "transactions", "entries",
		"bank_codes", "settlement_members", "settlement_member_accounts",
		"settlements", "settlement_positions",
		"ebics_queue", "ebics_orders",
		"audit_events", "id_sequences")
)

func shape(dir string, tables ...string) Shape {
	holds := make(map[string]struct{}, len(tables))
	for _, t := range tables {
		holds[t] = struct{}{}
	}
	return Shape{dir: dir, holds: holds}
}

// withPaymentLegs and withPaymentCycle mark the two columns sets that are one
// shape's and not another's. See Shape.
func (s Shape) withPaymentLegs() Shape  { s.paymentLegs = true; return s }
func (s Shape) withPaymentCycle() Shape { s.paymentCycle = true; return s }

// ErrNotThisStoresBook is returned when a method is handed a BookID this store
// does not answer for.
var ErrNotThisStoresBook = errors.New("sqlite: this store does not answer for that book")

// ErrReadOnly is returned when a write is attempted inside View.
var ErrReadOnly = errors.New("sqlite: write attempted in a read-only transaction")

// ErrNestedTransaction is returned when a unit of work is opened inside another
// one on the same store.
var ErrNestedTransaction = errors.New("sqlite: a unit of work is already open on this store (call the …Tx method, not its Update-wrapping sibling)")

// inUnitOfWork is the context key Update and View stamp into the context they
// hand their callback. Its value is the *Store, so two independent stores can
// still be driven from one context — only re-entering the same store is refused.
type inUnitOfWork struct{}

// maxAttempts and backoffBase bound Update's retry loop, and between them they
// set the RETRY BUDGET: the longest a losing unit of work will keep trying
// before it gives up and returns the lock error to the caller.
const (
	maxAttempts = 10
	backoffBase = 2 * time.Millisecond
)

// maxOpenConns is how many connections a CALLER may hold at once.
const maxOpenConns = 8

// store is the ONE implementation. There is a type per institution over it —
// BankStore, ClearingHouseStore, CentralBankStore — and each is a set of
// re-typed units of work over this, not a second body of SQL.
type store struct {
	db    *sql.DB
	clock func() time.Time

	// shape is which schema is underneath: the migrations that went in, the
	// tables Reset empties, and the two payment column lists. See Shape.
	shape Shape

	// book is the ONE BookID this store answers for, and every method taking one
	// refuses any other. See ErrNotThisStoresBook.
	book ledger.BookID

	// keep is one connection held for an ephemeral store's lifetime.
	keep *sql.Conn
}

// The three ways to open a database, one per institution.

func OpenBank(ctx context.Context, book ledger.BookID, path string, clock func() time.Time) (*BankStore, error) {
	if book == "" {
		return nil, fmt.Errorf("sqlite: open bank: no book; every store answers for exactly one and refuses the rest")
	}
	s, err := open(ctx, Bank, book, path, clock)
	if err != nil {
		return nil, err
	}
	return &BankStore{s}, nil
}

func OpenClearingHouse(ctx context.Context, path string, clock func() time.Time) (*ClearingHouseStore, error) {
	s, err := open(ctx, CSM, payment.ClearingHouseBook, path, clock)
	if err != nil {
		return nil, err
	}
	return &ClearingHouseStore{s}, nil
}

func OpenCentralBank(ctx context.Context, path string, clock func() time.Time) (*CentralBankStore, error) {
	s, err := open(ctx, CentralBank, payment.CentralBankBook, path, clock)
	if err != nil {
		return nil, err
	}
	return &CentralBankStore{s}, nil
}

// open is the body the three constructors share: the pool, the pragmas and the
// migrations for one shape.
func open(ctx context.Context, shape Shape, book ledger.BookID, path string, clock func() time.Time) (*store, error) {
	dsn, ephemeral := dsn(path)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	limit := maxOpenConns
	if ephemeral {
		limit++ // the retained connection, which never returns to the pool
	}
	db.SetMaxOpenConns(limit)
	db.SetMaxIdleConns(limit)
	// Never retire a connection: reopening one costs a fresh set of pragmas for
	// nothing, and retiring the last one destroys an ephemeral database.
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)

	s := &store{db: db, clock: clock, shape: shape, book: book}

	if ephemeral {
		conn, err := db.Conn(ctx)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("sqlite: retain connection: %w", err)
		}
		s.keep = conn
	}

	if err := db.PingContext(ctx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("sqlite: ping: %w", err)
	}
	if err := migrate(ctx, db, shape); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// Book is the one BookID this store answers for.
func (s *store) Book() ledger.BookID { return s.book }

// dsn builds the connection string, and every setting the store depends on is in
// it rather than issued afterwards. It reports whether the database is
// ephemeral.
func dsn(path string) (string, bool) {
	pragmas := []string{
		// SQLite ignores REFERENCES without this, per connection, silently.
		"_pragma=foreign_keys(1)",
		// Wait rather than fail when another connection holds a lock it will
		// release. It does NOT cover a transaction that holds a read and needs to
		// upgrade — see isTransient.
		"_pragma=busy_timeout(5000)",
	}

	if path == "" {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic("sqlite: " + err.Error())
		}
		// The leading slash is what makes the name shared between this pool's
		// connections rather than private to each; without it every connection gets a
		// database of its own, which is the ":memory:" failure again under another
		// name.
		name := "/cbs_" + hex.EncodeToString(b[:])
		return "file:" + name + "?vfs=memdb&" + strings.Join(pragmas, "&"), true
	}

	// Readers run while a writer holds the lock. Only a file can be WAL: an
	// in-memory database's journal_mode is pinned to MEMORY or OFF and a request
	// for WAL is ignored without an error.
	pragmas = append(pragmas, "_pragma=journal_mode(WAL)")
	return "file:" + url.PathEscape(path) + "?" + strings.Join(pragmas, "&"), false
}

// update and view are the shared bodies every Update and View in this package
// delegates to.
func (s *store) update(ctx context.Context, fn func(context.Context, *tx) error) error {
	return s.inUpdate(ctx, func(ctx context.Context, dbtx *sql.Tx) error {
		return fn(ctx, s.newTx(dbtx, false))
	})
}

func (s *store) view(ctx context.Context, fn func(context.Context, *tx) error) error {
	return s.inView(ctx, func(ctx context.Context, dbtx *sql.Tx) error {
		return fn(ctx, s.newTx(dbtx, true))
	})
}

// newTx wraps one database transaction as the value that implements all five Tx
// interfaces.
func (s *store) newTx(dbtx *sql.Tx, readOnly bool) *tx {
	return &tx{store: s, tx: dbtx, readOnly: readOnly, books: make(map[ledger.BookID]struct{})}
}

// inUpdate runs fn in one atomic unit of work over the raw *sql.Tx, retrying on
// the transient failures isTransient names.
func (s *store) inUpdate(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkNotNested(ctx); err != nil {
		return err
	}

	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		err = s.runInTx(ctx, false, fn)
		if err == nil || !isTransient(err) {
			return err
		}
		if err := backoff(ctx, attempt); err != nil {
			return err
		}
	}
	return err
}

// backoff waits before the next attempt, and it is the difference between a
// retry loop that works and one that does not.
func backoff(ctx context.Context, attempt int) error {
	window := backoffBase << (attempt - 1)
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("sqlite: " + err.Error())
	}
	wait := time.Duration(binary.BigEndian.Uint64(b[:]) % uint64(window))

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// inView runs fn in a read-only unit of work.
func (s *store) inView(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkNotNested(ctx); err != nil {
		return err
	}
	return s.runInTx(ctx, true, fn)
}

// runInTx is one attempt: BEGIN, run fn, COMMIT or ROLLBACK.
func (s *store) runInTx(ctx context.Context, readOnly bool, fn func(context.Context, *sql.Tx) error) error {
	dbtx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: readOnly})
	if err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}

	if err := fn(s.mark(ctx), dbtx); err != nil {
		// Roll back on the caller's behalf. The rollback error is deliberately
		// dropped: fn's error is why the unit of work failed.
		_ = dbtx.Rollback()
		return err
	}
	if err := dbtx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit: %w", err)
	}
	return nil
}

// mark stamps the context handed to a callback so a unit of work opened from
// inside it can be recognised.
func (s *store) mark(ctx context.Context) context.Context {
	return context.WithValue(ctx, inUnitOfWork{}, s)
}

// checkNotNested refuses to open a unit of work inside another one on this
// store. See ErrNestedTransaction.
func (s *store) checkNotNested(ctx context.Context) error {
	if ctx.Value(inUnitOfWork{}) == s {
		return ErrNestedTransaction
	}
	return nil
}

// Close releases the retained connection and the pool. An ephemeral database
// goes with them, which is the point: a closed store's rows are not something a
// later store should find.
func (s *store) Close() error {
	if s.keep != nil {
		_ = s.keep.Close()
		s.keep = nil
	}
	var err error
	if s.db != nil {
		err = s.db.Close()
		s.db = nil
	}
	return err
}

// ---------------------------------------------------------------------------
// Transient failures
// ---------------------------------------------------------------------------

// isTransient reports whether an error is one a retry can plausibly fix.
func isTransient(err error) bool {
	var serr *sqlite3.Error
	if !errors.As(err, &serr) {
		return false
	}
	switch serr.Code() & 0xFF {
	case sqlite3lib.SQLITE_BUSY, sqlite3lib.SQLITE_LOCKED:
		return true
	default:
		return false
	}
}

// isUniqueViolation reports whether err is a conflict on a unique INDEX.
func isUniqueViolation(err error) bool {
	var serr *sqlite3.Error
	if !errors.As(err, &serr) {
		return false
	}
	return serr.Code() == sqlite3lib.SQLITE_CONSTRAINT_UNIQUE
}

// ---------------------------------------------------------------------------
// Reset
// ---------------------------------------------------------------------------

// Reset discards all state, so a reset store behaves exactly like a freshly
// migrated one.
func (s *store) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var stmt strings.Builder
	for _, table := range slices.Sorted(maps.Keys(s.shape.holds)) {
		stmt.WriteString("DELETE FROM ")
		stmt.WriteString(table)
		stmt.WriteString(";\n")
	}
	if _, err := s.db.ExecContext(ctx, stmt.String()); err != nil {
		return fmt.Errorf("sqlite: reset: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// One type per institution
// ---------------------------------------------------------------------------

// The three institution stores.
type (
	BankStore          struct{ *store }
	ClearingHouseStore struct{ *store }
	CentralBankStore   struct{ *store }
)

// compile-time checks that each is the store the domain declares for its
// institution.
var (
	_ payment.BankStore          = (*BankStore)(nil)
	_ payment.ClearingHouseStore = (*ClearingHouseStore)(nil)
	_ payment.CentralBankStore   = (*CentralBankStore)(nil)
)

func (s *BankStore) Update(ctx context.Context, fn func(context.Context, payment.BankTx) error) error {
	return s.store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (s *BankStore) View(ctx context.Context, fn func(context.Context, payment.BankTx) error) error {
	return s.store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (s *ClearingHouseStore) Update(ctx context.Context, fn func(context.Context, payment.CsmTx) error) error {
	return s.store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (s *ClearingHouseStore) View(ctx context.Context, fn func(context.Context, payment.CsmTx) error) error {
	return s.store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (s *CentralBankStore) Update(ctx context.Context, fn func(context.Context, payment.CentralBankTx) error) error {
	return s.store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (s *CentralBankStore) View(ctx context.Context, fn func(context.Context, payment.CentralBankTx) error) error {
	return s.store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

// EBICS is the transport state of an institution that is DIALLED: its download
// queues and its order log.
func (s *ClearingHouseStore) EBICS() ebics.Store { return ebicsStore{s.store} }
func (s *CentralBankStore) EBICS() ebics.Store   { return ebicsStore{s.store} }

// ---------------------------------------------------------------------------
// The layers underneath a bank
// ---------------------------------------------------------------------------

// Every accessor below re-types update and view and holds no state, so each
// hands its callback the very same *tx: a Register and a Book built over one
// store share a database transaction.

// Ledger is the ledger.Store a Book takes, and both institutions that keep a
// book of accounts have one.
func (s *BankStore) Ledger() ledger.Store         { return ledgerStore{s.store} }
func (s *BankStore) BankLedger() ledger.BankStore { return bankLedgerStore{s.store} }

type bankLedgerStore struct{ *store }

var _ ledger.BankStore = bankLedgerStore{}

func (b bankLedgerStore) Update(ctx context.Context, fn func(context.Context, ledger.BankTx) error) error {
	return b.store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (b bankLedgerStore) View(ctx context.Context, fn func(context.Context, ledger.BankTx) error) error {
	return b.store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

// Ledger is the settlement agent's book, holding the members' reserve accounts.
// There is no BankLedger beside it: this schema creates no slot mapping.
func (s *CentralBankStore) Ledger() ledger.Store { return ledgerStore{s.store} }

type ledgerStore struct{ *store }

var _ ledger.Store = ledgerStore{}

func (l ledgerStore) Update(ctx context.Context, fn func(context.Context, ledger.Tx) error) error {
	return l.store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (l ledgerStore) View(ctx context.Context, fn func(context.Context, ledger.Tx) error) error {
	return l.store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

// Deposit, Product and Lending are a bank's other three layers. There is no
// equivalent on the other two types: no other schema creates a deposit account,
// a product or a facility.
func (s *BankStore) Deposit() deposit.Store { return depositStore{s.store} }

type depositStore struct{ *store }

var _ deposit.Store = depositStore{}

func (d depositStore) Update(ctx context.Context, fn func(context.Context, deposit.Tx) error) error {
	return d.store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (d depositStore) View(ctx context.Context, fn func(context.Context, deposit.Tx) error) error {
	return d.store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (s *BankStore) Product() product.Store { return productStore{s.store} }

type productStore struct{ *store }

var _ product.Store = productStore{}

func (p productStore) Update(ctx context.Context, fn func(context.Context, product.Tx) error) error {
	return p.store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (p productStore) View(ctx context.Context, fn func(context.Context, product.Tx) error) error {
	return p.store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (s *BankStore) Lending() lending.Store { return lendingStore{s.store} }

type lendingStore struct{ *store }

var _ lending.Store = lendingStore{}

func (l lendingStore) Update(ctx context.Context, fn func(context.Context, lending.Tx) error) error {
	return l.store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (l lendingStore) View(ctx context.Context, fn func(context.Context, lending.Tx) error) error {
	return l.store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}
