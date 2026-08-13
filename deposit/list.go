package deposit

import (
	"context"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// ---------------------------------------------------------------------------
// Enumeration

// ListAccounts returns all deposit accounts, ordered by creation time then
// insertion order.
func (r *Register) ListAccounts(ctx context.Context) ([]Account, error) {
	var out []Account
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListDepositAccounts(ctx, r.bookID)
		return err
	})
	return out, err
}

// ListHolds returns all holds for the given account, ordered by creation time
// then insertion order. An unknown account yields an empty slice.
func (r *Register) ListHolds(ctx context.Context, accountID AccountID) ([]Hold, error) {
	var out []Hold
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListHoldsForAccount(ctx, r.bookID, accountID)
		return err
	})
	return out, err
}

// ActiveHoldTotal is what an account's holds take off its available balance as
// at now. It folds the account's holds rather than asking the store to add them
// up, so Hold.ActiveAt is the only place the rule is written.
func ActiveHoldTotal(ctx context.Context, tx HoldLister, book ledger.BookID, id AccountID, now time.Time) (ledger.Amount, error) {
	holds, err := tx.ListHoldsForAccount(ctx, book, id)
	if err != nil {
		return 0, err
	}
	var total ledger.Amount
	for _, h := range holds {
		if h.ActiveAt(now) {
			total += h.Amount
		}
	}
	return total, nil
}

// ListSnapshots returns all end-of-day snapshots for the given account, ordered
// by business date. An unknown account yields an empty slice.
func (r *Register) ListSnapshots(ctx context.Context, accountID AccountID) ([]Snapshot, error) {
	var out []Snapshot
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListSnapshotsForAccount(ctx, r.bookID, accountID)
		return err
	})
	return out, err
}
