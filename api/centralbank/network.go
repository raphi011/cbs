package centralbank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
)

// The mesh, and the only read on this listener that is about every institution
// at once. It is here because the operator's console is, and on the Operator
// interface because the type system should say whose act it is.

func (s *surface) registerNetworkRoutes(mux *api.Router) {
	mux.HandleFunc("GET /network/flow", api.Handle(http.StatusOK, s.handleNetworkFlow))
	// The same mesh as it happens. A watcher that reconnects reads the snapshot
	// above again, because what it missed while disconnected is not replayed.
	mux.HandleFunc("GET /network/flow/events", s.handleWatchNetwork)
}

// handleNetworkFlow is every institution, every connection between them, and
// the files that crossed one.
func (s *surface) handleNetworkFlow(r *http.Request) (api.NetworkFlowDTO, error) {
	return s.op.NetworkFlow(r.Context(), api.LogLimit(r))
}

// handleWatchNetwork is the deployment's own report, pushed as it is made
// rather than answered. It is the first route on this API that does not close.
func (s *surface) handleWatchNetwork(w http.ResponseWriter, r *http.Request) {
	events, release := s.op.Watch()
	defer release()
	api.Stream(w, r, events)
}
