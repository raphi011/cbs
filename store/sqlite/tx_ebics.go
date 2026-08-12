package sqlite

import (
	"context"
	"fmt"

	"github.com/raphi011/cbs/ebics"
)

// The file transport's two tables: the download queues and the order log.
//
// They are in the clearing house's schema and the settlement agent's and in no
// other, because those are the two institutions that are DIALLED. A member bank
// cannot ask for any of this: EBICS is a method on those two store types and on
// no third, which is the topology made into a type. EBICS has no push, so a bank
// only ever dials out and hosts nothing.
//
// This file is the one place in the store that names ebics, and the dependency
// runs the only way it can: that package declares the port and imports nothing
// from this repository, so the transport stays unable to name a payment. See
// ebics.Store.

// One more adapter for the reason the others exist — Go allows one Update method
// per type and each Store interface declares a different callback — and the
// callback is handed the very same *tx, so nothing about the mechanism differs.
// What differs is that no unit of work ever spans this and the institution's
// own: a file is put on a connection outside the transaction that decided to
// send it, so the transport's writes are always their own.
//
// It is reached through ClearingHouseStore.EBICS and CentralBankStore.EBICS and
// through nothing else: a member bank hosts no queue.
type ebicsStore struct{ *store }

var _ ebics.Store = ebicsStore{}

func (e ebicsStore) Update(ctx context.Context, fn func(context.Context, ebics.Tx) error) error {
	return e.store.update(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

func (e ebicsStore) View(ctx context.Context, fn func(context.Context, ebics.Tx) error) error {
	return e.store.view(ctx, func(ctx context.Context, t *tx) error { return fn(ctx, t) })
}

// NextOrderSeq allocates the next order ordinal at this host.
//
// It draws from id_sequences under a counter of its own, which is what makes it
// gap-free and monotonic across a restart: an order id must be unique at the host
// that minted it for the life of its database, and a queue row is deleted when
// it is collected, so MAX(seq)+1 over the rows would hand the same id out twice.
//
// The counter is one-based, as every counter in that table is, and an order
// ordinal is zero-based, because A000 is the first id the protocol's format
// mints. The subtraction is the whole of the difference between the two.
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
//
// A scan of the whole table ordered by seq, which is what a work list is: the
// answered rows are never deleted, so the index that serves HAC does not serve
// this and there is nothing for one to select on but the status.
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
//
// The rows affected are what tells an unknown id from a known one. UPDATE is
// silent about a row that is not there, and an order this host never minted is
// ebics.ErrUnknownOrder rather than a write that reports success and changes
// nothing.
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
//
// By id and not by subscriber, because a C53 download collects the statements
// and leaves everything else where it was. The ids come from a slice this store
// handed the caller a moment earlier in the same transaction.
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
