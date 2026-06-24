package ledger

import (
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test Helpers
// ---------------------------------------------------------------------------

// testBook creates a new Book with a fixed clock for deterministic tests.
func testBook(t *testing.T) *Book {
	t.Helper()
	book := NewBook()
	book.clock = func() time.Time {
		return time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	}
	return book
}

// setupChartOfAccounts creates a standard chart of accounts for testing:
//
//	General Ledger
//	  ├── Customer Deposits (subledger)
//	  │     ├── Alice Checking (Liability)
//	  │     └── Bob Checking   (Liability)
//	  ├── Bank Assets (subledger)
//	  │     └── Cash           (Asset)
//	  └── Revenue (subledger)
//	        └── Fee Income     (Revenue)
func setupChartOfAccounts(t *testing.T, book *Book) (alice, bob, cash, feeIncome Account) {
	t.Helper()

	gl, err := book.CreateLedger("General Ledger")
	assertNoError(t, err)

	deposits, err := book.CreateSubledger(gl.ID, "Customer Deposits")
	assertNoError(t, err)

	assets, err := book.CreateSubledger(gl.ID, "Bank Assets")
	assertNoError(t, err)

	rev, err := book.CreateSubledger(gl.ID, "Revenue")
	assertNoError(t, err)

	alice, err = book.CreateAccount(deposits.ID, "Alice Checking", Liability)
	assertNoError(t, err)

	bob, err = book.CreateAccount(deposits.ID, "Bob Checking", Liability)
	assertNoError(t, err)

	cash, err = book.CreateAccount(assets.ID, "Cash", Asset)
	assertNoError(t, err)

	feeIncome, err = book.CreateAccount(rev.ID, "Fee Income", Revenue)
	assertNoError(t, err)

	return alice, bob, cash, feeIncome
}

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertError(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("expected error %v, got %v", target, err)
	}
}

func assertEqual[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
}

// ---------------------------------------------------------------------------
// Ledger & Subledger Tests
// ---------------------------------------------------------------------------

func TestCreateLedger(t *testing.T) {
	book := testBook(t)

	l, err := book.CreateLedger("General Ledger")
	assertNoError(t, err)
	assertEqual(t, "name", l.Name, "General Ledger")

	// Verify it can be retrieved.
	got, err := book.GetLedger(l.ID)
	assertNoError(t, err)
	assertEqual(t, "id", got.ID, l.ID)
}

func TestGetLedger_NotFound(t *testing.T) {
	book := testBook(t)

	_, err := book.GetLedger("nonexistent")
	assertError(t, err, ErrLedgerNotFound)
}

func TestCreateSubledger(t *testing.T) {
	book := testBook(t)

	l, err := book.CreateLedger("GL")
	assertNoError(t, err)

	sl, err := book.CreateSubledger(l.ID, "Deposits")
	assertNoError(t, err)
	assertEqual(t, "name", sl.Name, "Deposits")
	assertEqual(t, "ledgerID", sl.LedgerID, l.ID)

	// Verify retrieval.
	got, err := book.GetSubledger(sl.ID)
	assertNoError(t, err)
	assertEqual(t, "id", got.ID, sl.ID)
}

func TestCreateSubledger_LedgerNotFound(t *testing.T) {
	book := testBook(t)

	_, err := book.CreateSubledger("bad_ledger", "Deposits")
	assertError(t, err, ErrLedgerNotFound)
}

func TestGetSubledger_NotFound(t *testing.T) {
	book := testBook(t)

	_, err := book.GetSubledger("nonexistent")
	assertError(t, err, ErrSubledgerNotFound)
}

// ---------------------------------------------------------------------------
// Account Tests
// ---------------------------------------------------------------------------

