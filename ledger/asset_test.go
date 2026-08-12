package ledger_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/raphi011/cbs/ledger"
)

// The known assets are a package-level list in code rather than rows, so what
// there is to test is the list itself and the rule that reads it.

func TestLookupAssetReturnsTheDefinition(t *testing.T) {
	got, err := ledger.LookupAsset("BTC")
	if err != nil {
		t.Fatalf("LookupAsset(BTC): %v", err)
	}
	want := ledger.AssetDef{Code: "BTC", Name: "Bitcoin", Scale: 8, Class: ledger.Crypto}
	if got != want {
		t.Errorf("LookupAsset(BTC) = %+v, want %+v", got, want)
	}
}

func TestLookupAssetUnknownCode(t *testing.T) {
	_, err := ledger.LookupAsset("DOGE")
	if !errors.Is(err, ledger.ErrAssetNotFound) {
		t.Errorf("LookupAsset(DOGE) error = %v, want ErrAssetNotFound", err)
	}
	if !strings.Contains(err.Error(), "DOGE") {
		t.Errorf("error %q does not name the code that was asked for", err)
	}
}

// The cap exists because Amount is an int64: at 18 decimal places it would hold
// 9.2 whole units.
func TestKnownAssetsRespectMaxScale(t *testing.T) {
	assets := ledger.Assets()
	if len(assets) == 0 {
		t.Fatal("no known assets")
	}
	seen := make(map[ledger.AssetCode]bool, len(assets))
	for _, a := range assets {
		if a.Scale > ledger.MaxAssetScale {
			t.Errorf("%s has scale %d, above MaxAssetScale %d", a.Code, a.Scale, ledger.MaxAssetScale)
		}
		if a.Code == "" || a.Name == "" {
			t.Errorf("asset %+v has an empty code or name", a)
		}
		if seen[a.Code] {
			t.Errorf("asset code %s appears twice", a.Code)
		}
		seen[a.Code] = true
	}
}

// Assets returns a copy, so a caller rendering the list cannot edit the
// definitions CreateAccount validates against.
func TestAssetsReturnsACopy(t *testing.T) {
	ledger.Assets()[0].Scale = 99

	eur, err := ledger.LookupAsset("EUR")
	if err != nil {
		t.Fatalf("LookupAsset(EUR): %v", err)
	}
	if eur.Scale != 2 {
		t.Errorf("EUR scale = %d after mutating the returned slice, want 2", eur.Scale)
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

func TestCreateAccountRejectsUnknownAsset(t *testing.T) {
	book := testBook(t)
	sl := newSubledger(t, book)

	_, err := book.CreateAccount(context.Background(), sl.ID, "Dogecoin Custody", ledger.Asset, "DOGE")
	if !errors.Is(err, ledger.ErrAssetNotFound) {
		t.Errorf("CreateAccount with an unknown asset = %v, want ErrAssetNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// Per-asset balance
// ---------------------------------------------------------------------------

// newAccountIn creates an account named name, of type typ, denominated in
// asset, in subledger sl.
func newAccountIn(t *testing.T, book *ledger.Book, sl ledger.Subledger, name string, asset ledger.AssetCode, typ ledger.AccountType) ledger.Account {
	t.Helper()

	acct, err := book.CreateAccount(context.Background(), sl.ID, name, typ, asset)
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

	sl := newSubledger(t, book)
	eur := newAccountIn(t, book, sl, "Customer EUR", "EUR", ledger.Liability)
	btc := newAccountIn(t, book, sl, "Customer BTC", "BTC", ledger.Liability)

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
	// EUR specifically, not "either of the two". validateBalance walks its assets
	// in first-appearance order precisely so the message is deterministic, and the
	// EUR leg is first here; an assertion that passed on either code would not
	// notice if that ordering were lost.
	if !strings.Contains(err.Error(), "EUR") {
		t.Errorf("error %q does not name EUR, the first unbalanced asset", err)
	}
	if strings.Contains(err.Error(), "BTC") {
		t.Errorf("error %q names BTC; it should stop at the first offender", err)
	}
}

// The shape an FX trade takes under the per-asset rule: each asset balances
// through its own position account, so no rate is needed to validate it.
func TestPostAcceptsTwoAssetsBalancedThroughPositionAccounts(t *testing.T) {
	book := testBook(t)
	ctx := context.Background()

	// One subledger: an FX desk's four legs sit in the same chart of accounts.
	sl := newSubledger(t, book)
	eurCust := newAccountIn(t, book, sl, "Customer EUR", "EUR", ledger.Liability)
	eurPos := newAccountIn(t, book, sl, "EUR Position", "EUR", ledger.Liability)
	btcCust := newAccountIn(t, book, sl, "Customer BTC", "BTC", ledger.Liability)
	btcPos := newAccountIn(t, book, sl, "BTC Position", "BTC", ledger.Liability)

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

	sl := newSubledger(t, book)
	a := newAccountIn(t, book, sl, "Customer A", "EUR", ledger.Liability)
	b := newAccountIn(t, book, sl, "Customer B", "EUR", ledger.Liability)

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
