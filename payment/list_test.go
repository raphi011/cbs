package payment_test

import (
	"context"
	"testing"

	. "github.com/raphi011/cbs/payment"
)

// TestListBanksAndLookup is the read narrowed to what an institution can
// actually answer, and the narrowing is the finding.
//
// It asked the clearing house for two banks. A clearing house holds no banks
// table since Task 18d — it holds a ROSTER of addresses, which is a different
// claim and says nothing about a bank that has been founded and never admitted
// — so the read it used to serve is now two reads at two different scopes:
//
//   - each bank lists ITSELF, from its own database, and that is the whole of
//     what a bank knows about who else is on the network.
//   - the set of banks is the COMPOSITION ROOT's question, answered by
//     Stores.Banks and by no institution. See allBanks, and auditReaders, which
//     had the same problem.
func TestListBanksAndLookup(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, _, _ := setupTwoBanks(t, sys)

	assertEqual(t, "banks in the system", len(allBanks(t, ctx, sys)), 2)

	// A bank's own list is one long, and it is itself. Bank B is on the same
	// network and in the same roster, and Bank A cannot see it.
	own, err := sys.bank(a.BIC).ListBanks(ctx)
	assertNoError(t, err)
	assertEqual(t, "banks Bank A can list", len(own), 1)
	assertEqual(t, "and it is itself", own[0].ID, a.ID)

	got, err := sys.bank(a.BIC).GetBank(ctx, a.ID)
	assertNoError(t, err)
	assertEqual(t, "lookup returns Bank A", got.ID, a.ID)
	// GetBank returns a bound bank, so its Ledger is usable.
	assertEqual(t, "live ledger reachable", accountsOf(t, got).Reserve, accountsOf(t, a).Reserve)

	// Bank B's row is not missing from Bank A's store in the sense of a lookup
	// that failed; it was never there. Same sentinel either way, and the caller
	// that needs the difference is asking the wrong institution.
	_, err = sys.bank(a.BIC).GetBank(ctx, b.ID)
	assertError(t, err, ErrParticipantNotFound)
	_, err = sys.bank(a.BIC).GetBank(ctx, "nope")
	assertError(t, err, ErrParticipantNotFound)
}

func TestListPaymentsCyclesSettlements(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	st := runCycle(t, sys, SchemeSEPACT, func() {
		_, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme:          SchemeSEPACT,
			Debtor:          PartyRef{Account: alice},
			Creditor:        PartyRef{Account: bob},
			Amount:          30000,
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
			DebtorDetails:   PartyDetails{Agent: a.BIC}})
		assertNoError(t, err)
	})

	payments, err := sys.ListPayments(ctx)
	assertNoError(t, err)
	assertEqual(t, "payment count", len(payments), 1)

	cycles, err := sys.ListCycles(ctx)
	assertNoError(t, err)
	assertEqual(t, "cycle count", len(cycles), 1)

	// The settlements are the SETTLEMENT AGENT's rows and are read on its own
	// network: a settlement is what that institution did in its own book, and
	// the clearing house's shape has no table for it. The link back is
	// Settlement.CycleID, which is the direction it can be kept — see
	// GetSettlementByCycleID.
	settlements, err := sys.cb().ListSettlements(ctx)
	assertNoError(t, err)
	assertEqual(t, "settlement count", len(settlements), 1)
	assertEqual(t, "settlement id matches", settlements[0].ID, st.ID)

	gotSt, err := sys.cb().GetSettlement(ctx, st.ID)
	assertNoError(t, err)
	assertEqual(t, "get settlement id", gotSt.ID, st.ID)

	_, err = sys.cb().GetSettlement(ctx, "nope")
	assertError(t, err, ErrSettlementNotFound)

	// Both SEPA schemes are registered.
	assertEqual(t, "scheme count", len(sys.ListSchemes()), 2)
}

func TestListMandates(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	_, err := sys.bank(b.BIC).CreateMandate(ctx,
		a.BIC,
		PartyRef{Account: alice},
		PartyRef{Account: bob},
		50000,
	)
	assertNoError(t, err)

	mandates, err := sys.bank(b.BIC).ListMandates(ctx)
	assertNoError(t, err)
	assertEqual(t, "mandate count", len(mandates), 1)
}
