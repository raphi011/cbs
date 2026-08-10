package deposit_test

import (
	"context"
	"testing"

	. "github.com/raphi011/cbs/deposit"
)

func TestListAccountsHoldsSnapshots(t *testing.T) {
	ctx := context.Background()
	reg, book, deposits, prd := newTestRegister(t)
	cash := newCashAccount(t, book, deposits)

	alice, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
	assertNoError(t, err)
	_, err = reg.OpenAccount(ctx, "Bob", testAsset, prd, 0)
	assertNoError(t, err)

	accts, err := reg.ListAccounts(ctx)
	assertNoError(t, err)
	assertEqual(t, "deposit account count", len(accts), 2)
	assertEqual(t, "first account is Alice", accts[0].ID, alice.ID)

	// Fund Alice so holds have available balance to reserve against.
	fund(t, reg, cash, alice, 10000)

	// Two holds on Alice; none on an unknown account.
	_, err = reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 1000})
	assertNoError(t, err)
	_, err = reg.CreateHold(ctx, CreateHoldRequest{AccountID: alice.ID, Amount: 2000})
	assertNoError(t, err)

	holds, err := reg.ListHolds(ctx, alice.ID)
	assertNoError(t, err)
	assertEqual(t, "hold count for Alice", len(holds), 2)
	unknownHolds, err := reg.ListHolds(ctx, "nope")
	assertNoError(t, err)
	assertEqual(t, "holds for unknown account", len(unknownHolds), 0)

	// Snapshots enumerate by business date.
	_, err = reg.TakeEndOfDaySnapshot(ctx, alice.ID, fixedTime)
	assertNoError(t, err)
	snaps, err := reg.ListSnapshots(ctx, alice.ID)
	assertNoError(t, err)
	assertEqual(t, "snapshot count for Alice", len(snaps), 1)
	unknownSnaps, err := reg.ListSnapshots(ctx, "nope")
	assertNoError(t, err)
	assertEqual(t, "snapshots for unknown account", len(unknownSnaps), 0)
}
