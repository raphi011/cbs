// Package payment implements a small interbank payment system on top of the
// deposit and ledger packages. It exists to make the mechanics of payment
// clearing and settlement concrete and testable.
//
// Like the layers below it, the package holds no state of its own: banks, the
// two rows the other institutions keep about them, payments, mandates, clearing
// cycles and settlements all live in a Store behind the payment.Store and
// payment.Tx interfaces declared here.
//
// There is no such thing as THE store. There is one database per institution —
// N member banks, the clearing house, the settlement agent — and Stores is the
// set of them. A Network is one institution's handle on one of them, so which
// rows an act can even see is decided by which institution is performing it,
// and a method reaching for a table its institution's schema does not create is
// refused by name. See Identity, Networks and Stores.
//
// # The model
//
// Several participant banks each keep their own book of accounts (a
// ledger.Book) with a deposit.Register layered on top for their customer
// accounts. A separate central-bank book holds a reserve account per asset for
// every bank the scheme has ADMITTED — the central bank opens each one itself,
// when it answers that bank's application, so a founded bank has none. Each
// book is in its OWN database, so chart-of-accounts numbers, ID counters and
// idempotency keys are per bank because they are per database, and no
// transaction can span two of them: a unit of work is one institution's, and
// two institutions acting on one event are two units of work with a message
// between them. Banks only meet at the central bank — which is what makes the
// distinction between clearing and settlement real:
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
// No single function accepts a payment. Acceptance is three halves run by three
// different institutions, and which half is which depends on the scheme's
// direction:
//
//   - SubmitPaymentTx is the SUBMITTING bank's half. For a push that is the
//     debtor half — the account, the asset, the address, the funds, and the
//     debtor leg posted. For a pull the payee's bank submits, so the creditor
//     half runs and NOTHING is posted.
//   - AcceptInboundTx is the RECEIVING bank's half: the one the submitter
//     could not run, because it could not see that side. For a pull it is the
//     half that moves money, because the payer's bank is the only actor that
//     can look at the account being collected from.
//   - AcceptAtCSMTx is the CLEARING HOUSE's half, and only it makes the
//     CLEARING HOUSE's copy Accepted: it takes a payment both banks have now
//     looked at into the open cycle for its scheme. ErrCycleNotOpen is its
//     refusal — TM01 on the wire — because the cut-off is the clearing house's
//     and no bank refuses its own customer on account of it.
//
// A rejection splits the same way, into RejectAtCSMTx and ReverseDebtorLegTx,
// and is the one operation here that can HALF-HAPPEN: between the two the
// payment is Rejected and the payer's money is still in clearing suspense.
// RejectAtCSMTx documents that seam. Every rule above holds for one reason —
// the DEBTOR's bank posts the debtor leg — and the direction only decides
// whether that bank is the one submitting or the one answering.
//
// # Three acts a bank does with what it is TOLD
//
// One payment is three rows in three databases, so a bank's copy never advances
// because somebody else wrote on it. Every step of its life at a member bank is
// that bank's own act, and there are exactly three:
//
//   - AcceptAtBankTx records that the clearing house took the payment into a
//     cycle. It posts NOTHING — no money moves when a payment is accepted. What
//     it does is REMEMBER: a copy that jumped from instructed to settled could
//     not answer the question a customer asks in between, and would leave this
//     bank's audit log with a hole in the middle of its own payment's life.
//   - RejectAtBankTx records the rejection and reverses the debtor leg, and the
//     two commit together: a bank that cannot give the money back does not
//     record the rejection either.
//   - SettleAtBankTx records that the payment settled, and — only if this bank
//     holds the payee — releases the money out of its clearing suspense into
//     that customer's account. The status is the unconditional half and the
//     posting is the conditional one.
//
// Which bank reaches which status when is not uniform and is not a defect. The
// ACCP goes to the bank waiting for the answer to its instruction and to no
// other, so the bank that ANSWERED the instruction is never told and its copy
// stays Initiated until settlement — which is why Initiated -> Settled is a
// legal edge at a bank and not at the clearing house. Neither bank is wrong;
// they were told different things because they asked different things.
//
// A RETURN is the same shape again, and it is the one where the split decides
// who may say no:
//
//   - PostReturnLegTx is a BANK posting the customer leg it owns. The clawback
//     is always the CREDITOR's bank's, out of the account its creditor leg
//     actually credited; the refund is always the DEBTOR's bank's, into the
//     payer's account. Which of the two the RETURNING bank holds is what the
//     scheme's direction decides (ReturnerOf).
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
// BankAccounts.ReturnsReceivable is reached on one side and not the other.
//
// # And one act on its own books, driven by no message at all
//
// Network.Reconcile is a bank holding its own ledger against the camt.053
// statements it was sent, out of one database — its own. It is the only act here
// nobody asks a bank to perform, and it reports two kinds of finding: a BREAK,
// which is something this bank's books say cannot be true, and a POSITION, which
// is money in flight with how long it has been. Only the first is a defect. The
// reserve side closes into an identity and the clearing suspense cannot, because
// the mirror leg is netted; see reconcile.go and ageing.go, which carry the whole
// argument.
//
// It is one word from payment/recon and answers a different question. That
// package opens every institution's database at once, precisely because no
// institution may, which is why its reader is a test harness and this is a
// method.
//
// # One admission, three institutions
//
// The same split about a bank rather than a payment. The bank's own record, the
// central bank's settlement account and the clearing house's routing entry are
// three rows with one writer each — Bank, SettlementMember, RosterEntry — so no
// institution resolves an account off another institution's row. Four acts
// follow, each one institution's unit of work:
//
//   - FoundBankTx is the BANK building itself — its book, its chart of
//     accounts, its internal accounts per asset, its default deposit product.
//     It comes out Founded, which is a bank with a licence and no place in a
//     scheme: it can open customer accounts and take cash in, which lands in
//     its own vault, and it cannot LODGE that cash — putting it on reserve
//     needs the central bank to credit an account in the central bank's book.
//   - OpenSettlementAccountTx is the SETTLEMENT AGENT opening one account, in
//     one asset, in its own book, and recording that it holds it. Idempotent
//     per (BIC, asset), because one request asks for one currency and a
//     re-driven admission must not be given a second account.
//   - AdmitMemberTx is the CLEARING HOUSE writing where to send a message
//     addressed to this member — from the settlement agent's acknowledgement
//     rather than the bank's own word, because scheme membership follows the
//     settlement account.
//   - RecordMembershipTx is the BANK's second act: writing down the account
//     numbers it has been told, and becoming a Member.
//
// Nothing in this package composes them. What runs them in order is package
// provision, which stands outside the domain and calls each institution's act
// against that institution's own network. Nothing is carried between them and
// nothing puts them on a wire.
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
//   - Instant payments (real-time gross settlement). Scheme already exposes
//     SettlementModel (Net/Gross); what remains is a settlement path that
//     branches on it, settling a Gross payment immediately and per-payment
//     rather than through a clearing cycle. SettleCycle implements only the
//     netted path, so this is the one place the orchestrator must grow.
//   - Card schemes (authorise/capture then clear). The authorisation is a
//     deposit hold and the capture becomes the debtor leg; clearing and
//     settlement reuse the existing net machinery.
//
// See README.md ("Next Work") for the details.
//
// # Deliberate simplifications
//
// This is a learning model, not a production payment processor:
//
//   - Only the message definitions this system's flows need, and translate.go
//     renders and reads every one of them: the payment family (pacs.008,
//     pacs.003, pacs.002, pacs.004, pacs.009), the statement a settlement agent
//     sends its members (camt.053), and the pair a member lodges reserves with
//     (camt.050, camt.025). Package mesh carries them between institutions as
//     marshalled bytes, so they are parsed on arrival rather than passed as
//     structs. Admitting a bank travels on none of them — it is four direct
//     acts, and package provision is what runs them.
//
//     Absent: pain.001/pain.008 customer initiation (an instruction arrives
//     over this repository's REST API instead), the rest of the camt reporting
//     family — including camt.054 and the admi.002 a real receiver answers an
//     unreadable file with — camt.056/pacs.007 recalls and reversals, runtime
//     XSD validation, and message signing. There IS a golden-file check against
//     the real schemas (`make test-schemas`), but the schemas themselves are
//     gitignored as ISO's to redistribute rather than this repository's to
//     vendor, so it passes only where somebody has fetched them into
//     iso20022/testdata/xsd and no document is validated at RUNTIME anywhere.
//
//     Nor is there any batching of customer payments: a pacs.008 or pacs.003
//     built here carries exactly one transaction and one arriving with several
//     is refused, which is why pacs.002's PART group status is built and never
//     produced.
//
//   - An address is checked for STRUCTURE and never for existence. An IBAN's
//     length, country structure and both kinds of check digit are enforced
//     (package iban), so a mistyped address is refused before any lookup — but
//     a well-formed one belonging to nobody is refused only by the register that
//     finds no account, and only at the bank that holds the register. A
//     participant's BIC is checked for structure alone; there is no directory to
//     look one up in, which is why an instruction still carries the
//     counterparty's agent beside the address it could be derived from.
//
//     Addresses resolve by lookup against deposit.Identifier, and the one
//     normalisation is the one a person forces: an IBAN typed off a statement is
//     grouped in fours and may be lower-cased, so both sides are compacted
//     before comparison (deposit.Identifier.MatchValue). What is stored and what
//     a pacs.008 carries are already one string. No other scheme is normalised.
//
//   - Many assets, but no exchange between them. Accounts and schemes are
//     denominated in one of the known assets and transactions balance per
//     asset, so the multi-asset accounting is real — what is missing is
//     conversion. There are no rates, no FX trade and no position accounts, so
//     a payment whose two ends differ in asset is refused with ErrAssetMismatch
//     rather than converted. Amounts are ledger.Amount, integer minor units.
//
//   - One database transaction stands in for a settlement window, and it is ONE
//     institution's — the settlement agent's. SettleCycle moves the netted
//     reserves inside one Store.Update, so a net payer that cannot cover its
//     position aborts the whole batch. That is the essence of a real RTGS
//     window: the settlement agent holds the participants' reserve accounts,
//     checks that every payer can cover, and posts all of it or none.
//
//     The window does not span the MEMBERS. A member's mirror leg is its own
//     posting, made from the statement SettleCycle hands back (see
//     PostSettlementAdviceTx), and the creditor leg is the payee's bank's, made
//     from the clearing house's per-payment advice (see SettleAtBankTx). The
//     interval between the central bank's commit and a member's is the
//     unreconciled position, and it is modelled rather than hidden — see
//     SettleCycle. A return works the same way: SettleReturnTx is the reserve
//     reversal alone, each customer leg is its own bank's act
//     (PostReturnLegTx), and each reserve mirror is booked from a camt.053.
//
//     What a real system adds is what happens next — queueing the batch,
//     running a liquidity-saving optimisation, unwinding the defaulter, or
//     extending intraday credit. Here the batch simply fails and can be retried
//     once the member has LODGED, which is two acts and not one: cash paid in
//     reaches the bank's own vault, and putting it on reserve is a camt.050 to
//     the central bank (see LodgeReservesTx). A deposit alone does not unstick
//     a refused settlement, because settlement reads the central bank's book
//     and a deposit never touches it.
//
//     Who ASKS is not stood in for either. The window is instructed: closing a
//     cycle emits a pacs.009 carrying the net positions, and the central bank
//     decides. Its refusal is a pacs.002 — RJCT/AM04 when a net payer's reserve
//     cannot cover its position — rather than a Go error handed back to whoever
//     pressed settle. A refusal is told to NOBODY else: nothing was posted, so
//     every payment is where the cut-off left it, and the cycle stays Closed
//     with no settlement against it.
//
//   - Returns settle immediately rather than through a later R-cycle.
//
// See README.md for worked examples of the SCT and SDD posting choreography.
package payment
