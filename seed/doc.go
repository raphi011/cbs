// Package seed builds a comprehensive, internally consistent sample payment
// network for the core-banking system: four banks, customer deposit accounts in
// every lifecycle status, authorization holds, end-of-day snapshots, direct-debit
// mandates, and payments spanning the full lifecycle (settled, returned, cleared,
// accepted and rejected) across settled, closed and open clearing cycles. It also
// carries the credit layer's states: a priced overdraft actually accruing and
// capitalizing, a term loan part-way through its schedule, a revolving line with
// a billed cycle, and a loan delinquent enough to show an arrears bucket.
//
// The scenario is built on a deterministic clock so IDs and dates are
// reproducible across runs; the clock is switched to real wall-clock time before
// Populate returns, so operations performed afterwards (for example via the API
// after a reset) are timestamped live. It is the dataset the server boots from
// and resets to.
//
// It builds into a network AND through a mesh, and needs both running. The four
// banks here are provisioned — three rows apiece, in three institutions'
// databases — and then given actors, because a bank with rows and no inbox
// cannot be paid. Everything else — accounts, payments, cycles, settlements — is
// composed directly, one unit of work at a time, and deliberately so: a fixture
// is an outcome, and a conversation carried out at startup could not promise a
// fixed one.
package seed
