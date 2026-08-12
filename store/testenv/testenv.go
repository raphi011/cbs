// Package testenv opens the stores every test suite in this repository runs
// against.
package testenv

import (
	"context"
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/sqlite"
)

// compile-time checks that the implementation really does satisfy the
// interfaces this package hands out.
var (
	_ payment.BankStore          = (*sqlite.BankStore)(nil)
	_ payment.ClearingHouseStore = (*sqlite.ClearingHouseStore)(nil)
	_ payment.CentralBankStore   = (*sqlite.CentralBankStore)(nil)
	_ payment.Stores             = (*sqlite.Set)(nil)
)

// BankBook is the book the store New opens answers for.
const BankBook ledger.BookID = "bank"

// New opens ONE member bank's ephemeral SQLite store, migrated and empty,
// closed when the test ends.
func New(t *testing.T, clock func() time.Time) *sqlite.BankStore {
	t.Helper()
	store, err := sqlite.OpenBank(context.Background(), BankBook, "", clock)
	if err != nil {
		t.Fatalf("testenv: open sqlite: %v", err)
	}
	t.Cleanup(func() {
		// Close is idempotent, which matters because storetest closes the store
		// it was handed as well.
		if err := store.Close(); err != nil {
			t.Errorf("testenv: close: %v", err)
		}
	})
	return store
}

// NewSet opens an ephemeral database per institution — the clearing house's,
// the central bank's, and one per member bank as it is asked for — and closes
// them all when the test ends.
func NewSet(t *testing.T, clock func() time.Time) *sqlite.Set {
	t.Helper()
	set, err := sqlite.OpenSet(context.Background(), "", clock)
	if err != nil {
		t.Fatalf("testenv: open sqlite set: %v", err)
	}
	t.Cleanup(func() {
		if err := set.Close(); err != nil {
			t.Errorf("testenv: close set: %v", err)
		}
	})
	return set
}
