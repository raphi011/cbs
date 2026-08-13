package ebics

import (
	"context"
	"encoding/json"
	"net/http"
)

// SubscriberHeader carries who is calling.
const SubscriberHeader = "X-EBICS-Subscriber"

// Request is the envelope a subscriber sends. One order type, one payload, and
// the identity in the header rather than in here.
type Request struct {
	OrderType OrderType `json:"orderType"`
	Payload   []byte    `json:"payload,omitempty"`
}

// Response is the envelope the host answers with. Every request gets a return
// code; which of the other fields is filled depends on what was asked.
type Response struct {
	ReturnCode ReturnCode `json:"returnCode"`
	Detail     string     `json:"detail,omitempty"`

	// OrderID is the id an upload was given.
	OrderID OrderID `json:"orderId,omitempty"`

	// Files is what a download collected: a queue's, or the one file a published
	// type answers with.
	Files []File `json:"files,omitempty"`

	// Acknowledgements is the HAC answer.
	Acknowledgements []Acknowledgement `json:"acknowledgements,omitempty"`
}

// ServeHTTP is the host's single endpoint, and single is the protocol's own
// shape: EBICS is one URL that everything POSTs to, with the order type in the
// envelope rather than in a path.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "ebics: this endpoint takes a POST", http.StatusMethodNotAllowed)
		return
	}

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		write(w, &Response{ReturnCode: InvalidRequest, Detail: "the body is not an EBICS envelope"})
		return
	}

	sub := normaliseSubscriber(r.Header.Get(SubscriberHeader))
	if sub == "" {
		write(w, &Response{ReturnCode: InvalidRequest, Detail: SubscriberHeader + " names nobody"})
		return
	}

	write(w, s.answerRequest(r.Context(), sub, req))
}

// answerRequest is ServeHTTP without the HTTP, which is what makes the dispatch
// testable and what a second transport would call.
func (s *Server) answerRequest(ctx context.Context, sub SubscriberID, req Request) *Response {
	switch {
	case req.OrderType == HAC:
		acks, err := s.Acknowledgements(ctx, sub)
		if err != nil {
			return failed(err)
		}
		return &Response{ReturnCode: OK, Acknowledgements: acks}

	// Before the queues, because a published type is a download too and this is
	// the arm that answers it from a snapshot instead. See Server.Published.
	case req.OrderType.IsPublished():
		payload, err := s.Published(sub, req.OrderType)
		if err != nil {
			return failed(err)
		}
		return &Response{ReturnCode: OK, Files: []File{{OrderType: req.OrderType, Payload: payload}}}

	case req.OrderType.IsDownload():
		files, err := s.Download(ctx, sub, req.OrderType)
		if err != nil {
			return failed(err)
		}
		return &Response{ReturnCode: OK, Files: files}

	case req.OrderType.IsUpload():
		id, err := s.Upload(ctx, sub, req.OrderType, req.Payload)
		if err != nil {
			return failed(err)
		}
		return &Response{ReturnCode: OK, OrderID: id}

	default:
		return &Response{
			ReturnCode: UnsupportedOrderType,
			Detail:     "this host does not offer order type " + string(req.OrderType),
		}
	}
}

// failed turns a refusal into an envelope. An error that is not one is the
// host's own fault and is reported as such rather than blamed on the caller.
func failed(err error) *Response {
	var code ReturnCode = InternalError
	if c := CodeOf(err); c != "" {
		code = c
	}
	return &Response{ReturnCode: code, Detail: err.Error()}
}

func write(w http.ResponseWriter, resp *Response) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}
