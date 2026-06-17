package api

import (
	"log/slog"
	"net/http"

	"github.com/raphi011/ledger/payment"
)

// Server holds the application state — a single payment.Network, which is the
// root of the whole object graph (each participant bank owns its own ledger and
// deposit register) — and serves it over HTTP. Handlers are methods on *Server
// so they share state without package-level globals.
type Server struct {
	net *payment.Network
	log *slog.Logger
}

// NewServer builds a Server over the given network. If log is nil, the default
// slog logger is used.
func NewServer(net *payment.Network, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{net: net, log: log}
}

// Routes builds the HTTP handler: an enhanced ServeMux (Go 1.22+ method+path
// patterns) wrapped in the middleware chain (CORS, logging, recover).
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	s.registerParticipantRoutes(mux)
	s.registerLedgerRoutes(mux)
	s.registerDepositRoutes(mux)
	s.registerPaymentRoutes(mux)
	return s.withMiddleware(mux)
}

// participant resolves the {pid} path parameter to a live *payment.Participant.
// On failure it writes the appropriate error response and returns false, so
// callers can simply `return` when ok is false.
func (s *Server) participant(w http.ResponseWriter, r *http.Request) (*payment.Participant, bool) {
	pid := payment.ParticipantID(r.PathValue("pid"))
	p, err := s.net.GetParticipant(pid)
	if err != nil {
		writeError(w, err)
		return nil, false
	}
	return p, true
}
