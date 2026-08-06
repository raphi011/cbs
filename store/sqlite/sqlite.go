// Package sqlite is the SQLite implementation of ledger.Store, deposit.Store,
// payment.Store, product.Store and lending.Store, on modernc.org/sqlite — real
// SQLite transpiled to Go, so the module gains Go dependencies and loses every
// external one. No server, no Docker, no C toolchain.
//
// It is the only implementation, and it was certified against two that are
// gone. store/pg and store/mem were the oracles this port had: store/storetest
// held all three to the same answers at Task 17.1, and 17.2 and 17.3 deleted
// them. Nothing cross-checks the SQL now, which is why the two guards in
// sqlite_test.go — foreign keys are really enforced, and there is exactly one
// non-primary-key unique index — exist at all: both are failures that change no
// other test's outcome.
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
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
)

// ---------------------------------------------------------------------------
// Shapes
// ---------------------------------------------------------------------------

// Shape is which of the three schemas a store carries, and therefore which
// institution it can be.
//
// A shape is a SCHEMA and not a second driver. There is one implementation of
// every Store and Tx method in this package and it is shared by all three; what
// differs is which tables are underneath it, which is enough to make a crossing
// a table that is not there rather than a row nobody should have read. That was
// the point of putting the boundary in the DDL: "the clearing house has no
// ledger" was true before Task 18 and was asserted by exactly one test, and it
// is now a fact about the database.
//
// The three values are the three actors in mesh/ops.go — bankOps, csmOps,
// settlementOps — and there is no fourth. In particular there is no shape
// holding every table: the monolith that one existed as died with this task,
// and re-adding it would restore the ability to write a statement that spans two
// institutions, which is the whole of what was removed.
type Shape struct {
	// dir is the directory under schema/ holding this shape's migrations, and
	// it doubles as the name in a refusal.
	dir string

	// holds is every table this shape's schema creates. It is what Reset empties
	// and what the method guards consult, and it is written out rather than read
	// from sqlite_master so that a method can be refused BEFORE it reaches SQL —
	// a named sentinel rather than "no such table: cycles", which is a driver
	// string and not something a caller can match on.
	holds map[string]struct{}

	// paymentLegs and paymentCycle are the two places where two shapes hold the
	// SAME table with different columns, and they are the only such places.
	//
	// A crossing at table granularity is ErrNotInThisShape and needs nothing
	// here. These two are finer than that: both the bank and the clearing house
	// keep a copy of every payment they are a party to, and each copy carries
	// what that institution KNOWS. A bank has no cycles — it never sees a
	// cut-off, only a settlement advice quoting a reference it cannot resolve —
	// and the clearing house posts nothing, so it has no legs and no book to post
	// them in. Both arguments are written out in the schema files, on the
	// payments statement of each.
	//
	// They are fields rather than a test on dir because the alternative is a
	// string comparison against "bank" in the middle of an INSERT, which is the
	// kind of thing that survives a rename.
	paymentLegs  bool
	paymentCycle bool
}

// String names the shape, for the refusals.
func (s Shape) String() string { return s.dir }

