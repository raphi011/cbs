// Package deposit_test holds the deposit layer's tests. They live outside
// package deposit because they build a Register over a store from
// store/testenv, which reaches store/sqlite, which imports deposit.
package deposit_test

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	. "github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/store/testenv"
)

// fixedTime is the instant the test clock returns. The clock is deliberately
// frozen: every row then ties on CreatedAt, which is exactly the case the
// store's ordering tie-break has to get right.
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

// mutableClock is a test clock a test can move.
type mutableClock struct{ at time.Time }

func (c *mutableClock) set(t time.Time) { c.at = t }
func (c *mutableClock) now() time.Time  { return c.at }

// newTestRegister creates a Register backed by a fresh ledger with a fixed
// clock, returning the register, its book, the customer-deposits subledger and
// a published product to open accounts from.
func newTestRegister(t *testing.T) (*Register, *ledger.Book, ledger.SubledgerID, product.ID) {
	t.Helper()
	return newTestRegisterOn(t, func() time.Time { return fixedTime })
}

// newTestRegisterOn is newTestRegister on a caller-supplied clock, for the
// tests where WHEN an operation happened is the thing under test.
func newTestRegisterOn(t *testing.T, clock func() time.Time) (*Register, *ledger.Book, ledger.SubledgerID, product.ID) {
	t.Helper()
	return newTestRegisterOnIssuedBy(t, clock, testIssuer)
}

// newTestRegisterIssuedBy is newTestRegister under a caller-supplied allocation,
// for the tests where WHAT a register mints under is the thing under test —
// including the zero allocation, which mints nothing.
func newTestRegisterIssuedBy(t *testing.T, issuer iban.Issuer) (*Register, *ledger.Book, ledger.SubledgerID, product.ID) {
	t.Helper()
	return newTestRegisterOnIssuedBy(t, func() time.Time { return fixedTime }, issuer)
}

func newTestRegisterOnIssuedBy(t *testing.T, clock func() time.Time, issuer iban.Issuer) (*Register, *ledger.Book, ledger.SubledgerID, product.ID) {
	t.Helper()
	reg, cat, book, sub := newTestRegisterWithCatalogueIssuedBy(t, clock, issuer)
	ctx := context.Background()

	free, err := cat.CreateProduct(ctx, "Basic Current Account", product.CurrentAccount)
	assertNoError(t, err)
	today := ledger.DayStart(clock())
	_, err = cat.DraftVersion(ctx, free.ID, today, product.OverdraftPricing{})
	assertNoError(t, err)
	_, err = cat.PublishVersion(ctx, free.ID, today)
	assertNoError(t, err)

	return reg, book, sub, free.ID
}

// newTestRegisterWithCatalogue is newTestRegisterOn plus a catalogue over the
// same store and book, because after this change an account cannot be opened
// without a product to open it from.
func newTestRegisterWithCatalogue(t *testing.T, clock func() time.Time) (*Register, *product.Catalogue, *ledger.Book, ledger.SubledgerID) {
	t.Helper()
	return newTestRegisterWithCatalogueIssuedBy(t, clock, testIssuer)
}

func newTestRegisterWithCatalogueIssuedBy(t *testing.T, clock func() time.Time, issuer iban.Issuer) (*Register, *product.Catalogue, *ledger.Book, ledger.SubledgerID) {
	t.Helper()
	ctx := context.Background()
	store := testenv.New(t, clock)
	book := ledger.NewBook(store.Ledger(), "bank", clock)
	cat := product.NewCatalogue(store.Product(), book, book.ID(), clock)

	gl, err := book.CreateLedger(ctx, "General Ledger")
	assertNoError(t, err)
	deposits, err := book.CreateSubledger(ctx, gl.ID, "Customer Deposits")
	assertNoError(t, err)
	reg := NewRegister(store.Deposit(), book, book.ID(), clock, issuer, deposits.ID)

	return reg, cat, book, deposits.ID
}

// where an account's money is, and where what it owes in interest is.
func where(t *testing.T, reg *Register, id AccountID) ledger.Position {
	t.Helper()
	pos, err := reg.Position(context.Background(), id)
	assertNoError(t, err)
	return pos
}

func owed(t *testing.T, reg *Register, id AccountID) ledger.Position {
	t.Helper()
	pos, err := reg.Receivable(context.Background(), id)
	assertNoError(t, err)
	return pos
}

// setTerms is the pre-catalogue SetOverdraftTerms, rebuilt from the two calls
// that replaced it: a pinned limit plus a negotiated overlay.
func setTerms(ctx context.Context, reg *Register, id AccountID, limit ledger.Amount, rate, unarranged interest.Rate, dc interest.DayCount, effectiveFrom time.Time) (OverdraftTerms, error) {
	if _, err := reg.SetOverdraftLimit(ctx, id, limit, effectiveFrom); err != nil {
		return OverdraftTerms{}, err
	}
	return reg.SetOverdraftPricingOverlay(ctx, id,
		&product.OverdraftPricing{Rate: rate, UnarrangedRate: unarranged, DayCount: dc}, effectiveFrom)
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
	reg, book, deposits, prd := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	acct, err := reg.OpenAccount(context.Background(), "Alice", testAsset, prd, 0)
	assertNoError(t, err)
	fund(t, reg, cash, acct, 1000)
	return reg, book, acct
}

// fund credits a deposit account's position from the cash asset, simulating a
// customer deposit so the customer has spendable funds.
func fund(t *testing.T, reg *Register, cash ledger.AccountID, acct Account, amount ledger.Amount) {
	t.Helper()
	_, err := reg.Book().PostTransaction(context.Background(), ledger.PostTransactionRequest{
		Description: "Funding",
		Entries: []ledger.Entry{
			{AccountID: cash, Amount: amount, Direction: ledger.Debit},
			entry(where(t, reg, acct.ID), amount, ledger.Credit),
		},
	})
	assertNoError(t, err)
}

// entry is one leg against a position, for a test posting straight into the
// book. Both halves together, always: an entry naming a control account and no
// subsidiary is refused, and one naming a plain account and a subsidiary is too.
func entry(p ledger.Position, amount ledger.Amount, d ledger.Direction) ledger.Entry {
	return ledger.Entry{AccountID: p.Account, Subsidiary: p.Subsidiary, Amount: amount, Direction: d}
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

// Opening an account puts nobody in the chart of accounts. The FIRST account in
// an asset puts the bank's own control line there, and every account after it
// adds nothing at all — which is the whole observable point of pooling.
func TestOpenAccountAddsNoRowToTheChartOfAccounts(t *testing.T) {
	ctx := context.Background()
	reg, _, deposits, prd := newTestRegister(t)

	alice, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
	assertNoError(t, err)
	assertEqual(t, "status", alice.Status, Active)

	rows, err := reg.Book().ListAccounts(ctx, deposits)
	assertNoError(t, err)
	assertEqual(t, "rows in the customer folder", len(rows), 1)
	assertEqual(t, "and it is the bank's line, not Alice's", rows[0].Name, "Customer Deposits (EUR)")
	assertEqual(t, "a Liability", rows[0].Type, ledger.Liability)
	assertEqual(t, "that pools customers", rows[0].Control, true)

	bruno, err := reg.OpenAccount(ctx, "Bruno", testAsset, prd, 0)
	assertNoError(t, err)
	rows, err = reg.Book().ListAccounts(ctx, deposits)
	assertNoError(t, err)
	assertEqual(t, "rows after a second account", len(rows), 1)

	// Both customers' money is in that one line, under their own ids.
	assertEqual(t, "alice", where(t, reg, alice.ID), rows[0].ID.For(string(alice.ID)))
	assertEqual(t, "bruno", where(t, reg, bruno.ID), rows[0].ID.For(string(bruno.ID)))

	// Retrieval round-trips.
	got, err := reg.GetAccount(ctx, alice.ID)
	assertNoError(t, err)
	assertEqual(t, "id", got.ID, alice.ID)
}

// A deposit account's asset is what picks the control account its money pools
// in, so an account in bitcoin is nowhere near the euro pool — and a customer
// account is not a euro account by default, it is an account in whatever asset
// it was opened in.
func TestOpenAccountPoolsByAsset(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)

	euro, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
	assertNoError(t, err)
	acct, err := reg.OpenAccount(ctx, "Anna BTC", otherAsset, prd, 0)
	assertNoError(t, err)
	assertEqual(t, "deposit account asset", acct.Asset, otherAsset)

	pooled := where(t, reg, acct.ID)
	if pooled.Account == where(t, reg, euro.ID).Account {
		t.Fatalf("two assets pooled in one control account: %s", pooled.Account)
	}
	control, err := reg.Book().GetAccount(ctx, pooled.Account)
	assertNoError(t, err)
	assertEqual(t, "control account asset", control.Asset, acct.Asset)

	// It survives the store, not just the return value.
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "reloaded asset", got.Asset, otherAsset)
}