func TestCreateAccount(t *testing.T) {
	book := testBook(t)

	l, _ := book.CreateLedger("GL")
	sl, _ := book.CreateSubledger(l.ID, "Deposits")

	acct, err := book.CreateAccount(sl.ID, "Alice", Liability)
	assertNoError(t, err)
	assertEqual(t, "name", acct.Name, "Alice")
	assertEqual(t, "type", acct.Type, Liability)
	assertEqual(t, "subledgerID", acct.SubledgerID, sl.ID)

	// Verify retrieval.
	got, err := book.GetAccount(acct.ID)
	assertNoError(t, err)
	assertEqual(t, "id", got.ID, acct.ID)
}

func TestCreateAccount_SubledgerNotFound(t *testing.T) {
	book := testBook(t)

	_, err := book.CreateAccount("bad_sub", "Alice", Liability)
	assertError(t, err, ErrSubledgerNotFound)
}

func TestGetAccount_NotFound(t *testing.T) {
	book := testBook(t)

	_, err := book.GetAccount("nonexistent")
	assertError(t, err, ErrAccountNotFound)
}

// ---------------------------------------------------------------------------
// Account Type Tests
// ---------------------------------------------------------------------------

func TestAccountType_NormalBalance(t *testing.T) {
	tests := []struct {
		acctType AccountType
		normal   Direction
	}{
		{Asset, Debit},
		{Liability, Credit},
		{Equity, Credit},
		{Revenue, Credit},
		{Expense, Debit},
	}

	for _, tt := range tests {
		t.Run(tt.acctType.String(), func(t *testing.T) {
			assertEqual(t, "normal balance", tt.acctType.NormalBalance(), tt.normal)
		})
	}
}

func TestAccountType_String(t *testing.T) {
	assertEqual(t, "Asset", Asset.String(), "Asset")
	assertEqual(t, "Liability", Liability.String(), "Liability")
	assertEqual(t, "Equity", Equity.String(), "Equity")
	assertEqual(t, "Revenue", Revenue.String(), "Revenue")
	assertEqual(t, "Expense", Expense.String(), "Expense")
	assertEqual(t, "Unknown", AccountType(99).String(), "Unknown")
}

func TestDirection_String(t *testing.T) {
	assertEqual(t, "Debit", Debit.String(), "Debit")
	assertEqual(t, "Credit", Credit.String(), "Credit")
}

func TestDirection_Opposite(t *testing.T) {
	assertEqual(t, "opposite of Debit", Debit.Opposite(), Credit)
	assertEqual(t, "opposite of Credit", Credit.Opposite(), Debit)
}

func TestTransactionStatus_String(t *testing.T) {
	assertEqual(t, "Posted", Posted.String(), "Posted")
	assertEqual(t, "Reversed", Reversed.String(), "Reversed")
}

// ---------------------------------------------------------------------------
// Transaction Posting Tests
// ---------------------------------------------------------------------------

// TestPostTransaction_SimpleTransfer tests a basic two-legged transfer
// between two liability (customer deposit) accounts.
//
// Scenario: Customer Alice transfers $50 to customer Bob.
//
//	Debit Alice  $50 (liability decreases — Alice has less)
//	Credit Bob   $50 (liability increases — Bob has more)
func TestPostTransaction_SimpleTransfer(t *testing.T) {
	book := testBook(t)
	alice, bob, _, _ := setupChartOfAccounts(t, book)

	// First, fund Alice's account: bank receives cash from Alice.
	// Credit Alice (liability up) + Debit Cash (asset up).
	_, err := book.PostTransaction(PostTransactionRequest{
		Description: "Initial deposit",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 10000, Direction: Credit},
		},
	})
	// This should fail — unbalanced (only one leg).
	assertError(t, err, ErrUnbalancedTransaction)

	// Properly balanced initial deposit.
	cash := findAccountByName(t, book, "Cash")
	_, err = book.PostTransaction(PostTransactionRequest{
		Description: "Alice deposits $100",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 10000, Direction: Debit},
			{AccountID: alice.ID, Amount: 10000, Direction: Credit},
		},
	})
	assertNoError(t, err)

	// Now transfer $50 from Alice to Bob.
	tx, err := book.PostTransaction(PostTransactionRequest{
		IdempotencyKey: "transfer-001",
		Description:    "Alice sends $50 to Bob",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 5000, Direction: Debit},
			{AccountID: bob.ID, Amount: 5000, Direction: Credit},
		},
	})
	assertNoError(t, err)
	assertEqual(t, "status", tx.Status, Posted)
	assertEqual(t, "entries count", len(tx.Entries), 2)

	// Check balances.
	// Alice: credited 10000, debited 5000 -> net credit of 5000.
	// For Liability (normal=Credit): credit adds, debit subtracts.
	// Balance = +10000 - 5000 = 5000
	aliceBal, err := book.BookBalance(alice.ID)
	assertNoError(t, err)
	assertEqual(t, "alice book balance", aliceBal, Amount(5000))

	// Bob: credited 5000 -> net credit of 5000.
	bobBal, err := book.BookBalance(bob.ID)
	assertNoError(t, err)
	assertEqual(t, "bob book balance", bobBal, Amount(5000))
}

