package ledger_test

import (
	"context"
	"errors"
	"testing"

	"github.com/raphi011/cbs/ledger"
)

func TestCreateAssetRoundTrips(t *testing.T) {
	book := testBook(t)
	ctx := context.Background()

	want, err := book.CreateAsset(ctx, "EUR", "Euro", 2, ledger.Fiat)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	if want.Code != "EUR" || want.Scale != 2 || want.Class != ledger.Fiat {
		t.Fatalf("CreateAsset returned %+v", want)
	}

	got, err := book.GetAsset(ctx, "EUR")
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

	if _, err := book.CreateAsset(ctx, "EUR", "Euro", 2, ledger.Fiat); err != nil {
		t.Fatalf("first CreateAsset: %v", err)
	}
	_, err := book.CreateAsset(ctx, "EUR", "Euro again", 2, ledger.Fiat)
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
