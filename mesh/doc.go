// Package mesh is the transport that carries ISO 20022 messages between the
// institutions of this system.
//
// It exists to make the message the interface. Before it, a bank learned the
// fate of a payment by calling a function that returned it; after it, no actor
// in this package is TOLD that a payment was accepted except by receiving a
// pacs.002 saying so. Everything else here follows from taking that seriously.
//
// The qualification is load-bearing and is made again where it bites (see "One
// store, likewise" below): the actors share one store, bankOps carries
// GetPayment, and bank.receiveStatus deliberately reads the payment row and
// trusts it over the message it just received. A bank here CAN look the answer
// up. What it cannot do is be told by anything other than a message.
//
// # Actors
//
// A mesh owns N+2 actors: one per member bank, one clearing house, one central
// bank. Each is a goroutine with an unbounded inbox, and only that goroutine
// ever runs that actor's handler — which is what makes a bank's own state safe
// to touch inside a handler, and what makes "which institution did this" a
// question with an answer.
//
// The banks come from the participant roster, so the mesh's shape is the
// system's shape rather than a list kept in step by hand. The two institutions
// have no store row and are named in Config.
//
// # Bytes, not structs
//
// send takes an iso20022.Envelope, marshals it, and enqueues the BYTES. Nothing
// but bytes crosses an actor boundary. If two actors exchanged a *Pacs008 the
// message format would be decoration on a function call: malformed input would
// stop being a reachable failure mode, and FF01 — the rejection a receiver
// sends when it cannot parse what it was given — would be untestable. Because
// the bytes really are parsed on arrival, it is testable, and the inbox carries
// the sender beside them so that a message whose header cannot be read can
// still be answered.
//
// # The credit transfer flow
//
// A SEPA credit transfer is four messages and three decisions, and no two of
// them are made by the same institution:
//
//	payer's bank  --pacs.008-->  clearing house  --pacs.008-->  payee's bank
//	payer's bank  <--pacs.002--  clearing house  <--pacs.002--  payee's bank
//
// Reading that clockwise:
//
//   - The PAYER'S BANK submits. Mesh.Submit runs that half synchronously, so a
//     customer whose instruction fails their own bank's checks is told then and
//     there — and then sends, after the unit of work has committed. What it does
//     not answer is what the far side thinks; that arrives later, as a message.
//   - The CLEARING HOUSE routes the instruction on, by the creditor agent named
//     in it. It reads no store to do that: a clearing house that had to look a
//     payment up to decide where to send it could not route a message about a
//     payment it does not hold, which in a real network is every message.
//   - The PAYEE'S BANK resolves the message by ADDRESS — that is what produces
//     AC01 for an account number nobody holds — checks its own half, and answers
//     ACCP or RJCT.
//   - The CLEARING HOUSE clears the answer: an acceptance goes into the open
//     cycle for its scheme, and a payment with no window open is refused with
//     TM01, because the cut-off is the clearing house's and no bank refuses its
//     own customer's instruction on account of it. Then it tells the payer's
//     bank.
//   - The PAYER'S BANK, on a rejection, gives the payer their money back.
//
// Which institution runs which half is decided entirely by whose inbox the
// message landed in. That, rather than the XML, is what the mesh is for.
//
// A rejection therefore HALF-HAPPENS for an interval: the clearing house has
// rejected the payment and the payer's money is still in their own bank's
// clearing suspense, until a second actor, in a second unit of work, reverses
// it. payment.RejectAtCSMTx documents that seam; the mesh does not hide it — a
// reversal that fails has nobody to answer, so it becomes a dead letter and
// Drain returns it.
//
// # The direct debit flow
//
// A SEPA direct debit is the same four messages and the same three decisions,
// made by the same institutions in the same order — with the two banks swapped
// and the money moving at a different point in the chain:
//
//	payee's bank  --pacs.003-->  clearing house  --pacs.003-->  payer's bank
//	payee's bank  <--pacs.002--  clearing house  <--pacs.002--  payer's bank
//
// The PAYEE'S BANK submits, because a collection is the payee asking for what it
// is owed, and its submission MOVES NOTHING: the account being collected from is
// another bank's customer's, and this bank has never seen it. What it checks is
// its own half — the payee's account — plus the MANDATE, because in SEPA the
// creditor holds the mandate. A revoked one is refused there and then, at the
// payee's own bank, as an error to the caller and not as a message; MD01 is
// therefore a code this system never REJECTS with, and a real payer's bank
// keeps mandate records of its own and can. It does reach the wire the other
// way — iso20022.ReturnReasonNoMandate is the same MD01 in the return code set,
// sent by a debtor's bank about a settled collection its customer disputes, and
// the two are separate Go types for exactly this reason.
// payment.SDD.ValidateMandate says so at the layer that owns the limit.
//
// The CLEARING HOUSE routes it to the DEBTOR's agent, the element that names the
// bank holding the payer, because a pull travels towards the money's source
// while a push travels towards its destination. That one element is the whole
// difference in this actor.
//
// The PAYER'S BANK receives it, and this is the half that moves money. It
// resolves by address exactly as a payee's bank does on a push, and then posts
// the debtor leg — because this is the first moment any actor in the system has
// been able to look at the account being collected from. AM04, an account that
// cannot cover the amount, can only ever be said here.
//
// The rule that covers both flows, and that neither one on its own would tell
// you, is that the DEBTOR's bank posts the debtor leg. Which bank that is stays
// the same; whether it is submitting or answering is what the direction decides.
//
// One consequence is worth stating because it has no push counterpart. When the
// clearing house rejects a collection the payer's bank has already accepted, the
// bank waiting for an answer and the bank holding the money are two DIFFERENT
// banks — so the rejection goes to both, and the condition for the second is
// exactly that there is money to give back. See mesh.csm's receiveStatus.
//
// # Settlement
//
// The third institution, and the only one that moves reserves. A cut-off is two
// messages between the institutions, one STATEMENT out to each member whose
// position moved, and then one status per payment out to the banks:
//
//	clearing house  --pacs.009-->  central bank
//	central bank    --camt.053-->  each member whose net position moved
//	clearing house  <--pacs.002--  central bank
//	clearing house  --pacs.002-->  the submitter and the payee's bank, per payment
//
// Those two fan-outs are what a member's half of a cut-off is made of, and
// between them nothing of it is left in the settlement agent's unit of work: the
// camt.053 carries the MIRROR leg and the per-payment ACSC carries the CREDITOR
// leg (bank.receiveStatus, payment.PostCreditorLegTx).
//
// The camt.053 is what makes the MIRROR LEG the member's own act. A bank's
// clearing suspense holds money that has left a customer and not yet settled
// between banks; the mirror leg is that suspense moving against the bank's
// reserve, and it is a posting in the bank's own ledger. The settlement agent
// used to make it, inside its own unit of work, in a book that was not its.
// Now it states the movement and the closing balance, and the member books it
// (bank.receiveStatement, payment.PostSettlementAdviceTx).
//
// A statement is not an instruction, so it is ANSWERED BY NOTHING: the central
// bank has already settled, and there is nothing left to accept or refuse. A
// member that cannot book what it was told produces a dead letter and an advice
// row stuck at Advised, which is the unreconciled position in its most visible
// form.
//
// It goes out BEFORE the answer, and since 15b.3 that ordering is load-bearing
// rather than merely tidy. The CREDITOR leg is posted from the clearing house's
// ACSC fan-out, which is derived from the answer, so sending the statement first
// puts it in the member's inbox before the ACSC is even built — a happens-before
// chain rather than a race, because a send pushes onto the target's queue
// synchronously and one goroutine pops each queue in order.
//
// It bears on a NET RECEIVER: that bank's mirror leg credits the clearing
// suspense its creditor legs then draw on. A net payer's mirror leg debits its
// suspense instead, and a member whose position nets to zero is sent no
// statement at all; in both of those the suspense was funded by its own
// customers' debtor legs. Get the order wrong for a net receiver and it pays its
// customer out of a suspense the cut-off has not yet credited — legal, because
// suspense is a Liability and the ledger does not guard those, and wrong,
// because for that interval the bank's books say it lent its own customer the
// money. Asserted by mesh's TestTheMessagesACutOffPutsOnTheWire; see
// centralBank.advise.
//
// # What an undelivered statement suppresses, and why Task 19 is scoped from here
//
// centralBank.advise returns on the FIRST send it cannot make, and the cost is
// wider than the bank it could not reach:
//
//   - that member is never advised, and has no advice row at all — not even one
//     stuck at Advised, because nothing at that bank ever ran;
//   - every member AFTER it in the statement order is never advised either;
//   - the ACSC is never sent, so the clearing house never fans the per-payment
//     statuses out, and EVERY bank in the cycle — including the ones that were
//     advised — is left holding an instruction it believes outstanding on a
//     payment the domain has already marked Settled.
//
// None of it is reachable here, for the reason "What this mesh is not" gives
// below: delivery is exactly-once and in order, and a send to a live actor
// always succeeds. It becomes reachable in any transport that can lose a
// message. The settlement is FINAL in all three cases — the reserves moved and
// the cycle is Settled — so nothing may be unsaid, and there is deliberately no
// retry here rather than untested machinery for an unreachable failure. What is
// missing is the ability to NOTICE, and that is what Task 19's reconciliation is
// for: an advice row that is absent or stuck at Advised, and a cycle Settled
// whose banks were never told, are the two shapes it has to find.
//
// The refusal moved with the leg. A net payer whose reserve cannot cover its
// position used to be refused by the LEDGER, when the mirror leg took an Asset
// account negative in the member's own book. A member's settlement account at
// the central bank is a Liability, which the ledger does not guard, so
// SettleCycleTx now checks each net payer's reserve itself and answers with the
// same AM04. That is the central bank declining to extend uncollateralised
// intraday credit, which is the decision a settlement agent exists to make.
//
// A cut-off does not arrive in an inbox. It comes in from outside the mesh the
// way a customer's instruction does, so Mesh.CloseCycle runs the clearing
// house's half synchronously and then sends, exactly as Mesh.Submit does for a
// bank's.
//
// The CLEARING HOUSE nets the batch. That moves nothing at all — CloseCycleTx
// posts nowhere, it marks each payment Cleared and writes the positions onto the
// cycle — and then it INSTRUCTS, because discharging those positions moves
// central-bank reserves and no clearing house may do that. The instruction is a
// pacs.009 and not a pacs.008 because both parties to every leg are banks. Each
// leg runs between one member and the settlement agent rather than between two
// members: netting destroys the pairing of payer to payee, so what is left is
// every bank's single claim on, or obligation to, the central bank.
//
// The CENTRAL BANK discharges them, whole or not at all. SettleCycleTx is one
// unit of work holding every member's reserve position at once — the settlement
// accounts in the central bank's OWN book, which is where that position is
// recorded on this side — and that is what a settlement window is. A net payer
// whose reserve cannot cover its position aborts the batch, answered AM04, the
// same code a debtor's bank sends about a customer's empty account, said about a
// bank instead of about a customer. Before the mesh that refusal was a Go error
// returned to whoever pressed settle, which is not something a clearing house
// can act on.
//
// What that window does NOT hold is any member's own book, and it used to. Each
// member's mirror leg and creditor legs are that member's own units of work now,
// made on advice and afterwards. That is what makes settlement FINAL rather than
// simultaneous: when this unit of work commits the reserves have moved, and a
// member that has not yet booked its half has an unreconciled position rather
// than a claim on anyone. See payment.SettleCycle.
//
// The CLEARING HOUSE fans the acceptance out, per payment, to the bank that
// submitted it AND to the payee's bank. The central bank could not: it answers
// about a CYCLE and holds no way to look a payment up, which is the shape of
// "a central bank never sees an individual payment".
//
// Two recipients because they are told it for two reasons. The submitter has an
// instruction outstanding and this closes it; the CREDITOR's bank has a leg to
// post, and posting it is what pays the payee. On a pull they are the same
// institution and there is one message. Only the creditor's bank may act —
// payment.ErrNotThisBanksPayment refuses the other, and on a push that refusal
// is the ordinary case rather than a fault.
//
// A REFUSAL is fanned out to nobody, and that asymmetry is the one thing here
// worth stating twice. Nothing was posted, so every payment is exactly where the
// cut-off left it — Cleared, with the payer's money still in its own bank's
// clearing suspense — and a bank told "rejected" would try to reverse a debtor
// leg that must not be reversed. There is nothing truthful to tell a bank,
// because nothing about its payments changed. What changed is the cycle, and the
// cycle is where the failure shows: still Closed, with no settlement against it.
//
// # The return
//
// The R-transaction: a payment that has already settled, sent back. It is the
// only flow here that starts at the bank which ANSWERED rather than the one
// that submitted, and the only one in which a message a BANK composed is
// carried past the clearing house — the document travels unchanged to the
// settlement agent with only the header replaced, as csm.relay always does.
// Task 12's settlement instruction reaches the central bank too, but that one
// is the clearing house's own message about its own cut-off.
//
//	payee's bank  --pacs.004-->  clearing house  --pacs.004-->  central bank
//	payee's bank  <--pacs.002--  clearing house  <--pacs.002--  central bank
//
// The bank that RECEIVED the original instruction asks for it, which is the
// SEPA rule book's own division: the beneficiary bank returns a credit transfer
// it cannot apply, and the debtor bank returns a collection its customer
// disputes. So it is the payee's bank on a push and the payer's on a pull —
// exactly the opposite end from the one that submitted, in both directions. Its
// half MOVES NOTHING; the message is the whole of it.
//
// The CENTRAL BANK executes it, and that is this flow's one real decision.
// ReturnPaymentTx posts three compensating transactions in a single unit of
// work — the payer refunded, the payee clawed back, and the reserve movement
// between the two banks reversed — and the third is central-bank money, which
// no member bank and no clearing house may move. Splitting them would mean a
// payer refunded against a payee who was not debited. payment/doc.go records
// the consequence: returns settle immediately in this system rather than being
// netted in a later R-cycle, so a return IS a settlement act and belongs where
// settlement does.
//
// The CLEARING HOUSE carries it, and takes it into no cycle. It is in the path
// because a member bank in this mesh addresses the clearing house and nothing
// else — the routing table lives in one actor — and the destination follows
// from the message definition rather than from anything inside the message: a
// pacs.004 names no parties at all. Carrying a return costs it no store read
// going out, and the answer coming back costs it three of the reads the
// settlement fan-out already made — the payment, its scheme, the participant —
// to address the bank that asked.
//
// A bank in this system SENDS a return and never receives one, which is the
// shared store showing through. In a real network the pacs.004 travels to the
// debtor's bank, which credits its own customer; here the settlement agent
// posts that leg too, inside the atomic three. So a pacs.004 arriving at a bank
// is a dead letter, and TestAMessageAnActorHasNoHandlerForIsADeadLetter is what
// says so.
//
// The two refusals are split by whether anyone could be TOLD. A payment that
// has not settled cannot be returned, and the returning bank refuses that
// before the message exists — because ErrInvalidStateTransition is classified
// as never reaching a counterparty, so a pacs.004 sent for one would be
// dead-lettered by the settlement agent and the operator who asked would hear
// nothing at all. Everything the settlement agent can answer, it answers: a
// message carrying more than one return, or a count that disagrees with what
// arrived, comes back RJCT to the bank that sent it. A REDELIVERED return
// reaches the settlement agent, where the payment is already Returned, and is
// dead-lettered for the same reason the bank's own guard exists.
//
// # Unbounded queues, and what that costs
//
// An actor's inbox is an unbounded slice, not a buffered channel. A fixed
// buffer between two actors that message each other is a deadlock, and in this
// system they do: the clearing house sends to a bank while that bank is sending
// to the clearing house. An unbounded queue means a send never blocks, so no
// cycle can wedge.
//
// The cost is that nothing applies backpressure. A runaway producer grows the
// slice until the process runs out of memory, and there is no point at which
// this mesh tells a sender to slow down. That is the right trade for a fixed
// set of actors driving a bounded number of payments, and it is stated here
// rather than discovered later: a real network has flow control, and this one
// has an assumption.
//
// # Drain
//
// No test in this package waits for a DURATION to decide that work has
// finished, and none should: Drain blocks until no message is in flight and
// then returns, which is what lets a test say "submit, drain, assert" and mean
// it. Durations appear as deadlines — the moment at which a wedged handler
// stops being worth waiting for — and in exactly one place as a negative:
// TestStopWaitsForARunningHandler gives Stop 50ms to NOT return. That one is
// one-sided by construction, since a Stop that failed to wait returns in
// microseconds, so it can only ever pass falsely, never fail falsely. It is
// the only assertion in the package that is not deterministic, and it is
// labelled as such where it stands. The counter behind Drain is
// incremented before a message is enqueued and decremented only after the
// handler that consumed it has returned, so a message that begets a message
// never shows a moment of quiet in the middle of a chain.
//
// Drain also returns the dead letters, joined. An actor handler has nobody to
// return an error to; without this, every test that drained would pass over a
// swallowed failure, and an actor that eats errors is the worst kind of green
// test suite.
//
// # What this mesh is not
//
// Delivery here is exactly-once and in order, because the transport is a queue
// inside one process. That is a property of THIS mesh and not of a payment
// network. A real one loses messages, delivers them twice, and delivers them
// out of order, which is why real receivers deduplicate on BizMsgIdr and why
// real senders retry. None of that is modelled here, and a reader should not
// infer from its absence that clearing is reliable by nature.
//
// Likewise "the counterparty is down" is not expressible: a send to a live
// actor always succeeds. RC01 remains reachable, because a BIC can fail to
// resolve against the routing table, but a timeout does not.
//
// Nor is the network a boundary in any other sense. The actors share one
// process, one store and one clock; there is no serialisation cost, no
// authentication, no signature, and no cut-off enforced by anything but the
// clearing cycle's own state. What the mesh models is the SHAPE of interbank
// messaging — who may know what, and when — not its infrastructure.
//
// The clock is literally one: every header this package stamps is dated from
// payment.Network.Now, the same source the payments themselves are booked from
// (see Mesh.now). A mesh with a clock of its own would be a second answer to
// what time it is, and under the frozen clock the tests run on the two would be
// a year apart — a pacs.008 dated after the payment it carries. Two banks in a
// real network do not share a clock, and every timestamp comparison between them
// is a comparison across two, which is why real cut-offs are stated in a named
// time zone and enforced with a tolerance. None of that is modelled here.
//
// One store, likewise, is not one bank's. The actors are told apart by
// ledger.BookID and nothing enforces that a handler stays in its own book, so
// the boundary is asserted rather than given: ops.go narrows what each actor may
// CALL, and the recorder in books_test.go watches which books each one actually
// reaches. The measurements are in TestWhichBooksEachBankActuallyReaches, and
// one of them is a genuine crossing that this arrangement is what found.
package mesh
