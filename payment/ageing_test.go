package payment_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	. "github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/testenv"
)

// ---------------------------------------------------------------------------
// Ageing the two accounts money sits in
// ---------------------------------------------------------------------------

// agedSystem is a testSystem whose clock moves, because an ageing report
// measures time and every other fixture in this package is frozen. It starts at
// fixedTime, so a setup that does not advance behaves exactly as testNetwork's
// does and the value dates the other suites assert still hold.
type agedSystem struct {
	*testSystem
	nanos *atomic.Int64
}

func agedNetwork(t *testing.T) *agedSystem {
	t.Helper()
	nanos := &atomic.Int64{}
	nanos.Store(fixedTime.UnixNano())
	clock := func() time.Time { return time.Unix(0, nanos.Load()).UTC() }

	stores := testenv.NewSet(t, clock)
	nets := NewNetworks(stores, clock)
	return &agedSystem{
		testSystem: &testSystem{ClearingHouseNetwork: nets.ClearingHouse(), nets: nets, stores: stores},
		nanos:      nanos,
	}
}

// advance moves every institution's clock forward by whole days. It moves ONE
// clock because there is one: a deployment whose institutions disagreed about
// the date would be a different fixture answering a different question.
func (s *agedSystem) advance(days int) {
	s.nanos.Store(time.Unix(0, s.nanos.Load()).UTC().AddDate(0, 0, days).UnixNano())
}

// pushToAClosedPayee carries one credit transfer to finality against a payee
// whose account closes between their bank's acceptance and the cut-off, which
// is the case Unclaimed Balances exists for. It returns the payment and the two
// banks.
func pushToAClosedPayee(t *testing.T, sys *testSystem) (a, b *Bank, payer deposit.AccountID, pay Payment) {
	t.Helper()
	ctx := context.Background()
	a, b, alice, bob := setupTwoBanks(t, sys)
	runCycle(t, sys, SchemeSEPACT, func() {
		var err error
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 30000,
			Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
			DebtorDetails:   PartyDetails{Agent: a.BIC}})
		assertNoError(t, err)
		closeCreditorAccount(t, sys, pay)
	})
	return a, b, alice, pay
}

func unclaimed(t *testing.T, sys *testSystem, bic iso20022.BIC) AgeingReport {
	t.Helper()
	rep, err := sys.bank(bic).AgeUnclaimedBalances(context.Background(), testAsset)
	assertNoError(t, err)
	return rep
}

func suspense(t *testing.T, sys *testSystem, bic iso20022.BIC) AgeingReport {
	t.Helper()
	rep, err := sys.bank(bic).AgeClearingSuspense(context.Background(), testAsset)
	assertNoError(t, err)
	return rep
}

// TestAnUnclaimedBalanceIsAgedPerPayment is the stronger of the two answers this
// walk gives, and the reason it is stronger is a fact about the postings.
//
// Every credit into unclaimed balances is ONE payment's diverted creditor leg
// and carries that payment's id, so the report can say which payment, which
// scheme, and what may be done about it. A clearing suspense's residue cannot,
// and the test below is the contrast.
func TestAnUnclaimedBalanceIsAgedPerPayment(t *testing.T) {
	sys := agedNetwork(t)
	_, b, _, pay := pushToAClosedPayee(t, sys.testSystem)
	sys.advance(2)

	rep := unclaimed(t, sys.testSystem, b.BIC)
	assertEqual(t, "balance", rep.Balance, 30000)
	assertEqual(t, "the account it names", rep.Account, accountsOf(t, b).Unclaimed)
	assertEqual(t, "lots", len(rep.Lots), 1)
	assertEqual(t, "which payment put it there", rep.Lots[0].Payment, pay.ID)
	assertEqual(t, "and under which scheme", rep.Lots[0].Scheme, SchemeSEPACT)
	assertEqual(t, "age", rep.Lots[0].Days, 2)
	assertEqual(t, "nothing stops this bank returning it", rep.Lots[0].Blocked, "")
	assertEqual(t, "and there is a clock on it", rep.Lots[0].Deadline, ReturnWindowDays)
}

