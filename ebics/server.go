package ebics

import (
	"context"
	"errors"
	"sync"
)

// Server is one institution's EBICS side: who is enrolled with it, what they
// have uploaded, and what is waiting for each of them to collect.
type Server struct {
	store Store

	mu   sync.Mutex
	subs map[SubscriberID]struct{}

	// pubs is what this host OFFERS, by order type: one snapshot per type, the
	// same bytes for every subscriber. See Publish.
	pubs map[OrderType][]byte
}

// Order is a file a subscriber uploaded, as the hosting institution sees it.
// Payload is bytes; what they say is the institution's business and not this
// package's.
type Order struct {
	ID         OrderID
	Subscriber SubscriberID
	Type       OrderType
	Payload    []byte
}

// NewServer returns a host with nobody enrolled, holding its queues and its
// order log in store. Enrolment is provisioning's act, not a side effect of
// somebody dialling in.
func NewServer(store Store) *Server {
	return &Server{store: store, subs: map[SubscriberID]struct{}{}, pubs: map[OrderType][]byte{}}
}

// Enrol admits a subscriber, which is what makes it reachable. Enrolling twice
// is nothing, because provisioning runs against a deployment that may already
// hold the institution.
func (s *Server) Enrol(sub SubscriberID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.subs[sub] = struct{}{}
}

// Reset discards every enrolment and everything this host offers. The queues and
// the order log are rows and go with the rest of the institution's when its
// store is emptied, which is the same act one layer down.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.subs = map[SubscriberID]struct{}{}
	s.pubs = map[OrderType][]byte{}
}

// Enrolled reports whether sub can reach this host at all.
func (s *Server) Enrolled(sub SubscriberID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, ok := s.subs[sub]
	return ok
}

// Upload takes a file from a subscriber and answers with the order id it was
// given. The answer is technical: the file arrived, it is queued for this
// institution to work through, and nothing has looked inside it.
func (s *Server) Upload(ctx context.Context, sub SubscriberID, t OrderType, payload []byte) (OrderID, error) {
	if !s.Enrolled(sub) {
		return "", refuse(InvalidUserOrUserState, "%s is not enrolled at this host", sub)
	}
	if !t.IsUpload() {
		return "", refuse(UnsupportedOrderType, "%s is not an upload here", t)
	}

	return s.mint(ctx, func(ctx context.Context, tx Tx, seq int, id OrderID) error {
		return tx.AddOrder(ctx, seq, Order{ID: id, Subscriber: sub, Type: t, Payload: payload})
	})
}

// mint allocates this host's next order id and writes the row it names, in one
// unit of work.
func (s *Server) mint(ctx context.Context, write func(context.Context, Tx, int, OrderID) error) (OrderID, error) {
	var id OrderID
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		seq, err := tx.NextOrderSeq(ctx)
		if err != nil {
			return err
		}
		id = mintOrderID(seq)
		return write(ctx, tx, seq, id)
	})
	if err != nil {
		return "", internal(err)
	}
	return id, nil
}

// Download hands over what is waiting for sub and empties that much of the
// queue. C53 collects statements and BTD collects everything else, in the order
// it was enqueued.
func (s *Server) Download(ctx context.Context, sub SubscriberID, t OrderType) ([]File, error) {
	if !s.Enrolled(sub) {
		return nil, refuse(InvalidUserOrUserState, "%s is not enrolled at this host", sub)
	}
	if t != C53 && t != BTD {
		return nil, refuse(UnsupportedOrderType, "%s is not a queue that can be collected", t)
	}

	var taken []File
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		taken = nil
		waiting, err := tx.ListQueuedFiles(ctx, sub)
		if err != nil {
			return err
		}
		var ids []OrderID
		for _, f := range waiting {
			if collects(t, f.OrderType) {
				taken = append(taken, f)
				ids = append(ids, f.OrderID)
			}
		}
		if len(ids) == 0 {
			return nil
		}
		return tx.DeleteQueuedFiles(ctx, ids)
	})
	if err != nil {
		return nil, internal(err)
	}
	if len(taken) == 0 {
		return nil, refuse(NoDownloadDataAvailable, "nothing waiting for %s", sub)
	}
	return taken, nil
}