func TestOpenAccountRejectsUnregisteredAsset(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)

	_, err := reg.OpenAccount(ctx, "Anna DOGE", "DOGE", prd, 0)
	assertError(t, err, ledger.ErrAssetNotFound)
}

// A register filed under a folder the chart of accounts does not have refuses
// to open an account, rather than opening one whose money has nowhere to pool.
func TestOpenAccount_SubledgerNotFound(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)
	stray := NewRegister(reg.Store(), reg.Book(), reg.Book().ID(),
		func() time.Time { return fixedTime }, testIssuer, "bad_sub")

	_, err := stray.OpenAccount(ctx, "Alice", testAsset, prd, 0)
	assertError(t, err, ledger.ErrSubledgerNotFound)
}

func TestGetAccount_NotFound(t *testing.T) {
	ctx := context.Background()
	reg, _, _, _ := newTestRegister(t)

	_, err := reg.GetAccount(ctx, "nonexistent")
	assertError(t, err, ErrAccountNotFound)
}

// ---------------------------------------------------------------------------
// Balance & Hold Tests
// ---------------------------------------------------------------------------

func TestHold_ReducesAvailable(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits, prd := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
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
	reg, book, deposits, prd := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
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
	reg, _, _, _ := newTestRegister(t)
	assertError(t, reg.ReleaseHold(ctx, "nonexistent"), ErrHoldNotFound)
}

func TestCreateHold_Validation(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)

	_, err := reg.CreateHold(ctx, CreateHoldRequest{AccountID: "nonexistent", Amount: 100})
	assertError(t, err, ErrAccountNotFound)

	alice, _ := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
	_, err = reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 0})
	assertError(t, err, ErrInvalidAmount)

	// No funds, no overdraft: a hold of 1 overdraws available.
	_, err = reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 1})
	assertError(t, err, ErrInsufficientAvailable)
}

func TestHold_Expiration(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits, prd := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
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
	reg, _, _, prd := newTestRegister(t)

	// No overdraft: a withdrawal of 5000 on an empty account fails.
	noLimit, _ := reg.OpenAccount(ctx, "NoLimit", testAsset, prd, 0)
	assertError(t, reg.CheckWithdrawal(ctx, noLimit.ID, 5000), ErrInsufficientAvailable)

	// With a 5000 overdraft limit the same withdrawal is permitted.
	withLimit, _ := reg.OpenAccount(ctx, "WithLimit", testAsset, prd, 5000)
	assertNoError(t, reg.CheckWithdrawal(ctx, withLimit.ID, 5000))
	// But not a penny more.
	assertError(t, reg.CheckWithdrawal(ctx, withLimit.ID, 5001), ErrInsufficientAvailable)

	// A hold up to the overdraft limit succeeds on an unfunded account.
	_, err := reg.CreateHold(ctx, CreateHoldRequest{AccountID: withLimit.ID, Amount: 5000})
	assertNoError(t, err)
}

func TestCheckWithdrawal_NotFound(t *testing.T) {
	ctx := context.Background()
	reg, _, _, _ := newTestRegister(t)
	assertError(t, reg.CheckWithdrawal(ctx, "nonexistent", 100), ErrAccountNotFound)
}

// ---------------------------------------------------------------------------
// Capture
// ---------------------------------------------------------------------------

func TestCaptureHold_PostsGLTransaction(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits, prd := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
	fund(t, reg, cash, alice, 10000)

	h, _ := reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 3000})

	tx, err := reg.CaptureHold(ctx, h.ID, cash.Total(), 2500, "Gas purchase")
	assertNoError(t, err)
	assertEqual(t, "tx status", tx.Status, ledger.Posted)
	assertEqual(t, "tx legs", len(tx.Entries), 2)

	got, _ := reg.GetHold(ctx, h.ID)
	assertEqual(t, "hold captured", got.Status, HoldCaptured)

	// Book balance moved: 10000 - 2500 = 7500. The hold is not Active.
	bal, _ := reg.GetBalance(ctx, alice.ID)
	assertEqual(t, "book after capture", bal.Book, ledger.Amount(7500))
	assertEqual(t, "holds after capture", bal.Holds, ledger.Amount(0))
	assertEqual(t, "available after capture", bal.Available, ledger.Amount(7500))
}

func TestCaptureHold_DefaultsToHoldAmount(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits, prd := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
	fund(t, reg, cash, alice, 10000)

	h, _ := reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 3000})
	tx, err := reg.CaptureHold(ctx, h.ID, cash.Total(), 0, "Full capture")
	assertNoError(t, err)
	assertEqual(t, "captured amount leg", tx.Entries[0].Amount, ledger.Amount(3000))
}

func TestCaptureHold_Errors(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits, prd := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)

	_, err := reg.CaptureHold(ctx, "nonexistent", cash.Total(), 100, "")
	assertError(t, err, ErrHoldNotFound)

	alice, _ := reg.OpenAccount(ctx, "Alice", testAsset, prd, 1000)
	h, _ := reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 1000})
	assertNoError(t, reg.ReleaseHold(ctx, h.ID))
	_, err = reg.CaptureHold(ctx, h.ID, cash.Total(), 100, "")
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
	_, err = reg.CaptureHold(ctx, h.ID, ledger.AccountID("no.such.account").Total(), 500, "boom")
	assertError(t, err, ledger.ErrAccountNotFound)

	got, err := reg.GetHold(ctx, h.ID)
	assertNoError(t, err)
	assertEqual(t, "hold status after failed capture", got.Status, HoldActive)
	_ = book
}

// The half of atomicity a two-transaction implementation cannot provide: when a
// capture is composed into a caller's unit of work that later fails, the GL
// transaction it posted must not survive either.
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
		captured, err = reg.CaptureHoldTx(ctx, tx, h.ID, where(t, reg, acct.ID), 500, "compose")
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

// A capture that moves money into an account of a different asset needs no check in
// the deposit layer: the posting debits one asset and credits another, and the
// ledger's per-asset balance rule refuses it.
func TestCaptureHoldRejectsCrossAssetCounterparty(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits, prd := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)

	acct, err := reg.OpenAccount(ctx, "Anna EUR", testAsset, prd, 0)
	assertNoError(t, err)
	fund(t, reg, cash, acct, 10_000)

	btcGL, err := reg.Book().CreateAccount(ctx, deposits, "Merchant BTC", ledger.Liability, otherAsset)
	assertNoError(t, err)

	hold, err := reg.CreateHold(ctx, CreateHoldRequest{
		AccountID: acct.ID, Amount: 500, Description: "auth",
	})
	assertNoError(t, err)

	_, err = reg.CaptureHold(ctx, hold.ID, btcGL.ID.Total(), 500, "capture into a BTC account")
	assertError(t, err, ledger.ErrUnbalancedAsset)
}

// ---------------------------------------------------------------------------
// Status Lifecycle
// ---------------------------------------------------------------------------

func TestFreeze_BlocksHolds(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits, prd := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
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
	reg, _, _, prd := newTestRegister(t)
	alice, _ := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)

	assertNoError(t, reg.MarkDormant(ctx, alice.ID))
	got, _ := reg.GetAccount(ctx, alice.ID)
	assertEqual(t, "dormant", got.Status, Dormant)

	assertNoError(t, reg.Reactivate(ctx, alice.ID))
	got, _ = reg.GetAccount(ctx, alice.ID)
	assertEqual(t, "reactivated", got.Status, Active)
}

