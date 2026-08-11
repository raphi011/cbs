package ledger_test

import (
	"context"
	"testing"

	. "github.com/raphi011/cbs/ledger"
)

// rowFor is the trial balance line for one account, and a failure if there is
// none: every account in the chart is listed, so an absent row is the defect
// rather than a case to handle.
func rowFor(t *testing.T, b TrialBalance, id AccountID) TrialBalanceRow {
	t.Helper()
	for _, r := range b.Rows {
		if r.Account == id {
			return r
		}
	}
	t.Fatalf("no trial balance row for %s", id)
	return TrialBalanceRow{}
}

func totalFor(t *testing.T, b TrialBalance, asset AssetCode) TrialBalanceTotal {
	t.Helper()
	for _, tot := range b.Totals {
		if tot.Asset == asset {
			return tot
		}
	}
	t.Fatalf("no trial balance total for %s", asset)
	return TrialBalanceTotal{}
}

// TestATrialBalanceListsEveryAccountOnTheSideItsBalanceFalls is the classic
// two-column form: an account appears in one column, never both, and which one
// is what the report says about it.
func TestATrialBalanceListsEveryAccountOnTheSideItsBalanceFalls(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, bob, cash, feeIncome := setupChartOfAccounts(t, book)

	// Cash in from Alice, and a fee charged to Bob.
	_, err := book.PostTransaction(ctx, PostTransactionRequest{
		Description: "Alice pays in",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 10_000, Direction: Debit},
			{AccountID: alice.ID, Amount: 10_000, Direction: Credit},
		},
	})
	assertNoError(t, err)
	_, err = book.PostTransaction(ctx, PostTransactionRequest{
		Description: "Bob pays in, and is charged",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 5_000, Direction: Debit},
			{AccountID: bob.ID, Amount: 4_700, Direction: Credit},
			{AccountID: feeIncome.ID, Amount: 300, Direction: Credit},
		},
	})
	assertNoError(t, err)

	tb, err := book.TrialBalance(ctx, testClock())
	assertNoError(t, err)

	assertEqual(t, "accounts listed", len(tb.Rows), 4)
	assertEqual(t, "cash is a debit balance", rowFor(t, tb, cash.ID).Debits, 15_000)
	assertEqual(t, "cash prints in one column", rowFor(t, tb, cash.ID).Credits, 0)
	assertEqual(t, "Alice is a credit balance", rowFor(t, tb, alice.ID).Credits, 10_000)
	assertEqual(t, "Alice prints in one column", rowFor(t, tb, alice.ID).Debits, 0)
	assertEqual(t, "fee income is a credit balance", rowFor(t, tb, feeIncome.ID).Credits, 300)

	total := totalFor(t, tb, testAsset)
	assertEqual(t, "total debits", total.Debits, 15_000)
	assertEqual(t, "total credits", total.Credits, 15_000)
	assertEqual(t, "balanced", tb.Balanced(), true)
}

// TestATrialBalanceBalancesPerAssetAndNotAcross. A grand total over a
// multi-asset book is satisfied whenever the integers match, whatever they are
// worth, so the columns are reported per asset and never summed together.
func TestATrialBalanceBalancesPerAssetAndNotAcross(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)

	gl, err := book.CreateLedger(ctx, "General Ledger")
	assertNoError(t, err)
	sub, err := book.CreateSubledger(ctx, gl.ID, "Custody")
	assertNoError(t, err)

	euroCash, err := book.CreateAccount(ctx, sub.ID, "Cash", Asset, testAsset)
	assertNoError(t, err)
	euroOwed, err := book.CreateAccount(ctx, sub.ID, "Owed", Liability, testAsset)
	assertNoError(t, err)
	coin, err := book.CreateAccount(ctx, sub.ID, "Coin", Asset, "BTC")
	assertNoError(t, err)
	coinOwed, err := book.CreateAccount(ctx, sub.ID, "Coin Owed", Liability, "BTC")
	assertNoError(t, err)

	_, err = book.PostTransaction(ctx, PostTransactionRequest{
		Description: "a euro deposit and a bitcoin one, in one event",
		Entries: []Entry{
			{AccountID: euroCash.ID, Amount: 10_000, Direction: Debit},
			{AccountID: euroOwed.ID, Amount: 10_000, Direction: Credit},
			{AccountID: coin.ID, Amount: 7, Direction: Debit},
			{AccountID: coinOwed.ID, Amount: 7, Direction: Credit},
		},
	})
	assertNoError(t, err)

	tb, err := book.TrialBalance(ctx, testClock())
	assertNoError(t, err)

	assertEqual(t, "assets totalled", len(tb.Totals), 2)
	assertEqual(t, "the euro column", totalFor(t, tb, testAsset).Debits, 10_000)
	assertEqual(t, "the bitcoin column", totalFor(t, tb, "BTC").Debits, 7)
	assertEqual(t, "balanced", tb.Balanced(), true)
}

