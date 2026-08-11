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
//
// The claim is that an institution cannot perform another's act through a handle
// it legitimately holds. Everything else in this package measures what an act
// DOES; these measure who may reach it at all, and each one fails if the guard is
// removed from the act it names.

// TestAMemberBanksActsAreRefusedOnAnyOtherInstitutionsNetwork is the plan's bar
// for this task, in as many words: a bank's act performed with the clearing
// house's identity must FAIL, not merely behave oddly.
//
// # It is a correspondence table, not a sample
//
// The list below is every method in this package that calls Network.self, and
// it is derived from that side rather than from what seemed worth checking.
// That method is the whole of the mechanism — an act reaches the acting bank
// there or not at all — so a table written against it is complete by
// construction and a new act added without a guard shows up as a row nobody
// wrote. The two translators are on it for the same reason: they resolve one
// party in this bank's own register, through ResolveIdentifierTx.
//
// # Both wrong identities, because they are wrong in different ways
//
// The clearing house has no book of accounts at all; the central bank has one
// and it is the wrong one. A guard that read "is there a book here" would pass
// the second, so both are run against every act.
//
// What this does NOT measure is one member bank acting as another, and it
// cannot: two members are both members. That is what the subject guards are for
// — ErrNotThisBanksPayment, ErrStatementNotForThisBank, ErrNotThisBanksAdmission
// — and they are unchanged and tested where they always were. See
// ErrNotThisInstitutionsAct, which says why the two are not redundant.
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
		call func(n *Network) error
	}{
		// A well-formed address, so that what refuses the act is the identity
		// guard. A malformed one is refused for its check digits by every
		// institution alike, which would make this row pass without the guard.
		{"ResolveIdentifier", func(n *Network) error {
			_, err := n.ResolveIdentifier(ctx, mintAt(t, a, 999_999))
			return err
		}},
		{"AcceptInbound", func(n *Network) error { return n.AcceptInbound(ctx, pay.ID, relayedFrom(pay)) }},
		{"SettleAtBank", func(n *Network) error { _, err := n.SettleAtBank(ctx, pay.ID); return err }},
		{"PostReturnLeg", func(n *Network) error { _, err := n.PostReturnLeg(ctx, pay.ID, "AC04"); return err }},
		{"ReverseReturnLeg", func(n *Network) error { return n.ReverseReturnLeg(ctx, pay.ID, "AM04") }},
		{"PostSettlementAdvice", func(n *Network) error {
			_, err := n.PostSettlementAdvice(ctx, AdvisedMovement{
				Account: "200.100.001", Asset: testAsset, Movement: 1, Reference: "cyc-1",
			})
			return err
		}},
		{"LodgeReserves", func(n *Network) error {
			_, _, err := n.LodgeReserves(ctx, testAsset, 1, MessageContext{
				From: testBIC, To: testBIC2, MsgID: "m", Now: fixedTime,
			})
			return err
		}},
		{"RecordMembership", func(n *Network) error {
			_, err := n.RecordMembership(ctx, AdmissionAcknowledgement{
				BIC: testBIC, Issuer: testAllocation, Ref: "adm-1",
				Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001"},
			})
			return err
		}},
		{"CreditTransferRequest", func(n *Network) error {
			_, err := n.CreditTransferRequest(ctx, &iso20022.Pacs008{})
			return err
		}},
		{"DirectDebitRequest", func(n *Network) error {
			_, err := n.DirectDebitRequest(ctx, &iso20022.Pacs003{})
			return err
		}},
	}

	for _, imposter := range []struct {
		who string
		net *Network
	}{
		{"the clearing house", sys.Network},
		{"the central bank", sys.cb()},
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
//
// A Network that held a ledger.Book over CentralBankBook would put the book
// central-bank money lives in inside every institution in this system. What
// keeps a bank handler out of it in cmd/server is that bankOps names no method
// that reaches it — a fact about that package, not about this one, and all five
// of these are exported. A test fixture, api, seed or payment/recon could call
// any of them on any network and post reserves.
//
// The list is derived from the other side as the table above is: it is every
// caller of Network.centralBankBook.
func TestTheCentralBanksBookIsReachableOnlyFromTheSettlementAgentsNetwork(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, _, _, _ := setupTwoBanks(t, sys)

	acts := []struct {
		name string
		call func(n *Network) error
	}{
		{"OpenSettlementAccount", func(n *Network) error {
			_, _, err := n.OpenSettlementAccount(ctx, AdmissionRequest{
				Name: "Aurora Bank", BIC: testBIC, Country: testAllocation.Country, Asset: testAsset, Ref: "adm-1",
			})
			return err
		}},
		{"ReceiveLodgement", func(n *Network) error {
			_, err := n.ReceiveLodgement(ctx, LodgementInstruction{
				Ref: "lodge-1", BIC: testBIC, Asset: testAsset, Amount: 1,
			})
			return err
		}},
		{"SettleCycle", func(n *Network) error { _, _, err := n.SettleCycle(ctx, "cyc-1", nil); return err }},
		{"SettleReturn", func(n *Network) error {
			_, err := n.SettleReturn(ctx, ReturnInstruction{
				PaymentID: "pay-1", DebtorAgent: testBIC, CreditorAgent: testBIC2,
				Amount: 1, Asset: testAsset, Reason: "AC04",
			})
			return err
		}},
		{"ReserveBalance", func(n *Network) error { _, err := n.ReserveBalance(ctx, a.BIC, testAsset); return err }},
		{"CentralBank", func(n *Network) error { _, err := n.CentralBank(); return err }},
	}

	for _, imposter := range []struct {
		who string
		net *Network
	}{
		{"the clearing house", sys.Network},
		{"a member bank", sys.bank(a.BIC)},
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
//
// The zero Identity is the shape this whole task removes — a network with no
// answer to "whose book?" — and it is reachable by OMISSION, which is the
// easiest way there is to reach anything. Accepting one would leave every act
// below failing far from the line that got it wrong, with a refusal naming "no
// institution".
func TestANetworkBelongingToNobodyIsRefusedAtConstruction(t *testing.T) {
	sys := testNetwork(t)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("NewNetwork accepted the zero Identity; a network belonging to no institution has no answer to whose book an act is about")
		}
		if got, ok := r.(string); !ok || !strings.Contains(got, "identity") {
			t.Errorf("panicked with %v, want a message naming the missing identity", r)
		}
	}()
	NewNetwork(sys.Store(), sys.Now, Identity{})
}

// TestARegisteredSchemeReachesEveryInstitutionsNetwork is the one thing per-
// entity networks must NOT disagree about.
//
// A scheme is code rather than data — RegisterScheme takes a Go value with
// behaviour on it — so it is the only thing in this system that does not live in
// the store and therefore the only thing separate Networks could hold different
// answers to. The consequence of them disagreeing is not a narrower boundary, it
// is a bank that cannot read a message another bank composed perfectly well:
// ErrSchemeNotFound from a receiver whose registry never heard of the scheme,
// about a payment row both of them can see.
//
// It was live, briefly. Minting a fresh registry per Network is the obvious
// implementation and it broke two of payment's own translator tests and one of
// cmd/server's, all of which register a scheme on one handle and then act on
// another. See payment.Networks.schemes.
func TestARegisteredSchemeReachesEveryInstitutionsNetwork(t *testing.T) {
	sys := testNetwork(t)
	before := len(sys.ListSchemes())

	// Registered on the clearing house's handle, which is not where it is read.
	sys.RegisterScheme(dollarPush{})

	for _, seen := range []struct {
		who string
		net *Network
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
