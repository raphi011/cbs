package ledger_test

import (
	"context"
	"testing"
	"time"

	. "github.com/raphi011/cbs/ledger"
)

// createControlAccount is CreateControlAccountTx in its own unit of work. There
// is no plain form on Book — nothing outside a test creates one on its own —
// so the tests below open the Update themselves.
func createControlAccount(t *testing.T, book *Book, subledger SubledgerID, name string, typ AccountType) Account {
	t.Helper()
	var out Account
	err := book.Store().Update(context.Background(), func(ctx context.Context, tx Tx) error {
		var err error
		out, err = book.CreateControlAccountTx(ctx, tx, subledger, name, typ, testAsset)
		return err
	})
	assertNoError(t, err)
	return out
}

// pooledChart is a control account and a plain contra: the smallest chart on
// which the dimension is visible at all.
//
//	General Ledger
//	  ├── Customer Deposits (subledger)
//	  │     └── Customer Deposits (Liability, CONTROL)
//	  └── Bank Assets (subledger)
//	        └── Vault Cash (Asset, plain)
func pooledChart(t *testing.T, book *Book) (deposits, vault Account) {
	t.Helper()
	ctx := context.Background()

	gl, err := book.CreateLedger(ctx, "General Ledger")
	assertNoError(t, err)
	depositSub, err := book.CreateSubledger(ctx, gl.ID, "Customer Deposits")
	assertNoError(t, err)
	assetSub, err := book.CreateSubledger(ctx, gl.ID, "Bank Assets")
	assertNoError(t, err)

	deposits = createControlAccount(t, book, depositSub.ID, "Customer Deposits", Liability)
	vault, err = book.CreateAccount(ctx, assetSub.ID, "Vault Cash", Asset, testAsset)
	assertNoError(t, err)
	return deposits, vault
}

// takeIn is a cash deposit for one subsidiary: vault cash rises, the pool owes one
// more customer.
func takeIn(t *testing.T, book *Book, deposits, vault Account, subsidiary string, amount Amount) Transaction {
	t.Helper()
	posted, err := book.PostTransaction(context.Background(), PostTransactionRequest{
		Description: "cash in for " + subsidiary,
		Entries: []Entry{
			{AccountID: vault.ID, Amount: amount, Direction: Debit},
			{AccountID: deposits.ID, Subsidiary: subsidiary, Amount: amount, Direction: Credit},
		},
	})
	assertNoError(t, err)
	return posted
}

// TestAControlAccountRefusesAnUnqualifiedEntry is one half of the pair, and the
// obvious half: money credited to a pool with no subsidiary named belongs to
// nobody, and no later read can say whose it was.
func TestAControlAccountRefusesAnUnqualifiedEntry(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	deposits, vault := pooledChart(t, book)

	_, err := book.PostTransaction(ctx, PostTransactionRequest{
		Description: "cash in for nobody",
		Entries: []Entry{
			{AccountID: vault.ID, Amount: 1000, Direction: Debit},
			{AccountID: deposits.ID, Amount: 1000, Direction: Credit},
		},
	})
	assertError(t, err, ErrSubsidiaryRequired)

	// Refused means nothing was written, not that a half was.
	balance, err := book.BookBalance(ctx, vault.ID.Total())
	assertNoError(t, err)
	assertEqual(t, "vault cash after the refusal", balance, 0)
}

// TestAPlainAccountRefusesAQualifiedEntry is the other half, and the one that
// is not the obvious way round.
//
// Nothing aggregates a plain account by subsidiary, so the dimension would be
// written and never read: the posting would balance, the account's own balance
// would stay right, and the caller's belief that it had recorded whose money
// this is would be false with nothing to contradict it.
func TestAPlainAccountRefusesAQualifiedEntry(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	deposits, vault := pooledChart(t, book)

	_, err := book.PostTransaction(ctx, PostTransactionRequest{
		Description: "cash in, with the vault told whose it is",
		Entries: []Entry{
			{AccountID: vault.ID, Subsidiary: "dep_1", Amount: 1000, Direction: Debit},
			{AccountID: deposits.ID, Subsidiary: "dep_1", Amount: 1000, Direction: Credit},
		},
	})
	assertError(t, err, ErrSubsidiaryNotAllowed)
}

