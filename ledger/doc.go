// Package ledger implements a double-entry general ledger.
//
// It models the pure accounting core of a banking system: ledgers,
// subledgers, accounts, multi-legged transactions, postings, on-demand
// book balances, and an immutable audit log. It deliberately knows
// nothing about demand-deposit concerns such as account status, holds,
// available balance, or end-of-day snapshots — those live in the deposit
// package, which is layered on top of this one.
//
// # Control accounts
//
// An account may pool subsidiaries: one chart-of-accounts line standing for
// many customers, with every entry against it naming which. What a subsidiary IS
// stays outside this package — Entry.Subsidiary is an opaque string the layer
// above supplies, with no table behind it — so the ledger gains a dimension
// without learning what a customer is. Every balance read therefore takes a
// Position rather than an account: the whole pool, which is the control figure,
// or one subsidiary within it. The two come from one aggregate with one predicate
// between them, so no total is stored anywhere and nothing can drift.
//
// It also holds no state. Every entity lives behind the Store and Tx
// interfaces declared in store.go and implemented by store/sqlite; what stays
// here is the validation and the orchestration. The interfaces are still
// interfaces with one implementation behind them, because the boundary is what
// keeps this package from knowing how anything is stored — every rule here is
// stated against Store and Tx, and none of it names a table.
//
// See README.md for a detailed explanation of the banking concepts
// modeled by this package.
package ledger
