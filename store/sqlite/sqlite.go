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
// # There is no memory database, and that reverses a design ruling
//
// The spec for this sub-project says tests use
// file:<unique>?mode=memory&cache=shared rather than a bare ":memory:", and the
// argument it gives is right as far as it goes: ":memory:" hands every pooled
// connection a database of its own, which presents as a store that forgets
// writes at random rather than as an error.
//
// What it does not account for is that shared-cache mode has locking semantics
// of its OWN. Connections in a shared cache take table-level locks, and two
// transactions that each hold a read on one table and then try to write it do
// not produce a winner and a loser — they produce SQLITE_LOCKED for both, and
// retrying re-collides. Measured on two racers reading and then writing one row:
// under shared-cache memory BOTH exhausted every attempt, with and without
// backoff, and the row still held its opening value. The same shape on a file
// under WAL resolves the way store/mem and store/pg resolve it — one winner, and
// the loser's retry reads the winner's write and gets the DOMAIN's refusal.
//
// So the store is always a file, and an empty path is a temporary one removed
// when the store closes. The property store/mem existed for — a fresh checkout
// needs no setup — is kept, because a temp file needs no setup either. What is
// gained is that the configuration under test is the configuration that runs:
// one journal mode, one set of locking rules, no second concurrency model
// reachable only from the tests.
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
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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

// maxAttempts bounds Update's retry loop. See isTransient.
const maxAttempts = 5

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

	// ephemeral is the directory holding a temporary database, removed by Close.
	// It is empty for a store the caller named a path for, whose file is the
	// caller's to keep.
	ephemeral string
}

// Open opens the database at path, applies the embedded migrations and returns a
// store reading time from clock.
//
// An empty path means a temporary database, in a directory of its own, removed
// when the store closes. Two stores opened with an empty path never see each
// other's rows. That is what a test suite wants and it is what `cmd/server` with
// no -database means: ephemeral, and needing no setup, which is the property
// store/mem existed for.
func Open(ctx context.Context, path string, clock func() time.Time) (*Store, error) {
	s := &Store{clock: clock}

	if path == "" {
		dir, err := os.MkdirTemp("", "cbs-sqlite-")
		if err != nil {
			return nil, fmt.Errorf("sqlite: temporary database: %w", err)
		}
		s.ephemeral = dir
		path = filepath.Join(dir, "cbs.db")
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		s.removeEphemeral()
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns)
	// Never retire a connection: reopening one costs a fresh set of pragmas for
	// nothing.
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	s.db = db

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
// it rather than issued afterwards.
func dsn(path string) string {
	pragmas := []string{
		// SQLite ignores REFERENCES without this, per connection, silently.
		"_pragma=foreign_keys(1)",
		// Wait rather than fail when another connection holds the write lock and
		// can still make progress. It does NOT cover a transaction that holds a
		// read and needs to upgrade — see isTransient.
		"_pragma=busy_timeout(5000)",
		// Readers run while a writer holds the lock, and the locking rules are
		// the ones the races were measured against.
		"_pragma=journal_mode(WAL)",
	}
	return "file:" + url.PathEscape(path) + "?" + strings.Join(pragmas, "&")
}

// removeEphemeral deletes a temporary database's directory, if this store owns
// one. The WAL and shared-memory files sit beside the database, which is why a
// directory is removed rather than a file.
func (s *Store) removeEphemeral() {
	if s.ephemeral != "" {
		_ = os.RemoveAll(s.ephemeral)
		s.ephemeral = ""
	}
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
// Measured, before this existed, on two racers reading and then writing one row:
// shared-cache memory livelocked BOTH racers — every attempt of both read the
// stale value and failed, and the balance was still the opening one; a WAL file
// let one racer through on its first attempt while the other spent all five and
// ended holding SQLITE_BUSY. In every configuration the loser's error was a lock
// error and not the domain's refusal, which is the outcome the retry exists to
// prevent.
//
// The jitter is not decoration either. Symmetric racers that back off by the
// same amount re-collide on the same schedule, which is the livelock again one
// step slower. Each waiter needs its own delay, so the wait is a random point in
// a doubling window rather than the window's width.
func backoff(ctx context.Context, attempt int) error {
	window := time.Millisecond << (attempt - 1)
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

// Close releases the pool and, for a store opened with no path, removes the
// temporary database with it: a closed ephemeral store's rows are not something
// a later store should find.
//
// It is idempotent, because store/testenv closes a store the suite it hands it
// to also closes.
func (s *Store) Close() error {
	var err error
	if s.db != nil {
		err = s.db.Close()
		s.db = nil
	}
	s.removeEphemeral()
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