// TestTheSufficiencyCheckReadsTheSubsidiaryAndNotThePool is the test that would
// have caught the silent version of this design.
//
// The guard keeps Asset and Expense accounts off the wrong side of zero. Under
// pooling the pool is comfortably positive while a subsidiary under it is
// overdrawn, so a check that kept reading the account would pass, report
// nothing, and have stopped guarding — every Asset-side facility unbounded,
// with debits still equal to credits throughout.
func TestTheSufficiencyCheckReadsTheSubsidiaryAndNotThePool(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)

	gl, err := book.CreateLedger(ctx, "General Ledger")
	assertNoError(t, err)
	assetSub, err := book.CreateSubledger(ctx, gl.ID, "Bank Assets")
	assertNoError(t, err)
	fundingSub, err := book.CreateSubledger(ctx, gl.ID, "Funding")
	assertNoError(t, err)

	loans := createControlAccount(t, book, assetSub.ID, "Loans Principal", Asset)
	funding, err := book.CreateAccount(ctx, fundingSub.ID, "Wholesale Funding", Liability, testAsset)
	assertNoError(t, err)

	// One borrower draws a thousand. The pool holds a thousand; the other
	// borrower holds nothing.
	_, err = book.PostTransaction(ctx, PostTransactionRequest{
		Description: "advance to fac_1",
		Entries: []Entry{
			{AccountID: loans.ID, Subsidiary: "fac_1", Amount: 1000, Direction: Debit},
			{AccountID: funding.ID, Amount: 1000, Direction: Credit},
		},
	})
	assertNoError(t, err)

	pool, err := book.BookBalance(ctx, loans.ID.Total())
	assertNoError(t, err)
	assertEqual(t, "the pool before the repayment", pool, 1000)

	// A repayment on a borrower who has drawn nothing. The pool would still
	// stand at 600 and the transaction balances; only the subsidiary goes negative.
	_, err = book.PostTransaction(ctx, PostTransactionRequest{
		Description: "a repayment against a borrower who never drew",
		Entries: []Entry{
			{AccountID: loans.ID, Subsidiary: "fac_2", Amount: 400, Direction: Credit},
			{AccountID: funding.ID, Amount: 400, Direction: Debit},
		},
	})
	assertError(t, err, ErrInsufficientBalance)

	// And the guard does not fire on the borrower who did draw, which is what
	// says it is reading the subsidiary rather than refusing everything.
	_, err = book.PostTransaction(ctx, PostTransactionRequest{
		Description: "a repayment against the borrower who drew",
		Entries: []Entry{
			{AccountID: loans.ID, Subsidiary: "fac_1", Amount: 400, Direction: Credit},
			{AccountID: funding.ID, Amount: 400, Direction: Debit},
		},
	})
	assertNoError(t, err)

	drawn, err := book.BookBalance(ctx, loans.ID.For("fac_1"))
	assertNoError(t, err)
	assertEqual(t, "fac_1 after repaying 400 of 1000", drawn, 600)
}

// TestTheSubsidiaryBalancesSumToTheControlBalance cannot fail arithmetically,
// which is exactly why it is worth computing.
//
// It is the same argument a trial balance is built on: the two figures come
// from one aggregate with one predicate between them, so the sum is a control
// on the PIPELINE rather than on the arithmetic. What it would catch is a
// direct store write, a fixture that bypassed the posting refusals, or a
// balance query that grew a predicate it should not have.
func TestTheSubsidiaryBalancesSumToTheControlBalance(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	deposits, vault := pooledChart(t, book)

	subsidiaries := map[string]Amount{"dep_1": 1000, "dep_2": 250, "dep_3": 7}
	var want Amount
	for subsidiary, amount := range subsidiaries {
		takeIn(t, book, deposits, vault, subsidiary, amount)
		want += amount
	}

	control, err := book.BookBalance(ctx, deposits.ID.Total())
	assertNoError(t, err)
	assertEqual(t, "the control balance", control, want)

	var detail Amount
	for subsidiary := range subsidiaries {
		balance, err := book.BookBalance(ctx, deposits.ID.For(subsidiary))
		assertNoError(t, err)
		assertEqual(t, "the balance of "+subsidiary, balance, subsidiaries[subsidiary])
		detail += balance
	}
	assertEqual(t, "the detail against the control", detail, control)
}

