package ledger

import (
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test Helpers
// ---------------------------------------------------------------------------

// testService creates a new Service with a fixed clock for deterministic tests.
func testService(t *testing.T) *Service {
	t.Helper()
	svc := NewService()
	svc.clock = func() time.Time {
		return time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	}
	return svc
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
func setupChartOfAccounts(t *testing.T, svc *Service) (alice, bob, cash, feeIncome Account) {
	t.Helper()

	gl, err := svc.CreateLedger("General Ledger")
	assertNoError(t, err)

	deposits, err := svc.CreateSubledger(gl.ID, "Customer Deposits")
	assertNoError(t, err)

	assets, err := svc.CreateSubledger(gl.ID, "Bank Assets")
	assertNoError(t, err)

	rev, err := svc.CreateSubledger(gl.ID, "Revenue")
	assertNoError(t, err)

	alice, err = svc.CreateAccount(deposits.ID, "Alice Checking", Liability)
	assertNoError(t, err)

	bob, err = svc.CreateAccount(deposits.ID, "Bob Checking", Liability)
	assertNoError(t, err)

	cash, err = svc.CreateAccount(assets.ID, "Cash", Asset)
	assertNoError(t, err)

	feeIncome, err = svc.CreateAccount(rev.ID, "Fee Income", Revenue)
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
	svc := testService(t)

	l, err := svc.CreateLedger("General Ledger")
	assertNoError(t, err)
	assertEqual(t, "name", l.Name, "General Ledger")

	// Verify it can be retrieved.
	got, err := svc.GetLedger(l.ID)
	assertNoError(t, err)
	assertEqual(t, "id", got.ID, l.ID)
}

func TestGetLedger_NotFound(t *testing.T) {
	svc := testService(t)

	_, err := svc.GetLedger("nonexistent")
	assertError(t, err, ErrLedgerNotFound)
}

func TestCreateSubledger(t *testing.T) {
	svc := testService(t)

	l, err := svc.CreateLedger("GL")
	assertNoError(t, err)

	sl, err := svc.CreateSubledger(l.ID, "Deposits")
	assertNoError(t, err)
	assertEqual(t, "name", sl.Name, "Deposits")
	assertEqual(t, "ledgerID", sl.LedgerID, l.ID)

	// Verify retrieval.
	got, err := svc.GetSubledger(sl.ID)
	assertNoError(t, err)
	assertEqual(t, "id", got.ID, sl.ID)
}

func TestCreateSubledger_LedgerNotFound(t *testing.T) {
	svc := testService(t)

	_, err := svc.CreateSubledger("bad_ledger", "Deposits")
	assertError(t, err, ErrLedgerNotFound)
}

func TestGetSubledger_NotFound(t *testing.T) {
	svc := testService(t)

	_, err := svc.GetSubledger("nonexistent")
	assertError(t, err, ErrSubledgerNotFound)
}

// ---------------------------------------------------------------------------
// Account Tests
// ---------------------------------------------------------------------------

func TestCreateAccount(t *testing.T) {
	svc := testService(t)

	l, _ := svc.CreateLedger("GL")
	sl, _ := svc.CreateSubledger(l.ID, "Deposits")

	acct, err := svc.CreateAccount(sl.ID, "Alice", Liability)
	assertNoError(t, err)
	assertEqual(t, "name", acct.Name, "Alice")
	assertEqual(t, "type", acct.Type, Liability)
	assertEqual(t, "subledgerID", acct.SubledgerID, sl.ID)

	// Verify retrieval.
	got, err := svc.GetAccount(acct.ID)
	assertNoError(t, err)
	assertEqual(t, "id", got.ID, acct.ID)
}

func TestCreateAccount_SubledgerNotFound(t *testing.T) {
	svc := testService(t)

	_, err := svc.CreateAccount("bad_sub", "Alice", Liability)
	assertError(t, err, ErrSubledgerNotFound)
}

func TestGetAccount_NotFound(t *testing.T) {
	svc := testService(t)

	_, err := svc.GetAccount("nonexistent")
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

func TestHoldStatus_String(t *testing.T) {
	assertEqual(t, "Active", HoldActive.String(), "Active")
	assertEqual(t, "Released", HoldReleased.String(), "Released")
	assertEqual(t, "Captured", HoldCaptured.String(), "Captured")
	assertEqual(t, "Unknown", HoldStatus(99).String(), "Unknown")
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
	svc := testService(t)
	alice, bob, _, _ := setupChartOfAccounts(t, svc)

	// First, fund Alice's account: bank receives cash from Alice.
	// Credit Alice (liability up) + Debit Cash (asset up).
	_, err := svc.PostTransaction(PostTransactionRequest{
		Description: "Initial deposit",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 10000, Direction: Credit},
		},
	})
	// This should fail — unbalanced (only one leg).
	assertError(t, err, ErrUnbalancedTransaction)

	// Properly balanced initial deposit.
	cash := findAccountByName(t, svc, "Cash")
	_, err = svc.PostTransaction(PostTransactionRequest{
		Description: "Alice deposits $100",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 10000, Direction: Debit},
			{AccountID: alice.ID, Amount: 10000, Direction: Credit},
		},
	})
	assertNoError(t, err)

	// Now transfer $50 from Alice to Bob.
	tx, err := svc.PostTransaction(PostTransactionRequest{
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
	aliceBal, err := svc.GetBalance(alice.ID)
	assertNoError(t, err)
	assertEqual(t, "alice book balance", aliceBal.Book, Amount(5000))

	// Bob: credited 5000 -> net credit of 5000.
	bobBal, err := svc.GetBalance(bob.ID)
	assertNoError(t, err)
	assertEqual(t, "bob book balance", bobBal.Book, Amount(5000))
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
	svc := testService(t)
	alice, bob, cash, feeIncome := setupChartOfAccounts(t, svc)

	// Fund Alice with $200.
	_, err := svc.PostTransaction(PostTransactionRequest{
		Description: "Alice deposits $200",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 20000, Direction: Debit},
			{AccountID: alice.ID, Amount: 20000, Direction: Credit},
		},
	})
	assertNoError(t, err)

	// Transfer $100 to Bob with $2 fee.
	tx, err := svc.PostTransaction(PostTransactionRequest{
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
	aliceBal, _ := svc.GetBalance(alice.ID)
	assertEqual(t, "alice balance", aliceBal.Book, Amount(9800))

	// Bob: +10000 (credit)
	bobBal, _ := svc.GetBalance(bob.ID)
	assertEqual(t, "bob balance", bobBal.Book, Amount(10000))

	// Fee Income: +200 (credit). Revenue normal = Credit, so +200.
	feeBal, _ := svc.GetBalance(feeIncome.ID)
	assertEqual(t, "fee income balance", feeBal.Book, Amount(200))
}

// TestPostTransaction_BookingAndValueDate tests that booking date and
// value date are correctly stored.
func TestPostTransaction_BookingAndValueDate(t *testing.T) {
	svc := testService(t)
	alice, bob, _, _ := setupChartOfAccounts(t, svc)

	bookingDate := time.Date(2025, 1, 15, 14, 30, 0, 0, time.UTC)
	valueDate := time.Date(2025, 1, 16, 0, 0, 0, 0, time.UTC)

	tx, err := svc.PostTransaction(PostTransactionRequest{
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
	svc := testService(t)
	alice, bob, _, _ := setupChartOfAccounts(t, svc)

	tx, err := svc.PostTransaction(PostTransactionRequest{
		Description: "Transfer with default dates",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 1000, Direction: Debit},
			{AccountID: bob.ID, Amount: 1000, Direction: Credit},
		},
	})
	assertNoError(t, err)

	// Both should default to the service clock time.
	expectedTime := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	assertEqual(t, "booking date", tx.BookingDate, expectedTime)
	assertEqual(t, "value date", tx.ValueDate, expectedTime)
}

// ---------------------------------------------------------------------------
// Transaction Validation Tests
// ---------------------------------------------------------------------------

func TestPostTransaction_EmptyEntries(t *testing.T) {
	svc := testService(t)

	_, err := svc.PostTransaction(PostTransactionRequest{
		Description: "Empty",
		Entries:     []Entry{},
	})
	assertError(t, err, ErrEmptyTransaction)
}

func TestPostTransaction_InvalidAmount(t *testing.T) {
	svc := testService(t)
	alice, bob, _, _ := setupChartOfAccounts(t, svc)

	_, err := svc.PostTransaction(PostTransactionRequest{
		Description: "Zero amount",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 0, Direction: Debit},
			{AccountID: bob.ID, Amount: 0, Direction: Credit},
		},
	})
	assertError(t, err, ErrInvalidAmount)

	_, err = svc.PostTransaction(PostTransactionRequest{
		Description: "Negative amount",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: -100, Direction: Debit},
			{AccountID: bob.ID, Amount: -100, Direction: Credit},
		},
	})
	assertError(t, err, ErrInvalidAmount)
}

