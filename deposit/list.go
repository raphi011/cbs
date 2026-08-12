package deposit

import "context"

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
