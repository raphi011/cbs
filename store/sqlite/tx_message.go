package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/raphi011/cbs/payment"
)

// The message log: what this institution sent and received, and which payments
// each file carried.

// AppendMessage appends one envelope. Seq is allocated here, so whatever the
// caller set is overwritten: the sequence is a total order over this
// institution's whole traffic, and only the store can issue it.
func (t *tx) AppendMessage(ctx context.Context, m payment.Message) error {
	if err := t.write(); err != nil {
		return err
	}
	res, err := t.tx.ExecContext(ctx, `
		INSERT INTO messages (seq, direction, counterparty, msg_def_idr, msg_id, order_id, payload, occurred_at)
		VALUES (`+nextRowSeq("messages")+`, ?, ?, ?, ?, ?, ?, ?)`,
		string(m.Direction), string(m.Counterparty), m.MsgDefIdr, m.MsgID, m.OrderID,
		m.Payload, nullTime{m.At})
	if err != nil {
		return fmt.Errorf("sqlite: record the %s %s: %w", m.Direction, m.MsgDefIdr, err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("sqlite: record the %s %s: %w", m.Direction, m.MsgDefIdr, err)
	}
	for i, id := range m.Payments {
		if _, err := t.tx.ExecContext(ctx, `
			INSERT INTO message_payments (message_seq, position, payment_id) VALUES (?, ?, ?)`,
			seq, int64(i), string(id)); err != nil {
			return fmt.Errorf("sqlite: record %s as carried by message %d: %w", id, seq, err)
		}
	}
	return nil
}

// ListMessages returns the messages matching f in ascending Seq order, each
// carrying the payments it named.
func (t *tx) ListMessages(ctx context.Context, f payment.MessageFilter) ([]payment.Message, error) {
	where := make([]string, 0, 4)
	args := make([]any, 0, 4)
	add := func(clause string, arg any) {
		args = append(args, arg)
		where = append(where, clause)
	}
	if f.Seq > 0 {
		add("seq = ?", f.Seq)
	}
	if f.Direction != "" {
		add("direction = ?", string(f.Direction))
	}
	if f.Counterparty != "" {
		add("counterparty = ?", string(f.Counterparty))
	}
	if f.PaymentID != "" {
		// The join is a subquery rather than a JOIN so that a file carrying one
		// payment twice is still one row.
		add("seq IN (SELECT message_seq FROM message_payments WHERE payment_id = ?)", string(f.PaymentID))
	}
	if f.Before > 0 {
		add("seq < ?", f.Before)
	}

	query := "SELECT seq, direction, counterparty, msg_def_idr, msg_id, order_id, payload, occurred_at FROM messages"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	// ListAudit's paging, for its reason: a page is taken newest-first so that
	// LIMIT keeps the newest matches, and handed back oldest-first.
	descending := f.Limit > 0
	if descending {
		query += " ORDER BY seq DESC LIMIT ?"
		args = append(args, f.Limit)
	} else {
		query += " ORDER BY seq"
	}

	rows, err := t.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list messages: %w", err)
	}
	defer rows.Close()

	out := make([]payment.Message, 0)
	for rows.Next() {
		var (
			m          payment.Message
			occurredAt nullTime
		)
		if err := rows.Scan(&m.Seq, &m.Direction, &m.Counterparty, &m.MsgDefIdr, &m.MsgID,
			&m.OrderID, &m.Payload, &occurredAt); err != nil {
			return nil, fmt.Errorf("sqlite: list messages: %w", err)
		}
		m.At = occurredAt.Time
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list messages: %w", err)
	}
	if descending {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	for i := range out {
		if out[i].Payments, err = t.listMessagePayments(ctx, out[i].Seq); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// listMessagePayments is which payments one message carried, in document order.
func (t *tx) listMessagePayments(ctx context.Context, seq int64) ([]payment.PaymentID, error) {
	rows, err := t.tx.QueryContext(ctx,
		"SELECT payment_id FROM message_payments WHERE message_seq = ? ORDER BY position", seq)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list what message %d carried: %w", seq, err)
	}
	defer rows.Close()

	var out []payment.PaymentID
	for rows.Next() {
		var id payment.PaymentID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("sqlite: list what message %d carried: %w", seq, err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
