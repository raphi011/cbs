// Package storetest is the conformance suite every store implementation must
// pass: RunLedger for the ledger layer, RunDeposit for the deposit layer and
// RunPayment for the payment layer.
//
// It is a normal package rather than a set of _test.go files, because
// store/mem and store/pg both import it from their own tests. It talks only to
// the Store and Tx interfaces — never to ledger.Book, deposit.Register or
// payment.Network — so it pins the storage contract itself: identity
// allocation, ordering, idempotency, the balance aggregate, the audit log and
// rollback. Anything the implementations could plausibly disagree about belongs
// here, because this suite is the only thing keeping them from drifting apart.
//
// # Ordering
//
// Every listing is ORDER BY created_at, seq, where seq is a monotonic per-book,
// per-table sequence assigned when a row is first inserted — a counter in
// store/mem, a BIGSERIAL column in store/pg. An upsert must not reissue it, or
// editing a row moves it to the end of the list.
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

// RunLedger runs the ledger-layer conformance suite against a store. Both
// store/mem and store/pg must pass it identically — this is the main defence
// against the two implementations drifting.
//
// newStore must return a store with no state in it; the suite calls it once per
// subtest and closes the result.
func RunLedger(t *testing.T, newStore func(*testing.T) ledger.Store) {
	t.Helper()

	t.Run("NextSubledgerBlockStepsBy100PerBook", func(t *testing.T) {
		s := open(t, newStore)

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
		s := open(t, newStore)

		// ONE counter per book, shared by every prefix — not one counter per
		// prefix. The number doubles as a creation order, which is why ldg_1,
		// tx_2 and ent_3 interleave rather than each restarting at 1. A store
		// that keyed its counter by (book, prefix) would still hand out unique
		// IDs, so nothing else in this suite would notice.
		var inA, inB []string
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			for _, prefix := range []string{"ldg", "tx", "ent", "evt", "tx"} {
				id, err := tx.NextID(ctx, bookA, prefix)
				if err != nil {
					return err
				}
				inA = append(inA, id)
			}
			// A second book numbers independently, from its own 1.
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
		s := open(t, newStore)

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

			forAccount, err := tx.ListTransactionsForAccount(ctx, bookA, "200.100.002")
			if err != nil {
				return err
			}
			assertEqual(t, "transactions for the account", len(forAccount), 1)
			// Every leg comes back, not only the ones naming the account.
			check("ListTransactionsForAccount entries", forAccount[0].Entries)
			return nil
		})
	})

	t.Run("NextAccountSeqResetsPerTypeAndSubledger", func(t *testing.T) {
		s := open(t, newStore)

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
		s := open(t, newStore)

		// Chart-of-accounts IDs are unique within a book, not globally: two
		// banks may both hold an account numbered 200.100.001.
		const shared ledger.AccountID = "200.100.001"
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			if err := tx.PutAccount(ctx, bookA, ledger.Account{ID: shared, Name: "Alice at A", Type: ledger.Liability}); err != nil {
				return err
			}
			return tx.PutAccount(ctx, bookB, ledger.Account{ID: shared, Name: "Bob at B", Type: ledger.Liability})
		})

		var inA, inB ledger.Account
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			var err error
			if inA, err = tx.GetAccount(ctx, bookA, shared); err != nil {
				return err
			}
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

	t.Run("GetOnMissingRowsReturnsSentinels", func(t *testing.T) {
		s := open(t, newStore)

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

		// The same IDs in another book are equally not found: a lookup that
		// forgot to scope by book would return book-a's rows here.
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
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

	t.Run("ListOrderingIsCreatedAtThenSeq", func(t *testing.T) {
		s := open(t, newStore)

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
			forAccount, err = tx.ListTransactionsForAccount(ctx, bookA, "200.100.008")
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
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutAccount(ctx, bookA, ledger.Account{
				ID: "200.100.008", SubledgerID: "100", Type: ledger.Liability, Name: "renamed", CreatedAt: early,
			})
		})
		view(t, s, func(ctx context.Context, tx ledger.Tx) error {
			reordered, err := tx.ListAccounts(ctx, bookA)
			if err != nil {
				return err
			}
			assertOrder(t, "ListAccounts after an upsert", ids(reordered, func(a ledger.Account) string { return string(a.ID) }),
				"200.100.008", "200.100.020", "200.100.009", "100.200.001")
			return nil
		})
	})

	t.Run("DuplicateIdempotencyKeyRejected", func(t *testing.T) {
		s := open(t, newStore)

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

		// Keys are per book, so the same key is free in another book.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			return tx.PutTransaction(ctx, bookB, transaction("tx_9", "key-1"))
		})
	})

	t.Run("EmptyIdempotencyKeyNotDeduplicated", func(t *testing.T) {
		s := open(t, newStore)

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
		s := open(t, newStore)

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
			all, err = tx.ListTransactionsForAccount(ctx, bookA, cash)
			return err
		})
		assertEqual(t, "transactions for the account", len(all), 2)
	})

	t.Run("MarkReversedIsConditional", func(t *testing.T) {
		s := open(t, newStore)

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
		s := open(t, newStore)

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
		s := open(t, newStore)

		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			for _, e := range []ledger.AuditEvent{
				{ID: "evt_1", BookID: bookA, Scope: ledger.ScopeLedger, Type: ledger.EventLedgerCreated, EntityID: "ldg_1"},
				{ID: "evt_2", BookID: bookA, Scope: ledger.ScopeLedger, Type: ledger.EventAccountCreated, EntityID: "200.100.001"},
				{ID: "evt_3", BookID: bookA, Scope: ledger.ScopeDeposit, Type: ledger.EventAccountOpened, EntityID: "dep_1"},
				{ID: "evt_4", BookID: bookB, Scope: ledger.ScopeLedger, Type: ledger.EventAccountCreated, EntityID: "200.100.001"},
			} {
				if err := tx.AppendAudit(ctx, e); err != nil {
					return err
				}
			}
			return nil
		})

		// No filter: everything, from every book and scope.
		assertEqual(t, "unfiltered", len(audit(t, s, ledger.AuditFilter{})), 4)

		// BookID narrows to one book.
		byBook := audit(t, s, ledger.AuditFilter{BookID: bookB})
		assertEqual(t, "by book", len(byBook), 1)
		assertEqual(t, "by book id", byBook[0].ID, "evt_4")

		// Scope narrows to one layer.
		byScope := audit(t, s, ledger.AuditFilter{Scope: ledger.ScopeDeposit})
		assertEqual(t, "by scope", len(byScope), 1)
		assertEqual(t, "by scope id", byScope[0].ID, "evt_3")

		// Type narrows to one event type, across books.
		byType := audit(t, s, ledger.AuditFilter{Type: ledger.EventAccountCreated})
		assertEqual(t, "by type", len(byType), 2)

		// EntityID alone is not book-scoped, so it finds both books' rows; with
		// a book it finds one.
		byEntity := audit(t, s, ledger.AuditFilter{EntityID: "200.100.001"})
		assertEqual(t, "by entity", len(byEntity), 2)
		byBookAndEntity := audit(t, s, ledger.AuditFilter{BookID: bookA, EntityID: "200.100.001"})
		assertEqual(t, "by book and entity", len(byBookAndEntity), 1)
		assertEqual(t, "by book and entity id", byBookAndEntity[0].ID, "evt_2")

		// Filters compose.
		combined := audit(t, s, ledger.AuditFilter{BookID: bookA, Scope: ledger.ScopeLedger, Type: ledger.EventAccountCreated})
		assertEqual(t, "combined", len(combined), 1)
		assertEqual(t, "combined id", combined[0].ID, "evt_2")

		// Before is an exclusive upper bound on Seq.
		all := audit(t, s, ledger.AuditFilter{})
		before := audit(t, s, ledger.AuditFilter{Before: all[2].Seq})
		assertEqual(t, "before the third event", len(before), 2)
		assertEqual(t, "first before", before[0].ID, "evt_1")
		assertEqual(t, "second before", before[1].ID, "evt_2")
	})

	t.Run("AuditLimitKeepsNewestBelowBefore", func(t *testing.T) {
		s := open(t, newStore)

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
		s := open(t, newStore)

		// Seq is a store-GLOBAL total order, not per book or per scope, so a
		// scope's events are separated by gaps that belong to other scopes. A
		// pager that treated Before as "the previous few sequence numbers"
		// instead of as one predicate among the rest would return other layers'
		// events — or nothing at all.
		update(t, s, func(ctx context.Context, tx ledger.Tx) error {
			for round := range 3 {
				for _, e := range []ledger.AuditEvent{
					{BookID: bookA, Scope: ledger.ScopeLedger, Type: ledger.EventAccountCreated},
					{BookID: bookA, Scope: ledger.ScopeDeposit, Type: ledger.EventAccountOpened},
					{BookID: bookB, Scope: ledger.ScopeLedger, Type: ledger.EventAccountCreated},
					{BookID: ledger.NetworkBook, Scope: ledger.ScopePayment, Type: ledger.EventPaymentAccepted},
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

		filter := ledger.AuditFilter{BookID: ledger.NetworkBook, Scope: ledger.ScopePayment}
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
		// in different books and are separated by a deposit-scope event, so
		// paging between them only works if Before is one predicate among the
		// rest rather than a slice of the global sequence.
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
		s := open(t, newStore)

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
		s := open(t, newStore)

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
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// open builds a fresh store for one subtest and closes it when the subtest ends.
func open(t *testing.T, newStore func(*testing.T) ledger.Store) ledger.Store {
	t.Helper()
	s := newStore(t)
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
		out, err = tx.BookBalance(ctx, bookA, id, ledger.Debit)
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
