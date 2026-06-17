// Package deposit implements the demand-deposit (DDA) layer of the banking
// model. It sits on top of the pure general ledger in the ledger package and
// adds the operational concerns of a customer-facing checking/current account:
//
//   - Account status and lifecycle (Active, Dormant, Frozen, Closed).
//   - Overdraft limits.
//   - Authorization holds and the available balance they reduce.
//   - End-of-day balance snapshots.
//
// Each deposit account wraps a backing Liability general-ledger account:
// customer money is a liability of the bank, so the GL book balance of that
// liability account is the customer's spendable funds. The deposit layer never
// stores money itself — every movement of value is a real double-entry posting
// in the underlying ledger.Book. Holds and snapshots, by contrast, are
// operational state tracked only in this layer; they do not appear in the
// general ledger until a hold is captured into a real transaction.
//
// See README.md for a detailed explanation of the concepts modeled here.
package deposit
