package payment

import (
	"context"
	"fmt"
	"time"

	"github.com/raphi011/cbs/iso20022"
)

// What an institution keeps of the files it sent and received.

// A MessageDirection is which way one envelope went, read from the institution
// that recorded it. Every crossing is one send and one receive, in two
// databases.
type MessageDirection string

const (
	MessageSent     MessageDirection = "sent"
	MessageReceived MessageDirection = "received"
)

// A Message is one envelope an institution sent or received, kept as it
// travelled. It is the INSTITUTION's record and not the transport's, which the
// design record argues at length.
type Message struct {
	// Seq orders this institution's whole traffic and names one message. The
	// store allocates it, so whatever a caller sets is overwritten.
	Seq int64

	Direction    MessageDirection
	Counterparty iso20022.BIC

	// MsgDefIdr and MsgID are the envelope's own header: what the file is, and
	// the id its sender put on it.
	MsgDefIdr string
	MsgID     string

	// OrderID is the transport's handle for the crossing, and it is empty where
	// there was none. It is recorded rather than looked up: no institution reads
	// the transport's tables.
	OrderID string

	At time.Time

	// Payload is the file, as it travelled, and it is absent where the listing
	// asked for it to be left unread. PayloadSize stands in for it either way:
	// see MessageFilter.WithoutPayload.
	Payload     []byte
	PayloadSize int

	// Payments is which payments the file carried, in document order. It is the
	// join that takes a payment to the file that carried it.
	Payments []PaymentID
}

// PaymentsIn is which payments a document names, in the order it names them.
// The id on the wire is the SUBMITTING bank's and it crosses unchanged, so
// every institution's log joins on the same value.
func PaymentsIn(doc iso20022.Document) []PaymentID {
	var out []PaymentID
	add := func(id string) {
		if id != "" {
			out = append(out, PaymentID(id))
		}
	}
	switch d := doc.(type) {
	case *iso20022.Pacs008:
		for _, tx := range d.FIToFICstmrCdtTrf.CdtTrfTxInf {
			add(tx.PmtId.TxId)
		}
	case *iso20022.Pacs003:
		for _, tx := range d.FIToFICstmrDrctDbt.DrctDbtTxInf {
			add(tx.PmtId.TxId)
		}
	case *iso20022.Pacs004:
		for _, tx := range d.PmtRtr.TxInf {
			add(tx.OrgnlTxId)
		}
	case *iso20022.Pacs002:
		// A status naming no transaction is the FF01 answer to a file that would
		// not parse. And one answering a pacs.009 names the CUT-OFF rather than a
		// payment, because that is what a settlement instruction identifies — the
		// original message definition is the only thing that tells the two apart.
		orig, reports := ReadStatus(d)
		if orig.MsgDefIdr == (iso20022.Pacs009{}).MessageDefinitionIdentifier() {
			return nil
		}
		for _, r := range reports {
			add(r.TxID)
		}
	}
	// A pacs.009, a camt.050, a camt.053 and a camt.025 name NO payment, and that
	// is the domain rather than a gap: a cut-off's positions are M payments netted
	// and a statement is one account's movement.
	return out
}

// A MessageFilter narrows a message listing. The zero value is this
// institution's whole traffic, oldest first.
type MessageFilter struct {
	// Seq names ONE message, which is how the file itself is fetched once a
	// listing has named it. See Network.GetMessage.
	Seq int64

	Direction    MessageDirection
	Counterparty iso20022.BIC

	// PaymentID narrows to the files that carried one payment.
	PaymentID PaymentID

	// WithoutPayload leaves the files themselves unread. An index over a log that
	// keeps every file forever wants Message.PayloadSize and not the bytes, and
	// the mesh reads every institution's whole log.
	WithoutPayload bool

	// Before and Limit page the listing. See ledger.AuditFilter, which they are
	// modelled on: Before is a cursor over Seq, and a Limit takes the NEWEST
	// matches and still hands them back oldest first.
	Before int64
	Limit  int
}

// MessageLogTx is one institution's record of its own traffic. messages is in
// all three schemas, so nothing here can be a crossing.
type MessageLogTx interface {
	AppendMessage(ctx context.Context, m Message) error
	ListMessages(ctx context.Context, f MessageFilter) ([]Message, error)
}

// MessageLogStore is the message log at the width every institution has. It is
// CommonStore's shape one layer up, and it is here rather than in ledger
// because a message names the payments it carried.
type MessageLogStore interface {
	Update(ctx context.Context, fn func(context.Context, MessageLogTx) error) error
	View(ctx context.Context, fn func(context.Context, MessageLogTx) error) error
}

// RecordMessage records one envelope this institution sent or received.
func (s *Network) RecordMessage(ctx context.Context, m Message) error {
	return s.messages.Update(ctx, func(ctx context.Context, tx MessageLogTx) error {
		return s.RecordMessageTx(ctx, tx, m)
	})
}

// RecordMessageTx is RecordMessage inside a caller's unit of work, which is
// where an institution that is also writing payment rows about the same file
// records it.
func (s *Network) RecordMessageTx(ctx context.Context, tx MessageLogTx, m Message) error {
	if m.At.IsZero() {
		m.At = s.now()
	}
	return tx.AppendMessage(ctx, m)
}

// GetMessage is one file this institution logged, by the seq its own store
// allocated. A seq is reached from a listing rather than guessed, because it
// counts one institution's traffic and no two institutions agree on one.
func (s *Network) GetMessage(ctx context.Context, seq int64) (Message, error) {
	// A seq below the first is refused here rather than passed on, because
	// MessageFilter reads one as naming no message at all.
	if seq <= 0 {
		return Message{}, fmt.Errorf("%w: %d", ErrMessageNotFound, seq)
	}
	found, err := s.ListMessages(ctx, MessageFilter{Seq: seq})
	if err != nil {
		return Message{}, err
	}
	if len(found) == 0 {
		return Message{}, fmt.Errorf("%w: %d", ErrMessageNotFound, seq)
	}
	return found[0], nil
}

// ListMessages is this institution's own traffic, narrowed by f. There is no
// method that answers another institution's, and no schema it could read.
func (s *Network) ListMessages(ctx context.Context, f MessageFilter) ([]Message, error) {
	var out []Message
	err := s.messages.View(ctx, func(ctx context.Context, tx MessageLogTx) error {
		var err error
		out, err = tx.ListMessages(ctx, f)
		return err
	})
	return out, err
}
