package ledger

import (
	"encoding/json"
	"testing"
)

// The original audit log stored the live entity pointer in Payload, so mutating
// the entity afterwards rewrote history — an "immutable" log with mutable
// entries. Payload must be a snapshot taken at append time.
func TestAuditPayloadIsSnapshotNotReference(t *testing.T) {
	book := NewBook()
	acct := &Account{ID: AccountID("200.100.001"), Name: "Alice", Type: Liability}

	assertNoError(t, book.appendAudit(ScopeLedger, EventAccountCreated, string(acct.ID), acct))

	// Mutate the entity AFTER the event was appended.
	acct.Name = "Mutated"

	events := book.GetAuditLogForEntity(string(acct.ID))
	if len(events) != 1 {
		t.Fatalf("got %d events for %s, want 1", len(events), acct.ID)
	}

	var got struct{ Name string }
	assertNoError(t, json.Unmarshal(events[0].Payload, &got))
	assertEqual(t, "payload name after mutating the entity", got.Name, "Alice")
}