// TestWhoseBankYouAreDecidesWhetherYourSuspenseResidueHasAName is the contrast,
// and both halves come out of ONE fixture so that neither can be explained by
// the other's setup.
//
// A payer's bank raises its suspense with the DEBTOR leg, which names the
// payment. A payee's bank raises it with the netted mirror leg, which names
// nothing — deliberately, because a member holds no cycle and one movement
// covers a whole cut-off. So the same walk answers "which payment" at one end of
// one payment and cannot at the other, and the missing name is not something
// this report could recover.
func TestWhoseBankYouAreDecidesWhetherYourSuspenseResidueHasAName(t *testing.T) {
	ctx := context.Background()
	sys := agedNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys.testSystem)

	cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	pay, err := initiate(ctx, sys.testSystem, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 30000,
		Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	assertNoError(t, err)
	_, err = sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)
	_, statements, err := sys.settleCycle(ctx, cyc.ID)
	assertNoError(t, err)

	// Only the PAYEE's bank books its statement, and neither bank posts a
	// customer leg. So each is left holding the same money at a different stage
	// of the same cut-off: A has not yet been told, B has been told and has not
	// yet paid its customer.
	for _, st := range statements {
		if st.Agent != b.BIC {
			continue
		}
		_, err := sys.bank(st.Agent).PostSettlementAdvice(ctx, AdvisedMovement{
			Account: st.Account, Asset: st.Asset, Movement: st.Movement,
			ClosingBalance: st.ClosingBalance, Reference: st.Reference, ValueDate: st.ValueDate,
		})
		assertNoError(t, err)
	}
	sys.advance(4)

	payer := suspense(t, sys.testSystem, a.BIC)
	assertEqual(t, "the payer's bank still holds it", payer.Balance, 30000)
	assertEqual(t, "lots", len(payer.Lots), 1)
	assertEqual(t, "and its own leg named the payment", payer.Lots[0].Payment, pay.ID)
	assertEqual(t, "age", payer.Lots[0].Days, 4)

	payee := suspense(t, sys.testSystem, b.BIC)
	assertEqual(t, "the payee's bank holds it too", payee.Balance, 30000)
	assertEqual(t, "lots", len(payee.Lots), 1)
	assertEqual(t, "and the netted leg named nothing", payee.Lots[0].Payment, PaymentID(""))
	assertEqual(t, "age", payee.Lots[0].Days, 4)
}

// TestAClearingSuspenseHasNoDeadlineHoweverOldItIs states the ruling as a test.
//
// No rulebook puts a clock on a clearing suspense: what discharges it is a
// conversation, and a conversation has no due date this bank could hold anybody
// to. So the report ages and does not judge — inventing a deadline would be
// inventing a rulebook, and an operator's judgement about what is too long is
// exactly the thing this code has no standing to make.
func TestAClearingSuspenseHasNoDeadlineHoweverOldItIs(t *testing.T) {
	ctx := context.Background()
	sys := agedNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys.testSystem)

	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	_, err = initiate(ctx, sys.testSystem, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 30000,
		Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	assertNoError(t, err)
	sys.advance(100)

	rep := suspense(t, sys.testSystem, a.BIC)
	assertEqual(t, "a hundred days in suspense", rep.Lots[0].Days, 100)
	assertEqual(t, "and no deadline on it", rep.Lots[0].Deadline, 0)
	assertEqual(t, "so nothing is overdue", len(rep.Overdue()), 0)
}

// TestAnUnclaimedBalanceGoesOverdueWhenTheWindowRunsOut pins the boundary, and
// the boundary is a day on the settlement calendar rather than an age.
//
// The credit lands on a Wednesday and the window is three BANKING BUSINESS days,
// so it runs out on the Monday — five calendar days later, because the weekend
// is not part of it. A report that compared the age against the figure would
// have called this overdue on the Saturday, which is the error this does not
// make.
func TestAnUnclaimedBalanceGoesOverdueWhenTheWindowRunsOut(t *testing.T) {
	sys := agedNetwork(t)
	_, b, _, _ := pushToAClosedPayee(t, sys.testSystem)

	sys.advance(4)
	sunday := unclaimed(t, sys.testSystem, b.BIC)
	assertEqual(t, "older than the figure in calendar days", sunday.Lots[0].Days, 4)
	assertEqual(t, "and still inside the window", len(sunday.Overdue()), 0)

	sys.advance(1)
	overdue := unclaimed(t, sys.testSystem, b.BIC).Overdue()
	assertEqual(t, "on the Monday it runs out", len(overdue), 1)
	assertEqual(t, "the day it was due", overdue[0].Due, time.Date(2025, 1, 20, 0, 0, 0, 0, time.UTC))
}

