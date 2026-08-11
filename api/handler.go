package api

import (
	"fmt"
	"net/http"
	"time"
)

// The handler adapter: a handler RETURNS its response, and this is what turns
// one into something an http.ServeMux can be given.
//
// # What it buys is not brevity
//
// The five steps every route was made of — resolve the caller, decode the body,
// call the domain, map the error, encode the answer — are three of them
// identical everywhere and two of them the route's own. Writing all five out
// per handler is what let a route answer 200 with an error body, skip the error
// mapping, or write a header twice, and no test could see any of it: those are
// properties of a shape rather than of an answer. Here they are properties of
// one function.
//
// It is also why the writers in respond.go are unexported. A surface package
// cannot answer a request except through Handle, so the guarantee is enforced
// rather than trusted — the same move mesh/ops.go makes one layer down.
//
// # A nil response is 204 No Content
//
// One rule, covering both shapes that need it: a route that never has a body —
// a delete, a hold released — and a route whose body is sometimes absent, such
// as an interest charge on a facility that accrued nothing. status is what a
// response WITH a body gets; a nil one takes 204 whatever it says.

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
//
// field is named in the refusal because a request can carry two of them, and
// "invalid date" against a body holding a date and a firstDue tells the caller
// nothing about which to fix.
func ParseDay(field, raw string) (time.Time, error) {
	day, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, BadRequest("invalid %s (want YYYY-MM-DD)", field)
	}
	return day, nil
}
