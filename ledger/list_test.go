package ledger_test

import (
	"context"
	"testing"

	. "github.com/raphi011/cbs/ledger"
)

func TestListLedgersAndSubledgers(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)

	gl, err := book.CreateLedger(ctx, "General Ledger")
	assertNoError(t, err)
	trading, err := book.CreateLedger(ctx, "Trading Book")
	assertNoError(t, err)

	ledgers, err := book.ListLedgers(ctx)
	assertNoError(t, err)
	assertEqual(t, "ledger count", len(ledgers), 2)
	// Stable order: GL was created first.
	assertEqual(t, "first ledger", ledgers[0].ID, gl.ID)
	assertEqual(t, "second ledger", ledgers[1].ID, trading.ID)

	ar, err := book.CreateSubledger(ctx, gl.ID, "Accounts Receivable")
	assertNoError(t, err)
	ap, err := book.CreateSubledger(ctx, gl.ID, "Accounts Payable")
	assertNoError(t, err)

	subs, err := book.ListSubledgers(ctx, gl.ID)
	assertNoError(t, err)
	assertEqual(t, "subledger count for GL", len(subs), 2)
	assertEqual(t, "first subledger", subs[0].ID, ar.ID)
	assertEqual(t, "second subledger", subs[1].ID, ap.ID)

	// A different ledger has no subledgers.
	tradingSubs, err := book.ListSubledgers(ctx, trading.ID)
	assertNoError(t, err)
	assertEqual(t, "subledgers for trading", len(tradingSubs), 0)
	// An unknown ledger yields an empty slice, not nil-panic.
	unknownSubs, err := book.ListSubledgers(ctx, "nope")
	assertNoError(t, err)
	assertEqual(t, "subledgers for unknown", len(unknownSubs), 0)
}

func TestListAccountsAndTransactions(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, bob, cash, _ := setupChartOfAccounts(t, book)

	// alice and bob live under the Customer Deposits subledger.
	accts, err := book.ListAccounts(ctx, alice.SubledgerID)
	assertNoError(t, err)
	assertEqual(t, "accounts in deposits subledger", len(accts), 2)

	// Post a transfer touching alice and cash.
	_, err = book.PostTransaction(ctx, PostTransactionRequest{
		Description: "Alice deposits cash",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 5000, Direction: Debit},
			{AccountID: alice.ID, Amount: 5000, Direction: Credit},
		},
	})
	assertNoError(t, err)

	all, err := book.ListTransactions(ctx)
	assertNoError(t, err)
	assertEqual(t, "total transactions", len(all), 1)

	forAlice, err := book.ListTransactionsForAccount(ctx, alice.ID)
	assertNoError(t, err)
	assertEqual(t, "transactions for alice", len(forAlice), 1)

	forBob, err := book.ListTransactionsForAccount(ctx, bob.ID)
	assertNoError(t, err)
	assertEqual(t, "transactions for bob", len(forBob), 0)
}
