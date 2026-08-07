// Package testenv opens the stores every test suite in this repository runs
// against.
//
// There is one store, it needs no setup and it has real transactions, so what is
// left is opening it and closing it again — which is worth a package because
// every domain suite wants the same four lines.
//
// # There are two entry points because there are two kinds of suite
//
// New opens ONE member bank's database. That is what the ledger, deposit,
// product and lending suites want: none of them knows what an institution is,
// all of them post in a book called "bank", and a set of databases would be four
// more than they have anything to say about.
//
// NewSet opens the whole system — the clearing house's database, the central
// bank's, and one per member bank on demand. That is what payment, mesh, api,
// seed and cmd/server want, because every one of them drives more than one
// institution and no two of them share a database. The set is what
// payment.Networks is built over.
//
// It is an ordinary package rather than a set of _test.go files because the
// domain suites all import it, and a _test.go file in an implementation package
// would be unimportable.
//
// Anything a suite needs that does not name an implementation belongs in
// store/storetest instead, which store/sqlite's own tests import and which
// therefore may not import store/sqlite back — Store and Admit both live there
// for that reason. This package is the one allowed to name the implementation,
// which is why the compile-time assertions that it satisfies the domain's
// interfaces are here.
package testenv

import (
	"context"
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/sqlite"
	"github.com/raphi011/cbs/store/storetest"
)

// Store is the shape a suite needs when it wants several layers of the store at
// once. It is storetest.Store, aliased so that the suites which already say
// testenv.Store keep saying it.
type Store = storetest.Store

// compile-time checks that the implementation really does satisfy the two
// interfaces this package hands out.
var (
	_ Store          = (*sqlite.Store)(nil)
	_ payment.Stores = (*sqlite.Set)(nil)
)

// BankBook is the book the store New opens answers for.
//
// Every suite that takes a single store posts in a book of this name already —
// ledger, deposit, product and lending all say ledger.NewBook(store, "bank", …)
// — and a store answers for exactly ONE book and refuses the rest. So the value
// is not a default those suites could differ from: it is the book, and a suite
// naming another one is asking a bank's database about somebody else's rows.
//
// A real bank's book is its BIC. This one is not, and does not need to be: the
// four layers above have no idea what an institution is, and dressing their
// fixtures up as one would be pretending the book id carries meaning at a layer
// where it carries none.
const BankBook ledger.BookID = "bank"

// New opens ONE member bank's ephemeral SQLite store, migrated and empty, closed
// when the test ends.
//
// It returns the concrete type rather than Store, because there is one
// implementation and nothing for an interface to abstract over. Store names four
// layers and not five — lending's suites reach for Lending(), which is not on it
// — and callers that want an interface (ledger/book_test.go embeds it to count
// calls) still have Store.
//
// There is no naming scheme in this package, and that is the answer to Task
// 18's question rather than an omission. An in-memory SQLite database is named,
// process-wide, and two stores sharing a name would share rows — so the name has
// to be unique per STORE, and store/sqlite generates a random one inside Open for
// every empty path. Putting a scheme here as well would be a second source of
// names for the same thing. A suite that opens N+2 databases opens N+2 stores,
// each of which gets a name of its own; a per-test scheme would have had to learn
// to count.
//
// An ephemeral database is isolation for free: there is nothing to create and
// nothing to drop, and truncation would have made every test depend on the order
// tables are emptied in.
func New(t *testing.T, clock func() time.Time) *sqlite.Store {
	t.Helper()
	store, err := sqlite.Open(context.Background(), sqlite.Bank, BankBook, "", clock)
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

// NewSet opens an ephemeral database per institution — the clearing house's, the
// central bank's, and one per member bank as it is asked for — and closes them
// all when the test ends.
//
// Every one of them is separate and in-memory, so a suite running against this
// sees exactly what N+2 processes would: no statement spans two institutions, a
// bank reading another bank's rows finds nothing, and a method reaching for a
// table its institution's schema does not create is refused by name. That is
// what makes the split measurable in a test rather than merely intended, and it
// is why the payment, mesh, api and seed suites take this and not New.
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
