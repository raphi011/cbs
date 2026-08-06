// Package testenv opens the store every test suite in this repository runs
// against.
//
// It used to CHOOSE one. While store/pg existed it also owned a Postgres
// schema-per-test lifecycle and read TEST_DATABASE_URL, because "no database
// needed" and "a real BEGIN/COMMIT" were two different runs and only one of them
// could be the default; while store/mem existed alongside store/sqlite it named
// which of the two was the default. Both of those are gone. There is one store,
// it needs no setup and it has real transactions, so what is left is opening it
// and closing it again — which is worth a package because every domain suite
// wants the same four lines.
//
// It is an ordinary package rather than a set of _test.go files because the
// domain suites (ledger, deposit, payment, mesh, api, seed) all import it, and a
// _test.go file in an implementation package would be unimportable.
//
// Anything a suite needs that does not name an implementation belongs in
// store/storetest instead, which store/sqlite's own tests import and which
// therefore may not import store/sqlite back — Store and Admit both live there
// for that reason. This package is the one allowed to name the implementation,
// which is why the compile-time assertion that it satisfies Store is here.
package testenv

import (
	"context"
	"testing"
	"time"

	"github.com/raphi011/cbs/store/sqlite"
	"github.com/raphi011/cbs/store/storetest"
)

// Store is the shape a suite needs when it wants several layers of the store at
// once. It is storetest.Store, aliased so that the suites which already say
// testenv.Store keep saying it.
type Store = storetest.Store

// compile-time check that the implementation really does satisfy it.
var _ Store = (*sqlite.Store)(nil)

// New opens an ephemeral SQLite store and migrates it, closing it when the test
// ends.
//
// It returns the concrete type rather than Store, and that is a consequence of
// there being one implementation rather than an oversight. Store names four
// layers and not five — lending's suites reach for Lending(), which is not on
// it — so while two implementations existed the interface was the ceiling and
// lending/portfolio_test.go opened store/mem by hand to get above it. There is
// nothing left for an interface here to abstract over, and callers that want one
// (ledger/book_test.go embeds it to count calls) still have Store.
//
// There is no naming scheme in this package, and that is the answer to Task
// 18's question rather than an omission. An in-memory SQLite database is named,
// process-wide, and two stores sharing a name would share rows — so the name has
// to be unique per STORE, and store/sqlite generates a random one inside Open for
// every empty path. Putting a scheme here as well would be a second source of
// names for the same thing. Under Task 18 the count becomes N+2 databases per
// test, one per entity plus the network's and the central bank's; each is a
// store, so each calls Open and gets a name of its own, and a per-test scheme
// would have had to learn to count.
//
// It replaces OpenInFreshSchema, which created a Postgres schema per test and
// dropped it afterwards, because truncation would have made every test depend on
// the order tables are emptied in. An ephemeral database is that isolation for
// free: there is nothing to create and nothing to drop.
func New(t *testing.T, clock func() time.Time) *sqlite.Store {
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
