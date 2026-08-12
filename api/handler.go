package api

import (
	"fmt"
	"net/http"
	"time"
)

// The handler adapter: a handler RETURNS its response, and this is what turns
// one into something an http.ServeMux can be given.

// Handle adapts a handler that returns its response into an http.HandlerFunc.
func Handle[Res any](status int, fn func(*http.Request) (Res, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := fn(r)
		respond(w, status, res, err)
	}
}

// HandleBody is Handle for a route that carries a request body. The body is
// decoded into Req first, and one that will not decode is a 400 that never
// reaches fn — so a handler is never given a half-populated request.
func HandleBody[Req, Res any](status int, fn func(*http.Request, Req) (Res, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req Req
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, err)
			return
		}
		res, err := fn(r, req)
		respond(w, status, res, err)
	}
}

func respond[Res any](w http.ResponseWriter, status int, res Res, err error) {
	switch {
	case err != nil:
		writeError(w, err)
	case any(res) == nil:
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, status, res)
	}
}

// BadRequest is the refusal a HANDLER makes rather than the domain: a body that
// will not decode, an enum naming nothing, a required query parameter that is
// absent. errorStatus maps it to 400, so a handler returns it like any other
// error and no route has to remember which of two writers a refusal takes.
func BadRequest(format string, a ...any) error {
	return badRequest{fmt.Errorf(format, a...)}
}

type badRequest struct{ err error }

func (b badRequest) Error() string { return b.err.Error() }
func (b badRequest) Unwrap() error { return b.err }

// ParseDay reads a calendar day off a request field, in the one format every
// date on this API is written in.
func ParseDay(field, raw string) (time.Time, error) {
	day, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, BadRequest("invalid %s (want YYYY-MM-DD)", field)
	}
	return day, nil
}