// TestPostTransaction_MultiLeg tests a three-legged transaction that
// includes a fee: transfer + fee split.
//
// Scenario: Alice sends $100 to Bob, with a $2 transfer fee.
//
//	Debit Alice   $102 (she pays principal + fee)
//	Credit Bob    $100 (he receives the principal)
//	Credit Fees   $2   (bank earns the fee)
func TestPostTransaction_MultiLeg(t *testing.T) {
	book := testBook(t)
	alice, bob, cash, feeIncome := setupChartOfAccounts(t, book)

	// Fund Alice with $200.
	_, err := book.PostTransaction(PostTransactionRequest{
		Description: "Alice deposits $200",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 20000, Direction: Debit},
			{AccountID: alice.ID, Amount: 20000, Direction: Credit},
		},
	})
	assertNoError(t, err)

	// Transfer $100 to Bob with $2 fee.
	tx, err := book.PostTransaction(PostTransactionRequest{
		Description: "Transfer with fee",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 10200, Direction: Debit},
			{AccountID: bob.ID, Amount: 10000, Direction: Credit},
			{AccountID: feeIncome.ID, Amount: 200, Direction: Credit},
		},
	})
	assertNoError(t, err)
	assertEqual(t, "entries count", len(tx.Entries), 3)

	// Alice: +20000 (credit) - 10200 (debit) = 9800
	aliceBal, _ := book.BookBalance(alice.ID)
	assertEqual(t, "alice balance", aliceBal, Amount(9800))

	// Bob: +10000 (credit)
	bobBal, _ := book.BookBalance(bob.ID)
	assertEqual(t, "bob balance", bobBal, Amount(10000))

	// Fee Income: +200 (credit). Revenue normal = Credit, so +200.
	feeBal, _ := book.BookBalance(feeIncome.ID)
	assertEqual(t, "fee income balance", feeBal, Amount(200))
}

// TestPostTransaction_BookingAndValueDate tests that booking date and
// value date are correctly stored.
func TestPostTransaction_BookingAndValueDate(t *testing.T) {
	book := testBook(t)
	alice, bob, _, _ := setupChartOfAccounts(t, book)

	bookingDate := time.Date(2025, 1, 15, 14, 30, 0, 0, time.UTC)
	valueDate := time.Date(2025, 1, 16, 0, 0, 0, 0, time.UTC)

	tx, err := book.PostTransaction(PostTransactionRequest{
		Description: "Forward-dated transfer",
		BookingDate: bookingDate,
		ValueDate:   valueDate,
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 1000, Direction: Debit},
			{AccountID: bob.ID, Amount: 1000, Direction: Credit},
		},
	})
	assertNoError(t, err)
	assertEqual(t, "booking date", tx.BookingDate, bookingDate)
	assertEqual(t, "value date", tx.ValueDate, valueDate)
}

