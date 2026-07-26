package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
)

// RunDeposit runs the deposit-layer conformance suite against a store. Every
// deposit.Store implementation must pass it identically.
//
// It talks only to deposit.Store and deposit.Tx — never to deposit.Register — so
// what it pins is the storage contract: book scoping, not-found sentinels,
// listing order, the active-hold aggregate, snapshot upsert identity, and the
// cross-layer rollback that deposit.Tx embedding ledger.Tx exists to provide.
//
// newStore must return a store with no state in it; the suite calls it once per
// subtest and closes the result.
func RunDeposit(t *testing.T, newStore func(*testing.T) deposit.Store) {
	t.Helper()

	t.Run("DepositAccountRoundTripsAndIsBookScoped", func(t *testing.T) {
		s := openDeposit(t, newStore)

		// The same deposit account ID in two books is two different accounts,
		// exactly as in the ledger: an implementation that forgets its
		// WHERE book_id = $1 hands one bank another bank's customer.
		const shared deposit.AccountID = "dep_1"
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			if err := tx.PutDepositAccount(ctx, bookA, deposit.Account{
				ID: shared, GLAccount: "200.100.001", Name: "Alice at A",
				Status: deposit.Active, OverdraftLimit: 500, CreatedAt: early,
			}); err != nil {
				return err
			}
			return tx.PutDepositAccount(ctx, bookB, deposit.Account{
				ID: shared, GLAccount: "200.100.001", Name: "Bob at B",
				Status: deposit.Frozen, CreatedAt: early,
			})
		})

		var inA, inB deposit.Account
		var listedA []deposit.Account
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			var err error
			if inA, err = tx.GetDepositAccount(ctx, bookA, shared); err != nil {
				return err
			}
			if inB, err = tx.GetDepositAccount(ctx, bookB, shared); err != nil {
				return err
			}
			listedA, err = tx.ListDepositAccounts(ctx, bookA)
			return err
		})

		assertEqual(t, "account in book-a", inA.Name, "Alice at A")
		assertEqual(t, "account in book-b", inB.Name, "Bob at B")
		// Every field round-trips, not just the name.
		assertEqual(t, "gl account", string(inA.GLAccount), "200.100.001")
		assertEqual(t, "status", inA.Status.String(), deposit.Active.String())
		assertEqual(t, "overdraft limit", inA.OverdraftLimit, ledger.Amount(500))
		assertEqual(t, "created at", inA.CreatedAt.Equal(early), true)
		assertEqual(t, "book-b status is its own", inB.Status.String(), deposit.Frozen.String())

		// Listing one book must not show the other book's rows.
		assertEqual(t, "accounts listed for book-a", len(listedA), 1)

		// PutDepositAccount is an upsert on ID, which is how a status change is
		// written.
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			a := inA
			a.Status = deposit.Closed
			return tx.PutDepositAccount(ctx, bookA, a)
		})
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			a, err := tx.GetDepositAccount(ctx, bookA, shared)
			if err != nil {
				return err
			}
			assertEqual(t, "status after upsert", a.Status.String(), deposit.Closed.String())
			all, err := tx.ListDepositAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "upsert did not add a row", len(all), 1)
			return nil
		})
	})

	t.Run("GetOnMissingDepositRowsReturnsSentinels", func(t *testing.T) {
		s := openDeposit(t, newStore)

		// A store that reports "not found" as anything but the domain sentinel
		// turns every 404 in the deposit API into a 500.
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			if err := tx.PutDepositAccount(ctx, bookA, account("dep_1", early)); err != nil {
				return err
			}
			if err := tx.PutHold(ctx, bookA, hold("hld_1", "dep_1", 100, deposit.HoldActive, early, time.Time{})); err != nil {
				return err
			}
			return tx.PutSnapshot(ctx, bookA, snapshot("dep_1", day(15)))
		})

		// Unknown IDs in a book that holds a row of every kind.
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			_, err := tx.GetDepositAccount(ctx, bookA, "dep_nope")
			assertErrorIs(t, "GetDepositAccount on an unknown account", err, deposit.ErrAccountNotFound)

			_, err = tx.GetHold(ctx, bookA, "hld_nope")
			assertErrorIs(t, "GetHold on an unknown hold", err, deposit.ErrHoldNotFound)

			_, err = tx.GetSnapshot(ctx, bookA, "dep_1", deposit.SnapshotDateKey(day(99)))
			assertErrorIs(t, "GetSnapshot on a date with no snapshot", err, deposit.ErrSnapshotNotFound)

			_, err = tx.GetSnapshot(ctx, bookA, "dep_nope", deposit.SnapshotDateKey(day(15)))
			assertErrorIs(t, "GetSnapshot on an unknown account", err, deposit.ErrSnapshotNotFound)
			return nil
		})

		// The same IDs in another book are equally not found.
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			_, err := tx.GetDepositAccount(ctx, bookB, "dep_1")
			assertErrorIs(t, "GetDepositAccount across books", err, deposit.ErrAccountNotFound)

			_, err = tx.GetHold(ctx, bookB, "hld_1")
			assertErrorIs(t, "GetHold across books", err, deposit.ErrHoldNotFound)

			_, err = tx.GetSnapshot(ctx, bookB, "dep_1", deposit.SnapshotDateKey(day(15)))
			assertErrorIs(t, "GetSnapshot across books", err, deposit.ErrSnapshotNotFound)
			return nil
		})

		// And in a book that has never been written to at all.
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			_, err := tx.GetDepositAccount(ctx, "book-empty", "dep_1")
			assertErrorIs(t, "GetDepositAccount in an empty book", err, deposit.ErrAccountNotFound)

			_, err = tx.GetHold(ctx, "book-empty", "hld_1")
			assertErrorIs(t, "GetHold in an empty book", err, deposit.ErrHoldNotFound)

			_, err = tx.GetSnapshot(ctx, "book-empty", "dep_1", deposit.SnapshotDateKey(day(15)))
			assertErrorIs(t, "GetSnapshot in an empty book", err, deposit.ErrSnapshotNotFound)
			return nil
		})
	})

	t.Run("DepositListOrderingIsCreatedAtThenID", func(t *testing.T) {
		s := openDeposit(t, newStore)

		late := early.Add(time.Hour)

		// Same shape as the ledger's ordering fixture: every listing is inserted
		// out of order, contains a CreatedAt tie only an ID comparison can break,
		// and has the row created LAST sorting FIRST by ID — so a plain
		// ORDER BY id fails here instead of quietly reordering a UI list.
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			for _, a := range []deposit.Account{
				{ID: "dep_0", Name: "latecomer", CreatedAt: late.Add(time.Hour)},
				{ID: "dep_3", Name: "third", CreatedAt: late},
				{ID: "dep_2", Name: "second", CreatedAt: early},
				{ID: "dep_1", Name: "first", CreatedAt: early},
			} {
				if err := tx.PutDepositAccount(ctx, bookA, a); err != nil {
					return err
				}
			}
			// hld_z belongs to another account, so the per-account listing keeps
			// the same ID trap rather than accidentally being in ID order.
			for _, h := range []deposit.Hold{
				{ID: "hld_0", AccountID: "dep_1", Amount: 10, CreatedAt: late.Add(time.Hour)},
				{ID: "hld_3", AccountID: "dep_1", Amount: 10, CreatedAt: late},
				{ID: "hld_z", AccountID: "dep_2", Amount: 10, CreatedAt: early},
				{ID: "hld_2", AccountID: "dep_1", Amount: 10, CreatedAt: early},
				{ID: "hld_1", AccountID: "dep_1", Amount: 10, CreatedAt: early},
			} {
				if err := tx.PutHold(ctx, bookA, h); err != nil {
					return err
				}
			}
			// Snapshots order by business date, and are inserted backwards.
			for _, d := range []int{17, 15, 16} {
				if err := tx.PutSnapshot(ctx, bookA, snapshot("dep_1", day(d))); err != nil {
					return err
				}
			}
			return tx.PutSnapshot(ctx, bookA, snapshot("dep_2", day(1)))
		})

		var accounts []deposit.Account
		var holds []deposit.Hold
		var snaps []deposit.Snapshot
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			var err error
			if accounts, err = tx.ListDepositAccounts(ctx, bookA); err != nil {
				return err
			}
			if holds, err = tx.ListHoldsForAccount(ctx, bookA, "dep_1"); err != nil {
				return err
			}
			snaps, err = tx.ListSnapshotsForAccount(ctx, bookA, "dep_1")
			return err
		})

		assertOrder(t, "ListDepositAccounts", ids(accounts, func(a deposit.Account) string { return string(a.ID) }),
			"dep_1", "dep_2", "dep_3", "dep_0")

		assertOrder(t, "ListHoldsForAccount", ids(holds, func(h deposit.Hold) string { return string(h.ID) }),
			"hld_1", "hld_2", "hld_3", "hld_0")

		assertOrder(t, "ListSnapshotsForAccount", ids(snaps, func(sn deposit.Snapshot) string {
			return deposit.SnapshotDateKey(sn.Date)
		}), "2025-01-15", "2025-01-16", "2025-01-17")

		// Unknown accounts enumerate to empty rather than erroring; the deposit
		// listings are total.
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			hs, err := tx.ListHoldsForAccount(ctx, bookA, "dep_nope")
			if err != nil {
				return err
			}
			assertEqual(t, "holds for an unknown account", len(hs), 0)
			sn, err := tx.ListSnapshotsForAccount(ctx, bookA, "dep_nope")
			if err != nil {
				return err
			}
			assertEqual(t, "snapshots for an unknown account", len(sn), 0)
			return nil
		})
	})

	t.Run("ActiveHoldTotalExcludesReleasedCapturedAndExpired", func(t *testing.T) {
		s := openDeposit(t, newStore)

		now := early.Add(12 * time.Hour)
		yesterday := early.Add(-24 * time.Hour)
		tomorrow := early.Add(48 * time.Hour)

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			for _, h := range []deposit.Hold{
				// Counted: active, and either never expiring or expiring later.
				hold("hld_1", "dep_1", 100, deposit.HoldActive, early, time.Time{}),
				hold("hld_2", "dep_1", 200, deposit.HoldActive, early, tomorrow),
				// Not counted: no longer active.
				hold("hld_3", "dep_1", 400, deposit.HoldReleased, early, time.Time{}),
				hold("hld_4", "dep_1", 800, deposit.HoldCaptured, early, time.Time{}),
				// Not counted: expired before now.
				hold("hld_5", "dep_1", 1600, deposit.HoldActive, early, yesterday),
				// Not counted: another account, and another book.
				hold("hld_6", "dep_2", 3200, deposit.HoldActive, early, time.Time{}),
			} {
				if err := tx.PutHold(ctx, bookA, h); err != nil {
					return err
				}
			}
			return tx.PutHold(ctx, bookB, hold("hld_7", "dep_1", 6400, deposit.HoldActive, early, time.Time{}))
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			total, err := tx.ActiveHoldTotal(ctx, bookA, "dep_1", now)
			if err != nil {
				return err
			}
			assertEqual(t, "active hold total", total, ledger.Amount(300))

			// The other account in the same book has only its own hold.
			other, err := tx.ActiveHoldTotal(ctx, bookA, "dep_2", now)
			if err != nil {
				return err
			}
			assertEqual(t, "active hold total for the other account", other, ledger.Amount(3200))

			// Scoped by book: book-b's dep_1 is a different account.
			inB, err := tx.ActiveHoldTotal(ctx, bookB, "dep_1", now)
			if err != nil {
				return err
			}
			assertEqual(t, "active hold total in book-b", inB, ledger.Amount(6400))

			// Like BookBalance this is an aggregate: an unknown account is 0,
			// not an error.
			unknown, err := tx.ActiveHoldTotal(ctx, bookA, "dep_nope", now)
			if err != nil {
				return err
			}
			assertEqual(t, "active hold total for an unknown account", unknown, ledger.Amount(0))
			return nil
		})

		// Expiry is evaluated against the `now` passed in, not against the
		// store's clock: rewind past hld_5's expiry and it counts again.
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			total, err := tx.ActiveHoldTotal(ctx, bookA, "dep_1", yesterday.Add(-time.Hour))
			if err != nil {
				return err
			}
			assertEqual(t, "active hold total before every expiry", total, ledger.Amount(1900))
			return nil
		})

		// A hold expiring exactly at `now` has not expired yet — expiry is
		// strictly before now, the same boundary the Register used.
		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			total, err := tx.ActiveHoldTotal(ctx, bookA, "dep_1", tomorrow)
			if err != nil {
				return err
			}
			assertEqual(t, "hold expiring exactly at now still counts", total, ledger.Amount(300))
			return nil
		})
	})

	t.Run("SnapshotUpsertsByAccountAndDate", func(t *testing.T) {
		s := openDeposit(t, newStore)

		first := deposit.Snapshot{
			AccountID: "dep_1",
			Date:      day(15),
			Balance:   deposit.Balance{Book: 1000, Holds: 100, Available: 900},
			TakenAt:   early,
		}
		second := deposit.Snapshot{
			AccountID: "dep_1",
			// Same business day, later in the day: the same identity, so this
			// replaces the first rather than adding a row.
			Date:    day(15).Add(23 * time.Hour),
			Balance: deposit.Balance{Book: 2000, Holds: 0, Available: 2000},
			TakenAt: early.Add(23 * time.Hour),
		}

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			if err := tx.PutSnapshot(ctx, bookA, first); err != nil {
				return err
			}
			// A different account on the same date is a different snapshot.
			if err := tx.PutSnapshot(ctx, bookA, deposit.Snapshot{AccountID: "dep_2", Date: day(15)}); err != nil {
				return err
			}
			// And the same account on a different date.
			if err := tx.PutSnapshot(ctx, bookA, deposit.Snapshot{AccountID: "dep_1", Date: day(16)}); err != nil {
				return err
			}
			return tx.PutSnapshot(ctx, bookA, second)
		})

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			got, err := tx.GetSnapshot(ctx, bookA, "dep_1", deposit.SnapshotDateKey(day(15)))
			if err != nil {
				return err
			}
			assertEqual(t, "overwritten book balance", got.Balance.Book, ledger.Amount(2000))
			assertEqual(t, "overwritten holds", got.Balance.Holds, ledger.Amount(0))
			assertEqual(t, "overwritten available", got.Balance.Available, ledger.Amount(2000))

			// Two rows for dep_1: 2025-01-15 (overwritten once) and 2025-01-16.
			all, err := tx.ListSnapshotsForAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "snapshots for dep_1", len(all), 2)

			// dep_2's snapshot on the same date is untouched.
			other, err := tx.ListSnapshotsForAccount(ctx, bookA, "dep_2")
			if err != nil {
				return err
			}
			assertEqual(t, "snapshots for dep_2", len(other), 1)
			return nil
		})
	})

	t.Run("UpdateRollsBackDepositAndLedgerWritesTogether", func(t *testing.T) {
		s := openDeposit(t, newStore)

		// The reason deposit.Tx embeds ledger.Tx: a capture writes a hold and
		// posts a GL transaction, and a failure must undo both. Seed one
		// committed hold, then fail a unit of work that touches both layers.
		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			if err := tx.PutDepositAccount(ctx, bookA, account("dep_1", early)); err != nil {
				return err
			}
			return tx.PutHold(ctx, bookA, hold("hld_1", "dep_1", 500, deposit.HoldActive, early, time.Time{}))
		})

		boom := errors.New("storetest: deliberate failure")
		err := s.Update(context.Background(), func(ctx context.Context, tx deposit.Tx) error {
			// Deposit-layer writes.
			captured := hold("hld_1", "dep_1", 500, deposit.HoldCaptured, early, time.Time{})
			if err := tx.PutHold(ctx, bookA, captured); err != nil {
				return err
			}
			if err := tx.PutDepositAccount(ctx, bookA, account("dep_2", early)); err != nil {
				return err
			}
			if err := tx.PutSnapshot(ctx, bookA, snapshot("dep_1", day(15))); err != nil {
				return err
			}
			// Ledger-layer writes through the very same Tx.
			if err := tx.PutTransaction(ctx, bookA, transaction("tx_1", "")); err != nil {
				return err
			}
			if err := tx.AppendAudit(ctx, ledger.AuditEvent{
				ID: "evt_1", BookID: bookA, Scope: ledger.ScopeDeposit, Type: ledger.EventHoldCaptured,
			}); err != nil {
				return err
			}
			return boom
		})
		assertErrorIs(t, "Update return", err, boom)

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			// The hold is back to Active: the status write did not survive.
			h, err := tx.GetHold(ctx, bookA, "hld_1")
			if err != nil {
				return err
			}
			assertEqual(t, "hold status after rollback", h.Status.String(), deposit.HoldActive.String())

			_, err = tx.GetDepositAccount(ctx, bookA, "dep_2")
			assertErrorIs(t, "account written by the failed unit of work", err, deposit.ErrAccountNotFound)

			_, err = tx.GetSnapshot(ctx, bookA, "dep_1", deposit.SnapshotDateKey(day(15)))
			assertErrorIs(t, "snapshot written by the failed unit of work", err, deposit.ErrSnapshotNotFound)

			// And the ledger side rolled back with it.
			_, err = tx.GetTransaction(ctx, bookA, "tx_1")
			assertErrorIs(t, "GL transaction from the failed unit of work", err, ledger.ErrTransactionNotFound)

			events, err := tx.ListAudit(ctx, ledger.AuditFilter{})
			if err != nil {
				return err
			}
			assertEqual(t, "audit events after rollback", len(events), 0)
			return nil
		})
	})

	t.Run("ResetClearsDepositState", func(t *testing.T) {
		s := openDeposit(t, newStore)

		updateDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			if err := tx.PutDepositAccount(ctx, bookA, account("dep_1", early)); err != nil {
				return err
			}
			if err := tx.PutHold(ctx, bookA, hold("hld_1", "dep_1", 500, deposit.HoldActive, early, time.Time{})); err != nil {
				return err
			}
			return tx.PutSnapshot(ctx, bookA, snapshot("dep_1", day(15)))
		})

		if err := s.Reset(context.Background()); err != nil {
			t.Fatalf("Reset: %v", err)
		}

		viewDeposit(t, s, func(ctx context.Context, tx deposit.Tx) error {
			accounts, err := tx.ListDepositAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "deposit accounts after reset", len(accounts), 0)

			holds, err := tx.ListHoldsForAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "holds after reset", len(holds), 0)

			snaps, err := tx.ListSnapshotsForAccount(ctx, bookA, "dep_1")
			if err != nil {
				return err
			}
			assertEqual(t, "snapshots after reset", len(snaps), 0)

			total, err := tx.ActiveHoldTotal(ctx, bookA, "dep_1", early)
			if err != nil {
				return err
			}
			assertEqual(t, "active hold total after reset", total, ledger.Amount(0))
			return nil
		})
	})
}

