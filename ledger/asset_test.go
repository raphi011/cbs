package ledger_test

import (
	"context"
	"errors"
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
