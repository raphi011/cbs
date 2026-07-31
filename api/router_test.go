package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// TestRouterRecordsEveryPattern pins the property the surface tests rest on:
// http.ServeMux cannot be asked what is registered on it, so registration goes
// through a wrapper that remembers.
func TestRouterRecordsEveryPattern(t *testing.T) {
	r := newRouter()
	r.HandleFunc("GET /b", func(http.ResponseWriter, *http.Request) {})
	r.HandleFunc("GET /a", func(http.ResponseWriter, *http.Request) {})

	got := r.Patterns()
	if len(got) != 2 || got[0] != "GET /a" || got[1] != "GET /b" {
		t.Fatalf("Patterns() = %v, want them sorted", got)
	}
}

// TestRouteInventoryIsComplete is a golden list. It exists so that the operator
// split in the next tasks cannot silently drop a route: this is what the API
// serves today, and the three-surface test that replaces it asserts the same
// set arrives on exactly one operator each.
func TestRouteInventoryIsComplete(t *testing.T) {
	got := newServer(t, nil).RoutePatterns()
	if len(got) != 84 {
		t.Fatalf("the API serves %d routes, want 84:\n%s", len(got), strings.Join(got, "\n"))
	}
	for _, want := range []string{
		"POST /participants",
		"GET /participants/{pid}/deposit-accounts",
		"POST /cycles/{cid}/settle",
		"GET /payments",
		"GET /directory",
		"POST /admin/reset",
		"GET /assets",
	} {
		if !slices.Contains(got, want) {
			t.Errorf("route %q is missing from the inventory", want)
		}
	}
}
