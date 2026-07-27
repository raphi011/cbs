package ledger_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	. "github.com/raphi011/cbs/ledger"
)

// The original audit log stored the live entity pointer in Payload, so mutating
// the entity afterwards rewrote history — an "immutable" log with mutable
// entries. Payload must be a snapshot taken at append time.
func TestAuditPayloadIsSnapshotNotReference(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	acct := &Account{ID: AccountID("200.100.001"), Name: "Alice", Type: Liability}

	assertNoError(t, book.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		return book.AppendAuditTx(ctx, tx, ScopeLedger, EventAccountCreated, string(acct.ID), acct)
	}))

	// Mutate the entity AFTER the event was appended.
	acct.Name = "Mutated"

	events, err := book.GetAuditLogForEntity(ctx, string(acct.ID))
	assertNoError(t, err)
	if len(events) != 1 {
		t.Fatalf("got %d events for %s, want 1", len(events), acct.ID)
	}

	var got struct{ Name string }
	assertNoError(t, json.Unmarshal(events[0].Payload, &got))
	assertEqual(t, "payload name after mutating the entity", got.Name, "Alice")
}

// An audit event must never outlive the operation it describes, which is why it
// is written through the transaction rather than to a slice on the Book.
func TestAuditEventRollsBackWithTheTransaction(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)

	boom := errors.New("deliberate failure")
	err := book.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		if err := book.AppendAuditTx(ctx, tx, ScopeLedger, EventLedgerCreated, "ldg_1", map[string]string{"name": "GL"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Update: got error %v, want %v", err, boom)
	}

	events, err := book.GetAuditLog(ctx)
	assertNoError(t, err)
	assertEqual(t, "audit events after rollback", len(events), 0)
}