// TestReturningAnAgedUnclaimedBalanceClearsIt is the whole point of the report:
// what it finds is something a bank can act on, with an instrument that already
// exists.
//
// Nothing new is built for this. The payee's bank IS the returner on a push, and
// clawbackTx debits unclaimed balances with no customer check — the payee never
// received the money, so the bank is releasing an obligation rather than taking
// money off anybody. What was missing was never the posting; it was that an
// operator had to already know the payment id.
func TestReturningAnAgedUnclaimedBalanceClearsIt(t *testing.T) {
	sys := agedNetwork(t)
	a, b, alice, pay := pushToAClosedPayee(t, sys.testSystem)
	sys.advance(6) // the Wednesday credit's window ran out on Monday

	found := unclaimed(t, sys.testSystem, b.BIC).Overdue()
	assertEqual(t, "the report found one", len(found), 1)
	assertEqual(t, "and it is the payment that stranded", found[0].Payment, pay.ID)

	returned := returnTheWholeWay(t, sys.testSystem, pay, "AC04: account closed")
	assertEqual(t, "status", returned.Status, Returned)

	after := unclaimed(t, sys.testSystem, b.BIC)
	assertEqual(t, "the liability is released", after.Balance, 0)
	assertEqual(t, "and there is nothing left to report", len(after.Lots), 0)
	assertEqual(t, "the payer has it back", customerBalance(t, a, alice), 100000)
}

// TestAnUnclaimedBalanceOnAPullHasNoBankThatMayReturnIt is the gap this task
// found and does not close, asserted rather than written down.
//
// On a pull the bank left holding the money is the CREDITOR's — the biller's
// account closed between its bank's answer and the cut-off — and ReturnerOf
// names the DEBTOR's bank the returner, correctly: a pacs.004 on a pull is the
// payer's bank's instrument and the payer has asked for nothing. What the
// creditor's bank wants is a REVERSAL, pacs.007, which is deferred by design in
// iso20022/doc.go along with camt.056.
//
// So the lot is reported, blocked, and carries NO deadline however old it gets.
// A clock on money this bank has no instrument to move would be a report telling
// somebody off for a message they cannot send.
//
// # The block is load-bearing, and the second half measures why
//
// PostReturnLeg does not refuse this bank: on a pull the creditor's bank holds
// the clawback leg, so it is a party to the return and posts it without a
// choice — the refusal is the RETURNER's alone, and only on a push. Called with
// no pacs.004 behind it, it does what it is told: the unclaimed balance is
// released into this bank's clearing suspense and its own copy goes Returned,
// while no return exists at any other institution and no reserves come back.
// The money has moved from an account that says "owed to somebody unidentified"
// into one that says "in flight", and nothing is in flight.
//
// That is the same shape as a forged pacs.002 and has the same answer: the
// message is the authorisation, the channel is what makes it trustworthy, and
// reconciliation is what catches an act nobody asked for. It is why this report
// blocks the lot instead of offering the obvious method, and it is measured here
// so that a later reader does not reach for that method.
func TestAnUnclaimedBalanceOnAPullHasNoBankThatMayReturnIt(t *testing.T) {
	ctx := context.Background()
	sys := agedNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys.testSystem)

	debtor := PartyRef{Account: alice}
	creditor := PartyRef{Account: biller}
	m, err := sys.bank(b.BIC).CreateMandate(ctx, a.BIC, debtor, creditor, 0)
	assertNoError(t, err)

	var pay Payment
	runCycle(t, sys.testSystem, SchemeSEPADD, func() {
		pay, err = initiate(ctx, sys.testSystem, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
			Debtor: debtor, Creditor: creditor, Description: "Electricity bill",
			DebtorDetails:   PartyDetails{Agent: a.BIC, Name: "Alice"},
			CreditorDetails: PartyDetails{Agent: b.BIC}})
		assertNoError(t, err)
		// The biller closes between its bank's answer and the cut-off, which is
		// the same case as a closed payee on a push and lands in the same
		// account — at the bank that cannot do anything about it.
		closeCreditorAccount(t, sys.testSystem, pay)
	})
	sys.advance(30)

	rep := unclaimed(t, sys.testSystem, b.BIC)
	assertEqual(t, "the creditor's bank is holding it", rep.Balance, 25000)
	assertEqual(t, "for a month", rep.Lots[0].Days, 30)
	assertEqual(t, "and it is not overdue, because no clock applies", len(rep.Overdue()), 0)
	assertEqual(t, "no deadline", rep.Lots[0].Deadline, 0)
	if !strings.Contains(rep.Lots[0].Blocked, "pacs.007") {
		t.Fatalf("the block does not name the instrument that is missing: %q", rep.Lots[0].Blocked)
	}
	if !strings.Contains(rep.Lots[0].Blocked, string(a.BIC)) {
		t.Fatalf("the block does not name the bank whose return it would be: %q", rep.Lots[0].Blocked)
	}

	// What the obvious method does instead, measured. It succeeds, and every
	// figure it leaves behind is wrong about the world.
	out, err := sys.bank(b.BIC).PostReturnLeg(ctx, pay.ID, "AC04: account closed")
	assertNoError(t, err)
	assertEqual(t, "this bank's own copy now says", out.Status, Returned)
	assertEqual(t, "the liability is gone", bookBalance(t, b.Ledger, accountsOf(t, b).Unclaimed), 0)
	assertEqual(t, "and the money is in flight", bookBalance(t, b.Ledger, accountsOf(t, b).Suspense), 25000)
	assertEqual(t, "with the reserves exactly where they were", bookBalance(t, b.Ledger, accountsOf(t, b).Reserve), 25000)
	// The clearing house never heard of it, which is the whole of what is wrong.
	network, err := sys.GetPayment(ctx, pay.ID)
	assertNoError(t, err)
	assertEqual(t, "the clearing house's copy", network.Status, Settled)

	// This bank's own instrument reports a position and cannot call it a break,
	// because from inside nothing distinguishes it from money legitimately in
	// flight. payment/recon can and does — a suspense with nothing in flight and
	// no advice outstanding is the break that harness exists for.
	rec, err := sys.bank(b.BIC).Reconcile(ctx, testAsset)
	assertNoError(t, err)
	assertEqual(t, "no break from inside", rec.Reconciled(), true)
	assertEqual(t, "a position instead", len(rec.Positions), 1)
	assertEqual(t, "holding what was released", rec.Positions[0].Balance, 25000)
}

