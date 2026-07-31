package api

import (
	"net/http"
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
