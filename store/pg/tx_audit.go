package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// AppendAudit appends an event. Seq comes from the table's BIGSERIAL, so
// whatever the caller set is overwritten: the sequence is a total order over the
// whole store, and only the store can issue it.
func (t *tx) AppendAudit(ctx context.Context, e ledger.AuditEvent) error {
	if err := t.write(); err != nil {
		return err
	}
	metadata, err := marshalStringMap(e.Metadata)
	if err != nil {
		return fmt.Errorf("pg: append audit %s: %w", e.ID, err)
	}
	_, err = t.tx.Exec(ctx, `
		INSERT INTO audit_events (id, book_id, scope, type, entity_id, payload, metadata, actor, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		e.ID, string(e.BookID), string(e.Scope), e.Type, e.EntityID,
		jsonParam(e.Payload), metadata, e.Actor, nullTime(e.OccurredAt))
	if err != nil {
		return fmt.Errorf("pg: append audit %s: %w", e.ID, err)
	}
	return nil
}

// ListAudit returns the events matching f in ascending Seq order.
//
// Limit is applied LAST, after every other predicate, and it keeps the NEWEST
// matches below the cursor: ORDER BY seq DESC LIMIT n, reversed on the way out.
// That is what makes ?before= mean "the next page of THIS filter". Seq is a
// store-global order, so a filtered log's events are separated by gaps belonging
// to other books and scopes; a LIMIT applied before the filter — or one that
// took the oldest matches — would return short or empty pages that look like
// end-of-data.
func (t *tx) ListAudit(ctx context.Context, f ledger.AuditFilter) ([]ledger.AuditEvent, error) {
	where := make([]string, 0, 5)
	args := make([]any, 0, 5)
	add := func(clause string, arg any) {
		args = append(args, arg)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if f.BookID != "" {
		add("book_id = $%d", string(f.BookID))
	}
	if f.Scope != "" {
		add("scope = $%d", string(f.Scope))
	}
	if f.Type != "" {
		add("type = $%d", f.Type)
	}
	if f.EntityID != "" {
		add("entity_id = $%d", f.EntityID)
	}
	if f.Before > 0 {
		add("seq < $%d", f.Before)
	}

	query := "SELECT seq, id, book_id, scope, type, entity_id, payload, metadata, actor, occurred_at FROM audit_events"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	descending := f.Limit > 0
	if descending {
		args = append(args, f.Limit)
		query += fmt.Sprintf(" ORDER BY seq DESC LIMIT $%d", len(args))
	} else {
		query += " ORDER BY seq"
	}

	rows, err := t.tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("pg: list audit: %w", err)
	}
	defer rows.Close()

	out := make([]ledger.AuditEvent, 0)
	for rows.Next() {
		var (
			e          ledger.AuditEvent
			payload    []byte
			metadata   []byte
			occurredAt *time.Time
		)
		if err := rows.Scan(&e.Seq, &e.ID, &e.BookID, &e.Scope, &e.Type, &e.EntityID,
			&payload, &metadata, &e.Actor, &occurredAt); err != nil {
			return nil, fmt.Errorf("pg: list audit: %w", err)
		}
		if payload != nil {
			e.Payload = json.RawMessage(payload)
		}
		if e.Metadata, err = unmarshalStringMap(metadata); err != nil {
			return nil, fmt.Errorf("pg: audit %s metadata: %w", e.ID, err)
		}
		e.OccurredAt = readTime(occurredAt)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg: list audit: %w", err)
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

// nullTime renders Go's zero time as SQL NULL.
//
// Several fields use the zero time as "unset" — a hold that never expires, a
// cycle that has not closed — and IsZero() must still hold after a round trip.
// Storing the actual year-1 timestamp would not survive a session in a non-UTC
// timezone, so absence is stored as absence.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

// readTime is nullTime's inverse. Times come back in UTC so that formatting one
// into a business-date key (deposit.SnapshotDateKey) gives the same day it went
// in as.
func readTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

// jsonParam passes raw JSON through to a JSONB column, mapping a nil document to
// SQL NULL so that "no payload" round-trips as nil rather than as null-the-JSON.
func jsonParam(raw json.RawMessage) any {
	if raw == nil {
		return nil
	}
	return []byte(raw)
}

// marshalStringMap encodes a string map for a JSONB column. A nil map is NULL
// and an empty one is {}, because store/mem keeps the two apart and the API
// renders them differently.
func marshalStringMap(m map[string]string) (any, error) {
	if m == nil {
		return nil, nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return raw, nil
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