// collects reports whether a download of type t takes a file enqueued as
// enqueued. C53 is both a file's own type and the selector for it; BTD is only
// ever a selector, which is why nothing is enqueued under it.
func collects(t, enqueued OrderType) bool {
	if t == C53 {
		return enqueued == C53
	}
	return enqueued != C53
}

// Publish replaces what this host offers under t. A snapshot is not addressed to
// anybody and publishing it twice is publishing it once, which is what makes it
// a directory rather than a message.
func (s *Server) Publish(t OrderType, payload []byte) error {
	if !t.IsPublished() {
		return refuse(UnsupportedOrderType, "%s is not a type this host publishes", t)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.pubs[t] = payload
	return nil
}

// Published hands sub the snapshot this host offers under t. Nothing is emptied,
// so two subscribers collect the same file and one subscriber may collect it
// twice.
func (s *Server) Published(sub SubscriberID, t OrderType) ([]byte, error) {
	if !s.Enrolled(sub) {
		return nil, refuse(InvalidUserOrUserState, "%s is not enrolled at this host", sub)
	}
	if !t.IsPublished() {
		return nil, refuse(UnsupportedOrderType, "%s is not a type this host publishes", t)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	payload, ok := s.pubs[t]
	if !ok {
		return nil, refuse(NoDownloadDataAvailable, "this host publishes no %s", t)
	}
	return payload, nil
}

// Acknowledgements is the HAC answer: every order sub has uploaded here and
// what became of it, oldest first.
func (s *Server) Acknowledgements(ctx context.Context, sub SubscriberID) ([]Acknowledgement, error) {
	if !s.Enrolled(sub) {
		return nil, refuse(InvalidUserOrUserState, "%s is not enrolled at this host", sub)
	}

	var acks []Acknowledgement
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		acks, err = tx.ListAcknowledgements(ctx, sub)
		return err
	})
	if err != nil {
		return nil, internal(err)
	}
	return acks, nil
}

// Enqueue is the hosting institution addressing a file to a subscriber, and it
// is how anything ever reaches a member bank: nothing is pushed, so a file that
// is not in a queue will not be collected.
func (s *Server) Enqueue(ctx context.Context, sub SubscriberID, t OrderType, payload []byte) (OrderID, error) {
	if !s.Enrolled(sub) {
		return "", refuse(InvalidUserOrUserState, "%s is not enrolled at this host", sub)
	}
	if t == BTD || t == HAC {
		return "", refuse(UnsupportedOrderType, "%s selects files, it does not name one", t)
	}
	if t.IsPublished() {
		return "", refuse(UnsupportedOrderType, "%s is published to every subscriber, not addressed to one", t)
	}

	return s.mint(ctx, func(ctx context.Context, tx Tx, seq int, id OrderID) error {
		return tx.AddQueuedFile(ctx, seq, sub, File{OrderID: id, OrderType: t, Payload: payload})
	})
}

// Pending is every order that has arrived and not yet been answered, oldest
// first: the institution's own work list for the day.
func (s *Server) Pending(ctx context.Context) ([]Order, error) {
	var out []Order
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListPendingOrders(ctx)
		return err
	})
	if err != nil {
		return nil, internal(err)
	}
	return out, nil
}

// Processed records that the institution has worked through an order.
func (s *Server) Processed(ctx context.Context, id OrderID, detail string) error {
	return s.answer(ctx, id, Processed, detail)
}

// Rejected records that the institution could not work through an order at all:
// a file it could not parse, or one it will not accept.
func (s *Server) Rejected(ctx context.Context, id OrderID, detail string) error {
	return s.answer(ctx, id, Rejected, detail)
}

func (s *Server) answer(ctx context.Context, id OrderID, status OrderStatus, detail string) error {
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return tx.AnswerOrder(ctx, id, status, detail)
	})
	if errors.Is(err, ErrUnknownOrder) {
		return refuse(InvalidRequest, "no order %s at this host", id)
	}
	if err != nil {
		return internal(err)
	}
	return nil
}

// internal wraps whatever the store answered in the return code that says the
// host, and not the caller, is at fault.
func internal(err error) error {
	if err == nil {
		return nil
	}
	if CodeOf(err) != "" {
		return err
	}
	return refuse(InternalError, "%s", err)
}
