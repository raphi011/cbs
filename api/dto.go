package api

import (
	"encoding/json"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// This file defines the JSON wire format for the API. The domain packages use
// integer enums (rendered here via their String() methods) and typed string
// IDs (rendered as plain strings), so explicit DTOs keep the wire format
// readable and decoupled from the internal Go types. Monetary amounts stay as
// integer minor units (never floats or strings).
//
// Per-resource DTOs live in dto_ledger.go, dto_deposit.go, and dto_payment.go
// (mirroring the handlers_*.go split). This file holds only the cross-cutting
// types shared across resources.

// auditEventDTO is the wire shape of an audit event. All four layers render
// into it; scope says which one produced it.
type auditEventDTO struct {
	Seq int64  `json:"seq"`
	ID  string `json:"id"`
	// BookID is which institution's log this event is in, and it is on the wire
	// because Seq stopped identifying one.
	//
	// Each institution has its own database and its own counter, so every log
	// starts at 1 and "seq 7" names as many events as there are institutions. The
	// book is what disambiguates them, and a reader holding pages from several
	// logs has no other way to.
	BookID    string            `json:"bookId"`
	Scope     string            `json:"scope"`
	Timestamp time.Time         `json:"timestamp"`
	Type      string            `json:"type"`
	EntityID  string            `json:"entityId"`
	Payload   json.RawMessage   `json:"payload,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// toAuditDTO renders an audit event from any layer onto the wire; Scope says
// which one produced it.
func toAuditDTO(e ledger.AuditEvent) auditEventDTO {
	return auditEventDTO{
		Seq:       e.Seq,
		ID:        e.ID,
		BookID:    string(e.BookID),
		Scope:     string(e.Scope),
		Timestamp: e.OccurredAt,
		Type:      e.Type,
		EntityID:  e.EntityID,
		Payload:   e.Payload,
		Metadata:  e.Metadata,
	}
}

// nameRequest, descriptionRequest, and reasonRequest are generic single-field
// request bodies reused across multiple resources.

type nameRequest struct {
	Name string `json:"name"`
}

type descriptionRequest struct {
	Description string `json:"description"`
}

type reasonRequest struct {
	Reason string `json:"reason"`
}