func TestPostTransaction_AccountNotFound(t *testing.T) {
	svc := testService(t)
	alice, _, _, _ := setupChartOfAccounts(t, svc)

	_, err := svc.PostTransaction(PostTransactionRequest{
		Description: "Bad account",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 100, Direction: Debit},
			{AccountID: "nonexistent", Amount: 100, Direction: Credit},
		},
	})
	assertError(t, err, ErrAccountNotFound)
}

func TestPostTransaction_Unbalanced(t *testing.T) {
	svc := testService(t)
	alice, bob, _, _ := setupChartOfAccounts(t, svc)

	_, err := svc.PostTransaction(PostTransactionRequest{
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
	svc := testService(t)
	alice, bob, _, _ := setupChartOfAccounts(t, svc)

	// First post succeeds.
	tx1, err := svc.PostTransaction(PostTransactionRequest{
		IdempotencyKey: "key-1",
		Description:    "Transfer",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 100, Direction: Debit},
			{AccountID: bob.ID, Amount: 100, Direction: Credit},
		},
	})
	assertNoError(t, err)

	// Second post with same key fails.
	_, err = svc.PostTransaction(PostTransactionRequest{
		IdempotencyKey: "key-1",
		Description:    "Duplicate",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 100, Direction: Debit},
			{AccountID: bob.ID, Amount: 100, Direction: Credit},
		},
	})
	assertError(t, err, ErrDuplicateIdempotencyKey)

	// Can retrieve original by idempotency key.
	got, err := svc.GetTransactionByIdempotencyKey("key-1")
	assertNoError(t, err)
	assertEqual(t, "tx id", got.ID, tx1.ID)

	// Non-existent idempotency key.
	_, err = svc.GetTransactionByIdempotencyKey("no-such-key")
	assertError(t, err, ErrTransactionNotFound)
}

