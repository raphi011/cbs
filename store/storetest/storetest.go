// Package storetest is the shared suite a store must pass: RunLedger for the
// ledger layer, RunDeposit for the deposit layer, RunPayment for the payment
// layer, and RunProduct and RunLending for theirs.
//
// It is NOT a conformance suite: there is one implementation, so nothing here
// cross-checks anything.
//
// What each case pins is the CONTRACT. This file, deposit.go, payment.go,
// product.go and lending.go talk only to the Store and Tx interfaces — never to
// ledger.Book, deposit.Register or payment.Network — and they name no table and
// no dialect: identity allocation, ordering, idempotency, the balance aggregate,
// the audit log, rollback. Three store shapes — bank, csm, centralbank — each
// run this file.
//
// Where a case records what a store CANNOT show, it is recording why the case
// exists: the ephemeral store serialises writers, so it is blind to anything
// about read-then-write ordering — see races.go.
//
// races.go does not observe the interface boundary, on purpose. Its cases drive
// payment.Network, because the defect they exist for is an ordering the ACTS
// choose and the store cannot express — and the synthetic stand-in written to
// respect the boundary, ConcurrentReadThenWriteOnOneKeyAgrees below, was blind
// to three money defects of exactly that shape. Admit is here for the same
// reason: races.go needs it and this package may not import an implementation,
// because the implementation's tests import this package.
//
// # Ordering
//
// Every listing is ORDER BY created_at, seq, where seq is a monotonic per-book,
// per-table sequence assigned when a row is first inserted — a column allocated
// MAX(seq)+1 in store/sqlite. An upsert must not reissue it, or editing a row
// moves it to the end of the list.
//
// The tie-break is never the ID. IDs are counter-derived strings, so "tx_10"
// sorts before "tx_8" and a listing silently reorders itself the moment a
// counter crosses a power of ten. It is the same mechanism ledger.AuditEvent.Seq
// already uses, so the audit log and the entity listings agree on what order
// means.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Books used throughout the suite. Two of them, because almost every
// store-level guarantee is per book and the interesting failure mode is a
// implementation that leaks state across books.
//
// bookA is the book the store handed to a suite ANSWERS FOR, and every case
// that names one names it. A store answers for exactly one book and refuses the
// rest, so a case wanting a second book asks the factory for a second STORE —
// which is what a second book is: another institution's database.
//
// bookB is that second store's book. It is used by the handful of cases about
// isolation, and what each of them now demonstrates is stronger than what it did
// with two books in one database: the rows cannot collide because the databases
// cannot see each other, rather than because a primary key is composite.
//
// There was a bookC, for AuditPagingIsScopedToItsFilter, which needed a third
// book whose events were payment-scoped so that the pager had gaps to step over.
// The gaps come from the scopes and always did, so that case needs one book and
// the constant is gone.
const (
	bookA ledger.BookID = "book-a"
	bookB ledger.BookID = "book-b"
)

