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
const testBook ledger.BookID = "bank"

// newStore opens an ephemeral BANK store of its own, migrated and empty, closed
// and deleted when the test ends.
func newStore(t *testing.T) *store {
	t.Helper()
	return newShapeStore(t, Bank, testBook)
}

// newShapeStore opens an ephemeral store of a named shape, for the cases that
// are about the schema rather than about the store.
func newShapeStore(t *testing.T, shape Shape, book ledger.BookID) *store {
	t.Helper()
	s, err := open(context.Background(), shape, book, "", frozen)
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
func TestExactlyOneUniqueIndexPerShapeThatHasABook(t *testing.T) {
	for _, c := range []struct {
		shape Shape
		book  ledger.BookID
		want  []string
	}{
		// An equality, not a bound.
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
			// An absent TABLE, argued on the column that replaces it: the
			// classic arrangement's second entries table, and with it the
			// nightly reconciliation a stored control figure needs.
			{"entries", "What is ABSENT is the second entries table"},
			// An index's own reasoning, inside its column list.
			{"transactions_idempotency_key_idx", "the ONLY unique index in this schema"},
			// An absent foreign key, and the exemption that says which ones stay.
			{"subledgers", "carries NO foreign key"},
			// A column that is NOT here, which is the only kind of argument the
			// split adds and the only kind with nothing at all to hang on.
			{"payments", "cycle_id is NOT here. A BANK HAS NO CYCLES"},
			// A column that is not here because another one already says it:
			// bank_assets.settlement being empty is how far through provisioning
			// a bank got, and a status beside it would say so twice.
			{"banks", "There is NO column here saying how far through provisioning"},
			// A TABLE that is in no schema at all, argued on the columns it
			// would otherwise have fed. The business date is the deployment's
			// and is in a file beside these databases.
			{"transactions", "WHAT DAY IT IS IS NOT IN THIS DATABASE"},
			// State the TRANSPORT holds and no institution stores. A payment
			// waiting for its bank's cut-off has a row and no marker saying so.
			{"payments", "WHAT IS NOT HERE IN EITHER SHAPE IS THE HUB"},
		}},
		{CentralBank, payment.CentralBankBook, []struct{ object, argument string }{
			{"ledgers", "A note on what is NOT here: UNIQUE (book_id, name)"},
			{"subledgers", "carries NO foreign key"},
			{"transactions_idempotency_key_idx", "the ONLY unique index in THIS schema"},
			// The same absent table, in the shape a reader would expect to hold
			// it: the settlement agent decides which days money moves on.
			{"transactions", "WHAT DAY IT IS IS NOT IN THIS DATABASE EITHER"},
			// An absent foreign key with no parent to point at, in the shape
			// where being dialled and holding a reserve account are two facts:
			// the clearing house is a subscriber here and is not a member.
			{"ebics_queue", "There is no roster in this database for subscriber to reference"},
		}},
		{CSM, payment.ClearingHouseBook, []struct{ object, argument string }{
			// A COLUMN that is not here, which is the kind of argument with
			// nothing at all to hang on. It held a settlement id, and the id
			// belongs to another institution and never crosses the wire.
			{"cycles", "There is NO settlement_id column"},
			// The audit log's absent foreign key, argued in the shape that has
			// no books table at all.
			{"audit_events", "It has no foreign key to books"},
			// An argument for why something IS here, which a dump needs as much as an
			// absence: bytes addressed to another institution may sit in this one's
			// database because nothing this institution can call reaches them.
			{"ebics_queue", "WHY THE BYTES MAY SIT IN THIS INSTITUTION'S DATABASE AT ALL"},
			// An absent foreign key whose parent IS in this database, on the one
			// table where a member leaving the roster must not take its unread
			// files with it.
			{"ebics_queue", "No foreign key to roster_entries"},
			// An absent foreign key whose parent is in this same database, so
			// the constraint is writable and stays out anyway: what one
			// institution owes another must outlive the batch row naming it.
			{"held_files", "There is NO foreign key to cycles"},
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
func TestOrderingIsChronologicalWithinOneSecond(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	base := time.Date(2027, 7, 15, 10, 0, 0, 0, time.UTC)
	// Deliberately out of chronological order on insert, so the listing has to do
	// the ordering, and each of the two hazards is carried by the row that exposes
	// it.
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
	if err := (ledgerStore{s}).Update(ctx, func(ctx context.Context, tx ledger.Tx) error {
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
	if err := (ledgerStore{s}).View(ctx, func(ctx context.Context, tx ledger.Tx) error {
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
func TestTheRetryBudgetOutlastsASlowWriter(t *testing.T) {
	ctx := context.Background()
	s, err := open(ctx, Bank, testBook, filepath.Join(t.TempDir(), "budget.db"), frozen)
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
