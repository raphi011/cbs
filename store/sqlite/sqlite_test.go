package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

func frozen() time.Time { return time.Unix(0, 0).UTC() }

// testBook is the book the stores below answer for.
//
// A bank's book is its BIC in production and this is not one, deliberately: none
// of the cases in this file is about an institution. They are about the driver,
// the pragmas, the retry loop and the schema dump, and dressing their fixtures
// up as a member bank would suggest the value carried meaning here. The one
// thing it must be is CONSISTENT with the store, because a store answers for
// exactly one book and refuses the rest — see ErrNotThisStoresBook.
const testBook ledger.BookID = "bank"

// newStore opens an ephemeral BANK store of its own, migrated and empty, closed
// and deleted when the test ends.
//
// The bank shape is the default here because it is the widest — every table any
// shape has except the clearing house's roster and cycles and the central bank's
// member register — so a case about the driver rather than about an institution
// has the most to reach for. The three cases that ARE about a shape say which
// one; see newShapeStore.
func newStore(t *testing.T) *Store {
	t.Helper()
	return newShapeStore(t, Bank, testBook)
}

// newShapeStore opens an ephemeral store of a named shape, for the cases that
// are about the schema rather than about the store.
func newShapeStore(t *testing.T, shape Shape, book ledger.BookID) *Store {
	t.Helper()
	s, err := Open(context.Background(), shape, book, "", frozen)
	if err != nil {
		t.Fatalf("open %s: %v", shape, err)
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

// Each shape holds exactly the unique indexes its sentinel mapping can tell
// apart, and for the clearing house that number is ZERO.
//
// The sentinel mapping depends on it. SQLite names no index in a constraint
// error, so a unique conflict is identified by the extended code alone
// (SQLITE_CONSTRAINT_UNIQUE) — which is exactly as targeted as an index name,
// and only while the idempotency index is the only unique index in the database
// the conflict came from. Add a second and the mapping silently starts answering
// ErrDuplicateIdempotencyKey to an unrelated collision.
//
// It is PER SHAPE, and the interesting number is the csm's. The clearing house
// keeps no book of accounts, so it has no transactions table and therefore no
// idempotency index — which means SQLITE_CONSTRAINT_UNIQUE is UNRAISABLE in that
// database. Worth asserting rather than leaving implied: a unique index added
// there would have no mapping at all, and the first conflict on it would surface
// as a driver string.
//
// sqlite_autoindex_* entries are excluded: those are the indexes SQLite creates
// for PRIMARY KEY and UNIQUE column constraints, they raise
// SQLITE_CONSTRAINT_PRIMARYKEY rather than …_UNIQUE, and they are not what the
// mapping is about.
func TestExactlyOneUniqueIndexPerShapeThatHasABook(t *testing.T) {
	for _, c := range []struct {
		shape Shape
		book  ledger.BookID
		want  []string
	}{
		// An equality, not a bound. "At most one" was what could be asserted
		// before the schema was translated; now the one index is there, and
		// naming it is what makes this fail if it is ever RENAMED or dropped as
		// well as if a second one appears. A dropped index would leave the
		// mapping answering ErrDuplicateIdempotencyKey to nothing at all.
		{Bank, testBook, []string{"transactions_idempotency_key_idx"}},
		{CentralBank, payment.CentralBankBook, []string{"transactions_idempotency_key_idx"}},
		// Nil rather than empty, because that is what a nil slice scans to
		// below and slices.Equal treats the two as equal anyway.
		{CSM, payment.ClearingHouseBook, nil},
	} {
		t.Run(c.shape.String(), func(t *testing.T) {
			s := newShapeStore(t, c.shape, c.book)

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

			if !slices.Equal(names, c.want) {
				t.Errorf("unique indexes in the %s schema = %v, want exactly %v; the SQLITE_CONSTRAINT_UNIQUE mapping cannot tell two of them apart",
					c.shape, names, c.want)
			}
		})
	}
}

// The arguments in the schema reach a schema dump.
//
// This is the property CLAUDE.md protects — the reasoning is recorded in the
// database, because a missing constraint is invisible in a schema dump. It holds
// only for comments inside a statement's parentheses: one moved above its
// statement is dropped silently, so it is worth a test rather than a convention
// nobody checks.
//
// What is asserted is not that comments exist. It is that the arguments about
// what the schema does NOT do are in the database, one of each kind the ruling
// distinguishes: an absent constraint on a table, an absent one recorded on an
// index's column list, and a COMMENT ON COLUMN's successor. Any of them moved
// back to column 0 fails this and nothing else in the repository.
//
// One case per SHAPE, because there are three schema files and a comment that
// slid to column 0 in one of them is invisible to a check on another. The csm's
// two are the ones worth naming: it is the shape with no book of accounts, so
// its arguments are all about tables the other two do not have or foreign keys
// that cross into another institution's database — the class of argument that
// has no column to hang on and would otherwise live nowhere.
func TestSchemaArgumentsReachSqliteMaster(t *testing.T) {
	for _, c := range []struct {
		shape Shape
		book  ledger.BookID
		want  []struct{ object, argument string }
	}{
		{Bank, testBook, []struct{ object, argument string }{
			// An absent table constraint. Nothing else records that the missing
			// UNIQUE is a decision.
			{"ledgers", "A note on what is NOT here: UNIQUE (book_id, name)"},
			// An absent CHECK, which has no column to hang a comment from.
			{"accounts", "There is deliberately no CHECK restricting it to the known codes"},
			// An index's own reasoning, inside its column list.
			{"transactions_idempotency_key_idx", "the ONLY unique index in this schema"},
			// An absent foreign key, and the exemption that says which ones stay.
			{"subledgers", "carries NO foreign key"},
			// A column that is NOT here, which is the only kind of argument the
			// split adds and the only kind with nothing at all to hang on.
			{"payments", "cycle_id is NOT here. A BANK HAS NO CYCLES"},
		}},
		{CentralBank, payment.CentralBankBook, []struct{ object, argument string }{
			{"ledgers", "A note on what is NOT here: UNIQUE (book_id, name)"},
			{"subledgers", "carries NO foreign key"},
			{"transactions_idempotency_key_idx", "the ONLY unique index in THIS schema"},
		}},
		{CSM, payment.ClearingHouseBook, []struct{ object, argument string }{
			// A COLUMN that is not here, which is the kind of argument with
			// nothing at all to hang on. It held a settlement id, and the id
			// belongs to another institution and never crosses the wire.
			{"cycles", "There is NO settlement_id column"},
			// The audit log's absent foreign key, argued in the shape that has
			// no books table at all.
			{"audit_events", "It has no foreign key to books"},
		}},
	} {
		t.Run(c.shape.String(), func(t *testing.T) {
			s := newShapeStore(t, c.shape, c.book)
			for _, want := range c.want {
				var ddl string
				if err := s.db.QueryRowContext(context.Background(),
					"SELECT sql FROM sqlite_master WHERE name = ?", want.object).Scan(&ddl); err != nil {
					t.Fatalf("read %s from the %s schema's sqlite_master: %v", want.object, c.shape, err)
				}
				if !strings.Contains(ddl, want.argument) {
					t.Errorf("%s.%s: the database does not hold %q — the argument is in the file and not in the database, which is the failure this test exists for",
						c.shape, want.object, want.argument)
				}
			}
		})
	}
}

// A listing's order is chronological for instants inside one second.
//
// timeLayout claims two things are load-bearing — UTC always, and nine
// fractional digits always, INCLUDING when they are all zero — because a string
// comparison is only a chronological comparison while every value has the same
// width and the same zone. Nothing else in this repository can fail on either:
// every suite runs on a frozen clock at one whole second in UTC, so no second
// instant is ever compared against it and no offset is ever written.
//
// Both halves were watched failing, and one of them not where the comment this
// replaces said it would:
//
//   - timeLayout changed to time.RFC3339Nano, which is what a reader reaches
//     for: the whole suite stays green and this fails, because a WHOLE SECOND
//     renders with no fraction at all and 'Z' is a larger byte than '.', so
//     "…:00Z" sorts after "…:00.045Z" and the earliest row comes out last.
//     Trailing zeros among values that all have a fraction are NOT the hazard:
//     ".45" already sorts before ".5". The fixture below therefore carries a
//     whole second, which is the pair that decides it.
//   - the .UTC() dropped from formatTime: the row written at +02:00 renders
//     with a later hour for the same instant and sorts last.
func TestOrderingIsChronologicalWithinOneSecond(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	base := time.Date(2027, 7, 15, 10, 0, 0, 0, time.UTC)
	// Deliberately out of chronological order on insert, so the listing has to
	// do the ordering, and each of the two hazards is carried by the row that
	// exposes it.
	//
	// ldg_1 is a WHOLE SECOND, which a variable-width layout renders with no
	// fraction at all, and it is the earliest — so a layout that drops the zeros
	// sends the first row to the end.
	//
	// ldg_2 is written in a +02:00 zone and is the SECOND earliest, so its digits
	// read 12:00 where every other row reads 10:00 — a store that does not
	// normalise to UTC sends it to the end too. Putting the offset on a row that
	// belongs last would prove nothing, because a broken sort would agree.
	rows := []struct {
		id string
		at time.Time
	}{
		{"ldg_3", base.Add(450 * time.Millisecond)},
		{"ldg_1", base},
		{"ldg_5", base.Add(900 * time.Millisecond)},
		{"ldg_4", base.Add(500 * time.Millisecond)},
		{"ldg_2", base.Add(45 * time.Millisecond).In(time.FixedZone("CEST", 2*60*60))},
	}
	if err := s.Update(ctx, func(ctx context.Context, tx ledger.Tx) error {
		for _, r := range rows {
			if err := tx.PutLedger(ctx, "bank", ledger.Ledger{ID: ledger.LedgerID(r.id), CreatedAt: r.at}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var got []string
	if err := s.View(ctx, func(ctx context.Context, tx ledger.Tx) error {
		out, err := tx.ListLedgers(ctx, "bank")
		if err != nil {
			return err
		}
		for _, l := range out {
			got = append(got, string(l.ID))
		}
		return nil
	}); err != nil {
		t.Fatalf("list: %v", err)
	}

	want := []string{"ldg_1", "ldg_2", "ldg_3", "ldg_4", "ldg_5"}
	if !slices.Equal(got, want) {
		t.Errorf("ListLedgers = %v, want %v — a string comparison stopped being a chronological one", got, want)
	}
}

// A read-then-write race is refused by the domain's own guard, not by a lock
// error, because Update retries the unit of work.
//
// busy_timeout does not cover a transaction that holds a read and needs to
// upgrade — it cannot succeed by waiting — so without a retry the loser gets
// SQLITE_BUSY where what it is owed is the domain's refusal. With isTransient
// stubbed to false this fails five runs out of five.
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
	s, err := Open(ctx, Bank, testBook, filepath.Join(t.TempDir(), "budget.db"), frozen)
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
