package payment_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	. "github.com/raphi011/cbs/payment"
)

// What a Network's identity is worth, measured rather than asserted.

// TestAMemberBanksActsAreRefusedOnAnyOtherInstitutionsNetwork is the plan's bar
// for this task, in as many words: a bank's act performed with the clearing
// house's identity must FAIL, not merely behave oddly.
func TestAMemberBanksActsAreRefusedOnAnyOtherInstitutionsNetwork(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, _, alice, _ := setupTwoBanks(t, sys)

	// A real payment in an open cycle, so that nothing below is refused for want
	// of one and a missing guard would get far enough to post.
	openCycle(t, ctx, sys, SchemeSEPACT)
	pay, err := initiate(ctx, sys, InitiatePaymentRequest{
		Scheme:          SchemeSEPACT,
		Debtor:          PartyRef{Account: alice},
		Creditor:        PartyRef{Account: alice},
		Amount:          1,
		CreditorDetails: PartyDetails{Agent: testBIC, Name: "Alice"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	assertNoError(t, err)

	acts := []struct {
		name string
		call func(n *BankNetwork) error
	}{
		// A well-formed address, so that what refuses the act is the identity
		// guard. A malformed one is refused for its check digits by every
		// institution alike, which would make this row pass without the guard.
		{"ResolveIdentifier", func(n *BankNetwork) error {
			_, err := n.ResolveIdentifier(ctx, mintAt(t, a, 999_999))
			return err
		}},
		{"AcceptInbound", func(n *BankNetwork) error { return n.AcceptInbound(ctx, pay.ID, relayedFrom(pay)) }},
		{"SettleAtBank", func(n *BankNetwork) error { _, err := n.SettleAtBank(ctx, pay.ID); return err }},
		{"PostReturnLeg", func(n *BankNetwork) error { _, err := n.PostReturnLeg(ctx, pay.ID, "AC04"); return err }},
		{"ReverseReturnLeg", func(n *BankNetwork) error { return n.ReverseReturnLeg(ctx, pay.ID, "AM04") }},
		{"PostSettlementAdvice", func(n *BankNetwork) error {
			_, err := n.PostSettlementAdvice(ctx, AdvisedMovement{
				Account: "200.100.001", Asset: testAsset, Movement: 1, Reference: "cyc-1",
			})
			return err
		}},
		{"LodgeReserves", func(n *BankNetwork) error {
			_, _, err := n.LodgeReserves(ctx, testAsset, 1, MessageContext{
				From: testBIC, To: testBIC2, MsgID: "m", Now: fixedTime,
			})
			return err
		}},
		{"RecordMembership", func(n *BankNetwork) error {
			_, err := n.RecordMembership(ctx, AdmissionAcknowledgement{
				BIC: testBIC, Issuer: testAllocation, Ref: "adm-1",
				Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001"},
			})
			return err
		}},
		{"CreditTransferRequest", func(n *BankNetwork) error {
			_, err := n.CreditTransferRequest(ctx, &iso20022.Pacs008{})
			return err
		}},
		{"DirectDebitRequest", func(n *BankNetwork) error {
			_, err := n.DirectDebitRequest(ctx, &iso20022.Pacs003{})
			return err
		}},
	}

	for _, imposter := range []struct {
		who string
		net *BankNetwork
	}{
		{"the clearing house", BankHandleOverClearingHouse(sys.bank(a.BIC), sys.ClearingHouseNetwork)},
		{"the central bank", BankHandleOverCentralBank(sys.bank(a.BIC), sys.cb())},
	} {
		for _, act := range acts {
			t.Run(act.name+" as "+imposter.who, func(t *testing.T) {
				err := act.call(imposter.net)
				if !errors.Is(err, ErrNotThisInstitutionsAct) {
					t.Fatalf("%s performed %s and got %v, want ErrNotThisInstitutionsAct.\n"+
						"A member bank's act reached through another institution's Network is the "+
						"crossing an identity exists to refuse.", imposter.who, act.name, err)
				}
				// The refusal names both halves of what went wrong, because a
				// sentinel alone leaves an operator guessing which handle was
				// held and which act was asked for.
				if got := err.Error(); !strings.Contains(got, imposter.who) {
					t.Errorf("the refusal reads %q; it does not name the institution that asked", got)
				}
			})
		}
	}
}

// TestTheCentralBanksBookIsReachableOnlyFromTheSettlementAgentsNetwork is the
// other half of the identity, and the one that removes a handle rather than an
// argument.
func TestTheCentralBanksBookIsReachableOnlyFromTheSettlementAgentsNetwork(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, _, _, _ := setupTwoBanks(t, sys)

	acts := []struct {
		name string
		call func(n *CentralBankNetwork) error
	}{
		{"OpenSettlementAccount", func(n *CentralBankNetwork) error {
			_, _, err := n.OpenSettlementAccount(ctx, AdmissionRequest{
				Name: "Aurora Bank", BIC: testBIC, Country: testAllocation.Country, Asset: testAsset, Ref: "adm-1",
			})
			return err
		}},
		{"ReceiveLodgement", func(n *CentralBankNetwork) error {
			_, err := n.ReceiveLodgement(ctx, LodgementInstruction{
				Ref: "lodge-1", BIC: testBIC, Asset: testAsset, Amount: 1,
			})
			return err
		}},
		{"SettleCycle", func(n *CentralBankNetwork) error { _, _, err := n.SettleCycle(ctx, "cyc-1", nil); return err }},
		{"SettleReturn", func(n *CentralBankNetwork) error {
			_, err := n.SettleReturn(ctx, ReturnInstruction{
				PaymentID: "pay-1", DebtorAgent: testBIC, CreditorAgent: testBIC2,
				Amount: 1, Asset: testAsset, Reason: "AC04",
			})
			return err
		}},
		{"ReserveBalance", func(n *CentralBankNetwork) error { _, err := n.ReserveBalance(ctx, a.BIC, testAsset); return err }},
		{"CentralBank", func(n *CentralBankNetwork) error { _, err := n.CentralBank(); return err }},
	}

	for _, imposter := range []struct {
		who string
		net *CentralBankNetwork
	}{
		{"the clearing house", CentralBankHandleOverClearingHouse(sys.cb(), sys.ClearingHouseNetwork)},
		{"a member bank", CentralBankHandleOverBank(sys.cb(), sys.bank(a.BIC))},
	} {
		for _, act := range acts {
			t.Run(act.name+" as "+imposter.who, func(t *testing.T) {
				err := act.call(imposter.net)
				if !errors.Is(err, ErrNotThisInstitutionsAct) {
					t.Fatalf("%s performed %s and got %v, want ErrNotThisInstitutionsAct.\n"+
						"The central bank's book of accounts is the settlement agent's alone.",
						imposter.who, act.name, err)
				}
			})
		}
	}
}

// TestANetworkBelongingToNobodyIsRefusedAtConstruction pins the panic.
func TestANetworkBelongingToNobodyIsRefusedAtConstruction(t *testing.T) {
	sys := testNetwork(t)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("the core accepted the zero Identity; a network belonging to no institution has no answer to whose book an act is about")
		}
		if got, ok := r.(string); !ok || !strings.Contains(got, "identity") {
			t.Errorf("panicked with %v, want a message naming the missing identity", r)
		}
	}()
	NetworkWithoutAnIdentity(sys.Now)
}

// TestARegisteredSchemeReachesEveryInstitutionsNetwork is the one thing per-
// entity networks must NOT disagree about.
func TestARegisteredSchemeReachesEveryInstitutionsNetwork(t *testing.T) {
	sys := testNetwork(t)
	before := len(sys.ListSchemes())

	// Registered on the clearing house's handle, which is not where it is read.
	sys.RegisterScheme(dollarPush{})

	// The three institutions are three types, and the registry is the one thing
	// they must agree about, so the rows are held by what they have in common
	// rather than by a handle only one of them could be.
	for _, seen := range []struct {
		who string
		net interface {
			Scheme(id SchemeID) (Scheme, bool)
			ListSchemes() []Scheme
		}
	}{
		{"the central bank", sys.cb()},
		{"a member bank", sys.bank(testBIC)},
		{"a second view of the clearing house", sys.nets.ClearingHouse()},
	} {
		if _, ok := seen.net.Scheme(dollarPush{}.ID()); !ok {
			t.Errorf("%s has never heard of a scheme the clearing house registered.\n"+
				"Separate stores are what the institutions have; separate scheme registries "+
				"would make one institution unable to read what another can write.", seen.who)
		}
		if got := len(seen.net.ListSchemes()); got != before+1 {
			t.Errorf("%s lists %d schemes, want %d", seen.who, got, before+1)
		}
	}
}
