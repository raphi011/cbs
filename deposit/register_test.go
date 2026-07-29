// Package deposit_test holds the deposit layer's tests.
//
// They live outside package deposit because they build a Register over
// store/mem, and store/mem imports deposit — an in-package test file importing
// it would be an import cycle. The package is dot-imported so the test bodies
// read exactly as they did when they were in-package; deposit/export_test.go
// re-exports the one unexported thing they still need.
package deposit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/store/testenv"
)

// fixedTime is the instant returned by the test clock, matching the ledger
// package's own test clock.
//
// The clock is deliberately frozen: every row then ties on CreatedAt, which is
// exactly the case the store's ordering tie-break has to get right. A ticking
// clock would hide a listing that falls back to comparing IDs — where "dep_10"
// sorts ahead of "dep_8" — so freezing it is the stronger fixture.
var fixedTime = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

// ---------------------------------------------------------------------------
// Test Helpers
// ---------------------------------------------------------------------------

// testAsset is what accounts in these tests are denominated in unless a test
// says otherwise. A second asset is registered alongside it, so a test can open
// an account in something that is not the euro without registering it first.
const (
	testAsset  ledger.AssetCode = "EUR"
	otherAsset ledger.AssetCode = "BTC"
)

// mutableClock is a test clock a test can move. The rest of this package runs
// on the frozen one above, which is the stronger fixture for row ordering — but
// an accrual window opens at the instant terms are set, so a test that reprices
// an account has to be able to say when that happened.
type mutableClock struct{ at time.Time }

func (c *mutableClock) set(t time.Time) { c.at = t }
func (c *mutableClock) now() time.Time  { return c.at }

// newTestRegister creates a Register backed by a fresh ledger with a fixed
// clock, returning the register, its *ledger.Book, and the customer-deposits
// subledger ID.
func newTestRegister(t *testing.T) (*Register, *ledger.Book, ledger.SubledgerID) {
	t.Helper()
	return newTestRegisterOn(t, func() time.Time { return fixedTime })
}

// newTestRegisterOn is newTestRegister on a caller-supplied clock, for the
// tests where WHEN an operation happened is the thing under test.
func newTestRegisterOn(t *testing.T, clock func() time.Time) (*Register, *ledger.Book, ledger.SubledgerID) {
	t.Helper()
	ctx := context.Background()
	store := testenv.New(t, clock)
	book := ledger.NewBook(store, "bank", clock)
	reg := NewRegister(store.Deposit(), book, book.ID(), clock)

	gl, err := book.CreateLedger(ctx, "General Ledger")
	assertNoError(t, err)
	deposits, err := book.CreateSubledger(ctx, gl.ID, "Customer Deposits")
	assertNoError(t, err)

	return reg, book, deposits.ID
}

// newCashAccount creates a Bank Assets subledger alongside the register's
// customer-deposits one, and a counterparty Asset account (cash) within it,
// for tests that fund customer accounts via a capture posting.
func newCashAccount(t *testing.T, book *ledger.Book, deposits ledger.SubledgerID) ledger.AccountID {
	t.Helper()
	ctx := context.Background()
	dep, err := book.GetSubledger(ctx, deposits)
	assertNoError(t, err)
	assets, err := book.CreateSubledger(ctx, dep.LedgerID, "Bank Assets")
	assertNoError(t, err)
	cash, err := book.CreateAccount(ctx, assets.ID, "Cash", ledger.Asset, testAsset)
	assertNoError(t, err)
	return cash.ID
}

// newFundedAccount opens a deposit account and funds it with 1000, returning the
// register, the ledger beneath it and the account.
func newFundedAccount(t *testing.T) (*Register, *ledger.Book, Account) {
	t.Helper()
	reg, book, deposits := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	acct, err := reg.OpenAccount(context.Background(), deposits, "Alice", testAsset, 0)
	assertNoError(t, err)
	fund(t, reg, cash, acct, 1000)
	return reg, book, acct
}

// fund credits a deposit account's backing GL account from the cash asset,
// simulating a customer deposit so the customer has spendable funds.
func fund(t *testing.T, reg *Register, cash ledger.AccountID, acct Account, amount ledger.Amount) {
	t.Helper()
	_, err := reg.Book().PostTransaction(context.Background(), ledger.PostTransactionRequest{
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
	ctx := context.Background()
	reg, _, deposits := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, deposits, "Alice", testAsset, 0)
	assertNoError(t, err)
	assertEqual(t, "status", acct.Status, Active)

	// The backing GL account exists and is a Liability.
	gl, err := reg.Book().GetAccount(ctx, acct.GLAccount)
	assertNoError(t, err)
	assertEqual(t, "gl type", gl.Type, ledger.Liability)
	assertEqual(t, "gl name", gl.Name, "Alice")

	// Retrieval round-trips.
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "id", got.ID, acct.ID)
}

// A deposit account's asset is a copy of its backing GL account's, so the two
// can never disagree — and a customer account is not a euro account by
// default, it is an account in whatever asset it was opened in.
func TestOpenAccountRecordsAssetMatchingItsGLAccount(t *testing.T) {
	ctx := context.Background()
	reg, _, deposits := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, deposits, "Anna BTC", otherAsset, 0)
	assertNoError(t, err)
	assertEqual(t, "deposit account asset", acct.Asset, otherAsset)

	gl, err := reg.Book().GetAccount(ctx, acct.GLAccount)
	assertNoError(t, err)
	assertEqual(t, "GL account asset", gl.Asset, acct.Asset)

	// It survives the store, not just the return value.
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "reloaded asset", got.Asset, otherAsset)
}

func TestOpenAccountRejectsUnregisteredAsset(t *testing.T) {
	ctx := context.Background()
	reg, _, deposits := newTestRegister(t)

	_, err := reg.OpenAccount(ctx, deposits, "Anna DOGE", "DOGE", 0)
	assertError(t, err, ledger.ErrAssetNotFound)
}

func TestOpenAccount_SubledgerNotFound(t *testing.T) {
	ctx := context.Background()
	reg, _, _ := newTestRegister(t)

	_, err := reg.OpenAccount(ctx, "bad_sub", "Alice", testAsset, 0)
	assertError(t, err, ledger.ErrSubledgerNotFound)
}

func TestGetAccount_NotFound(t *testing.T) {
	ctx := context.Background()
	reg, _, _ := newTestRegister(t)

	_, err := reg.GetAccount(ctx, "nonexistent")
	assertError(t, err, ErrAccountNotFound)
}

// ---------------------------------------------------------------------------
// Balance & Hold Tests
// ---------------------------------------------------------------------------

func TestHold_ReducesAvailable(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, deposits, "Alice", testAsset, 0)
	fund(t, reg, cash, alice, 10000)

	h, err := reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 3000})
	assertNoError(t, err)
	assertEqual(t, "hold status", h.Status, HoldActive)

	bal, err := reg.GetBalance(ctx, alice.ID)
	assertNoError(t, err)
	assertEqual(t, "book", bal.Book, ledger.Amount(10000))
	assertEqual(t, "holds", bal.Holds, ledger.Amount(3000))
	assertEqual(t, "available", bal.Available, ledger.Amount(7000))
}

func TestHold_Release_RestoresAvailable(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, deposits, "Alice", testAsset, 0)
	fund(t, reg, cash, alice, 10000)

	h, _ := reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 3000})
	assertNoError(t, reg.ReleaseHold(ctx, h.ID))

	bal, _ := reg.GetBalance(ctx, alice.ID)
	assertEqual(t, "holds", bal.Holds, ledger.Amount(0))
	assertEqual(t, "available", bal.Available, ledger.Amount(10000))

	// Releasing again fails.
	assertError(t, reg.ReleaseHold(ctx, h.ID), ErrHoldNotActive)
}

func TestReleaseHold_NotFound(t *testing.T) {
	ctx := context.Background()
	reg, _, _ := newTestRegister(t)
	assertError(t, reg.ReleaseHold(ctx, "nonexistent"), ErrHoldNotFound)
}

func TestCreateHold_Validation(t *testing.T) {
	ctx := context.Background()
	reg, _, deposits := newTestRegister(t)

	_, err := reg.CreateHold(ctx, CreateHoldRequest{AccountID: "nonexistent", Amount: 100})
	assertError(t, err, ErrAccountNotFound)

	alice, _ := reg.OpenAccount(ctx, deposits, "Alice", testAsset, 0)
	_, err = reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 0})
	assertError(t, err, ErrInvalidAmount)

	// No funds, no overdraft: a hold of 1 overdraws available.
	_, err = reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 1})
	assertError(t, err, ErrInsufficientAvailable)
}

