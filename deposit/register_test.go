package deposit

import (
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// fixedTime is the instant returned by the test clock, matching the ledger
// package's own test clock.
var fixedTime = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// Test Helpers
// ---------------------------------------------------------------------------

// testRegister creates a Register backed by a fresh ledger with a fixed clock,
// returning the register, the customer-deposits subledger, and a counterparty
// Asset account (cash) for capture postings.
func testRegister(t *testing.T) (*Register, ledger.SubledgerID, ledger.AccountID) {
	t.Helper()
	clock := func() time.Time { return fixedTime }
	book := ledger.NewBookWithClock(clock)
	reg := NewRegisterWithClock(book, clock)

	gl, err := book.CreateLedger("General Ledger")
	assertNoError(t, err)
	deposits, err := book.CreateSubledger(gl.ID, "Customer Deposits")
	assertNoError(t, err)
	assets, err := book.CreateSubledger(gl.ID, "Bank Assets")
	assertNoError(t, err)
	cash, err := book.CreateAccount(assets.ID, "Cash", ledger.Asset)
	assertNoError(t, err)

	return reg, deposits.ID, cash.ID
}

// fund credits a deposit account's backing GL account from the cash asset,
// simulating a customer deposit so the customer has spendable funds.
func fund(t *testing.T, reg *Register, cash ledger.AccountID, acct Account, amount ledger.Amount) {
	t.Helper()
	_, err := reg.book.PostTransaction(ledger.PostTransactionRequest{
		Description: "Funding",
		Entries: []ledger.Entry{
			{AccountID: cash, Amount: amount, Direction: ledger.Debit},
			{AccountID: acct.GLAccount, Amount: amount, Direction: ledger.Credit},
		},
	})
	assertNoError(t, err)
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
// Account Tests
// ---------------------------------------------------------------------------

func TestOpenAccount_CreatesBackingGLAccount(t *testing.T) {
	reg, deposits, _ := testRegister(t)

	acct, err := reg.OpenAccount(deposits, "Alice", 0)
	assertNoError(t, err)
	assertEqual(t, "status", acct.Status, Active)

	// The backing GL account exists and is a Liability.
	gl, err := reg.book.GetAccount(acct.GLAccount)
	assertNoError(t, err)
	assertEqual(t, "gl type", gl.Type, ledger.Liability)
	assertEqual(t, "gl name", gl.Name, "Alice")

	// Retrieval round-trips.
	got, err := reg.GetAccount(acct.ID)
	assertNoError(t, err)
	assertEqual(t, "id", got.ID, acct.ID)
}

func TestOpenAccount_SubledgerNotFound(t *testing.T) {
	reg, _, _ := testRegister(t)

	_, err := reg.OpenAccount("bad_sub", "Alice", 0)
	assertError(t, err, ledger.ErrSubledgerNotFound)
}

func TestGetAccount_NotFound(t *testing.T) {
	reg, _, _ := testRegister(t)

	_, err := reg.GetAccount("nonexistent")
	assertError(t, err, ErrAccountNotFound)
}

// ---------------------------------------------------------------------------
// Balance & Hold Tests
// ---------------------------------------------------------------------------

func TestHold_ReducesAvailable(t *testing.T) {
	reg, deposits, cash := testRegister(t)
	alice, _ := reg.OpenAccount(deposits, "Alice", 0)
	fund(t, reg, cash, alice, 10000)

	h, err := reg.CreateHold(CreateHoldRequest{AccountID: alice.ID, Amount: 3000})
	assertNoError(t, err)
	assertEqual(t, "hold status", h.Status, HoldActive)

	bal, err := reg.GetBalance(alice.ID)
	assertNoError(t, err)
	assertEqual(t, "book", bal.Book, ledger.Amount(10000))
	assertEqual(t, "holds", bal.Holds, ledger.Amount(3000))
	assertEqual(t, "available", bal.Available, ledger.Amount(7000))
}

func TestHold_Release_RestoresAvailable(t *testing.T) {
	reg, deposits, cash := testRegister(t)
	alice, _ := reg.OpenAccount(deposits, "Alice", 0)
	fund(t, reg, cash, alice, 10000)

	h, _ := reg.CreateHold(CreateHoldRequest{AccountID: alice.ID, Amount: 3000})
	assertNoError(t, reg.ReleaseHold(h.ID))

	bal, _ := reg.GetBalance(alice.ID)
	assertEqual(t, "holds", bal.Holds, ledger.Amount(0))
	assertEqual(t, "available", bal.Available, ledger.Amount(10000))

	// Releasing again fails.
	assertError(t, reg.ReleaseHold(h.ID), ErrHoldNotActive)
}

func TestReleaseHold_NotFound(t *testing.T) {
	reg, _, _ := testRegister(t)
	assertError(t, reg.ReleaseHold("nonexistent"), ErrHoldNotFound)
}

func TestCreateHold_Validation(t *testing.T) {
	reg, deposits, _ := testRegister(t)

	_, err := reg.CreateHold(CreateHoldRequest{AccountID: "nonexistent", Amount: 100})
	assertError(t, err, ErrAccountNotFound)

	alice, _ := reg.OpenAccount(deposits, "Alice", 0)
	_, err = reg.CreateHold(CreateHoldRequest{AccountID: alice.ID, Amount: 0})
	assertError(t, err, ErrInvalidAmount)

	// No funds, no overdraft: a hold of 1 overdraws available.
	_, err = reg.CreateHold(CreateHoldRequest{AccountID: alice.ID, Amount: 1})
	assertError(t, err, ErrInsufficientAvailable)
}

func TestHold_Expiration(t *testing.T) {
	reg, deposits, cash := testRegister(t)
	alice, _ := reg.OpenAccount(deposits, "Alice", 0)
	fund(t, reg, cash, alice, 10000)

	_, err := reg.CreateHold(CreateHoldRequest{
		AccountID: alice.ID,
		Amount:    3000,
		ExpiresAt: time.Date(2025, 1, 14, 0, 0, 0, 0, time.UTC), // yesterday
	})
	assertNoError(t, err)

	bal, _ := reg.GetBalance(alice.ID)
	assertEqual(t, "holds", bal.Holds, ledger.Amount(0))
	assertEqual(t, "available", bal.Available, ledger.Amount(10000))
}

// ---------------------------------------------------------------------------
// Overdraft
// ---------------------------------------------------------------------------

func TestOverdraft_PermitsWithdrawal(t *testing.T) {
	reg, deposits, _ := testRegister(t)

	// No overdraft: a withdrawal of 5000 on an empty account fails.
	noLimit, _ := reg.OpenAccount(deposits, "NoLimit", 0)
	assertError(t, reg.CheckWithdrawal(noLimit.ID, 5000), ErrInsufficientAvailable)

	// With a 5000 overdraft limit the same withdrawal is permitted.
	withLimit, _ := reg.OpenAccount(deposits, "WithLimit", 5000)
	assertNoError(t, reg.CheckWithdrawal(withLimit.ID, 5000))
	// But not a penny more.
	assertError(t, reg.CheckWithdrawal(withLimit.ID, 5001), ErrInsufficientAvailable)

	// A hold up to the overdraft limit succeeds on an unfunded account.
	_, err := reg.CreateHold(CreateHoldRequest{AccountID: withLimit.ID, Amount: 5000})
	assertNoError(t, err)
}

func TestCheckWithdrawal_NotFound(t *testing.T) {
	reg, _, _ := testRegister(t)
	assertError(t, reg.CheckWithdrawal("nonexistent", 100), ErrAccountNotFound)
}

// ---------------------------------------------------------------------------
// Capture
// ---------------------------------------------------------------------------

func TestCaptureHold_PostsGLTransaction(t *testing.T) {
	reg, deposits, cash := testRegister(t)
	alice, _ := reg.OpenAccount(deposits, "Alice", 0)
	fund(t, reg, cash, alice, 10000)

	h, _ := reg.CreateHold(CreateHoldRequest{AccountID: alice.ID, Amount: 3000})

	tx, err := reg.CaptureHold(h.ID, cash, 2500, "Gas purchase")
	assertNoError(t, err)
	assertEqual(t, "tx status", tx.Status, ledger.Posted)
	assertEqual(t, "tx legs", len(tx.Entries), 2)

	got, _ := reg.GetHold(h.ID)
	assertEqual(t, "hold captured", got.Status, HoldCaptured)

	// Book balance moved: 10000 - 2500 = 7500. Hold no longer active.
	bal, _ := reg.GetBalance(alice.ID)
	assertEqual(t, "book after capture", bal.Book, ledger.Amount(7500))
	assertEqual(t, "holds after capture", bal.Holds, ledger.Amount(0))
	assertEqual(t, "available after capture", bal.Available, ledger.Amount(7500))
}

func TestCaptureHold_DefaultsToHoldAmount(t *testing.T) {
	reg, deposits, cash := testRegister(t)
	alice, _ := reg.OpenAccount(deposits, "Alice", 0)
	fund(t, reg, cash, alice, 10000)

	h, _ := reg.CreateHold(CreateHoldRequest{AccountID: alice.ID, Amount: 3000})
	tx, err := reg.CaptureHold(h.ID, cash, 0, "Full capture")
	assertNoError(t, err)
	assertEqual(t, "captured amount leg", tx.Entries[0].Amount, ledger.Amount(3000))
}

func TestCaptureHold_Errors(t *testing.T) {
	reg, deposits, cash := testRegister(t)

	_, err := reg.CaptureHold("nonexistent", cash, 100, "")
	assertError(t, err, ErrHoldNotFound)

	alice, _ := reg.OpenAccount(deposits, "Alice", 1000)
	h, _ := reg.CreateHold(CreateHoldRequest{AccountID: alice.ID, Amount: 1000})
	assertNoError(t, reg.ReleaseHold(h.ID))
	_, err = reg.CaptureHold(h.ID, cash, 100, "")
	assertError(t, err, ErrHoldNotActive)
}

// ---------------------------------------------------------------------------
// Status Lifecycle
// ---------------------------------------------------------------------------

func TestFreeze_BlocksHolds(t *testing.T) {
	reg, deposits, cash := testRegister(t)
	alice, _ := reg.OpenAccount(deposits, "Alice", 0)
	fund(t, reg, cash, alice, 10000)

	assertNoError(t, reg.Freeze(alice.ID))

	_, err := reg.CreateHold(CreateHoldRequest{AccountID: alice.ID, Amount: 1000})
	assertError(t, err, ErrAccountFrozen)
	assertError(t, reg.CheckWithdrawal(alice.ID, 1000), ErrAccountFrozen)

	// Unfreeze restores the ability to hold.
	assertNoError(t, reg.Unfreeze(alice.ID))
	_, err = reg.CreateHold(CreateHoldRequest{AccountID: alice.ID, Amount: 1000})
	assertNoError(t, err)
}

func TestDormantReactivate(t *testing.T) {
	reg, deposits, _ := testRegister(t)
	alice, _ := reg.OpenAccount(deposits, "Alice", 0)

	assertNoError(t, reg.MarkDormant(alice.ID))
	got, _ := reg.GetAccount(alice.ID)
	assertEqual(t, "dormant", got.Status, Dormant)

	assertNoError(t, reg.Reactivate(alice.ID))
	got, _ = reg.GetAccount(alice.ID)
	assertEqual(t, "reactivated", got.Status, Active)
}

func TestClose_RequiresZeroBalance(t *testing.T) {
	reg, deposits, cash := testRegister(t)
	alice, _ := reg.OpenAccount(deposits, "Alice", 0)
	fund(t, reg, cash, alice, 10000)

	// Non-zero balance blocks close.
	assertError(t, reg.Close(alice.ID), ErrAccountNotEmpty)

	// Drain the balance, then close succeeds.
	h, _ := reg.CreateHold(CreateHoldRequest{AccountID: alice.ID, Amount: 10000})
	_, err := reg.CaptureHold(h.ID, cash, 10000, "Final withdrawal")
	assertNoError(t, err)
	assertNoError(t, reg.Close(alice.ID))

	got, _ := reg.GetAccount(alice.ID)
	assertEqual(t, "closed", got.Status, Closed)

	// Operations on a closed account report ErrAccountClosed.
	assertError(t, reg.CheckWithdrawal(alice.ID, 1), ErrAccountClosed)
	// Closed is terminal.
	assertError(t, reg.Close(alice.ID), ErrInvalidStatusTransition)
}

func TestIllegalStatusTransitions(t *testing.T) {
	reg, deposits, _ := testRegister(t)
	alice, _ := reg.OpenAccount(deposits, "Alice", 0)

	// Cannot unfreeze an active account, nor reactivate one.
	assertError(t, reg.Unfreeze(alice.ID), ErrInvalidStatusTransition)
	assertError(t, reg.Reactivate(alice.ID), ErrInvalidStatusTransition)

	// Freeze, then a freeze again is illegal; dormant from frozen is illegal.
	assertNoError(t, reg.Freeze(alice.ID))
	assertError(t, reg.Freeze(alice.ID), ErrInvalidStatusTransition)
	assertError(t, reg.MarkDormant(alice.ID), ErrInvalidStatusTransition)

	// Status ops on a missing account.
	assertError(t, reg.Freeze("nonexistent"), ErrAccountNotFound)
}

// ---------------------------------------------------------------------------
// Snapshots
// ---------------------------------------------------------------------------

func TestSnapshot_RoundTrip(t *testing.T) {
	reg, deposits, cash := testRegister(t)
	alice, _ := reg.OpenAccount(deposits, "Alice", 0)
	fund(t, reg, cash, alice, 10000)

	date := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	snap, err := reg.TakeEndOfDaySnapshot(alice.ID, date)
	assertNoError(t, err)
	assertEqual(t, "snapshot book", snap.Balance.Book, ledger.Amount(10000))

	got, err := reg.GetSnapshot(alice.ID, date)
	assertNoError(t, err)
	assertEqual(t, "retrieved book", got.Balance.Book, ledger.Amount(10000))

	// Missing date.
	other := time.Date(2025, 1, 16, 0, 0, 0, 0, time.UTC)
	_, err = reg.GetSnapshot(alice.ID, other)
	assertError(t, err, ErrSnapshotNotFound)

	// Missing account.
	_, err = reg.TakeEndOfDaySnapshot("nonexistent", date)
	assertError(t, err, ErrAccountNotFound)
	_, err = reg.GetSnapshot("nonexistent", date)
	assertError(t, err, ErrAccountNotFound)
}

// ---------------------------------------------------------------------------
// Enum String() methods
// ---------------------------------------------------------------------------

func TestStringers(t *testing.T) {
	assertEqual(t, "active", Active.String(), "Active")
	assertEqual(t, "dormant", Dormant.String(), "Dormant")
	assertEqual(t, "frozen", Frozen.String(), "Frozen")
	assertEqual(t, "closed", Closed.String(), "Closed")
	assertEqual(t, "status unknown", AccountStatus(99).String(), "Unknown")

	assertEqual(t, "hold active", HoldActive.String(), "Active")
	assertEqual(t, "hold released", HoldReleased.String(), "Released")
	assertEqual(t, "hold captured", HoldCaptured.String(), "Captured")
	assertEqual(t, "hold unknown", HoldStatus(99).String(), "Unknown")
}

// mustCash returns the cash account created by testRegister by name, for tests
// that do not capture the third return value.
func mustCash(t *testing.T, reg *Register) ledger.AccountID {
	t.Helper()
	// Cash is the only Asset account in the test fixture.
	for id := 1; id <= 100; id++ {
		acct, err := reg.book.GetAccount(ledger.AccountID("acct_" + itoa(id)))
		if err != nil {
			continue
		}
		if acct.Type == ledger.Asset {
			return acct.ID
		}
	}
	t.Fatal("cash account not found")
	return ""
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
