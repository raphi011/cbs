package ledger_test

import (
	"context"
	"errors"
	"testing"

	. "github.com/raphi011/cbs/ledger"
)

// The slots these tests map. Real ones are declared in the layer that owns the
// flow; these are the two shapes that matter here — a control slot that holds a
// balance and takes no product, and an income slot that carries none and does.
var (
	principalSlot = Slot{Key: "test.principal", Type: Liability, Control: true}
	incomeSlot    = Slot{Key: "test.income", Type: Revenue, ByProduct: true}
)

func mapSlot(t *testing.T, book *Book, product string, slot Slot, account AccountID) error {
	t.Helper()
	return book.Store().Update(context.Background(), func(ctx context.Context, tx Tx) error {
		return book.MapSlotTx(ctx, tx, product, slot, testAsset, account)
	})
}

func slotAccount(t *testing.T, book *Book, product string, slot Slot) (AccountID, error) {
	t.Helper()
	var out AccountID
	err := book.Store().View(context.Background(), func(ctx context.Context, tx Tx) error {
		var err error
		out, err = book.SlotAccountTx(ctx, tx, product, slot, testAsset)
		return err
	})
	return out, err
}

// A flow asks for a role and gets an account. That is the whole point of the
// mapping: neither the deposit layer nor the lending one names an account, and
// an operator who repoints a slot moves where the flow posts without either of
// them being rebuilt.
func TestASlotResolvesToTheAccountItWasPointedAt(t *testing.T) {
	book := testBook(t)
	deposits, _ := pooledChart(t, book)

	if _, err := slotAccount(t, book, "", principalSlot); !errors.Is(err, ErrSlotNotMapped) {
		t.Fatalf("an unmapped slot resolved to %v, want ErrSlotNotMapped", err)
	}
	assertNoError(t, mapSlot(t, book, "", principalSlot, deposits.ID))

	got, err := slotAccount(t, book, "", principalSlot)
	assertNoError(t, err)
	assertEqual(t, "the mapped account", string(got), string(deposits.ID))
}

// The account has to be the kind the slot needs, and it is checked HERE, at the
// write. A Revenue account filling a Liability slot posts the right amount on
// the wrong side of the balance sheet; a plain account filling a control slot is
// refused at every posting from then on, none of which names the configuration
// change that caused it.
func TestMapSlotRefusesAnAccountTheSlotCannotUse(t *testing.T) {
	book := testBook(t)
	deposits, vault := pooledChart(t, book)

	// Right type, wrong pooling: Vault Cash is an Asset AND plain, so this
	// fails the type test first — the case worth having is the one that differs
	// ONLY in pooling.
	if err := mapSlot(t, book, "", principalSlot, vault.ID); !errors.Is(err, ErrSlotAccountMismatch) {
		t.Errorf("mapping a plain Asset to a control Liability slot: %v, want ErrSlotAccountMismatch", err)
	}
	plainLiability := plainAccount(t, book, "Clearing Suspense", Liability)
	if err := mapSlot(t, book, "", principalSlot, plainLiability.ID); !errors.Is(err, ErrSlotAccountMismatch) {
		t.Errorf("mapping a plain account to a control slot: %v, want ErrSlotAccountMismatch", err)
	}
	if err := mapSlot(t, book, "", incomeSlot, deposits.ID); !errors.Is(err, ErrSlotAccountMismatch) {
		t.Errorf("mapping a Liability to a Revenue slot: %v, want ErrSlotAccountMismatch", err)
	}
	if err := mapSlot(t, book, "", principalSlot, "200.nope.001"); !errors.Is(err, ErrAccountNotFound) {
		t.Errorf("mapping an account that does not exist: %v, want ErrAccountNotFound", err)
	}

	// And nothing was written by any of them.
	if _, err := slotAccount(t, book, "", principalSlot); !errors.Is(err, ErrSlotNotMapped) {
		t.Errorf("a refused mapping left a row behind: %v", err)
	}
}

// A product may earn into its own revenue line and falls back to the bank's when
// it has none. One row per exception rather than a copy of the whole mapping per
// product.
func TestAProductOverridesOneSlotAndInheritsTheRest(t *testing.T) {
	book := testBook(t)
	income := plainAccount(t, book, "Interest Income", Revenue)
	savingsIncome := plainAccount(t, book, "Interest Income: Savings", Revenue)

	assertNoError(t, mapSlot(t, book, "", incomeSlot, income.ID))
	assertNoError(t, mapSlot(t, book, "prd_savings", incomeSlot, savingsIncome.ID))

	got, err := slotAccount(t, book, "prd_savings", incomeSlot)
	assertNoError(t, err)
	assertEqual(t, "the product's own line", string(got), string(savingsIncome.ID))

	inherited, err := slotAccount(t, book, "prd_basic", incomeSlot)
	assertNoError(t, err)
	assertEqual(t, "a product with no line of its own", string(inherited), string(income.ID))
}

// A slot that HOLDS A BALANCE takes no product-specific row, and the refusal is
// the whole reason Slot.ByProduct exists. Money already posted under an obligor
// stays where it was posted: if a later resolution answered with another line,
// the balance anybody read would be the second half only, and moving the first
// half is a reclassification journal this system does not have.
func TestAProductMayNotOverrideALineThatHoldsABalance(t *testing.T) {
	book := testBook(t)
	deposits, _ := pooledChart(t, book)
	assertNoError(t, mapSlot(t, book, "", principalSlot, deposits.ID))

	other := createControlAccount(t, book, deposits.SubledgerID, "Savings Deposits", Liability)
	if err := mapSlot(t, book, "prd_savings", principalSlot, other.ID); !errors.Is(err, ErrSlotNotProductScoped) {
		t.Fatalf("a product-scoped balance line: %v, want ErrSlotNotProductScoped", err)
	}

	// The bank-wide row is what every product still resolves, so no customer's
	// money has moved.
	got, err := slotAccount(t, book, "prd_savings", principalSlot)
	assertNoError(t, err)
	assertEqual(t, "what the product resolves", string(got), string(deposits.ID))
}

// plainAccount opens one account of the given type in its own folder, for the
// tests above that need a wrong-shaped candidate.
func plainAccount(t *testing.T, book *Book, name string, typ AccountType) Account {
	t.Helper()
	ctx := context.Background()
	ledgers, err := book.ListLedgers(ctx)
	assertNoError(t, err)
	if len(ledgers) == 0 {
		l, err := book.CreateLedger(ctx, "General Ledger")
		assertNoError(t, err)
		ledgers = append(ledgers, l)
	}
	sub, err := book.CreateSubledger(ctx, ledgers[0].ID, name+" folder")
	assertNoError(t, err)
	acct, err := book.CreateAccount(ctx, sub.ID, name, typ, testAsset)
	assertNoError(t, err)
	return acct
}