func TestHold_Expiration(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, deposits, "Alice", testAsset, 0)
	fund(t, reg, cash, alice, 10000)

	_, err := reg.CreateHold(ctx, CreateHoldRequest{
		AccountID: alice.ID,
		Amount:    3000,
		ExpiresAt: time.Date(2025, 1, 14, 0, 0, 0, 0, time.UTC), // yesterday
	})
	assertNoError(t, err)

	bal, _ := reg.GetBalance(ctx, alice.ID)
	assertEqual(t, "holds", bal.Holds, ledger.Amount(0))
	assertEqual(t, "available", bal.Available, ledger.Amount(10000))
}

// ---------------------------------------------------------------------------
// Overdraft
// ---------------------------------------------------------------------------

func TestOverdraft_PermitsWithdrawal(t *testing.T) {
	ctx := context.Background()
	reg, _, deposits := newTestRegister(t)

	// No overdraft: a withdrawal of 5000 on an empty account fails.
	noLimit, _ := reg.OpenAccount(ctx, deposits, "NoLimit", testAsset, 0)
	assertError(t, reg.CheckWithdrawal(ctx, noLimit.ID, 5000), ErrInsufficientAvailable)

	// With a 5000 overdraft limit the same withdrawal is permitted.
	withLimit, _ := reg.OpenAccount(ctx, deposits, "WithLimit", testAsset, 5000)
	assertNoError(t, reg.CheckWithdrawal(ctx, withLimit.ID, 5000))
	// But not a penny more.
	assertError(t, reg.CheckWithdrawal(ctx, withLimit.ID, 5001), ErrInsufficientAvailable)

	// A hold up to the overdraft limit succeeds on an unfunded account.
	_, err := reg.CreateHold(ctx, CreateHoldRequest{AccountID: withLimit.ID, Amount: 5000})
	assertNoError(t, err)
}

func TestCheckWithdrawal_NotFound(t *testing.T) {
	ctx := context.Background()
	reg, _, _ := newTestRegister(t)
	assertError(t, reg.CheckWithdrawal(ctx, "nonexistent", 100), ErrAccountNotFound)
}

// ---------------------------------------------------------------------------
// Capture
// ---------------------------------------------------------------------------

func TestCaptureHold_PostsGLTransaction(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, deposits, "Alice", testAsset, 0)
	fund(t, reg, cash, alice, 10000)

	h, _ := reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 3000})

	tx, err := reg.CaptureHold(ctx, h.ID, cash, 2500, "Gas purchase")
	assertNoError(t, err)
	assertEqual(t, "tx status", tx.Status, ledger.Posted)
	assertEqual(t, "tx legs", len(tx.Entries), 2)

	got, _ := reg.GetHold(ctx, h.ID)
	assertEqual(t, "hold captured", got.Status, HoldCaptured)

	// Book balance moved: 10000 - 2500 = 7500. Hold no longer active.
	bal, _ := reg.GetBalance(ctx, alice.ID)
	assertEqual(t, "book after capture", bal.Book, ledger.Amount(7500))
	assertEqual(t, "holds after capture", bal.Holds, ledger.Amount(0))
	assertEqual(t, "available after capture", bal.Available, ledger.Amount(7500))
}

func TestCaptureHold_DefaultsToHoldAmount(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, deposits, "Alice", testAsset, 0)
	fund(t, reg, cash, alice, 10000)

	h, _ := reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 3000})
	tx, err := reg.CaptureHold(ctx, h.ID, cash, 0, "Full capture")
	assertNoError(t, err)
	assertEqual(t, "captured amount leg", tx.Entries[0].Amount, ledger.Amount(3000))
}

func TestCaptureHold_Errors(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)

	_, err := reg.CaptureHold(ctx, "nonexistent", cash, 100, "")
	assertError(t, err, ErrHoldNotFound)

	alice, _ := reg.OpenAccount(ctx, deposits, "Alice", testAsset, 1000)
	h, _ := reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 1000})
	assertNoError(t, reg.ReleaseHold(ctx, h.ID))
	_, err = reg.CaptureHold(ctx, h.ID, cash, 100, "")
	assertError(t, err, ErrHoldNotActive)
}

// CaptureHold writes a hold status and posts a GL transaction. They must commit
// together: if the posting fails, the hold must remain Active.
func TestCaptureHoldIsAtomic(t *testing.T) {
	ctx := context.Background()
	reg, book, acct := newFundedAccount(t) // helper: account with 1000 available

	h, err := reg.CreateHold(ctx, CreateHoldRequest{AccountID: acct.ID, Amount: 500})
	assertNoError(t, err)

	// Capture against a counterparty account that does not exist, so the GL
	// posting fails after the hold write.
	_, err = reg.CaptureHold(ctx, h.ID, ledger.AccountID("no.such.account"), 500, "boom")
	assertError(t, err, ledger.ErrAccountNotFound)

	got, err := reg.GetHold(ctx, h.ID)
	assertNoError(t, err)
	assertEqual(t, "hold status after failed capture", got.Status, HoldActive)
	_ = book
}

// The other half of atomicity, and the half a two-transaction implementation
// cannot provide: when a capture is composed into a caller's unit of work that
// later fails, the GL transaction it posted must not survive either.
//
// This is what the payment layer will rely on — a settlement that captures a
// hold and then hits a problem must leave no posting behind.
func TestCaptureHoldRollsBackWithTheCallersUnitOfWork(t *testing.T) {
	ctx := context.Background()
	reg, book, acct := newFundedAccount(t)

	h, err := reg.CreateHold(ctx, CreateHoldRequest{AccountID: acct.ID, Amount: 500})
	assertNoError(t, err)

	before, err := book.ListTransactions(ctx)
	assertNoError(t, err)

	// One unit of work: capture the hold, then fail.
	boom := errors.New("caller changed its mind")
	var captured ledger.Transaction
	err = reg.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		captured, err = reg.CaptureHoldTx(ctx, tx, h.ID, acct.GLAccount, 500, "compose")
		if err != nil {
			return err
		}
		return boom
	})
	assertError(t, err, boom)

	// The capture itself had succeeded inside the transaction...
	assertEqual(t, "capture posted inside the unit of work", captured.Status, ledger.Posted)

	// ...but nothing of it survived the rollback: no GL transaction,
	// and the hold is still Active.
	after, err := book.ListTransactions(ctx)
	assertNoError(t, err)
	assertEqual(t, "GL transactions after rollback", len(after), len(before))
	if _, err := book.GetTransaction(ctx, captured.ID); !errors.Is(err, ledger.ErrTransactionNotFound) {
		t.Fatalf("rolled-back GL transaction %s: got error %v, want %v", captured.ID, err, ledger.ErrTransactionNotFound)
	}

	got, err := reg.GetHold(ctx, h.ID)
	assertNoError(t, err)
	assertEqual(t, "hold status after rollback", got.Status, HoldActive)

	// The audit log did not keep the event either.
	events, err := reg.GetAuditLog(ctx)
	assertNoError(t, err)
	for _, e := range events {
		if e.Type == ledger.EventHoldCaptured {
			t.Fatalf("audit log kept a %s event from a rolled-back unit of work", e.Type)
		}
	}
}

// A capture that moves money into an account of a different asset needs no
// check in the deposit layer: the posting debits one asset and credits
// another, and the ledger's per-asset balance rule refuses it. This is the
// invariant paying for itself — deposit relies on the GL posting for a check
// it never has to implement on its own.
func TestCaptureHoldRejectsCrossAssetCounterparty(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)

	acct, err := reg.OpenAccount(ctx, deposits, "Anna EUR", testAsset, 0)
	assertNoError(t, err)
	fund(t, reg, cash, acct, 10_000)

	btcGL, err := reg.Book().CreateAccount(ctx, deposits, "Merchant BTC", ledger.Liability, otherAsset)
	assertNoError(t, err)

	hold, err := reg.CreateHold(ctx, CreateHoldRequest{
		AccountID: acct.ID, Amount: 500, Description: "auth",
	})
	assertNoError(t, err)

	_, err = reg.CaptureHold(ctx, hold.ID, btcGL.ID, 500, "capture into a BTC account")
	assertError(t, err, ledger.ErrUnbalancedAsset)
}

// ---------------------------------------------------------------------------
// Status Lifecycle
// ---------------------------------------------------------------------------

