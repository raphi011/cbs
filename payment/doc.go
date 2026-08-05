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
// with branches to Rejected (from Initiated or Accepted) and Returned (a SEPA
// R-transaction after settlement).
//
// # One payment, three actors
//
// The package's central design fact is that no single function accepts a
// payment any more. What used to be InitiatePaymentTx is three halves run by
// three different institutions, and which half is which depends on the
// scheme's direction:
//
//   - SubmitPaymentTx is the SUBMITTING bank's half. For a push that is the
//     debtor half — the account, the asset, the address, the funds, and the
//     debtor leg posted. For a pull the payee's bank submits, so the creditor
//     half runs and NOTHING is posted.
//   - AcceptInboundTx is the RECEIVING bank's half: the one the submitter
//     could not run, because it could not see that side. For a pull it is the
//     half that moves money, because the payer's bank is the only actor that
//     can look at the account being collected from.
//   - AcceptAtCSMTx is the CLEARING HOUSE's half, and only it makes a payment
//     Accepted: it takes a payment both banks have now looked at into the open
//     cycle for its scheme. ErrCycleNotOpen is its refusal — TM01 on the wire —
//     because the cut-off is the clearing house's and no bank refuses its own
//     customer on account of it.
//
// A rejection splits the same way, into RejectAtCSMTx and ReverseDebtorLegTx,
// and is the first operation here that can HALF-HAPPEN: between the two the
// payment is Rejected and the payer's money is still in clearing suspense.
// RejectAtCSMTx documents that seam at length. Every rule above holds for
// exactly one reason — the DEBTOR's bank posts the debtor leg — and the
// direction only decides whether that bank is the one submitting or the one
// answering.
//
// A RETURN is the same shape a third time, and it is the one where the split
// decides who may say no:
//
//   - PostReturnLegTx is a BANK posting the customer leg it owns. The clawback
//     is always the CREDITOR's bank's, out of the account its creditor leg
//     actually credited; the refund is always the DEBTOR's bank's, into the
//     payer's account. Which of the two the RETURNING bank holds is what the
//     scheme's direction decides (ReturnerOf), and that is the whole of what it
//     decides.
//   - SettleReturnTx is the SETTLEMENT AGENT's half: the reserve reversal
//     between the two members' settlement accounts, in the central bank's own
//     book, plus a statement for each. It reads no payment row, because a
//     settlement agent holds none — everything it acts on came off the pacs.004
//     (ReadReturn).
//   - ReverseReturnLegTx unwinds the leg the RETURNING bank posted, when the
//     settlement agent refuses the return it sent. It takes only a payment that
//     is still Settled, so it can never unwind half of a return that finished.
//
// The rule the ordering buys is that a BANK CAN REFUSE A LEG ONLY IF IT POSTS
// IT BEFORE IT SENDS. The returning bank posts first, so its refusal costs
// nothing and is an error to its caller rather than a message. The other bank
// posts after the reserves have moved and cannot refuse, which is why
// ParticipantAccounts.ReturnsReceivable is reached on one side and not the
// other: every participant is opened one per asset, and the bank that must
// force a posting into it is the one that first hears about the return when it
// is already final.
//
// # Schemes
//
// The Scheme interface abstracts over payment schemes. Two are implemented:
// SEPA Credit Transfer (SCT, a push payment) and SEPA Direct Debit (SDD, a
// pull payment requiring a mandate). Both are net-settled, so adding another
// net-settled scheme means only implementing Scheme and registering it — the
// orchestrator does not change.
//
// Validation is a PAIR on that interface, split by which bank can see the
// answer: Validate is the debtor bank's (the funds) and ValidateMandate is the
// creditor bank's (the mandate, which in SEPA the creditor holds). Both are on
// the interface rather than reached by a type assertion, because a scheme that
// implemented only one of them would be silently half-validated.
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
//   - Five ISO 20022 messages, and no more. pacs.008, pacs.003, pacs.002,
//     pacs.004 and pacs.009 are implemented — translate.go now renders and
//     reads all five. ReadReturn is the newest of the five readers and is now
//     wired: mesh/centralbank.go turns an arriving pacs.004 into a
//     ReturnInstruction and hands it to SettleReturnTx, which reads no payment
//     row at all, and mesh/bank.go reads the relayed copy the same way to post
//     its own customer leg. That pair is what it exists to mean — a settlement
//     agent resolving accounts from OrgnlTxRef instead of from a payment row it
//     may no longer hold. Package mesh is what carries messages between the
//     institutions as marshalled bytes, so they are parsed on arrival rather
//     than passed as structs. What is absent is
//     pain.001/pain.008 customer initiation (an instruction arrives over this
//     repository's REST API instead), the camt reporting family — including
//     camt.054 and the admi.002 a real receiver answers an unreadable file
//     with — camt.056/pacs.007 recalls and reversals, runtime XSD validation
//     (a golden-file check against the real schemas exists — `make
//     test-schemas` — but it cannot be run as the tree stands:
//     iso20022/testdata holds no xsd directory, because the schemas are ISO's
//     to redistribute rather than this repository's to vendor. So a plain `go
//     test` skips every one of its subtests, and `make test-schemas`, which
//     sets ISO20022_REQUIRE_SCHEMAS=1 to turn each skip into a failure, fails.
//     Until someone fetches the schemas, nothing here is checked against a
//     real one) and message signing. Nor is there any batching of customer
//     payments: a pacs.008 or pacs.003 built here carries exactly one
//     transaction and one arriving with several is refused, which is why
//     pacs.002's PART group status is built and never produced.
//
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
//
//   - Many assets, but no exchange between them. Accounts and schemes are
//     denominated in one of the known assets and transactions balance per
//     asset, so the multi-asset accounting is real — what is missing is
//     conversion. There are no rates, no FX trade and no position accounts,
//     so a payment whose two ends differ in asset is refused with
//     ErrAssetMismatch rather than converted. Amounts are ledger.Amount,
//     integer minor units.
//
//   - One database transaction stands in for a settlement window. Every book —
//     each participant's and the central bank's — lives in the same Store, told
//     apart by its ledger.BookID, and payment.Tx embeds deposit.Tx embeds
//     ledger.Tx, so a single transaction can reach all three layers. SettleCycle
//     moves the netted reserves inside one Store.Update, so a net payer that
//     cannot cover its position aborts the whole batch. That is the essence of a
//     real RTGS settlement window: the settlement agent holds the participants'
//     reserve accounts, checks that every payer can cover, and posts all of it
//     or none.
//     What that window no longer spans is the MEMBERS. Both legs in a member's
//     own book used to be in this unit of work: the mirror leg is the member's
//     own posting now, made from the statement SettleCycle hands back (see
//     PostSettlementAdviceTx), and the creditor leg is the payee's bank's, made
//     from the clearing house's per-payment advice (see PostCreditorLegTx). The
//     interval between the central bank's commit and a member's is the
//     unreconciled position, and it is modelled rather than hidden — see
//     SettleCycle.
//     A RETURN's window used to span the members too, and that exception is
//     gone: ReturnPaymentTx composed every institution's act inside one
//     Store.Update, and Task 16e deleted it once mesh could carry the
//     conversation instead. What is left is SettleReturnTx — the reserve
//     reversal, in the central bank's own book, reading no payment at all — with
//     each customer leg its own bank's act (PostReturnLegTx) and each reserve
//     mirror booked from a camt.053, exactly as at a cut-off.
//     What a real system adds is what happens next — queueing the batch,
//     running a liquidity-saving optimisation, unwinding the defaulter, or
//     extending intraday credit. Here the batch simply fails and can be retried
//     once the member is funded. See Network for details.
//
//     What is no longer standing in for anything is who ASKS. The window is
//     instructed: closing a cycle emits a pacs.009 carrying the net positions,
//     and the central bank decides. Its refusal is a pacs.002 — RJCT/AM04 when
//     a net payer's reserve cannot cover its position — rather than a Go error
//     handed back to whoever pressed settle, which is not something a clearing
//     house could act on. A refusal is told to NOBODY else: nothing was posted,
//     so every payment is exactly where the cut-off left it, and the failure
//     shows on the cycle, which stays Closed with no settlement against it.
//
//   - Returns settle immediately rather than through a later R-cycle.
//
// See README.md for worked examples of the SCT and SDD posting choreography.
package payment
