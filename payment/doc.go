// Package payment implements a small interbank payment system on top of the
// deposit and ledger packages. It exists to make the mechanics of payment
// clearing and settlement concrete and testable.
//
// Like the layers below it, the package holds no state of its own: participants,
// payments, mandates, clearing cycles and settlements all live in a Store
// (store/mem in-process, store/pg on Postgres) behind the payment.Store and
// payment.Tx interfaces declared here.
//
// # The model
//
// Several participant banks each keep their own book of accounts (a
// ledger.Book, their general ledger) with a deposit.Register layered on top for
// their customer accounts. A separate central-bank book holds a reserve account
// for every participant. The books all live in one Store and are told apart by
// their ledger.BookID, so chart-of-accounts numbers and ID counters stay per
// bank while a single transaction can still span several of them. Banks only
// meet at the central bank — which is exactly what makes the distinction
// between clearing and settlement real:
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
//   - No identifier format validation: an IBAN's check digit, length and
//     country code go unchecked, and a participant's BIC is checked for
//     structure only — there is no directory to look it up in. Addresses
//     resolve by lookup against
//     deposit.Identifier, not by parsing — literally, with one exception the
//     readable stored form forces: an IBAN is compared with its display
//     separators removed from both sides (deposit.Identifier.MatchValue), so
//     that the SE89-AURORA-1001 this system stores and the SE89AURORA1001 a
//     pacs.008 carries are the one address they are. No other scheme is
//     normalised.
//   - A single currency, using ledger.Amount (integer minor units).
//   - One database transaction stands in for a settlement window. Every book —
//     each participant's and the central bank's — lives in the same Store, told
//     apart by its ledger.BookID, and payment.Tx embeds deposit.Tx embeds
//     ledger.Tx, so a single transaction reaches all three layers. SettleCycle
//     moves the netted reserves, mirrors them in each bank's own books and pays
//     out every creditor inside one Store.Update, so a net payer that cannot
//     cover its position aborts the whole batch. That is the essence of a real
//     RTGS settlement window: the settlement agent holds the participants'
//     accounts, checks that every payer can cover, and posts all of it or none.
//     What a real system adds is what happens next — queueing the batch,
//     running a liquidity-saving optimisation, unwinding the defaulter, or
//     extending intraday credit. Here the batch simply fails and can be retried
//     once the member is funded. See Network for details.
//   - Returns settle immediately rather than through a later R-cycle.
//
// See README.md for worked examples of the SCT and SDD posting choreography.
package payment
