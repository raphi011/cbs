// Package storetest is the shared suite a store must pass: RunLedger for the
// ledger layer, RunDeposit for the deposit layer, RunPayment for the payment
// layer, and RunProduct and RunLending for theirs.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Books used throughout the suite. Two of them, because almost every
// store-level guarantee is per book and the interesting failure mode is a
// implementation that leaks state across books.
const (
	bookA ledger.BookID = "book-a"
	bookB ledger.BookID = "book-b"
)

// RunLedger runs the ledger-layer suite against a store.
func RunLedger(t *testing.T, newStore func(*testing.T, ledger.BookID) ledger.BankStore) {
	t.Helper()

	t.Run("NextSubledgerBlockStepsBy100PerBook", func(t *testing.T) {
		// TWO STORES, because a store answers for one book and refuses the rest.
		s := open(t, newStore, bookA)
		other := open(t, newStore, bookB)

		var gotA []int
		var gotB []int
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			for range 3 {
				block, err := tx.NextSubledgerBlock(ctx, bookA)
				if err != nil {
					return err
				}
				gotA = append(gotA, block)
			}
			return nil
		})
		update(t, other, func(ctx context.Context, tx ledger.BankTx) error {
			block, err := tx.NextSubledgerBlock(ctx, bookB)
			if err != nil {
				return err
			}
			gotB = append(gotB, block)
			return nil
		})

		assertEqual(t, "book-a blocks", sliceString(gotA), "[100 200 300]")
		// A second book starts its own chart of accounts at 100.
		assertEqual(t, "book-b blocks", sliceString(gotB), "[100]")
	})

	t.Run("NextIDSharesOneCounterPerBook", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// ONE counter per book, shared by every prefix — not one counter per prefix.
		// The number doubles as a creation order, which is why ldg_1, tx_2 and ent_3
		// interleave rather than each restarting at 1.
		other := open(t, newStore, bookB)

		var inA, inB []string
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			for _, prefix := range []string{"ldg", "tx", "ent", "evt", "tx"} {
				id, err := tx.NextID(ctx, bookA, prefix)
				if err != nil {
					return err
				}
				inA = append(inA, id)
			}
			return nil
		})
		// A second book numbers independently, from its own 1 — in a database of
		// its own, which is where a second book is.
		update(t, other, func(ctx context.Context, tx ledger.BankTx) error {
			id, err := tx.NextID(ctx, bookB, "tx")
			if err != nil {
				return err
			}
			inB = append(inB, id)
			return nil
		})

		assertEqual(t, "ids from one shared counter", sliceString(inA), "[ldg_1 tx_2 ent_3 evt_4 tx_5]")
		assertEqual(t, "a second book starts again at 1", sliceString(inB), "[tx_1]")
	})

	t.Run("TransactionEntriesKeepTheirOrder", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// Transaction.Entries is an ordered slice, and the order is meaningful: it is
		// the order the legs were written in, and it is what a settlement
		// transaction's determinism rests on.
		want := []ledger.Entry{
			{ID: "ent_10", AccountID: "100.100.001", Amount: 400, Direction: ledger.Debit},
			{ID: "ent_8", AccountID: "200.100.001", Amount: 100, Direction: ledger.Credit},
			{ID: "ent_20", AccountID: "200.100.002", Amount: 250, Direction: ledger.Credit},
			{ID: "ent_9", AccountID: "200.100.003", Amount: 50, Direction: ledger.Credit},
		}
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutTransaction(ctx, bookA, ledger.Transaction{
				ID: "tx_1", Status: ledger.Posted, Entries: want,
				CreatedAt: time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
			})
		})

		check := func(label string, got []ledger.Entry) {
			t.Helper()
			assertOrder(t, label, ids(got, func(e ledger.Entry) string { return string(e.ID) }),
				"ent_10", "ent_8", "ent_20", "ent_9")
			for i := range got {
				if i >= len(want) {
					return
				}
				// The legs travel together: an order that shuffles amounts onto
				// the wrong accounts is worse than one that merely reorders.
				assertEqual(t, label+" account "+string(got[i].ID), string(got[i].AccountID), string(want[i].AccountID))
				assertEqual(t, label+" amount "+string(got[i].ID), got[i].Amount, want[i].Amount)
			}
		}

		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			one, err := tx.GetTransaction(ctx, bookA, "tx_1")
			if err != nil {
				return err
			}
			check("GetTransaction entries", one.Entries)

			all, err := tx.ListTransactions(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "transactions listed", len(all), 1)
			check("ListTransactions entries", all[0].Entries)

			forAccount, err := tx.ListTransactionsForPosition(ctx, bookA, ledger.AccountID("200.100.002").Total())
			if err != nil {
				return err
			}
			assertEqual(t, "transactions for the account", len(forAccount), 1)
			// Every leg comes back, not only the ones naming the account.
			check("ListTransactionsForPosition entries", forAccount[0].Entries)
			return nil
		})
	})

	t.Run("AccountRoundTripsItsControlFlag", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// Whether an account pools subsidiaries decides which entries the domain will
		// accept against it, both ways round.
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			if err := tx.PutAccount(ctx, bookA, ledger.Account{
				ID: "200.100.001", SubledgerID: "100", Name: "Customer Deposits",
				Type: ledger.Liability, Asset: "EUR", Control: true,
			}); err != nil {
				return err
			}
			return tx.PutAccount(ctx, bookA, ledger.Account{
				ID: "100.100.001", SubledgerID: "100", Name: "Vault Cash",
				Type: ledger.Asset, Asset: "EUR",
			})
		})

		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			pooled, err := tx.GetAccount(ctx, bookA, "200.100.001")
			if err != nil {
				return err
			}
			assertEqual(t, "the pooling account's flag", pooled.Control, true)
			plain, err := tx.GetAccount(ctx, bookA, "100.100.001")
			if err != nil {
				return err
			}
			assertEqual(t, "the plain account's flag", plain.Control, false)

			list, err := tx.ListAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "accounts listed", len(list), 2)
			for _, a := range list {
				assertEqual(t, "listed "+string(a.ID)+"'s flag", a.Control, a.ID == "200.100.001")
			}
			return nil
		})
	})

	t.Run("SlotAccountsUpsertPerProductAndAreBookScoped", func(t *testing.T) {
		s := open(t, newStore, bookA)
		other := open(t, newStore, bookB)

		// The mapping is keyed by (product, slot, asset) and the bank-wide row is the
		// one with an empty product.
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			for _, row := range []ledger.SlotAccount{
				{Slot: "deposit.principal", Asset: "EUR", Account: "200.100.001"},
				{Slot: "deposit.principal", Asset: "BTC", Account: "200.100.002"},
				{Slot: "deposit.interest_income", Asset: "EUR", Account: "400.300.001"},
				{Product: "prd_savings", Slot: "deposit.interest_income", Asset: "EUR", Account: "400.300.002"},
			} {
				if err := tx.PutSlotAccount(ctx, bookA, row); err != nil {
					return err
				}
			}
			// Repointing a slot is the same act as pointing it.
			return tx.PutSlotAccount(ctx, bookA, ledger.SlotAccount{
				Slot: "deposit.principal", Asset: "EUR", Account: "200.100.009",
			})
		})
		update(t, other, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutSlotAccount(ctx, bookB, ledger.SlotAccount{
				Slot: "deposit.principal", Asset: "EUR", Account: "200.500.001",
			})
		})

		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			repointed, err := tx.GetSlotAccount(ctx, bookA, "", "deposit.principal", "EUR")
			if err != nil {
				return err
			}
			assertEqual(t, "the repointed slot", string(repointed), "200.100.009")

			perProduct, err := tx.GetSlotAccount(ctx, bookA, "prd_savings", "deposit.interest_income", "EUR")
			if err != nil {
				return err
			}
			assertEqual(t, "the product's own line", string(perProduct), "400.300.002")

			// A product with no row of its own is NOT the bank-wide row here.
			_, err = tx.GetSlotAccount(ctx, bookA, "prd_basic", "deposit.interest_income", "EUR")
			assertErrorIs(t, "an unmapped product", err, ledger.ErrSlotNotMapped)

			_, err = tx.GetSlotAccount(ctx, bookA, "", "deposit.principal", "USD")
			assertErrorIs(t, "an unmapped asset", err, ledger.ErrSlotNotMapped)

			// Ordered by slot, then product, then asset — a configuration
			// listing, so nothing about when a row was written belongs in it.
			list, err := tx.ListSlotAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			var got []string
			for _, row := range list {
				got = append(got, row.Slot+"/"+row.Product+"/"+string(row.Asset)+"="+string(row.Account))
			}
			assertEqual(t, "the mapping", sliceString(got),
				"[deposit.interest_income//EUR=400.300.001 deposit.interest_income/prd_savings/EUR=400.300.002 "+
					"deposit.principal//BTC=200.100.002 deposit.principal//EUR=200.100.009]")
			return nil
		})

		// Another institution's database answers for its own mapping and holds
		// none of this one's.
		view(t, other, func(ctx context.Context, tx ledger.BankTx) error {
			mine, err := tx.GetSlotAccount(ctx, bookB, "", "deposit.principal", "EUR")
			if err != nil {
				return err
			}
			assertEqual(t, "the other bank's line", string(mine), "200.500.001")
			list, err := tx.ListSlotAccounts(ctx, bookB)
			if err != nil {
				return err
			}
			assertEqual(t, "rows in the other bank's mapping", len(list), 1)
			return nil
		})
	})

	t.Run("SubsidiaryScopedReadsSumToTheWholeAccount", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// One control account, three subsidiaries, and the reads that have to tell
		// them apart.
		const (
			pooled ledger.AccountID = "200.100.001"
			contra ledger.AccountID = "100.100.001"
		)
		day := func(n int) time.Time {
			return time.Date(2025, 1, n, 12, 0, 0, 0, time.UTC)
		}
		post := func(id ledger.TransactionID, subsidiary string, amount ledger.Amount, on time.Time) {
			t.Helper()
			update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
				return tx.PutTransaction(ctx, bookA, ledger.Transaction{
					ID: id, Status: ledger.Posted, CreatedAt: on,
					Entries: []ledger.Entry{
						{ID: ledger.EntryID(string(id) + "_a"), AccountID: contra,
							Amount: amount, Direction: ledger.Debit, ValueDate: on},
						{ID: ledger.EntryID(string(id) + "_b"), AccountID: pooled, Subsidiary: subsidiary,
							Amount: amount, Direction: ledger.Credit, ValueDate: on},
					},
				})
			})
		}
		post("tx_1", "dep_1", 1000, day(4))
		post("tx_2", "dep_2", 250, day(4))
		post("tx_3", "dep_1", 40, day(6))

		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			// The dimension is on the entry it was written on, and only on it.
			one, err := tx.GetTransaction(ctx, bookA, "tx_1")
			if err != nil {
				return err
			}
			assertEqual(t, "the pooled leg's subsidiary", one.Entries[1].Subsidiary, "dep_1")
			assertEqual(t, "the plain leg's subsidiary", one.Entries[0].Subsidiary, "")

			// The pool and one subsidiary within it are the same scan with one
			// clause dropped, which is the claim the arithmetic rests on.
			assertEqual(t, "entries in the pool", scanned(t, s, pooled.Total(), ledger.EntryFilter{}),
				"[tx_1_b tx_2_b tx_3_b]")
			assertEqual(t, "entries under dep_1", scanned(t, s, pooled.For("dep_1"), ledger.EntryFilter{}),
				"[tx_1_b tx_3_b]")
			assertEqual(t, "entries under a subsidiary with no postings",
				scanned(t, s, pooled.For("dep_9"), ledger.EntryFilter{}), "[]")

			// And the listing: one subsidiary's statement is their own postings,
			// each carrying the other leg of its own event.
			forSubsidiary, err := tx.ListTransactionsForPosition(ctx, bookA, pooled.For("dep_1"))
			if err != nil {
				return err
			}
			assertOrder(t, "dep_1's transactions",
				ids(forSubsidiary, func(t ledger.Transaction) string { return string(t.ID) }), "tx_1", "tx_3")
			assertEqual(t, "legs on dep_1's first transaction", len(forSubsidiary[0].Entries), 2)

			forWhole, err := tx.ListTransactionsForPosition(ctx, bookA, pooled.Total())
			if err != nil {
				return err
			}
			assertEqual(t, "the pool's transactions", len(forWhole), 3)
			return nil
		})
	})

	t.Run("EntryValueDateDefaultsToTransaction", func(t *testing.T) {
		s := open(t, newStore, bookA)

		ctx := context.Background()
		value := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
		var got ledger.Transaction
		if err := s.Update(ctx, func(ctx context.Context, tx ledger.BankTx) error {
			err := tx.PutTransaction(ctx, bookA, ledger.Transaction{
				ID:        "txn_vd_default",
				ValueDate: value,
				Entries: []ledger.Entry{
					{ID: "ent_1", AccountID: "100.001.001", Amount: 500, Direction: ledger.Debit, ValueDate: value},
					{ID: "ent_2", AccountID: "200.001.001", Amount: 500, Direction: ledger.Credit, ValueDate: value},
				},
			})
			if err != nil {
				return err
			}
			got, err = tx.GetTransaction(ctx, bookA, "txn_vd_default")
			return err
		}); err != nil {
			t.Fatalf("put/get: %v", err)
		}
		for i, e := range got.Entries {
			if !e.ValueDate.Equal(value) {
				t.Errorf("entry %d value date = %v, want %v", i, e.ValueDate, value)
			}
		}
	})

	t.Run("EntriesKeepDivergentValueDates", func(t *testing.T) {
		s := open(t, newStore, bookA)

		ctx := context.Background()
		early := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
		late := time.Date(2026, 3, 17, 0, 0, 0, 0, time.UTC)
		var got ledger.Transaction
		if err := s.Update(ctx, func(ctx context.Context, tx ledger.BankTx) error {
			err := tx.PutTransaction(ctx, bookA, ledger.Transaction{
				ID:        "txn_vd_split",
				ValueDate: late,
				Entries: []ledger.Entry{
					{ID: "ent_3", AccountID: "100.001.001", Amount: 500, Direction: ledger.Debit, ValueDate: early},
					{ID: "ent_4", AccountID: "200.001.001", Amount: 500, Direction: ledger.Credit, ValueDate: late},
				},
			})
			if err != nil {
				return err
			}
			got, err = tx.GetTransaction(ctx, bookA, "txn_vd_split")
			return err
		}); err != nil {
			t.Fatalf("put/get: %v", err)
		}
		if len(got.Entries) != 2 {
			t.Fatalf("entries = %d, want 2", len(got.Entries))
		}
		if !got.Entries[0].ValueDate.Equal(early) {
			t.Errorf("leg 0 value date = %v, want %v", got.Entries[0].ValueDate, early)
		}
		if !got.Entries[1].ValueDate.Equal(late) {
			t.Errorf("leg 1 value date = %v, want %v", got.Entries[1].ValueDate, late)
		}
	})

	t.Run("NextAccountSeqResetsPerTypeAndSubledger", func(t *testing.T) {
		s := open(t, newStore, bookA)

		var liabilityIn100, assetIn100, liabilityIn200 []int
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			for range 2 {
				n, err := tx.NextAccountSeq(ctx, bookA, 200, "100")
				if err != nil {
					return err
				}
				liabilityIn100 = append(liabilityIn100, n)
			}
			// Same subledger, different type block: independent counter.
			n, err := tx.NextAccountSeq(ctx, bookA, 100, "100")
			if err != nil {
				return err
			}
			assetIn100 = append(assetIn100, n)
			// Same type block, different subledger: independent counter.
			n, err = tx.NextAccountSeq(ctx, bookA, 200, "200")
			if err != nil {
				return err
			}
			liabilityIn200 = append(liabilityIn200, n)
			return nil
		})

		assertEqual(t, "(200,100) sequence", sliceString(liabilityIn100), "[1 2]")
		assertEqual(t, "(100,100) sequence", sliceString(assetIn100), "[1]")
		assertEqual(t, "(200,200) sequence", sliceString(liabilityIn200), "[1]")
	})

	t.Run("SameIDsInDifferentBooks", func(t *testing.T) {
		// TWO STORES, and this is the case where the change says the most.
		s := open(t, newStore, bookA)
		other := open(t, newStore, bookB)

		const shared ledger.AccountID = "200.100.001"
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutAccount(ctx, bookA, ledger.Account{ID: shared, Name: "Alice at A", Type: ledger.Liability})
		})
		update(t, other, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutAccount(ctx, bookB, ledger.Account{ID: shared, Name: "Bob at B", Type: ledger.Liability})
		})

		var inA, inB ledger.Account
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			var err error
			inA, err = tx.GetAccount(ctx, bookA, shared)
			return err
		})
		view(t, other, func(ctx context.Context, tx ledger.BankTx) error {
			var err error
			inB, err = tx.GetAccount(ctx, bookB, shared)
			return err
		})

		assertEqual(t, "account in book-a", inA.Name, "Alice at A")
		assertEqual(t, "account in book-b", inB.Name, "Bob at B")

		// Listing one book must not show the other book's rows.
		var listed []ledger.Account
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			var err error
			listed, err = tx.ListAccounts(ctx, bookA)
			return err
		})
		assertEqual(t, "accounts listed for book-a", len(listed), 1)
	})

	// An account's asset is what every per-asset rule above the store reads, so
	// a store that drops it — an insert that forgets the column, a scan that
	// forgets the field — turns every account into an account in nothing.
	t.Run("AccountRoundTripsItsAsset", func(t *testing.T) {
		s := open(t, newStore, bookA)

		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutAccount(ctx, bookA, ledger.Account{
				ID: "100.custody.001", SubledgerID: "custody", Name: "Custody",
				Type: ledger.Asset, Asset: "BTC",
			})
		})

		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			got, err := tx.GetAccount(ctx, bookA, "100.custody.001")
			if err != nil {
				return err
			}
			if got.Asset != "BTC" {
				t.Errorf("account asset = %q, want BTC", got.Asset)
			}
			list, err := tx.ListAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			if len(list) != 1 || list[0].Asset != "BTC" {
				t.Errorf("ListAccounts = %+v, want one BTC account", list)
			}
			return nil
		})
	})

	t.Run("GetOnMissingRowsReturnsSentinels", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// A store that reports "not found" as anything other than the domain sentinel
		// — a wrapped sql.ErrNoRows, say — turns every 404 in the API into a 500, and
		// turns a typo'd account ID in PostTransaction into an internal error instead
		// of ledger.ErrAccountNotFound.
		early := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			if err := tx.PutLedger(ctx, bookA, ledger.Ledger{ID: "ldg_1", Name: "GL", CreatedAt: early}); err != nil {
				return err
			}
			if err := tx.PutSubledger(ctx, bookA, ledger.Subledger{ID: "100", LedgerID: "ldg_1", CreatedAt: early}); err != nil {
				return err
			}
			if err := tx.PutAccount(ctx, bookA, ledger.Account{ID: "200.100.001", SubledgerID: "100", Type: ledger.Liability, CreatedAt: early}); err != nil {
				return err
			}
			return tx.PutTransaction(ctx, bookA, transaction("tx_1", "key-1"))
		})

		// Unknown IDs, in a book that does have rows of every kind.
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			_, err := tx.GetLedger(ctx, bookA, "ldg_nope")
			assertErrorIs(t, "GetLedger on an unknown ledger", err, ledger.ErrLedgerNotFound)

			_, err = tx.GetSubledger(ctx, bookA, "999")
			assertErrorIs(t, "GetSubledger on an unknown subledger", err, ledger.ErrSubledgerNotFound)

			_, err = tx.GetAccount(ctx, bookA, "999.999.999")
			assertErrorIs(t, "GetAccount on an unknown account", err, ledger.ErrAccountNotFound)

			_, err = tx.GetTransaction(ctx, bookA, "tx_nope")
			assertErrorIs(t, "GetTransaction on an unknown transaction", err, ledger.ErrTransactionNotFound)

			_, err = tx.GetTransactionByIdempotencyKey(ctx, bookA, "key-nope")
			assertErrorIs(t, "GetTransactionByIdempotencyKey on an unused key", err, ledger.ErrTransactionNotFound)
			return nil
		})

		// The same IDs in ANOTHER STORE are equally not found. What this catches is
		// a second bank's database answering with the first bank's rows, which is
		// what the whole split is for.
		other := open(t, newStore, bookB)
		view(t, other, func(ctx context.Context, tx ledger.BankTx) error {
			_, err := tx.GetLedger(ctx, bookB, "ldg_1")
			assertErrorIs(t, "GetLedger across books", err, ledger.ErrLedgerNotFound)

			_, err = tx.GetSubledger(ctx, bookB, "100")
			assertErrorIs(t, "GetSubledger across books", err, ledger.ErrSubledgerNotFound)

			_, err = tx.GetAccount(ctx, bookB, "200.100.001")
			assertErrorIs(t, "GetAccount across books", err, ledger.ErrAccountNotFound)

			_, err = tx.GetTransaction(ctx, bookB, "tx_1")
			assertErrorIs(t, "GetTransaction across books", err, ledger.ErrTransactionNotFound)

			_, err = tx.GetTransactionByIdempotencyKey(ctx, bookB, "key-1")
			assertErrorIs(t, "GetTransactionByIdempotencyKey across books", err, ledger.ErrTransactionNotFound)
			return nil
		})

		// And in an entirely empty book, where the tables hold nothing at all.
		// It is a third STORE for the reason above, and a book of its own.
		empty := open(t, newStore, "book-empty")
		view(t, empty, func(ctx context.Context, tx ledger.BankTx) error {
			_, err := tx.GetLedger(ctx, "book-empty", "ldg_1")
			assertErrorIs(t, "GetLedger in an empty book", err, ledger.ErrLedgerNotFound)

			_, err = tx.GetSubledger(ctx, "book-empty", "100")
			assertErrorIs(t, "GetSubledger in an empty book", err, ledger.ErrSubledgerNotFound)

			_, err = tx.GetAccount(ctx, "book-empty", "200.100.001")
			assertErrorIs(t, "GetAccount in an empty book", err, ledger.ErrAccountNotFound)

			_, err = tx.GetTransaction(ctx, "book-empty", "tx_1")
			assertErrorIs(t, "GetTransaction in an empty book", err, ledger.ErrTransactionNotFound)

			_, err = tx.GetTransactionByIdempotencyKey(ctx, "book-empty", "key-1")
			assertErrorIs(t, "GetTransactionByIdempotencyKey in an empty book", err, ledger.ErrTransactionNotFound)
			return nil
		})
	})

	t.Run("ParentReferencesAreNotEnforced", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// A store is a per-table key/value layer.
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			// A subledger under a ledger that does not exist.
			if err := tx.PutSubledger(ctx, bookA, ledger.Subledger{
				ID: "100", LedgerID: "ldg_nope", Name: "orphan", CreatedAt: early,
			}); err != nil {
				return err
			}
			// An account under a subledger that does not exist, and one with no
			// subledger at all.
			if err := tx.PutAccount(ctx, bookA, ledger.Account{
				ID: "200.900.001", SubledgerID: "900", Type: ledger.Liability, Name: "orphan", CreatedAt: early,
			}); err != nil {
				return err
			}
			if err := tx.PutAccount(ctx, bookA, ledger.Account{
				ID: "200.900.002", Type: ledger.Liability, Name: "no subledger", CreatedAt: early,
			}); err != nil {
				return err
			}
			// A transaction whose legs name accounts that do not exist.
			return tx.PutTransaction(ctx, bookA, ledger.Transaction{
				ID: "tx_1", Status: ledger.Posted, CreatedAt: early,
				Entries: []ledger.Entry{
					{ID: "ent_1", AccountID: "999.999.001", Amount: 100, Direction: ledger.Debit},
					{ID: "ent_2", AccountID: "999.999.002", Amount: 100, Direction: ledger.Credit},
				},
			})
		})

		// The orphans are readable, and the aggregate over an account that was
		// never created still adds up — a balance is a sum over entries, not a
		// join to a chart of accounts.
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			sl, err := tx.GetSubledger(ctx, bookA, "100")
			if err != nil {
				return err
			}
			assertEqual(t, "orphan subledger's ledger", string(sl.LedgerID), "ldg_nope")

			a, err := tx.GetAccount(ctx, bookA, "200.900.001")
			if err != nil {
				return err
			}
			assertEqual(t, "orphan account's subledger", string(a.SubledgerID), "900")

			balance, err := ledger.BookBalance(ctx, tx, bookA, ledger.AccountID("999.999.001").Total(), ledger.Debit)
			if err != nil {
				return err
			}
			assertEqual(t, "balance of an account that was never created", balance, ledger.Amount(100))
			return nil
		})
	})

	t.Run("ListOrderingIsCreatedAtThenSeq", func(t *testing.T) {
		s := open(t, newStore, bookA)

		early := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
		late := early.Add(time.Hour)

		// Every listing is ordered by CreatedAt, ties broken by the row's insertion
		// sequence — all five of them, not just the first.
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			for _, l := range []ledger.Ledger{
				{ID: "ldg_10", Name: "latest, inserted first", CreatedAt: late},
				{ID: "ldg_8", Name: "first", CreatedAt: early},
				{ID: "ldg_20", Name: "second", CreatedAt: early},
				{ID: "ldg_9", Name: "third", CreatedAt: early},
			} {
				if err := tx.PutLedger(ctx, bookA, l); err != nil {
					return err
				}
			}
			// Chart-of-accounts blocks have the same problem: "100" < "50".
			for _, sl := range []ledger.Subledger{
				{ID: "300", LedgerID: "ldg_8", Name: "latest, inserted first", CreatedAt: late},
				{ID: "100", LedgerID: "ldg_8", Name: "first", CreatedAt: early},
				{ID: "50", LedgerID: "ldg_8", Name: "second", CreatedAt: early},
				{ID: "200", LedgerID: "ldg_8", Name: "third", CreatedAt: early},
			} {
				if err := tx.PutSubledger(ctx, bookA, sl); err != nil {
					return err
				}
			}
			// The chart-of-accounts trap: 100.200.001 sorts first by ID but was
			// opened an hour after the 200.100.xxx accounts, and among those
			// 200.100.020 was opened before 200.100.009.
			for _, a := range []ledger.Account{
				{ID: "100.200.001", SubledgerID: "200", Type: ledger.Asset, Name: "latest, inserted first", CreatedAt: late},
				{ID: "200.100.008", SubledgerID: "100", Type: ledger.Liability, Name: "first", CreatedAt: early},
				{ID: "200.100.020", SubledgerID: "100", Type: ledger.Liability, Name: "second", CreatedAt: early},
				{ID: "200.100.009", SubledgerID: "100", Type: ledger.Liability, Name: "third", CreatedAt: early},
			} {
				if err := tx.PutAccount(ctx, bookA, a); err != nil {
					return err
				}
			}
			// tx_z touches another account, so the per-account listing keeps the
			// same trap rather than accidentally being in ID order.
			for _, txn := range []ledger.Transaction{
				{ID: "tx_10", Status: ledger.Posted, CreatedAt: late, Entries: []ledger.Entry{
					{ID: "ent_10", AccountID: "200.100.008", Amount: 100, Direction: ledger.Debit},
				}},
				{ID: "tx_8", Status: ledger.Posted, CreatedAt: early, Entries: []ledger.Entry{
					{ID: "ent_8", AccountID: "200.100.008", Amount: 100, Direction: ledger.Debit},
				}},
				{ID: "tx_z", Status: ledger.Posted, CreatedAt: early, Entries: []ledger.Entry{
					{ID: "ent_z", AccountID: "999.999.999", Amount: 100, Direction: ledger.Debit},
				}},
				{ID: "tx_20", Status: ledger.Posted, CreatedAt: early, Entries: []ledger.Entry{
					{ID: "ent_20", AccountID: "200.100.008", Amount: 100, Direction: ledger.Debit},
				}},
				{ID: "tx_9", Status: ledger.Posted, CreatedAt: early, Entries: []ledger.Entry{
					{ID: "ent_9", AccountID: "200.100.008", Amount: 100, Direction: ledger.Debit},
				}},
			} {
				if err := tx.PutTransaction(ctx, bookA, txn); err != nil {
					return err
				}
			}
			return nil
		})

		var ledgers []ledger.Ledger
		var subledgers []ledger.Subledger
		var accounts []ledger.Account
		var transactions, forAccount []ledger.Transaction
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			var err error
			if ledgers, err = tx.ListLedgers(ctx, bookA); err != nil {
				return err
			}
			if subledgers, err = tx.ListSubledgers(ctx, bookA); err != nil {
				return err
			}
			if accounts, err = tx.ListAccounts(ctx, bookA); err != nil {
				return err
			}
			if transactions, err = tx.ListTransactions(ctx, bookA); err != nil {
				return err
			}
			forAccount, err = tx.ListTransactionsForPosition(ctx, bookA, ledger.AccountID("200.100.008").Total())
			return err
		})

		assertOrder(t, "ListLedgers", ids(ledgers, func(l ledger.Ledger) string { return string(l.ID) }),
			"ldg_8", "ldg_20", "ldg_9", "ldg_10")

		assertOrder(t, "ListSubledgers", ids(subledgers, func(sl ledger.Subledger) string { return string(sl.ID) }),
			"100", "50", "200", "300")

		assertOrder(t, "ListAccounts", ids(accounts, func(a ledger.Account) string { return string(a.ID) }),
			"200.100.008", "200.100.020", "200.100.009", "100.200.001")

		assertOrder(t, "ListTransactions", ids(transactions, func(txn ledger.Transaction) string { return string(txn.ID) }),
			"tx_8", "tx_z", "tx_20", "tx_9", "tx_10")

		// The per-account listing is the same order, minus the transaction that
		// never touches the account.
		assertOrder(t, "ListTransactionsForAccount", ids(forAccount, func(txn ledger.Transaction) string { return string(txn.ID) }),
			"tx_8", "tx_20", "tx_9", "tx_10")

		// An upsert keeps a row where it was: marking a transaction reversed, or
		// re-putting an account with a new status, must not move it to the end.
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			if err := tx.PutAccount(ctx, bookA, ledger.Account{
				ID: "200.100.008", SubledgerID: "100", Type: ledger.Liability, Name: "renamed", CreatedAt: early,
			}); err != nil {
				return err
			}
			if err := tx.PutLedger(ctx, bookA, ledger.Ledger{
				ID: "ldg_8", Name: "renamed", CreatedAt: early,
			}); err != nil {
				return err
			}
			return tx.PutSubledger(ctx, bookA, ledger.Subledger{
				ID: "100", LedgerID: "ldg_8", Name: "renamed", CreatedAt: early,
			})
		})
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			reordered, err := tx.ListAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			assertOrder(t, "ListAccounts after an upsert", ids(reordered, func(a ledger.Account) string { return string(a.ID) }),
				"200.100.008", "200.100.020", "200.100.009", "100.200.001")

			reledgered, err := tx.ListLedgers(ctx, bookA)
			if err != nil {
				return err
			}
			assertOrder(t, "ListLedgers after an upsert", ids(reledgered, func(l ledger.Ledger) string { return string(l.ID) }),
				"ldg_8", "ldg_20", "ldg_9", "ldg_10")

			resubled, err := tx.ListSubledgers(ctx, bookA)
			if err != nil {
				return err
			}
			assertOrder(t, "ListSubledgers after an upsert", ids(resubled, func(sl ledger.Subledger) string { return string(sl.ID) }),
				"100", "50", "200", "300")
			return nil
		})
	})

	t.Run("DuplicateIdempotencyKeyRejected", func(t *testing.T) {
		s := open(t, newStore, bookA)

		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_1", "key-1"))
		})

		// A second, different transaction claiming the same key is refused by
		// the store itself, not just by a caller's pre-check.
		err := s.Update(context.Background(), func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_2", "key-1"))
		})
		assertErrorIs(t, "second put with the same key", err, ledger.ErrDuplicateIdempotencyKey)

		var found ledger.Transaction
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			var err error
			found, err = tx.GetTransactionByIdempotencyKey(ctx, bookA, "key-1")
			return err
		})
		assertEqual(t, "transaction behind key-1", string(found.ID), "tx_1")

		// Keys are per book, so the same key is free in another book — which is
		// another bank's database, and the index that enforces this is that
		// database's own.
		other := open(t, newStore, bookB)
		update(t, other, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutTransaction(ctx, bookB, transaction("tx_9", "key-1"))
		})
	})

	t.Run("RePuttingATransactionReleasesItsOldIdempotencyKey", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// PutTransaction is an upsert, and an upsert may change the key.
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_1", "key-1"))
		})
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_1", "key-2"))
		})

		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			_, err := tx.GetTransactionByIdempotencyKey(ctx, bookA, "key-1")
			assertErrorIs(t, "lookup by the key that was replaced", err, ledger.ErrTransactionNotFound)

			got, err := tx.GetTransactionByIdempotencyKey(ctx, bookA, "key-2")
			if err != nil {
				return err
			}
			assertEqual(t, "transaction behind the new key", string(got.ID), "tx_1")
			assertEqual(t, "the transaction's stored key", got.IdempotencyKey, "key-2")
			return nil
		})

		// The released key is free for a different transaction to claim.
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_2", "key-1"))
		})
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			got, err := tx.GetTransactionByIdempotencyKey(ctx, bookA, "key-1")
			if err != nil {
				return err
			}
			assertEqual(t, "transaction behind the reclaimed key", string(got.ID), "tx_2")

			all, err := tx.ListTransactions(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "transactions stored", len(all), 2)
			return nil
		})

		// Dropping a key entirely releases it too.
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_2", ""))
		})
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			_, err := tx.GetTransactionByIdempotencyKey(ctx, bookA, "key-1")
			assertErrorIs(t, "lookup by a key that was cleared", err, ledger.ErrTransactionNotFound)
			return nil
		})
	})

	t.Run("HandledDuplicateKeyLeavesTheUnitOfWorkUsable", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// ErrDuplicateIdempotencyKey is a domain sentinel, which means a caller is
		// entitled to handle it and carry on — retry under a fresh key, fall back to
		// the existing transaction, record the collision.
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_1", "key-1"))
		})

		err := s.Update(context.Background(), func(ctx context.Context, tx ledger.BankTx) error {
			dup := tx.PutTransaction(ctx, bookA, transaction("tx_2", "key-1"))
			if !errors.Is(dup, ledger.ErrDuplicateIdempotencyKey) {
				return fmt.Errorf("storetest: duplicate put returned %v, want ErrDuplicateIdempotencyKey", dup)
			}
			// Everything after the handled error must still work: a read, a
			// write of another kind, and a second transaction under a free key.
			if _, err := tx.GetTransaction(ctx, bookA, "tx_1"); err != nil {
				return fmt.Errorf("storetest: read after a handled duplicate: %w", err)
			}
			if err := tx.PutLedger(ctx, bookA, ledger.Ledger{ID: "ldg_1", Name: "GL", CreatedAt: early}); err != nil {
				return fmt.Errorf("storetest: write after a handled duplicate: %w", err)
			}
			if err := tx.PutTransaction(ctx, bookA, transaction("tx_3", "key-2")); err != nil {
				return fmt.Errorf("storetest: retry under a free key: %w", err)
			}
			return tx.AppendAudit(ctx, ledger.AuditEvent{
				ID: "evt_1", BookID: bookA, Scope: ledger.ScopeLedger, Type: ledger.EventLedgerCreated,
			})
		})
		if err != nil {
			t.Fatalf("Update after a handled duplicate key: %v", err)
		}

		// And all of it committed: the sentinel cost the caller one statement,
		// not the unit of work.
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			ledgers, err := tx.ListLedgers(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "ledgers committed after a handled duplicate", len(ledgers), 1)

			all, err := tx.ListTransactions(ctx, bookA)
			if err != nil {
				return err
			}
			assertOrder(t, "transactions after a handled duplicate",
				ids(all, func(txn ledger.Transaction) string { return string(txn.ID) }), "tx_1", "tx_3")

			// The refused write left nothing behind — not even a row with no
			// key, which a store that inserted first and cleaned up later could
			// plausibly leave.
			_, err = tx.GetTransaction(ctx, bookA, "tx_2")
			assertErrorIs(t, "the refused transaction", err, ledger.ErrTransactionNotFound)

			behind, err := tx.GetTransactionByIdempotencyKey(ctx, bookA, "key-1")
			if err != nil {
				return err
			}
			assertEqual(t, "transaction still behind key-1", string(behind.ID), "tx_1")

			events, err := tx.ListAudit(ctx, ledger.AuditFilter{})
			if err != nil {
				return err
			}
			assertEqual(t, "audit events committed", len(events), 1)
			return nil
		})
	})

	t.Run("EveryTextTheDomainAcceptsRoundTrips", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// The store is a key/value layer: it does not validate, it stores.
		corpus := []string{
			"Aurora Bank",
			"Crédit Soleil",
			"三菱UFJ銀行",
			"Банк «Восток»",
			"🏦 Emoji Bank",
			`He said "50% off"; 'ok'`,
			`C:\path\to\nowhere`,
			"$1 -- DROP TABLE ledgers",
			"Ban\x00k",   // refused by the domain; must never reach a store
			"Ban\xffk",   // likewise
			"two\nlines", // likewise
		}

		type sample struct{ id, text string }
		var samples []sample
		for i, text := range corpus {
			if err := ledger.ValidateText("name", text); err != nil {
				continue
			}
			samples = append(samples, sample{id: fmt.Sprintf("%03d", i), text: text})
		}
		if len(samples) == 0 {
			t.Fatal("storetest: the whole corpus was rejected by the domain; nothing was tested")
		}

		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			for _, sm := range samples {
				if err := tx.PutLedger(ctx, bookA, ledger.Ledger{
					ID: ledger.LedgerID("ldg_" + sm.id), Name: sm.text, CreatedAt: early,
				}); err != nil {
					return err
				}
				if err := tx.PutTransaction(ctx, bookA, ledger.Transaction{
					ID:          ledger.TransactionID("txt_" + sm.id),
					Status:      ledger.Posted,
					CreatedAt:   early,
					Description: sm.text,
					// Metadata goes into a JSON column rather than a plain text
					// one, which is a different encoder and a different CHECK,
					// so it needs its own sample.
					Metadata: map[string]string{sm.text: sm.text},
					Entries: []ledger.Entry{
						{ID: ledger.EntryID("ent_" + sm.id), AccountID: "100.100.001", Amount: 100, Direction: ledger.Debit},
					},
				}); err != nil {
					return err
				}
				if err := tx.AppendAudit(ctx, ledger.AuditEvent{
					ID: "evt_" + sm.id, BookID: bookA, Scope: ledger.ScopeLedger,
					Type: ledger.EventLedgerCreated, EntityID: "ldg_" + sm.id,
					Metadata: map[string]string{"name": sm.text},
				}); err != nil {
					return err
				}
			}
			return nil
		})

		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			for _, sm := range samples {
				l, err := tx.GetLedger(ctx, bookA, ledger.LedgerID("ldg_"+sm.id))
				if err != nil {
					return err
				}
				assertEqual(t, "ledger name round-trip", l.Name, sm.text)

				txn, err := tx.GetTransaction(ctx, bookA, ledger.TransactionID("txt_"+sm.id))
				if err != nil {
					return err
				}
				assertEqual(t, "description round-trip", txn.Description, sm.text)
				assertEqual(t, "metadata value round-trip", txn.Metadata[sm.text], sm.text)
			}
			events, err := tx.ListAudit(ctx, ledger.AuditFilter{})
			if err != nil {
				return err
			}
			assertEqual(t, "audit events stored", len(events), len(samples))
			for i, e := range events {
				assertEqual(t, "audit metadata round-trip", e.Metadata["name"], samples[i].text)
			}
			return nil
		})
	})

	t.Run("EmptyIdempotencyKeyNotDeduplicated", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// An empty key is an absent key, not an identity: it must never
		// deduplicate, however many transactions carry it.
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			for _, id := range []ledger.TransactionID{"tx_1", "tx_2", "tx_3"} {
				if err := tx.PutTransaction(ctx, bookA, transaction(id, "")); err != nil {
					return err
				}
			}
			return nil
		})

		var all []ledger.Transaction
		var lookupErr error
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			var err error
			if all, err = tx.ListTransactions(ctx, bookA); err != nil {
				return err
			}
			_, lookupErr = tx.GetTransactionByIdempotencyKey(ctx, bookA, "")
			return nil
		})

		assertEqual(t, "transactions stored", len(all), 3)
		assertErrorIs(t, "lookup by empty key", lookupErr, ledger.ErrTransactionNotFound)
	})

	// A balance is a fold over ScanEntries, so what the store owes is the ROWS:
	// this position's and no other's, narrowed by a window the index can serve.
	t.Run("ScanEntriesYieldsThePositionsEntriesInTheWindow", func(t *testing.T) {
		s := open(t, newStore, bookA)

		const pooled ledger.AccountID = "902.001.001"
		day := func(d int) time.Time { return time.Date(2026, 5, d, 0, 0, 0, 0, time.UTC) }

		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			for _, p := range []struct {
				id         string
				subsidiary string
				when       time.Time
			}{
				{"1", "dep_1", day(1)},
				{"2", "dep_2", day(2)},
				{"3", "dep_1", day(3)},
				// Undated, so it is in no window and in every unbounded scan.
				{"4", "dep_1", time.Time{}},
			} {
				err := tx.PutTransaction(ctx, bookA, ledger.Transaction{
					ID: ledger.TransactionID("txn_scan_" + p.id), ValueDate: p.when,
					Entries: []ledger.Entry{
						{ID: ledger.EntryID("ent_scan_" + p.id), AccountID: pooled, Subsidiary: p.subsidiary,
							Amount: 100, Direction: ledger.Credit, ValueDate: p.when},
						{ID: ledger.EntryID("ent_scan_other_" + p.id), AccountID: "902.001.002",
							Amount: 100, Direction: ledger.Debit, ValueDate: p.when},
					},
				})
				if err != nil {
					return err
				}
			}
			return nil
		})

		cases := []struct {
			label  string
			pos    ledger.Position
			filter ledger.EntryFilter
			want   string
		}{
			{"the whole account", pooled.Total(), ledger.EntryFilter{},
				"[ent_scan_1 ent_scan_2 ent_scan_3 ent_scan_4]"},
			{"one subsidiary", pooled.For("dep_1"), ledger.EntryFilter{},
				"[ent_scan_1 ent_scan_3 ent_scan_4]"},
			{"a subsidiary nothing was posted for", pooled.For("dep_9"), ledger.EntryFilter{}, "[]"},
			// [From, To): the lower bound is in, the upper is out, and the
			// undated entry is in neither.
			{"a half-open window", pooled.Total(), ledger.EntryFilter{From: day(1), To: day(3)},
				"[ent_scan_1 ent_scan_2]"},
			{"a lower bound alone", pooled.Total(), ledger.EntryFilter{From: day(2)},
				"[ent_scan_2 ent_scan_3]"},
			{"an upper bound alone", pooled.Total(), ledger.EntryFilter{To: day(2)}, "[ent_scan_1]"},
			{"a subsidiary within a window", pooled.For("dep_1"), ledger.EntryFilter{From: day(2), To: day(4)},
				"[ent_scan_3]"},
		}
		for _, c := range cases {
			assertEqual(t, c.label, scanned(t, s, c.pos, c.filter), c.want)
		}

		// An Entry comes back whole: five columns, and the account the position
		// named rather than a sixth column repeating it.
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			for e, err := range tx.ScanEntries(ctx, bookA, pooled.For("dep_2"), ledger.EntryFilter{}) {
				if err != nil {
					return err
				}
				assertEqual(t, "the entry's id", string(e.ID), "ent_scan_2")
				assertEqual(t, "its account", string(e.AccountID), string(pooled))
				assertEqual(t, "its subsidiary", e.Subsidiary, "dep_2")
				assertEqual(t, "its amount", e.Amount, ledger.Amount(100))
				assertEqual(t, "its direction", e.Direction, ledger.Credit)
				assertEqual(t, "its value date", e.ValueDate.Equal(day(2)), true)
			}
			return nil
		})
	})

	// A scan stops when the caller stops, and it must not report an error for
	// having been stopped: a fold that has seen enough is not a failure.
	t.Run("ScanEntriesStopsWhenTheFoldBreaks", func(t *testing.T) {
		s := open(t, newStore, bookA)

		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutTransaction(ctx, bookA, ledger.Transaction{
				ID: "txn_scan_break", Status: ledger.Posted,
				Entries: []ledger.Entry{
					{ID: "ent_break_1", AccountID: "903.001.001", Amount: 100, Direction: ledger.Debit},
					{ID: "ent_break_2", AccountID: "903.001.001", Amount: 200, Direction: ledger.Debit},
					{ID: "ent_break_3", AccountID: "903.001.002", Amount: 300, Direction: ledger.Credit},
				},
			})
		})

		seen := 0
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			for _, err := range tx.ScanEntries(ctx, bookA, ledger.AccountID("903.001.001").Total(), ledger.EntryFilter{}) {
				if err != nil {
					return err
				}
				seen++
				break
			}
			// And the unit of work is still usable afterwards: an abandoned scan
			// leaves nothing behind that the next read trips over.
			_, err := ledger.BookBalance(ctx, tx, bookA, ledger.AccountID("903.001.001").Total(), ledger.Debit)
			return err
		})
		assertEqual(t, "entries seen before the break", seen, 1)
	})

	// ValueDateBalanceExcludesZeroValueDateEntries pins a rule two stores once
	// disagreed about and one store can still get wrong.
	// ValueDateBalanceNetsAReversalOnTheOriginalsDay exercises the contract
	// line in ledger.ValueDateBalance directly: a reversal posts its own mirrored
	// entries, value-dated onto the original leg's day (ReverseTransactionTx),
	// and those are what cancel the original — so a bound falling after the
	// original's value date must see the net, zero, not the gross.
	// ValueDatedSeriesExcludesZeroValueDateEntries is the movement-bucketing half
	// of ValueDateBalanceExcludesZeroValueDateEntries above, and it needs its own
	// subtest: Series.Opening inherits that ruling by delegating to the same
	// balance query, but the per-day buckets are built by separate code and could
	// get it right in one place and wrong in the other.
	t.Run("MarkReversedIsConditional", func(t *testing.T) {
		s := open(t, newStore, bookA)

		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_1", ""))
		})

		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.MarkReversed(ctx, bookA, "tx_1")
		})

		// Posted -> Reversed happens at most once, so two racing reversals
		// cannot both win.
		second := s.Update(context.Background(), func(ctx context.Context, tx ledger.BankTx) error {
			return tx.MarkReversed(ctx, bookA, "tx_1")
		})
		assertErrorIs(t, "second MarkReversed", second, ledger.ErrTransactionAlreadyReversed)

		missing := s.Update(context.Background(), func(ctx context.Context, tx ledger.BankTx) error {
			return tx.MarkReversed(ctx, bookA, "tx_nope")
		})
		assertErrorIs(t, "MarkReversed on an unknown transaction", missing, ledger.ErrTransactionNotFound)

		var got ledger.Transaction
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			var err error
			got, err = tx.GetTransaction(ctx, bookA, "tx_1")
			return err
		})
		assertEqual(t, "status", got.Status.String(), ledger.Reversed.String())
	})

	t.Run("AuditOrderedBySeq", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// One instant for all four, so OccurredAt ties on every event: Seq,
		// assigned by the store, is the only thing that can order the log.
		at := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			for _, id := range []string{"evt_1", "evt_2", "evt_3", "evt_4"} {
				// Seq 999 is deliberately wrong; the store must overwrite it.
				if err := tx.AppendAudit(ctx, ledger.AuditEvent{
					Seq: 999, ID: id, BookID: bookA, Scope: ledger.ScopeLedger,
					Type: ledger.EventLedgerCreated, EntityID: id, OccurredAt: at,
				}); err != nil {
					return err
				}
			}
			return nil
		})

		events := audit(t, s, ledger.AuditFilter{})
		assertEqual(t, "event count", len(events), 4)
		for i, e := range events {
			if i > 0 && e.Seq <= events[i-1].Seq {
				t.Fatalf("audit not ordered by Seq: event %d has Seq %d, previous %d", i, e.Seq, events[i-1].Seq)
			}
			assertEqual(t, "event order", e.ID, []string{"evt_1", "evt_2", "evt_3", "evt_4"}[i])
		}
	})

	t.Run("AuditFilterByScopeTypeEntityAndBefore", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// Every event is this store's own book, because a store answers for one and
		// refuses the rest — which is what the refusal below asserts.
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			for _, e := range []ledger.AuditEvent{
				{ID: "evt_1", BookID: bookA, Scope: ledger.ScopeLedger, Type: ledger.EventLedgerCreated, EntityID: "ldg_1"},
				{ID: "evt_2", BookID: bookA, Scope: ledger.ScopeLedger, Type: ledger.EventAccountCreated, EntityID: "200.100.001"},
				{ID: "evt_3", BookID: bookA, Scope: ledger.ScopeDeposit, Type: ledger.EventAccountOpened, EntityID: "dep_1"},
				{ID: "evt_4", BookID: bookA, Scope: ledger.ScopeLedger, Type: ledger.EventAccountCreated, EntityID: "200.100.002"},
			} {
				if err := tx.AppendAudit(ctx, e); err != nil {
					return err
				}
			}
			return nil
		})

		// No filter: everything, every scope. This institution's own log is the
		// whole of what a store holds.
		assertEqual(t, "unfiltered", len(audit(t, s, ledger.AuditFilter{})), 4)

		// BookID naming THIS store's book still narrows, which on a one-book
		// store means it changes nothing and must not break.
		assertEqual(t, "by this store's own book", len(audit(t, s, ledger.AuditFilter{BookID: bookA})), 4)

		// BookID naming ANOTHER institution's book is REFUSED, and that is the point:
		// an empty page is what a caller reading the wrong store would otherwise get,
		// and it is indistinguishable from end-of-data.
		if _, err := listAudit(s, ledger.AuditFilter{BookID: bookB}); err == nil {
			t.Error("a filter naming another institution's book was answered rather than refused; an empty page reads as end-of-data")
		}

		// Scope narrows to one layer.
		byScope := audit(t, s, ledger.AuditFilter{Scope: ledger.ScopeDeposit})
		assertEqual(t, "by scope", len(byScope), 1)
		assertEqual(t, "by scope id", byScope[0].ID, "evt_3")

		// Type narrows to one event type.
		byType := audit(t, s, ledger.AuditFilter{Type: ledger.EventAccountCreated})
		assertEqual(t, "by type", len(byType), 2)

		// EntityID picks one entity out of a type that has two.
		byEntity := audit(t, s, ledger.AuditFilter{EntityID: "200.100.001"})
		assertEqual(t, "by entity", len(byEntity), 1)
		assertEqual(t, "by entity id", byEntity[0].ID, "evt_2")

		// Filters compose: scope and type together match two, and the entity
		// narrows them to one.
		combined := audit(t, s, ledger.AuditFilter{BookID: bookA, Scope: ledger.ScopeLedger, Type: ledger.EventAccountCreated})
		assertEqual(t, "combined", len(combined), 2)
		narrowed := audit(t, s, ledger.AuditFilter{
			BookID: bookA, Scope: ledger.ScopeLedger, Type: ledger.EventAccountCreated, EntityID: "200.100.001",
		})
		assertEqual(t, "combined and narrowed", len(narrowed), 1)
		assertEqual(t, "combined and narrowed id", narrowed[0].ID, "evt_2")

		// Before is an exclusive upper bound on Seq.
		all := audit(t, s, ledger.AuditFilter{})
		before := audit(t, s, ledger.AuditFilter{Before: all[2].Seq})
		assertEqual(t, "before the third event", len(before), 2)
		assertEqual(t, "first before", before[0].ID, "evt_1")
		assertEqual(t, "second before", before[1].ID, "evt_2")
	})

	t.Run("AuditLimitKeepsNewestBelowBefore", func(t *testing.T) {
		s := open(t, newStore, bookA)

		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			for _, id := range []string{"evt_1", "evt_2", "evt_3", "evt_4", "evt_5"} {
				if err := tx.AppendAudit(ctx, ledger.AuditEvent{
					ID: id, BookID: bookA, Scope: ledger.ScopeLedger, Type: ledger.EventLedgerCreated, EntityID: id,
				}); err != nil {
					return err
				}
			}
			return nil
		})

		all := audit(t, s, ledger.AuditFilter{})
		assertEqual(t, "event count", len(all), 5)

		// A page is the newest Limit events below the cursor, still handed back
		// oldest-first, so a caller can walk backwards with Before while each
		// page reads chronologically.
		page1 := audit(t, s, ledger.AuditFilter{Limit: 2})
		assertEqual(t, "page 1 size", len(page1), 2)
		assertEqual(t, "page 1 first", page1[0].ID, "evt_4")
		assertEqual(t, "page 1 second", page1[1].ID, "evt_5")

		page2 := audit(t, s, ledger.AuditFilter{Before: page1[0].Seq, Limit: 2})
		assertEqual(t, "page 2 size", len(page2), 2)
		assertEqual(t, "page 2 first", page2[0].ID, "evt_2")
		assertEqual(t, "page 2 second", page2[1].ID, "evt_3")

		page3 := audit(t, s, ledger.AuditFilter{Before: page2[0].Seq, Limit: 2})
		assertEqual(t, "page 3 size", len(page3), 1)
		assertEqual(t, "page 3 first", page3[0].ID, "evt_1")

		// A limit larger than the result set is not an error.
		assertEqual(t, "oversized limit", len(audit(t, s, ledger.AuditFilter{Limit: 99})), 5)
	})

	t.Run("AuditPagingIsScopedToItsFilter", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// Seq is a store-GLOBAL total order, not per book or per scope, so a scope's
		// events are separated by gaps that belong to other scopes.
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			for round := range 3 {
				for _, e := range []ledger.AuditEvent{
					{BookID: bookA, Scope: ledger.ScopeLedger, Type: ledger.EventAccountCreated},
					{BookID: bookA, Scope: ledger.ScopeDeposit, Type: ledger.EventAccountOpened},
					{BookID: bookA, Scope: ledger.ScopeLedger, Type: ledger.EventLedgerCreated},
					{BookID: bookA, Scope: ledger.ScopePayment, Type: ledger.EventPaymentAccepted},
				} {
					e.ID = fmt.Sprintf("evt_%s_%d", e.Type, round)
					e.EntityID = fmt.Sprintf("ent_%d", round)
					if err := tx.AppendAudit(ctx, e); err != nil {
						return err
					}
				}
			}
			return nil
		})

		filter := ledger.AuditFilter{BookID: bookA, Scope: ledger.ScopePayment}
		all := audit(t, s, filter)
		assertEqual(t, "payment events", len(all), 3)

		// Limit is applied LAST, after every other predicate: two payment
		// events, not "the newest two rows, of which some are payment events".
		paged := filter
		paged.Limit = 2
		page1 := audit(t, s, paged)
		assertEqual(t, "page 1 size", len(page1), 2)
		assertEqual(t, "page 1 first", page1[0].Seq, all[1].Seq)
		assertEqual(t, "page 1 second", page1[1].Seq, all[2].Seq)

		// The cursor is the page's OLDEST Seq, and there are other scopes'
		// events immediately below it.
		paged.Before = page1[0].Seq
		page2 := audit(t, s, paged)
		assertEqual(t, "page 2 size", len(page2), 1)
		assertEqual(t, "page 2 first", page2[0].Seq, all[0].Seq)
		assertEqual(t, "page 2 scope", string(page2[0].Scope), string(ledger.ScopePayment))

		// Below the oldest match is an empty page, even though lower sequence
		// numbers exist in other scopes.
		paged.Before = all[0].Seq
		assertEqual(t, "page below the oldest match", len(audit(t, s, paged)), 0)

		// Before composes with Scope and EntityID together.
		byEntity := ledger.AuditFilter{Scope: ledger.ScopeLedger, EntityID: "ent_1"}
		ent1 := audit(t, s, byEntity)
		assertEqual(t, "ledger events for ent_1", len(ent1), 2)
		byEntity.Before = ent1[1].Seq
		assertEqual(t, "ledger events for ent_1 below the newer one", len(audit(t, s, byEntity)), 1)
		byEntity.Before = ent1[0].Seq
		assertEqual(t, "ledger events for ent_1 below the older one", len(audit(t, s, byEntity)), 0)

		// A Type filter narrows within a scope and still pages.
		byType := ledger.AuditFilter{Type: ledger.EventPaymentAccepted, Limit: 1}
		assertEqual(t, "newest payment.accepted", len(audit(t, s, byType)), 1)
		assertEqual(t, "newest payment.accepted seq", audit(t, s, byType)[0].Seq, all[2].Seq)
	})

	t.Run("ResetClearsEverything", func(t *testing.T) {
		s := open(t, newStore, bookA)

		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			if _, err := tx.NextID(ctx, bookA, "ldg"); err != nil {
				return err
			}
			if _, err := tx.NextSubledgerBlock(ctx, bookA); err != nil {
				return err
			}
			if _, err := tx.NextAccountSeq(ctx, bookA, 200, "100"); err != nil {
				return err
			}
			if err := tx.PutLedger(ctx, bookA, ledger.Ledger{ID: "ldg_1", Name: "GL"}); err != nil {
				return err
			}
			if err := tx.PutSubledger(ctx, bookA, ledger.Subledger{ID: "100", LedgerID: "ldg_1"}); err != nil {
				return err
			}
			if err := tx.PutAccount(ctx, bookA, ledger.Account{ID: "200.100.001", SubledgerID: "100", Type: ledger.Liability}); err != nil {
				return err
			}
			if err := tx.PutTransaction(ctx, bookA, transaction("tx_1", "key-1")); err != nil {
				return err
			}
			return tx.AppendAudit(ctx, ledger.AuditEvent{ID: "evt_1", BookID: bookA, Scope: ledger.ScopeLedger, Type: ledger.EventLedgerCreated})
		})

		if err := s.Reset(context.Background()); err != nil {
			t.Fatalf("Reset: %v", err)
		}

		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			ledgers, err := tx.ListLedgers(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "ledgers after reset", len(ledgers), 0)

			subledgers, err := tx.ListSubledgers(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "subledgers after reset", len(subledgers), 0)

			accounts, err := tx.ListAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "accounts after reset", len(accounts), 0)

			txs, err := tx.ListTransactions(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "transactions after reset", len(txs), 0)

			events, err := tx.ListAudit(ctx, ledger.AuditFilter{})
			if err != nil {
				return err
			}
			assertEqual(t, "audit events after reset", len(events), 0)
			return nil
		})

		// The idempotency key is free again, and every counter restarts.
		var id string
		var block, seq int
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			var err error
			if id, err = tx.NextID(ctx, bookA, "ldg"); err != nil {
				return err
			}
			if block, err = tx.NextSubledgerBlock(ctx, bookA); err != nil {
				return err
			}
			if seq, err = tx.NextAccountSeq(ctx, bookA, 200, "100"); err != nil {
				return err
			}
			return tx.PutTransaction(ctx, bookA, transaction("tx_1", "key-1"))
		})
		assertEqual(t, "first id after reset", id, "ldg_1")
		assertEqual(t, "first subledger block after reset", block, 100)
		assertEqual(t, "first account seq after reset", seq, 1)
	})

	t.Run("UpdateRollsBackOnError", func(t *testing.T) {
		s := open(t, newStore, bookA)

		boom := errors.New("storetest: deliberate failure")
		err := s.Update(context.Background(), func(ctx context.Context, tx ledger.BankTx) error {
			if _, err := tx.NextID(ctx, bookA, "ldg"); err != nil {
				return err
			}
			if _, err := tx.NextSubledgerBlock(ctx, bookA); err != nil {
				return err
			}
			if _, err := tx.NextAccountSeq(ctx, bookA, 200, "100"); err != nil {
				return err
			}
			if err := tx.PutLedger(ctx, bookA, ledger.Ledger{ID: "ldg_1", Name: "GL"}); err != nil {
				return err
			}
			if err := tx.PutAccount(ctx, bookA, ledger.Account{ID: "200.100.001", Type: ledger.Liability}); err != nil {
				return err
			}
			if err := tx.PutTransaction(ctx, bookA, transaction("tx_1", "key-1")); err != nil {
				return err
			}
			if err := tx.AppendAudit(ctx, ledger.AuditEvent{ID: "evt_1", BookID: bookA, Scope: ledger.ScopeLedger, Type: ledger.EventLedgerCreated}); err != nil {
				return err
			}
			return boom
		})
		assertErrorIs(t, "Update return", err, boom)

		// Nothing the failed unit of work wrote may survive it — including the
		// audit event, which must never outlive the operation it describes.
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			ledgers, err := tx.ListLedgers(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "ledgers after rollback", len(ledgers), 0)

			accounts, err := tx.ListAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "accounts after rollback", len(accounts), 0)

			txs, err := tx.ListTransactions(ctx, bookA)
			if err != nil {
				return err
			}
			assertEqual(t, "transactions after rollback", len(txs), 0)

			events, err := tx.ListAudit(ctx, ledger.AuditFilter{})
			if err != nil {
				return err
			}
			assertEqual(t, "audit events after rollback", len(events), 0)
			return nil
		})

		// Identity allocation rolls back with the transaction, so the counters
		// are gap-free rather than merely unique.
		var id string
		var block, seq int
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			var err error
			if id, err = tx.NextID(ctx, bookA, "ldg"); err != nil {
				return err
			}
			if block, err = tx.NextSubledgerBlock(ctx, bookA); err != nil {
				return err
			}
			seq, err = tx.NextAccountSeq(ctx, bookA, 200, "100")
			return err
		})
		assertEqual(t, "id after rollback", id, "ldg_1")
		assertEqual(t, "subledger block after rollback", block, 100)
		assertEqual(t, "account seq after rollback", seq, 1)

		// The idempotency key the rolled-back transaction claimed is free.
		update(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_1", "key-1"))
		})
	})

	t.Run("ConcurrentReadThenWriteOnOneKeyAgrees", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// This suite's only case with two units of work running AT ONCE, and the gap
		// it closes is the reason it exists.
		const claimants = 8
		const wanted = "the-one-and-only"

		errs := make([]error, claimants)
		var start, done sync.WaitGroup
		start.Add(1)
		for i := range claimants {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait()
				errs[i] = s.Update(context.Background(), func(ctx context.Context, tx ledger.BankTx) error {
					id, err := tx.NextID(ctx, bookA, "race")
					if err != nil {
						return err
					}
					accounts, err := tx.ListAccounts(ctx, bookA)
					if err != nil {
						return err
					}
					for _, a := range accounts {
						if a.Name == wanted {
							return errTaken
						}
					}
					// TWO writes, as the operation this stands for has: a payment row and the
					// ledger postings behind it.
					if err := tx.PutTransaction(ctx, bookA, transaction(ledger.TransactionID("txn_"+id), "")); err != nil {
						return err
					}
					return tx.PutAccount(ctx, bookA, ledger.Account{
						ID: ledger.AccountID(id), Name: wanted, Type: ledger.Liability,
					})
				})
			}()
		}
		start.Done()
		done.Wait()

		won := 0
		for i, err := range errs {
			switch {
			case err == nil:
				won++
			case errors.Is(err, errTaken):
			default:
				t.Fatalf("claimant %d failed for the wrong reason: %v", i, err)
			}
		}
		assertEqual(t, "claimants that got the name", won, 1)

		// And the store holds one row, not one row per winner. The count above
		// would still be 1 if every loser had written and then been rolled back
		// by something else.
		var held []ledger.Account
		view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
			var err error
			held, err = tx.ListAccounts(ctx, bookA)
			return err
		})
		assertEqual(t, "accounts named "+wanted, len(held), 1)
	})
}

