// Package lending is the credit layer over a general ledger: term loans and
// revolving credit lines, their interest, their schedules and their arrears.
//
// It is the mirror image of deposit. Where a customer is an obligor under one
// Liability control account, a facility is an obligor under three — drawn
// principal, accrued interest receivable, and interest the bank owes back — and
// the Portfolio stores no money itself: disbursing posts a real GL transaction,
// and so does every accrual and every repayment. A bank's chart of accounts is
// therefore bounded by the assets it lends in, not by the number of borrowers.
//
// # Why an arranged overdraft is not here
//
// An overdrawn current account is a loan, so the obvious place for it is this
// package. It is in deposit instead, and the reason is worth understanding
// before adding a third product here.
//
// A term loan's drawn amount exists independently: €10,000 over five years is a
// fact whether or not the borrower holds a current account. So does a revolving
// line's. An arranged overdraft's does not. It IS the negative balance of the
// customer's own Liability account, viewed by sign — the same fact, read the
// other way. Giving it a facility record with a drawn amount would store a
// number that already exists, which is the drift this codebase's unified ledger
// exists without; and its Asset-side classification is available as an
// aggregation instead (deposit.Totals).
//
// Two further consequences follow from the same fact. An overdraft has no days
// past due, because arrears count from a missed due date and an overdraft has
// none — it is repayable on demand, and the sanction for exceeding the limit is
// the unarranged rate. And real core banking agrees about the packaging: an
// arranged overdraft is a current-account feature in every system that ships
// one, not a Loans product.
//
// It is also what keeps this package from importing deposit. Money moves
// against a generic ledger.Position counterparty, the way deposit.CaptureHold
// already works, so a disbursement into a customer's current account — an
// obligor under that bank's deposit control account — and one into the vault are
// the same call. A repayment that must also respect a
// deposit account's status is orchestrated one layer up, through the same Tx.
//
// # Units of work
//
// Every mutating method comes in two forms, exactly as in deposit: the plain
// form wraps one Store.Update, and the …Tx form takes a caller-supplied Tx so a
// participant can run its deposit and lending end-of-day in one transaction. A
// …Tx method must never call a plain method on the Portfolio or the Book — that
// opens a second unit of work inside the first, which the store refuses.
//
// # Thread safety
//
// All public methods on Portfolio are safe for concurrent use; the Store
// provides the isolation.
package lending
