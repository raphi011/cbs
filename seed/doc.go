// Package seed builds a comprehensive, internally consistent sample payment
// network for the core-banking system: four banks, customer deposit accounts in
// every lifecycle status, authorization holds, end-of-day snapshots, direct-debit
// mandates, and payments spanning the full lifecycle (settled, returned, cleared,
// accepted and rejected) across settled, closed and open clearing cycles. It also
// carries the credit layer's states: a priced overdraft actually accruing and
// capitalizing, a term loan part-way through its schedule, a revolving line with
// a billed cycle, and a loan delinquent enough to show an arrears bucket.
//
// The scenario is built on the DEPLOYMENT's clock so IDs and dates are
// reproducible across runs: rewound to BaseDate, advanced a day at a time, and
// left wherever the timeline ended, which is the business date the deployment
// goes on from. Nothing here switches to wall-clock time — a business date is
// advanced by an operator, and the two dates on one screen would be a year
// apart. It is the dataset the server boots from and resets to.
//
// A day is the only step it takes, so everything written on one date carries the
// same instant and its order is the order it was written in. That is
// calendar.Clock's own property rather than this package's convenience.
//
// It builds into a network AND through a deployment, and needs both. The banks
// here are provisioned — three rows apiece, in three institutions' databases —
// and then given their place, because a bank with rows and no download queue
// cannot be paid. Everything else — accounts, payments, cycles, settlements — is
// composed directly, one unit of work at a time, and deliberately so: a fixture
// is an outcome, and a conversation carried over the transport would not finish
// until a business day ran, so the dataset would depend on when somebody
// advanced the clock.
//
// It uploads no file at all, and that is what makes the three acts it asks a
// deployment for synchronous: an admission, a directory pull and the settlement
// agent's address. See Deployment.
package seed