// ---------------------------------------------------------------------------
// Deposit helpers
// ---------------------------------------------------------------------------

// early is the base instant for the deposit fixtures, matching the one the
// ledger suite uses.
var early = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

// day builds a business date in January 2025, the granularity snapshots are
// identified at.
func day(n int) time.Time { return time.Date(2025, 1, n, 0, 0, 0, 0, time.UTC) }

func account(id deposit.AccountID, createdAt time.Time) deposit.Account {
	return deposit.Account{
		ID:        id,
		GLAccount: "200.100.001",
		Name:      string(id),
		Status:    deposit.Active,
		CreatedAt: createdAt,
	}
}

func hold(id deposit.HoldID, acct deposit.AccountID, amount ledger.Amount, status deposit.HoldStatus, createdAt, expiresAt time.Time) deposit.Hold {
	return deposit.Hold{
		ID:        id,
		AccountID: acct,
		Amount:    amount,
		ExpiresAt: expiresAt,
		Status:    status,
		CreatedAt: createdAt,
	}
}

func snapshot(acct deposit.AccountID, date time.Time) deposit.Snapshot {
	return deposit.Snapshot{
		AccountID: acct,
		Date:      date,
		Balance:   deposit.Balance{Book: 1000, Holds: 100, Available: 900},
		TakenAt:   early,
	}
}

// openDeposit builds a fresh store for one subtest and closes it when the
// subtest ends.
func openDeposit(t *testing.T, newStore func(*testing.T) deposit.Store) deposit.Store {
	t.Helper()
	s := newStore(t)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// updateDeposit runs a unit of work that is expected to succeed.
func updateDeposit(t *testing.T, s deposit.Store, fn func(context.Context, deposit.Tx) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// viewDeposit runs a read-only unit of work that is expected to succeed.
func viewDeposit(t *testing.T, s deposit.Store, fn func(context.Context, deposit.Tx) error) {
	t.Helper()
	if err := s.View(context.Background(), fn); err != nil {
		t.Fatalf("View: %v", err)
	}
}