// TestPostTransaction_DefaultDates tests that dates default correctly
// when not provided.
func TestPostTransaction_DefaultDates(t *testing.T) {
	book := testBook(t)
	alice, bob, _, _ := setupChartOfAccounts(t, book)

	tx, err := book.PostTransaction(PostTransactionRequest{
		Description: "Transfer with default dates",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 1000, Direction: Debit},
			{AccountID: bob.ID, Amount: 1000, Direction: Credit},
		},
	})
	assertNoError(t, err)

	// Both should default to the book clock time.
	expectedTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	assertEqual(t, "booking date", tx.BookingDate, expectedTime)
	assertEqual(t, "value date", tx.ValueDate, expectedTime)
}

// ---------------------------------------------------------------------------
// Transaction Validation Tests
// ---------------------------------------------------------------------------

func TestPostTransaction_EmptyEntries(t *testing.T) {
	book := testBook(t)

	_, err := book.PostTransaction(PostTransactionRequest{
		Description: "Empty",
		Entries:     []Entry{},
	})
	assertError(t, err, ErrEmptyTransaction)
}

func TestPostTransaction_InvalidAmount(t *testing.T) {
	book := testBook(t)
	alice, bob, _, _ := setupChartOfAccounts(t, book)

	_, err := book.PostTransaction(PostTransactionRequest{
		Description: "Zero amount",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 0, Direction: Debit},
			{AccountID: bob.ID, Amount: 0, Direction: Credit},
		},
	})
	assertError(t, err, ErrInvalidAmount)

	_, err = book.PostTransaction(PostTransactionRequest{
		Description: "Negative amount",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: -100, Direction: Debit},
			{AccountID: bob.ID, Amount: -100, Direction: Credit},
		},
	})
	assertError(t, err, ErrInvalidAmount)
}

func TestPostTransaction_AccountNotFound(t *testing.T) {
	book := testBook(t)
	alice, _, _, _ := setupChartOfAccounts(t, book)

	_, err := book.PostTransaction(PostTransactionRequest{
		Description: "Bad account",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 100, Direction: Debit},
			{AccountID: "nonexistent", Amount: 100, Direction: Credit},
		},
	})
	assertError(t, err, ErrAccountNotFound)
}

func TestPostTransaction_Unbalanced(t *testing.T) {
	book := testBook(t)
	alice, bob, _, _ := setupChartOfAccounts(t, book)

	_, err := book.PostTransaction(PostTransactionRequest{
		Description: "Unbalanced",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 100, Direction: Debit},
			{AccountID: bob.ID, Amount: 200, Direction: Credit},
		},
	})
	assertError(t, err, ErrUnbalancedTransaction)
}

// ---------------------------------------------------------------------------
// Idempotency Tests
// ---------------------------------------------------------------------------

func TestPostTransaction_Idempotency(t *testing.T) {
	book := testBook(t)
	alice, bob, _, _ := setupChartOfAccounts(t, book)

	// First post succeeds.
	tx1, err := book.PostTransaction(PostTransactionRequest{
		IdempotencyKey: "key-1",
		Description:    "Transfer",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 100, Direction: Debit},
			{AccountID: bob.ID, Amount: 100, Direction: Credit},
		},
	})
	assertNoError(t, err)

	// Second post with same key fails.
	_, err = book.PostTransaction(PostTransactionRequest{
		IdempotencyKey: "key-1",
		Description:    "Duplicate",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 100, Direction: Debit},
			{AccountID: bob.ID, Amount: 100, Direction: Credit},
		},
	})
	assertError(t, err, ErrDuplicateIdempotencyKey)

	// Can retrieve original by idempotency key.
	got, err := book.GetTransactionByIdempotencyKey("key-1")
	assertNoError(t, err)
	assertEqual(t, "tx id", got.ID, tx1.ID)

	// Non-existent idempotency key.
	_, err = book.GetTransactionByIdempotencyKey("no-such-key")
	assertError(t, err, ErrTransactionNotFound)
}

// ---------------------------------------------------------------------------
// Transaction Retrieval Tests
// ---------------------------------------------------------------------------

