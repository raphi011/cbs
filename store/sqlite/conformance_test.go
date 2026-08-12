// The shared suites, run against store/sqlite.
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

// newBank opens an ephemeral BANK store of its own answering for the given
// book, migrated and empty, discarded when the test ends. No skip and no
// environment variable: this store needs no setup.
func newBank(t *testing.T, book ledger.BookID) *sqlite.BankStore {
	t.Helper()
	s, err := sqlite.OpenBank(context.Background(), book, "", frozen)
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
func newStore(t *testing.T) *sqlite.BankStore {
	t.Helper()
	return testenv.New(t, frozen)
}

// newClearingHouse and newCentralBank are the other two institutions, each
// opened by its own constructor and neither taking a book: there is one of each,
// and each answers for a book that is a constant.
func newClearingHouse(t *testing.T) *sqlite.ClearingHouseStore {
	t.Helper()
	s, err := sqlite.OpenClearingHouse(context.Background(), "", frozen)
	if err != nil {
		t.Fatalf("open clearing house: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return s
}

func newCentralBank(t *testing.T) *sqlite.CentralBankStore {
	t.Helper()
	s, err := sqlite.OpenCentralBank(context.Background(), "", frozen)
	if err != nil {
		t.Fatalf("open central bank: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return s
}

// TestConformance runs all five shared suites against SQLite. Anything a store
// could plausibly get wrong belongs in storetest rather than here, because those
// are the cases all three shapes have to pass.
func TestConformance(t *testing.T) {
	storetest.RunLedger(t, func(t *testing.T, b ledger.BookID) ledger.BankStore { return newBank(t, b).BankLedger() })
	storetest.RunDeposit(t, func(t *testing.T, b ledger.BookID) deposit.Store { return newBank(t, b).Deposit() })
	storetest.RunProduct(t, func(t *testing.T, b ledger.BookID) product.Store { return newBank(t, b).Product() })
	storetest.RunPayment(t, func(t *testing.T, b ledger.BookID) payment.BankStore { return newBank(t, b) })
	storetest.RunClearingHousePayment(t, func(t *testing.T) payment.ClearingHouseStore { return newClearingHouse(t) })
	storetest.RunCentralBankPayment(t, func(t *testing.T) payment.CentralBankStore { return newCentralBank(t) })
	storetest.RunLending(t, func(t *testing.T, b ledger.BookID) lending.Store { return newBank(t, b).Lending() })
}

// TestRaces runs the race suite that needs only concurrent units of work.
func TestRaces(t *testing.T) {
	storetest.RunSystemRaces(t, func(t *testing.T) payment.Stores { return testenv.NewSet(t, frozen) })
	storetest.RunClearingHouseRaces(t, func(t *testing.T) payment.ClearingHouseStore { return newClearingHouse(t) })
	storetest.RunCentralBankRaces(t, func(t *testing.T) payment.CentralBankStore { return newCentralBank(t) })
}

// TestConcurrentTxRaces runs the races that need several units of work open at
// once.
func TestConcurrentTxRaces(t *testing.T) {
	storetest.RunConcurrentTxRaces(t, func(t *testing.T) ledger.Store { return newStore(t).Ledger() })
}

// A unit of work must never be opened inside another one on the same store.
func TestNestedUnitOfWorkIsRefused(t *testing.T) {
	s := newStore(t)

	cases := map[string]func(context.Context) error{
		"UpdateInUpdate": func(ctx context.Context) error {
			return s.Update(ctx, func(ctx context.Context, _ payment.BankTx) error { return nil })
		},
		"ViewInUpdate": func(ctx context.Context) error {
			return s.View(ctx, func(ctx context.Context, _ payment.BankTx) error { return nil })
		},
		"LedgerUpdateInUpdate": func(ctx context.Context) error {
			return s.Ledger().Update(ctx, func(ctx context.Context, _ ledger.Tx) error { return nil })
		},
		"BankLedgerUpdateInUpdate": func(ctx context.Context) error {
			return s.BankLedger().Update(ctx, func(ctx context.Context, _ ledger.BankTx) error { return nil })
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
	}

	for name, nested := range cases {
		t.Run(name, func(t *testing.T) {
			err := s.Update(context.Background(), func(ctx context.Context, _ payment.BankTx) error {
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
	err := s.Update(context.Background(), func(ctx context.Context, _ payment.BankTx) error {
		return other.Update(ctx, func(ctx context.Context, tx payment.BankTx) error {
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
			err := s.Ledger().View(context.Background(), write)
			if !errors.Is(err, sqlite.ErrReadOnly) {
				t.Fatalf("View(%s): got error %v, want %v", name, err, sqlite.ErrReadOnly)
			}
		})
	}
}