func TestFreeze_BlocksHolds(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, deposits, "Alice", testAsset, 0)
	fund(t, reg, cash, alice, 10000)

	assertNoError(t, reg.Freeze(ctx, alice.ID))

	_, err := reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 1000})
	assertError(t, err, ErrAccountFrozen)
	assertError(t, reg.CheckWithdrawal(ctx, alice.ID, 1000), ErrAccountFrozen)

	// Unfreeze restores the ability to hold.
	assertNoError(t, reg.Unfreeze(ctx, alice.ID))
	_, err = reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 1000})
	assertNoError(t, err)
}

func TestDormantReactivate(t *testing.T) {
	ctx := context.Background()
	reg, _, deposits := newTestRegister(t)
	alice, _ := reg.OpenAccount(ctx, deposits, "Alice", testAsset, 0)

	assertNoError(t, reg.MarkDormant(ctx, alice.ID))
	got, _ := reg.GetAccount(ctx, alice.ID)
	assertEqual(t, "dormant", got.Status, Dormant)

	assertNoError(t, reg.Reactivate(ctx, alice.ID))
	got, _ = reg.GetAccount(ctx, alice.ID)
	assertEqual(t, "reactivated", got.Status, Active)
}

func TestClose_RequiresZeroBalance(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, deposits, "Alice", testAsset, 0)
	fund(t, reg, cash, alice, 10000)

	// Non-zero balance blocks close.
	assertError(t, reg.Close(ctx, alice.ID), ErrAccountNotEmpty)

	// Drain the balance, then close succeeds.
	h, _ := reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 10000})
	_, err := reg.CaptureHold(ctx, h.ID, cash, 10000, "Final withdrawal")
	assertNoError(t, err)
	assertNoError(t, reg.Close(ctx, alice.ID))

	got, _ := reg.GetAccount(ctx, alice.ID)
	assertEqual(t, "closed", got.Status, Closed)

	// Operations on a closed account report ErrAccountClosed.
	assertError(t, reg.CheckWithdrawal(ctx, alice.ID, 1), ErrAccountClosed)
	// Closed is terminal.
	assertError(t, reg.Close(ctx, alice.ID), ErrInvalidStatusTransition)
}

func TestIllegalStatusTransitions(t *testing.T) {
	ctx := context.Background()
	reg, _, deposits := newTestRegister(t)
	alice, _ := reg.OpenAccount(ctx, deposits, "Alice", testAsset, 0)

	// Cannot unfreeze an active account, nor reactivate one.
	assertError(t, reg.Unfreeze(ctx, alice.ID), ErrInvalidStatusTransition)
	assertError(t, reg.Reactivate(ctx, alice.ID), ErrInvalidStatusTransition)

	// Freeze, then a freeze again is illegal; dormant from frozen is illegal.
	assertNoError(t, reg.Freeze(ctx, alice.ID))
	assertError(t, reg.Freeze(ctx, alice.ID), ErrInvalidStatusTransition)
	assertError(t, reg.MarkDormant(ctx, alice.ID), ErrInvalidStatusTransition)

	// Status ops on a missing account.
	assertError(t, reg.Freeze(ctx, "nonexistent"), ErrAccountNotFound)
}

// ---------------------------------------------------------------------------
// Snapshots
// ---------------------------------------------------------------------------

func TestSnapshot_RoundTrip(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, deposits, "Alice", testAsset, 0)
	fund(t, reg, cash, alice, 10000)

	date := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	snap, err := reg.TakeEndOfDaySnapshot(ctx, alice.ID, date)
	assertNoError(t, err)
	assertEqual(t, "snapshot book", snap.Balance.Book, ledger.Amount(10000))

	got, err := reg.GetSnapshot(ctx, alice.ID, date)
	assertNoError(t, err)
	assertEqual(t, "retrieved book", got.Balance.Book, ledger.Amount(10000))

	// Missing date.
	other := time.Date(2025, 1, 16, 0, 0, 0, 0, time.UTC)
	_, err = reg.GetSnapshot(ctx, alice.ID, other)
	assertError(t, err, ErrSnapshotNotFound)

	// Missing account.
	_, err = reg.TakeEndOfDaySnapshot(ctx, "nonexistent", date)
	assertError(t, err, ErrAccountNotFound)
	_, err = reg.GetSnapshot(ctx, "nonexistent", date)
	assertError(t, err, ErrAccountNotFound)
}

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

// Deposit events share the store's audit log with the ledger's, so they must
// carry both the scope that distinguishes them and the book that owns them.
func TestAuditEventsAreScopedAndAttributedToTheBook(t *testing.T) {
	ctx := context.Background()
	reg, _, deposits := newTestRegister(t)

	alice, err := reg.OpenAccount(ctx, deposits, "Alice", testAsset, 0)
	assertNoError(t, err)

	events, err := reg.GetAuditLog(ctx)
	assertNoError(t, err)
	assertEqual(t, "deposit audit events", len(events), 1)
	assertEqual(t, "type", events[0].Type, ledger.EventAccountOpened)
	assertEqual(t, "scope", events[0].Scope, ledger.ScopeDeposit)
	assertEqual(t, "book", events[0].BookID, reg.BookID())
	assertEqual(t, "entity", events[0].EntityID, string(alice.ID))

	// The GL account this opened is a ledger-scope event on the same book, and
	// the two logs do not bleed into each other.
	glEvents, err := reg.Book().GetAuditLog(ctx)
	assertNoError(t, err)
	for _, e := range glEvents {
		assertEqual(t, "ledger log stays ledger-scoped", e.Scope, ledger.ScopeLedger)
	}
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

// ---------------------------------------------------------------------------
// Overdraft Terms
// ---------------------------------------------------------------------------

func TestSetOverdraftTerms_CreatesTheReceivableOnFirstRate(t *testing.T) {
	ctx := context.Background()
	reg, book, sub := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", "EUR", 0)
	assertNoError(t, err)
	if acct.InterestGL != "" {
		t.Fatalf("a new account already has a receivable: %s", acct.InterestGL)
	}

	// A limit on its own does not create one: an interest-free overdraft is a
	// real product, and an account that will never accrue should not carry an
	// account nothing posts to.
	withLimit, err := reg.SetOverdraftTerms(ctx, acct.ID, 50_000, 0, 0, interest.ACT365)
	assertNoError(t, err)
	assertEqual(t, "limit", withLimit.OverdraftLimit, ledger.Amount(50_000))
	if withLimit.InterestGL != "" {
		t.Errorf("a zero rate created a receivable: %s", withLimit.InterestGL)
	}

	// A rate does.
	priced, err := reg.SetOverdraftTerms(ctx, acct.ID, 50_000, 150_000, 350_000, interest.Thirty360)
	assertNoError(t, err)
	if priced.InterestGL == "" {
		t.Fatal("a non-zero rate created no receivable")
	}
	assertEqual(t, "rate", priced.Rate, interest.Rate(150_000))
	assertEqual(t, "unarranged rate", priced.UnarrangedRate, interest.Rate(350_000))
	assertEqual(t, "day count", priced.DayCount, interest.Thirty360)

	// The receivable is an Asset in the account's own asset.
	gl, err := book.GetAccount(ctx, priced.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable type", gl.Type.String(), ledger.Asset.String())
	assertEqual(t, "receivable asset", string(gl.Asset), "EUR")

	// Setting terms again reuses it rather than opening a second one.
	again, err := reg.SetOverdraftTerms(ctx, acct.ID, 60_000, 160_000, 350_000, interest.Thirty360)
	assertNoError(t, err)
	assertEqual(t, "receivable reused", string(again.InterestGL), string(priced.InterestGL))
}

func TestSetOverdraftTerms_Rejects(t *testing.T) {
	ctx := context.Background()
	reg, _, sub := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", "EUR", 0)
	assertNoError(t, err)

	_, err = reg.SetOverdraftTerms(ctx, acct.ID, -1, 0, 0, interest.ACT365)
	assertError(t, err, ErrInvalidAmount)

	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 50_000, -1, 0, interest.ACT365)
	assertError(t, err, ErrInvalidRate)

	// An unarranged rate with no arranged rate is not a product.
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 50_000, 0, 350_000, interest.ACT365)
	assertError(t, err, ErrInvalidRate)

	_, err = reg.SetOverdraftTerms(ctx, "nonexistent", 0, 0, 0, interest.ACT365)
	assertError(t, err, ErrAccountNotFound)

	// A closed account takes no new terms.
	closed, err := reg.OpenAccount(ctx, sub, "Gone", "EUR", 0)
	assertNoError(t, err)
	assertNoError(t, reg.Close(ctx, closed.ID))
	_, err = reg.SetOverdraftTerms(ctx, closed.ID, 50_000, 150_000, 0, interest.ACT365)
	assertError(t, err, ErrAccountClosed)
}