// errTaken is the losers' answer in ConcurrentReadThenWriteOnOneKeyAgrees: the
// name was already claimed.
var errTaken = errors.New("storetest: the name is already claimed")

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// open builds a fresh store for one subtest and closes it when the subtest ends.
func open(t *testing.T, newStore func(*testing.T, ledger.BookID) ledger.BankStore, book ledger.BookID) ledger.BankStore {
	t.Helper()
	s := newStore(t, book)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// update runs a unit of work that is expected to succeed.
func update(t *testing.T, s ledger.BankStore, fn func(context.Context, ledger.BankTx) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// view runs a read-only unit of work that is expected to succeed.
func view(t *testing.T, s ledger.BankStore, fn func(context.Context, ledger.BankTx) error) {
	t.Helper()
	if err := s.View(context.Background(), fn); err != nil {
		t.Fatalf("View: %v", err)
	}
}

// scanned is the ids a scan yields, sorted and rendered: ScanEntries promises
// which entries come back and not in which order, so the assertion is about
// membership.
func scanned(t *testing.T, s ledger.BankStore, pos ledger.Position, f ledger.EntryFilter) string {
	t.Helper()
	ids := []string{}
	view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
		for e, err := range tx.ScanEntries(ctx, bookA, pos, f) {
			if err != nil {
				return err
			}
			ids = append(ids, string(e.ID))
		}
		return nil
	})
	slices.Sort(ids)
	return sliceString(ids)
}

