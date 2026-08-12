// Package seed builds a comprehensive, internally consistent sample payment
// network for the core-banking system: four banks, customer deposit accounts in
// every lifecycle status, authorization holds, end-of-day snapshots, direct-debit
// mandates, and payments spanning the lifecycle (settled, returned, accepted and
// rejected) across settled and open clearing cycles. It also
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
// cannot be paid.
//
// # Two doors, and which payment goes through which
//
// What this scenario SETTLES it composes directly, one institution's half and
// one unit of work at a time: a fixture is an outcome, and a conversation
// carried over the transport would not finish until a business day ran, so the
// dataset would depend on when somebody advanced the clock.
//
// What it leaves IN FLIGHT it submits and uploads for real, because that state
// is one only the transport can produce. A receiving bank's share of an
// instruction file is built when the clearing house takes that file in, and a
// share is what it hands over when the cycle settles — so a payment put into a
// cycle any other way would be settled by the first business day and delivered
// to nobody. The build's closing act runs the file-moving phases of a day
// itself, which keeps the outcome fixed: the payments come out Accepted in the
// open cut-off for their scheme, and the first advance settles them and pays
// their payees.
//
// Both are synchronous and complete when Populate returns. Nothing waits in a
// bank's hub and nothing waits in a download queue. See Deployment.
package seed
