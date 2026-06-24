// Package ledger implements an in-memory, double-entry general ledger.
//
// It models the pure accounting core of a banking system: ledgers,
// subledgers, accounts, multi-legged transactions, postings, on-demand
// book balances, and an immutable audit log. It deliberately knows
// nothing about demand-deposit concerns such as account status, holds,
// available balance, or end-of-day snapshots — those live in the deposit
// package, which is layered on top of this one.
//
// See README.md for a detailed explanation of the banking concepts
// modeled by this package.
package ledger
