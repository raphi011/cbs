// This file's tests live in package deposit_test for register_test.go's stated
// reason: they build a Register over store/testenv, which reaches store/sqlite,
// which imports deposit.
package deposit_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/store/sqlite"
	"github.com/raphi011/cbs/store/testenv"
)

// twoCustomers opens two accounts in one bank's register, funds the first, and
// returns the register, the payer and the payee.
//
// ONE register, because that is the whole of the arrangement a book transfer
// needs: a register spans one book, so both accounts are its own by
// construction and there is no cross-bank case for these tests to describe.
func twoCustomers(t *testing.T, limit ledger.Amount) (*Register, Account, Account) {
	t.Helper()
	ctx := context.Background()
	reg, book, deposits, prd := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)

	payer, err := reg.OpenAccount(ctx, deposits, "Alice", testAsset, prd, limit)
	assertNoError(t, err)
	payee, err := reg.OpenAccount(ctx, deposits, "Bob", testAsset, prd, 0)
	assertNoError(t, err)
	fund(t, reg, cash, payer, 1000)
	return reg, payer, payee
}

// assertBooks fails unless the two accounts hold exactly these book balances.
func assertBooks(t *testing.T, reg *Register, payer, payee Account, out, in ledger.Amount) {
	t.Helper()
	ctx := context.Background()
	a, err := reg.GetBalance(ctx, payer.ID)
	assertNoError(t, err)
	b, err := reg.GetBalance(ctx, payee.ID)
	assertNoError(t, err)
	assertEqual(t, "the payer's book balance", a.Book, out)
	assertEqual(t, "the payee's book balance", b.Book, in)
}

// ---------------------------------------------------------------------------
// The transfer itself
// ---------------------------------------------------------------------------

func TestATransferMovesMoneyBetweenTwoAccountsInOneBook(t *testing.T) {
	ctx := context.Background()
	reg, payer, payee := twoCustomers(t, 0)

	glTx, err := reg.Transfer(ctx, payer.ID, payee.ID, 250, "rent")
	assertNoError(t, err)

	assertBooks(t, reg, payer, payee, 750, 250)

	// ONE transaction with both legs on it, which is what makes the movement
	// atomic rather than two postings that could disagree.
	if len(glTx.Entries) != 2 {
		t.Fatalf("the transfer posted %d entries, want 2", len(glTx.Entries))
	}
	for _, e := range glTx.Entries {
		switch {
		case e.AccountID == payer.GLAccount && e.Direction == ledger.Debit:
		case e.AccountID == payee.GLAccount && e.Direction == ledger.Credit:
		default:
			t.Errorf("unexpected leg: %s %v %d", e.AccountID, e.Direction, e.Amount)
		}
		assertEqual(t, "leg amount", e.Amount, 250)
	}
}