// ---------------------------------------------------------------------------
// Transaction Retrieval Tests
// ---------------------------------------------------------------------------

func TestGetTransaction(t *testing.T) {
	svc := testService(t)
	alice, bob, _, _ := setupChartOfAccounts(t, svc)

	tx, _ := svc.PostTransaction(PostTransactionRequest{
		Description: "Test",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 100, Direction: Debit},
			{AccountID: bob.ID, Amount: 100, Direction: Credit},
		},
	})

	got, err := svc.GetTransaction(tx.ID)
	assertNoError(t, err)
	assertEqual(t, "id", got.ID, tx.ID)
}

func TestGetTransaction_NotFound(t *testing.T) {
	svc := testService(t)

	_, err := svc.GetTransaction("nonexistent")
	assertError(t, err, ErrTransactionNotFound)
}

// ---------------------------------------------------------------------------
// Transaction Reversal Tests
// ---------------------------------------------------------------------------

// TestReverseTransaction tests that reversing a transaction exactly
// offsets its balance impact.
func TestReverseTransaction(t *testing.T) {
	svc := testService(t)
	alice, bob, cash, _ := setupChartOfAccounts(t, svc)

	// Fund Alice.
	svc.PostTransaction(PostTransactionRequest{
		Description: "Deposit",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 10000, Direction: Debit},
			{AccountID: alice.ID, Amount: 10000, Direction: Credit},
		},
	})

	// Transfer $50 from Alice to Bob.
	tx, _ := svc.PostTransaction(PostTransactionRequest{
		Description: "Transfer",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 5000, Direction: Debit},
			{AccountID: bob.ID, Amount: 5000, Direction: Credit},
		},
	})

	// Reverse the transfer.
	reversal, err := svc.ReverseTransaction(tx.ID, "Reversal of erroneous transfer")
	assertNoError(t, err)
	assertEqual(t, "reversal status", reversal.Status, Posted)
	assertEqual(t, "reversalOf", reversal.ReversalOf, tx.ID)
	assertEqual(t, "entries count", len(reversal.Entries), 2)

	// Original should be marked as Reversed.
	original, _ := svc.GetTransaction(tx.ID)
	assertEqual(t, "original status", original.Status, Reversed)

	// Alice's balance should be back to 10000.
	aliceBal, _ := svc.GetBalance(alice.ID)
	assertEqual(t, "alice balance after reversal", aliceBal.Book, Amount(10000))

	// Bob's balance should be back to 0.
	bobBal, _ := svc.GetBalance(bob.ID)
	assertEqual(t, "bob balance after reversal", bobBal.Book, Amount(0))
}

