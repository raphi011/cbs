package payment

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/raphi011/cbs/ledger"
)

// The payment layer's audit trail.
//
// Every event here is network-scoped: participants, mandates, payments and
// clearing cycles belong to no single bank, so they are recorded under
// ledger.NetworkBook with ledger.ScopePayment — the same book the network's IDs
// are drawn from. A bank's own books keep their ledger- and deposit-scope logs
// separately, and the two never mix, because Scope discriminates them.

// appendAuditTx records an immutable payment-scope event through the caller's
// transaction, so an event never outlives an operation that rolled back. This
// is also the only way it can work: the store's unit of work is not reentrant,
// so an append that opened its own Update from inside an operation would be
// refused.
//
// payload is marshalled now rather than held by reference, so later mutation of
// the entity cannot rewrite the record of what happened. The event's Seq is
// assigned by the store.
func (s *Network) appendAuditTx(ctx context.Context, tx Tx, eventType, entityID string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("audit %s: marshal payload: %w", eventType, err)
	}
	id, err := tx.NextID(ctx, ledger.NetworkBook, "evt")
	if err != nil {
		return err
	}
	return tx.AppendAudit(ctx, ledger.AuditEvent{
		ID:         id,
		BookID:     ledger.NetworkBook,
		Scope:      ledger.ScopePayment,
		Type:       eventType,
		EntityID:   entityID,
		Payload:    raw,
		OccurredAt: s.now(),
	})
}

// ListAudit returns audit events from any layer, narrowed by f and ordered by
// Seq ascending. See ledger.AuditFilter for the filter and paging semantics.
//
// It is the network's single read path into the log, because every book — each
// participant's, the central bank's, and the network's own — lives in the one
// store behind it. A caller picks the log it wants with f.BookID and f.Scope;
// Seq is a store-global total order, so a Before cursor is only meaningful
// together with the filter that produced it.
func (s *Network) ListAudit(ctx context.Context, f ledger.AuditFilter) ([]ledger.AuditEvent, error) {
	var out []ledger.AuditEvent
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListAudit(ctx, f)
		return err
	})
	return out, err
}