// overdrawBy pushes an account to a negative book balance by posting a debit
// straight to the general ledger, which is how a payment reaches it too.
func overdrawBy(t *testing.T, book *ledger.Book, sub ledger.SubledgerID, acct Account, amount ledger.Amount) {
	t.Helper()
	ctx := context.Background()
	counterparty, err := book.CreateAccount(ctx, sub, "Counterparty "+string(acct.ID), ledger.Liability, acct.Asset)
	if err != nil {
		t.Fatalf("counterparty: %v", err)
	}
	if _, err := book.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "overdraw",
		Entries: []ledger.Entry{
			{AccountID: acct.GLAccount, Amount: amount, Direction: ledger.Debit},
			{AccountID: counterparty.ID, Amount: amount, Direction: ledger.Credit},
		},
	}); err != nil {
		t.Fatalf("overdraw: %v", err)
	}
}

// fundBy posts the mirror-image transaction to overdrawBy: it credits the
// account and debits an Asset counterparty, the shape a customer deposit
// takes.
func fundBy(t *testing.T, book *ledger.Book, sub ledger.SubledgerID, acct Account, amount ledger.Amount) {
	t.Helper()
	ctx := context.Background()
	counterparty, err := book.CreateAccount(ctx, sub, "Counterparty "+string(acct.ID), ledger.Asset, acct.Asset)
	if err != nil {
		t.Fatalf("counterparty: %v", err)
	}
	if _, err := book.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "fund",
		Entries: []ledger.Entry{
			{AccountID: counterparty.ID, Amount: amount, Direction: ledger.Debit},
			{AccountID: acct.GLAccount, Amount: amount, Direction: ledger.Credit},
		},
	}); err != nil {
		t.Fatalf("fund: %v", err)
	}
}

func TestAccrueOverdraft_PostsTheDeltaOfTheRoundedValue(t *testing.T) {
	ctx := context.Background()
	reg, book, sub := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", "EUR", 0)
	assertNoError(t, err)
	acct, err = reg.SetOverdraftTerms(ctx, acct.ID, 50_000, 150_000, 0, interest.ACT365)
	assertNoError(t, err)

	// €50 overdrawn at 15% ACT/365 accrues 2.054794 cents a day.
	overdrawBy(t, book, sub, acct, 5_000)

	day := func(n int) time.Time {
		return time.Date(2025, time.January, 15+n, 0, 0, 0, 0, time.UTC)
	}
	// Day 0 is the account's starting point; nothing accrues over zero days.
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(0)))

	// The GL always holds Minor() of the record, so the posted amount is the
	// change in the rounding, not the day's exact interest.
	wantGL := []ledger.Amount{2, 4, 6, 8, 10}
	wantAccrued := []interest.Accrued{2_054_794, 4_109_588, 6_164_382, 8_219_176, 10_273_970}
	for i := 1; i <= 5; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(i)))

		got, err := reg.GetAccount(ctx, acct.ID)
		assertNoError(t, err)
		assertEqual(t, "accrued on record", got.Accrued, wantAccrued[i-1])

		balance, err := book.BookBalance(ctx, got.InterestGL)
		assertNoError(t, err)
		assertEqual(t, "receivable balance", balance, wantGL[i-1])
		if balance != got.Accrued.Minor() {
			t.Fatalf("day %d: GL %d != Minor(accrued) %d", i, balance, got.Accrued.Minor())
		}
	}
}

func TestAccrueOverdraft_IsIdempotentPerDate(t *testing.T) {
	ctx := context.Background()
	reg, book, sub := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", "EUR", 0)
	assertNoError(t, err)
	acct, err = reg.SetOverdraftTerms(ctx, acct.ID, 50_000, 150_000, 0, interest.ACT365)
	assertNoError(t, err)
	overdrawBy(t, book, sub, acct, 5_000)

	start := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, 1)))

	after, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)

	// The same date again, and a date before it, are both no-ops: an
	// end-of-day batch re-run must not charge a second day.
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, 1)))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start))

	again, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued after re-runs", again.Accrued, after.Accrued)
	if !again.LastAccrualDate.Equal(after.LastAccrualDate) {
		t.Errorf("last accrual date moved backwards: %v then %v", after.LastAccrualDate, again.LastAccrualDate)
	}
}

func TestAccrueOverdraft_TiersAtTheLimit(t *testing.T) {
	ctx := context.Background()
	reg, book, sub := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", "EUR", 0)
	assertNoError(t, err)
	// Arranged 12% up to €100, unarranged 36% beyond it.
	acct, err = reg.SetOverdraftTerms(ctx, acct.ID, 10_000, 120_000, 360_000, interest.ACT360)
	assertNoError(t, err)

	// €150 drawn: €100 arranged, €50 unarranged.
	overdrawBy(t, book, sub, acct, 15_000)

	start := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, 1)))

	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)

	// 10_000 × 120_000 / 360 = 3_333_333, plus 5_000 × 360_000 / 360 = 5_000_000.
	assertEqual(t, "tiered accrual", got.Accrued, interest.Accrued(8_333_333))
}

// TestAccrueOverdraft_ExcessFallsBackToTheArrangedRate covers the configuration
// most accounts here are opened with: an arranged rate and NO unarranged one.
// An unarranged rate is a surcharge, so its absence must mean the same rate
// applies throughout — never that the part beyond the limit is free, which
// would make exceeding a limit cheaper than respecting it.
func TestAccrueOverdraft_ExcessFallsBackToTheArrangedRate(t *testing.T) {
	ctx := context.Background()
	reg, book, sub := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", "EUR", 0)
	assertNoError(t, err)
	// Arranged 12% up to €100, and no unarranged rate at all.
	acct, err = reg.SetOverdraftTerms(ctx, acct.ID, 10_000, 120_000, 0, interest.ACT360)
	assertNoError(t, err)

	// €150 drawn: €100 inside the limit, €50 beyond it.
	overdrawBy(t, book, sub, acct, 15_000)

	start := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, 1)))

	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)

	// The WHOLE €150 accrues at 12%. It is still computed tier by tier, and
	// Accrue truncates each one, so the figure is one micro-minor-unit under
	// the single-base 5_000_000:
	//
	//	arranged 10_000 × 120_000 / 360 = 3_333_333.33 -> 3_333_333
	//	excess    5_000 × 120_000 / 360 = 1_666_666.67 -> 1_666_666
	//	                                                 = 4_999_999
	//
	// Skipping the excess would leave 3_333_333 — the €50 beyond the limit
	// costing nothing at all.
	assertEqual(t, "excess accrual at the arranged rate", got.Accrued, interest.Accrued(4_999_999))
}

func TestAccrueOverdraft_IgnoresHoldsAndCredits(t *testing.T) {
	ctx := context.Background()
	reg, book, sub := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Alice", "EUR", 0)
	assertNoError(t, err)
	acct, err = reg.SetOverdraftTerms(ctx, acct.ID, 50_000, 150_000, 0, interest.ACT365)
	assertNoError(t, err)

	// In credit, with a hold that takes available below zero. Interest accrues
	// on the book balance, because a hold is not borrowed money.
	fundBy(t, book, sub, acct, 1_000)
	_, err = reg.CreateHold(ctx, CreateHoldRequest{AccountID: acct.ID, Amount: 900, Description: "auth"})
	assertNoError(t, err)

	start := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, 1)))

	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued while in credit", got.Accrued, interest.Accrued(0))
}

func TestAccrueOverdraft_NoRateAccruesNothing(t *testing.T) {
	ctx := context.Background()
	reg, book, sub := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", "EUR", 50_000)
	assertNoError(t, err)
	overdrawBy(t, book, sub, acct, 5_000)

	start := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, 1)))

	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued with no rate", got.Accrued, interest.Accrued(0))
	assertEqual(t, "receivable still absent", string(got.InterestGL), "")
}

