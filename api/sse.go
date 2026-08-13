package api

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// The push channel. Every other route here answers a request and closes; this
// one stays open and writes what happens next.

// A StreamEvent is one thing that happened, pushed to a watcher. Name selects
// the shape of Data and is what a browser adds a listener for.
type StreamEvent struct {
	Name string
	Data any
}

// Stream writes events until the watcher goes away or the channel closes. A
// closed channel is a watcher that fell too far behind, and the reconnect it
// provokes is how that watcher gets a whole picture again.
func Stream(w http.ResponseWriter, r *http.Request, events <-chan StreamEvent) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Buffering an event stream buffers away the whole point of it.
	h.Set("X-Accel-Buffering", "no")

	rc := http.NewResponseController(w)
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-events:
			if !open {
				return
			}
			data, err := json.Marshal(e.Data)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Name, data); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}
