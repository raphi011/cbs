package api

import (
	"encoding/json"
	"net/http"
)

// The writers, and they are unexported on purpose.

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// writeError maps err to an HTTP status (see errorStatus) and writes a JSON
// body of the form {"error": "..."}.
func writeError(w http.ResponseWriter, err error) {
	writeJSON(w, errorStatus(err), errorBody{Error: err.Error()})
}

// writeBadRequest reports a malformed request as a 400 with the given message.
// It is what the middleware uses, which is the one place that refuses a request
// before any handler is chosen; a handler returns BadRequest instead.
func writeBadRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, errorBody{Error: msg})
}

type errorBody struct {
	Error string `json:"error"`
}

// decodeJSON decodes the request body into dst, rejecting unknown fields so
// typos in client payloads surface as errors rather than being silently
// ignored. The failure is a BadRequest, so HandleBody returns it like any other.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return BadRequest("%s", err)
	}
	return nil
}
