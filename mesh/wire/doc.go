// Package wire is the transport underneath the mesh: a set of actors, each
// with an unbounded inbox and a goroutine reading it, and one send that every
// message in this system passes through.
//
// It carries BYTES and routes them by BIC. Nothing here parses a message,
// composes one, or knows what an institution is — which actor plays a member
// bank and which plays the settlement agent is the mesh's question, and the
// answer never reaches this package. What is left when that is taken away is
// small enough to be worth reading on its own: a queue, a goroutine, a counter,
// and the accounting that makes Drain mean something.
//
// The one thing here that is not delivery is the ACTOR CONTEXT: WithActor marks
// a context as belonging to the institution whose goroutine is running, and the
// mesh's book recorder reads it back. It is here because dispatch is the only
// place that knows whose handler is about to run.
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
// this transport tells a sender to slow down. That is the right trade for a
// fixed set of actors driving a bounded number of payments, and it is stated
// here rather than discovered later: a real network has flow control, and this
// one has an assumption.
//
// # Drain
//
// No test above this package waits for a DURATION to decide that work has
// finished, and none should: Drain blocks until no message is in flight and
// then returns, which is what lets a test say "submit, drain, assert" and mean
// it. The counter behind it is incremented before a message is enqueued and
// decremented only after the handler that consumed it has returned, so a
// message that begets a message never shows a moment of quiet in the middle of
// a chain.
//
// Drain also returns the dead letters, joined. An actor handler has nobody to
// return an error to; without this, every test that drained would pass over a
// swallowed failure, and an actor that eats errors is the worst kind of green
// test suite.
//
// # What this transport is not
//
// Delivery here is exactly-once and in order, because the transport is a queue
// inside one process. That is a property of THIS transport and not of a payment
// network. A real one loses messages, delivers them twice, and delivers them
// out of order, which is why real receivers deduplicate on BizMsgIdr and why
// real senders retry. None of that is modelled here, and a reader should not
// infer from its absence that clearing is reliable by nature.
//
// Likewise "the counterparty is down" is not expressible: a send to a live
// actor always succeeds. A BIC can fail to resolve against the actor table —
// ErrUnknownBIC, which the mesh answers as RC01 — but a timeout cannot.
//
// Nor is the network a boundary in any other sense. The actors share one
// process and one clock; there is no serialisation cost, no authentication and
// no signature. What the mesh models above this is the SHAPE of interbank
// messaging — who may know what, and when — not its infrastructure.
//
// # One thing this package does decide, and it is about addresses
//
// A BIC is claimed before the actor that will answer to it exists, because a
// joining institution's handler is bound to an identity its caller is still
// writing. Claim and Register are the two halves, and Register consumes the
// reservation in the same critical section that inserts the actor — a gap there
// would let a second claim take an address out from under a caller whose own
// work had already committed.
//
// That authority is over CONNECTIVITY and nothing else. Whether an institution
// is a member of the scheme is decided one institution over, in the clearing
// house's roster, and a bank this transport can reach perfectly well may still
// be refused every payment.
package wire
