package storetest

import (
	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
)

// Store is the shape an implementation presents when a suite needs more than one
// layer of it at once: a ledger.Store that can also present itself as a
// deposit.Store, a payment.Store and a product.Store over the same state and the
// same unit of work. Go allows a type one Update method, which is why the three
// narrower views are adapters rather than more methods on this one.
//
// The per-layer suites do not take this. RunPayment takes a payment.Store and
// sees nothing else, which is what keeps it honest about which contract it is
// pinning. RunRaces takes this, because it drives payment.Network and then reads
// the result back through the central bank's ledger.
//
// It lives here rather than in store/testenv because store/testenv imports both
// implementations and this package must import neither: every implementation's
// tests import this package. store/testenv aliases it, so the compile-time
// assertions that the implementations satisfy it stay in the one place that is
// allowed to name them.
type Store interface {
	ledger.Store
	Deposit() deposit.Store
	Payment() payment.Store
	Product() product.Store
}