func TestATransferIsOneAuditEventKeyedByThePayer(t *testing.T) {
	ctx := context.Background()
	reg, payer, payee := twoCustomers(t, 0)

	glTx, err := reg.Transfer(ctx, payer.ID, payee.ID, 250, "rent")
	assertNoError(t, err)

	events, err := reg.GetAuditLog(ctx)
	assertNoError(t, err)
	var transfers []ledger.AuditEvent
	for _, e := range events {
		if e.Type == ledger.EventTransferPosted {
			transfers = append(transfers, e)
		}
	}
	if len(transfers) != 1 {
		t.Fatalf("got %d transfer.posted events, want 1", len(transfers))
	}
	assertEqual(t, "the event's entity", transfers[0].EntityID, string(payer.ID))

	// The payload names the side the key does not, and the transaction that
	// carries both legs. Without the payee on it the log would say the payer
	// sent money and not where.
	payload := string(transfers[0].Payload)
	for _, want := range []string{string(payee.ID), string(glTx.ID), string(testAsset), "250"} {
		if !strings.Contains(payload, want) {
			t.Errorf("transfer.posted payload = %s, missing %q", payload, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Money out is harder than money in
// ---------------------------------------------------------------------------

func TestATransferMayPushThePayerIntoTheirOverdraft(t *testing.T) {
	ctx := context.Background()
	reg, payer, payee := twoCustomers(t, 500)

	// 1000 on the book and 500 of headroom, so 1200 is a legitimate transfer.
	// The available balance is what a withdrawal is measured against, which is
	// the same rule a card capture follows.
	_, err := reg.Transfer(ctx, payer.ID, payee.ID, 1200, "rent")
	assertNoError(t, err)
	assertBooks(t, reg, payer, payee, -200, 1200)
}

func TestATransferBeyondTheLimitIsRefusedAndPostsNothing(t *testing.T) {
	ctx := context.Background()
	reg, payer, payee := twoCustomers(t, 500)

	_, err := reg.Transfer(ctx, payer.ID, payee.ID, 1501, "rent")
	assertError(t, err, ErrInsufficientAvailable)
	assertBooks(t, reg, payer, payee, 1000, 0)
}

func TestAFrozenPayerCannotSendAndAFrozenPayeeStillReceives(t *testing.T) {
	ctx := context.Background()

	reg, payer, payee := twoCustomers(t, 0)
	assertNoError(t, reg.Freeze(ctx, payer.ID))
	_, err := reg.Transfer(ctx, payer.ID, payee.ID, 100, "rent")
	assertError(t, err, ErrAccountFrozen)
	assertBooks(t, reg, payer, payee, 1000, 0)

	// The other direction, and the asymmetry is the point: the freeze this
	// system models is a DEBIT block, so money owed to the customer keeps
	// arriving while they cannot take any out.
	reg, payer, payee = twoCustomers(t, 0)
	assertNoError(t, reg.Freeze(ctx, payee.ID))
	_, err = reg.Transfer(ctx, payer.ID, payee.ID, 100, "rent")
	assertNoError(t, err)
	assertBooks(t, reg, payer, payee, 900, 100)
}

func TestADormantPayerCannotSendAndADormantPayeeStillReceives(t *testing.T) {
	ctx := context.Background()

	reg, payer, payee := twoCustomers(t, 0)
	assertNoError(t, reg.MarkDormant(ctx, payer.ID))
	_, err := reg.Transfer(ctx, payer.ID, payee.ID, 100, "rent")
	assertError(t, err, ErrAccountDormant)

	// A credit is precisely what revives a dormant account, so refusing this
	// one would strand a payment for want of a customer login. The status is
	// not changed BY the credit — reactivation is its own act — which is what
	// this second half pins.
	reg, payer, payee = twoCustomers(t, 0)
	assertNoError(t, reg.MarkDormant(ctx, payee.ID))
	_, err = reg.Transfer(ctx, payer.ID, payee.ID, 100, "rent")
	assertNoError(t, err)
	assertBooks(t, reg, payer, payee, 900, 100)
}

func TestATransferIntoAClosedAccountIsRefused(t *testing.T) {
	ctx := context.Background()
	reg, payer, payee := twoCustomers(t, 0)
	assertNoError(t, reg.Close(ctx, payee.ID))

	// Closed is the one status that refuses a credit, and it must: closing
	// required a zero balance, so money landing afterwards is stranded — no
	// withdrawal can reach it and closing again cannot clear it.
	_, err := reg.Transfer(ctx, payer.ID, payee.ID, 100, "rent")
	assertError(t, err, ErrAccountClosed)
	assertBooks(t, reg, payer, payee, 1000, 0)
}

// ---------------------------------------------------------------------------
// What a transfer refuses about the request itself
// ---------------------------------------------------------------------------

func TestATransferToTheAccountItCameFromIsRefused(t *testing.T) {
	ctx := context.Background()
	reg, payer, _ := twoCustomers(t, 0)

	_, err := reg.Transfer(ctx, payer.ID, payer.ID, 100, "rent")
	assertError(t, err, ErrSameAccount)

	// It posts nothing and it logs nothing, which is the whole reason it is
	// refused rather than treated as a no-op: a self-cancelling pair of entries
	// would leave a trail saying money moved.
	events, err := reg.GetAuditLog(ctx)
	assertNoError(t, err)
	for _, e := range events {
		if e.Type == ledger.EventTransferPosted {
			t.Fatalf("a refused transfer wrote %s", e.Type)
		}
	}
}

func TestATransferBetweenTwoAssetsIsRefusedByName(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits, prd := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)

	payer, err := reg.OpenAccount(ctx, deposits, "Alice", testAsset, prd, 0)
	assertNoError(t, err)
	payee, err := reg.OpenAccount(ctx, deposits, "Bob", otherAsset, prd, 0)
	assertNoError(t, err)
	fund(t, reg, cash, payer, 1000)

	// The ledger would refuse this posting too, as an unbalanced asset. That
	// answer names the symptom; this one names the two accounts that disagreed,
	// and it arrives before anything is posted.
	_, err = reg.Transfer(ctx, payer.ID, payee.ID, 100, "rent")
	assertError(t, err, ErrAssetMismatch)
	if errors.Is(err, ledger.ErrUnbalancedAsset) {
		t.Errorf("the refusal reached the ledger: %v", err)
	}
	assertBooks(t, reg, payer, payee, 1000, 0)
}

func TestATransferOfNothingIsRefused(t *testing.T) {
	ctx := context.Background()
	reg, payer, payee := twoCustomers(t, 0)

	for _, amount := range []ledger.Amount{0, -100} {
		_, err := reg.Transfer(ctx, payer.ID, payee.ID, amount, "rent")
		assertError(t, err, ErrInvalidAmount)
	}
	assertBooks(t, reg, payer, payee, 1000, 0)
}

func TestATransferNamingAnAccountThisBankDoesNotHoldIsRefused(t *testing.T) {
	ctx := context.Background()
	reg, payer, payee := twoCustomers(t, 0)

	// A register spans one book, so "not held here" is the only answer it can
	// give about an account it cannot see. There is no cross-bank case to
	// distinguish and no guard that could write one.
	_, err := reg.Transfer(ctx, payer.ID, "dep_nobody", 100, "rent")
	assertError(t, err, ErrAccountNotFound)
	_, err = reg.Transfer(ctx, "dep_nobody", payee.ID, 100, "rent")
	assertError(t, err, ErrAccountNotFound)
	assertBooks(t, reg, payer, payee, 1000, 0)
}

// ---------------------------------------------------------------------------
// Two transfers out of one account, on a file
// ---------------------------------------------------------------------------

// TestTwoTransfersOutOfOneAccountCannotBothSpendTheSameMoney opens a FILE, for
// the reason CLAUDE.md gives: on the ephemeral store a second reader blocks
// until the winner commits, so a loser reaches the guard however the code
// underneath behaves. Only a file under WAL lets a reader past an uncommitted
// writer, which is where a check-then-post would spend the balance twice.
func TestTwoTransfersOutOfOneAccountCannotBothSpendTheSameMoney(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits, prd := newTestRegisterOnFile(t)
	cash := newCashAccount(t, book, deposits)

	payer, err := reg.OpenAccount(ctx, deposits, "Alice", testAsset, prd, 0)
	assertNoError(t, err)
	first, err := reg.OpenAccount(ctx, deposits, "Bob", testAsset, prd, 0)
	assertNoError(t, err)
	second, err := reg.OpenAccount(ctx, deposits, "Carol", testAsset, prd, 0)
	assertNoError(t, err)
	fund(t, reg, cash, payer, 1000)

	// Each alone fits; together they do not, and no overdraft covers the
	// difference.
	errs := make(chan error, 2)
	for _, payee := range []Account{first, second} {
		go func() {
			_, err := reg.Transfer(ctx, payer.ID, payee.ID, 600, "rent")
			errs <- err
		}()
	}

	var refused int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
		case errors.Is(err, ErrInsufficientAvailable):
			refused++
		default:
			t.Fatalf("Transfer: %v", err)
		}
	}
	if refused != 1 {
		t.Fatalf("%d of the two transfers were refused, want 1", refused)
	}

	bal, err := reg.GetBalance(ctx, payer.ID)
	assertNoError(t, err)
	assertEqual(t, "the payer's book balance", bal.Book, 400)
}

// newTestRegisterOnFile is newTestRegister over a real file rather than the
// ephemeral store, so a second reader is not serialised behind the first. It
// names store/sqlite directly, as store/sqlite's own lock tests do, because
// testenv opens ephemeral databases and that is the right default for every
// other suite here.
func newTestRegisterOnFile(t *testing.T) (*Register, *ledger.Book, ledger.SubledgerID, product.ID) {
	t.Helper()
	ctx := context.Background()
	clock := func() time.Time { return fixedTime }
	store, err := sqlite.Open(ctx, sqlite.Bank, testenv.BankBook, filepath.Join(t.TempDir(), "transfers.db"), clock)
	assertNoError(t, err)
	t.Cleanup(func() { assertNoError(t, store.Close()) })
	book := ledger.NewBook(store, testenv.BankBook, clock)
	reg := NewRegister(store.Deposit(), book, book.ID(), clock, testIssuer)
	cat := product.NewCatalogue(store.Product(), book, book.ID(), clock)

	gl, err := book.CreateLedger(ctx, "General Ledger")
	assertNoError(t, err)
	deposits, err := book.CreateSubledger(ctx, gl.ID, "Customer Deposits")
	assertNoError(t, err)

	free, err := cat.CreateProduct(ctx, "Basic Current Account", product.CurrentAccount)
	assertNoError(t, err)
	today := ledger.DayStart(clock())
	_, err = cat.DraftVersion(ctx, free.ID, today, product.OverdraftPricing{})
	assertNoError(t, err)
	_, err = cat.PublishVersion(ctx, free.ID, today)
	assertNoError(t, err)

	return reg, book, deposits.ID, free.ID
}