func TestChargeOverdraftInterest_CapitalizesAndLeavesANegativeResidue(t *testing.T) {
	ctx := context.Background()
	reg, book, sub := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", "EUR", 0)
	assertNoError(t, err)
	acct, err = reg.SetOverdraftTerms(ctx, acct.ID, 50_000, 150_000, 0, interest.ACT365)
	assertNoError(t, err)
	overdrawBy(t, book, sub, acct, 5_000)

	start := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= 30; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, i)))
	}

	before, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	// 30 days × 2.054794 cents = 61.643820 cents, which rounds to 62.
	assertEqual(t, "accrued over 30 days", before.Accrued, interest.Accrued(61_643_820))
	assertEqual(t, "receivable before charging", before.Accrued.Minor(), ledger.Amount(62))

	charged, err := reg.ChargeOverdraftInterest(ctx, acct.ID, start.AddDate(0, 0, 30))
	assertNoError(t, err)
	if len(charged.Entries) != 2 {
		t.Fatalf("charge posted %d entries, want 2", len(charged.Entries))
	}

	after, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)

	// 62 was charged and 61.64382 was earned, so the record is left NEGATIVE by
	// the rounding-up. That is correct: Minor() of it is still 0, so the
	// receivable and the record agree, and tomorrow's accrual absorbs it.
	assertEqual(t, "residue after charging", after.Accrued, interest.Accrued(-356_180))
	assertEqual(t, "residue rounds to zero", after.Accrued.Minor(), ledger.Amount(0))

	receivable, err := book.BookBalance(ctx, after.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable cleared", receivable, ledger.Amount(0))

	// The interest is now part of what the customer owes, which is what makes
	// an overdraft compound: −5000 − 62.
	balance, err := reg.GetBalance(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "book balance after charging", balance.Book, ledger.Amount(-5_062))

	// The invariant survives the residue: keep accruing and the GL still
	// tracks Minor() of the record exactly.
	for i := 31; i <= 33; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, i)))
		got, err := reg.GetAccount(ctx, acct.ID)
		assertNoError(t, err)
		gl, err := book.BookBalance(ctx, got.InterestGL)
		assertNoError(t, err)
		if gl != got.Accrued.Minor() {
			t.Fatalf("day %d after charging: GL %d != Minor(accrued) %d", i, gl, got.Accrued.Minor())
		}
	}
}

func TestChargeOverdraftInterest_NothingAccruedPostsNothing(t *testing.T) {
	ctx := context.Background()
	reg, _, sub := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Alice", "EUR", 0)
	assertNoError(t, err)
	acct, err = reg.SetOverdraftTerms(ctx, acct.ID, 50_000, 150_000, 0, interest.ACT365)
	assertNoError(t, err)

	txn, err := reg.ChargeOverdraftInterest(ctx, acct.ID, time.Date(2025, time.February, 1, 0, 0, 0, 0, time.UTC))
	assertNoError(t, err)
	if txn.ID != "" {
		t.Errorf("charging nothing posted transaction %s", txn.ID)
	}
}

// TestChargeOverdraftInterest_RefusesClosedAccount pins the guard on an
// explicitly-invoked charge: posting a debit to an account CloseTx only let
// through at zero would reopen a balance on it. Reaching a closed account with
// an overdraft history takes the whole flow — charge, repay, close — because
// CloseTx now refuses an account whose receivable is still outstanding.
func TestChargeOverdraftInterest_RefusesClosedAccount(t *testing.T) {
	ctx := context.Background()
	reg, book, sub := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Carla", "EUR", 0)
	assertNoError(t, err)
	acct, err = reg.SetOverdraftTerms(ctx, acct.ID, 50_000, 150_000, 0, interest.ACT365)
	assertNoError(t, err)
	overdrawBy(t, book, sub, acct, 5_000)

	start := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= 5; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, i)))
	}

	accrued, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	if accrued.Accrued <= 0 {
		t.Fatalf("expected nonzero accrued interest before closing, got %d", accrued.Accrued)
	}

	// Charge the receivable into the balance, then repay the whole balance —
	// principal and capitalized interest alike. 5 days × 2.054794 cents =
	// 10.27397, charged as 10, so the account owes 5,010.
	if _, err := reg.ChargeOverdraftInterest(ctx, acct.ID, start.AddDate(0, 0, 5)); err != nil {
		t.Fatalf("ChargeOverdraftInterest: %v", err)
	}
	fundBy(t, book, sub, acct, 5_010)
	assertNoError(t, reg.Close(ctx, acct.ID))

	_, err = reg.ChargeOverdraftInterest(ctx, acct.ID, start.AddDate(0, 0, 6))
	assertError(t, err, ErrAccountClosed)
}

// TestClose_RefusesAStrandedReceivable is the guard that test now depends on.
// An account that was overdrawn, accrued interest and repaid its principal has
// a zero book balance and a live receivable: closing there would strand
// recognized interest income as a debit in an Asset account nothing can ever
// clear, because accrual afterwards skips a closed account and charging one is
// refused outright. lending.CloseTx applies the same rule to a facility.
func TestClose_RefusesAStrandedReceivable(t *testing.T) {
	ctx := context.Background()
	reg, book, sub := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Carla", "EUR", 0)
	assertNoError(t, err)
	acct, err = reg.SetOverdraftTerms(ctx, acct.ID, 50_000, 150_000, 0, interest.ACT365)
	assertNoError(t, err)
	overdrawBy(t, book, sub, acct, 5_000)

	start := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= 5; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, i)))
	}

	// The customer account's own balance is back to zero, which is everything
	// the old guard checked.
	fundBy(t, book, sub, acct, 5_000)
	balance, err := reg.GetBalance(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "book balance before closing", balance.Book, ledger.Amount(0))

	before, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "receivable before closing", before.Accrued.Minor(), ledger.Amount(10))
	assertError(t, reg.Close(ctx, acct.ID), ErrAccountNotEmpty)

	// Charge, repay the capitalized 10, and the same close succeeds: the flow
	// is charge -> repay -> close, exactly as lending already requires.
	if _, err := reg.ChargeOverdraftInterest(ctx, acct.ID, start.AddDate(0, 0, 5)); err != nil {
		t.Fatalf("ChargeOverdraftInterest: %v", err)
	}
	fundBy(t, book, sub, acct, 10)

	// The residue a capitalization leaves — here 10.27397 earned against 10
	// charged — is not collectable and is not in the ledger, so it must not
	// block a close. Only Minor() does.
	after, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "residue after charging", after.Accrued, interest.Accrued(273_970))
	assertEqual(t, "residue rounds to zero", after.Accrued.Minor(), ledger.Amount(0))
	assertNoError(t, reg.Close(ctx, acct.ID))
}

// TestClose_SucceedsOnAnExactHalfMinorUnitResidue pins the one residue
// Accrued.Minor() cannot represent as settled: exactly half a minor unit.
// Minor() rounds half AWAY from zero, so Minor(500_000) is 1 and
// Minor(-500_000) is -1 — never 0 — even though the receivable itself is
// fully cleared. If CloseTx tested the record instead of the receivable's own
// ledger balance, an account that ever lands on this exact residue could
// never be closed again: once its balance stops moving, further accrual adds
// nothing and the residue never resolves.
//
// €18.25 overdrawn at 10% (ACT/365), for exactly one day:
//
//	1_825 × 100_000 × 1 / 365 = 500_000 micro-minor-units, exactly half a cent.
//
// Minor(500_000) = 1, so charging capitalizes 1 cent and leaves the record at
// 500_000 − 1_000_000 = −500_000. Minor(−500_000) = −1: the old guard's test
// is nonzero, but the receivable — credited by the same 1 cent that was
// debited into it during accrual — is back to zero.
func TestClose_SucceedsOnAnExactHalfMinorUnitResidue(t *testing.T) {
	ctx := context.Background()
	reg, book, sub := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Dana", "EUR", 0)
	assertNoError(t, err)
	acct, err = reg.SetOverdraftTerms(ctx, acct.ID, 2_000, 100_000, 0, interest.ACT365)
	assertNoError(t, err)
	overdrawBy(t, book, sub, acct, 1_825)

	start := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, 1)))

	before, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued exactly half a minor unit", before.Accrued, interest.Accrued(500_000))

	charged, err := reg.ChargeOverdraftInterest(ctx, acct.ID, start.AddDate(0, 0, 1))
	assertNoError(t, err)
	if len(charged.Entries) != 2 {
		t.Fatalf("charge posted %d entries, want 2", len(charged.Entries))
	}

	after, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "residue at exactly minus half a minor unit", after.Accrued, interest.Accrued(-500_000))
	assertEqual(t, "residue rounds AWAY from zero, not to it", after.Accrued.Minor(), ledger.Amount(-1))

	receivable, err := book.BookBalance(ctx, after.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable is fully cleared despite the nonzero residue", receivable, ledger.Amount(0))

	// Repay the 1,825 principal plus the capitalized cent: 1,826 in total.
	fundBy(t, book, sub, acct, 1_826)
	balance, err := reg.GetBalance(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "book balance before closing", balance.Book, ledger.Amount(0))

	// The old guard (Accrued.Minor() != 0) would refuse this close forever.
	// The fixed guard reads the receivable's own ledger balance instead, which
	// is zero, and lets it through.
	assertNoError(t, reg.Close(ctx, acct.ID))
}