func TestGetTransaction(t *testing.T) {
	book := testBook(t)
	alice, bob, _, _ := setupChartOfAccounts(t, book)

	tx, _ := book.PostTransaction(PostTransactionRequest{
		Description: "Test",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 100, Direction: Debit},
			{AccountID: bob.ID, Amount: 100, Direction: Credit},
		},
	})

	got, err := book.GetTransaction(tx.ID)
	assertNoError(t, err)
	assertEqual(t, "id", got.ID, tx.ID)
}

func TestGetTransaction_NotFound(t *testing.T) {
	book := testBook(t)

	_, err := book.GetTransaction("nonexistent")
	assertError(t, err, ErrTransactionNotFound)
}

// ---------------------------------------------------------------------------
// Transaction Reversal Tests
// ---------------------------------------------------------------------------

// TestReverseTransaction tests that reversing a transaction exactly
// offsets its balance impact.
func TestReverseTransaction(t *testing.T) {
	book := testBook(t)
	alice, bob, cash, _ := setupChartOfAccounts(t, book)

	// Fund Alice.
	book.PostTransaction(PostTransactionRequest{
		Description: "Deposit",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 10000, Direction: Debit},
			{AccountID: alice.ID, Amount: 10000, Direction: Credit},
		},
	})

	// Transfer $50 from Alice to Bob.
	tx, _ := book.PostTransaction(PostTransactionRequest{
		Description: "Transfer",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 5000, Direction: Debit},
			{AccountID: bob.ID, Amount: 5000, Direction: Credit},
		},
	})

	// Reverse the transfer.
	reversal, err := book.ReverseTransaction(tx.ID, "Reversal of erroneous transfer")
	assertNoError(t, err)
	assertEqual(t, "reversal status", reversal.Status, Posted)
	assertEqual(t, "reversalOf", reversal.ReversalOf, tx.ID)
	assertEqual(t, "entries count", len(reversal.Entries), 2)

	// Original should be marked as Reversed.
	original, _ := book.GetTransaction(tx.ID)
	assertEqual(t, "original status", original.Status, Reversed)

	// Alice's balance should be back to 10000.
	aliceBal, _ := book.BookBalance(alice.ID)
	assertEqual(t, "alice balance after reversal", aliceBal, Amount(10000))

	// Bob's balance should be back to 0.
	bobBal, _ := book.BookBalance(bob.ID)
	assertEqual(t, "bob balance after reversal", bobBal, Amount(0))
}

func TestReverseTransaction_NotFound(t *testing.T) {
	book := testBook(t)

	_, err := book.ReverseTransaction("nonexistent", "")
	assertError(t, err, ErrTransactionNotFound)
}

func TestReverseTransaction_AlreadyReversed(t *testing.T) {
	book := testBook(t)
	alice, bob, _, _ := setupChartOfAccounts(t, book)

	tx, _ := book.PostTransaction(PostTransactionRequest{
		Description: "Transfer",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 100, Direction: Debit},
			{AccountID: bob.ID, Amount: 100, Direction: Credit},
		},
	})

	// First reversal succeeds.
	_, err := book.ReverseTransaction(tx.ID, "First reversal")
	assertNoError(t, err)

	// Second reversal fails.
	_, err = book.ReverseTransaction(tx.ID, "Second reversal")
	assertError(t, err, ErrTransactionAlreadyReversed)
}

// ---------------------------------------------------------------------------
// Audit Trail Tests
// ---------------------------------------------------------------------------

func TestAuditLog(t *testing.T) {
	book := testBook(t)
	alice, bob, _, _ := setupChartOfAccounts(t, book)

	// Post a transaction.
	book.PostTransaction(PostTransactionRequest{
		Description: "Transfer",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 100, Direction: Debit},
			{AccountID: bob.ID, Amount: 100, Direction: Credit},
		},
	})

	log := book.GetAuditLog()

	// Should have: ledger created, 3x subledger created, 4x account created,
	// 1x transaction posted = 9 events.
	if len(log) < 9 {
		t.Fatalf("expected at least 9 audit events, got %d", len(log))
	}

	// Verify first event is ledger creation.
	assertEqual(t, "first event type", log[0].Type, EventLedgerCreated)

	// Verify last event is the transaction.
	last := log[len(log)-1]
	assertEqual(t, "last event type", last.Type, EventTransactionPosted)
}