// TestARefundThePayerCouldNotTakeIsTerminal is the third state of this account
// and the one that looks most like the first.
//
// Both are unclaimed balances holding a settled payment's money for a customer
// who closed their account. What differs is that this money has ALREADY been
// sent back: it is the refund leg of a completed return, sitting at the PAYER's
// bank because the payer could not take it. There is nothing further to return
// it to, and a report that offered a return here would be offering to send money
// back to the bank that had just sent it.
//
// The status of this bank's own copy is what tells them apart, and it is the
// only thing that does.
func TestARefundThePayerCouldNotTakeIsTerminal(t *testing.T) {
	ctx := context.Background()
	sys := agedNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys.testSystem)

	var pay Payment
	runCycle(t, sys.testSystem, SchemeSEPACT, func() {
		var err error
		pay, err = initiate(ctx, sys.testSystem, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 30000,
			Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
			DebtorDetails:   PartyDetails{Agent: a.BIC}})
		assertNoError(t, err)
	})
	// The payer empties and closes the account after the cut-off, then the
	// payment is returned into it.
	spendTheCredit(t, sys.testSystem, a, alice, b, bob, 70000)
	assertNoError(t, a.Deposit.Close(ctx, alice))
	returnTheWholeWay(t, sys.testSystem, pay, "AC04: account closed")
	sys.advance(60)

	rep := unclaimed(t, sys.testSystem, a.BIC)
	assertEqual(t, "the payer's bank is holding it", rep.Balance, 30000)
	assertEqual(t, "which payment", rep.Lots[0].Payment, pay.ID)
	assertEqual(t, "no deadline", rep.Lots[0].Deadline, 0)
	if !strings.Contains(rep.Lots[0].Blocked, "already been returned") {
		t.Fatalf("the block does not say why this one is terminal: %q", rep.Lots[0].Blocked)
	}
}

// TestAgeingIsNotAnActTheOtherTwoInstitutionsCanPerform is the same boundary
// Reconcile has, and it is asserted here too because these are separate entry
// points: a clearing house has no ledger and a settlement agent holds neither of
// these accounts.
//
// Both acts are methods on BankNetwork, so neither is reachable through a handle
// Networks minted for another institution. What is measured here is the one
// crossing left: a bank's handle over another institution's core, which only
// this package can assemble. See export_test.go.
func TestAgeingIsNotAnActTheOtherTwoInstitutionsCanPerform(t *testing.T) {
	ctx := context.Background()
	sys := agedNetwork(t)
	setupTwoBanks(t, sys.testSystem)

	asCSM := BankHandleOverClearingHouse(sys.bank(testBIC), sys.ClearingHouseNetwork)
	if _, err := asCSM.AgeClearingSuspense(ctx, testAsset); err == nil {
		t.Fatal("the clearing house aged a suspense it does not have")
	}
	asCB := BankHandleOverCentralBank(sys.bank(testBIC), sys.cb())
	if _, err := asCB.AgeUnclaimedBalances(ctx, testAsset); err == nil {
		t.Fatal("the settlement agent aged unclaimed balances it does not have")
	}
}
