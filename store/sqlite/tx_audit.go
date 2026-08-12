package sqlite

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// AppendAudit appends an event. Seq is allocated here, so whatever the caller
// set is overwritten: the sequence is a total order over the whole store, and
// only the store can issue it.
func (t *tx) AppendAudit(ctx context.Context, e ledger.AuditEvent) error {
	// The book arrives on the event rather than as a parameter, so this is the one
	// place the guard has to reach into an argument to find it.
	if err := t.own(e.BookID); err != nil {
		return err
	}
	if err := t.write(); err != nil {
		return err
	}
	metadata, err := marshalStringMap(e.Metadata)
	if err != nil {
		return fmt.Errorf("sqlite: append audit %s: %w", e.ID, err)
	}
	_, err = t.tx.ExecContext(ctx, `
		INSERT INTO audit_events (seq, id, book_id, scope, type, entity_id, payload, metadata, actor, occurred_at)
		VALUES (`+nextRowSeq("audit_events")+`, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, string(e.BookID), string(e.Scope), e.Type, e.EntityID,
		jsonParam(e.Payload), metadata, e.Actor, nullTime{e.OccurredAt})
	if err != nil {
		return fmt.Errorf("sqlite: append audit %s: %w", e.ID, err)
	}
	return nil
}

// ListAudit returns the events matching f in ascending Seq order.
func (t *tx) ListAudit(ctx context.Context, f ledger.AuditFilter) ([]ledger.AuditEvent, error) {
	// An EMPTY BookID is "no filter", which means "this institution's whole log" —
	// there is nothing else in the database.
	if f.BookID != "" {
		if err := t.own(f.BookID); err != nil {
			return nil, err
		}
	}
	where := make([]string, 0, 5)
	args := make([]any, 0, 5)
	add := func(clause string, arg any) {
		args = append(args, arg)
		where = append(where, clause)
	}
	if f.BookID != "" {
		add("book_id = ?", string(f.BookID))
	}
	if f.Scope != "" {
		add("scope = ?", string(f.Scope))
	}
	if f.Type != "" {
		add("type = ?", f.Type)
	}
	if f.EntityID != "" {
		add("entity_id = ?", f.EntityID)
	}
	if f.Before > 0 {
		add("seq < ?", f.Before)
	}

	query := "SELECT seq, id, book_id, scope, type, entity_id, payload, metadata, actor, occurred_at FROM audit_events"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	descending := f.Limit > 0
	if descending {
		query += " ORDER BY seq DESC LIMIT ?"
		args = append(args, f.Limit)
	} else {
		query += " ORDER BY seq"
	}

	rows, err := t.tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("sqlite: list audit: %w", err)
	}
	defer rows.Close()

	out := make([]ledger.AuditEvent, 0)
	for rows.Next() {
		var (
			e          ledger.AuditEvent
			payload    []byte
			metadata   []byte
			occurredAt nullTime
		)
		if err := rows.Scan(&e.Seq, &e.ID, &e.BookID, &e.Scope, &e.Type, &e.EntityID,
			&payload, &metadata, &e.Actor, &occurredAt); err != nil {
			return nil, fmt.Errorf("sqlite: list audit: %w", err)
		}
		if payload != nil {
			e.Payload = json.RawMessage(payload)
		}
		if e.Metadata, err = unmarshalStringMap(metadata); err != nil {
			return nil, fmt.Errorf("sqlite: audit %s metadata: %w", e.ID, err)
		}
		e.OccurredAt = occurredAt.Time
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: list audit: %w", err)
	}
	if descending {
		// The page was taken newest-first so that LIMIT would keep the newest
		// matches; the contract hands it back oldest-first.
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Value conversion
// ---------------------------------------------------------------------------

// nullTime carries an instant in and out of a TEXT timestamp column, rendering
// Go's zero time as SQL NULL.
type nullTime struct{ time.Time }

// Value renders the instant for storage, or NULL.
func (n nullTime) Value() (driver.Value, error) {
	if n.IsZero() {
		return nil, nil
	}
	return formatTime(n.Time), nil
}

// Scan reads one back. A NULL is the zero time, which is what the field means.
func (n *nullTime) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		n.Time = time.Time{}
		return nil
	case string:
		t, err := parseTime(v)
		if err != nil {
			return fmt.Errorf("sqlite: read timestamp %q: %w", v, err)
		}
		n.Time = t
		return nil
	case []byte:
		return n.Scan(string(v))
	case time.Time:
		n.Time = v.UTC()
		return nil
	default:
		return fmt.Errorf("sqlite: cannot read %T as a timestamp", src)
	}
}

// jsonParam passes raw JSON through to a json_valid column, mapping a nil
// document to SQL NULL so that "no payload" round-trips as nil rather than as
// null-the-JSON.
func jsonParam(raw json.RawMessage) any {
	if raw == nil {
		return nil
	}
	return string(raw)
}

// marshalStringMap encodes a string map for a json_valid column. A nil map is
// NULL and an empty one is {}, because the API renders the two differently and
// a round trip that collapsed them would change what a caller sees.
func marshalStringMap(m map[string]string) (any, error) {
	if m == nil {
		return nil, nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

// unmarshalStringMap is marshalStringMap's inverse.
func unmarshalStringMap(raw []byte) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	out := make(map[string]string)
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