func TestAuditLogForEntity(t *testing.T) {
	book := testBook(t)
	alice, bob, _, _ := setupChartOfAccounts(t, book)

	tx, _ := book.PostTransaction(PostTransactionRequest{
		Description: "Transfer",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 100, Direction: Debit},
			{AccountID: bob.ID, Amount: 100, Direction: Credit},
		},
	})

	// Get events for Alice's account.
	aliceEvents := book.GetAuditLogForEntity(string(alice.ID))
	assertEqual(t, "alice events", len(aliceEvents), 1)
	assertEqual(t, "event type", aliceEvents[0].Type, EventAccountCreated)

	// Get events for the transaction.
	txEvents := book.GetAuditLogForEntity(string(tx.ID))
	assertEqual(t, "tx events", len(txEvents), 1)
	assertEqual(t, "event type", txEvents[0].Type, EventTransactionPosted)
}

func TestAuditLog_ImmutableCopy(t *testing.T) {
	book := testBook(t)
	book.CreateLedger("GL")

	log1 := book.GetAuditLog()
	log2 := book.GetAuditLog()

	// Modifying the returned slice should not affect the internal log.
	log1[0].Type = "tampered"
	if log2[0].Type == "tampered" {
		t.Fatal("audit log returned mutable reference")
	}
}

// ---------------------------------------------------------------------------
// Insufficient Balance Tests
// ---------------------------------------------------------------------------

// TestPostTransaction_InsufficientBalance_Asset tests that a transaction
// is rejected when it would cause an Asset account's book balance
// to go below zero.
func TestPostTransaction_InsufficientBalance_Asset(t *testing.T) {
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	// Fund cash account with $100.
	book.PostTransaction(PostTransactionRequest{
		Description: "Initial deposit",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 10000, Direction: Debit},
			{AccountID: alice.ID, Amount: 10000, Direction: Credit},
		},
	})

	// Try to withdraw more cash than available (credit cash $150).
	_, err := book.PostTransaction(PostTransactionRequest{
		Description: "Overdraw cash",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 15000, Direction: Debit},
			{AccountID: cash.ID, Amount: 15000, Direction: Credit},
		},
	})
	assertError(t, err, ErrInsufficientBalance)

	// Cash balance should be unchanged.
	bal, _ := book.BookBalance(cash.ID)
	assertEqual(t, "cash balance unchanged", bal, Amount(10000))
}

// TestPostTransaction_InsufficientBalance_LiabilityNotChecked tests that
// Liability accounts are not subject to balance checking.
func TestPostTransaction_InsufficientBalance_LiabilityNotChecked(t *testing.T) {
	book := testBook(t)
	alice, _, cash, _ := setupChartOfAccounts(t, book)

	// Debit Alice (Liability) without any prior credit — this should succeed
	// because Liability accounts are not checked for insufficient balance.
	_, err := book.PostTransaction(PostTransactionRequest{
		Description: "Debit unfunded liability",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 5000, Direction: Debit},
			{AccountID: cash.ID, Amount: 5000, Direction: Debit},
		},
	})
	// This will fail with ErrUnbalancedTransaction since both are debits,
	// but let's do a proper balanced test.
	assertError(t, err, ErrUnbalancedTransaction)

	// Proper test: debit a Liability with no prior balance.
	l, _ := book.CreateLedger("Test")
	sl, _ := book.CreateSubledger(l.ID, "Test")
	liab, _ := book.CreateAccount(sl.ID, "Test Liability", Liability)

	_, err = book.PostTransaction(PostTransactionRequest{
		Description: "Debit unfunded liability",
		Entries: []Entry{
			{AccountID: liab.ID, Amount: 5000, Direction: Debit},
			{AccountID: alice.ID, Amount: 5000, Direction: Credit},
		},
	})
	assertNoError(t, err)
}

