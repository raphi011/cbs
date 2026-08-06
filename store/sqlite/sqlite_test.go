package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func frozen() time.Time { return time.Unix(0, 0).UTC() }

// newStore opens an ephemeral store of its own, migrated and empty, closed and
// deleted when the test ends.
func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), "", frozen)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return s
}

// Foreign keys are enforced.
//
// This is the test the package doc calls the reason the pragmas ride in the DSN.
// SQLite ignores REFERENCES unless foreign_keys is on, per connection, and it is
// off by default — so dropping it from the DSN changes NO other test outcome
// anywhere in this repository. Twenty-two REFERENCES clauses would become
// decoration and every suite would stay green.
func TestForeignKeysAreEnforced(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE fk_parent (id TEXT PRIMARY KEY) STRICT;
		CREATE TABLE fk_child (
		    id     TEXT PRIMARY KEY,
		    parent TEXT NOT NULL REFERENCES fk_parent (id) ON DELETE CASCADE
		) STRICT;`); err != nil {
		t.Fatalf("create: %v", err)
	}

	// An orphan is refused.
	if _, err := s.db.ExecContext(ctx,
		"INSERT INTO fk_child (id, parent) VALUES ('c1', 'nobody')"); err == nil {
		t.Fatal("an orphan child was accepted; foreign keys are not enforced")
	}

	// And a delete cascades. Refusal alone would pass with a plain NOT NULL
	// somewhere, so both halves are asserted.
	if _, err := s.db.ExecContext(ctx, "INSERT INTO fk_parent (id) VALUES ('p1')"); err != nil {
		t.Fatalf("insert parent: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO fk_child (id, parent) VALUES ('c1', 'p1')"); err != nil {
		t.Fatalf("insert child: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, "DELETE FROM fk_parent WHERE id = 'p1'"); err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	var children int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM fk_child").Scan(&children); err != nil {
		t.Fatalf("count children: %v", err)
	}
	if children != 0 {
		t.Errorf("children after cascading delete = %d, want 0", children)
	}
}

// The pragma reaches every connection in the pool, not just the first.
//
// A pragma issued as a statement after Open configures exactly one connection
// and then looks like it worked. This asks several connections at once — held
// open simultaneously, so the pool has to produce distinct ones rather than
// handing the same warm connection back each time.
func TestPragmasReachEveryPooledConnection(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	const n = 4
	conns := make([]*sql.Conn, n)
	for i := range conns {
		c, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("conn %d: %v", i, err)
		}
		conns[i] = c
		defer c.Close()
	}
	for i, c := range conns {
		var fk int
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("conn %d: read pragma: %v", i, err)
		}
		if fk != 1 {
			t.Errorf("conn %d: foreign_keys = %d, want 1", i, fk)
		}
	}
}

// Exactly one non-primary-key unique index exists in the schema.
//
// The sentinel mapping depends on it. SQLite names no index in a constraint
// error, so a unique conflict is identified by the extended code alone
// (SQLITE_CONSTRAINT_UNIQUE) rather than by matching an index name the way
// store/pg does. That is exactly as targeted as the name — and only while the
// idempotency index is the only unique index there is. Add a second and the
// mapping silently starts answering ErrDuplicateIdempotencyKey to an unrelated
// collision.
//
// sqlite_autoindex_* entries are excluded: those are the indexes SQLite creates
// for PRIMARY KEY and UNIQUE column constraints, they raise
// SQLITE_CONSTRAINT_PRIMARYKEY rather than …_UNIQUE, and they are not what the
// mapping is about.
func TestExactlyOneUniqueIndex(t *testing.T) {
	s := newStore(t)

	rows, err := s.db.QueryContext(context.Background(), `
		SELECT name FROM sqlite_master
		 WHERE type = 'index' AND sql IS NOT NULL AND sql LIKE 'CREATE UNIQUE INDEX%'
		 ORDER BY name`)
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Task 17.1b adds transactions_idempotency_key_idx and this becomes an
	// equality on that name. Until the schema is translated the assertion that
	// can be made is the one that catches a second index appearing.
	if len(names) > 1 {
		t.Errorf("unique indexes = %v, want at most one; the SQLITE_CONSTRAINT_UNIQUE mapping cannot tell them apart", names)
	}
}

// The arguments in the schema reach a schema dump.
//
// This is the property CLAUDE.md protects — the reasoning is recorded in the
// database, because a missing constraint is invisible in a schema dump — and
// under Postgres it was free, because COMMENT ON is stored. Here it holds only
// for comments inside a statement's parentheses, so it is worth a test rather
// than a convention nobody checks.
func TestSchemaCommentsReachSqliteMaster(t *testing.T) {
	s := newStore(t)

	var ddl string
	if err := s.db.QueryRowContext(context.Background(),
		"SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'books'").Scan(&ddl); err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	if !strings.Contains(ddl, "--") {
		t.Errorf("books carries no comment in sqlite_master; its reasoning did not reach the database:\n%s", ddl)
	}
}

// A unit of work may not be opened inside another one on the same store.
func TestNestedUnitOfWorkIsRefused(t *testing.T) {
	s := newStore(t)

	err := s.inUpdate(context.Background(), func(ctx context.Context, _ *sql.Tx) error {
		return s.inUpdate(ctx, func(context.Context, *sql.Tx) error { return nil })
	})
	if !errors.Is(err, ErrNestedTransaction) {
		t.Fatalf("nested Update: got %v, want %v", err, ErrNestedTransaction)
	}

	// A different store is not the same unit of work.
	other := newStore(t)
	if err := s.inUpdate(context.Background(), func(ctx context.Context, _ *sql.Tx) error {
		return other.inUpdate(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO books (id) VALUES ('b')")
			return err
		})
	}); err != nil {
		t.Fatalf("unit of work on a second store: %v", err)
	}
}

// A read-then-write race is refused by the domain's own guard, not by a lock
// error, because Update retries the unit of work.
//
// This is the case the spec got wrong. busy_timeout does not cover a transaction
// that holds a read and needs to upgrade — it cannot succeed by waiting — so
// without a retry the loser gets SQLITE_BUSY where store/mem and store/pg both
// return the domain's refusal. With isTransient stubbed to false this fails five
// runs out of five.
//
// What it pins is the retry, not its BACKOFF: on the ephemeral store a retry's
// read blocks until the winner commits, so removing the delay changes nothing
// here (measured, five runs, no failures). The delay is pinned by
// TestTheRetryBudgetOutlastsASlowWriter, which opens a file for that reason.
//
// What the retry buys is the LOSER'S REASON. The winner count is right either
// way, which is precisely why this needs its own test: storetest's
// RunConcurrentTxRaces asserts that every loser failed with the documented
// sentinel, and a lock error is not it.
func TestUpdateRetriesUntilTheDomainGuardDecides(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE acct (id TEXT PRIMARY KEY, bal INTEGER NOT NULL) STRICT;
		INSERT INTO acct VALUES ('a', 1000);`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	errInsufficient := errors.New("insufficient balance")

	// Both racers open, both read, and only then does either write — the shape
	// storetest.RunConcurrentTxRaces holds its racers to, and the shape
	// busy_timeout cannot absorb.
	const n = 2
	var opened sync.WaitGroup
	opened.Add(n)
	release := make(chan struct{})
	var once sync.Once

	errs := make([]error, n)
	var done sync.WaitGroup
	for i := range n {
		done.Add(1)
		go func() {
			defer done.Done()
			first := true
			errs[i] = s.inUpdate(ctx, func(ctx context.Context, tx *sql.Tx) error {
				var bal int
				if err := tx.QueryRowContext(ctx, "SELECT bal FROM acct WHERE id = 'a'").Scan(&bal); err != nil {
					return err
				}
				// Only the first attempt waits at the barrier. A retry must not,
				// or the retries deadlock waiting for racers that have finished.
				if first {
					first = false
					opened.Done()
					<-release
				}
				if bal < 600 {
					return errInsufficient
				}
				_, err := tx.ExecContext(ctx, "UPDATE acct SET bal = bal - 600 WHERE id = 'a'")
				return err
			})
		}()
	}
	opened.Wait()
	once.Do(func() { close(release) })
	done.Wait()

	winners := 0
	for i, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, errInsufficient):
		default:
			t.Errorf("racer %d lost for the wrong reason: %v — the retry did not reach the domain guard", i, err)
		}
	}
	if winners != 1 {
		t.Errorf("winners = %d, want exactly 1", winners)
	}

	var bal int
	if err := s.db.QueryRowContext(ctx, "SELECT bal FROM acct WHERE id = 'a'").Scan(&bal); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if bal != 400 {
		t.Errorf("balance = %d, want 400", bal)
	}
}

