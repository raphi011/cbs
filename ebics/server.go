package ebics

import (
	"context"
	"errors"
	"sync"
)

// Server is one institution's EBICS side: who is enrolled with it, what they
// have uploaded, and what is waiting for each of them to collect.
//
// # What it holds, and where
//
// The queues and the order log are ROWS, in the hosting institution's own
// database, reached through the Store this server is built over. A file released
// into a receiving bank's queue is an obligation with reserves already moved
// against it, and a log that a restart empties answers nothing about the orders
// it has forgotten. See Store, which carries the argument.
//
// ENROLMENT is the one thing here that is memory, and it is memory because it is
// DERIVED: who may reach a host is who the clearing house's roster names, plus
// the clearing house itself at the settlement agent, and provisioning rebuilds
// every queue from those at each boot. Nothing is lost by not writing it down —
// a subscriber's rows survive whether or not the enrolment that admitted them
// does, and a party dropped from the roster stops being reachable, which is the
// answer the roster was always giving.
//
// It is safe for concurrent use. Requests arrive on HTTP goroutines while the
// hosting institution is working through the same rows; the store's transactions
// order those, and the mutex below covers only the roster's shadow.
type Server struct {
	store Store

	mu   sync.Mutex
	subs map[SubscriberID]struct{}
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
	return &Server{store: store, subs: map[SubscriberID]struct{}{}}
}

// Enrol admits a subscriber, which is what makes it reachable. Enrolling twice
// is nothing, because provisioning runs against a deployment that may already
// hold the institution.
//
// This is the whole of the routing table. An institution addresses a file to a
// subscriber by putting it in that subscriber's queue, so there is no URL to
// look up and nothing that can fall out of step with the roster: a party with no
// enrolment has no queue, and a file addressed to it has nowhere to go.
//
// It writes nothing, and creates no queue row either — a queue is the rows
// addressed to a subscriber, so an empty one is the absence of them. See Server
// on why this is the one thing here that is not stored.
func (s *Server) Enrol(sub SubscriberID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.subs[sub] = struct{}{}
}

// Reset discards every enrolment. The queues and the order log are rows and go
// with the rest of the institution's when its store is emptied, which is the
// same act one layer down.
//
// The host itself survives, which is the point of it being a method rather than
// a new Server — a listener mounted this one at startup and would go on serving
// the discarded one.
//
// It is an operator's act and not the protocol's: nothing a subscriber can send
// reaches it. What it stands in for is a gateway rebuilt from nothing, which is
// why the enrolments go — provisioning is what admits a subscriber, so a reseed
// admits them again.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.subs = map[SubscriberID]struct{}{}
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
//
// The order is in the log before this returns, and that is what the answer is
// worth. A subscriber told EBICS_OK goes away, so an order that was acknowledged
// and then forgotten is a file nobody will ever be told the fate of.
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
//
// An arriving order and a released file are the same act from the counter's
// side, which is why they share one: an id has to be unique at the host that
// minted it whatever produced it, or a HAC answer would be about somebody else's
// file.
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
//
// The files are gone from the host once this returns. Real EBICS downloads in
// three phases and the client's receipt is what marks them delivered, so a
// client that died mid-transfer collects them again; here a client that dies
// after the response is written has lost them.
//
// The read and the delete are ONE unit of work, which is what makes that the
// only way to lose one: a host that committed the delete and then failed to
// answer would be losing files it had told nobody about.
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

// Acknowledgements is the HAC answer: every order sub has uploaded here and what
// became of it, oldest first. An id it never sent is simply absent — the file
// says what the host knows, and does not refuse a question about an order it has
// never heard of.
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
//
// It refuses a subscriber with no enrolment, because there is nowhere to put the
// file. That refusal is about reachability and not about membership — the
// institution's own judgement about whether it deals with that party at all is
// made in its own rows, before it gets here.
func (s *Server) Enqueue(ctx context.Context, sub SubscriberID, t OrderType, payload []byte) (OrderID, error) {
	if !s.Enrolled(sub) {
		return "", refuse(InvalidUserOrUserState, "%s is not enrolled at this host", sub)
	}
	if t == BTD || t == HAC {
		return "", refuse(UnsupportedOrderType, "%s selects files, it does not name one", t)
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

// Processed records that the institution has worked through an order. It says
// nothing about the outcome for the payments inside it — a file of a thousand
// transfers where nine hundred were rejected is still a processed order, and the
// nine hundred rejections travel back as a status report the subscriber
// collects.
func (s *Server) Processed(ctx context.Context, id OrderID, detail string) error {
	return s.answer(ctx, id, Processed, detail)
}

// Rejected records that the institution could not work through an order at all:
// a file it could not parse, or one it will not accept. This is the failure that
// has nowhere else to be recorded, because the uploader was told EBICS_OK and
// went away.
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
//
// A subscriber cannot act on it and is not meant to: the alternative is a
// refusal blaming the sender for a file it composed correctly, which is worse
// than an honest EBICS_INTERNAL_ERROR. The cause travels in the detail, so an
// operator reading the day's report still sees what failed.
func internal(err error) error {
	if err == nil {
		return nil
	}
	if CodeOf(err) != "" {
		return err
	}
	return refuse(InternalError, "%s", err)
}
