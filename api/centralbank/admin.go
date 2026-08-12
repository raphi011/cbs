package centralbank

import (
	"context"
	"net/http"
	"time"

	"github.com/raphi011/cbs/api"
)

// resetTimeout bounds a detached reset. It is generous — clearing and reseeding
// a local database is well under a second — because the deadline exists to stop
// a wedged connection holding a goroutine forever, not to police the work.
const resetTimeout = 30 * time.Second

func (s *surface) registerAdminRoutes(mux *api.Router) {
	mux.HandleFunc("POST /admin/reset", api.Handle(http.StatusOK, s.handleReset))
}

// handleReset clears the store and rebuilds the sample dataset. The request
// takes no body. Clearing a store is real work that can fail, so the error is
// reported rather than swallowed behind a 200.
func (s *surface) handleReset(r *http.Request) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), resetTimeout)
	defer cancel()

	if err := s.inst.Reset(ctx); err != nil {
		s.inst.Log().Error("application state reset failed", "error", err)
		return nil, err
	}
	s.inst.Log().Info("application state reset")
	return map[string]string{"status": "reset"}, nil
}