func TestClose_RequiresZeroBalance(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits, prd := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
	fund(t, reg, cash, alice, 10000)

	// Non-zero balance blocks close.
	assertError(t, reg.Close(ctx, alice.ID), ErrAccountNotEmpty)

	// Drain the balance, then close succeeds.
	h, _ := reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 10000})
	_, err := reg.CaptureHold(ctx, h.ID, cash.Total(), 10000, "Final withdrawal")
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
	reg, _, _, prd := newTestRegister(t)
	alice, _ := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)

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
	reg, book, deposits, prd := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
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
	reg, _, _, prd := newTestRegister(t)

	alice, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
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

// One receivable serves every account in an asset, and the detail under it is
// the entries — which is what makes a shared one duplicate nothing.
// Σ(subsidiary balances) == control balance cannot fail arithmetically, and
// that is why it is worth computing: it would catch a posting that named the
// pool without naming whose interest it was.
func TestOneReceivableServesEveryAccountInAnAsset(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, prd := newTestRegister(t)

	bruno, err := reg.OpenAccount(ctx, "Bruno", "EUR", prd, 50_000)
	assertNoError(t, err)
	carla, err := reg.OpenAccount(ctx, "Carla", "EUR", prd, 50_000)
	assertNoError(t, err)

	// One line in the receivable folder, whatever the customer count, and it is
	// an Asset control account in the accounts' own asset.
	brunoOwes := owed(t, reg, bruno.ID)
	carlaOwes := owed(t, reg, carla.ID)
	assertEqual(t, "one receivable for both", brunoOwes.Account, carlaOwes.Account)
	assertEqual(t, "under bruno", brunoOwes.Subsidiary, string(bruno.ID))
	shared, err := book.GetAccount(ctx, brunoOwes.Account)
	assertNoError(t, err)
	assertEqual(t, "receivable type", shared.Type.String(), ledger.Asset.String())
	assertEqual(t, "receivable asset", string(shared.Asset), "EUR")
	assertEqual(t, "receivable pools customers", shared.Control, true)
	rows, err := book.ListAccounts(ctx, shared.SubledgerID)
	assertNoError(t, err)
	assertEqual(t, "rows in the receivable folder", len(rows), 1)

	// Two customers overdrawn by different amounts for thirty days.
	for _, c := range []struct {
		acct  Account
		drawn ledger.Amount
	}{{bruno, 20_000}, {carla, 60_000}} {
		_, err = setTerms(ctx, reg, c.acct.ID, 50_000, 150_000, 350_000, interest.Thirty360, time.Time{})
		assertNoError(t, err)
		overdrawValueDated(t, reg, book, sub, c.acct, c.drawn, fixedTime, fixedTime)
		assertNoError(t, reg.AccrueOverdraft(ctx, c.acct.ID, fixedTime.AddDate(0, 0, 30)))
	}

	brunoAccrued, err := book.BookBalance(ctx, brunoOwes)
	assertNoError(t, err)
	carlaAccrued, err := book.BookBalance(ctx, carlaOwes)
	assertNoError(t, err)
	pool, err := book.BookBalance(ctx, shared.ID.Total())
	assertNoError(t, err)

	if brunoAccrued <= 0 || carlaAccrued <= brunoAccrued {
		t.Fatalf("expected two different non-zero accruals, got bruno %d, carla %d", brunoAccrued, carlaAccrued)
	}
	assertEqual(t, "the pool is the sum of its customers", pool, brunoAccrued+carlaAccrued)
}

// Four writes on one effective DAY — the opening row plus three setter calls — are
// ONE row. A terms row is identified by (account, day), which makes "the terms in
// force on day D" unique by construction rather than by a validation rule.
func TestSameDayTermsWritesCollapseToOneRow(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, "Bruno", "EUR", prd, 0)
	assertNoError(t, err)

	_, err = setTerms(ctx, reg, acct.ID, 50_000, 150_000, 350_000, interest.Thirty360, time.Time{})
	assertNoError(t, err)
	last, err := setTerms(ctx, reg, acct.ID, 60_000, 160_000, 350_000, interest.Thirty360, time.Time{})
	assertNoError(t, err)

	// The row the last call returned carries the overlay it wrote.
	if last.Pricing == nil {
		t.Fatal("the last write dropped its overlay")
	}
	assertEqual(t, "rate", last.Pricing.Rate, interest.Rate(160_000))
	assertEqual(t, "unarranged rate", last.Pricing.UnarrangedRate, interest.Rate(350_000))
	assertEqual(t, "day count", last.Pricing.DayCount, interest.Thirty360)
	assertEqual(t, "product carried forward", string(last.ProductID), string(prd))

	history, err := reg.OverdraftTermsHistory(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "timeline length after three same-day calls", len(history), 1)
	assertEqual(t, "the surviving row is the last one written", history[0].Pricing.Rate, interest.Rate(160_000))
	assertEqual(t, "and its limit", history[0].OverdraftLimit, ledger.Amount(60_000))
}

func TestSetOverdraftTerms_Rejects(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, "Bruno", "EUR", prd, 0)
	assertNoError(t, err)

	_, err = setTerms(ctx, reg, acct.ID, -1, 0, 0, interest.ACT365, time.Time{})
	assertError(t, err, ErrInvalidAmount)

	_, err = setTerms(ctx, reg, acct.ID, 50_000, -1, 0, interest.ACT365, time.Time{})
	assertError(t, err, ErrInvalidRate)

	// An unarranged rate with no arranged rate is not a product.
	_, err = setTerms(ctx, reg, acct.ID, 50_000, 0, 350_000, interest.ACT365, time.Time{})
	assertError(t, err, ErrInvalidRate)

	_, err = setTerms(ctx, reg, "nonexistent", 0, 0, 0, interest.ACT365, time.Time{})
	assertError(t, err, ErrAccountNotFound)

	// A closed account takes no new terms.
	closed, err := reg.OpenAccount(ctx, "Gone", "EUR", prd, 0)
	assertNoError(t, err)
	assertNoError(t, reg.Close(ctx, closed.ID))
	_, err = setTerms(ctx, reg, closed.ID, 50_000, 150_000, 0, interest.ACT365, time.Time{})
	assertError(t, err, ErrAccountClosed)
}

// overdrawBy pushes an account to a negative book balance by posting a debit
// straight to the general ledger, which is how a payment reaches it too.
func overdrawBy(t *testing.T, reg *Register, book *ledger.Book, sub ledger.SubledgerID, acct Account, amount ledger.Amount) {
	t.Helper()
	ctx := context.Background()
	counterparty, err := book.CreateAccount(ctx, sub, "Counterparty "+string(acct.ID), ledger.Liability, acct.Asset)
	if err != nil {
		t.Fatalf("counterparty: %v", err)
	}
	if _, err := book.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "overdraw",
		Entries: []ledger.Entry{
			entry(where(t, reg, acct.ID), amount, ledger.Debit),
			{AccountID: counterparty.ID, Amount: amount, Direction: ledger.Credit},
		},
	}); err != nil {
		t.Fatalf("overdraw: %v", err)
	}
}

// overdrawValueDated is overdrawBy with explicit dates. A BACK-DATED posting is one
// whose booking date is today and whose value date is a day already accrued through,
// which overdrawBy — value-dated at the clock — cannot express.
func overdrawValueDated(t *testing.T, reg *Register, book *ledger.Book, sub ledger.SubledgerID, acct Account, amount ledger.Amount, booking, value time.Time) {
	t.Helper()
	ctx := context.Background()
	counterparty, err := book.CreateAccount(ctx, sub,
		"Counterparty "+string(acct.ID)+" "+value.Format("2006-01-02"), ledger.Liability, acct.Asset)
	assertNoError(t, err)
	_, err = book.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "overdraw",
		BookingDate: booking,
		ValueDate:   value,
		Entries: []ledger.Entry{
			entry(where(t, reg, acct.ID), amount, ledger.Debit),
			{AccountID: counterparty.ID, Amount: amount, Direction: ledger.Credit},
		},
	})
	assertNoError(t, err)
}