// ---------------------------------------------------------------------------
// Balance Edge Cases
// ---------------------------------------------------------------------------

func TestBookBalance_AccountNotFound(t *testing.T) {
	book := testBook(t)

	_, err := book.BookBalance("nonexistent")
	assertError(t, err, ErrAccountNotFound)
}

// TestBookBalance_ZeroForNewAccount tests that a new account has zero
// balance.
func TestBookBalance_ZeroForNewAccount(t *testing.T) {
	book := testBook(t)
	alice, _, _, _ := setupChartOfAccounts(t, book)

	bal, err := book.BookBalance(alice.ID)
	assertNoError(t, err)
	assertEqual(t, "book", bal, Amount(0))
}

// TestBookBalance_AllAccountTypes tests that balance calculations work
// correctly for every account type.
func TestBookBalance_AllAccountTypes(t *testing.T) {
	book := testBook(t)

	l, _ := book.CreateLedger("GL")
	sl, _ := book.CreateSubledger(l.ID, "Test")

	// Create one account of each type.
	asset, _ := book.CreateAccount(sl.ID, "Asset", Asset)
	liability, _ := book.CreateAccount(sl.ID, "Liability", Liability)
	equity, _ := book.CreateAccount(sl.ID, "Equity", Equity)
	revenue, _ := book.CreateAccount(sl.ID, "Revenue", Revenue)
	expense, _ := book.CreateAccount(sl.ID, "Expense", Expense)

	accounts := []Account{asset, liability, equity, revenue, expense}

	// Post a debit of 100 and credit of 100 between pairs.
	// Debit asset, credit liability.
	book.PostTransaction(PostTransactionRequest{
		Description: "D asset, C liability",
		Entries: []Entry{
			{AccountID: asset.ID, Amount: 1000, Direction: Debit},
			{AccountID: liability.ID, Amount: 1000, Direction: Credit},
		},
	})

	// Debit expense, credit revenue.
	book.PostTransaction(PostTransactionRequest{
		Description: "D expense, C revenue",
		Entries: []Entry{
			{AccountID: expense.ID, Amount: 500, Direction: Debit},
			{AccountID: revenue.ID, Amount: 500, Direction: Credit},
		},
	})

	// Credit equity, debit asset.
	book.PostTransaction(PostTransactionRequest{
		Description: "D asset, C equity",
		Entries: []Entry{
			{AccountID: asset.ID, Amount: 2000, Direction: Debit},
			{AccountID: equity.ID, Amount: 2000, Direction: Credit},
		},
	})

	// Expected balances (in normal direction):
	// Asset (normal=Debit): +1000 + 2000 = 3000
	// Liability (normal=Credit): +1000
	// Equity (normal=Credit): +2000
	// Revenue (normal=Credit): +500
	// Expense (normal=Debit): +500
	expected := []Amount{3000, 1000, 2000, 500, 500}

	for i, acct := range accounts {
		bal, _ := book.BookBalance(acct.ID)
		assertEqual(t, acct.Name+" balance", bal, expected[i])
	}
}

// ---------------------------------------------------------------------------
// Integration Test: Full Ledger Workflow
// ---------------------------------------------------------------------------

