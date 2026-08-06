package deposit

import "github.com/raphi011/cbs/ledger"

// The deposit package's own tests live in package deposit_test, because they
// build a Register over a store from store/testenv, which reaches store/sqlite,
// which imports deposit — an in-package test file importing it would be an
// import cycle. The helper below re-exports
// the one unexported field those tests still need to reach. Being in a _test.go
// file, it exists only during `go test` and never widens the public API.

// Book exposes the general ledger the register is layered on, so tests can fund
// a deposit account and inspect its backing GL account directly.
func (r *Register) Book() *ledger.Book { return r.gl }