func TestRunEndOfDay_AccruesEveryOverdrawnAccount(t *testing.T) {
	ctx := context.Background()
	reg, book, sub := newTestRegister(t)

	overdrawn, err := reg.OpenAccount(ctx, sub, "Bruno", "EUR", 0)
	assertNoError(t, err)
	overdrawn, err = reg.SetOverdraftTerms(ctx, overdrawn.ID, 50_000, 150_000, 0, interest.ACT365)
	assertNoError(t, err)
	overdrawBy(t, book, sub, overdrawn, 5_000)

	inCredit, err := reg.OpenAccount(ctx, sub, "Alice", "EUR", 0)
	assertNoError(t, err)
	inCredit, err = reg.SetOverdraftTerms(ctx, inCredit.ID, 50_000, 150_000, 0, interest.ACT365)
	assertNoError(t, err)
	fundBy(t, book, sub, inCredit, 20_000)

	unpriced, err := reg.OpenAccount(ctx, sub, "Bella", "EUR", 50_000)
	assertNoError(t, err)
	overdrawBy(t, book, sub, unpriced, 5_000)

	start := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	assertNoError(t, reg.RunEndOfDay(ctx, start))
	assertNoError(t, reg.RunEndOfDay(ctx, start.AddDate(0, 0, 1)))

	got, err := reg.GetAccount(ctx, overdrawn.ID)
	assertNoError(t, err)
	assertEqual(t, "overdrawn accrued", got.Accrued, interest.Accrued(2_054_794))

	quiet, err := reg.GetAccount(ctx, inCredit.ID)
	assertNoError(t, err)
	assertEqual(t, "in-credit accrued", quiet.Accrued, interest.Accrued(0))

	free, err := reg.GetAccount(ctx, unpriced.ID)
	assertNoError(t, err)
	assertEqual(t, "unpriced accrued", free.Accrued, interest.Accrued(0))

	// A second run for the same date changes nothing.
	assertNoError(t, reg.RunEndOfDay(ctx, start.AddDate(0, 0, 1)))
	again, err := reg.GetAccount(ctx, overdrawn.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued after a re-run", again.Accrued, got.Accrued)
}

// This test is the design, not a detail of it. An overdrawn current account is
// a loan, and it reaches the Asset side of the balance sheet by AGGREGATION,
// not by a posting: its drawn amount is the negative balance of its own
// Liability account viewed by sign, and has no independent existence. A future
// author who adds a reclassification journal or a sweep will fail here.
func TestTotals_OverdraftsAreDerivedAndNothingIsPosted(t *testing.T) {
	ctx := context.Background()
	reg, book, sub := newTestRegister(t)

	bruno, err := reg.OpenAccount(ctx, sub, "Bruno", "EUR", 50_000)
	assertNoError(t, err)
	alice, err := reg.OpenAccount(ctx, sub, "Alice", "EUR", 0)
	assertNoError(t, err)
	satoshi, err := reg.OpenAccount(ctx, sub, "Satoshi", "BTC", 0)
	assertNoError(t, err)

	fundBy(t, book, sub, alice, 20_000)
	fundBy(t, book, sub, satoshi, 500_000)
	overdrawBy(t, book, sub, bruno, 5_000)

	// Run end-of-day before asserting anything. RunEndOfDayTx already loops
	// every deposit account nightly, which is exactly where a future author
	// would be tempted to add a reclassification posting or a sweep of drawn
	// balances into an Asset account — the doc comment above even names "a
	// nightly feed" as the real-bank analogue. None of these accounts has a
	// non-zero Rate, so this must accrue and post nothing; if it does, that
	// is a real finding, not something to relax below. Do not remove this
	// call as unnecessary: it is what makes the EOD path a place this test
	// actually pins, rather than one it happens to be silent about.
	assertNoError(t, reg.RunEndOfDay(ctx, time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)))

	// Overdrawing posted exactly one transaction against Bruno's account, and
	// both of its legs are Liability: the debit to Bruno, the credit to the
	// counterparty. No Asset account was touched, and no second transaction
	// reclassified anything.
	txs, err := book.ListTransactionsForAccount(ctx, bruno.GLAccount)
	assertNoError(t, err)
	assertEqual(t, "transactions against an overdrawn account", len(txs), 1)
	for _, e := range txs[0].Entries {
		gl, err := book.GetAccount(ctx, e.AccountID)
		assertNoError(t, err)
		if gl.Type == ledger.Asset {
			t.Fatalf("overdrawing posted to an Asset account (%s): the drawn amount "+
				"has no independent existence and must not be stored", gl.Name)
		}
	}

	totals, err := reg.Totals(ctx)
	assertNoError(t, err)

	// Per asset, because summing euro and bitcoin is not a number.
	assertEqual(t, "EUR deposits", totals.Deposits["EUR"], ledger.Amount(20_000))
	assertEqual(t, "EUR overdrafts", totals.Overdrafts["EUR"], ledger.Amount(5_000))
	assertEqual(t, "BTC deposits", totals.Deposits["BTC"], ledger.Amount(500_000))
	assertEqual(t, "BTC overdrafts", totals.Overdrafts["BTC"], ledger.Amount(0))

	// And the aggregate really is the sum of the negative balances, rather
	// than a number maintained alongside them.
	var drawn ledger.Amount
	accounts, err := reg.ListAccounts(ctx)
	assertNoError(t, err)
	for _, a := range accounts {
		if a.Asset != "EUR" {
			continue
		}
		bal, err := reg.GetBalance(ctx, a.ID)
		assertNoError(t, err)
		if bal.Book < 0 {
			drawn += -bal.Book
		}
	}
	assertEqual(t, "derived equals the sum of negative balances", totals.Overdrafts["EUR"], drawn)
}

// ---------------------------------------------------------------------------
// Value-dated overdraft accrual
// ---------------------------------------------------------------------------

// accrualStart is the day the value-dated accrual tests open their terms window
// on. It is a midnight because a window opens at the clock's instant, and these
// tests want that instant to be a business date.
var accrualStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// postTo moves amount against acct's backing GL account at the given value
// date, with a fresh counterparty as the contra leg — overdrawBy and fundBy
// with the direction and the value date spelled out, which is what a backdated
// posting needs. The contra is typed so the book keeps its classification: a
// debit to the customer is funded by another Liability, a credit to them by an
// Asset, exactly as those two helpers already do.
func postTo(t *testing.T, book *ledger.Book, sub ledger.SubledgerID, acct Account, amount ledger.Amount, dir ledger.Direction, value time.Time) {
	t.Helper()
	ctx := context.Background()
	contraType, other := ledger.Liability, ledger.Credit
	if dir == ledger.Credit {
		contraType, other = ledger.Asset, ledger.Debit
	}
	contra, err := book.CreateAccount(ctx, sub, "Counterparty "+string(acct.ID), contraType, acct.Asset)
	if err != nil {
		t.Fatalf("counterparty: %v", err)
	}
	if _, err := book.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "test movement",
		ValueDate:   value,
		Entries: []ledger.Entry{
			{AccountID: acct.GLAccount, Amount: amount, Direction: dir},
			{AccountID: contra.ID, Amount: amount, Direction: other},
		},
	}); err != nil {
		t.Fatalf("post: %v", err)
	}
}

// newOverdraftAccount opens a priced overdraft account on a movable clock, with
// the window opening at accrualStart: EUR 500 arranged at 15% ACT/365.
func newOverdraftAccount(t *testing.T) (*Register, *ledger.Book, ledger.SubledgerID, Account, *mutableClock) {
	t.Helper()
	ctx := context.Background()
	clock := &mutableClock{}
	clock.set(accrualStart)
	reg, book, sub := newTestRegisterOn(t, clock.now)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 0)
	assertNoError(t, err)
	acct, err = reg.SetOverdraftTerms(ctx, acct.ID, 50_000, 150_000, 0, interest.ACT365)
	assertNoError(t, err)
	return reg, book, sub, acct, clock
}