// RunLedger runs the ledger-layer suite against a store.
//
// newStore must return a store with no state in it; the suite calls it once per
// subtest and closes the result.
func RunLedger(t *testing.T, newStore func(*testing.T, ledger.BookID) ledger.Store) {
	t.Helper()

	t.Run("NextSubledgerBlockStepsBy100PerBook", func(t *testing.T) {
		// TWO STORES, because a store answers for one book and refuses the
		// rest. The claim is the same one it always was — a second book's chart
		// of accounts starts at 100 rather than continuing the first's — and
		// what changed is that the two books are now two databases, which is the
		// only place a second book can be. See the note on bookA above.
		s := open(t, newStore, bookA)
		other := open(t, newStore, bookB)

		var gotA []int
		var gotB []int
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			for range 3 {
				block, err := tx.NextSubledgerBlock(ctx, bookA)
				if err != nil {
					return err
				}
				gotA = append(gotA, block)
			}
			return nil
		})
		update(t, other, func(ctx context.Context, tx ledger.Tx) error {
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

		// ONE counter per book, shared by every prefix — not one counter per
		// prefix. The number doubles as a creation order, which is why ldg_1,
		// tx_2 and ent_3 interleave rather than each restarting at 1. A store
		// that keyed its counter by (book, prefix) would still hand out unique
		// IDs, so nothing else in this suite would notice.
		other := open(t, newStore, bookB)

		var inA, inB []string
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		update(t, other, func(ctx context.Context, tx ledger.Tx) error {
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

		// Transaction.Entries is an ordered slice, and the order is meaningful:
		// it is the order the legs were written in, and it is what a settlement
		// transaction's determinism rests on. A store that reconstructs entries
		// from rows must therefore keep an explicit position — sorting by entry
		// ID would look right until a counter crossed a power of ten, which is
		// exactly what this fixture does.
		want := []ledger.Entry{
			{ID: "ent_10", AccountID: "100.100.001", Amount: 400, Direction: ledger.Debit},
			{ID: "ent_8", AccountID: "200.100.001", Amount: 100, Direction: ledger.Credit},
			{ID: "ent_20", AccountID: "200.100.002", Amount: 250, Direction: ledger.Credit},
			{ID: "ent_9", AccountID: "200.100.003", Amount: 50, Direction: ledger.Credit},
		}
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

		// Whether an account pools subsidiaries decides which entries the domain
		// will accept against it, both ways round. A store that dropped the flag
		// on the way out would report every account as plain, and the refusal
		// that keeps money from landing in a pool with nobody's name on it would
		// stop firing everywhere at once.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

		// The mapping is keyed by (product, slot, asset) and the bank-wide row
		// is the one with an empty product. A store that keyed on the slot alone
		// would collapse a product's own revenue line onto the bank's; one that
		// appended instead of upserting would leave two answers to a question
		// that has one, and which of them a flow posted to would be whichever
		// the store listed first.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		update(t, other, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutSlotAccount(ctx, bookB, ledger.SlotAccount{
				Slot: "deposit.principal", Asset: "EUR", Account: "200.500.001",
			})
		})

		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
			// The fallback is the domain's rule, stated once in
			// ledger.Book.SlotAccountTx; a store that also implemented it would
			// make "this product has its own line" unaskable.
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
		view(t, other, func(ctx context.Context, tx ledger.Tx) error {
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
		// them apart. What this pins is the whole claim the dimension rests on:
		// a position with no subsidiary is the WHOLE account and not the subsidiary
		// named by the empty string, so the control figure is the same aggregate
		// with one predicate dropped rather than a total stored beside the
		// detail. A store that filtered on subsidiary_id = '' instead would
		// report a control balance of zero and every subtest above would still
		// pass.
		const (
			pooled ledger.AccountID = "200.100.001"
			contra ledger.AccountID = "100.100.001"
		)
		day := func(n int) time.Time {
			return time.Date(2025, 1, n, 12, 0, 0, 0, time.UTC)
		}
		post := func(id ledger.TransactionID, subsidiary string, amount ledger.Amount, on time.Time) {
			t.Helper()
			update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			// The dimension is on the entry it was written on, and only on it.
			one, err := tx.GetTransaction(ctx, bookA, "tx_1")
			if err != nil {
				return err
			}
			assertEqual(t, "the pooled leg's subsidiary", one.Entries[1].Subsidiary, "dep_1")
			assertEqual(t, "the plain leg's subsidiary", one.Entries[0].Subsidiary, "")

			whole, err := tx.BookBalance(ctx, bookA, pooled.Total(), ledger.Credit)
			if err != nil {
				return err
			}
			assertEqual(t, "the control balance", whole, ledger.Amount(1290))

			var detail ledger.Amount
			for subsidiary, want := range map[string]ledger.Amount{"dep_1": 1040, "dep_2": 250} {
				got, err := tx.BookBalance(ctx, bookA, pooled.For(subsidiary), ledger.Credit)
				if err != nil {
					return err
				}
				assertEqual(t, "the balance of "+subsidiary, got, want)
				detail += got
			}
			assertEqual(t, "the detail against the control", detail, whole)

			// A subsidiary nothing was ever posted for is zero, like an account
			// with no entries. Nothing here holds a list to check it against.
			absent, err := tx.BookBalance(ctx, bookA, pooled.For("dep_9"), ledger.Credit)
			if err != nil {
				return err
			}
			assertEqual(t, "a subsidiary with no postings", absent, ledger.Amount(0))

			// And the same detail asked for the other way round: the caller that
			// does not know the subsidiaries yet. It is the drill-down a control
			// account's page renders, and it must agree with the per-subsidiary
			// reads above rather than being a second count of the same entries.
			breakdown, err := tx.SubsidiaryBalances(ctx, bookA, pooled, ledger.Credit)
			if err != nil {
				return err
			}
			var listed []string
			var summed ledger.Amount
			for _, row := range breakdown {
				listed = append(listed, row.Subsidiary)
				summed += row.Balance
			}
			assertEqual(t, "the subsidiaries, in order", sliceString(listed), "[dep_1 dep_2]")
			assertEqual(t, "the breakdown against the control", summed, whole)
			assertEqual(t, "dep_1's row", breakdown[0].Balance, ledger.Amount(1040))

			// The value-dated reads carry the subsidiary too: interest is computed
			// from these, and a series that read the pool would accrue the whole
			// bank's balance for every customer under it.
			asAt, err := tx.ValueDateBalance(ctx, bookA, pooled.For("dep_1"), ledger.Credit, day(5))
			if err != nil {
				return err
			}
			assertEqual(t, "dep_1 as at the 5th", asAt, ledger.Amount(1000))

			series, err := tx.ValueDatedSeries(ctx, bookA, pooled.For("dep_1"), ledger.Credit, day(5), day(7))
			if err != nil {
				return err
			}
			assertEqual(t, "dep_1's opening on the 5th", series.Opening, ledger.Amount(1000))
			assertEqual(t, "days dep_1 moved on", len(series.Movements), 1)
			assertEqual(t, "dep_1's movement", series.Movements[0].Amount, ledger.Amount(40))

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
		if err := s.Update(ctx, func(ctx context.Context, tx ledger.Tx) error {
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
		if err := s.Update(ctx, func(ctx context.Context, tx ledger.Tx) error {
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
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		//
		// The claim: chart-of-accounts IDs are unique within a book, not globally,
		// so two banks may both hold an account numbered 200.100.001. Here the two
		// banks are two databases that cannot see each other, which is why the ids
		// cannot collide in the first place.
		s := open(t, newStore, bookA)
		other := open(t, newStore, bookB)

		const shared ledger.AccountID = "200.100.001"
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutAccount(ctx, bookA, ledger.Account{ID: shared, Name: "Alice at A", Type: ledger.Liability})
		})
		update(t, other, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutAccount(ctx, bookB, ledger.Account{ID: shared, Name: "Bob at B", Type: ledger.Liability})
		})

		var inA, inB ledger.Account
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			inA, err = tx.GetAccount(ctx, bookA, shared)
			return err
		})
		view(t, other, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			inB, err = tx.GetAccount(ctx, bookB, shared)
			return err
		})

		assertEqual(t, "account in book-a", inA.Name, "Alice at A")
		assertEqual(t, "account in book-b", inB.Name, "Bob at B")

		// Listing one book must not show the other book's rows.
		var listed []ledger.Account
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutAccount(ctx, bookA, ledger.Account{
				ID: "100.custody.001", SubledgerID: "custody", Name: "Custody",
				Type: ledger.Asset, Asset: "BTC",
			})
		})

		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

		// A store that reports "not found" as anything other than the domain
		// sentinel — a wrapped sql.ErrNoRows, say — turns every 404 in the API
		// into a 500, and turns a typo'd account ID in PostTransaction into an
		// internal error instead of ledger.ErrAccountNotFound.
		early := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		view(t, other, func(ctx context.Context, tx ledger.Tx) error {
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
		view(t, empty, func(ctx context.Context, tx ledger.Tx) error {
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

		// A store is a per-table key/value layer. "The parent must exist" is a
		// DOMAIN rule — ledger.Book reads the ledger before creating a
		// subledger, the subledger before an account, and every account before
		// posting to it — and putting it in the schema as well enforces it twice,
		// in two places that answer differently: the constraint fires first, and
		// it fires as a foreign-key violation where the domain would have said
		// ErrLedgerNotFound. See subledgers in store/sqlite/schema/bank/0001_init.sql.
		//
		// It is written from the failure it prevents: a composite FK on
		// subledgers(book_id, ledger_id) turns the first write below into a
		// constraint violation, and no other fixture in this suite writes a dangling
		// LedgerID.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

			balance, err := tx.BookBalance(ctx, bookA, ledger.AccountID("999.999.001").Total(), ledger.Debit)
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

		// Every listing is ordered by CreatedAt, ties broken by the row's
		// insertion sequence — all five of them, not just the first.
		//
		// The tie-break must NOT be the ID. IDs are counter-derived strings, so
		// "ldg_10" sorts before "ldg_8" and a listing silently reorders itself
		// the moment a counter crosses a power of ten. Each fixture below is
		// therefore built to three rules:
		//
		//   - a CreatedAt tie that only the tie-break can resolve,
		//   - IDs spanning the 9 -> 10 boundary, so lexicographic ID order and
		//     insertion order genuinely disagree, and
		//   - the row inserted FIRST carrying the LATEST CreatedAt, so an
		//     implementation that orders by sequence alone fails too.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		//
		// All three of the ledger's own tables, not just accounts. The rule is
		// one line of SQL per table in a store that writes SQL — the insertion
		// sequence is left out of the update branch — and one line is what a
		// port gets wrong in one table and right in the others. Measured on
		// store/sqlite: re-putting a LEDGER with its sequence rewritten moved it
		// to the end of its listing and every case in this file still passed,
		// because only accounts were re-put here.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_1", "key-1"))
		})

		// A second, different transaction claiming the same key is refused by
		// the store itself, not just by a caller's pre-check.
		err := s.Update(context.Background(), func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_2", "key-1"))
		})
		assertErrorIs(t, "second put with the same key", err, ledger.ErrDuplicateIdempotencyKey)

		var found ledger.Transaction
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			found, err = tx.GetTransactionByIdempotencyKey(ctx, bookA, "key-1")
			return err
		})
		assertEqual(t, "transaction behind key-1", string(found.ID), "tx_1")

		// Keys are per book, so the same key is free in another book — which is
		// another bank's database, and the index that enforces this is that
		// database's own.
		other := open(t, newStore, bookB)
		update(t, other, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookB, transaction("tx_9", "key-1"))
		})
	})

	t.Run("RePuttingATransactionReleasesItsOldIdempotencyKey", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// PutTransaction is an upsert, and an upsert may change the key. A store
		// that only ever adds to its idempotency index keeps resolving a key the
		// transaction no longer carries — and then refuses the next transaction that
		// legitimately claims it.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_1", "key-1"))
		})
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_1", "key-2"))
		})

		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_2", "key-1"))
		})
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_2", ""))
		})
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			_, err := tx.GetTransactionByIdempotencyKey(ctx, bookA, "key-1")
			assertErrorIs(t, "lookup by a key that was cleared", err, ledger.ErrTransactionNotFound)
			return nil
		})
	})

	t.Run("HandledDuplicateKeyLeavesTheUnitOfWorkUsable", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// ErrDuplicateIdempotencyKey is a domain sentinel, which means a caller is
		// entitled to handle it and carry on — retry under a fresh key, fall back to
		// the existing transaction, record the collision. That is only true if the
		// failed statement did not take the whole unit of work with it, and whether
		// it does is the database's answer rather than the domain's. SQLite rolls
		// back the failed STATEMENT and leaves the transaction usable; a database
		// that aborted the whole transaction would need a savepoint here. This
		// subtest is what says the promise is kept either way.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_1", "key-1"))
		})

		err := s.Update(context.Background(), func(ctx context.Context, tx ledger.Tx) error {
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
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

		// The store is a key/value layer: it does not validate, it stores. What
		// it MUST do is hold, byte for byte, every string the domain is willing
		// to hand it — because the domain is the only validator, so a store that
		// refuses what the domain accepted refuses it with nobody to say why.
		//
		// A store must hold every byte sequence the domain lets through, so the
		// corpus below is filtered through ledger.ValidateText: anything the domain
		// rejects never reaches a store, and everything else has to survive the trip.
		// Loosening ValidateText widens this corpus, which is what makes this subtest
		// bite.
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

		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			for _, id := range []ledger.TransactionID{"tx_1", "tx_2", "tx_3"} {
				if err := tx.PutTransaction(ctx, bookA, transaction(id, "")); err != nil {
					return err
				}
			}
			return nil
		})

		var all []ledger.Transaction
		var lookupErr error
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

	t.Run("BookBalanceIncludesReversedTransactions", func(t *testing.T) {
		s := open(t, newStore, bookA)

		const cash ledger.AccountID = "100.100.001"
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookA, ledger.Transaction{
				ID:        "tx_1",
				Status:    ledger.Posted,
				CreatedAt: time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC),
				Entries: []ledger.Entry{
					{ID: "ent_1", AccountID: cash, Amount: 10_000, Direction: ledger.Debit},
				},
			})
		})
		assertEqual(t, "balance after posting", balance(t, s, cash), ledger.Amount(10_000))

		// Marking the original Reversed must NOT change the balance. The status
		// is informational; nothing has moved yet.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.MarkReversed(ctx, bookA, "tx_1")
		})
		assertEqual(t, "balance after MarkReversed", balance(t, s, cash), ledger.Amount(10_000))

		// The reversal's own mirrored entries are what cancel the original.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookA, ledger.Transaction{
				ID:         "tx_2",
				Status:     ledger.Posted,
				ReversalOf: "tx_1",
				CreatedAt:  time.Date(2025, 1, 15, 13, 0, 0, 0, time.UTC),
				Entries: []ledger.Entry{
					{ID: "ent_2", AccountID: cash, Amount: 10_000, Direction: ledger.Credit},
				},
			})
		})
		assertEqual(t, "balance after the reversal posts", balance(t, s, cash), ledger.Amount(0))

		// Both transactions remain visible: the audit trail is never rewritten.
		var all []ledger.Transaction
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			all, err = tx.ListTransactionsForPosition(ctx, bookA, ledger.AccountID(cash).Total())
			return err
		})
		assertEqual(t, "transactions for the account", len(all), 2)
	})

	t.Run("ValueDateBalanceCountsOnlyEntriesBeforeTheBound", func(t *testing.T) {
		s := open(t, newStore, bookA)

		day := func(d int) time.Time { return time.Date(2026, 4, d, 0, 0, 0, 0, time.UTC) }

		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			// Three debits on three consecutive days, one carrying a time of day.
			for i, when := range []time.Time{
				day(10),
				day(11).Add(23 * time.Hour),
				day(12),
			} {
				err := tx.PutTransaction(ctx, bookA, ledger.Transaction{
					ID:        ledger.TransactionID("txn_vdb_" + strconv.Itoa(i)),
					ValueDate: when,
					Entries: []ledger.Entry{
						{ID: ledger.EntryID("ent_vdb_d" + strconv.Itoa(i)), AccountID: "900.001.001", Amount: 100, Direction: ledger.Debit, ValueDate: when},
						{ID: ledger.EntryID("ent_vdb_c" + strconv.Itoa(i)), AccountID: "900.001.002", Amount: 100, Direction: ledger.Credit, ValueDate: when},
					},
				})
				if err != nil {
					return err
				}
			}
			return nil
		})

		cases := []struct {
			before time.Time
			want   ledger.Amount
		}{
			{day(10), 0},   // nothing value-dated before the 10th
			{day(11), 100}, // the 10th only
			{day(12), 200}, // the 10th and the 11th, time of day included
			{day(13), 300},
		}
		for _, c := range cases {
			var got ledger.Amount
			view(t, s, func(ctx context.Context, tx ledger.Tx) error {
				var err error
				got, err = tx.ValueDateBalance(ctx, bookA, ledger.AccountID("900.001.001").Total(), ledger.Debit, c.before)
				return err
			})
			assertEqual(t, fmt.Sprintf("balance before %v", c.before), got, c.want)
		}
	})

	t.Run("ValueDateBalanceOfUnknownAccountIsZero", func(t *testing.T) {
		s := open(t, newStore, bookA)

		var got ledger.Amount
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			got, err = tx.ValueDateBalance(ctx, bookA, ledger.AccountID("999.999.001").Total(), ledger.Debit,
				time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
			return err
		})
		assertEqual(t, "balance", got, ledger.Amount(0))
	})

	// ValueDateBalanceExcludesZeroValueDateEntries pins a rule two stores once
	// disagreed about and one store can still get wrong. The obvious in-Go
	// implementation (!ValueDate.Before(before)) counts a zero time.Time{},
	// which is before every real bound; a SQL store writes a zero date as NULL
	// and "NULL < ?" is never true. This fixture's own transaction() helper
	// creates exactly that shape, so the two answers were reachable on every
	// entry posted with no value date.
	//
	// The ruling: a zero ValueDate means "not value-dated", so it is excluded
	// from every bound rather than included in all of them. Book resolves
	// every entry it posts (see PostTransaction), so this only arises from a
	// Tx caller constructing an Entry directly, as this test does.
	t.Run("ValueDateBalanceExcludesZeroValueDateEntries", func(t *testing.T) {
		s := open(t, newStore, bookA)

		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookA, ledger.Transaction{
				ID:        "txn_vdb_zero",
				Status:    ledger.Posted,
				CreatedAt: time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
				// ValueDate deliberately left zero on both the transaction and
				// its entries.
				Entries: []ledger.Entry{
					{ID: "ent_vdb_zero_d", AccountID: "900.001.003", Amount: 100, Direction: ledger.Debit},
					{ID: "ent_vdb_zero_c", AccountID: "900.001.004", Amount: 100, Direction: ledger.Credit},
				},
			})
		})

		var got ledger.Amount
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			// A bound far in the future would catch this entry were a zero
			// ValueDate treated as "before everything", which is what the naive
			// in-Go check does.
			got, err = tx.ValueDateBalance(ctx, bookA, ledger.AccountID("900.001.003").Total(), ledger.Debit,
				time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
			return err
		})
		assertEqual(t, "balance", got, ledger.Amount(0))
	})

	// ValueDateBalanceNetsAReversalOnTheOriginalsDay exercises the contract
	// line in Tx.ValueDateBalance directly: a reversal posts its own mirrored
	// entries, value-dated onto the original leg's day (ReverseTransactionTx),
	// and those are what cancel the original — so a bound falling after the
	// original's value date must see the net, zero, not the gross.
	t.Run("ValueDateBalanceNetsAReversalOnTheOriginalsDay", func(t *testing.T) {
		s := open(t, newStore, bookA)

		const cash ledger.AccountID = "900.001.005"
		originalValue := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		before := ledger.NextDay(originalValue) // a bound after the original's day

		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookA, ledger.Transaction{
				ID:        "txn_vdb_rev_orig",
				Status:    ledger.Posted,
				CreatedAt: time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC),
				ValueDate: originalValue,
				Entries: []ledger.Entry{
					{ID: "ent_vdb_rev_orig", AccountID: cash, Amount: 10_000, Direction: ledger.Debit, ValueDate: originalValue},
				},
			})
		})

		var gross ledger.Amount
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			gross, err = tx.ValueDateBalance(ctx, bookA, ledger.AccountID(cash).Total(), ledger.Debit, before)
			return err
		})
		assertEqual(t, "balance before the reversal", gross, ledger.Amount(10_000))

		// Mark the original Reversed, then post the reversal's own mirrored
		// entry, value-dated onto the same day as the leg it corrects — what
		// ReverseTransactionTx actually does.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			if err := tx.MarkReversed(ctx, bookA, "txn_vdb_rev_orig"); err != nil {
				return err
			}
			return tx.PutTransaction(ctx, bookA, ledger.Transaction{
				ID:         "txn_vdb_rev_reversal",
				Status:     ledger.Posted,
				ReversalOf: "txn_vdb_rev_orig",
				CreatedAt:  time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
				ValueDate:  originalValue,
				Entries: []ledger.Entry{
					{ID: "ent_vdb_rev_reversal", AccountID: cash, Amount: 10_000, Direction: ledger.Credit, ValueDate: originalValue},
				},
			})
		})

		var netted ledger.Amount
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			netted, err = tx.ValueDateBalance(ctx, bookA, ledger.AccountID(cash).Total(), ledger.Debit, before)
			return err
		})
		assertEqual(t, "balance after the reversal, read on the original's day", netted, ledger.Amount(0))
	})

	t.Run("ValueDatedSeriesBucketsByDayAndCarriesAnOpening", func(t *testing.T) {
		s := open(t, newStore, bookA)
		day := func(d int) time.Time { return time.Date(2026, 6, d, 0, 0, 0, 0, time.UTC) }
		postValueDatedSeriesFixture(t, s, day)

		// The window is [4th, 9th): the 1st is opening; the 4th, 5th and 7th
		// are movements; the 9th is outside it (to is exclusive).
		var got ledger.Series
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			got, err = tx.ValueDatedSeries(ctx, bookA, ledger.AccountID("901.001.001").Total(), ledger.Debit, day(4), day(9))
			return err
		})

		// Opening is only the 1st: the 4th sits exactly ON from, and the
		// movement check just below is what actually pins that from is
		// inclusive. An implementation that used > instead of >= for the
		// lower bound would silently drop the 4th out of both Opening (still
		// 100, so this line alone would not catch it) and Movements (now 2
		// instead of 3, which the length check below does catch).
		assertEqual(t, "opening (only the 1st; the 4th is a movement, not opening)", got.Opening, ledger.Amount(100))
		if len(got.Movements) != 3 {
			t.Fatalf("movements = %d, want 3 (the 4th, the 5th, the 7th)", len(got.Movements))
		}
		if !got.Movements[0].Day.Equal(day(4)) || got.Movements[0].Amount != 50 {
			t.Errorf("movement[0] = %+v, want {%v 50} (from is inclusive)", got.Movements[0], day(4))
		}
		if !got.Movements[1].Day.Equal(day(5)) || got.Movements[1].Amount != 400 {
			t.Errorf("movement[1] = %+v, want {%v 400} (both of the 5th's postings, netted)", got.Movements[1], day(5))
		}
		if !got.Movements[2].Day.Equal(day(7)) || got.Movements[2].Amount != 0 {
			t.Errorf("movement[2] = %+v, want {%v 0} (equal and opposite postings still emit a zero movement, not none)", got.Movements[2], day(7))
		}
	})

	t.Run("ValueDatedSeriesSignsByNormalDirection", func(t *testing.T) {
		s := open(t, newStore, bookA)
		day := func(d int) time.Time { return time.Date(2026, 6, d, 0, 0, 0, 0, time.UTC) }
		postValueDatedSeriesFixture(t, s, day)

		// The credit side of the same postings, read with Credit as normal.
		var got ledger.Series
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			got, err = tx.ValueDatedSeries(ctx, bookA, ledger.AccountID("901.001.002").Total(), ledger.Credit, day(4), day(9))
			return err
		})
		assertEqual(t, "opening", got.Opening, ledger.Amount(100))
		if len(got.Movements) != 3 ||
			got.Movements[0].Amount != 50 || got.Movements[1].Amount != 400 || got.Movements[2].Amount != 0 {
			t.Errorf("movements = %+v, want 50, 400, 0", got.Movements)
		}

		// And read against the wrong normal, everything inverts — including
		// the 7th, which stays 0 either way (there is no negative zero).
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			got, err = tx.ValueDatedSeries(ctx, bookA, ledger.AccountID("901.001.002").Total(), ledger.Debit, day(4), day(9))
			return err
		})
		if got.Opening != -100 || len(got.Movements) != 3 ||
			got.Movements[0].Amount != -50 || got.Movements[1].Amount != -400 || got.Movements[2].Amount != 0 {
			t.Errorf("inverted series = %+v / opening %d, want -50, -400, 0 / -100", got.Movements, got.Opening)
		}
	})

	t.Run("ValueDatedSeriesOfEmptyWindowIsEmpty", func(t *testing.T) {
		s := open(t, newStore, bookA)
		day := func(d int) time.Time { return time.Date(2026, 6, d, 0, 0, 0, 0, time.UTC) }
		postValueDatedSeriesFixture(t, s, day)

		// [8th, 9th): past the 7th's net-zero pair (it contributes nothing
		// to Opening either way) and before the 9th. Nothing at all falls in
		// this window, so Movements is genuinely empty — unlike the 7th
		// above, which is a movement that happens to net to zero.
		var got ledger.Series
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			got, err = tx.ValueDatedSeries(ctx, bookA, ledger.AccountID("901.001.001").Total(), ledger.Debit, day(8), day(9))
			return err
		})
		assertEqual(t, "opening (the 1st, the 4th, both of the 5th, and the 7th's net-zero pair)", got.Opening, ledger.Amount(550))
		if len(got.Movements) != 0 {
			t.Errorf("movements = %+v, want none", got.Movements)
		}
	})

	// ValueDatedSeriesExcludesZeroValueDateEntries is the movement-bucketing
	// half of ValueDateBalanceExcludesZeroValueDateEntries above, and it needs
	// its own subtest: Series.Opening inherits that ruling by delegating to the
	// same balance query, but the per-day buckets are built by separate code and
	// could get it right in one place and wrong in the other.
	//
	// A SQL store gets it free — a zero value date is stored as NULL, and NULL
	// falls out of a day grouping, so there is no row to bucket. An in-Go one has
	// to skip it explicitly or it buckets the entry onto time.Time{}'s day, year 1,
	// and emits a movement for it. What this pins is that no movement for year 1
	// ever appears, however the buckets are built.
	t.Run("ValueDatedSeriesExcludesZeroValueDateEntries", func(t *testing.T) {
		s := open(t, newStore, bookA)
		day := func(d int) time.Time { return time.Date(2026, 6, d, 0, 0, 0, 0, time.UTC) }
		postValueDatedSeriesFixture(t, s, day)

		// Same shape as the fixture's own postings, into the same accounts, but
		// with the value date deliberately left zero on both the transaction
		// and its entries.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookA, ledger.Transaction{
				ID:     "txn_vds_zero",
				Status: ledger.Posted,
				Entries: []ledger.Entry{
					{ID: "ent_vds_zero_d", AccountID: "901.001.001", Amount: 700, Direction: ledger.Debit},
					{ID: "ent_vds_zero_c", AccountID: "901.001.002", Amount: 700, Direction: ledger.Credit},
				},
			})
		})

		// A window that starts before the year-1 day the naive bucketing would
		// produce would not distinguish it from a legitimate movement, so read
		// the same [4th, 9th) window every other subtest uses: the three
		// movements it contains must be exactly the three the fixture seeds,
		// and Opening must not have absorbed the 700 either.
		var got ledger.Series
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			got, err = tx.ValueDatedSeries(ctx, bookA, ledger.AccountID("901.001.001").Total(), ledger.Debit, day(4), day(9))
			return err
		})
		assertEqual(t, "opening (unchanged by an entry that is not value-dated)", got.Opening, ledger.Amount(100))
		if len(got.Movements) != 3 {
			t.Fatalf("movements = %+v, want the fixture's 3 (a zero value date is not a day)", got.Movements)
		}

		// And from the beginning of time, where the year-1 bucket would be in
		// range rather than merely before the window.
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			got, err = tx.ValueDatedSeries(ctx, bookA, ledger.AccountID("901.001.001").Total(), ledger.Debit, time.Time{}, day(9))
			return err
		})
		assertEqual(t, "opening from the beginning of time", got.Opening, ledger.Amount(0))
		if len(got.Movements) != 4 {
			t.Fatalf("movements from the beginning of time = %+v, want 4 (the 1st, 4th, 5th, 7th)", got.Movements)
		}
		if !got.Movements[0].Day.Equal(day(1)) {
			t.Errorf("first movement = %+v, want the 1st — a zero value date must not bucket onto year 1", got.Movements[0])
		}
	})

	t.Run("ValueDatedSeriesOfUnknownAccountIsEmpty", func(t *testing.T) {
		s := open(t, newStore, bookA)
		day := func(d int) time.Time { return time.Date(2026, 6, d, 0, 0, 0, 0, time.UTC) }
		// Seeded, so an empty result means "this account has none" rather than
		// "there is no data at all" — which an unseeded store cannot tell apart.
		postValueDatedSeriesFixture(t, s, day)

		var got ledger.Series
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			got, err = tx.ValueDatedSeries(ctx, bookA, ledger.AccountID("999.999.001").Total(), ledger.Debit, day(1), day(9))
			return err
		})
		assertEqual(t, "opening", got.Opening, ledger.Amount(0))
		if len(got.Movements) != 0 {
			t.Errorf("movements = %+v, want none", got.Movements)
		}
	})

	t.Run("MarkReversedIsConditional", func(t *testing.T) {
		s := open(t, newStore, bookA)

		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_1", ""))
		})

		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.MarkReversed(ctx, bookA, "tx_1")
		})

		// Posted -> Reversed happens at most once, so two racing reversals
		// cannot both win.
		second := s.Update(context.Background(), func(ctx context.Context, tx ledger.Tx) error {
			return tx.MarkReversed(ctx, bookA, "tx_1")
		})
		assertErrorIs(t, "second MarkReversed", second, ledger.ErrTransactionAlreadyReversed)

		missing := s.Update(context.Background(), func(ctx context.Context, tx ledger.Tx) error {
			return tx.MarkReversed(ctx, bookA, "tx_nope")
		})
		assertErrorIs(t, "MarkReversed on an unknown transaction", missing, ledger.ErrTransactionNotFound)

		var got ledger.Transaction
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			got, err = tx.GetTransaction(ctx, bookA, "tx_1")
			return err
		})
		assertEqual(t, "status", got.Status.String(), ledger.Reversed.String())
	})

	t.Run("AuditOrderedBySeq", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// The clock is frozen, so OccurredAt ties on every event: Seq, assigned
		// by the store, is the only thing that can order the log.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			for _, id := range []string{"evt_1", "evt_2", "evt_3", "evt_4"} {
				// Seq 999 is deliberately wrong; the store must overwrite it.
				if err := tx.AppendAudit(ctx, ledger.AuditEvent{
					Seq: 999, ID: id, BookID: bookA, Scope: ledger.ScopeLedger,
					Type: ledger.EventLedgerCreated, EntityID: id, OccurredAt: tx.Now(),
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
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

		// BookID naming ANOTHER institution's book is REFUSED, and that is the
		// point: an empty page is what a caller reading the wrong store would
		// otherwise get, and it is indistinguishable from end-of-data. Which
		// error is the implementation's to name — this package names no dialect
		// — so what is asserted is that there IS one.
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

		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

		// Seq is a store-GLOBAL total order, not per book or per scope, so a
		// scope's events are separated by gaps that belong to other scopes. A
		// pager that treated Before as "the previous few sequence numbers"
		// instead of as one predicate among the rest would return other layers'
		// events — or nothing at all.
		// One book, four kinds of event, because a store answers for one book.
		// The gaps this case needs come from the SCOPES rather than from the
		// books — which is what they always came from, since Seq is global and a
		// scope's events are what the filter selects. Two of these were book-b's
		// and book-c's; bookC exists for this case alone and now names nothing,
		// which is why it is gone from the constants above.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

		// Before composes with Scope and EntityID together. The two matches are
		// separated by a deposit-scope event, so paging between them only works
		// if Before is one predicate among the rest rather than a slice of the
		// global sequence.
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

		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		err := s.Update(context.Background(), func(ctx context.Context, tx ledger.Tx) error {
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
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookA, transaction("tx_1", "key-1"))
		})
	})

	t.Run("ConcurrentReadThenWriteOnOneKeyAgrees", func(t *testing.T) {
		s := open(t, newStore, bookA)

		// This suite's only case with two units of work running AT ONCE, and
		// the gap it closes is the reason it exists.
		//
		// Everything else here is single-threaded, so the suite would otherwise say
		// nothing at all about a rule the APPLICATION makes by reading and then
		// writing.
		//
		// races.go does the same job on payment's acts, which is where the rule this
		// stands in for actually lives; the stand-in here was blind to three money
		// defects of that shape. What is left for this one is the shape stated at the
		// store interface with no act to hang it on, which is what a store
		// implementer reads.
		//
		// What it encodes is the shape payment.SubmitPaymentTx uses to refuse a
		// duplicate client reference: allocate an id, THEN read the key, THEN
		// write. NextID is what makes it atomic — its row on the identity
		// counter is locked until the transaction ends, so the second caller
		// blocks there and, when it gets through, either sees the first
		// caller's committed row or finds the first rolled back and the id with
		// it. The same serialization every admission act that decides from a READ
		// relies on — see payment.admissionSequenceTx, which draws an id for the
		// lock and throws the number away. FoundBankTx is the one that does not
		// call it, and does not need to: it allocates the bank's own id before it
		// touches anything, which is this same ordering under a different name.
		//
		// The claim is exactly that and no wider. It does NOT say a store makes
		// read-then-write atomic on its own; it says this ORDERING admits one writer,
		// whatever is underneath.
		//
		// On store/sqlite the ordering is not the only thing holding it: the store
		// admits one writer and Store.Update re-runs a loser against the winner's
		// committed row, measured at ten runs out of ten with the ordering removed.
		// See payment.admissionSequenceTx. The case stays as the statement of the
		// shape a replacement has to keep.
		//
		// The read is on a NON-key attribute (the account's name) because that
		// is the shape of the rule: a store cannot enforce it with a primary
		// key, and a unique index would answer a constraint violation where the
		// domain answers its sentinel. See
		// transactions_idempotency_key_idx in store/sqlite/schema/bank/0001_init.sql.
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
				errs[i] = s.Update(context.Background(), func(ctx context.Context, tx ledger.Tx) error {
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
					// TWO writes, as the operation this stands for has: a
					// payment row and the ledger postings behind it. The
					// window a wrong ordering leaves open is the interval
					// between the read and the commit, so a case whose write
					// was a single row would be measuring a narrower race than
					// the one that shipped.
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
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			held, err = tx.ListAccounts(ctx, bookA)
			return err
		})
		assertEqual(t, "accounts named "+wanted, len(held), 1)
	})
}

