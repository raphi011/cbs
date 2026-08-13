package api

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// The message log's plumbing, shared by all three surfaces because every
// institution keeps one and none of them is the same type as another.

// MessageDTO is one file an institution sent or received, WITHOUT the bytes: a
// listing is an index over a log that holds every file forever, and one
// document is fetched at a time.
type MessageDTO struct {
	Seq          int64  `json:"seq"`
	Direction    string `json:"direction"`
	Counterparty string `json:"counterparty"`
	// MsgDefIdr and MsgID are the envelope's own header: what the file is, and
	// the id its sender put on it.
	MsgDefIdr string `json:"msgDefIdr"`
	MsgID     string `json:"msgId"`
	// OrderID is the transport's handle for the crossing, absent where there was
	// none.
	OrderID string    `json:"orderId,omitempty"`
	At      time.Time `json:"at"`
	// Payments is what the file carried, in document order. A file naming none is
	// ordinary: a routing table and a statement both do.
	Payments []string `json:"payments"`
	// PayloadSize is how big the document is, which is how a listing says a file
	// is there without carrying it.
	PayloadSize int `json:"payloadSize"`
}

func ToMessageDTO(m payment.Message) MessageDTO {
	ids := make([]string, len(m.Payments))
	for i, id := range m.Payments {
		ids[i] = string(id)
	}
	return MessageDTO{
		Seq:          m.Seq,
		Direction:    string(m.Direction),
		Counterparty: string(m.Counterparty),
		MsgDefIdr:    m.MsgDefIdr,
		MsgID:        m.MsgID,
		OrderID:      m.OrderID,
		At:           m.At,
		Payments:     ids,
		PayloadSize:  m.PayloadSize,
	}
}

// MessageDocumentDTO is one message with the file as it travelled. Rendering a
// document is not validating it, and nothing on this route checks it against a
// schema.
type MessageDocumentDTO struct {
	MessageDTO
	Document string `json:"document"`
}

// MessageReader is what a message route needs of an institution, named here
// rather than taking one because all three kinds keep a log and no two of them
// are the same type.
type MessageReader interface {
	ListMessages(ctx context.Context, f payment.MessageFilter) ([]payment.Message, error)
	GetMessage(ctx context.Context, seq int64) (payment.Message, error)
}

// MessagePage answers one institution's own traffic, narrowed by the query.
// Every /messages route on every surface ends here, so all of them share the
// ordering, the DTO and the empty-page shape.
func MessagePage(r *http.Request, net MessageReader) ([]MessageDTO, error) {
	f, err := messageFilterFrom(r)
	if err != nil {
		return nil, err
	}
	messages, err := net.ListMessages(r.Context(), f)
	if err != nil {
		return nil, err
	}
	out := make([]MessageDTO, len(messages))
	for i, m := range messages {
		out[i] = ToMessageDTO(m)
	}
	return out, nil
}

// MessageDocument answers one message and the file it carried. The seq is one
// this institution's own listing named; see payment.Network.GetMessage.
func MessageDocument(r *http.Request, net MessageReader) (MessageDocumentDTO, error) {
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil || seq <= 0 {
		return MessageDocumentDTO{}, BadRequest("invalid message seq %q", r.PathValue("seq"))
	}
	m, err := net.GetMessage(r.Context(), seq)
	if err != nil {
		return MessageDocumentDTO{}, err
	}
	return MessageDocumentDTO{MessageDTO: ToMessageDTO(m), Document: string(m.Payload)}, nil
}

// messageFilterFrom parses the listing's query parameters. A direction that is
// neither half of a crossing is refused rather than quietly matching nothing.
func messageFilterFrom(r *http.Request) (payment.MessageFilter, error) {
	q := r.URL.Query()
	f := payment.MessageFilter{
		Counterparty: iso20022.BIC(q.Get("counterparty")),
		PaymentID:    payment.PaymentID(q.Get("payment")),
		Limit:        LogLimit(r),
		// A listing is an index, so it never reads the files themselves.
		WithoutPayload: true,
	}
	switch d := payment.MessageDirection(q.Get("direction")); d {
	case "", payment.MessageSent, payment.MessageReceived:
		f.Direction = d
	default:
		return f, BadRequest("invalid direction %q (want %q or %q)",
			d, payment.MessageSent, payment.MessageReceived)
	}
	if v, err := strconv.ParseInt(q.Get("before"), 10, 64); err == nil && v > 0 {
		f.Before = v
	}
	return f, nil
}
