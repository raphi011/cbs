package mem

import (
	"context"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
)

// The deposit half of tx. It is the same type that implements ledger.Tx, which
// is the whole point: one transaction spans both layers, so a hold write and
// the GL posting that captures it commit or roll back together.
//
// The method names carry a Deposit prefix where ledger.Tx already claimed the
// bare one (PutAccount, GetAccount, ListAccounts); see deposit.Tx for why.

// compile-time check that tx satisfies the deposit interface too.
var _ deposit.Tx = (*tx)(nil)

// ---------------------------------------------------------------------------
// Deposit accounts
// ---------------------------------------------------------------------------

func (t *tx) PutDepositAccount(ctx context.Context, book ledger.BookID, a deposit.Account) error {
	if err := t.write(); err != nil {
		return err
	}
	bucket(t.state.depositAccounts, book)[a.ID] = a
	return nil
}

func (t *tx) GetDepositAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) (deposit.Account, error) {
	a, ok := t.state.depositAccounts[book][id]
	if !ok {
		return deposit.Account{}, deposit.ErrAccountNotFound
	}
	return a, nil
}

func (t *tx) ListDepositAccounts(ctx context.Context, book ledger.BookID) ([]deposit.Account, error) {
	out := make([]deposit.Account, 0, len(t.state.depositAccounts[book]))
	for _, a := range t.state.depositAccounts[book] {
		out = append(out, a)
	}
	sortByCreation(out, func(a deposit.Account) (time.Time, string) { return a.CreatedAt, string(a.ID) })
	return out, nil
}

// ---------------------------------------------------------------------------
// Holds
// ---------------------------------------------------------------------------

func (t *tx) PutHold(ctx context.Context, book ledger.BookID, h deposit.Hold) error {
	if err := t.write(); err != nil {
		return err
	}
	bucket(t.state.holds, book)[h.ID] = h
	return nil
}

func (t *tx) GetHold(ctx context.Context, book ledger.BookID, id deposit.HoldID) (deposit.Hold, error) {
	h, ok := t.state.holds[book][id]
	if !ok {
		return deposit.Hold{}, deposit.ErrHoldNotFound
	}
	return h, nil
}

// ListHoldsForAccount scans the book's holds rather than keeping an
// account-to-holds index. The index would be a second thing to keep in sync
// inside the store, and store/pg has none either — it filters on account_id, the
// same way ListTransactionsForAccount already works here.
func (t *tx) ListHoldsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.Hold, error) {
	out := make([]deposit.Hold, 0)
	for _, h := range t.state.holds[book] {
		if h.AccountID == id {
			out = append(out, h)
		}
	}
	sortByCreation(out, func(h deposit.Hold) (time.Time, string) { return h.CreatedAt, string(h.ID) })
	return out, nil
}

// ActiveHoldTotal sums the holds that currently reduce an account's available
// balance: those still Active and not past their expiry.
//
// A zero ExpiresAt means the hold never expires. Like BookBalance this is an
// aggregate, so an account with no holds — including one that does not exist —
// totals zero rather than erroring.
func (t *tx) ActiveHoldTotal(ctx context.Context, book ledger.BookID, id deposit.AccountID, now time.Time) (ledger.Amount, error) {
	var total ledger.Amount
	for _, h := range t.state.holds[book] {
		if h.AccountID != id || h.Status != deposit.HoldActive {
			continue
		}
		if !h.ExpiresAt.IsZero() && h.ExpiresAt.Before(now) {
			continue
		}
		total += h.Amount
	}
	return total, nil
}

// ---------------------------------------------------------------------------
// End-of-day snapshots
// ---------------------------------------------------------------------------

// PutSnapshot upserts a snapshot under (account, business date): taking a second
// snapshot for the same date replaces the first.
func (t *tx) PutSnapshot(ctx context.Context, book ledger.BookID, s deposit.Snapshot) error {
	if err := t.write(); err != nil {
		return err
	}
	key := snapshotKey{account: s.AccountID, dateKey: deposit.SnapshotDateKey(s.Date)}
	bucket(t.state.snapshots, book)[key] = s
	return nil
}

func (t *tx) GetSnapshot(ctx context.Context, book ledger.BookID, id deposit.AccountID, dateKey string) (deposit.Snapshot, error) {
	s, ok := t.state.snapshots[book][snapshotKey{account: id, dateKey: dateKey}]
	if !ok {
		return deposit.Snapshot{}, deposit.ErrSnapshotNotFound
	}
	return s, nil
}

func (t *tx) ListSnapshotsForAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) ([]deposit.Snapshot, error) {
	out := make([]deposit.Snapshot, 0)
	for key, s := range t.state.snapshots[book] {
		if key.account == id {
			out = append(out, s)
		}
	}
	// Snapshots are identified by their business date, so the date is already a
	// total order within an account; the key is the tie-break only for form.
	sortByCreation(out, func(s deposit.Snapshot) (time.Time, string) {
		return s.Date, deposit.SnapshotDateKey(s.Date)
	})
	return out, nil
}
