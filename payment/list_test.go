package payment_test

import (
	"context"
	"testing"

	. "github.com/raphi011/cbs/payment"
)

func TestListBanksAndLookup(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, _, _ := setupTwoBanks(t, sys)

	parts, err := sys.ListBanks(ctx)
	assertNoError(t, err)
	assertEqual(t, "participant count", len(parts), 2)

	got, err := sys.GetBank(ctx, a.ID)
	assertNoError(t, err)
	assertEqual(t, "lookup returns Bank A", got.ID, a.ID)
	// GetBank returns a bound bank, so its Ledger is usable.
	assertEqual(t, "live ledger reachable", accountsOf(t, got).Reserve, accountsOf(t, a).Reserve)

	_, err = sys.GetBank(ctx, "nope")
	assertError(t, err, ErrParticipantNotFound)

	_ = b
}

func TestListPaymentsCyclesSettlements(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	st := runCycle(t, sys, SchemeSEPACT, func() {
		_, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme:          SchemeSEPACT,
			Debtor:          PartyRef{Participant: a.ID, Account: alice},
			Creditor:        PartyRef{Participant: b.ID, Account: bob},
			Amount:          30000,
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		})
		assertNoError(t, err)
	})

	payments, err := sys.ListPayments(ctx)
	assertNoError(t, err)
	assertEqual(t, "payment count", len(payments), 1)

	cycles, err := sys.ListCycles(ctx)
	assertNoError(t, err)
	assertEqual(t, "cycle count", len(cycles), 1)

	settlements, err := sys.ListSettlements(ctx)
	assertNoError(t, err)
	assertEqual(t, "settlement count", len(settlements), 1)
	assertEqual(t, "settlement id matches", settlements[0].ID, st.ID)

	gotSt, err := sys.GetSettlement(ctx, st.ID)
	assertNoError(t, err)
	assertEqual(t, "get settlement id", gotSt.ID, st.ID)

	_, err = sys.GetSettlement(ctx, "nope")
	assertError(t, err, ErrSettlementNotFound)

	// Both SEPA schemes are registered.
	assertEqual(t, "scheme count", len(sys.ListSchemes()), 2)
}

func TestListMandates(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	_, err := sys.CreateMandate(ctx,
		PartyRef{Participant: a.ID, Account: alice},
		PartyRef{Participant: b.ID, Account: bob},
		50000,
	)
	assertNoError(t, err)

	mandates, err := sys.ListMandates(ctx)
	assertNoError(t, err)
	assertEqual(t, "mandate count", len(mandates), 1)
}
