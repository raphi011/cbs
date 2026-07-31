package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	"github.com/raphi011/cbs/payment"
)

// Server holds the application state: one payment.Network, the root of the
// whole object graph (each participant bank owns its own book of accounts and a
// deposit register over it).
//
// The Network is a fixed handle rather than a swappable pointer, because it no
// longer holds any data — every entity lives in the Store behind it. That is
// also what turns Reset from a pointer swap into a store operation, which is
// the only form of it that clears a database.
type Server struct {
	net *payment.Network

	// boundPID is the participant this listener belongs to, empty on the
	// central bank's and the clearing house's.
	//
	// It is what replaces the {pid} path segment: a bank's port already names
	// the bank, so a bank's routes have nowhere to put another bank's id. The
	// value lives on a shallow copy of the Server (see forBank) rather than in
	// the request context, which is what leaves all 58 bank handlers untouched
	// — they call s.participant(w, r) exactly as before.
	boundPID payment.ParticipantID

	// populate rebuilds the sample dataset. It must be idempotent: the process
	// calls it at boot and Reset calls it again after clearing the store.
	populate func(context.Context, *payment.Network) error

	// resetMu serializes Reset. See the method for why one unit of work cannot
	// do the job instead.
	resetMu sync.Mutex

	log *slog.Logger
}

// NewServer builds a Server over an existing network.
//
// populate is the sample-dataset builder, called again on every Reset; pass nil
// for a server that resets to an empty system. If log is nil, the default slog
// logger is used.
//
// NewServer performs no I/O — the caller populates the network before serving —
// so a store that is unavailable fails where it can be reported rather than
// inside a constructor with no error to return.
func NewServer(net *payment.Network, populate func(context.Context, *payment.Network) error, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	if populate == nil {
		populate = func(context.Context, *payment.Network) error { return nil }
	}
	return &Server{net: net, populate: populate, log: log}
}

// network returns the live network. Cheap, lock-free, safe for concurrent use.
func (s *Server) network() *payment.Network { return s.net }

// Reset discards all persisted state and rebuilds the sample dataset. It must
// go through the store: swapping an in-memory object graph would leave every
// row in the database intact and still report success.
//
// Resets serialize. They are the one operation in this system that is NOT a
// single unit of work and cannot be made into one: the clear is a TRUNCATE
// outside any transaction, and the rebuild is dozens of separate units of work,
// because the seed builder drives the ordinary public API. Two overlapping
// resets therefore interleave — the second clears over the first's half-built
// scenario and the first finishes writing on top of the second's — leaving
// several copies of some entities and none of others. Eight concurrent resets
// produced twelve participants where there should have been four.
//
// A mutex is the honest fix for that shape: the operation is rare, bounded, and
// idempotent, so a second caller waiting for the first and then redoing the work
// is exactly the right answer. Note this makes resets exclusive within ONE
// process; two servers sharing a database could still race, which is a property
// of a teaching tool that does not pretend to be an HA deployment.
func (s *Server) Reset(ctx context.Context) error {
	s.resetMu.Lock()
	defer s.resetMu.Unlock()

	if err := s.net.Store().Reset(ctx); err != nil {
		return err
	}
	return s.populate(ctx, s.net)
}

// Routes builds the HTTP handler: an enhanced ServeMux (Go 1.22+ method+path
// patterns) wrapped in the middleware chain (CORS, logging, recover).
func (s *Server) Routes() http.Handler {
	return s.withMiddleware(s.routes().mux)
}

// RoutePatterns is every pattern Routes registers, sorted. It exists for the
// tests that hold the operator split honest; nothing in serving uses it.
func (s *Server) RoutePatterns() []string { return s.routes().Patterns() }

func (s *Server) routes() *router {
	mux := newRouter()
	s.registerParticipantRoutes(mux)
	s.registerLedgerRoutes(mux)
	s.registerDepositRoutes(mux)
	s.registerProductRoutes(mux)
	s.registerLendingRoutes(mux)
	s.registerPaymentRoutes(mux)
	s.registerDirectoryRoutes(mux)
	s.registerAuditRoutes(mux)
	s.registerAdminRoutes(mux)
	return mux
}

// forBank returns a view of this Server bound to one participant. The copy is
// shallow on purpose: every listener shares the one Network, the one populate
// func and the one log.
//
// It is built field by field rather than as *s because a sync.Mutex copied by
// value is a second, independent lock. resetMu therefore stays behind, on the
// original Server — which is the one the central bank's listener uses, and the
// central bank is the only operator with a reset route. So there is exactly one
// resetMu in the process, guarding the only surface that can reach it.
func (s *Server) forBank(pid payment.ParticipantID) *Server {
	return &Server{
		net:      s.net,
		boundPID: pid,
		populate: s.populate,
		log:      s.log,
	}
}

// participant resolves the listener's own participant. On failure it writes the
// appropriate error response and returns false, so callers can simply `return`
// when ok is false.
//
// Transitional: until the switch-over an unbound Server falls back to the {pid}
// path parameter, so the combined Routes() keeps working alongside the three
// operator surfaces.
func (s *Server) participant(w http.ResponseWriter, r *http.Request) (*payment.Participant, bool) {
	pid := s.boundPID
	if pid == "" {
		pid = payment.ParticipantID(r.PathValue("pid"))
	}
	p, err := s.network().GetParticipant(r.Context(), pid)
	if err != nil {
		writeError(w, err)
		return nil, false
	}
	return p, true
}