// TestATrialBalanceCountsAControlAccountOnceAndNotOncePerSubsidiary is the
// observable point of moving customer accounts out of the chart of accounts:
// the report is bounded by the institution rather than by its customer base.
func TestATrialBalanceCountsAControlAccountOnceAndNotOncePerSubsidiary(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	deposits, vault := pooledChart(t, book)

	for _, subsidiary := range []string{"dep_1", "dep_2", "dep_3", "dep_4"} {
		takeIn(t, book, deposits, vault, subsidiary, 1000)
	}

	tb, err := book.TrialBalance(ctx, testClock())
	assertNoError(t, err)

	// Four customers, two lines.
	assertEqual(t, "accounts listed", len(tb.Rows), 2)
	pooled := rowFor(t, tb, deposits.ID)
	assertEqual(t, "the control line is marked as one", pooled.Control, true)
	assertEqual(t, "the control line carries the pool", pooled.Credits, 4_000)
	assertEqual(t, "the vault line is not a control line", rowFor(t, tb, vault.ID).Control, false)
	assertEqual(t, "balanced", tb.Balanced(), true)

	// And the pool is the sum of the subsidiaries under it — the same statement the
	// trial balance is built on, read the other way.
	var detail Amount
	for _, subsidiary := range []string{"dep_1", "dep_2", "dep_3", "dep_4"} {
		balance, err := book.BookBalance(ctx, deposits.ID.For(subsidiary))
		assertNoError(t, err)
		detail += balance
	}
	assertEqual(t, "the detail against the control line", detail, pooled.Credits)
}

// TestATrialBalanceReportsWhatIsRecordedAndNotYetEffective. The columns are
// book balances because only the book is guaranteed to balance: the two legs of
// one event may take economic effect on different days, so a value-dated
// restatement of these columns would not add up. InFlight is where that
// divergence is reported instead of hidden.
func TestATrialBalanceReportsWhatIsRecordedAndNotYetEffective(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	today := testClock()
	later := today.AddDate(0, 0, 3)

	// A withdrawal whose cash leg settles in three days: recorded now, not yet
	// effective.
	_, err := book.PostTransaction(ctx, PostTransactionRequest{
		Description: "Alice pays in",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 10_000, Direction: Debit},
			{AccountID: alice.ID, Amount: 10_000, Direction: Credit},
		},
	})
	assertNoError(t, err)
	_, err = book.PostTransaction(ctx, PostTransactionRequest{
		Description: "Alice withdraws, the cash leaves in three days",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 4_000, Direction: Debit},
			{AccountID: cash.ID, Amount: 4_000, Direction: Credit, ValueDate: later},
		},
	})
	assertNoError(t, err)

	tb, err := book.TrialBalance(ctx, today)
	assertNoError(t, err)
	assertEqual(t, "balanced", tb.Balanced(), true)

	// The cash line has a credit recorded against it that has not yet taken
	// effect, so the book stands 4,000 SHORT of the debit side its value dates
	// justify. Alice's own leg is dated today, so her line has nothing in
	// flight — which is the asymmetry that makes this figure worth reporting.
	assertEqual(t, "cash in flight", rowFor(t, tb, cash.ID).InFlight, -4_000)
	assertEqual(t, "Alice in flight", rowFor(t, tb, alice.ID).InFlight, Amount(0))
	// And the asset total is exactly what a value-dated restatement of the two
	// columns would be out by, which here is one leg of one event.
	assertEqual(t, "the asset's in-flight total", totalFor(t, tb, testAsset).InFlight, -4_000)

	// Past the value date there is nothing in flight, and no column moves.
	settled, err := book.TrialBalance(ctx, later)
	assertNoError(t, err)
	assertEqual(t, "cash in flight once settled", rowFor(t, settled, cash.ID).InFlight, Amount(0))
	assertEqual(t, "the asset's in-flight total once settled",
		totalFor(t, settled, testAsset).InFlight, Amount(0))
	assertEqual(t, "the cash column is unchanged", rowFor(t, settled, cash.ID).Debits,
		rowFor(t, tb, cash.ID).Debits)
}

// TestATrialBalanceListsAnAccountThatHasNeverBeenPostedTo. A chart-of-accounts
// line with no entries is a fact about the chart, so it is listed at zero
// rather than dropped — and an empty book is balanced.
func TestATrialBalanceListsAnAccountThatHasNeverBeenPostedTo(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)

	empty, err := book.TrialBalance(ctx, testClock())
	assertNoError(t, err)
	assertEqual(t, "rows in an empty book", len(empty.Rows), 0)
	assertEqual(t, "an empty book is balanced", empty.Balanced(), true)

	alice, _, _, _ := setupChartOfAccounts(t, book)
	tb, err := book.TrialBalance(ctx, testClock())
	assertNoError(t, err)
	assertEqual(t, "accounts listed", len(tb.Rows), 4)
	assertEqual(t, "an unposted account's debits", rowFor(t, tb, alice.ID).Debits, Amount(0))
	assertEqual(t, "an unposted account's credits", rowFor(t, tb, alice.ID).Credits, Amount(0))
	assertEqual(t, "balanced", tb.Balanced(), true)
}

// TestATrialBalanceIsTakenAgainstOneViewOfTheBook is what the single unit of
// work buys: the whole report is read at one snapshot, so it cannot see half of
// a transaction. Driving it through a caller's Tx alongside a posting is the
// case that would expose a per-account View.
func TestATrialBalanceIsTakenAgainstOneViewOfTheBook(t *testing.T) {
	ctx := context.Background()
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	var tb TrialBalance
	err := book.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := book.PostTransactionTx(ctx, tx, PostTransactionRequest{
			Description: "Alice pays in",
			Entries: []Entry{
				{AccountID: cash.ID, Amount: 10_000, Direction: Debit},
				{AccountID: alice.ID, Amount: 10_000, Direction: Credit},
			},
		}); err != nil {
			return err
		}
		var err error
		tb, err = book.TrialBalanceTx(ctx, tx, testClock())
		return err
	})
	assertNoError(t, err)

	assertEqual(t, "the posting is visible inside its own unit of work",
		rowFor(t, tb, cash.ID).Debits, 10_000)
	assertEqual(t, "balanced", tb.Balanced(), true)
}
