package api

import (
	"log/slog"
	"net/http"
	"slices"
)

// Router is an http.ServeMux that remembers what was registered on it.
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