// errTaken is the losers' answer in ConcurrentReadThenWriteOnOneKeyAgrees: the
// name was already claimed. It stands in for payment.ErrDuplicateEndToEndID
// rather than naming it — races.go names payment's sentinels directly, on the
// acts themselves. What this case is worth beside those is the shape stated once
// at the store interface, for rules the acts have not made yet.
var errTaken = errors.New("storetest: the name is already claimed")

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// open builds a fresh store for one subtest and closes it when the subtest ends.
func open(t *testing.T, newStore func(*testing.T, ledger.BookID) ledger.Store, book ledger.BookID) ledger.Store {
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
func update(t *testing.T, s ledger.Store, fn func(context.Context, ledger.Tx) error) {
	t.Helper()
	if err := s.Update(context.Background(), fn); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// view runs a read-only unit of work that is expected to succeed.
func view(t *testing.T, s ledger.Store, fn func(context.Context, ledger.Tx) error) {
	t.Helper()
	if err := s.View(context.Background(), fn); err != nil {
		t.Fatalf("View: %v", err)
	}
}

// balance reads one account's book balance, in the Debit-normal direction.
func balance(t *testing.T, s ledger.Store, id ledger.AccountID) ledger.Amount {
	t.Helper()
	var out ledger.Amount
	view(t, s, func(ctx context.Context, tx ledger.Tx) error {
		var err error
		out, err = tx.BookBalance(ctx, bookA, ledger.AccountID(id).Total(), ledger.Debit)
		return err
	})
	return out
}

// audit reads the audit log through a filter.
func audit(t *testing.T, s ledger.Store, f ledger.AuditFilter) []ledger.AuditEvent {
	t.Helper()
	var out []ledger.AuditEvent
	view(t, s, func(ctx context.Context, tx ledger.Tx) error {
		var err error
		out, err = tx.ListAudit(ctx, f)
		return err
	})
	return out
}

// listAudit is audit without the assertion, for the one case that is about the
// ERROR rather than about the page: a filter naming another institution's book
// must be refused, and audit above would fail the test on the way past.
func listAudit(s ledger.Store, f ledger.AuditFilter) ([]ledger.AuditEvent, error) {
	var out []ledger.AuditEvent
	err := s.View(context.Background(), func(ctx context.Context, tx ledger.Tx) error {
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

// postValueDatedSeriesFixture seeds the postings the ValueDatedSeries*
// subtests share, between 901.001.001 ("acct1") and 901.001.002 ("acct2"),
// each posting a balanced debit/credit pair:
//
//   - the 1st:  100, acct1 debited — before every from used below, so it is
//     always opening, never a movement.
//   - the 4th:   50, acct1 debited — sits exactly ON a from used below, to
//     pin that from is inclusive: it must appear as its own movement and
//     never fold into Opening.
//   - the 5th:  200 + 200 (one carrying a time of day), both acct1 debited —
//     nets into one movement of 400, pinning that same-day postings are
//     netted rather than listed separately.
//   - the 7th:  300 acct1 debited, 300 acct2 debited (i.e. acct1 credited) —
//     equal and opposite on both accounts, netting to zero on either. Pins
//     that a net-zero day is still emitted as a movement (Amount 0) rather
//     than filtered out.
//   - the 9th:  400, acct1 debited — outside every window used below (to is
//     exclusive), so it never appears in Opening or Movements.
func postValueDatedSeriesFixture(t *testing.T, s ledger.Store, day func(int) time.Time) {
	t.Helper()
	const (
		acct1 ledger.AccountID = "901.001.001"
		acct2 ledger.AccountID = "901.001.002"
	)
	posts := []struct {
		id     string
		when   time.Time
		amount ledger.Amount
		debit  ledger.AccountID // the other account is credited
	}{
		{"a", day(1), 100, acct1},
		{"e", day(4), 50, acct1},
		{"b", day(5), 200, acct1},
		{"c", day(5).Add(17 * time.Hour), 200, acct1},
		{"f", day(7), 300, acct1},
		{"g", day(7), 300, acct2},
		{"d", day(9), 400, acct1},
	}
	update(t, s, func(ctx context.Context, tx ledger.Tx) error {
		for _, p := range posts {
			credit := acct2
			if p.debit == acct2 {
				credit = acct1
			}
			err := tx.PutTransaction(ctx, bookA, ledger.Transaction{
				ID:        ledger.TransactionID("txn_vds_" + p.id),
				ValueDate: p.when,
				Entries: []ledger.Entry{
					{ID: ledger.EntryID("ent_vds_d" + p.id), AccountID: p.debit, Amount: p.amount, Direction: ledger.Debit, ValueDate: p.when},
					{ID: ledger.EntryID("ent_vds_c" + p.id), AccountID: credit, Amount: p.amount, Direction: ledger.Credit, ValueDate: p.when},
				},
			})
			if err != nil {
				return err
			}
		}
		return nil
	})
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
// It reports rather than fails, so one run names every listing that drifted
// instead of stopping at the first.
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