// TestAStatementOverOneSubsidiaryCarriesNoOtherSubsidiariesPostings is what a customer
// recognises: their own account, over a chart of accounts line they share with
// everyone else at the bank.
//
// The transactions are returned WHOLE — a statement row still carries the other
// leg of its own event — and it is the position that selects among the legs.
func TestAStatementOverOneSubsidiaryCarriesNoOtherSubsidiariesPostings(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	deposits, vault := pooledChart(t, book)

	first := takeIn(t, book, deposits, vault, "dep_1", 1000)
	takeIn(t, book, deposits, vault, "dep_2", 250)
	second := takeIn(t, book, deposits, vault, "dep_1", 40)

	hist, err := book.AccountHistory(ctx, deposits.ID.For("dep_1"))
	assertNoError(t, err)
	assertEqual(t, "rows in dep_1's statement", len(hist.Rows), 2)
	assertEqual(t, "the first row", hist.Rows[0].Transaction, first.ID)
	assertEqual(t, "the second row", hist.Rows[1].Transaction, second.ID)
	assertEqual(t, "the running balance", hist.Rows[1].Running, 1040)
	assertEqual(t, "the closing balance", hist.Closing, 1040)

	// The pool's own history is every subsidiary's, and it closes at the control
	// figure.
	pooled, err := book.AccountHistory(ctx, deposits.ID.Total())
	assertNoError(t, err)
	assertEqual(t, "rows in the pool's history", len(pooled.Rows), 3)
	assertEqual(t, "the pool's closing balance", pooled.Closing, 1290)
}

// TestAReversalCarriesTheSubsidiaryItReverses pins the one place the dimension
// could be dropped without any refusal firing: a reversal builds its own
// entries, and a mirrored leg that credited the pool unqualified would be
// refused — but one that named the WRONG subsidiary would post cleanly, leave the
// pool square, and leave one customer permanently short.
func TestAReversalCarriesTheSubsidiaryItReverses(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	deposits, vault := pooledChart(t, book)

	posted := takeIn(t, book, deposits, vault, "dep_1", 1000)
	takeIn(t, book, deposits, vault, "dep_2", 250)

	_, err := book.ReverseTransaction(ctx, posted.ID, "taken in against the wrong account")
	assertNoError(t, err)

	reversed, err := book.BookBalance(ctx, deposits.ID.For("dep_1"))
	assertNoError(t, err)
	assertEqual(t, "dep_1 after the reversal", reversed, 0)

	untouched, err := book.BookBalance(ctx, deposits.ID.For("dep_2"))
	assertNoError(t, err)
	assertEqual(t, "dep_2, which the reversal did not name", untouched, 250)
}

// TestTheValueDatedReadsCarryTheSubsidiaryToo. The book balance is not the only
// read that grows the dimension: interest is computed from a value-dated series
// and would otherwise accrue on the whole pool for every customer under it.
func TestTheValueDatedReadsCarryTheSubsidiaryToo(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	deposits, vault := pooledChart(t, book)

	day := func(n int) time.Time {
		return time.Date(2025, 1, n, 0, 0, 0, 0, time.UTC)
	}
	post := func(subsidiary string, amount Amount, on time.Time) {
		t.Helper()
		_, err := book.PostTransaction(ctx, PostTransactionRequest{
			Description: "cash in for " + subsidiary,
			ValueDate:   on,
			Entries: []Entry{
				{AccountID: vault.ID, Amount: amount, Direction: Debit},
				{AccountID: deposits.ID, Subsidiary: subsidiary, Amount: amount, Direction: Credit},
			},
		})
		assertNoError(t, err)
	}
	post("dep_1", 1000, day(4))
	post("dep_2", 250, day(4))
	post("dep_1", 40, day(6))

	balance, err := book.ValueDateBalance(ctx, deposits.ID.For("dep_1"), day(5))
	assertNoError(t, err)
	assertEqual(t, "dep_1 as at the 5th", balance, 1000)

	var series Series
	err = book.Store().View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		series, err = book.SeriesTx(ctx, tx, deposits.ID.For("dep_1"), day(5), day(6))
		return err
	})
	assertNoError(t, err)
	assertEqual(t, "dep_1's opening on the 5th", series.Opening, 1000)
	assertEqual(t, "days dep_1 moved on", len(series.Movements), 1)
	assertEqual(t, "dep_1's movement on the 6th", series.Movements[0].Amount, 40)
}
