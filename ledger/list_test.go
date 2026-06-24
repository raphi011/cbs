package ledger

import "testing"

func TestListLedgersAndSubledgers(t *testing.T) {
	book := testBook(t)

	gl, err := book.CreateLedger("General Ledger")
	assertNoError(t, err)
	trading, err := book.CreateLedger("Trading Book")
	assertNoError(t, err)

	ledgers := book.ListLedgers()
	assertEqual(t, "ledger count", len(ledgers), 2)
	// Stable order: GL was created first.
	assertEqual(t, "first ledger", ledgers[0].ID, gl.ID)
	assertEqual(t, "second ledger", ledgers[1].ID, trading.ID)

	ar, err := book.CreateSubledger(gl.ID, "Accounts Receivable")
	assertNoError(t, err)
	ap, err := book.CreateSubledger(gl.ID, "Accounts Payable")
	assertNoError(t, err)

	subs := book.ListSubledgers(gl.ID)
	assertEqual(t, "subledger count for GL", len(subs), 2)
	assertEqual(t, "first subledger", subs[0].ID, ar.ID)
	assertEqual(t, "second subledger", subs[1].ID, ap.ID)

	// A different ledger has no subledgers.
	assertEqual(t, "subledgers for trading", len(book.ListSubledgers(trading.ID)), 0)
	// An unknown ledger yields an empty slice, not nil-panic.
	assertEqual(t, "subledgers for unknown", len(book.ListSubledgers("nope")), 0)
}

func TestListAccountsAndTransactions(t *testing.T) {
	book := testBook(t)
	alice, bob, cash, _ := setupChartOfAccounts(t, book)

	// alice and bob live under the Customer Deposits subledger.
	accts := book.ListAccounts(alice.SubledgerID)
	assertEqual(t, "accounts in deposits subledger", len(accts), 2)

	// Post a transfer touching alice and cash.
	_, err := book.PostTransaction(PostTransactionRequest{
		Description: "Alice deposits cash",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 5000, Direction: Debit},
			{AccountID: alice.ID, Amount: 5000, Direction: Credit},
		},
	})
	assertNoError(t, err)

	all := book.ListTransactions()
	assertEqual(t, "total transactions", len(all), 1)

	forAlice := book.ListTransactionsForAccount(alice.ID)
	assertEqual(t, "transactions for alice", len(forAlice), 1)

	forBob := book.ListTransactionsForAccount(bob.ID)
	assertEqual(t, "transactions for bob", len(forBob), 0)
}
