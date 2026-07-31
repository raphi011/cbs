package api

import (
	"encoding/json"
	"net/http"
)

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

// writeBadRequest reports a malformed request (bad JSON, invalid enum value,
// missing field) as a 400 with the given message.
func writeBadRequest(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, errorBody{Error: msg})
}

// writeUnprocessable reports a well-formed request that the state refuses, as a
// 422 with the given message. It is the hand-written twin of the errorStatus
// arm that maps the domain's business-state sentinels — for the refusals the
// API itself makes and the domain has no error for.
func writeUnprocessable(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: msg})
}

type errorBody struct {
	Error string `json:"error"`
}

// decodeJSON decodes the request body into dst, rejecting unknown fields so
// typos in client payloads surface as errors rather than being silently
// ignored. A non-nil result should be treated as a 400 by the caller.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