// audit reads the audit log through a filter.
func audit(t *testing.T, s ledger.BankStore, f ledger.AuditFilter) []ledger.AuditEvent {
	t.Helper()
	var out []ledger.AuditEvent
	view(t, s, func(ctx context.Context, tx ledger.BankTx) error {
		var err error
		out, err = tx.ListAudit(ctx, f)
		return err
	})
	return out
}

// listAudit is audit without the assertion, for the one case that is about the
// ERROR rather than about the page: a filter naming another institution's book
// must be refused, and audit above would fail the test on the way past.
func listAudit(s ledger.BankStore, f ledger.AuditFilter) ([]ledger.AuditEvent, error) {
	var out []ledger.AuditEvent
	err := s.View(context.Background(), func(ctx context.Context, tx ledger.BankTx) error {
		var err error
		out, err = tx.ListAudit(ctx, f)
		return err
	})
	return out, err
}

// transaction builds a minimal balanced transaction for tests that only care
// about its identity.
func transaction(id ledger.TransactionID, key string) ledger.Transaction {
	return ledger.Transaction{
		ID:             id,
		IdempotencyKey: key,
		Status:         ledger.Posted,
		CreatedAt:      time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
		Entries: []ledger.Entry{
			{ID: ledger.EntryID(string(id) + "_a"), AccountID: "100.100.001", Amount: 100, Direction: ledger.Debit},
			{ID: ledger.EntryID(string(id) + "_b"), AccountID: "200.100.001", Amount: 100, Direction: ledger.Credit},
		},
	}
}

func sliceString[T any](s []T) string {
	return fmt.Sprint(s)
}

// ids projects a listing down to the IDs its order is asserted on.
func ids[T any](items []T, key func(T) string) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = key(item)
	}
	return out
}

// assertOrder checks a listing's contents and their order in one shot, so a
// wrong order reports the whole sequence rather than the first mismatched slot.
func assertOrder(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s order: got %v, want %v", label, got, want)
	}
}

func assertEqual[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
}

func assertErrorIs(t *testing.T, label string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: got error %v, want %v", label, err, target)
	}
}