func TestReverseTransaction_NotFound(t *testing.T) {
	svc := testService(t)

	_, err := svc.ReverseTransaction("nonexistent", "")
	assertError(t, err, ErrTransactionNotFound)
}

func TestReverseTransaction_AlreadyReversed(t *testing.T) {
	svc := testService(t)
	alice, bob, _, _ := setupChartOfAccounts(t, svc)

	tx, _ := svc.PostTransaction(PostTransactionRequest{
		Description: "Transfer",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 100, Direction: Debit},
			{AccountID: bob.ID, Amount: 100, Direction: Credit},
		},
	})

	// First reversal succeeds.
	_, err := svc.ReverseTransaction(tx.ID, "First reversal")
	assertNoError(t, err)

	// Second reversal fails.
	_, err = svc.ReverseTransaction(tx.ID, "Second reversal")
	assertError(t, err, ErrTransactionAlreadyReversed)
}

// ---------------------------------------------------------------------------
// Hold Tests
// ---------------------------------------------------------------------------

// TestHold_FullLifecycle tests the authorization-capture flow.
//
// Scenario: Alice has $100. A $30 hold is placed (card auth at gas pump).
// Available drops to $70, book stays at $100. Then the hold is captured
// for $25 (actual gas pumped).
func TestHold_FullLifecycle(t *testing.T) {
	svc := testService(t)
	alice, _, cash, _ := setupChartOfAccounts(t, svc)

	// Fund Alice.
	svc.PostTransaction(PostTransactionRequest{
		Description: "Deposit",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 10000, Direction: Debit},
			{AccountID: alice.ID, Amount: 10000, Direction: Credit},
		},
	})

	// Place hold.
	hold, err := svc.CreateHold(CreateHoldRequest{
		AccountID:   alice.ID,
		Amount:      3000,
				Description: "Gas pump authorization",
	})
	assertNoError(t, err)
	assertEqual(t, "hold status", hold.Status, HoldActive)

	// Check balance: book=10000, holds=3000, available=7000.
	bal, _ := svc.GetBalance(alice.ID)
	assertEqual(t, "book with hold", bal.Book, Amount(10000))
	assertEqual(t, "holds amount", bal.Holds, Amount(3000))
	assertEqual(t, "available with hold", bal.Available, Amount(7000))

	// Capture for actual amount ($25).
	tx, err := svc.CaptureHold(hold.ID, cash.ID, 2500, "Gas purchase")
	assertNoError(t, err)
	assertEqual(t, "tx status", tx.Status, Posted)

	// Hold should be captured.
	h, _ := svc.GetHold(hold.ID)
	assertEqual(t, "hold captured", h.Status, HoldCaptured)

	// Balance: book=10000-2500=7500, holds=0, available=7500.
	bal, _ = svc.GetBalance(alice.ID)
	assertEqual(t, "book after capture", bal.Book, Amount(7500))
	assertEqual(t, "holds after capture", bal.Holds, Amount(0))
	assertEqual(t, "available after capture", bal.Available, Amount(7500))
}

// TestHold_Release tests that releasing a hold restores available balance.
func TestHold_Release(t *testing.T) {
	svc := testService(t)
	alice, _, cash, _ := setupChartOfAccounts(t, svc)

	// Fund Alice.
	svc.PostTransaction(PostTransactionRequest{
		Description: "Deposit",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 10000, Direction: Debit},
			{AccountID: alice.ID, Amount: 10000, Direction: Credit},
		},
	})

	// Place hold.
	hold, _ := svc.CreateHold(CreateHoldRequest{
		AccountID: alice.ID,
		Amount:    3000,
			})

	// Release hold.
	err := svc.ReleaseHold(hold.ID)
	assertNoError(t, err)

	// Available should be fully restored.
	bal, _ := svc.GetBalance(alice.ID)
	assertEqual(t, "book", bal.Book, Amount(10000))
	assertEqual(t, "holds", bal.Holds, Amount(0))
	assertEqual(t, "available", bal.Available, Amount(10000))
}

