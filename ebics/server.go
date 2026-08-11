package ebics

import "sync"

// Server is one institution's EBICS side: who is enrolled with it, what they
// have uploaded, and what is waiting for each of them to collect.
//
// It holds all of that in memory and none of it in a database, which is a
// deliberate absence rather than a shortcut. A queue is one institution holding
// bytes addressed to another; making it a table would put a second copy of a
// payment in the holder's own database, where every rule in the system says the
// holder must read that payment out of its own rows instead. What a restart
// costs is the files in flight at that instant, which is what a real gateway's
// operator would re-send.
//
// It is safe for concurrent use. Requests arrive on HTTP goroutines while the
// hosting institution is working through the same maps.
type Server struct {
	mu   sync.Mutex
	seq  int
	subs map[SubscriberID]*subscriber

	// orders is every order ever uploaded here, by id, and arrivals is the
	// sequence they came in. Two structures because HAC asks by id and the
	// institution works through them in order.
	orders   map[OrderID]*record
	arrivals []OrderID
}

// subscriber is one enrolled party: the files waiting for it, and the ids of the
// orders it has sent.
type subscriber struct {
	queue []File
	sent  []OrderID
}

// record is an uploaded order and what the host has since made of it.
type record struct {
	order  Order
	status OrderStatus
	detail string
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

// NewServer returns a host with nobody enrolled. Enrolment is provisioning's
// act, not a side effect of somebody dialling in.
func NewServer() *Server {
	return &Server{subs: map[SubscriberID]*subscriber{}, orders: map[OrderID]*record{}}
}

// Enrol admits a subscriber, creating its download queue. Enrolling twice is
// nothing, because provisioning runs against a deployment that may already hold
// the institution.
//
// This is the whole of the routing table. An institution addresses a file to a
// subscriber by putting it in that subscriber's queue, so there is no URL to
// look up and nothing that can fall out of step with the roster: a party with no
// enrolment has no queue, and a file addressed to it has nowhere to go.
func (s *Server) Enrol(sub SubscriberID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.subs[sub]; !ok {
		s.subs[sub] = &subscriber{}
	}
}

// Reset empties this host: every enrolment, every queue, and every order it has
// recorded. The host itself survives, which is the point of it being a method
// rather than a new Server — a listener mounted this one at startup and would go
// on serving the discarded one.
//
// It is an operator's act and not the protocol's: nothing a subscriber can send
// reaches it. What it stands in for is a gateway rebuilt from nothing, which is
// why the enrolments go too — provisioning is what creates a queue, so a reseed
// makes them again.
func (s *Server) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq = 0
	s.subs = map[SubscriberID]*subscriber{}
	s.orders = map[OrderID]*record{}
	s.arrivals = nil
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
func (s *Server) Upload(sub SubscriberID, t OrderType, payload []byte) (OrderID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.subs[sub]; !ok {
		return "", refuse(InvalidUserOrUserState, "%s is not enrolled at this host", sub)
	}
	if !t.IsUpload() {
		return "", refuse(UnsupportedOrderType, "%s is not an upload here", t)
	}

	id := s.mint()
	s.orders[id] = &record{
		order:  Order{ID: id, Subscriber: sub, Type: t, Payload: payload},
		status: Received,
	}
	s.arrivals = append(s.arrivals, id)
	s.subs[sub].sent = append(s.subs[sub].sent, id)
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
func (s *Server) Download(sub SubscriberID, t OrderType) ([]File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sb, ok := s.subs[sub]
	if !ok {
		return nil, refuse(InvalidUserOrUserState, "%s is not enrolled at this host", sub)
	}
	if t != C53 && t != BTD {
		return nil, refuse(UnsupportedOrderType, "%s is not a queue that can be collected", t)
	}

	var taken, kept []File
	for _, f := range sb.queue {
		if collects(t, f.OrderType) {
			taken = append(taken, f)
			continue
		}
		kept = append(kept, f)
	}
	if len(taken) == 0 {
		return nil, refuse(NoDownloadDataAvailable, "nothing waiting for %s", sub)
	}
	sb.queue = kept
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
func (s *Server) Acknowledgements(sub SubscriberID) ([]Acknowledgement, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sb, ok := s.subs[sub]
	if !ok {
		return nil, refuse(InvalidUserOrUserState, "%s is not enrolled at this host", sub)
	}

	acks := make([]Acknowledgement, 0, len(sb.sent))
	for _, id := range sb.sent {
		r := s.orders[id]
		acks = append(acks, Acknowledgement{
			OrderID:   id,
			OrderType: r.order.Type,
			Status:    r.status,
			Detail:    r.detail,
		})
	}
	return acks, nil
}

// Enqueue is the hosting institution addressing a file to a subscriber, and it
// is how anything ever reaches a member bank: nothing is pushed, so a file that
// is not in a queue will not be collected.
//
// It refuses a subscriber with no enrolment, because there is no queue to put
// the file in. That refusal is about reachability and not about membership — the
// institution's own judgement about whether it deals with that party at all is
// made in its own rows, before it gets here.
func (s *Server) Enqueue(sub SubscriberID, t OrderType, payload []byte) (OrderID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sb, ok := s.subs[sub]
	if !ok {
		return "", refuse(InvalidUserOrUserState, "%s is not enrolled at this host", sub)
	}
	if t == BTD || t == HAC {
		return "", refuse(UnsupportedOrderType, "%s selects files, it does not name one", t)
	}

	id := s.mint()
	sb.queue = append(sb.queue, File{OrderID: id, OrderType: t, Payload: payload})
	return id, nil
}

// Pending is every order that has arrived and not yet been answered, oldest
// first: the institution's own work list for the day.
func (s *Server) Pending() []Order {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []Order
	for _, id := range s.arrivals {
		if r := s.orders[id]; r.status == Received {
			out = append(out, r.order)
		}
	}
	return out
}

// Processed records that the institution has worked through an order. It says
// nothing about the outcome for the payments inside it — a file of a thousand
// transfers where nine hundred were rejected is still a processed order, and the
// nine hundred rejections travel back as a status report the subscriber
// collects.
func (s *Server) Processed(id OrderID, detail string) error {
	return s.answer(id, Processed, detail)
}

// Rejected records that the institution could not work through an order at all:
// a file it could not parse, or one it will not accept. This is the failure that
// has nowhere else to be recorded, because the uploader was told EBICS_OK and
// went away.
func (s *Server) Rejected(id OrderID, detail string) error {
	return s.answer(id, Rejected, detail)
}

func (s *Server) answer(id OrderID, status OrderStatus, detail string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.orders[id]
	if !ok {
		return refuse(InvalidRequest, "no order %s at this host", id)
	}
	r.status = status
	r.detail = detail
	return nil
}

// mint allocates the next order id. One counter serves every subscriber, so an
// id is unique at this host and says nothing anywhere else.
func (s *Server) mint() OrderID {
	id := mintOrderID(s.seq)
	s.seq++
	return id
}