func TestOverdraftAccrualCorrectsABackdatedCredit(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, acct, _ := newOverdraftAccount(t)

	// Overdrawn by EUR 200 on day 1, then accrue ten days.
	postTo(t, book, sub, acct, 20_000, ledger.Debit, accrualStart)
	for i := 1; i <= 10; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, i)))
	}
	ten, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	after := ten.Accrued.Minor()
	if after <= 0 {
		t.Fatalf("accrued nothing over ten overdrawn days: %d", after)
	}

	// A salary credit arrives on day 11, backdated to day 3: from day 3 the
	// account was never overdrawn, so most of that interest was never owed.
	postTo(t, book, sub, acct, 20_000, ledger.Credit, accrualStart.AddDate(0, 0, 3))

	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, 11)))
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	corrected := got.Accrued.Minor()
	if corrected >= after {
		t.Errorf("accrued after backdated credit = %d, want less than %d", corrected, after)
	}

	// And the receivable's ledger balance agrees with the record.
	recv, err := book.BookBalance(ctx, got.InterestGL)
	assertNoError(t, err)
	if recv != corrected {
		t.Errorf("receivable = %d, accrued = %d; the two must agree", recv, corrected)
	}
}

func TestOverdraftAccrualCorrectsABackdatedDebit(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, acct, _ := newOverdraftAccount(t)

	postTo(t, book, sub, acct, 20_000, ledger.Debit, accrualStart)
	for i := 1; i <= 10; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, i)))
	}
	ten, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	after := ten.Accrued.Minor()

	// A card settlement lands late, backdated to day 3: he was more overdrawn
	// than the ledger knew.
	postTo(t, book, sub, acct, 20_000, ledger.Debit, accrualStart.AddDate(0, 0, 3))

	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, 11)))
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	if corrected := got.Accrued.Minor(); corrected <= after {
		t.Errorf("accrued after backdated debit = %d, want more than %d", corrected, after)
	}

	// "More than before" is not enough: simply accruing one more day on today's
	// balance is also more than before, which is exactly what the incrementing
	// version this replaced would do — 82 cents plus a day at −400 is 99. The
	// recompute goes back and re-derives days 3 to 11 at the balance the
	// backdated debit gave them:
	//
	//	days 1-2   at −200   2 × 8_219_178  =  16_438_356
	//	days 3-11  at −400   9 × 16_438_356 = 147_945_204
	//	                                    = 164_383_560 -> 164 cents
	//
	// 164 is the figure only a recompute reaches.
	assertEqual(t, "accrued after backdated debit", got.Accrued, interest.Accrued(164_383_560))

	// And the receivable really holds it: the ledger balance, not Accrued.Minor()
	// restated. Reading the GL account is what proves the recompute's delta was
	// posted rather than only recorded on the account row.
	receivable, err := book.BookBalance(ctx, got.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable after backdated debit", receivable, ledger.Amount(164))
}

func TestOverdraftAccrualIgnoresAForwardValueDatedDebit(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, acct, _ := newOverdraftAccount(t)

	// Booked today, value-dated five days out. It has not taken effect yet.
	postTo(t, book, sub, acct, 20_000, ledger.Debit, accrualStart.AddDate(0, 0, 5))

	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, 1)))
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	if got.Accrued != 0 {
		t.Errorf("accrued = %d on a debit that has not taken effect, want 0", got.Accrued)
	}
}

func TestOverdraftAccrualFreezesPriorAccrualOnARepricing(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, acct, clock := newOverdraftAccount(t)

	postTo(t, book, sub, acct, 20_000, ledger.Debit, accrualStart)
	for i := 1; i <= 10; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, i)))
	}
	ten, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	atReprice := ten.Accrued.Minor()

	// Reprice to triple the rate on day 10, then look at the window.
	clock.set(accrualStart.AddDate(0, 0, 10))
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 50_000, 450_000, 0, interest.ACT365)
	assertNoError(t, err)

	after, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	if after.AccruedGross != 0 {
		t.Errorf("AccruedGross = %d after repricing, want 0 (a new window)", after.AccruedGross)
	}
	if after.Accrued.Minor() != atReprice {
		t.Errorf("accrued = %d after repricing, want %d unchanged; prior accrual is frozen, not rewritten",
			after.Accrued.Minor(), atReprice)
	}
}

func TestOverdraftCorrectionRefundsWhatTheReceivableCannotAbsorb(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, acct, _ := newOverdraftAccount(t)

	postTo(t, book, sub, acct, 20_000, ledger.Debit, accrualStart)
	for i := 1; i <= 30; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, i)))
	}
	// Capitalise, emptying the receivable.
	_, err := reg.ChargeOverdraftInterest(ctx, acct.ID, accrualStart.AddDate(0, 0, 30))
	assertNoError(t, err)
	charged, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	recvAfterCharge, err := book.BookBalance(ctx, charged.InterestGL)
	assertNoError(t, err)
	if recvAfterCharge != 0 {
		t.Fatalf("receivable after capitalisation = %d, want 0", recvAfterCharge)
	}
	balBefore, err := book.BookBalance(ctx, acct.GLAccount)
	assertNoError(t, err)

	// Now say he was never overdrawn at all: a credit backdated to day 1.
	postTo(t, book, sub, acct, 20_000, ledger.Credit, accrualStart)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, 31)))

	recv, err := book.BookBalance(ctx, charged.InterestGL)
	assertNoError(t, err)
	if recv < 0 {
		t.Errorf("receivable = %d; the correction must clamp rather than drive an asset negative", recv)
	}
	balAfter, err := book.BookBalance(ctx, acct.GLAccount)
	assertNoError(t, err)
	if balAfter <= balBefore {
		t.Errorf("customer balance %d -> %d; the unabsorbed correction must be refunded to them", balBefore, balAfter)
	}

	// The refunded part left in cash, so it must leave the record too. If it
	// does not, the account carries a permanent negative that the customer gets
	// the benefit of twice: the next interest genuinely owed is swallowed
	// paying it off, and at a tenth of a cent a day it never unwinds. This is
	// the same assertion the unclamped sibling above makes, and it is the one
	// that catches it.
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	if recv != got.Accrued.Minor() {
		t.Errorf("receivable = %d, accrued = %d; a refund that stays on the record is money given back twice",
			recv, got.Accrued.Minor())
	}

	// And the account goes on working. Overdraw him again and accrue: the
	// receivable and the record must still agree afterwards. With the refund
	// left on the record they do not — the delta is a difference of rounded
	// values, so the posting still lands, but the record it is supposed to
	// mirror is 247 cents adrift and stays that way. api/dto_deposit.go
	// reports that record straight through.
	postTo(t, book, sub, acct, 20_000, ledger.Debit, accrualStart.AddDate(0, 0, 32))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, 33)))
	next, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	nextRecv, err := book.BookBalance(ctx, next.InterestGL)
	assertNoError(t, err)
	if nextRecv <= 0 {
		t.Errorf("receivable = %d after two freshly overdrawn days, want it to have moved", nextRecv)
	}
	if nextRecv != next.Accrued.Minor() {
		t.Errorf("receivable = %d, accrued = %d; the refund was left on the record", nextRecv, next.Accrued.Minor())
	}
}

// TestOverdraftRepricingChargesTheOutgoingTermsFirst pins the day a repricing
// used to swallow. Freezing the old window and opening the new one at the same
// instant leaves the span since the last end-of-day belonging to neither: the
// old window's AccruedGross stops where its last run left it, and the new one
// starts today.
func TestOverdraftRepricingChargesTheOutgoingTermsFirst(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, acct, clock := newOverdraftAccount(t)

	postTo(t, book, sub, acct, 20_000, ledger.Debit, accrualStart)
	for i := 1; i <= 9; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, i)))
	}
	nine, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	// Nine days at 15% on €200: 9 × 8_219_178.
	assertEqual(t, "accrued through day 9", nine.Accrued, interest.Accrued(73_972_602))

	// Reprice to triple the rate on day 10, with day 10 not yet accrued.
	clock.set(accrualStart.AddDate(0, 0, 10))
	_, err = reg.SetOverdraftTerms(ctx, acct.ID, 50_000, 450_000, 0, interest.ACT365)
	assertNoError(t, err)

	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)

	// Day 10 belongs to the OUTGOING terms and must be charged at them before
	// the window moves. Three outcomes are distinguishable here, which is the
	// point of tripling the rate rather than nudging it:
	//
	//	73_972_602  the day was dropped between the two windows
	//	82_191_780  charged at the outgoing 15%   <- correct
	//	98_630_136  charged at the incoming 45%
	assertEqual(t, "accrued after repricing", got.Accrued, interest.Accrued(82_191_780))

	receivable, err := book.BookBalance(ctx, got.InterestGL)
	assertNoError(t, err)
	assertEqual(t, "receivable after repricing", receivable, ledger.Amount(82))
	if receivable != got.Accrued.Minor() {
		t.Errorf("receivable %d != Minor(accrued) %d across a repricing", receivable, got.Accrued.Minor())
	}

	// And the new window prices at the new rate: a day at 45% is 24_657_534.
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, 11)))
	after, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued a day into the new window", after.Accrued, interest.Accrued(106_849_314))
}