// TestHold_Expiration tests that expired holds are excluded from
// available balance calculation.
func TestHold_Expiration(t *testing.T) {
	svc := testService(t)
	alice, _, cash, _ := setupChartOfAccounts(t, svc)

	// Fund Alice.
	svc.PostTransaction(PostTransactionRequest{
		Description: "Deposit",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 10000, Direction: Debit},
			{AccountID: alice.ID, Amount: 10000, Direction: Credit},
		},
	})

	// Place hold that expires in the past.
	svc.CreateHold(CreateHoldRequest{
		AccountID: alice.ID,
		Amount:    3000,
				ExpiresAt: time.Date(2025, 1, 14, 0, 0, 0, 0, time.UTC), // yesterday
	})

	// Expired hold should not affect available balance.
	bal, _ := svc.GetBalance(alice.ID)
	assertEqual(t, "holds", bal.Holds, Amount(0))
	assertEqual(t, "available", bal.Available, Amount(10000))
}

func TestCreateHold_Validation(t *testing.T) {
	svc := testService(t)

	// Account not found.
	_, err := svc.CreateHold(CreateHoldRequest{
		AccountID: "nonexistent",
		Amount:    100,
			})
	assertError(t, err, ErrAccountNotFound)

	alice, _, _, _ := setupChartOfAccounts(t, svc)

	// Invalid amount.
	_, err = svc.CreateHold(CreateHoldRequest{
		AccountID: alice.ID,
		Amount:    0,
			})
	assertError(t, err, ErrInvalidAmount)
}

func TestReleaseHold_NotFound(t *testing.T) {
	svc := testService(t)

	err := svc.ReleaseHold("nonexistent")
	assertError(t, err, ErrHoldNotFound)
}

func TestReleaseHold_NotActive(t *testing.T) {
	svc := testService(t)
	alice, _, _, _ := setupChartOfAccounts(t, svc)

	hold, _ := svc.CreateHold(CreateHoldRequest{
		AccountID: alice.ID,
		Amount:    100,
			})

	// Release it.
	svc.ReleaseHold(hold.ID)

	// Try to release again.
	err := svc.ReleaseHold(hold.ID)
	assertError(t, err, ErrHoldNotActive)
}

func TestCaptureHold_Validation(t *testing.T) {
	svc := testService(t)
	alice, _, _, _ := setupChartOfAccounts(t, svc)

	// Hold not found.
	_, err := svc.CaptureHold("nonexistent", alice.ID, 100, "")
	assertError(t, err, ErrHoldNotFound)

	hold, _ := svc.CreateHold(CreateHoldRequest{
		AccountID: alice.ID,
		Amount:    100,
			})

	// Counterparty not found.
	_, err = svc.CaptureHold(hold.ID, "nonexistent", 100, "")
	assertError(t, err, ErrAccountNotFound)

	// Release hold, then try to capture.
	svc.ReleaseHold(hold.ID)
	_, err = svc.CaptureHold(hold.ID, alice.ID, 100, "")
	assertError(t, err, ErrHoldNotActive)
}

// TestHold_MultipleHolds tests that multiple holds on the same account
// correctly stack.
func TestHold_MultipleHolds(t *testing.T) {
	svc := testService(t)
	alice, _, cash, _ := setupChartOfAccounts(t, svc)

	// Fund Alice with $100.
	svc.PostTransaction(PostTransactionRequest{
		Description: "Deposit",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 10000, Direction: Debit},
			{AccountID: alice.ID, Amount: 10000, Direction: Credit},
		},
	})

	// Place two holds.
	svc.CreateHold(CreateHoldRequest{AccountID: alice.ID, Amount: 2000})
	svc.CreateHold(CreateHoldRequest{AccountID: alice.ID, Amount: 3000})

	// Available = 10000 - 2000 - 3000 = 5000.
	bal, _ := svc.GetBalance(alice.ID)
	assertEqual(t, "available", bal.Available, Amount(5000))
	assertEqual(t, "holds", bal.Holds, Amount(5000))
}

// ---------------------------------------------------------------------------
// End-of-Day Snapshot Tests
// ---------------------------------------------------------------------------

