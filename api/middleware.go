package api

import (
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// withMiddleware wraps h in the standard chain.
func withMiddleware(log *slog.Logger, h http.Handler) http.Handler {
	return recoverPanic(log, logRequests(log, cors(screenRequestTarget(h))))
}

// screenRequestTarget refuses a URL whose decoded path or query carries a
// control character.
func screenRequestTarget(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// r.URL.Path is already percent-decoded, which is the form the handlers
		// and the store see.
		if err := ledger.ValidateText("path", r.URL.Path); err != nil {
			writeBadRequest(w, err.Error())
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			writeBadRequest(w, "malformed query string")
			return
		}
		for key, values := range query {
			if err := ledger.ValidateText("query parameter name", key); err != nil {
				writeBadRequest(w, err.Error())
				return
			}
			for _, v := range values {
				if err := ledger.ValidateText(key, v); err != nil {
					writeBadRequest(w, err.Error())
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// cors allows a browser-based frontend (e.g. a React dev server on a different
// origin) to call the API. For a local learning tool it allows any origin and
// short-circuits preflight requests.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// logRequests logs the method, path, status, and duration of each request.
func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start).String(),
		)
	})
}

// recoverPanic turns a panic in a handler into a 500 response instead of
// crashing the server.
func recoverPanic(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("panic recovered", "error", rec, "path", r.URL.Path)
				writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal server error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the response status code for logging.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}