// The three shapes. See Shape.
var (
	// Bank is one member bank: a ledger, a deposit register, products, lending,
	// its own single row in banks, the mandates it holds as creditor bank, its
	// own copy of each payment it is a party to, and the advices it was sent.
	Bank = shape("bank",
		"books", "ledgers", "subledgers", "accounts", "transactions", "entries",
		"deposit_accounts", "deposit_account_identifiers", "holds", "snapshots", "overdraft_terms",
		"products", "product_versions",
		"facilities", "installments", "facility_terms",
		"banks", "bank_assets", "mandates", "payments", "settlement_advices",
		"audit_events", "id_sequences").withPaymentLegs()

	// CSM is the clearing house: a roster, cycles, its own copy of each payment
	// it carries, and no book of accounts of any kind.
	CSM = shape("csm",
		"roster_entries", "roster_entry_assets",
		"payments", "cycles", "cycle_payments",
		"audit_events", "id_sequences").withPaymentCycle()

	// CentralBank is the settlement agent: a ledger holding the members' reserve
	// accounts, its own member register, the settlements it discharged, and no
	// customers and no payments.
	CentralBank = shape("centralbank",
		"books", "ledgers", "subledgers", "accounts", "transactions", "entries",
		"settlement_members", "settlement_member_accounts",
		"settlements", "settlement_positions",
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

// ErrNotInThisShape is returned by a method whose table this store's schema does
// not create: PutCycle on a bank, PutBank on the clearing house, PutPayment on
// the central bank.
//
// It exists because payment.Tx is ONE interface over three schemas. Go has no
// way to give the clearing house a Tx without PutBank on it, so every shape
// implements every method and two thirds of them have nothing to write to. What
// they must not do is fail as SQL. "no such table: banks" is a driver string, it
// arrives from whichever statement happened to run first, and nothing above the
// store can match on it — so a crossing would be a 500 with a stack trace
// instead of a refusal a caller can handle and a test can assert.
//
// It is checked BEFORE anything else in the method, including the read-only
// guard, and that ordering is deliberate. A View is a legitimate thing to open
// on any store; asking the clearing house to list its cycles inside one is
// legitimate too; asking it to list its BANKS is not, and it is not more
// legitimate for being a read. The class of defect this ordering avoids is the
// one Task 18b's own review found twice: a guard placed after an early return
// that leaves its "does not apply" path open.
var ErrNotInThisShape = errors.New("sqlite: this store's schema holds no such table")

// ErrNotThisStoresBook is returned when a method is handed a BookID this store
// does not answer for.
//
// EACH STORE OWNS EXACTLY ONE BookID, and this is the guard that makes that more
// than a convention. It is the central new refusal of Task 18 and what it turns
// a crossing into is a loud error: before it, a bank's network reaching into the
// central bank's book got a silent not-found — an empty listing, a zero balance,
// a "ledger not found" three layers away — and the only instrument that could
// see it was the book recorder in mesh/books_test.go, which watches mesh actors
// and is therefore blind to anything that never becomes a message. Two of the
// six crossings this sub-project found were of exactly that kind.
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
// change. store/mem and store/pg both answered the same way, for the same
// reason.
var ErrReadOnly = errors.New("sqlite: write attempted in a read-only transaction")

// ErrNestedTransaction is returned when a unit of work is opened inside another
// one on the same store.
//
// The hazard is the one store/pg had: a nested Update takes a SECOND connection
// and runs a SEPARATE transaction, so the inner writes commit even when the
// outer ones roll back. Under SQLite it is worse than under Postgres, because
// the inner transaction then contends with the outer one for the write lock and
// the pair can wedge — a hang rather than an error. store/mem refused it too,
// there because its mutex was not reentrant, so the single most likely mistake
// in this codebase has answered the same way under every store this repository
// has had.
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
// express is concurrency its tests cannot check, and store/mem was exactly that
// store — one process-wide mutex, every race invisible.
//
// It is not unbounded either: SQLite admits one writer, so connections past the
// handful that are reading only queue up to contend for the same lock.
const maxOpenConns = 8

// Store is a SQLite-backed ledger.Store. Call Open; the zero value is unusable.
type Store struct {
	db    *sql.DB
	clock func() time.Time

	// shape is which schema is underneath, and therefore which methods have a
	// table to answer from. See Shape and ErrNotInThisShape.
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

// Open opens the database at path as one institution, applies that shape's
// migrations and returns a store reading time from clock.
//
// shape is which schema goes in and book is the one BookID the result answers
// for; see Shape and ErrNotThisStoresBook. Both are required, and the pair is
// what a store IS after Task 18 — a database with no institution attached is the
// thing that was removed, so there is no way to ask for one.
//
// An empty path means an ephemeral in-memory database of its own: the name is
// random, so two stores opened with an empty path never see each other's rows.
// That is what a test suite wants and it is what `cmd/server` with no -database
// means — ephemeral, and needing no setup, which is the property store/mem
// existed for. It matters more than it did: a test now opens N+2 of these, and
// two banks sharing rows would be the split silently not happening.
func Open(ctx context.Context, shape Shape, book ledger.BookID, path string, clock func() time.Time) (*Store, error) {
	if shape.holds == nil {
		return nil, fmt.Errorf("sqlite: open: no shape; a database with no institution attached is what Task 18 removed")
	}
	if book == "" {
		return nil, fmt.Errorf("sqlite: open %s: no book; every store answers for exactly one and refuses the rest", shape)
	}
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

	s := &Store{db: db, clock: clock, shape: shape, book: book}

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

// Shape is which schema this store carries, and Book is the one BookID it
// answers for.
//
// Both are exported because the composition root has to be able to ask. Nothing
// in the domain does: an institution's handle knows which institution it is (see
// payment.Identity), and a store that had to be interrogated about its own
// identity would be one the caller had not been given deliberately.
func (s *Store) Shape() Shape        { return s.shape }
func (s *Store) Book() ledger.BookID { return s.book }

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

// Update runs fn in one atomic unit of work: BEGIN, fn, COMMIT, or ROLLBACK if
// fn returns an error.
//
// It retries, with a delay, on the transient failures isTransient names. See
// isTransient for why a retry is needed at all and backoff for why it is not
// enough on its own.
func (s *Store) Update(ctx context.Context, fn func(context.Context, ledger.Tx) error) error {
	return s.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

// View runs fn in a read-only unit of work. Writes through the Tx it provides
// fail with ErrReadOnly.
func (s *Store) View(ctx context.Context, fn func(context.Context, ledger.Tx) error) error {
	return s.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

// update and view are the shared bodies the five Store interfaces' Update and
// View methods delegate to. Go allows one Update method per type and the
// interfaces declare it with five different callback types, so the adapters at
// the foot of this file re-type these rather than reimplementing them — the
// callback gets the very same *tx, which is what lets a Register and a Book over
// one store share a transaction.
func (s *Store) update(ctx context.Context, fn func(context.Context, *tx) error) error {
	return s.inUpdate(ctx, func(ctx context.Context, dbtx *sql.Tx) error {
		return fn(ctx, s.newTx(dbtx, false))
	})
}

func (s *Store) view(ctx context.Context, fn func(context.Context, *tx) error) error {
	return s.inView(ctx, func(ctx context.Context, dbtx *sql.Tx) error {
		return fn(ctx, s.newTx(dbtx, true))
	})
}

// newTx wraps one database transaction as the value that implements all five Tx
// interfaces. A fresh books set per attempt is deliberate: a retried unit of
// work has rolled its books rows back, so remembering them across attempts would
// skip the insert that the foreign keys need.
func (s *Store) newTx(dbtx *sql.Tx, readOnly bool) *tx {
	return &tx{store: s, tx: dbtx, readOnly: readOnly, books: make(map[ledger.BookID]struct{})}
}

// inUpdate runs fn in one atomic unit of work over the raw *sql.Tx, retrying on
// the transient failures isTransient names.
//
// It is separate from Update because the retry, the nesting guard and the
// transient classification were testable — and worth watching fail — before any
// statement of the port existed, and the tests that pin them still drive this
// directly rather than through a ledger.Tx.
func (s *Store) inUpdate(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
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
// store/pg retried with no delay and was right to: Postgres detects a deadlock
// after deadlock_timeout, about a second, so its retries were spaced by the
// database whether the caller thinks about it or not. SQLite answers
// immediately, so five undelayed attempts all finish inside the winner's commit
// window and the loser exhausts them without ever seeing the winner's write.
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
func (s *Store) inView(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.checkNotNested(ctx); err != nil {
		return err
	}
	return s.runInTx(ctx, true, fn)
}

// runInTx is one attempt: BEGIN, run fn, COMMIT or ROLLBACK.
func (s *Store) runInTx(ctx context.Context, readOnly bool, fn func(context.Context, *sql.Tx) error) error {
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
func (s *Store) mark(ctx context.Context) context.Context {
	return context.WithValue(ctx, inUnitOfWork{}, s)
}

// checkNotNested refuses to open a unit of work inside another one on this
// store. See ErrNestedTransaction.
func (s *Store) checkNotNested(ctx context.Context) error {
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
func (s *Store) Close() error {
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
// error. store/mem and store/pg both answered with the sentinel, the first
// because its mutex made the loser read after the winner had committed and the
// second because it retried too. The retry re-runs the callback, which reads
// again and reaches the domain's guard. That is what store/storetest's
// RunConcurrentTxRaces asserts: not that somebody lost, but that the loser lost
// for the documented reason.
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
// store/pg asked isUniqueViolationOn(err, "transactions_idempotency_key_idx"),
// because SQLSTATE 23505 carries the constraint's name. SQLite names nothing, so
// what identifies the index here is the extended code alone:
// SQLITE_CONSTRAINT_UNIQUE (2067) for a unique index, and
// SQLITE_CONSTRAINT_PRIMARYKEY (1555) for a primary key, which is a different
// answer and stays out.
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
// What it empties is this SHAPE's tables and nothing else, which is not a
// narrowing so much as the only thing it could now mean: there is one list per
// schema and it is Shape.holds, so a table added to a schema and forgotten here
// is no longer possible. The single list this replaces had all 31 tables in it
// and would have started failing on whichever shape it reached first.
//
// The order is irrelevant: every REFERENCES in every schema is ON DELETE
// CASCADE, so a parent takes its children with it and a child emptied first is a
// no-op when its parent follows.
//
// store/pg needed TRUNCATE … RESTART IDENTITY to put its BIGSERIALs back to 1.
// Nothing here does: every seq is allocated MAX(seq)+1 over the table it belongs
// to (see nextRowSeq), so a table with no rows starts at 1 again on its own. The
// counters in id_sequences are ordinary rows and go with the delete.
//
// The schema itself survives; only rows are removed.
func (s *Store) Reset(ctx context.Context) error {
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
// The four other shapes of the same store
// ---------------------------------------------------------------------------

// Deposit returns this store as a deposit.Store.
//
// It is an adapter rather than a second implementation because Go allows one
// Update method per type and the four Store interfaces declare Update with four
// different callback types. The adapter holds no state and
// hands the callback the very same *tx, so a Register and a Book built over one
// Store share a database transaction.
func (s *Store) Deposit() deposit.Store { return depositStore{s} }

type depositStore struct{ *Store }

var _ deposit.Store = depositStore{}

func (d depositStore) Update(ctx context.Context, fn func(context.Context, deposit.Tx) error) error {
	return d.Store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (d depositStore) View(ctx context.Context, fn func(context.Context, deposit.Tx) error) error {
	return d.Store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

// Product returns this store as a product.Store.
func (s *Store) Product() product.Store { return productStore{s} }

// productStore re-types Store's update and view; Reset and Close are promoted
// unchanged from the embedded *Store.
type productStore struct{ *Store }

// compile-time check that the adapter satisfies the interface it exists for.
var _ product.Store = productStore{}

func (p productStore) Update(ctx context.Context, fn func(context.Context, product.Tx) error) error {
	return p.Store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (p productStore) View(ctx context.Context, fn func(context.Context, product.Tx) error) error {
	return p.Store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

// Lending returns this store as a lending.Store.
//
// Like Deposit, it is an adapter over the same *tx rather than a second
// implementation: a facility write and its GL posting share one transaction
// because both go through the same value.
func (s *Store) Lending() lending.Store { return lendingStore{s} }

type lendingStore struct{ *Store }

var _ lending.Store = lendingStore{}

func (l lendingStore) Update(ctx context.Context, fn func(context.Context, lending.Tx) error) error {
	return l.Store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (l lendingStore) View(ctx context.Context, fn func(context.Context, lending.Tx) error) error {
	return l.Store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

// Payment returns this store as a payment.Store. It is the handle a
// payment.Network takes, and the only one it needs: the Network derives its own
// ledger and deposit views from it.
func (s *Store) Payment() payment.Store { return paymentStore{s} }

type paymentStore struct{ *Store }

var _ payment.Store = paymentStore{}

func (p paymentStore) Update(ctx context.Context, fn func(context.Context, payment.Tx) error) error {
	return p.Store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (p paymentStore) View(ctx context.Context, fn func(context.Context, payment.Tx) error) error {
	return p.Store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

// compile-time check that the store satisfies the interface it is written
// against; the other four are checked on the adapters above.
var _ ledger.Store = (*Store)(nil)