// The retry budget outlasts a slow writer.
//
// The case above races two units of work that both commit at once, which the
// budget covers however small it is. This one makes the winner HOLD its write
// lock well past the old budget before committing, which is the shape that
// exposed the size as load-bearing rather than arbitrary.
//
// Why holding matters: a reader does not block on a writer, so a retry that
// starts before the winner commits re-reads the stale value, passes the domain's
// check on it, and fails at the write again. The loop converges only once an
// attempt begins AFTER the commit. With the previous budget of about 15ms and a
// 20ms hold, the loser exhausted every attempt and surfaced SQLITE_BUSY —
// twenty runs out of twenty — where what the caller was owed is the domain's
// refusal.
//
// It runs against a FILE and not against the ephemeral store, and that is the
// point rather than an accident. On memdb the retry's SELECT blocks until the
// winner commits, so the loser reaches the domain guard whatever the budget is
// and this case cannot fail — measured: it passes on memdb with the old budget.
// Only WAL lets a reader past an uncommitted writer, so only a file can show
// whether the budget is big enough.
//
// The hold is far longer than any single statement here and far shorter than the
// budget, so this fails if the budget is cut and passes if it is kept.
func TestTheRetryBudgetOutlastsASlowWriter(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "budget.db"), frozen)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE acct (id TEXT PRIMARY KEY, bal INTEGER NOT NULL) STRICT;
		INSERT INTO acct VALUES ('a', 1000);`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	errInsufficient := errors.New("insufficient balance")
	const hold = 120 * time.Millisecond

	const n = 2
	var opened sync.WaitGroup
	opened.Add(n)
	release := make(chan struct{})
	errs := make([]error, n)
	var done sync.WaitGroup

	for i := range n {
		done.Add(1)
		go func() {
			defer done.Done()
			first := true
			errs[i] = s.inUpdate(ctx, func(ctx context.Context, tx *sql.Tx) error {
				var bal int
				if err := tx.QueryRowContext(ctx, "SELECT bal FROM acct WHERE id = 'a'").Scan(&bal); err != nil {
					return err
				}
				if first {
					first = false
					opened.Done()
					<-release
				}
				if bal < 600 {
					return errInsufficient
				}
				if _, err := tx.ExecContext(ctx, "UPDATE acct SET bal = bal - 600 WHERE id = 'a'"); err != nil {
					return err
				}
				// Whoever got the write lock keeps it, so the loser's early
				// retries all read a value that has not been committed yet.
				time.Sleep(hold)
				return nil
			})
		}()
	}
	opened.Wait()
	close(release)
	done.Wait()

	winners := 0
	for i, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, errInsufficient):
		default:
			t.Errorf("racer %d lost for the wrong reason: %v — the retry budget is shorter than the writer it has to outlast", i, err)
		}
	}
	if winners != 1 {
		t.Errorf("winners = %d, want exactly 1", winners)
	}

	var bal int
	if err := s.db.QueryRowContext(ctx, "SELECT bal FROM acct WHERE id = 'a'").Scan(&bal); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if bal != 400 {
		t.Errorf("balance = %d, want 400", bal)
	}
}

// An ephemeral store keeps its rows for its whole lifetime.
//
// The hazard this guards is the one that made ":memory:" unusable: a store whose
// rows depend on which pooled connection answers, so that it forgets between two
// calls with nothing in either error to say so. This writes, forces the pool to
// churn every connection, and reads back.
func TestAnEphemeralStoreDoesNotForget(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	if _, err := s.db.ExecContext(ctx, "INSERT INTO books (id) VALUES ('bank')"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Churn: open every connection the pool will give, then release them all.
	conns := make([]*sql.Conn, 0, maxOpenConns)
	for range maxOpenConns {
		c, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("conn: %v", err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		if err := c.Close(); err != nil {
			t.Fatalf("close conn: %v", err)
		}
	}

	var books int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM books").Scan(&books); err != nil {
		t.Fatalf("count: %v", err)
	}
	if books != 1 {
		t.Errorf("books after the pool churned = %d, want 1", books)
	}
}

// Two stores opened with an empty path never see each other's rows.
func TestTwoEphemeralStoresAreIsolated(t *testing.T) {
	a, b := newStore(t), newStore(t)
	ctx := context.Background()

	if _, err := a.db.ExecContext(ctx, "INSERT INTO books (id) VALUES ('only-in-a')"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var inB int
	if err := b.db.QueryRowContext(ctx, "SELECT count(*) FROM books").Scan(&inB); err != nil {
		t.Fatalf("count: %v", err)
	}
	if inB != 0 {
		t.Errorf("books visible in the second store = %d, want 0", inB)
	}
}
