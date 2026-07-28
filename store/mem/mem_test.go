package mem_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
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

// TestDepositConformance runs the deposit half of the suite against the same
// implementation, through the deposit.Store view of the store. It is the same
// underlying *mem.Store and the same *tx — which is exactly what lets a capture
// write a hold and post a GL transaction in one unit of work.
func TestDepositConformance(t *testing.T) {
	storetest.RunDeposit(t, func(t *testing.T) deposit.Store {
		return mem.New(func() time.Time { return time.Unix(0, 0).UTC() }).Deposit()
	})
}

// TestPaymentConformance runs the payment half of the suite against the same
// implementation, through the payment.Store view. It is the same underlying
// *mem.Store and the same *tx as the ledger and deposit views — which is what
// lets a settlement post across every participant's book, the central bank's
// and its own record in one unit of work.
func TestPaymentConformance(t *testing.T) {
	storetest.RunPayment(t, func(t *testing.T) payment.Store {
		return mem.New(func() time.Time { return time.Unix(0, 0).UTC() }).Payment()
	})
}

// TestLendingConformance runs the lending half of the suite against the same
// implementation, through the lending.Store view of the store. It is the same
// underlying *mem.Store and the same *tx — which is exactly what lets a
// disbursement's facility write and its GL posting commit or roll back
// together.
func TestLendingConformance(t *testing.T) {
	storetest.RunLending(t, func(t *testing.T) lending.Store {
		return mem.New(func() time.Time { return time.Unix(0, 0).UTC() }).Lending()
	})
}

// A deposit unit of work must never be opened inside another one on the same
// store: mem's mutex is not reentrant, so it would deadlock without a word.
// Implementation-specific, hence tested here rather than in storetest.
func TestNestedUnitOfWorkIsRefused(t *testing.T) {
	s := mem.New(func() time.Time { return time.Unix(0, 0).UTC() })
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	cases := map[string]func(context.Context) error{
		"UpdateInUpdate": func(ctx context.Context) error {
			return s.Update(ctx, func(ctx context.Context, _ ledger.Tx) error { return nil })
		},
		"ViewInUpdate": func(ctx context.Context) error {
			return s.View(ctx, func(ctx context.Context, _ ledger.Tx) error { return nil })
		},
		"DepositUpdateInUpdate": func(ctx context.Context) error {
			return s.Deposit().Update(ctx, func(ctx context.Context, _ deposit.Tx) error { return nil })
		},
		"PaymentUpdateInUpdate": func(ctx context.Context) error {
			return s.Payment().Update(ctx, func(ctx context.Context, _ payment.Tx) error { return nil })
		},
		"PaymentViewInUpdate": func(ctx context.Context) error {
			return s.Payment().View(ctx, func(ctx context.Context, _ payment.Tx) error { return nil })
		},
	}

	for name, nested := range cases {
		t.Run(name, func(t *testing.T) {
			err := s.Update(context.Background(), func(ctx context.Context, _ ledger.Tx) error {
				return nested(ctx)
			})
			if !errors.Is(err, mem.ErrNestedTransaction) {
				t.Fatalf("nested %s: got error %v, want %v", name, err, mem.ErrNestedTransaction)
			}
		})
	}

	// A different store is not the same unit of work, so driving two stores from
	// one context stays legal.
	other := mem.New(func() time.Time { return time.Unix(0, 0).UTC() })
	err := s.Update(context.Background(), func(ctx context.Context, _ ledger.Tx) error {
		return other.Update(ctx, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutLedger(ctx, "book", ledger.Ledger{ID: "ldg_1"})
		})
	})
	if err != nil {
		t.Fatalf("unit of work on a second store: %v", err)
	}
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
