package api

import (
	"net/http"
	"slices"
	"strings"
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
	// stripPrefix is removed from the front of every path registered through
	// this view. See (*router).strip.
	stripPrefix string
}

func newRouter() *router {
	return &router{mux: http.NewServeMux(), patterns: &[]string{}}
}

// strip returns a view of this router that removes prefix from the path of
// every pattern registered through it, sharing the underlying mux and record.
//
// Transitional. The four participant-scoped register functions still name
// /participants/{pid}/… because the combined Routes() still serves those paths;
// a bank's own listener has already answered "which bank?" with its port, so it
// registers the same handlers with the segment removed. The next change deletes
// Routes(), rewrites those patterns at their source, and removes this method —
// a prefix that a router silently removes is not something to keep.
func (r *router) strip(prefix string) *router {
	return &router{mux: r.mux, patterns: r.patterns, stripPrefix: prefix}
}

func (r *router) HandleFunc(pattern string, h http.HandlerFunc) {
	if r.stripPrefix != "" {
		method, path, ok := strings.Cut(pattern, " ")
		if !ok {
			panic("router: pattern without a method: " + pattern)
		}
		trimmed := strings.TrimPrefix(path, r.stripPrefix)
		if trimmed == path {
			panic("router: pattern does not carry the stripped prefix: " + pattern)
		}
		pattern = method + " " + trimmed
	}
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
