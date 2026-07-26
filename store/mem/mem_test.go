package mem_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/store/mem"
	"github.com/raphi011/cbs/store/storetest"
)

// TestConformance runs the shared store conformance suite against the in-memory
// implementation. store/pg runs the same suite; any behaviour the two could
// drift apart on belongs in storetest rather than here.
//
// The clock is frozen so that every CreatedAt and OccurredAt ties, which forces
// the suite to rely on IDs and audit sequence numbers for ordering rather than
// on wall-clock luck.
func TestConformance(t *testing.T) {
	storetest.RunLedger(t, func(t *testing.T) ledger.Store {
		return mem.New(func() time.Time { return time.Unix(0, 0).UTC() })
	})
}

// View holds only a read lock, so a write through its Tx would be a data race
// rather than a change that could be rolled back. It is refused instead —
// implementation-specific, hence tested here rather than in storetest.
func TestViewRejectsWrites(t *testing.T) {
	s := mem.New(func() time.Time { return time.Unix(0, 0).UTC() })
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	cases := map[string]func(context.Context, ledger.Tx) error{
		"NextID": func(ctx context.Context, tx ledger.Tx) error {
			_, err := tx.NextID(ctx, "book", "ldg")
			return err
		},
		"PutLedger": func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutLedger(ctx, "book", ledger.Ledger{ID: "ldg_1"})
		},
		"AppendAudit": func(ctx context.Context, tx ledger.Tx) error {
			return tx.AppendAudit(ctx, ledger.AuditEvent{ID: "evt_1", BookID: "book"})
		},
		"MarkReversed": func(ctx context.Context, tx ledger.Tx) error {
			return tx.MarkReversed(ctx, "book", "tx_1")
		},
	}

	for name, write := range cases {
		t.Run(name, func(t *testing.T) {
			err := s.View(context.Background(), write)
			if !errors.Is(err, mem.ErrReadOnly) {
				t.Fatalf("View(%s): got error %v, want %v", name, err, mem.ErrReadOnly)
			}
		})
	}
}
