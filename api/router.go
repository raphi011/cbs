package api

import (
	"net/http"
	"slices"
)

// router is an http.ServeMux that remembers what was registered on it.
//
// ServeMux has no introspection: once a pattern is handed to HandleFunc it is
// unreachable. That is fine for serving and useless for proving that an API
// split across three operators kept every route exactly once, which is the one
// claim a re-home of this size has to be able to make. Recording the patterns
// on the way in costs a slice and makes that claim testable.
type router struct {
	mux      *http.ServeMux
	patterns *[]string
}

func newRouter() *router {
	return &router{mux: http.NewServeMux(), patterns: &[]string{}}
}

func (r *router) HandleFunc(pattern string, h http.HandlerFunc) {
	*r.patterns = append(*r.patterns, pattern)
	r.mux.HandleFunc(pattern, h)
}

// Patterns returns the registered patterns, sorted, as a copy — so a caller
// cannot reorder the router's own record by sorting the result again.
func (r *router) Patterns() []string {
	out := slices.Clone(*r.patterns)
	slices.Sort(out)
	return out
}
