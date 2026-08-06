package api

import (
	"context"
	"net/http"
	"time"
)

// resetTimeout bounds a detached reset. It is generous — clearing and reseeding
// a local database is well under a second — because the deadline exists to stop
// a wedged connection holding a goroutine forever, not to police the work.
const resetTimeout = 30 * time.Second

func (s *Server) registerAdminRoutes(mux *router) {
	mux.HandleFunc("POST /admin/reset", s.handleReset)
}

// handleReset clears the store and rebuilds the sample dataset. The request
// takes no body. Clearing a store is real work that can fail, so the error is
// reported rather than swallowed behind a 200.
//
// # Why the work outlives the request
//
// A reset is two steps — empty everything, then seed it again — and there is no
// state between them that anyone should ever see. Against store/mem that used
// to be free: a reset was a pointer swap, and a restart rebuilt the scenario
// anyway. Against a real database both steps are durable — including the
// ephemeral one, which outlives a request even though it does not outlive the
// process — so a request cancelled in between leaves a store that is cleared
// and not reseeded, permanently:
// seed.Populate's idempotency probe finds the participants the reseed already
// wrote and returns without finishing the job, on this call and on every one
// after it. A curl that hangs up after 80ms was enough.
//
// So the reset is detached from the request's cancellation and given a deadline
// of its own. The client may leave; the reset may not. It is the one handler in
// this API for which that is true, and it is true because it is the one handler
// whose work is not scoped to the answer it returns.
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), resetTimeout)
	defer cancel()

	if err := s.Reset(ctx); err != nil {
		s.log.Error("application state reset failed", "error", err)
		writeError(w, err)
		return
	}
	s.log.Info("application state reset")
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}
