package sqlite

import (
	"context"
	"fmt"

	"github.com/raphi011/cbs/ebics"
)

// The file transport's two tables: the download queues and the order log.

// One more adapter for the reason the others exist — Go allows one Update
// method per type and each Store interface declares a different callback — and
// the callback is handed the very same *tx, so nothing about the mechanism
// differs.
type ebicsStore struct{ *store }

var _ ebics.Store = ebicsStore{}

func (e ebicsStore) Update(ctx context.Context, fn func(context.Context, ebics.Tx) error) error {
	return e.store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (e ebicsStore) View(ctx context.Context, fn func(context.Context, ebics.Tx) error) error {
	return e.store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

// NextOrderSeq allocates the next order ordinal at this host.
func (t *tx) NextOrderSeq(ctx context.Context) (int, error) {
	n, err := t.nextSeq(ctx, t.store.book, "ebics_order")
	if err != nil {
		return 0, err
	}
	return int(n) - 1, nil
}

// AddOrder appends one uploaded order to the log, as received.
func (t *tx) AddOrder(ctx context.Context, seq int, o ebics.Order) error {
	if err := t.write(); err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx, `
		INSERT INTO ebics_orders (order_id, subscriber, order_type, payload, status, detail, seq)
		VALUES (?, ?, ?, ?, ?, '', ?)`,
		string(o.ID), string(o.Subscriber), string(o.Type), o.Payload, string(ebics.Received), int64(seq)); err != nil {
		return fmt.Errorf("sqlite: record order %s from %s: %w", o.ID, o.Subscriber, err)
	}
	return nil
}

// ListPendingOrders is every order that has arrived and not been answered,
// oldest first.
func (t *tx) ListPendingOrders(ctx context.Context) ([]ebics.Order, error) {
	rows, err := t.tx.QueryContext(ctx, `
		SELECT order_id, subscriber, order_type, payload FROM ebics_orders
		WHERE status = ? ORDER BY seq`, string(ebics.Received))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list the orders waiting to be worked through: %w", err)
	}
	defer rows.Close()

	var out []ebics.Order
	for rows.Next() {
		var o ebics.Order
		if err := rows.Scan(&o.ID, &o.Subscriber, &o.Type, &o.Payload); err != nil {
			return nil, fmt.Errorf("sqlite: list the orders waiting to be worked through: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// AnswerOrder records what the institution made of an order.
func (t *tx) AnswerOrder(ctx context.Context, id ebics.OrderID, status ebics.OrderStatus, detail string) error {
	if err := t.write(); err != nil {
		return err
	}
	res, err := t.tx.ExecContext(ctx,
		"UPDATE ebics_orders SET status = ?, detail = ? WHERE order_id = ?",
		string(status), detail, string(id))
	if err != nil {
		return fmt.Errorf("sqlite: answer order %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("sqlite: answer order %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("sqlite: answer order %s: %w", id, ebics.ErrUnknownOrder)
	}
	return nil
}

// ListAcknowledgements is one subscriber's uploads and what became of each,
// oldest first. The payload is not read: HAC says what the host knows about the
// ORDER, and the file's contents travel back as a business message or not at all.
func (t *tx) ListAcknowledgements(ctx context.Context, sub ebics.SubscriberID) ([]ebics.Acknowledgement, error) {
	rows, err := t.tx.QueryContext(ctx, `
		SELECT order_id, order_type, status, detail FROM ebics_orders
		WHERE subscriber = ? ORDER BY seq`, string(sub))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list what %s has uploaded: %w", sub, err)
	}
	defer rows.Close()

	// Empty rather than nil, because "this subscriber has uploaded nothing" is an
	// answer HAC gives and not an absence.
	out := make([]ebics.Acknowledgement, 0)
	for rows.Next() {
		var a ebics.Acknowledgement
		if err := rows.Scan(&a.OrderID, &a.OrderType, &a.Status, &a.Detail); err != nil {
			return nil, fmt.Errorf("sqlite: list what %s has uploaded: %w", sub, err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AddQueuedFile puts one file in a subscriber's download queue.
func (t *tx) AddQueuedFile(ctx context.Context, seq int, sub ebics.SubscriberID, f ebics.File) error {
	if err := t.write(); err != nil {
		return err
	}
	if _, err := t.tx.ExecContext(ctx, `
		INSERT INTO ebics_queue (order_id, subscriber, order_type, payload, seq)
		VALUES (?, ?, ?, ?, ?)`,
		string(f.OrderID), string(sub), string(f.OrderType), f.Payload, int64(seq)); err != nil {
		return fmt.Errorf("sqlite: queue a %s for %s: %w", f.OrderType, sub, err)
	}
	return nil
}

// ListQueuedFiles is everything waiting for one subscriber, in enqueue order.
func (t *tx) ListQueuedFiles(ctx context.Context, sub ebics.SubscriberID) ([]ebics.File, error) {
	rows, err := t.tx.QueryContext(ctx, `
		SELECT order_id, order_type, payload FROM ebics_queue
		WHERE subscriber = ? ORDER BY seq`, string(sub))
	if err != nil {
		return nil, fmt.Errorf("sqlite: list what is waiting for %s: %w", sub, err)
	}
	defer rows.Close()

	var out []ebics.File
	for rows.Next() {
		var f ebics.File
		if err := rows.Scan(&f.OrderID, &f.OrderType, &f.Payload); err != nil {
			return nil, fmt.Errorf("sqlite: list what is waiting for %s: %w", sub, err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// DeleteQueuedFiles takes the collected files out of the queue.
func (t *tx) DeleteQueuedFiles(ctx context.Context, ids []ebics.OrderID) error {
	if err := t.write(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = string(id)
	}
	if _, err := t.tx.ExecContext(ctx,
		"DELETE FROM ebics_queue WHERE order_id IN ("+placeholders(len(ids))+")", args...); err != nil {
		return fmt.Errorf("sqlite: take %d collected files out of a queue: %w", len(ids), err)
	}
	return nil
}
