package api

import (
	"log/slog"
	"net/http"
	"slices"
)

// Router is an http.ServeMux that remembers what was registered on it.
//
// ServeMux has no introspection: once a pattern is handed to HandleFunc it is
// unreachable. That is fine for serving and useless for proving that an API
// split across three operators kept every route exactly once, which is the one
// claim a re-home of this size has to be able to make. Recording the patterns
// on the way in costs a slice and makes that claim testable.
//
// It is what api/bank, api/csm and api/centralbank each hand back from their
// Routes function, and it is the reason none of the three has to remember the
// middleware chain: registering and serving are two calls, and Handler below is
// the second.
type Router struct {
	mux      *http.ServeMux
	patterns *[]string
}

func NewRouter() *Router {
	return &Router{mux: http.NewServeMux(), patterns: &[]string{}}
}

func (r *Router) HandleFunc(pattern string, h http.HandlerFunc) {
	*r.patterns = append(*r.patterns, pattern)
	r.mux.HandleFunc(pattern, h)
}

// Patterns returns the registered patterns, sorted, as a copy — so a caller
// cannot reorder the router's own record by sorting the result again.
func (r *Router) Patterns() []string {
	out := slices.Clone(*r.patterns)
	slices.Sort(out)
	return out
}

// Handler wraps the routes in the standard middleware chain. It is the only way
// a surface becomes something an http.Server can be given, so a surface that
// skipped the chain would have to be built out of an http.ServeMux by hand.
func (r *Router) Handler(log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return withMiddleware(log, r.mux)
}
