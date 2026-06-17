package payment

import "testing"

func TestListParticipantsAndLookup(t *testing.T) {
	sys := testNetwork(t)
	a, b, _, _ := setupTwoBanks(t, sys)

	parts := sys.ListParticipants()
	assertEqual(t, "participant count", len(parts), 2)

	got, err := sys.GetParticipant(a.ID)
	assertNoError(t, err)
	assertEqual(t, "lookup returns Bank A", got.ID, a.ID)
	// GetParticipant returns the live pointer, so its Ledger is usable.
	assertEqual(t, "live ledger reachable", got.ReserveAccount, a.ReserveAccount)

	_, err = sys.GetParticipant("nope")
	assertError(t, err, ErrParticipantNotFound)

	_ = b
}

func TestListPaymentsCyclesSettlements(t *testing.T) {
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	st := runCycle(t, sys, SchemeSEPACT, func() {
		_, err := sys.InitiatePayment(InitiatePaymentRequest{
			Scheme:   SchemeSEPACT,
			Debtor:   PartyRef{Participant: a.ID, Account: alice},
			Creditor: PartyRef{Participant: b.ID, Account: bob},
			Amount:   30000,
		})
		assertNoError(t, err)
	})

	assertEqual(t, "payment count", len(sys.ListPayments()), 1)
	assertEqual(t, "cycle count", len(sys.ListCycles()), 1)

	settlements := sys.ListSettlements()
	assertEqual(t, "settlement count", len(settlements), 1)
	assertEqual(t, "settlement id matches", settlements[0].ID, st.ID)

	gotSt, err := sys.GetSettlement(st.ID)
	assertNoError(t, err)
	assertEqual(t, "get settlement id", gotSt.ID, st.ID)

	_, err = sys.GetSettlement("nope")
	assertError(t, err, ErrSettlementNotFound)

	// Both SEPA schemes are registered.
	assertEqual(t, "scheme count", len(sys.ListSchemes()), 2)
}

func TestListMandates(t *testing.T) {
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	_, err := sys.CreateMandate(
		PartyRef{Participant: a.ID, Account: alice},
		PartyRef{Participant: b.ID, Account: bob},
		50000,
	)
	assertNoError(t, err)

	assertEqual(t, "mandate count", len(sys.ListMandates()), 1)
}
