package payment

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/raphi011/cbs/ledger"
)

// The payment layer's audit trail.
//
// Every event here is recorded under THE ACTING INSTITUTION's own book, with
// ledger.ScopePayment — the same book that institution's IDs are drawn from. A
// bank's own books keep their ledger- and deposit-scope logs separately, and the
// three never mix, because Scope discriminates them.
//
// It used to say "network-scoped": participants, mandates, payments and clearing
// cycles belonged to no single bank, so they went under ledger.NetworkBook and
// one audit stream held all three institutions' payment-layer events
// interleaved. Task 18 split the databases and the stream went with them. Each
// of those rows had exactly one owner all along — a mandate is the creditor
// bank's, a cycle is the clearing house's — and what the shared book was hiding
// is that "the network did this" was never a thing that happened. Some institution
// did it.
//
// The visible consequence is that one payment now leaves events in up to three
// logs, and that is the honest record rather than a duplication: the payer's
// bank initiated it, the clearing house cleared it, the payee's bank credited
// it, and each of those is that institution's own history. Reading them
// together is the reconciliation harness's job, and it is the one thing no
// institution may do.

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
