// Package sqlite is the SQLite implementation of every store the domain
// declares — ledger's, deposit's, product's, lending's, ebics's, and one per
// institution at the payment layer — on modernc.org/sqlite, real SQLite
// transpiled to Go, so the module gains Go dependencies and loses every external
// one. No server, no Docker, no C toolchain.
//
// There are three ways in and each is an institution: OpenBank,
// OpenClearingHouse, OpenCentralBank. See BankStore.
//
// Nothing cross-checks the SQL: this is the only implementation. That is why the
// two guards in sqlite_test.go — foreign keys are really enforced, and there is
// exactly one non-primary-key unique index — exist at all: both are failures
// that change no other test's outcome.
//
// # Everything the connection needs rides in the DSN
//
// database/sql hands out a pool of connections and SQLite settings are
// per-connection. A pragma issued as a statement after Open therefore configures
// exactly one connection out of however many the pool later creates, and then
// looks like it worked — the first query on a fresh connection is the one that
// behaves differently. Every pragma this store depends on is in the DSN, where
// the driver applies it to each connection as that connection is opened.
//
// foreign_keys is the one that makes this more than tidiness. SQLite ignores
// REFERENCES entirely unless it is on, silently, and it is off by default; the
// schema has twenty-two of them. A dropped pragma changes no test outcome
// anywhere in this repository except the one written for it — see
// TestForeignKeysAreEnforced.
//
// # The ephemeral database is memdb, not shared cache
//
// An empty path means memory, which is the spec's intent, but not by the
// mechanism the spec names. It says file:<unique>?mode=memory&cache=shared, and
// the argument it gives against a bare ":memory:" is right: ":memory:" hands
// every pooled connection a database of its own, which presents as a store that
// forgets writes at random rather than as an error.
//
// Shared cache fixes that and brings a locking model of its own. Connections in
// a shared cache take TABLE-level locks that are held until the transaction
// ends, so a read-then-write pair returns SQLITE_LOCKED rather than SQLITE_BUSY.
// Two things follow and the second is disqualifying:
//
//   - The code differs from the file-backed path's, so one retry classification
//     would have to know which mechanism it is running on.
//   - modernc does not return that code to the caller. It diverts into
//     sqlite3_unlock_notify and waits on a mutex with no deadline and no
//     context (conn.retry, conn.go:435-455 in v1.56.0) — read, not measured
//     here, but there is nothing in that function to end the wait if the
//     blocking transaction stays open. A suite that holds transactions open at
//     a barrier is the shape that would find out. SQLite's own documentation
//     calls shared-cache mode obsolete and names WAL as the replacement.
//
// memdb is the in-memory VFS that shares one database between connections
// without any of that: the name must begin with "/", the locking is the ordinary
// kind, and a loser gets SQLITE_BUSY exactly as it would on a file — so one
// classification covers both. Twenty runs of the read-then-write race on memdb
// and twenty of the slow-writer race on a WAL file both give one winner and a
// loser that reaches the domain's own refusal.
//
// Two obligations come with it, both measured rather than assumed: the database
// is destroyed when its LAST connection closes, so the store holds one for its
// lifetime; and the name is process-wide, so it is random per store or two
// stores would share rows.
//
// A named path is an ordinary file under WAL, which no in-memory database can
// be — journal_mode on any of them is pinned to MEMORY or OFF and an attempt to
// set WAL is ignored without an error.
//
// # Update retries with backoff
//
// See isTransient and backoff.
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
//
// A shape is a SCHEMA and not a second driver. There is one implementation of
// every Store and Tx method in this package and it is shared by all three; what
// differs is which tables are underneath it. Putting the boundary in the DDL is
// what makes "the clearing house has no ledger" a fact about the database rather
// than a claim one test asserts.
//
// It refuses nothing, and that is the point: WHICH METHODS AN
// INSTITUTION HAS IS A TYPE — see BankStore, ClearingHouseStore and
// CentralBankStore — so a method reaching a table its schema does not create
// cannot be named, let alone called. What is left here is what a type cannot
// say: which tables Reset empties, where the migrations live, and the two column
// lists below.
//
// The three values are the three institutions and there is no fourth. In
// particular there is no shape holding every table: one would restore the
// ability to write a statement that spans two institutions.
type Shape struct {
	// dir is the directory under schema/ holding this shape's migrations.
	dir string

	// holds is every table this shape's schema creates. It is what Reset empties,
	// and it is written out rather than read from sqlite_master so that a table
	// added to a schema and forgotten here is not possible.
	holds map[string]struct{}

	// paymentLegs and paymentCycle are the two places where two shapes hold the
	// SAME table with different columns, and they are the only such places.
	//
	// They are finer than any type: both the bank and the clearing house keep a
	// copy of every payment they are a party to — that is payment.PaymentRowsTx,
	// which both have — and each copy carries what that institution KNOWS. A bank
	// has no cycles, only a settlement advice quoting a reference it cannot
	// resolve; the clearing house posts nothing, so it has no legs and no book to
	// post them in. Both arguments are written out in the schema files, on the
	// payments statement of each.
	//
	// So the honest claim is that the TYPE refuses a table an institution does
	// not have, and the implementation still decides which columns it writes.
	//
	// They are fields rather than a test on dir because the alternative is a
	// string comparison against "bank" in the middle of an INSERT, which is the
	// kind of thing that survives a rename.
	paymentLegs  bool
	paymentCycle bool
}

