// Package mesh is the transport that carries ISO 20022 messages between the
// institutions of this system.
//
// It exists to make the message the interface. Before it, a bank learned the
// fate of a payment by calling a function that returned it; after it, a bank
// cannot learn that a payment was accepted except by receiving a pacs.002
// saying so. Everything else here follows from taking that seriously.
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
// reaches. The measurements are in TestABankHandlerTouchesOnlyItsOwnBook, and
// one of them is a genuine crossing that this arrangement is what found.
package mesh
