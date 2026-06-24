package api

import (
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/raphi011/cbs/payment"
)

// Server holds the application state behind an atomic pointer so the whole state
// can be swapped in one step (see Reset). The state is a single payment.Network,
// the root of the whole object graph (each participant bank owns its own ledger
// and deposit register). Handlers read it via network() so a reset is observed by
// every in-flight and future request. newState builds a fresh network for the
// initial boot and for every reset.
type Server struct {
	state    atomic.Pointer[payment.Network]
	newState func() *payment.Network
	log      *slog.Logger
}

// NewServer builds a Server whose state is produced by newState. newState is
// called once now for the initial state and again on every Reset. If log is nil,
// the default slog logger is used.
func NewServer(newState func() *payment.Network, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{newState: newState, log: log}
	s.state.Store(newState())
	return s
}

// network returns the live network. Cheap, lock-free, safe for concurrent use.
func (s *Server) network() *payment.Network { return s.state.Load() }

// Reset atomically replaces the entire application state with a freshly built
// one. In-flight requests that already loaded the previous network finish against
// it consistently; new requests see the fresh state.
func (s *Server) Reset() { s.state.Store(s.newState()) }

// Routes builds the HTTP handler: an enhanced ServeMux (Go 1.22+ method+path
// patterns) wrapped in the middleware chain (CORS, logging, recover).
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	s.registerParticipantRoutes(mux)
	s.registerLedgerRoutes(mux)
	s.registerDepositRoutes(mux)
	s.registerPaymentRoutes(mux)
	s.registerAdminRoutes(mux)
	return s.withMiddleware(mux)
}

// participant resolves the {pid} path parameter to a live *payment.Participant.
// On failure it writes the appropriate error response and returns false, so
// callers can simply `return` when ok is false.
func (s *Server) participant(w http.ResponseWriter, r *http.Request) (*payment.Participant, bool) {
	pid := payment.ParticipantID(r.PathValue("pid"))
	p, err := s.network().GetParticipant(pid)
	if err != nil {
		writeError(w, err)
		return nil, false
	}
	return p, true
}
