// Package sqlite is the SQLite implementation of ledger.Store, deposit.Store,
// payment.Store, product.Store and lending.Store, on modernc.org/sqlite — real
// SQLite transpiled to Go, so the module gains Go dependencies and loses every
// external one. No server, no Docker, no C toolchain.
//
// While this package is being written store/mem and store/pg both still exist
// and store/storetest holds all three to the same answers. That is deliberate
// and it does not last: the two of them are the only oracles this port has, and
// Task 17.2 and 17.3 delete them.
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
	"net/url"
	"strings"
	"time"

	sqlite3 "modernc.org/sqlite"
	sqlite3lib "modernc.org/sqlite/lib"
)

// ErrReadOnly is returned when a write is attempted inside View, mirroring
// store/mem and store/pg. The transaction is opened read-only as well, but
// failing in Go first makes all three stores return the same error for the same
// mistake rather than one domain sentinel and two different driver errors.
var ErrReadOnly = errors.New("sqlite: write attempted in a read-only transaction")

// ErrNestedTransaction is returned when a unit of work is opened inside another
// one on the same store.
//
// store/mem refuses this because its mutex is not reentrant and nesting would
// deadlock. Here the hazard is store/pg's: a nested Update takes a SECOND
// connection and runs a SEPARATE transaction, so the inner writes commit even
// when the outer ones roll back. Under SQLite it is worse than under Postgres,
// because the inner transaction then contends with the outer one for the write
// lock and the pair can wedge. All three stores refuse it with the same shape of
// error, so the mistake behaves identically whichever is underneath.
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
// It is not 1. A single connection would serialize every unit of work and turn
// this store into store/mem with a SQL dialect — which would pass the
// conformance suites and quietly make storetest.RunConcurrentTxRaces
// unrunnable, since that suite holds every racer at a barrier INSIDE an open
// transaction and a second racer would never get a connection to arrive on.
// Concurrency this store cannot express is concurrency its tests cannot check.
//
// It is not unbounded either: SQLite admits one writer, so connections past the
// handful that are reading only queue up to contend for the same lock.
const maxOpenConns = 8

// Store is a SQLite-backed ledger.Store. Call Open; the zero value is unusable.
type Store struct {
	db    *sql.DB
	clock func() time.Time

	// keep is one connection held for an ephemeral store's lifetime. A memdb
	// database is destroyed when its last connection closes, and database/sql
	// retires idle connections when it likes, so without this a store loses
	// everything between two calls with nothing in either error to say so. It is
	// nil for a file, which has nothing to lose.
	keep *sql.Conn
}

// Open opens the database at path, applies the embedded migrations and returns a
// store reading time from clock.
//
// An empty path means an ephemeral in-memory database of its own: the name is
// random, so two stores opened with an empty path never see each other's rows.
// That is what a test suite wants and it is what `cmd/server` with no -database
// means — ephemeral, and needing no setup, which is the property store/mem
// existed for.
func Open(ctx context.Context, path string, clock func() time.Time) (*Store, error) {
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

	s := &Store{db: db, clock: clock}

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
	if err := migrate(ctx, db); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

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

// inUpdate runs fn in one atomic unit of work: BEGIN, fn, COMMIT, or ROLLBACK if
// fn returns an error. It retries on the transient failures isTransient names.
//
// Task 17.1c wraps this in the exported Update and View that hand out a
// ledger.Tx. It is separate from them because the retry, the nesting guard and
// the transient classification are testable — and worth watching fail — before
// any statement of the port is written.
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
// store/pg retries with no delay and is right to: Postgres detects a deadlock
// after deadlock_timeout, about a second, so its retries are spaced by the
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
// is told the database is locked where store/mem and store/pg both return the
// domain's own refusal, and a caller that catches the sentinel does not catch a
// lock error. The retry re-runs the callback, which reads again and reaches the
// domain's guard, which refuses for the reason the other two stores refuse for.
// That is what store/storetest's RunConcurrentTxRaces asserts: not that somebody
// lost, but that the loser lost for the documented reason.
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
