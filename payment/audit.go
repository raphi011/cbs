package payment

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/raphi011/cbs/ledger"
)

// The payment layer's audit trail.

// appendAuditTx records an immutable payment-scope event through the caller's
// transaction, so an event never outlives an operation that rolled back.
func (s *Network) appendAuditTx(ctx context.Context, tx ledger.CommonTx, eventType, entityID string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("audit %s: marshal payload: %w", eventType, err)
	}
	id, err := tx.NextID(ctx, s.book(), "evt")
	if err != nil {
		return err
	}
	return tx.AppendAudit(ctx, ledger.AuditEvent{
		ID:         id,
		BookID:     s.book(),
		Scope:      ledger.ScopePayment,
		Type:       eventType,
		EntityID:   entityID,
		Payload:    raw,
		OccurredAt: s.now(),
	})
}

// ListAudit returns audit events from any layer, narrowed by f and ordered by
// Seq ascending. See ledger.AuditFilter for the filter and paging semantics.
func (s *Network) ListAudit(ctx context.Context, f ledger.AuditFilter) ([]ledger.AuditEvent, error) {
	var out []ledger.AuditEvent
	err := s.common.View(ctx, func(ctx context.Context, tx ledger.CommonTx) error {
		var err error
		out, err = tx.ListAudit(ctx, f)
		return err
	})
	return out, err
}