// expectedFromTimeline sums what the account SHOULD have accrued, day by day,
// from functions the test states rather than from the implementation under
// test.
func expectedFromTimeline(start time.Time, days int, dc interest.DayCount,
	drawnOn func(n int) ledger.Amount, rateOn func(n int) interest.Rate) interest.Accrued {
	var total interest.Accrued
	for n := 1; n <= days; n++ {
		from := start.AddDate(0, 0, n-1)
		total += interest.Accrue(drawnOn(n), rateOn(n), dc, from, from.AddDate(0, 0, 1))
	}
	return total
}

// countingTx wraps a Tx and counts entry scans, so a test can assert that a run
// was skipped BEFORE any I/O rather than that it happened to produce zero. The
// two are indistinguishable from the balance alone.
type countingTx struct {
	Tx
	scans *int
}

func (t countingTx) ScanEntries(ctx context.Context, book ledger.BookID, pos ledger.Position, f ledger.EntryFilter) iter.Seq2[ledger.Entry, error] {
	*t.scans++
	return t.Tx.ScanEntries(ctx, book, pos, f)
}

// fundBy posts the mirror-image transaction to overdrawBy: it credits the
// account and debits an Asset counterparty, the shape a customer deposit
// takes.
func fundBy(t *testing.T, reg *Register, book *ledger.Book, sub ledger.SubledgerID, acct Account, amount ledger.Amount) {
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
			entry(where(t, reg, acct.ID), amount, ledger.Credit),
		},
	}); err != nil {
		t.Fatalf("fund: %v", err)
	}
}

func TestAccrueOverdraft_PostsTheDeltaOfTheRoundedValue(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, "Bruno", "EUR", prd, 0)
	assertNoError(t, err)
	_, err = setTerms(ctx, reg, acct.ID, 50_000, 150_000, 0, interest.ACT365, time.Time{})
	assertNoError(t, err)

	// €50 overdrawn at 15% ACT/365 accrues 2.054794 cents a day.
	overdrawBy(t, reg, book, sub, acct, 5_000)

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

		balance, err := book.BookBalance(ctx, owed(t, reg, got.ID))
		assertNoError(t, err)
		assertEqual(t, "receivable balance", balance, wantGL[i-1])
		if balance != got.Accrued.Minor() {
			t.Fatalf("day %d: GL %d != Minor(accrued) %d", i, balance, got.Accrued.Minor())
		}
	}
}

func TestAccrueOverdraft_IsIdempotentPerDate(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, "Bruno", "EUR", prd, 0)
	assertNoError(t, err)
	_, err = setTerms(ctx, reg, acct.ID, 50_000, 150_000, 0, interest.ACT365, time.Time{})
	assertNoError(t, err)
	overdrawBy(t, reg, book, sub, acct, 5_000)

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
	reg, book, sub, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, "Bruno", "EUR", prd, 0)
	assertNoError(t, err)
	// Arranged 12% up to €100, unarranged 36% beyond it.
	_, err = setTerms(ctx, reg, acct.ID, 10_000, 120_000, 360_000, interest.ACT360, time.Time{})
	assertNoError(t, err)

	// €150 drawn: €100 arranged, €50 unarranged.
	overdrawBy(t, reg, book, sub, acct, 15_000)

	start := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, 1)))

	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)

	// 10_000 × 120_000 / 360 = 3_333_333, plus 5_000 × 360_000 / 360 = 5_000_000.
	assertEqual(t, "tiered accrual", got.Accrued, interest.Accrued(8_333_333))
}

// An unarranged rate is a surcharge, so its absence must mean the arranged rate
// applies throughout — never that the part beyond the limit is free, which would make
// exceeding a limit cheaper than respecting it.
func TestAccrueOverdraft_ExcessFallsBackToTheArrangedRate(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, "Bruno", "EUR", prd, 0)
	assertNoError(t, err)
	// Arranged 12% up to €100, and no unarranged rate at all.
	_, err = setTerms(ctx, reg, acct.ID, 10_000, 120_000, 0, interest.ACT360, time.Time{})
	assertNoError(t, err)

	// €150 drawn: €100 inside the limit, €50 beyond it.
	overdrawBy(t, reg, book, sub, acct, 15_000)

	start := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, 1)))

	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)

	// The WHOLE €150 accrues at 12%, computed tier by tier with Accrue truncating
	// each, so the figure is one micro-minor-unit under the single-base 5_000_000.
	assertEqual(t, "excess accrual at the arranged rate", got.Accrued, interest.Accrued(4_999_999))
}

func TestAccrueOverdraft_IgnoresHoldsAndCredits(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, "Alice", "EUR", prd, 0)
	assertNoError(t, err)
	_, err = setTerms(ctx, reg, acct.ID, 50_000, 150_000, 0, interest.ACT365, time.Time{})
	assertNoError(t, err)

	// In credit, with a hold that takes available below zero. Interest accrues
	// on the book balance, because a hold is not borrowed money.
	fundBy(t, reg, book, sub, acct, 1_000)
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
	reg, book, sub, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, "Bruno", "EUR", prd, 50_000)
	assertNoError(t, err)
	overdrawBy(t, reg, book, sub, acct, 5_000)

	start := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, 1)))

	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued with no rate", got.Accrued, interest.Accrued(0))
	accrued, err := book.BookBalance(ctx, owed(t, reg, acct.ID))
	assertNoError(t, err)
	assertEqual(t, "and nothing was posted under it", accrued, ledger.Amount(0))
}

func TestChargeOverdraftInterest_CapitalizesAndLeavesANegativeResidue(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, "Bruno", "EUR", prd, 0)
	assertNoError(t, err)
	_, err = setTerms(ctx, reg, acct.ID, 50_000, 150_000, 0, interest.ACT365, time.Time{})
	assertNoError(t, err)
	overdrawBy(t, reg, book, sub, acct, 5_000)

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

	receivable, err := book.BookBalance(ctx, owed(t, reg, after.ID))
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
		gl, err := book.BookBalance(ctx, owed(t, reg, got.ID))
		assertNoError(t, err)
		if gl != got.Accrued.Minor() {
			t.Fatalf("day %d after charging: GL %d != Minor(accrued) %d", i, gl, got.Accrued.Minor())
		}
	}
}

func TestChargeOverdraftInterest_NothingAccruedPostsNothing(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, "Alice", "EUR", prd, 0)
	assertNoError(t, err)
	_, err = setTerms(ctx, reg, acct.ID, 50_000, 150_000, 0, interest.ACT365, time.Time{})
	assertNoError(t, err)

	txn, err := reg.ChargeOverdraftInterest(ctx, acct.ID, time.Date(2025, time.February, 1, 0, 0, 0, 0, time.UTC))
	assertNoError(t, err)
	if txn.ID != "" {
		t.Errorf("charging nothing posted transaction %s", txn.ID)
	}
}

// The guard on an explicitly-invoked charge: posting a debit to an account CloseTx
// only let through at zero would reopen a balance on it. Reaching a closed account
// with an overdraft history takes the whole flow — charge, repay, close.
func TestChargeOverdraftInterest_RefusesClosedAccount(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, "Carla", "EUR", prd, 0)
	assertNoError(t, err)
	_, err = setTerms(ctx, reg, acct.ID, 50_000, 150_000, 0, interest.ACT365, time.Time{})
	assertNoError(t, err)
	overdrawBy(t, reg, book, sub, acct, 5_000)

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
	fundBy(t, reg, book, sub, acct, 5_010)
	assertNoError(t, reg.Close(ctx, acct.ID))

	_, err = reg.ChargeOverdraftInterest(ctx, acct.ID, start.AddDate(0, 0, 6))
	assertError(t, err, ErrAccountClosed)
}

