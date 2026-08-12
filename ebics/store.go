package ebics

import "context"

// Store is where a host keeps what it is holding: the files waiting in each
// subscriber's download queue, and the log of every order uploaded to it.
//
// # Why the transport declares this and does not import one
//
// This package carries bytes and imports nothing from the rest of this
// repository, which is what lets a clearing house hold a file addressed to a
// member bank without being able to read it. A port declared HERE keeps the
// dependency running one way: the institution hosting a queue reaches it through
// nothing but Server.
//
// # A queue is a table, and that is not the holder reading somebody else's mail
//
// The objection to storing a queue is about READERS rather than bytes: an
// institution able to query the files it carries is one query away from looking
// inside a payment it is only a hop for. What answers it is that no institution
// can write that query — the vocabulary below is subscribers, order ids, order
// types and opaque payloads. A file in a receiving bank's queue is an obligation
// the reserves have moved against, so it must outlive the process holding it.
// See docs/adr/0004, which is the ruling and the home of this argument.
//
// # One unit of work per act
//
// Every method on Server that touches these rows opens its own transaction,
// never one shared with an institution's own work: a file is put on a connection
// OUTSIDE the unit of work that decided to send it, so a rollback cannot leave a
// queue holding a file about a payment no institution recorded.
type Store interface {
	Update(ctx context.Context, fn func(context.Context, Tx) error) error
	View(ctx context.Context, fn func(context.Context, Tx) error) error
}

// Tx is one unit of work over a host's transport state.
//
// The two halves it spans are the QUEUE — files addressed to a subscriber and
// not yet collected — and the ORDER LOG, which is every order a subscriber has
// uploaded and what the host has since made of it. They are separate because
// they are emptied by different things: a queue row lives until it is collected,
// and a log row lives for ever, because HAC is an audit trail and an answer that
// disappears is not one.
//
// One counter serves both, and NextOrderSeq is it. An order id is unique at the
// host that minted it, and a host that reused one would answer a subscriber's
// HAC question about somebody else's file.
type Tx interface {
	// NextOrderSeq is the ordinal of the next order this host mints, counting
	// from zero. It is gap-free and never goes backwards, which is what makes an
	// order id unique at this host for the life of its database — a queue row is
	// deleted when it is collected, so a counter derived from the rows would hand
	// the same id out twice.
	NextOrderSeq(ctx context.Context) (int, error)

	// AddOrder appends one uploaded order to the log, as Received. seq is the
	// ordinal its id was minted from, and it is the order the host works through
	// them in.
	AddOrder(ctx context.Context, seq int, o Order) error

	// ListPendingOrders is every order that has arrived and not been answered,
	// oldest first: the hosting institution's work list.
	ListPendingOrders(ctx context.Context) ([]Order, error)

	// AnswerOrder records what the institution made of an order, and answers
	// ErrUnknownOrder for an id this host never minted.
	AnswerOrder(ctx context.Context, id OrderID, status OrderStatus, detail string) error

	// ListAcknowledgements is the log's answer about one subscriber: every order
	// it has uploaded here and what became of each, oldest first. The payloads
	// are not read — HAC says what the host knows about the ORDER.
	ListAcknowledgements(ctx context.Context, sub SubscriberID) ([]Acknowledgement, error)

	// AddQueuedFile puts one file in a subscriber's download queue. seq is the
	// ordinal of the id it was given, and files come out of a queue in it.
	AddQueuedFile(ctx context.Context, seq int, sub SubscriberID, f File) error

	// ListQueuedFiles is everything waiting for one subscriber, in the order it
	// was enqueued. WHICH of them a download takes is the protocol's question and
	// is asked above this interface; see collects.
	ListQueuedFiles(ctx context.Context, sub SubscriberID) ([]File, error)

	// DeleteQueuedFiles takes the collected files out of the queue. Ids rather
	// than a subscriber, because a C53 download collects some of what is waiting
	// and leaves the rest.
	DeleteQueuedFiles(ctx context.Context, ids []OrderID) error
}
