// Package payment implements a small, in-memory interbank payment system on
// top of the ledger package. It exists to make the mechanics of payment
// clearing and settlement concrete and testable.
//
// # The model
//
// Several participant banks each keep their own ledger.Book (their general
// ledger) with a deposit.Register layered on top for their customer accounts.
// A separate central-bank ledger.Book holds a reserve account for every
// participant. Banks only meet at the central bank — which is exactly what
// makes the distinction between clearing and settlement real:
//
//   - Clearing is the exchange and netting of payment instructions. No central
//     bank money moves; banks just agree on who owes whom.
//   - Settlement is the movement of reserves between banks at the central bank.
//     This is the point of finality.
//
// A payment travels through an explicit lifecycle:
//
//	Initiated -> Accepted -> Cleared -> Settled
//
// with branches to Rejected (before clearing) and Returned (a SEPA
// R-transaction after settlement).
//
// # Schemes
//
// The Scheme interface abstracts over payment schemes. Two are implemented:
// SEPA Credit Transfer (SCT, a push payment) and SEPA Direct Debit (SDD, a
// pull payment requiring a mandate). Both are net-settled, so adding another
// net-settled scheme means only implementing Scheme and registering it — the
// orchestrator does not change.
//
// # Next work
//
// Two schemes are designed for but not yet implemented:
//
//   - Instant payments (real-time gross settlement). The Scheme already
//     exposes SettlementModel (Net/Gross); what remains is a settlement path
//     that branches on it, settling a Gross payment immediately and per-payment
//     rather than through a clearing cycle. SettleCycle currently implements
//     only the netted path, so this is the one place the orchestrator must grow.
//   - Card schemes (authorise/capture then clear). The authorisation is a
//     deposit hold and the capture becomes the debtor leg; clearing and
//     settlement reuse the existing net machinery. This slots in cleanly now
//     that holds live in the deposit layer.
//
// See README.md ("Next Work") for the details.
//
// # Deliberate simplifications
//
// This is a learning model, not a production payment processor:
//
//   - No ISO 20022 (pain.001 / pacs.008 / pacs.003) message parsing; the
//     Payment struct stands in for the instruction. Scheme docs name the
//     messages they correspond to.
//   - No IBAN or BIC validation; routing is by explicit ParticipantID.
//   - A single currency, using ledger.Amount (integer minor units).
//   - Postings across the per-bank and central-bank ledgers are NOT atomic;
//     the Network serializes whole operations under one lock. A real RTGS uses
//     a locked settlement window. See Network for details.
//   - Returns settle immediately rather than through a later R-cycle.
//
// See README.md for worked examples of the SCT and SDD posting choreography.
package payment