// An account that was overdrawn, accrued interest and repaid its principal has
// a zero book balance and a live receivable.
func TestClose_RefusesAStrandedReceivable(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, "Carla", "EUR", prd, 0)
	assertNoError(t, err)
	_, err = setTerms(ctx, reg, acct.ID, 50_000, 150_000, 0, interest.ACT365, time.Time{})
	assertNoError(t, err)
	overdrawBy(t, reg, book, sub, acct, 5_000)

	start := time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)
	for i := 0; i <= 5; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, start.AddDate(0, 0, i)))
	}

	// The customer account's own balance is back to zero.
	fundBy(t, reg, book, sub, acct, 5_000)
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
	fundBy(t, reg, book, sub, acct, 10)

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
func TestClose_SucceedsOnAnExactHalfMinorUnitResidue(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, "Dana", "EUR", prd, 0)
	assertNoError(t, err)
	_, err = setTerms(ctx, reg, acct.ID, 2_000, 100_000, 0, interest.ACT365, time.Time{})
	assertNoError(t, err)
	overdrawBy(t, reg, book, sub, acct, 1_825)

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

	receivable, err := book.BookBalance(ctx, owed(t, reg, after.ID))
	assertNoError(t, err)
	assertEqual(t, "receivable is fully cleared despite the nonzero residue", receivable, ledger.Amount(0))

	// Repay the 1,825 principal plus the capitalized cent: 1,826 in total.
	fundBy(t, reg, book, sub, acct, 1_826)
	balance, err := reg.GetBalance(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "book balance before closing", balance.Book, ledger.Amount(0))

	// A guard on Accrued.Minor() != 0 would refuse this close forever. Reading the
	// receivable's own ledger balance instead gives zero, and lets it through.
	assertNoError(t, reg.Close(ctx, acct.ID))
}

func TestRunEndOfDay_AccruesEveryOverdrawnAccount(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, prd := newTestRegister(t)

	overdrawn, err := reg.OpenAccount(ctx, "Bruno", "EUR", prd, 0)
	assertNoError(t, err)
	_, err = setTerms(ctx, reg, overdrawn.ID, 50_000, 150_000, 0, interest.ACT365, time.Time{})
	assertNoError(t, err)
	overdrawBy(t, reg, book, sub, overdrawn, 5_000)

	inCredit, err := reg.OpenAccount(ctx, "Alice", "EUR", prd, 0)
	assertNoError(t, err)
	_, err = setTerms(ctx, reg, inCredit.ID, 50_000, 150_000, 0, interest.ACT365, time.Time{})
	assertNoError(t, err)
	fundBy(t, reg, book, sub, inCredit, 20_000)

	unpriced, err := reg.OpenAccount(ctx, "Bella", "EUR", prd, 50_000)
	assertNoError(t, err)
	overdrawBy(t, reg, book, sub, unpriced, 5_000)

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

// This test is the design. An overdrawn current account is a loan, and it reaches the
// Asset side of the balance sheet by AGGREGATION, not by a posting: its drawn amount
// is the negative balance of its own Liability account viewed by sign.
func TestTotals_OverdraftsAreDerivedAndNothingIsPosted(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, prd := newTestRegister(t)

	bruno, err := reg.OpenAccount(ctx, "Bruno", "EUR", prd, 50_000)
	assertNoError(t, err)
	alice, err := reg.OpenAccount(ctx, "Alice", "EUR", prd, 0)
	assertNoError(t, err)
	satoshi, err := reg.OpenAccount(ctx, "Satoshi", "BTC", prd, 0)
	assertNoError(t, err)

	fundBy(t, reg, book, sub, alice, 20_000)
	fundBy(t, reg, book, sub, satoshi, 500_000)
	overdrawBy(t, reg, book, sub, bruno, 5_000)

	// Run end-of-day before asserting anything. RunEndOfDayTx loops every deposit
	// account nightly, which is where a reclassification posting or a sweep of
	// drawn balances would be tempting.
	assertNoError(t, reg.RunEndOfDay(ctx, time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC)))

	// Overdrawing posted exactly one transaction against Bruno's account, and both legs
	// are Liability. No Asset account was touched, and no second transaction
	// reclassified anything.
	txs, err := book.ListTransactionsForPosition(ctx, where(t, reg, bruno.ID))
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

// accrualStart is the day the value-dated accrual tests open their accounts on, and
// therefore the day their recompute window opens. A midnight, because these tests
// want that day to be a business date.
var accrualStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// postTo moves amount against acct's position at the given value date, with a
// fresh counterparty as the contra leg.
func postTo(t *testing.T, reg *Register, book *ledger.Book, sub ledger.SubledgerID, acct Account, amount ledger.Amount, dir ledger.Direction, value time.Time) {
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
			entry(where(t, reg, acct.ID), amount, dir),
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
	reg, book, sub, prd := newTestRegisterOn(t, clock.now)

	acct, err := reg.OpenAccount(ctx, "Bruno", testAsset, prd, 0)
	assertNoError(t, err)
	_, err = setTerms(ctx, reg, acct.ID, 50_000, 150_000, 0, interest.ACT365, time.Time{})
	assertNoError(t, err)
	return reg, book, sub, acct, clock
}

func TestOverdraftAccrualCorrectsABackdatedCredit(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, acct, _ := newOverdraftAccount(t)

	// Overdrawn by EUR 200 on day 1, then accrue ten days.
	postTo(t, reg, book, sub, acct, 20_000, ledger.Debit, accrualStart)
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
	postTo(t, reg, book, sub, acct, 20_000, ledger.Credit, accrualStart.AddDate(0, 0, 3))

	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, 11)))
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	corrected := got.Accrued.Minor()
	if corrected >= after {
		t.Errorf("accrued after backdated credit = %d, want less than %d", corrected, after)
	}

	// And the receivable's ledger balance agrees with the record.
	recv, err := book.BookBalance(ctx, owed(t, reg, got.ID))
	assertNoError(t, err)
	if recv != corrected {
		t.Errorf("receivable = %d, accrued = %d; the two must agree", recv, corrected)
	}
}

func TestOverdraftAccrualCorrectsABackdatedDebit(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, acct, _ := newOverdraftAccount(t)

	postTo(t, reg, book, sub, acct, 20_000, ledger.Debit, accrualStart)
	for i := 1; i <= 10; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, i)))
	}
	ten, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	after := ten.Accrued.Minor()

	// A card settlement lands late, backdated to day 3: he was more overdrawn
	// than the ledger knew.
	postTo(t, reg, book, sub, acct, 20_000, ledger.Debit, accrualStart.AddDate(0, 0, 3))

	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, 11)))
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	if corrected := got.Accrued.Minor(); corrected <= after {
		t.Errorf("accrued after backdated debit = %d, want more than %d", corrected, after)
	}

	// "More than before" is not enough: accruing one more day on today's balance
	// is also more than before, which is what an incrementing version would do —
	// 82 cents plus a day at −400 is 99.
	assertEqual(t, "accrued after backdated debit", got.Accrued, interest.Accrued(164_383_560))

	// And the receivable really holds it: the ledger balance, not Accrued.Minor()
	// restated. Reading the GL account is what proves the recompute's delta was
	// posted rather than only recorded on the account row.
	receivable, err := book.BookBalance(ctx, owed(t, reg, got.ID))
	assertNoError(t, err)
	assertEqual(t, "receivable after backdated debit", receivable, ledger.Amount(164))
}

func TestOverdraftAccrualIgnoresAForwardValueDatedDebit(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, acct, _ := newOverdraftAccount(t)

	// Booked today, value-dated five days out. It has not taken effect yet.
	postTo(t, reg, book, sub, acct, 20_000, ledger.Debit, accrualStart.AddDate(0, 0, 5))

	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, 1)))
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	if got.Accrued != 0 {
		t.Errorf("accrued = %d on a debit that has not taken effect, want 0", got.Accrued)
	}
}

