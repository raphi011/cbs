// The shared suites, run against store/sqlite.
//
// Almost nothing is written here. What tests this store is store/storetest, and
// this file is one line per suite. That division was a conformance argument once
// — three implementations, one set of answers — and it survives it: the suites
// are written against the Store and Tx interfaces and name no table, so what
// they pin is the CONTRACT, and Task 18 runs the same file against each of its
// three store shapes. What is NOT here is as deliberate: the refusals this
// package owns (ErrReadOnly, ErrNestedTransaction) and the guards on the driver
// and the schema are in sqlite_test.go, which is an internal test package
// because it reads sqlite_master and drives the retry directly.
//
// It is an external test package for the ordinary reason: store/testenv imports
// this package, so a test file inside it that imported testenv back would be a
// cycle.
package sqlite_test

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
	"github.com/raphi011/cbs/store/sqlite"
	"github.com/raphi011/cbs/store/storetest"
	"github.com/raphi011/cbs/store/testenv"
)

// frozen is the clock every test store reads: a fixed instant, so CreatedAt ties
// everywhere and the ordering assertions have to rely on the seq column rather
// than on wall-clock luck.
func frozen() time.Time { return time.Unix(0, 0).UTC() }

// newStore opens an ephemeral store of its own, migrated and empty, discarded
// when the test ends. No skip and no environment variable: needing no setup is
// the property store/mem existed for, and this store having it is what let
// store/mem go.
func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
	return testenv.New(t, frozen)
}

// TestConformance runs all five shared suites against SQLite. Anything a store
// could plausibly get wrong belongs in storetest rather than here, because these
// are the cases Task 18's three shapes will each have to pass.
func TestConformance(t *testing.T) {
	storetest.RunLedger(t, func(t *testing.T) ledger.Store { return newStore(t) })
	storetest.RunDeposit(t, func(t *testing.T) deposit.Store { return newStore(t).Deposit() })
	storetest.RunProduct(t, func(t *testing.T) product.Store { return newStore(t).Product() })
	storetest.RunPayment(t, func(t *testing.T) payment.Store { return newStore(t).Payment() })
	storetest.RunLending(t, func(t *testing.T) lending.Store { return newStore(t).Lending() })
}

// TestRaces runs the race suite that needs only concurrent units of work.
//
// Each case pins that exactly one racer wins and that every loser lost for the
// DOCUMENTED reason. What is worth knowing about them here is measured and is
// recorded where the orderings are: with payment.admissionSequenceTx made to
// return nil, all four cases still pass ten runs out of ten, because SQLite
// admits one writer and Store.Update re-runs a loser against the winner's
// committed row. They are regression guards on an ordering this store no longer
// needs, kept because Task 18 removes the book their counter is drawn from and
// this is what would notice if the replacement stopped ordering anything.
func TestRaces(t *testing.T) {
	storetest.RunRaces(t, func(t *testing.T) storetest.Store { return newStore(t) })
}

// TestConcurrentTxRaces runs the races that need several units of work open at
// once.
//
// The precondition is that two calls to Update can be inside their callbacks at
// the same instant, and this store meets it: the pool hands out maxOpenConns
// connections and each unit of work takes one, which is why maxOpenConns is not
// 1. That was established by running this suite rather than by reasoning about
// it — a store that admitted one at a time would not fail these cases, it would
// STOP in them, because the barrier waits for a racer that cannot arrive.
//
// Two of the nine cases do not bite on a single run —
// ConcurrentAdmissionsAgreeOnOneCentralBank in TestRaces and
// ConcurrentMarkReversedOnlyOneWins here each passed one run on store/pg with
// their guard removed and failed within ten. A single green run of either is not
// evidence about them; use -count=10 for anything that rests on one.
func TestConcurrentTxRaces(t *testing.T) {
	storetest.RunConcurrentTxRaces(t, func(t *testing.T) storetest.Store { return newStore(t) })
}

// A unit of work must never be opened inside another one on the same store.
//
// Without the guard the nested Update takes a SECOND connection and runs a
// SEPARATE transaction, so its writes would commit even when the outer ones roll
// back — and worse than that under SQLite, because the inner transaction then
// contends with the outer one for the write lock and the pair can wedge. It is
// refused outright, which is what store/mem and store/pg did too, so the single
// most likely mistake in this codebase has never depended on the backend.
//
// All five shapes are driven, not just ledger's, because each is a separate
// Update method on a separate adapter type and the guard is per method.
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
		"ProductUpdateInUpdate": func(ctx context.Context) error {
			return s.Product().Update(ctx, func(ctx context.Context, _ product.Tx) error { return nil })
		},
		"LendingUpdateInUpdate": func(ctx context.Context) error {
			return s.Lending().Update(ctx, func(ctx context.Context, _ lending.Tx) error { return nil })
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
			if !errors.Is(err, sqlite.ErrNestedTransaction) {
				t.Fatalf("nested %s: got error %v, want %v", name, err, sqlite.ErrNestedTransaction)
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

// View opens a read-only transaction, so a write through its Tx cannot be part
// of anything that commits. It is refused with a named sentinel rather than left
// to the driver, which is what store/mem and store/pg did too.
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
		"LockAccounts": func(ctx context.Context, tx ledger.Tx) error {
			return tx.LockAccounts(ctx, "book", []ledger.AccountID{"100.100.001"})
		},
	}

	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			err := s.View(context.Background(), write)
			if !errors.Is(err, sqlite.ErrReadOnly) {
				t.Fatalf("View(%s): got error %v, want %v", name, err, sqlite.ErrReadOnly)
			}
		})
	}
}
