package deposit

import "testing"

func TestListAccountsHoldsSnapshots(t *testing.T) {
	reg, deposits, cash := testRegister(t)

	alice, err := reg.OpenAccount(deposits, "Alice", 0)
	assertNoError(t, err)
	_, err = reg.OpenAccount(deposits, "Bob", 0)
	assertNoError(t, err)

	accts := reg.ListAccounts()
	assertEqual(t, "deposit account count", len(accts), 2)
	assertEqual(t, "first account is Alice", accts[0].ID, alice.ID)

	// Fund Alice so holds have available balance to reserve against.
	fund(t, reg, cash, alice, 10000)

	// Two holds on Alice; none on an unknown account.
	_, err = reg.CreateHold(CreateHoldRequest{AccountID: alice.ID, Amount: 1000})
	assertNoError(t, err)
	_, err = reg.CreateHold(CreateHoldRequest{AccountID: alice.ID, Amount: 2000})
	assertNoError(t, err)

	holds := reg.ListHolds(alice.ID)
	assertEqual(t, "hold count for Alice", len(holds), 2)
	assertEqual(t, "holds for unknown account", len(reg.ListHolds("nope")), 0)

	// Snapshots enumerate by business date.
	_, err = reg.TakeEndOfDaySnapshot(alice.ID, fixedTime)
	assertNoError(t, err)
	snaps := reg.ListSnapshots(alice.ID)
	assertEqual(t, "snapshot count for Alice", len(snaps), 1)
	assertEqual(t, "snapshots for unknown account", len(reg.ListSnapshots("nope")), 0)
}
