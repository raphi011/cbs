package api

import "net/http"

func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /admin/reset", s.handleReset)
}

// handleReset clears the store and rebuilds the sample dataset. The request
// takes no body. Clearing a store is real work that can fail, so the error is
// reported rather than swallowed behind a 200.
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if err := s.Reset(r.Context()); err != nil {
		s.log.Error("application state reset failed", "error", err)
		writeError(w, err)
		return
	}
	s.log.Info("application state reset")
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}