// TestOverdraftRepricingDoesNotRewindTheAccrualWindow pins the other direction
// of the same seam: a repricing that happens while LastAccrualDate is already
// AHEAD of the wall clock.
//
// End-of-day is callable for any date — POST /participants/{pid}/end-of-day
// takes the date from the request body — so a run for a date in the future is
// reachable, and it charges the whole span up to that date. Opening the new
// terms window at the wall clock would then put TermsEffectiveFrom and
// LastAccrualDate BEHIND a span already charged, with AccruedGross reset to
// zero; every subsequent run would recompute that overlap from scratch and add
// it to Accrued a second time. Account.LastAccrualDate documents that it never
// moves backwards, and this is the test of that claim on this path.
func TestOverdraftRepricingDoesNotRewindTheAccrualWindow(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, acct, clock := newOverdraftAccount(t)

	postTo(t, book, sub, acct, 20_000, ledger.Debit, accrualStart)

	// An end-of-day run for day 100, while the clock still reads day 0. One
	// hundred days at 15% on €200: 100 × 8_219_178 = 821_917_800 -> 821 cents.
	ahead := accrualStart.AddDate(0, 0, 100)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, ahead))
	charged, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued through day 100", charged.Accrued, interest.Accrued(821_917_800))

	// Reprice at the wall clock, which is a hundred days behind what has been
	// charged. The span is already accrued, so the outgoing-terms close is a
	// no-op — but the window must not reopen behind it.
	clock.set(accrualStart)
	repriced, err := reg.SetOverdraftTerms(ctx, acct.ID, 50_000, 450_000, 0, interest.ACT365)
	assertNoError(t, err)

	if repriced.LastAccrualDate.Before(ahead) {
		t.Errorf("LastAccrualDate rewound to %s, want no earlier than %s",
			repriced.LastAccrualDate, ahead)
	}
	if repriced.TermsEffectiveFrom.Before(ahead) {
		t.Errorf("TermsEffectiveFrom opened at %s, behind the charged-through date %s",
			repriced.TermsEffectiveFrom, ahead)
	}
	assertEqual(t, "accrued across the repricing", repriced.Accrued, interest.Accrued(821_917_800))

	// The proof it is not double-charged: one more day at the new 45% rate adds
	// exactly one day, not a hundred and one. €200 at 45%: 24_657_534.
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, ahead.AddDate(0, 0, 1)))
	after, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued a day past the repricing", after.Accrued, interest.Accrued(846_575_334))

	receivable, err := book.BookBalance(ctx, after.InterestGL)
	assertNoError(t, err)
	// 846_575_334 sub-minor units rounds to 847 cents.
	assertEqual(t, "receivable after the repricing", receivable, ledger.Amount(847))
}

// TestOverdraftAccrualUnderThirty360SkipsThe31st is the only 30/360 coverage on
// the deposit path, and dailyOverdraftAccrual leans on that convention for its
// justification: under 30/360-US the 31st collapses onto the 30th, so a day can
// count as no days at all, and what a window comes to depends on how it is cut.
func TestOverdraftAccrualUnderThirty360SkipsThe31st(t *testing.T) {
	ctx := context.Background()
	clock := &mutableClock{}
	start := time.Date(2026, 1, 29, 0, 0, 0, 0, time.UTC)
	clock.set(start)
	reg, book, sub := newTestRegisterOn(t, clock.now)

	acct, err := reg.OpenAccount(ctx, sub, "Bruno", testAsset, 0)
	assertNoError(t, err)
	acct, err = reg.SetOverdraftTerms(ctx, acct.ID, 50_000, 150_000, 0, interest.Thirty360)
	assertNoError(t, err)
	postTo(t, book, sub, acct, 10_000, ledger.Debit, start)

	// €100 at 15% over a 360-day year: 10_000 × 150_000 / 360 = 4_166_666.67,
	// truncated to 4_166_666 a day.
	day := func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(30)))
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued 29 -> 30 January", got.Accrued, interest.Accrued(4_166_666))

	// The 31st is free: Days(30th, 31st) is zero under this convention, so the
	// run is refused before it starts and nothing is added.
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(31)))
	got, err = reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued 30 -> 31 January", got.Accrued, interest.Accrued(4_166_666))

	// 1 February charges one day, not two: Days(31st, 1st) is 1, and the 31st
	// still counts for nothing.
	//
	// 8_333_332 is also what pins the day-by-day walk. One call over the whole
	// window would be 10_000 × 150_000 × 2 / 360 = 8_333_333 — a unit more,
	// because Accrue divides once per call and this balance truncates upward
	// when the days are taken together.
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)))
	got, err = reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued 31 January -> 1 February", got.Accrued, interest.Accrued(8_333_332))
}

// TestCheckCredit_RefusesOnlyAClosedAccount is the whole status matrix from the
// receiving side, and it is deliberately far more permissive than
// CheckWithdrawal's.
//
// A credit cannot fail for want of money, so the only question it can answer is
// whether this account is still somewhere money may land. Dormant is — an
// incoming payment is exactly what revives it. Frozen is, because a freeze here
// is a DEBIT block: the garnishment and fraud-investigation case, where money
// owed to the customer keeps arriving while they cannot take any out.
func TestCheckCredit_RefusesOnlyAClosedAccount(t *testing.T) {
	ctx := context.Background()
	reg, _, deposits := newTestRegister(t)

	active, _ := reg.OpenAccount(ctx, deposits, "Active", testAsset, 0)
	assertNoError(t, reg.CheckCredit(ctx, active.ID))

	dormant, _ := reg.OpenAccount(ctx, deposits, "Dormant", testAsset, 0)
	assertNoError(t, reg.MarkDormant(ctx, dormant.ID))
	assertNoError(t, reg.CheckCredit(ctx, dormant.ID))

	frozen, _ := reg.OpenAccount(ctx, deposits, "Frozen", testAsset, 0)
	assertNoError(t, reg.Freeze(ctx, frozen.ID))
	assertNoError(t, reg.CheckCredit(ctx, frozen.ID))

	closed, _ := reg.OpenAccount(ctx, deposits, "Closed", testAsset, 0)
	assertNoError(t, reg.Close(ctx, closed.ID))
	assertError(t, reg.CheckCredit(ctx, closed.ID), ErrAccountClosed)

	// An unknown account is not creditable either, and says so as itself.
	assertError(t, reg.CheckCredit(ctx, "dep_nope"), ErrAccountNotFound)
}

// TestCheckWithdrawal_NamesDormancy pins the error a blocked debit on a dormant
// account reports. It used to fall through requireActive's default branch and
// come back as ErrInvalidStatusTransition — an error about changing a status,
// raised by an operation that was not changing one.
func TestCheckWithdrawal_NamesDormancy(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, deposits, "Alice", testAsset, 0)
	fund(t, reg, cash, alice, 10000)

	assertNoError(t, reg.MarkDormant(ctx, alice.ID))

	// Money out is blocked, and names dormancy rather than a status transition.
	assertError(t, reg.CheckWithdrawal(ctx, alice.ID, 1000), ErrAccountDormant)
	_, err := reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 1000})
	assertError(t, err, ErrAccountDormant)

	// Money in is not: it is what revives the account.
	assertNoError(t, reg.CheckCredit(ctx, alice.ID))

	// And a real status-transition error is still that error, so the two have
	// not been collapsed.
	assertError(t, reg.Unfreeze(ctx, alice.ID), ErrInvalidStatusTransition)
}