// TestSettingOverdraftTermsAccruesAndResetsNothing pins what a repricing costs
// the accrual state: nothing at all.
func TestSettingOverdraftTermsAccruesAndResetsNothing(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, acct, clock := newOverdraftAccount(t)

	postTo(t, reg, book, sub, acct, 20_000, ledger.Debit, accrualStart)
	for i := 1; i <= 10; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, i)))
	}
	ten, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)

	// Reprice to triple the rate from day 10, then look at the account.
	clock.set(accrualStart.AddDate(0, 0, 10))
	row, err := setTerms(ctx, reg, acct.ID, 50_000, 450_000, 0, interest.ACT365,
		accrualStart.AddDate(0, 0, 10))
	assertNoError(t, err)
	assertEqual(t, "the row that was written carries the new rate", row.Pricing.Rate, interest.Rate(450_000))

	after, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued after repricing", after.Accrued, ten.Accrued)
	assertEqual(t, "accrued gross after repricing", after.AccruedGross, ten.AccruedGross)
	if after.AccruedGross == 0 {
		t.Error("AccruedGross was reset by a repricing; there is no window to restart")
	}
	if !after.LastAccrualDate.Equal(ten.LastAccrualDate) {
		t.Errorf("last accrual date moved on a repricing: %v then %v",
			ten.LastAccrualDate, after.LastAccrualDate)
	}
}

func TestOverdraftCorrectionRefundsWhatTheReceivableCannotAbsorb(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, acct, _ := newOverdraftAccount(t)

	postTo(t, reg, book, sub, acct, 20_000, ledger.Debit, accrualStart)
	for i := 1; i <= 30; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, i)))
	}
	// Capitalise, emptying the receivable.
	_, err := reg.ChargeOverdraftInterest(ctx, acct.ID, accrualStart.AddDate(0, 0, 30))
	assertNoError(t, err)
	charged, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	recvAfterCharge, err := book.BookBalance(ctx, owed(t, reg, charged.ID))
	assertNoError(t, err)
	if recvAfterCharge != 0 {
		t.Fatalf("receivable after capitalisation = %d, want 0", recvAfterCharge)
	}
	balBefore, err := book.BookBalance(ctx, where(t, reg, acct.ID))
	assertNoError(t, err)

	// Now say he was never overdrawn at all: a credit backdated to day 1.
	postTo(t, reg, book, sub, acct, 20_000, ledger.Credit, accrualStart)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, 31)))

	recv, err := book.BookBalance(ctx, owed(t, reg, charged.ID))
	assertNoError(t, err)
	if recv < 0 {
		t.Errorf("receivable = %d; the correction must clamp rather than drive an asset negative", recv)
	}
	balAfter, err := book.BookBalance(ctx, where(t, reg, acct.ID))
	assertNoError(t, err)
	if balAfter <= balBefore {
		t.Errorf("customer balance %d -> %d; the unabsorbed correction must be refunded to them", balBefore, balAfter)
	}

	// The refunded part left in cash, so it must leave the record too.
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	if recv != got.Accrued.Minor() {
		t.Errorf("receivable = %d, accrued = %d; a refund that stays on the record is money given back twice",
			recv, got.Accrued.Minor())
	}

	// And the account goes on working. Overdraw him again and accrue: the
	// receivable and the record must still agree.
	postTo(t, reg, book, sub, acct, 20_000, ledger.Debit, accrualStart.AddDate(0, 0, 32))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, 33)))
	next, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	nextRecv, err := book.BookBalance(ctx, owed(t, reg, next.ID))
	assertNoError(t, err)
	if nextRecv <= 0 {
		t.Errorf("receivable = %d after two freshly overdrawn days, want it to have moved", nextRecv)
	}
	if nextRecv != next.Accrued.Minor() {
		t.Errorf("receivable = %d, accrued = %d; the refund was left on the record", nextRecv, next.Accrued.Minor())
	}
}

// TestARepricingPricesItsOwnEffectiveDay pins the answer at the boundary. There
// is no window boundary to fall between: a repricing writes a row and
// pre-accrues nothing.
func TestARepricingPricesItsOwnEffectiveDay(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, acct, clock := newOverdraftAccount(t)

	postTo(t, reg, book, sub, acct, 20_000, ledger.Debit, accrualStart)
	for i := 1; i <= 9; i++ {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, i)))
	}
	nine, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	// Days 1-9 at 15% on €200: 9 × 8_219_178. Unchanged.
	assertEqual(t, "accrued through day 9", nine.Accrued, interest.Accrued(73_972_602))

	// Reprice to triple the rate from day 10, with day 10 not yet accrued.
	clock.set(accrualStart.AddDate(0, 0, 10))
	_, err = setTerms(ctx, reg, acct.ID, 50_000, 450_000, 0, interest.ACT365,
		accrualStart.AddDate(0, 0, 10))
	assertNoError(t, err)

	entered, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	// Entering the row posts nothing. Pre-accruing the outgoing span here would
	// land on 82_191_780 before any run had asked for day 10.
	assertEqual(t, "accrued when the row is merely entered", entered.Accrued, interest.Accrued(73_972_602))

	// The next run adds day 10. Three outcomes are distinguishable, which is the
	// point of tripling the rate rather than nudging it.
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, 10)))
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued after repricing", got.Accrued, interest.Accrued(98_630_136))

	receivable, err := book.BookBalance(ctx, owed(t, reg, got.ID))
	assertNoError(t, err)
	assertEqual(t, "receivable after repricing", receivable, ledger.Amount(99))
	if receivable != got.Accrued.Minor() {
		t.Errorf("receivable %d != Minor(accrued) %d across a repricing", receivable, got.Accrued.Minor())
	}

	// Day 11 is a second day at the new rate: 98_630_136 + 24_657_534.
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, accrualStart.AddDate(0, 0, 11)))
	after, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued a day past the repricing", after.Accrued, interest.Accrued(123_287_670))
}

// TestOverdraftRepricingDoesNotRewindTheAccrualWindow pins the other direction
// of the same seam: a repricing entered while LastAccrualDate is already AHEAD
// of the wall clock.
func TestOverdraftRepricingDoesNotRewindTheAccrualWindow(t *testing.T) {
	ctx := context.Background()
	reg, book, sub, acct, clock := newOverdraftAccount(t)

	postTo(t, reg, book, sub, acct, 20_000, ledger.Debit, accrualStart)

	// An end-of-day run for day 100, while the clock still reads day 0. One
	// hundred days at 15% on €200: 100 × 8_219_178 = 821_917_800 -> 821 cents.
	ahead := accrualStart.AddDate(0, 0, 100)
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, ahead))
	charged, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued through day 100", charged.Accrued, interest.Accrued(821_917_800))

	// Reprice from day 100, entered while the wall clock reads day 0 — the entry date a
	// hundred days behind the effective date, and behind what has already been charged.
	// Day 100 has been accrued, so the row is retroactive by one day.
	clock.set(accrualStart)
	row, err := setTerms(ctx, reg, acct.ID, 50_000, 450_000, 0, interest.ACT365, ahead)
	assertNoError(t, err)
	if !row.EffectiveFrom.Equal(ahead) {
		t.Errorf("row effective from %s, want %s", row.EffectiveFrom, ahead)
	}
	if !row.CreatedAt.Equal(accrualStart) {
		t.Errorf("row created at %s, want the wall clock %s", row.CreatedAt, accrualStart)
	}

	repriced, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	if repriced.LastAccrualDate.Before(ahead) {
		t.Errorf("LastAccrualDate rewound to %s, want no earlier than %s",
			repriced.LastAccrualDate, ahead)
	}
	assertEqual(t, "accrued across the repricing", repriced.Accrued, interest.Accrued(821_917_800))

	// The proof it is not double-charged: the next run adds ONE day and reprices
	// ONE day, not a hundred and one of either. €200 at 45% is 24_657_534 a day,
	// at 15% it is 8_219_178.
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, ahead.AddDate(0, 0, 1)))
	after, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued a day past the repricing", after.Accrued, interest.Accrued(863_013_690))

	receivable, err := book.BookBalance(ctx, owed(t, reg, after.ID))
	assertNoError(t, err)
	// 863_013_690 sub-minor units rounds to 863 cents.
	assertEqual(t, "receivable after the repricing", receivable, ledger.Amount(863))
}

