package ledger_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/raphi011/cbs/ledger"
)

// A test book already holds the euro (see testBook), so the tests that
// register an asset for the first time use the dollar instead.

func TestCreateAssetRoundTrips(t *testing.T) {
	book := testBook(t)
	ctx := context.Background()

	want, err := book.CreateAsset(ctx, "USD", "US Dollar", 2, ledger.Fiat)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	if want.Code != "USD" || want.Scale != 2 || want.Class != ledger.Fiat {
		t.Fatalf("CreateAsset returned %+v", want)
	}

	got, err := book.GetAsset(ctx, "USD")
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if got != want {
		t.Errorf("GetAsset = %+v, want %+v", got, want)
	}
}

func TestCreateAssetRejectsScaleAboveNine(t *testing.T) {
	book := testBook(t)

	// 18 decimals is what an 18-decimal crypto asset would want. int64
	// holds 9.2 units at that scale, so the registry refuses it rather
	// than letting the overflow surface as a wrong balance later.
	_, err := book.CreateAsset(context.Background(), "ETH", "Ether", 18, ledger.Crypto)
	if !errors.Is(err, ledger.ErrInvalidScale) {
		t.Errorf("CreateAsset(scale 18) error = %v, want ErrInvalidScale", err)
	}
}

func TestCreateAssetRejectsDuplicateCode(t *testing.T) {
	book := testBook(t)
	ctx := context.Background()

	if _, err := book.CreateAsset(ctx, "USD", "US Dollar", 2, ledger.Fiat); err != nil {
		t.Fatalf("first CreateAsset: %v", err)
	}
	_, err := book.CreateAsset(ctx, "USD", "US Dollar again", 2, ledger.Fiat)
	if !errors.Is(err, ledger.ErrDuplicateAsset) {
		t.Errorf("duplicate CreateAsset error = %v, want ErrDuplicateAsset", err)
	}
}

func TestGetAssetUnknownCode(t *testing.T) {
	book := testBook(t)

	_, err := book.GetAsset(context.Background(), "BTC")
	if !errors.Is(err, ledger.ErrAssetNotFound) {
		t.Errorf("GetAsset error = %v, want ErrAssetNotFound", err)
	}
}

// newSubledger creates a ledger and a subledger in book and returns the
// subledger, so an account test has somewhere to put an account.
func newSubledger(t *testing.T, book *ledger.Book) ledger.Subledger {
	t.Helper()
	ctx := context.Background()

	l, err := book.CreateLedger(ctx, "General Ledger")
	if err != nil {
		t.Fatalf("CreateLedger: %v", err)
	}
	sl, err := book.CreateSubledger(ctx, l.ID, "Customer Deposits")
	if err != nil {
		t.Fatalf("CreateSubledger: %v", err)
	}
	return sl
}

func TestCreateAccountRecordsItsAsset(t *testing.T) {
	book := testBook(t)
	ctx := context.Background()

	if _, err := book.CreateAsset(ctx, "BTC", "Bitcoin", 8, ledger.Crypto); err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	sl := newSubledger(t, book)

	acct, err := book.CreateAccount(ctx, sl.ID, "Crypto Custody", ledger.Asset, "BTC")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if acct.Asset != "BTC" {
		t.Errorf("account asset = %q, want BTC", acct.Asset)
	}

	got, err := book.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got.Asset != "BTC" {
		t.Errorf("reloaded account asset = %q, want BTC", got.Asset)
	}
}

