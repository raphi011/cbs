package api

import (
	"encoding/json"
	"net/http"
)

// WriteJSON writes v as a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// WriteError maps err to an HTTP status (see errorStatus) and writes a JSON
// body of the form {"error": "..."}.
func WriteError(w http.ResponseWriter, err error) {
	WriteJSON(w, errorStatus(err), errorBody{Error: err.Error()})
}

// WriteBadRequest reports a malformed request (bad JSON, invalid enum value,
// missing field) as a 400 with the given message.
func WriteBadRequest(w http.ResponseWriter, msg string) {
	WriteJSON(w, http.StatusBadRequest, errorBody{Error: msg})
}

type errorBody struct {
	Error string `json:"error"`
}

// DecodeJSON decodes the request body into dst, rejecting unknown fields so
// typos in client payloads surface as errors rather than being silently
// ignored. A non-nil result should be treated as a 400 by the caller.
func DecodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