// TestOverdraftAccrualUnderThirty360SkipsThe31st is the only 30/360 coverage on
// the deposit path, and interest.Recompute's day-at-a-time walk leans on that
// convention: under 30/360-US the 31st collapses onto the 30th, so a day can
// count as no days at all, and what a window comes to depends on how it is cut.
func TestOverdraftAccrualUnderThirty360SkipsThe31st(t *testing.T) {
	ctx := context.Background()
	clock := &mutableClock{}
	start := time.Date(2026, 1, 29, 0, 0, 0, 0, time.UTC)
	clock.set(start)
	reg, book, sub, prd := newTestRegisterOn(t, clock.now)

	acct, err := reg.OpenAccount(ctx, "Bruno", testAsset, prd, 0)
	assertNoError(t, err)
	_, err = setTerms(ctx, reg, acct.ID, 50_000, 150_000, 0, interest.Thirty360, time.Time{})
	assertNoError(t, err)
	postTo(t, reg, book, sub, acct, 10_000, ledger.Debit, start)

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
	// still counts for nothing. 8_333_332 also pins the day-by-day walk — one call
	// over the whole window would be 10_000 × 150_000 × 2 / 360 = 8_333_333, a
	// unit more, because Accrue divides once per call.
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)))
	got, err = reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued 31 January -> 1 February", got.Accrued, interest.Accrued(8_333_332))
}

// TestCheckCredit_RefusesOnlyAClosedAccount is the whole status matrix from the
// receiving side, deliberately far more permissive than CheckWithdrawal's.
func TestCheckCredit_RefusesOnlyAClosedAccount(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)

	active, _ := reg.OpenAccount(ctx, "Active", testAsset, prd, 0)
	assertNoError(t, reg.CheckCredit(ctx, active.ID))

	dormant, _ := reg.OpenAccount(ctx, "Dormant", testAsset, prd, 0)
	assertNoError(t, reg.MarkDormant(ctx, dormant.ID))
	assertNoError(t, reg.CheckCredit(ctx, dormant.ID))

	frozen, _ := reg.OpenAccount(ctx, "Frozen", testAsset, prd, 0)
	assertNoError(t, reg.Freeze(ctx, frozen.ID))
	assertNoError(t, reg.CheckCredit(ctx, frozen.ID))

	closed, _ := reg.OpenAccount(ctx, "Closed", testAsset, prd, 0)
	assertNoError(t, reg.Close(ctx, closed.ID))
	assertError(t, reg.CheckCredit(ctx, closed.ID), ErrAccountClosed)

	// An unknown account is not creditable either, and says so as itself.
	assertError(t, reg.CheckCredit(ctx, "dep_nope"), ErrAccountNotFound)
}

// TestCheckWithdrawal_NamesDormancy pins the error a blocked debit on a dormant
// account reports.
func TestCheckWithdrawal_NamesDormancy(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits, prd := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)
	alice, _ := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
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

// ---------------------------------------------------------------------------
// The catalogue: what a product binding buys
// ---------------------------------------------------------------------------

// One published version reprices every account bound to the product, per day,
// with no per-account write at all. This is the capability the catalogue exists
// for, and nothing before it could express the change.
func TestPublishingAVersionRepricesEveryFloatingAccount(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, cat, book, sub := newTestRegisterWithCatalogue(t, clock.now)

	basic, err := cat.CreateProduct(ctx, "Basic Current Account", product.CurrentAccount)
	assertNoError(t, err)
	_, err = cat.DraftVersion(ctx, basic.ID, day(0), product.OverdraftPricing{Rate: 120_000, DayCount: interest.ACT365})
	assertNoError(t, err)
	_, err = cat.PublishVersion(ctx, basic.ID, day(0))
	assertNoError(t, err)

	const drawn ledger.Amount = 100_000
	one, err := reg.OpenAccount(ctx, "Bruno", testAsset, basic.ID, 500_000)
	assertNoError(t, err)
	two, err := reg.OpenAccount(ctx, "Bella", testAsset, basic.ID, 500_000)
	assertNoError(t, err)
	overdrawValueDated(t, reg, book, sub, one, drawn, day(0), day(0))
	overdrawValueDated(t, reg, book, sub, two, drawn, day(0), day(0))

	// Repriced from day 30, published on day 10 — forward-dated, which is the
	// only direction the catalogue allows.
	clock.set(day(10))
	_, err = cat.DraftVersion(ctx, basic.ID, day(30), product.OverdraftPricing{Rate: 180_000, DayCount: interest.ACT365})
	assertNoError(t, err)
	_, err = cat.PublishVersion(ctx, basic.ID, day(30))
	assertNoError(t, err)

	clock.set(day(60))
	for _, acct := range []Account{one, two} {
		assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(60)))
	}

	// Thirty days at 12%, thirty at 18% — for both accounts, from one row.
	want := expectedFromTimeline(day(0), 60, interest.ACT365,
		func(int) ledger.Amount { return drawn },
		func(d int) interest.Rate {
			if d < 30 {
				return 120_000
			}
			return 180_000
		})
	for _, acct := range []Account{one, two} {
		got, err := reg.GetAccount(ctx, acct.ID)
		assertNoError(t, err)
		assertEqual(t, "accrued for "+string(acct.ID), got.AccruedGross, want)
	}
}

// A negotiated rate is one customer's price instead of the product's and outranks a
// product reprice underneath it. Clearing it puts the account back on the product, at
// whatever the product costs by then.
func TestAnOverlayOutranksTheProductAndClearsBackToIt(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, cat, book, sub := newTestRegisterWithCatalogue(t, clock.now)

	basic, err := cat.CreateProduct(ctx, "Basic", product.CurrentAccount)
	assertNoError(t, err)
	for _, v := range []struct {
		from time.Time
		rate interest.Rate
	}{{day(0), 120_000}, {day(30), 180_000}} {
		_, err = cat.DraftVersion(ctx, basic.ID, v.from, product.OverdraftPricing{Rate: v.rate, DayCount: interest.ACT365})
		assertNoError(t, err)
		_, err = cat.PublishVersion(ctx, basic.ID, v.from)
		assertNoError(t, err)
	}

	const drawn ledger.Amount = 100_000
	acct, err := reg.OpenAccount(ctx, "Bruno", testAsset, basic.ID, 500_000)
	assertNoError(t, err)
	overdrawValueDated(t, reg, book, sub, acct, drawn, day(0), day(0))

	// Negotiated 9% from day 10, cleared from day 40.
	_, err = reg.SetOverdraftPricingOverlay(ctx, acct.ID,
		&product.OverdraftPricing{Rate: 90_000, DayCount: interest.ACT365}, day(10))
	assertNoError(t, err)
	_, err = reg.SetOverdraftPricingOverlay(ctx, acct.ID, nil, day(40))
	assertNoError(t, err)

	clock.set(day(50))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(50)))

	want := expectedFromTimeline(day(0), 50, interest.ACT365,
		func(int) ledger.Amount { return drawn },
		func(d int) interest.Rate {
			switch {
			case d < 10:
				return 120_000 // floating
			case d < 40:
				return 90_000 // negotiated, through the day-30 reprice
			default:
				return 180_000 // cleared, back onto the repriced product
			}
		})
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued", got.AccruedGross, want)
}

// The limit is pinned: publishing a version cannot change what a customer may
// spend. This is the central claim of the design, and it is cheap to pin.
func TestPublishingAVersionCannotMoveTheAvailableBalance(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, cat, _, _ := newTestRegisterWithCatalogue(t, clock.now)

	basic, err := cat.CreateProduct(ctx, "Basic", product.CurrentAccount)
	assertNoError(t, err)
	_, err = cat.DraftVersion(ctx, basic.ID, day(0), product.OverdraftPricing{Rate: 120_000})
	assertNoError(t, err)
	_, err = cat.PublishVersion(ctx, basic.ID, day(0))
	assertNoError(t, err)

	acct, err := reg.OpenAccount(ctx, "Bruno", testAsset, basic.ID, 500_000)
	assertNoError(t, err)
	before, err := reg.GetBalance(ctx, acct.ID)
	assertNoError(t, err)

	clock.set(day(1))
	_, err = cat.DraftVersion(ctx, basic.ID, day(2), product.OverdraftPricing{Rate: 900_000})
	assertNoError(t, err)
	_, err = cat.PublishVersion(ctx, basic.ID, day(2))
	assertNoError(t, err)

	clock.set(day(3))
	after, err := reg.GetBalance(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "available", after.Available, before.Available)

	// And raising the limit is a per-account decision that does move it.
	_, err = reg.SetOverdraftLimit(ctx, acct.ID, 800_000, day(3))
	assertNoError(t, err)
	raised, err := reg.GetBalance(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "available after a limit rise", raised.Available, before.Available+300_000)
}