func TestCreateAccountRejectsUnregisteredAsset(t *testing.T) {
	book := testBook(t)
	sl := newSubledger(t, book)

	_, err := book.CreateAccount(context.Background(), sl.ID, "Dogecoin Custody", ledger.Asset, "DOGE")
	if !errors.Is(err, ledger.ErrAssetNotFound) {
		t.Errorf("CreateAccount with unregistered asset = %v, want ErrAssetNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Per-asset balance
// ---------------------------------------------------------------------------

// newAccountIn registers asset in book (scale 2 for EUR, 8 for BTC) if it is
// not registered yet — testBook already registers EUR, so this ignores
// ErrDuplicateAsset rather than treating it as a fixture failure — then
// creates an account of type typ, denominated in asset, in a fresh subledger.
func newAccountIn(t *testing.T, book *ledger.Book, asset ledger.AssetCode, typ ledger.AccountType) ledger.Account {
	t.Helper()
	ctx := context.Background()

	scale, class := uint8(2), ledger.Fiat
	if asset == "BTC" {
		scale, class = 8, ledger.Crypto
	}
	if _, err := book.CreateAsset(ctx, asset, string(asset), scale, class); err != nil && !errors.Is(err, ledger.ErrDuplicateAsset) {
		t.Fatalf("CreateAsset(%s): %v", asset, err)
	}

	sl := newSubledger(t, book)
	acct, err := book.CreateAccount(ctx, sl.ID, string(asset)+" account", typ, asset)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return acct
}

// A cross-asset transfer balances globally — 100 debit, 100 credit — and is
// still nonsense: it turns euros into bitcoin at an implied rate of 1. The
// per-asset rule is what rejects it.
func TestPostRejectsCrossAssetTransfer(t *testing.T) {
	book := testBook(t)
	ctx := context.Background()

	eur := newAccountIn(t, book, "EUR", ledger.Liability)
	btc := newAccountIn(t, book, "BTC", ledger.Liability)

	_, err := book.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "turn euros into bitcoin",
		Entries: []ledger.Entry{
			{AccountID: eur.ID, Amount: 100, Direction: ledger.Debit},
			{AccountID: btc.ID, Amount: 100, Direction: ledger.Credit},
		},
	})
	if !errors.Is(err, ledger.ErrUnbalancedAsset) {
		t.Fatalf("cross-asset PostTransaction error = %v, want ErrUnbalancedAsset", err)
	}
	if !strings.Contains(err.Error(), "EUR") && !strings.Contains(err.Error(), "BTC") {
		t.Errorf("error %q does not name the offending asset", err)
	}
}

// The shape an FX trade takes under the per-asset rule: each asset balances
// through its own position account, so no rate is needed to validate it.
func TestPostAcceptsTwoAssetsBalancedThroughPositionAccounts(t *testing.T) {
	book := testBook(t)
	ctx := context.Background()

	eurCust := newAccountIn(t, book, "EUR", ledger.Liability)
	eurPos := newAccountIn(t, book, "EUR", ledger.Liability)
	btcCust := newAccountIn(t, book, "BTC", ledger.Liability)
	btcPos := newAccountIn(t, book, "BTC", ledger.Liability)

	if _, err := book.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "FX: customer sells 100 EUR for 200 BTC units",
		Entries: []ledger.Entry{
			{AccountID: eurCust.ID, Amount: 100, Direction: ledger.Debit},
			{AccountID: eurPos.ID, Amount: 100, Direction: ledger.Credit},
			{AccountID: btcPos.ID, Amount: 200, Direction: ledger.Debit},
			{AccountID: btcCust.ID, Amount: 200, Direction: ledger.Credit},
		},
	}); err != nil {
		t.Fatalf("balanced two-asset PostTransaction: %v", err)
	}
}

// The pre-existing single-asset failure must keep its own error.
func TestPostStillRejectsSingleAssetImbalance(t *testing.T) {
	book := testBook(t)
	ctx := context.Background()

	a := newAccountIn(t, book, "EUR", ledger.Liability)
	b := newAccountIn(t, book, "EUR", ledger.Liability)

	_, err := book.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "lopsided",
		Entries: []ledger.Entry{
			{AccountID: a.ID, Amount: 100, Direction: ledger.Debit},
			{AccountID: b.ID, Amount: 90, Direction: ledger.Credit},
		},
	})
	if !errors.Is(err, ledger.ErrUnbalancedAsset) {
		t.Errorf("imbalanced PostTransaction error = %v, want ErrUnbalancedAsset", err)
	}
}
