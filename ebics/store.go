package ebics

import "context"

// Store is where a host keeps what it is holding: the files waiting in each
// subscriber's download queue, and the log of every order uploaded to it.
type Store interface {
	Update(ctx context.Context, fn func(context.Context, Tx) error) error
	View(ctx context.Context, fn func(context.Context, Tx) error) error
}

// Tx is one unit of work over a host's transport state.
type Tx interface {
	// NextOrderSeq is the ordinal of the next order this host mints, counting from
	// zero.
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
