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
//
// # Why the work outlives the request
//
// A reset is two steps — empty everything, then seed it again — and there is no
// state between them that anyone should ever see. Both steps are durable, so a
// request cancelled in between leaves a store that is cleared and not reseeded,
// permanently: seed.Populate's idempotency probe finds the participants the
// reseed already wrote and returns without finishing the job, on this call and
// on every one after it. A curl that hangs up after 80ms is enough.
//
// So the reset is detached from the request's cancellation and given a deadline
// of its own. The client may leave; the reset may not. It is the one handler in
// this API for which that is true, and it is true because it is the one handler
// whose work is not scoped to the answer it returns.
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