// String names the shape, for the migration errors.
func (s Shape) String() string { return s.dir }

// The three shapes. See Shape.
var (
	// Bank is one member bank: a ledger, a deposit register, products, lending,
	// its own single row in banks, its copy of the scheme's routing directory,
	// the mandates it holds as creditor bank, its own copy of each payment it is
	// a party to, and the advices it was sent.
	//
	// It holds NO transport, and that is the topology rather than an omission.
	// EBICS has no push, so a bank dials the two institutions that host queues
	// and is never dialled itself; the files it is owed wait in a queue in
	// somebody else's database until it comes to collect them.
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
//
// EACH STORE OWNS EXACTLY ONE BookID, and this is the guard that makes that more
// than a convention. What it turns a crossing into is a loud error: without it,
// a bank's network reaching into the central bank's book gets a silent not-found
// — an empty listing, a zero balance, a "ledger not found" three layers away —
// and the only instrument that could see it is the book recorder in
// cmd/server/books_test.go, which watches an institution's units of work and is
// therefore blind to anything that never becomes one.
//
// It does not replace the recorder and the recorder gets STRONGER beside it. The
// two answer different questions: this one says a store was asked about a book
// that is not its own, and the recorder says which books an act touched at all.
// A bank posting in the wrong place within its OWN book is still the recorder's
// to catch, and always will be — a BookID is an ordinary argument and one value
// of it is as valid as another until something says otherwise. This is that
// something, for the one value it can decide.
var ErrNotThisStoresBook = errors.New("sqlite: this store does not answer for that book")

// ErrReadOnly is returned when a write is attempted inside View.
//
// The transaction is opened read-only as well, so the database would refuse the
// write anyway; failing in Go first is what makes the answer a named sentinel a
// caller can match on rather than a driver error whose text is the driver's to
// change.
var ErrReadOnly = errors.New("sqlite: write attempted in a read-only transaction")

// ErrNestedTransaction is returned when a unit of work is opened inside another
// one on the same store.
//
// A nested Update takes a SECOND connection and runs a SEPARATE transaction, so
// the inner writes commit even when the outer ones roll back — and under SQLite
// the inner transaction then contends with the outer one for the write lock and
// the pair can wedge, which is a hang rather than an error.
var ErrNestedTransaction = errors.New("sqlite: a unit of work is already open on this store (call the …Tx method, not its Update-wrapping sibling)")

// inUnitOfWork is the context key Update and View stamp into the context they
// hand their callback. Its value is the *Store, so two independent stores can
// still be driven from one context — only re-entering the same store is refused.
type inUnitOfWork struct{}

// maxAttempts and backoffBase bound Update's retry loop, and between them they
// set the RETRY BUDGET: the longest a losing unit of work will keep trying
// before it gives up and returns the lock error to the caller.
//
// The budget has to exceed the longest write transaction the system runs, and
// that is not a tuning preference. It is a FILE's problem rather than an
// ephemeral store's, and the difference is worth knowing: on memdb a retry's
// SELECT blocks until the winner commits, so the loser reaches the domain guard
// however small the budget is. Under WAL a reader does not block on a writer, so
// the retry re-reads the value the winner has not committed yet, passes the
// domain's check on stale data, and fails at the write again — converging only
// once an attempt starts AFTER the commit.
//
// Measured on a file, with a writer holding its lock for 120ms: at the previous
// budget of five attempts from a 1ms window the loser exhausted every attempt
// and surfaced SQLITE_BUSY, three runs out of three. At the budget below it
// reaches the domain's refusal, twenty runs out of twenty.
// TestTheRetryBudgetOutlastsASlowWriter is that case, and it opens a file for
// exactly this reason.
//
// So: ten attempts over a doubling window from 2ms is a worst case near two
// seconds and an expected wait near one, against a busy_timeout of five. That is
// generous for the transactions this system runs today and cheap when nothing
// contends, because a unit of work that does not lose never waits at all.
const (
	maxAttempts = 10
	backoffBase = 2 * time.Millisecond
)

// maxOpenConns is how many connections a CALLER may hold at once.
//
// It is not 1. A single connection would serialize every unit of work — which
// would pass every shared suite and quietly make storetest.RunConcurrentTxRaces
// unrunnable, since that suite holds every racer at a barrier INSIDE an open
// transaction and a second racer would never get a connection to arrive on. It
// would not FAIL the suite; it would stop in it. Concurrency this store cannot
// express is concurrency its tests cannot check.
//
// It is not unbounded either: SQLite admits one writer, so connections past the
// handful that are reading only queue up to contend for the same lock.
const maxOpenConns = 8

// store is the ONE implementation. There is a type per institution over it —
// BankStore, ClearingHouseStore, CentralBankStore — and each is a set of
// re-typed units of work over this, not a second body of SQL.
//
// It is unexported because no caller has a database without an institution
// attached: OpenBank, OpenClearingHouse and OpenCentralBank are the three ways
// in, and each returns the type whose methods that schema can answer.
type store struct {
	db    *sql.DB
	clock func() time.Time

	// shape is which schema is underneath: the migrations that went in, the
	// tables Reset empties, and the two payment column lists. See Shape.
	shape Shape

	// book is the ONE BookID this store answers for, and every method taking one
	// refuses any other. See ErrNotThisStoresBook.
	//
	// It is a field on the store and not on the schema because only one of the
	// three shapes knows its own value at build time: the clearing house's and
	// the central bank's books are constants, and a bank's is the bank's id,
	// which is minted when that bank's database is provisioned. So the value
	// arrives at Open, from the composition root that named the database.
	book ledger.BookID

	// keep is one connection held for an ephemeral store's lifetime. A memdb
	// database is destroyed when its last connection closes, and database/sql
	// retires idle connections when it likes, so without this a store loses
	// everything between two calls with nothing in either error to say so. It is
	// nil for a file, which has nothing to lose.
	keep *sql.Conn
}

// The three ways to open a database, one per institution.
//
// The SHAPE IS A CONSTRUCTOR and not a parameter, which is what makes each
// result a type whose methods its schema can answer. A single Open returning one
// type whatever it was handed would leave the mismatch it removes: open the
// clearing house's schema, ask for a bank's unit of work, and every bank-only
// method reaches a table that is not there.
//
// Two of the three take no book, because there is one clearing house and one
// settlement agent and each answers for a BookID that is a constant. A bank's is
// the one value no schema knows at build time: it is minted when that bank's
// database is provisioned, and it is the bank's BIC (see payment.AsBank), which
// is also the name of the file. The four layers below payment know nothing of
// institutions and open a bank's schema under a book of their own, which is why
// this takes a BookID rather than a BIC.
//
// An empty path means an ephemeral in-memory database of its own: the name is
// random, so two stores opened with an empty path never see each other's rows.
// That is what a test suite wants and it is what `cmd/server` with no -database
// means — ephemeral, and needing no setup. A test opens N+2 of these, and two
// banks sharing rows would be the split silently not happening.

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
//
// It is exported because the composition root has to be able to ask. Nothing in
// the domain does: an institution's handle knows which institution it is (see
// payment.Identity), and a store that had to be interrogated about its own
// identity would be one the caller had not been given deliberately.
//
// There is no exported Shape accessor beside it, and there is nothing left to
// ask: the shape is the TYPE now.
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
		// connections rather than private to each; without it every connection
		// gets a database of its own, which is the ":memory:" failure again under
		// another name. Random, because the name is process-wide.
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
//
// Update runs fn in one atomic unit of work: BEGIN, fn, COMMIT, or ROLLBACK if
// fn returns an error, retrying with a delay on the transient failures
// isTransient names. View opens the same thing read-only, and a write through
// its Tx fails with ErrReadOnly.
//
// Go allows one Update method per type and the interfaces declare it with a
// callback type each, so the institution types and the layer adapters below
// re-type these rather than reimplementing them — the callback gets the very
// same *tx, which is what lets a Register and a Book over one store share a
// transaction.
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
// interfaces. A fresh books set per attempt is deliberate: a retried unit of
// work has rolled its books rows back, so remembering them across attempts would
// skip the insert that the foreign keys need.
func (s *store) newTx(dbtx *sql.Tx, readOnly bool) *tx {
	return &tx{store: s, tx: dbtx, readOnly: readOnly, books: make(map[ledger.BookID]struct{})}
}

// inUpdate runs fn in one atomic unit of work over the raw *sql.Tx, retrying on
// the transient failures isTransient names.
//
// It is separate from Update because the retry, the nesting guard and the
// transient classification were testable — and worth watching fail — before any
// statement of the port existed, and the tests that pin them still drive this
// directly rather than through a ledger.Tx.
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
//
// The retries are DELAYED. SQLite answers a lock conflict immediately, so five
// undelayed attempts would all finish inside the winner's commit window and the
// loser would exhaust them without ever seeing the winner's write.
//
// The delay is what a file needs. Measured on two racers reading and then
// writing one row under WAL: with the sleep removed one racer won on its first
// attempt and the other spent every attempt and ended holding SQLITE_BUSY, so
// the loser's error was a lock error and not the domain's refusal. On the
// ephemeral store the same removal changes nothing, because there the retry's
// read blocks until the winner commits — five runs, no failures. See maxAttempts
// for why the SIZE of the budget matters as much as its existence.
//
// The jitter is not decoration either. Symmetric racers that back off by the
// same amount re-collide on the same schedule, which is the livelock again one
// step slower. Each waiter needs its own delay, so the wait is a random point in
// a doubling window rather than the window's width.
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
//
// It is idempotent, because store/testenv closes a store the suite it hands it
// to also closes.
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
//
// SQLITE_BUSY and SQLITE_LOCKED both mean "another connection holds what this
// one needs". Retrying is not a substitute for correct ordering; it exists
// because the ordering the domain arranged can still lose a race for the write
// lock, and losing it is transient by definition.
//
// # Why busy_timeout is not enough, measured
//
// The DSN asks SQLite to wait five seconds rather than fail. It does not wait
// for the case that matters here. A transaction that already holds a read and
// then tries to write cannot succeed by waiting — the connection it would wait
// for is itself blocked behind this one — so SQLite declines to sleep and
// answers immediately. Both configurations were measured on modernc v1.56.0
// (SQLite 3.53.3), two transactions open, both reading and then writing:
// shared-cache memory answers SQLITE_LOCKED (6) and a WAL file answers
// SQLITE_BUSY (5), both in under ten milliseconds with busy_timeout(5000) set.
//
// Without the retry the money is still right — one writer wins — but the loser
// is told the database is locked, where what it is owed is the domain's own
// refusal: a caller that catches ErrInsufficientBalance does not catch a lock
// error. The retry re-runs the callback, which reads again and reaches the
// domain's guard. That is what store/storetest's RunConcurrentTxRaces asserts:
// not that somebody lost, but that the loser lost for the documented reason.
//
// The primary code is what is matched. SQLite's extended codes carry the primary
// one in their low byte, so SQLITE_BUSY_SNAPSHOT and SQLITE_LOCKED_SHAREDCACHE
// are covered without naming them.
//
// A constraint violation is deliberately NOT in the list. A duplicate
// idempotency key is an answer, and retrying it would turn a definite answer
// into four more attempts at the same one.
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
//
// SQLite names no constraint in its errors, so what identifies the index here is
// the extended code alone: SQLITE_CONSTRAINT_UNIQUE (2067) for a unique index,
// and SQLITE_CONSTRAINT_PRIMARYKEY (1555) for a primary key, which is a
// different answer and stays out.
//
// That is exactly as targeted as matching the name, and it is targeted for a
// reason outside this function: the idempotency index is the only non-primary-key
// unique index in the schema. TestExactlyOneUniqueIndex reads sqlite_master and
// fails if a second one appears, because a second one would silently make
// ErrDuplicateIdempotencyKey the answer to an unrelated collision. The one call
// site is PutTransaction, and it is the guard that lets it be written this way.
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
//
// What it empties is this SHAPE's tables and nothing else. There is one list per
// schema and it is Shape.holds, so a table added to a schema and forgotten here
// is not possible.
//
// The order is irrelevant: every REFERENCES in every schema is ON DELETE
// CASCADE, so a parent takes its children with it and a child emptied first is a
// no-op when its parent follows.
//
// Nothing has to reset a sequence: every seq is allocated MAX(seq)+1 over the
// table it belongs to (see nextRowSeq), so a table with no rows starts at 1
// again on its own. The counters in id_sequences are ordinary rows and go with
// the delete.
//
// The schema itself survives; only rows are removed.
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

// The three institution stores. Each is the ONE implementation above, seen at
// the width its schema can answer: a bank reaches 74 methods through its unit of
// work, the clearing house 30, the settlement agent 46, and none of the three
// can NAME a table its schema does not create.
//
// Nothing is duplicated to get that. There is one tx struct, one Store body and
// one set of statements; what differs is which interface a caller is handed.
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
//
// It is on these two types and on no third, which is the topology rather than an
// omission. EBICS has no push, so a member bank dials the two institutions that
// host queues and is never dialled itself; the files it is owed wait in a queue
// in somebody else's database until it comes to collect them.
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
//
// BankLedger beside it is a bank's WIDER ledger: the same tables plus the slot
// mapping, which slot_accounts makes a bank's alone. They are two accessors
// rather than one because a Book is the ledger BOTH institutions have, so it
// takes the narrow one even here; the mapping is reached through the transaction
// a deposit or lending act already holds.
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
