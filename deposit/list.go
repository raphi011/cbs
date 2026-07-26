package deposit

import "context"

// ---------------------------------------------------------------------------
// Enumeration
//
// These read-only methods enumerate deposit-layer entities for callers that
// need to browse the register (for example a UI). Each runs in a read-only unit
// of work. To keep enumeration simple they are total: an unknown account yields
// an empty slice rather than an error. Callers that want a 404 can pre-validate
// with GetAccount. The store returns accounts and holds ordered by CreatedAt
// then ID, and snapshots by business date.
// ---------------------------------------------------------------------------

// ListAccounts returns all deposit accounts, ordered by creation time then ID.
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
// then ID. An unknown account yields an empty slice.
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