func TestEndOfDaySnapshot(t *testing.T) {
	svc := testService(t)
	alice, _, cash, _ := setupChartOfAccounts(t, svc)

	// Fund Alice.
	svc.PostTransaction(PostTransactionRequest{
		Description: "Deposit",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 10000, Direction: Debit},
			{AccountID: alice.ID, Amount: 10000, Direction: Credit},
		},
	})

	// Take snapshot.
	date := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	snap, err := svc.TakeEndOfDaySnapshot(alice.ID, date)
	assertNoError(t, err)
	assertEqual(t, "snapshot book", snap.Balance.Book, Amount(10000))

	// Retrieve snapshot.
	got, err := svc.GetSnapshot(alice.ID, date)
	assertNoError(t, err)
	assertEqual(t, "retrieved book", got.Balance.Book, Amount(10000))

	// Non-existent snapshot returns ErrSnapshotNotFound.
	otherDate := time.Date(2025, 1, 16, 0, 0, 0, 0, time.UTC)
	_, err = svc.GetSnapshot(alice.ID, otherDate)
	assertError(t, err, ErrSnapshotNotFound)
}

func TestEndOfDaySnapshot_AccountNotFound(t *testing.T) {
	svc := testService(t)

	date := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	_, err := svc.TakeEndOfDaySnapshot("nonexistent", date)
	assertError(t, err, ErrAccountNotFound)
}

func TestGetSnapshot_AccountNotFound(t *testing.T) {
	svc := testService(t)

	date := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	_, err := svc.GetSnapshot("nonexistent", date)
	assertError(t, err, ErrAccountNotFound)
}

// TestEndOfDaySnapshot_Overwrite tests that taking a snapshot for the
// same account/date overwrites the previous one.
func TestEndOfDaySnapshot_Overwrite(t *testing.T) {
	svc := testService(t)
	alice, _, cash, _ := setupChartOfAccounts(t, svc)

	date := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)

	// Fund Alice and take snapshot.
	svc.PostTransaction(PostTransactionRequest{
		Description: "First deposit",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 5000, Direction: Debit},
			{AccountID: alice.ID, Amount: 5000, Direction: Credit},
		},
	})
	svc.TakeEndOfDaySnapshot(alice.ID, date)

	// Post another transaction and retake snapshot.
	svc.PostTransaction(PostTransactionRequest{
		Description: "Second deposit",
		Entries: []Entry{
			{AccountID: cash.ID, Amount: 3000, Direction: Debit},
			{AccountID: alice.ID, Amount: 3000, Direction: Credit},
		},
	})
	snap, _ := svc.TakeEndOfDaySnapshot(alice.ID, date)
	assertEqual(t, "overwritten snapshot", snap.Balance.Book, Amount(8000))
}

// ---------------------------------------------------------------------------
// Audit Trail Tests
// ---------------------------------------------------------------------------

func TestAuditLog(t *testing.T) {
	svc := testService(t)
	alice, bob, _, _ := setupChartOfAccounts(t, svc)

	// Post a transaction.
	svc.PostTransaction(PostTransactionRequest{
		Description: "Transfer",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 100, Direction: Debit},
			{AccountID: bob.ID, Amount: 100, Direction: Credit},
		},
	})

	log := svc.GetAuditLog()

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
	svc := testService(t)
	alice, bob, _, _ := setupChartOfAccounts(t, svc)

	tx, _ := svc.PostTransaction(PostTransactionRequest{
		Description: "Transfer",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 100, Direction: Debit},
			{AccountID: bob.ID, Amount: 100, Direction: Credit},
		},
	})

	// Get events for Alice's account.
	aliceEvents := svc.GetAuditLogForEntity(string(alice.ID))
	assertEqual(t, "alice events", len(aliceEvents), 1)
	assertEqual(t, "event type", aliceEvents[0].Type, EventAccountCreated)

	// Get events for the transaction.
	txEvents := svc.GetAuditLogForEntity(string(tx.ID))
	assertEqual(t, "tx events", len(txEvents), 1)
	assertEqual(t, "event type", txEvents[0].Type, EventTransactionPosted)
}