// A version edited in the database stops the accrual rather than pricing a day
// from a row nobody published. This is the property the content hash exists for
// and the only test that proves it.
func TestATamperedVersionStopsTheAccrual(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, cat, book, sub := newTestRegisterWithCatalogue(t, clock.now)

	basic, err := cat.CreateProduct(ctx, "Basic", product.CurrentAccount)
	assertNoError(t, err)
	_, err = cat.DraftVersion(ctx, basic.ID, day(0), product.OverdraftPricing{Rate: 120_000, DayCount: interest.ACT365})
	assertNoError(t, err)
	published, err := cat.PublishVersion(ctx, basic.ID, day(0))
	assertNoError(t, err)

	acct, err := reg.OpenAccount(ctx, "Bruno", testAsset, basic.ID, 500_000)
	assertNoError(t, err)
	overdrawValueDated(t, reg, book, sub, acct, 100_000, day(0), day(0))

	// Straight through the store, as a manual UPDATE would: the pricing moves
	// and the hash is left behind.
	tampered := published
	tampered.Overdraft.Rate = 900_000
	assertNoError(t, reg.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		return tx.PutProductVersion(ctx, book.ID(), tampered)
	}))

	clock.set(day(10))
	err = reg.AccrueOverdraft(ctx, acct.ID, day(10))
	if !errors.Is(err, product.ErrHashMismatch) {
		t.Fatalf("AccrueOverdraft = %v, want ErrHashMismatch", err)
	}
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "nothing was accrued", got.AccruedGross, interest.Accrued(0))
}

func TestOpenAccountRefusesABadProduct(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, cat, _, _ := newTestRegisterWithCatalogue(t, clock.now)

	if _, err := reg.OpenAccount(ctx, "Bruno", testAsset, "prd_nope", 0); !errors.Is(err, product.ErrProductNotFound) {
		t.Errorf("unknown product: %v, want ErrProductNotFound", err)
	}

	// A product with no published version has no price, so an account opened
	// from it could not resolve a single day. Refusing here is what stops that
	// turning into an accrual failure weeks later.
	unpriced, err := cat.CreateProduct(ctx, "Unpriced", product.CurrentAccount)
	assertNoError(t, err)
	if _, err := reg.OpenAccount(ctx, "Bruno", testAsset, unpriced.ID, 0); !errors.Is(err, product.ErrVersionNotFound) {
		t.Errorf("unpriced product: %v, want ErrVersionNotFound", err)
	}

	// Retired takes it off sale without unpricing the accounts already on it —
	// which TestRetireTakesAProductOffSaleWithoutUnpricingIt pins in product.
	basic, err := cat.CreateProduct(ctx, "Basic", product.CurrentAccount)
	assertNoError(t, err)
	_, err = cat.DraftVersion(ctx, basic.ID, day(0), product.OverdraftPricing{Rate: 120_000})
	assertNoError(t, err)
	_, err = cat.PublishVersion(ctx, basic.ID, day(0))
	assertNoError(t, err)
	acct, err := reg.OpenAccount(ctx, "Bruno", testAsset, basic.ID, 0)
	assertNoError(t, err)
	_, err = cat.RetireProduct(ctx, basic.ID)
	assertNoError(t, err)

	if _, err := reg.OpenAccount(ctx, "Bella", testAsset, basic.ID, 0); !errors.Is(err, product.ErrProductRetired) {
		t.Errorf("retired product: %v, want ErrProductRetired", err)
	}
	if _, err := reg.GetAccountWithTerms(ctx, acct.ID); err != nil {
		t.Errorf("an account on a retired product stopped resolving: %v", err)
	}
}

// Migrating between products is a forward-dated row like any other, so the days
// before it still resolve against the product that priced them.
func TestChangeProductPricesEachDayFromTheProductInForceOnIt(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, cat, book, sub := newTestRegisterWithCatalogue(t, clock.now)

	basic, err := cat.CreateProduct(ctx, "Basic", product.CurrentAccount)
	assertNoError(t, err)
	premium, err := cat.CreateProduct(ctx, "Premium", product.CurrentAccount)
	assertNoError(t, err)
	for _, p := range []struct {
		id   product.ID
		rate interest.Rate
	}{{basic.ID, 120_000}, {premium.ID, 70_000}} {
		_, err = cat.DraftVersion(ctx, p.id, day(0), product.OverdraftPricing{Rate: p.rate, DayCount: interest.ACT365})
		assertNoError(t, err)
		_, err = cat.PublishVersion(ctx, p.id, day(0))
		assertNoError(t, err)
	}

	const drawn ledger.Amount = 100_000
	acct, err := reg.OpenAccount(ctx, "Bruno", testAsset, basic.ID, 500_000)
	assertNoError(t, err)
	overdrawValueDated(t, reg, book, sub, acct, drawn, day(0), day(0))

	_, err = reg.ChangeProduct(ctx, acct.ID, premium.ID, day(20))
	assertNoError(t, err)

	clock.set(day(40))
	assertNoError(t, reg.AccrueOverdraft(ctx, acct.ID, day(40)))

	want := expectedFromTimeline(day(0), 40, interest.ACT365,
		func(int) ledger.Amount { return drawn },
		func(d int) interest.Rate {
			if d < 20 {
				return 120_000
			}
			return 70_000
		})
	got, err := reg.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	assertEqual(t, "accrued", got.AccruedGross, want)
}

// A limit change carries the account's other terms forward, because each row is
// a complete statement of the account's own terms from its day. Losing the
// overlay here would silently reprice the customer.
func TestALimitChangeKeepsTheOverlayAndTheProduct(t *testing.T) {
	ctx := context.Background()
	origin := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	day := func(n int) time.Time { return origin.AddDate(0, 0, n) }

	clock := &mutableClock{at: day(0)}
	reg, cat, _, _ := newTestRegisterWithCatalogue(t, clock.now)

	basic, err := cat.CreateProduct(ctx, "Basic", product.CurrentAccount)
	assertNoError(t, err)
	_, err = cat.DraftVersion(ctx, basic.ID, day(0), product.OverdraftPricing{Rate: 120_000, DayCount: interest.ACT365})
	assertNoError(t, err)
	_, err = cat.PublishVersion(ctx, basic.ID, day(0))
	assertNoError(t, err)

	acct, err := reg.OpenAccount(ctx, "Bruno", testAsset, basic.ID, 500_000)
	assertNoError(t, err)
	_, err = reg.SetOverdraftPricingOverlay(ctx, acct.ID,
		&product.OverdraftPricing{Rate: 90_000, DayCount: interest.ACT365}, day(5))
	assertNoError(t, err)

	clock.set(day(10))
	row, err := reg.SetOverdraftLimit(ctx, acct.ID, 800_000, day(10))
	assertNoError(t, err)
	if row.Pricing == nil {
		t.Fatal("the limit change dropped the negotiated overlay")
	}
	assertEqual(t, "overlay rate carried forward", int64(row.Pricing.Rate), int64(90_000))
	assertEqual(t, "product carried forward", string(row.ProductID), string(basic.ID))
	assertEqual(t, "the new limit", row.OverdraftLimit, ledger.Amount(800_000))
}

// testIssuer is what every register in this package mints under: one German
// bank code, because this suite is about the register and not about addressing.
// The one test that cares which country an address is in says so itself.
var testIssuer = iban.Issuer{Country: iban.DE, BankCode: "99900001"}
