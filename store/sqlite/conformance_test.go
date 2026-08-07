// The shared suites, run against store/sqlite.
//
// Almost nothing is written here. What tests this store is store/storetest, and
// this file is one line per suite. Those suites are written against the Store
// and Tx interfaces and name no table, so what they pin is the CONTRACT, and the
// same file runs against each of the three store shapes. What is NOT here is as
// deliberate: the refusals this package owns (ErrReadOnly, ErrNestedTransaction)
// and the guards on the driver and the schema are in sqlite_test.go, which is an
// internal test package because it reads sqlite_master and drives the retry
// directly.
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

// newBank opens an ephemeral BANK store of its own answering for the given book,
// migrated and empty, discarded when the test ends. No skip and no environment
// variable: this store needs no setup.
//
// It takes the book because a store answers for exactly ONE of them, so a suite
// wanting two books asks for two stores — see storetest's bookA and bookB. The
// shape is the bank's because it is the only one of the three that holds every
// table these five suites reach.
func newBank(t *testing.T, book ledger.BookID) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), sqlite.Bank, book, "", frozen)
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

// newStore is newBank on the book store/testenv opens for the domain suites, for
// the cases here that need one store and do not care which book it answers for.
func newStore(t *testing.T) *sqlite.Store {
	t.Helper()
	return testenv.New(t, frozen)
}

// TestConformance runs all five shared suites against SQLite. Anything a store
// could plausibly get wrong belongs in storetest rather than here, because those
// are the cases all three shapes have to pass.
func TestConformance(t *testing.T) {
	storetest.RunLedger(t, func(t *testing.T, b ledger.BookID) ledger.Store { return newBank(t, b) })
	storetest.RunDeposit(t, func(t *testing.T, b ledger.BookID) deposit.Store { return newBank(t, b).Deposit() })
	storetest.RunProduct(t, func(t *testing.T, b ledger.BookID) product.Store { return newBank(t, b).Product() })
	storetest.RunPayment(t, func(t *testing.T, b ledger.BookID) payment.Store { return newBank(t, b).Payment() })
	storetest.RunClearingHousePayment(t, func(t *testing.T) payment.Store {
		return openShape(t, sqlite.CSM, payment.ClearingHouseBook).Payment()
	})
	storetest.RunCentralBankPayment(t, func(t *testing.T) payment.Store {
		return openShape(t, sqlite.CentralBank, payment.CentralBankBook).Payment()
	})
	storetest.RunLending(t, func(t *testing.T, b ledger.BookID) lending.Store { return newBank(t, b).Lending() })
}

// TestRaces runs the race suite that needs only concurrent units of work.
//
// Each case pins that exactly one racer wins and that every loser lost for the
// DOCUMENTED reason. With payment.admissionSequenceTx made to return nil, all
// four still pass ten runs out of ten, because SQLite admits one writer and
// Store.Update re-runs a loser against the winner's committed row. They are
// regression guards on an ordering this store does not need.
func TestRaces(t *testing.T) {
	storetest.RunSystemRaces(t, func(t *testing.T) payment.Stores { return testenv.NewSet(t, frozen) })
	storetest.RunClearingHouseRaces(t, func(t *testing.T) payment.Store {
		return openShape(t, sqlite.CSM, payment.ClearingHouseBook).Payment()
	})
	storetest.RunCentralBankRaces(t, func(t *testing.T) payment.Store {
		return openShape(t, sqlite.CentralBank, payment.CentralBankBook).Payment()
	})
}

// openShape opens one institution's ephemeral store, for the two race suites
// that are about a single institution's database.
func openShape(t *testing.T, shape sqlite.Shape, book ledger.BookID) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), shape, book, "", frozen)
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
// ConcurrentMarkReversedOnlyOneWins here. A single green run of either is not
// evidence about them; use -count=10 for anything that rests on one.
func TestConcurrentTxRaces(t *testing.T) {
	storetest.RunConcurrentTxRaces(t, func(t *testing.T) storetest.Store { return newStore(t) })
}

// A unit of work must never be opened inside another one on the same store.
//
// Without the guard the nested Update takes a SECOND connection and runs a
// SEPARATE transaction, so its writes would commit even when the outer ones roll
// back — and worse under SQLite, because the inner transaction then contends
// with the outer one for the write lock and the pair can wedge. It is refused
// outright.
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
			return tx.PutLedger(ctx, testenv.BankBook, ledger.Ledger{ID: "ldg_1"})
		})
	})
	if err != nil {
		t.Fatalf("unit of work on a second store: %v", err)
	}
}

// View opens a read-only transaction, so a write through its Tx cannot be part
// of anything that commits. It is refused with a named sentinel rather than left
// to the driver.
func TestViewRejectsWrites(t *testing.T) {
	s := newStore(t)

	cases := map[string]func(context.Context, ledger.Tx) error{
		"NextID": func(ctx context.Context, tx ledger.Tx) error {
			_, err := tx.NextID(ctx, testenv.BankBook, "ldg")
			return err
		},
		"PutLedger": func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutLedger(ctx, testenv.BankBook, ledger.Ledger{ID: "ldg_1"})
		},
		"AppendAudit": func(ctx context.Context, tx ledger.Tx) error {
			return tx.AppendAudit(ctx, ledger.AuditEvent{ID: "evt_1", BookID: testenv.BankBook})
		},
		"MarkReversed": func(ctx context.Context, tx ledger.Tx) error {
			return tx.MarkReversed(ctx, testenv.BankBook, "tx_1")
		},
		"LockAccounts": func(ctx context.Context, tx ledger.Tx) error {
			return tx.LockAccounts(ctx, testenv.BankBook, []ledger.AccountID{"100.100.001"})
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
