// The Postgres store's tests. Everything here skips without TEST_DATABASE_URL,
// so `go test ./...` needs no database.
//
// Almost nothing is written here. store/pg has to behave exactly as store/mem
// does, so what tests it are the shared suites in store/storetest, and this file
// is mostly one line per suite pointing it at a fresh schema. What is left over
// is the pair of refusals whose ERROR store/pg owns —
// ErrNestedTransaction and ErrReadOnly — where both stores refuse the same
// mistake for reasons of their own.
//
// The races used to live here, and until Task 17.0 that looked like where they
// belonged: they are about two transactions running at once, which under a
// single process-wide mutex is not a thing that happens. Half of them were about
// something else. storetest.RunRaces holds BOTH stores to one answer on
// payment's acts, which is a claim a pg-only test cannot make, and
// storetest.RunConcurrentTxRaces holds the store primitives — that one really
// does need concurrent open transactions, and this file is where the decision to
// run it is made.
package pg_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/store/pg"
	"github.com/raphi011/cbs/store/storetest"
	"github.com/raphi011/cbs/store/testenv"
)

// frozen is the clock every test store reads: a fixed instant, so CreatedAt ties
// everywhere and the ordering assertions have to rely on the seq column rather
// than on wall-clock luck.
func frozen() time.Time { return time.Unix(0, 0).UTC() }

// requireDSN skips a test unless a database was provided.
func requireDSN(t *testing.T) string {
	t.Helper()
	dsn := testenv.DSN()
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres tests")
	}
	return dsn
}

// newStore opens a store in a schema of its own, migrated and empty, dropped
// when the test ends.
func newStore(t *testing.T) *pg.Store {
	t.Helper()
	return testenv.OpenInFreshSchema(t, requireDSN(t), frozen)
}

// TestConformance runs all three shared conformance suites against Postgres.
// store/mem runs the identical suites; any behaviour the two could drift apart
// on belongs in storetest rather than here.
func TestConformance(t *testing.T) {
	requireDSN(t)

	storetest.RunLedger(t, func(t *testing.T) ledger.Store { return newStore(t) })
	storetest.RunDeposit(t, func(t *testing.T) deposit.Store { return newStore(t).Deposit() })
	storetest.RunProduct(t, func(t *testing.T) product.Store { return newStore(t).Product() })
	storetest.RunPayment(t, func(t *testing.T) payment.Store { return newStore(t).Payment() })
	storetest.RunLending(t, func(t *testing.T) lending.Store { return newStore(t).Lending() })
}

// TestRaces runs the race suite that needs only concurrent units of work.
// store/mem runs the identical cases and the two must agree on who wins; the
// cases that reach that answer here through an ordering the domain arranged, and
// there through the mutex, say so about themselves.
func TestRaces(t *testing.T) {
	requireDSN(t)

	storetest.RunRaces(t, func(t *testing.T) storetest.Store { return newStore(t) })
}

// TestConcurrentTxRaces runs the races that need several units of work open at
// once. store/pg meets that precondition — a pool of connections, one
// transaction each — and store/mem does not, so this call is here and not in
// store/mem's test file.
func TestConcurrentTxRaces(t *testing.T) {
	requireDSN(t)

	storetest.RunConcurrentTxRaces(t, func(t *testing.T) storetest.Store { return newStore(t) })
}

// A unit of work must never be opened inside another one on the same store.
//
// store/mem refuses this because its mutex would deadlock. Here the failure
// would be quieter and worse: the nested Update takes a SECOND connection and
// runs a SEPARATE transaction, so its writes would commit even when the outer
// ones roll back. Both stores refuse it, so the mistake behaves the same either
// way.
func TestNestedUnitOfWorkIsRefused(t *testing.T) {
	s := newStore(t)

	cases := map[string]func(context.Context) error{
		"UpdateInUpdate": func(ctx context.Context) error {
			return s.Update(ctx, func(ctx context.Context, _ ledger.Tx) error { return nil })
		},
		"ViewInUpdate": func(ctx context.Context) error {
			return s.View(ctx, func(ctx context.Context, _ ledger.Tx) error { return nil })
		},
		"DepositUpdateInUpdate": func(ctx context.Context) error {
			return s.Deposit().Update(ctx, func(ctx context.Context, _ deposit.Tx) error { return nil })
		},
		"PaymentUpdateInUpdate": func(ctx context.Context) error {
			return s.Payment().Update(ctx, func(ctx context.Context, _ payment.Tx) error { return nil })
		},
		"PaymentViewInUpdate": func(ctx context.Context) error {
			return s.Payment().View(ctx, func(ctx context.Context, _ payment.Tx) error { return nil })
		},
	}

	for name, nested := range cases {
		t.Run(name, func(t *testing.T) {
			err := s.Update(context.Background(), func(ctx context.Context, _ ledger.Tx) error {
				return nested(ctx)
			})
			if !errors.Is(err, pg.ErrNestedTransaction) {
				t.Fatalf("nested %s: got error %v, want %v", name, err, pg.ErrNestedTransaction)
			}
		})
	}

	// A different store is not the same unit of work, so driving two stores from
	// one context stays legal.
	other := newStore(t)
	err := s.Update(context.Background(), func(ctx context.Context, _ ledger.Tx) error {
		return other.Update(ctx, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutLedger(ctx, "book", ledger.Ledger{ID: "ldg_1"})
		})
	})
	if err != nil {
		t.Fatalf("unit of work on a second store: %v", err)
	}
}

// View opens a READ ONLY transaction, so a write through its Tx cannot be part
// of anything that commits. It is refused with the same shape of error store/mem
// uses.
func TestViewRejectsWrites(t *testing.T) {
	s := newStore(t)

	cases := map[string]func(context.Context, ledger.Tx) error{
		"NextID": func(ctx context.Context, tx ledger.Tx) error {
			_, err := tx.NextID(ctx, "book", "ldg")
			return err
		},
		"PutLedger": func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutLedger(ctx, "book", ledger.Ledger{ID: "ldg_1"})
		},
		"AppendAudit": func(ctx context.Context, tx ledger.Tx) error {
			return tx.AppendAudit(ctx, ledger.AuditEvent{ID: "evt_1", BookID: "book"})
		},
		"MarkReversed": func(ctx context.Context, tx ledger.Tx) error {
			return tx.MarkReversed(ctx, "book", "tx_1")
		},
	}

	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			err := s.View(context.Background(), write)
			if !errors.Is(err, pg.ErrReadOnly) {
				t.Fatalf("View(%s): got error %v, want %v", name, err, pg.ErrReadOnly)
			}
		})
	}
}