func TestAuditLog_ImmutableCopy(t *testing.T) {
	svc := testService(t)
	svc.CreateLedger("GL")

	log1 := svc.GetAuditLog()
	log2 := svc.GetAuditLog()

	// Modifying the returned slice should not affect the internal log.
	log1[0].Type = "tampered"
	if log2[0].Type == "tampered" {
		t.Fatal("audit log returned mutable reference")
	}
}

// ---------------------------------------------------------------------------
// Balance Edge Cases
// ---------------------------------------------------------------------------

func TestGetBalance_AccountNotFound(t *testing.T) {
	svc := testService(t)

	_, err := svc.GetBalance("nonexistent")
	assertError(t, err, ErrAccountNotFound)
}

// TestGetBalance_ZeroForNewAccount tests that a new account has zero
// balance.
func TestGetBalance_ZeroForNewAccount(t *testing.T) {
	svc := testService(t)
	alice, _, _, _ := setupChartOfAccounts(t, svc)

	bal, err := svc.GetBalance(alice.ID)
	assertNoError(t, err)
	assertEqual(t, "book", bal.Book, Amount(0))
	assertEqual(t, "holds", bal.Holds, Amount(0))
	assertEqual(t, "available", bal.Available, Amount(0))
}

// TestGetBalance_AllAccountTypes tests that balance calculations work
// correctly for every account type.
func TestGetBalance_AllAccountTypes(t *testing.T) {
	svc := testService(t)

	l, _ := svc.CreateLedger("GL")
	sl, _ := svc.CreateSubledger(l.ID, "Test")

	// Create one account of each type.
	asset, _ := svc.CreateAccount(sl.ID, "Asset", Asset)
	liability, _ := svc.CreateAccount(sl.ID, "Liability", Liability)
	equity, _ := svc.CreateAccount(sl.ID, "Equity", Equity)
	revenue, _ := svc.CreateAccount(sl.ID, "Revenue", Revenue)
	expense, _ := svc.CreateAccount(sl.ID, "Expense", Expense)

	accounts := []Account{asset, liability, equity, revenue, expense}

	// Post a debit of 100 and credit of 100 between pairs.
	// Debit asset, credit liability.
	svc.PostTransaction(PostTransactionRequest{
		Description: "D asset, C liability",
		Entries: []Entry{
			{AccountID: asset.ID, Amount: 1000, Direction: Debit},
			{AccountID: liability.ID, Amount: 1000, Direction: Credit},
		},
	})

	// Debit expense, credit revenue.
	svc.PostTransaction(PostTransactionRequest{
		Description: "D expense, C revenue",
		Entries: []Entry{
			{AccountID: expense.ID, Amount: 500, Direction: Debit},
			{AccountID: revenue.ID, Amount: 500, Direction: Credit},
		},
	})

	// Credit equity, debit asset.
	svc.PostTransaction(PostTransactionRequest{
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
		bal, _ := svc.GetBalance(acct.ID)
		assertEqual(t, acct.Name+" balance", bal.Book, expected[i])
	}
}

// ---------------------------------------------------------------------------
// Integration Test: Full Banking Workflow
// ---------------------------------------------------------------------------

// TestFullBankingWorkflow simulates a realistic banking day:
//
//  1. Set up chart of accounts.
//  2. Customer deposits cash.
//  3. Customer makes a card payment (hold -> capture).
//  4. Customer receives a wire transfer.
//  5. An erroneous fee is posted and then reversed.
//  6. End-of-day snapshots are taken.
//  7. Audit trail is verified.
func TestFullBankingWorkflow(t *testing.T) {
	svc := testService(t)

	// Step 1: Chart of accounts.
	gl, _ := svc.CreateLedger("General Ledger")
	customerDeposits, _ := svc.CreateSubledger(gl.ID, "Customer Deposits")
	bankAssets, _ := svc.CreateSubledger(gl.ID, "Bank Assets")
	rev, _ := svc.CreateSubledger(gl.ID, "Revenue")
	exp, _ := svc.CreateSubledger(gl.ID, "Expenses")

	alice, _ := svc.CreateAccount(customerDeposits.ID, "Alice Checking", Liability)
	nostro, _ := svc.CreateAccount(bankAssets.ID, "Nostro USD", Asset)
	cashAccount, _ := svc.CreateAccount(bankAssets.ID, "Cash Vault", Asset)
	feeIncome, _ := svc.CreateAccount(rev.ID, "Fee Income", Revenue)
	merchant, _ := svc.CreateAccount(exp.ID, "Merchant Payable", Expense)

	// Step 2: Alice deposits $500 cash at the teller.
	svc.PostTransaction(PostTransactionRequest{
		IdempotencyKey: "deposit-001",
		Description:    "Cash deposit at branch",
		Entries: []Entry{
			{AccountID: cashAccount.ID, Amount: 50000, Direction: Debit},
			{AccountID: alice.ID, Amount: 50000, Direction: Credit},
		},
	})

	aliceBal, _ := svc.GetBalance(alice.ID)
	assertEqual(t, "after deposit", aliceBal.Book, Amount(50000))

	// Step 3: Alice swipes her card at a restaurant ($80 auth).
	hold, _ := svc.CreateHold(CreateHoldRequest{
		AccountID:   alice.ID,
		Amount:      8000,
				Description: "Card auth: Restaurant",
		ExpiresAt:   time.Date(2025, 1, 22, 0, 0, 0, 0, time.UTC),
	})

	aliceBal, _ = svc.GetBalance(alice.ID)
	assertEqual(t, "book with hold", aliceBal.Book, Amount(50000))
	assertEqual(t, "available with hold", aliceBal.Available, Amount(42000))

	// Restaurant captures for $75 (tip adjusted).
	svc.CaptureHold(hold.ID, merchant.ID, 7500, "Restaurant bill")

	aliceBal, _ = svc.GetBalance(alice.ID)
	assertEqual(t, "book after capture", aliceBal.Book, Amount(42500))
	assertEqual(t, "available after capture", aliceBal.Available, Amount(42500))

	// Step 4: Alice receives a $200 wire transfer.
	svc.PostTransaction(PostTransactionRequest{
		IdempotencyKey: "wire-001",
		Description:    "Incoming wire transfer",
		BookingDate:    time.Date(2025, 1, 15, 14, 30, 0, 0, time.UTC),
		ValueDate:      time.Date(2025, 1, 16, 0, 0, 0, 0, time.UTC),
		Entries: []Entry{
			{AccountID: nostro.ID, Amount: 20000, Direction: Debit},
			{AccountID: alice.ID, Amount: 20000, Direction: Credit},
		},
	})

	aliceBal, _ = svc.GetBalance(alice.ID)
	assertEqual(t, "after wire", aliceBal.Book, Amount(62500))

	// Step 5: Erroneous $10 fee, then reversal.
	errTx, _ := svc.PostTransaction(PostTransactionRequest{
		IdempotencyKey: "fee-001",
		Description:    "Monthly maintenance fee (error)",
		Entries: []Entry{
			{AccountID: alice.ID, Amount: 1000, Direction: Debit},
			{AccountID: feeIncome.ID, Amount: 1000, Direction: Credit},
		},
	})

	aliceBal, _ = svc.GetBalance(alice.ID)
	assertEqual(t, "after fee", aliceBal.Book, Amount(61500))

	svc.ReverseTransaction(errTx.ID, "Reverse erroneous fee")

	aliceBal, _ = svc.GetBalance(alice.ID)
	assertEqual(t, "after fee reversal", aliceBal.Book, Amount(62500))

	// Step 6: End-of-day snapshot.
	eod := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	snap, _ := svc.TakeEndOfDaySnapshot(alice.ID, eod)
	assertEqual(t, "eod snapshot", snap.Balance.Book, Amount(62500))

	// Step 7: Audit trail should contain all operations.
	auditLog := svc.GetAuditLog()
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
	if counts[EventHoldCreated] != 1 {
		t.Fatalf("expected 1 hold.created event, got %d", counts[EventHoldCreated])
	}
	if counts[EventHoldCaptured] != 1 {
		t.Fatalf("expected 1 hold.captured event, got %d", counts[EventHoldCaptured])
	}
	if counts[EventSnapshotTaken] != 1 {
		t.Fatalf("expected 1 snapshot.taken event, got %d", counts[EventSnapshotTaken])
	}
}

// ---------------------------------------------------------------------------
// Helper functions for tests
// ---------------------------------------------------------------------------

func findAccountByName(t *testing.T, svc *Service, name string) Account {
	t.Helper()
	for _, acct := range svc.accounts {
		if acct.Name == name {
			return *acct
		}
	}
	t.Fatalf("account %q not found", name)
	return Account{}
}
