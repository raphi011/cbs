package bank

import (
	"net/http"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/payment"
)

// The two adapters this surface registers through. They are api.Handle and
// api.HandleBody with one step in front: this listener's own bank, resolved
// before the handler runs.
//
// That resolution is the guard sixty-odd routes here begin with, and putting it
// in the adapter is what stops it being sixty-odd copies of a lookup that can
// each get the identity from somewhere else. A handler is handed the bank it
// acts as; it has no argument it could name another one with, and nothing to
// remember.

// handle registers a handler that needs this bank and no request body.
func handle[Res any](s *surface, status int, fn func(*http.Request, *payment.Bank) (Res, error)) http.HandlerFunc {
	return api.Handle(status, func(r *http.Request) (Res, error) {
		p, err := s.participant(r.Context())
		if err != nil {
			var zero Res
			return zero, err
		}
		return fn(r, p)
	})
}

// handleBody registers one that needs both.
func handleBody[Req, Res any](s *surface, status int, fn func(*http.Request, *payment.Bank, Req) (Res, error)) http.HandlerFunc {
	return api.HandleBody(status, func(r *http.Request, req Req) (Res, error) {
		p, err := s.participant(r.Context())
		if err != nil {
			var zero Res
			return zero, err
		}
		return fn(r, p, req)
	})
}