// TestFullLedgerWorkflow simulates a realistic banking day at the general
// ledger level:
//
//  1. Set up chart of accounts.
//  2. Customer deposits cash.
//  3. Customer makes a card payment.
//  4. Customer receives a wire transfer.
//  5. An erroneous fee is posted and then reversed.
//  6. Audit trail is verified.
func TestFullLedgerWorkflow(t *testing.T) {
	book := testBook(t)

	// Step 1: Chart of accounts.
	gl, _ := book.CreateLedger("General Ledger")
	customerDeposits, _ := book.CreateSubledger(gl.ID, "Customer Deposits")
	bankAssets, _ := book.CreateSubledger(gl.ID, "Bank Assets")
	rev, _ := book.CreateSubledger(gl.ID, "Revenue")

	alice, _ := book.CreateAccount(customerDeposits.ID, "Alice Checking", Liability)
	nostro, _ := book.CreateAccount(bankAssets.ID, "Nostro USD", Asset)
	cashAccount, _ := book.CreateAccount(bankAssets.ID, "Cash Vault", Asset)
	feeIncome, _ := book.CreateAccount(rev.ID, "Fee Income", Revenue)

	// Step 2: Alice deposits $500 cash at the teller.
	book.PostTransaction(PostTransactionRequest{
		IdempotencyKey: "deposit-001",
		Description:    "Cash deposit at branch",
		Entries: []Entry{
			{AccountID: cashAccount.ID, Amount: 50000, Direction: Debit},
			{AccountID: alice.ID, Amount: 50000, Direction: Credit},
		},
	})

	aliceBal, _ := book.BookBalance(alice.ID)
	assertEqual(t, "after deposit", aliceBal, Amount(50000))

	// Step 3: Alice pays a restaurant $75 in cash.
	book.PostTransaction(PostTransactionRequest{
		Description: "Restaurant bill",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 7500, Direction: Debit},
			{AccountID: cashAccount.ID, Amount: 7500, Direction: Credit},
		},
	})

	aliceBal, _ = book.BookBalance(alice.ID)
	assertEqual(t, "book after payment", aliceBal, Amount(42500))

	// Step 4: Alice receives a $200 wire transfer.
	book.PostTransaction(PostTransactionRequest{
		IdempotencyKey: "wire-001",
		Description:    "Incoming wire transfer",
		BookingDate:    time.Date(2025, 1, 15, 14, 30, 0, 0, time.UTC),
		ValueDate:      time.Date(2025, 1, 16, 0, 0, 0, 0, time.UTC),
		Entries: []Entry{
			{AccountID: nostro.ID, Amount: 20000, Direction: Debit},
			{AccountID: alice.ID, Amount: 20000, Direction: Credit},
		},
	})

	aliceBal, _ = book.BookBalance(alice.ID)
	assertEqual(t, "after wire", aliceBal, Amount(62500))

	// Step 5: Erroneous $10 fee, then reversal.
	errTx, _ := book.PostTransaction(PostTransactionRequest{
		IdempotencyKey: "fee-001",
		Description:    "Monthly maintenance fee (error)",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 1000, Direction: Debit},
			{AccountID: feeIncome.ID, Amount: 1000, Direction: Credit},
		},
	})

	aliceBal, _ = book.BookBalance(alice.ID)
	assertEqual(t, "after fee", aliceBal, Amount(61500))

	book.ReverseTransaction(errTx.ID, "Reverse erroneous fee")

	aliceBal, _ = book.BookBalance(alice.ID)
	assertEqual(t, "after fee reversal", aliceBal, Amount(62500))

	// Step 6: Audit trail should contain all operations.
	auditLog := book.GetAuditLog()
	if len(auditLog) == 0 {
		t.Fatal("audit log should not be empty")
	}

	// Count event types.
	counts := make(map[AuditEventType]int)
	for _, e := range auditLog {
		counts[e.Type]++
	}

	if counts[EventTransactionPosted] < 3 {
		t.Fatalf("expected at least 3 transaction.posted events, got %d", counts[EventTransactionPosted])
	}
	if counts[EventTransactionReversed] != 1 {
		t.Fatalf("expected 1 transaction.reversed event, got %d", counts[EventTransactionReversed])
	}
}

// ---------------------------------------------------------------------------
// Helper functions for tests
// ---------------------------------------------------------------------------

func findAccountByName(t *testing.T, book *Book, name string) Account {
	t.Helper()
	for _, acct := range book.accounts {
		if acct.Name == name {
			return *acct
		}
	}
	t.Fatalf("account %q not found", name)
	return Account{}
}
