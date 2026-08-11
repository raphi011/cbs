package ebics

import (
	"encoding/json"
	"net/http"
)

// SubscriberHeader carries who is calling.
//
// It is a claim and not a credential: anybody who can reach the listener can
// write any value into it. See the package doc for what a real subscriber holds
// instead — three key pairs and an enrolment that goes partly through the post.
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

	// Files is what a C53 or BTD download collected.
	Files []File `json:"files,omitempty"`

	// Acknowledgements is the HAC answer. It is structured where every other
	// download is opaque bytes, because HAC is about the transport rather than
	// about payments — a real one is a camt.086 file, and inventing a file
	// format for it here would be a format with one reader.
	Acknowledgements []Acknowledgement `json:"acknowledgements,omitempty"`
}

// ServeHTTP is the host's single endpoint, and single is the protocol's own
// shape: EBICS is one URL that everything POSTs to, with the order type in the
// envelope rather than in a path. It answers wherever it is mounted and reads no
// route out of the URL.
//
// A refusal is HTTP 200 carrying a return code, which is what real EBICS does
// and is worth keeping. The status says whether the host was REACHED; the return
// code says what it decided. Collapsing the two would make "the clearing house
// is down" and "the clearing house does not know you" the same answer to a
// caller, and they call for opposite things.
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

	write(w, s.answerRequest(sub, req))
}

// answerRequest is ServeHTTP without the HTTP, which is what makes the dispatch
// testable and what a second transport would call.
func (s *Server) answerRequest(sub SubscriberID, req Request) *Response {
	switch {
	case req.OrderType == HAC:
		acks, err := s.Acknowledgements(sub)
		if err != nil {
			return failed(err)
		}
		return &Response{ReturnCode: OK, Acknowledgements: acks}

	case req.OrderType.IsDownload():
		files, err := s.Download(sub, req.OrderType)
		if err != nil {
			return failed(err)
		}
		return &Response{ReturnCode: OK, Files: files}

	case req.OrderType.IsUpload():
		id, err := s.Upload(sub, req.OrderType, req.Payload)
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
