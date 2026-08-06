// Package testenv picks the store every test suite in this repository runs
// against.
//
// It used to do more than that. While store/pg existed this package also owned a
// Postgres schema-per-test lifecycle and read TEST_DATABASE_URL to decide which
// backend a suite got, because "no database needed" and "a real BEGIN/COMMIT"
// were two different runs and only one of them could be the default. Both are
// now one run: store/sqlite needs no server, no Docker and no toolchain, and it
// has real transactions. Choosing is all that is left.
//
// It is an ordinary package rather than a set of _test.go files because the
// domain suites (ledger, deposit, payment, mesh, api, seed) all import it, and a
// _test.go file in an implementation package would be unimportable.
//
// Anything a suite needs that does not name an implementation belongs in
// store/storetest, which every implementation's tests import and which therefore
// may not import an implementation back — Store and Admit both live there for
// that reason. This package is the one place allowed to name an implementation,
// which is why the assertions that they satisfy Store are here.
package testenv

import (
	"context"
	"testing"
	"time"

	"github.com/raphi011/cbs/store/mem"
	"github.com/raphi011/cbs/store/sqlite"
	"github.com/raphi011/cbs/store/storetest"
)

// Store is the shape every implementation shares. It is storetest.Store, aliased
// so that the suites which already say testenv.Store keep saying it.
type Store = storetest.Store

// compile-time checks that the implementations really are interchangeable here.
var (
	_ Store = (*mem.Store)(nil)
	_ Store = (*sqlite.Store)(nil)
)

// New returns the store the suites run against.
//
// It is still store/mem, and that is deliberate rather than left over: Task 17.3
// is where it changes, and changing it here would delete the oracle a commit
// early. Nothing reads an environment variable to decide any more — with
// store/pg gone there is one store to fall back to and one to move to, and the
// choice is in this line rather than in whoever exported what.
func New(t *testing.T, clock func() time.Time) Store {
	t.Helper()
	return mem.New(clock)
}

// OpenSQLite opens an ephemeral SQLite store and migrates it, closing it when
// the test ends.
//
// There is no naming scheme here, and that is the answer to the question rather
// than an omission. An in-memory SQLite database is named, process-wide, and two
// stores sharing a name would share rows — so the name has to be unique per
// STORE, and store/sqlite generates a random one inside Open for every empty
// path. Putting a scheme here as well would be a second source of names for the
// same thing.
//
// It is what Task 18 needs. There the count is N+2 databases per test, one per
// entity plus the network's and the central bank's; each is a store, so each
// calls Open and gets a name of its own. A per-test scheme would have had to
// learn to count.
//
// It replaces OpenInFreshSchema, which created a Postgres schema per test and
// dropped it afterwards, because truncation would have made every test depend on
// the order tables are emptied in. An ephemeral database is that isolation for
// free: there is nothing to create and nothing to drop.
func OpenSQLite(t *testing.T, clock func() time.Time) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(context.Background(), "", clock)
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
