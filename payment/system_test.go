package payment_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	. "github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/testenv"
)

// fixedTime is the instant returned by the test clock, matching the ledger
// package's own test clock.
var fixedTime = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

// testAsset is the asset these tests operate in. SEPA is a euro scheme, so a
// test bank that clears SEPA is a euro bank; euroOnly is the joining set that
// says so at AddParticipant.
const testAsset ledger.AssetCode = "EUR"

var euroOnly = []ledger.AssetCode{testAsset}

func testNetwork(t *testing.T) *Network {
	t.Helper()
	clock := func() time.Time { return fixedTime }
	store := testenv.New(t, clock)
	return NewNetwork(store.Payment(), clock)
}

// accountsOf returns a participant's internal accounts in the test asset,
// failing the test if it does not operate in it.
func accountsOf(t *testing.T, p *Participant) ParticipantAccounts {
	t.Helper()
	accts, err := p.AccountsFor(testAsset)
	assertNoError(t, err)
	return accts
}

// setupTwoBanks creates two participant banks, opens a customer account at
// each (Alice at Bank A, Bob at Bank B), and funds Alice with 100000. The
// returned account IDs are deposit account IDs; their backing GL account IDs
// are resolved when checking book balances.
func setupTwoBanks(t *testing.T, sys *Network) (a, b *Participant, alice, bob deposit.AccountID) {
	t.Helper()
	ctx := context.Background()

	a, err := sys.AddParticipant(ctx, "Bank A", euroOnly)
	assertNoError(t, err)
	b, err = sys.AddParticipant(ctx, "Bank B", euroOnly)
	assertNoError(t, err)

	aliceAcct, err := a.OpenCustomerAccount(ctx, "Alice", testAsset)
	assertNoError(t, err)
	bobAcct, err := b.OpenCustomerAccount(ctx, "Bob", testAsset)
	assertNoError(t, err)

	assertNoError(t, sys.Deposit(ctx, a.ID, aliceAcct.ID, 100000, "Alice opening deposit"))
	return a, b, aliceAcct.ID, bobAcct.ID
}

// runCycle opens, closes, and settles a cycle for the given scheme, returning
// the settled settlement.
func runCycle(t *testing.T, sys *Network, scheme SchemeID, submit func()) Settlement {
	t.Helper()
	ctx := context.Background()
	cyc, err := sys.OpenCycle(ctx, scheme)
	assertNoError(t, err)
	submit()
	_, err = sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)
	st, err := sys.SettleCycle(ctx, cyc.ID)
	assertNoError(t, err)
	return st
}

// bookBalance returns the GL book balance of an arbitrary ledger account.
func bookBalance(t *testing.T, l *ledger.Book, acct ledger.AccountID) ledger.Amount {
	t.Helper()
	bal, err := l.BookBalance(context.Background(), acct)
	assertNoError(t, err)
	return bal
}

// customerBalance returns the book balance of a customer deposit account at a
// participant, resolved through the participant's deposit layer.
func customerBalance(t *testing.T, p *Participant, acct deposit.AccountID) ledger.Amount {
	t.Helper()
	bal, err := p.Deposit.GetBalance(context.Background(), acct)
	assertNoError(t, err)
	return bal.Book
}

// assertReserveMirror checks that a bank's own reserve asset equals the
// central bank's view of that bank's reserve.
func assertReserveMirror(t *testing.T, sys *Network, p *Participant) {
	t.Helper()
	own := bookBalance(t, p.Ledger, accountsOf(t, p).Reserve)
	cb, err := sys.ReserveBalance(context.Background(), p.ID, testAsset)
	assertNoError(t, err)
	assertEqual(t, "reserve mirror for "+p.Name, own, cb)
}

// ---------------------------------------------------------------------------
// SEPA Credit Transfer
// ---------------------------------------------------------------------------

func TestSCT_HappyPath(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	var pay Payment
	runCycle(t, sys, SchemeSEPACT, func() {
		var err error
		pay, err = sys.InitiatePayment(ctx, InitiatePaymentRequest{
			Scheme:      SchemeSEPACT,
			Debtor:      PartyRef{Participant: a.ID, Account: alice},
			Creditor:    PartyRef{Participant: b.ID, Account: bob},
			Amount:      30000,
			Description: "Invoice 42",
		})
		assertNoError(t, err)
		assertEqual(t, "status after initiation", pay.Status, Accepted)
		// Debtor leg is value-dated to settlement (T+1).
		assertEqual(t, "value date", pay.ValueDate, fixedTime.Add(24*time.Hour))
		// Alice paid immediately; Bob not yet credited.
		assertEqual(t, "alice after init", customerBalance(t, a, alice), 70000)
		assertEqual(t, "bob after init", customerBalance(t, b, bob), 0)
	})

	// After settlement the money has arrived and suspense is flat.
	assertEqual(t, "alice final", customerBalance(t, a, alice), 70000)
	assertEqual(t, "bob final", customerBalance(t, b, bob), 30000)
	assertEqual(t, "bank A suspense", bookBalance(t, a.Ledger, accountsOf(t, a).Suspense), 0)
	assertEqual(t, "bank B suspense", bookBalance(t, b.Ledger, accountsOf(t, b).Suspense), 0)
	assertEqual(t, "bank A reserve", bookBalance(t, a.Ledger, accountsOf(t, a).Reserve), 70000)
	assertEqual(t, "bank B reserve", bookBalance(t, b.Ledger, accountsOf(t, b).Reserve), 30000)
	assertReserveMirror(t, sys, a)
	assertReserveMirror(t, sys, b)

	got, err := sys.GetPayment(ctx, pay.ID)
	assertNoError(t, err)
	assertEqual(t, "status settled", got.Status, Settled)
}

func TestSCT_Netting(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	// Fund Bob so Bank B can also be a payer.
	assertNoError(t, sys.Deposit(ctx, b.ID, bob, 50000, "Bob opening deposit"))

	st := runCycle(t, sys, SchemeSEPACT, func() {
		_, err := sys.InitiatePayment(ctx, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 30000,
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		})
		assertNoError(t, err)
		_, err = sys.InitiatePayment(ctx, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 10000,
			Debtor: PartyRef{Participant: b.ID, Account: bob}, Creditor: PartyRef{Participant: a.ID, Account: alice},
		})
		assertNoError(t, err)
	})

	// Net: A owes 30000, receives 10000 => net -20000; B is the mirror +20000.
	assertEqual(t, "net A", st.NetPositions[a.ID], -20000)
	assertEqual(t, "net B", st.NetPositions[b.ID], 20000)

	// Gross customer movements still apply: Alice -30000 +10000, Bob -10000 +30000.
	assertEqual(t, "alice", customerBalance(t, a, alice), 80000)
	assertEqual(t, "bob", customerBalance(t, b, bob), 70000)
	// Reserves moved only by the net.
	assertEqual(t, "bank A reserve", bookBalance(t, a.Ledger, accountsOf(t, a).Reserve), 80000)
	assertEqual(t, "bank B reserve", bookBalance(t, b.Ledger, accountsOf(t, b).Reserve), 70000)
	assertReserveMirror(t, sys, a)
	assertReserveMirror(t, sys, b)
}

func TestSCT_InsufficientFunds(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	_, err = sys.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 150000, // more than Alice has
		Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
	})
	assertError(t, err, deposit.ErrInsufficientAvailable)
}

// ---------------------------------------------------------------------------
// SEPA Direct Debit
// ---------------------------------------------------------------------------

func TestSDD_HappyPath(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)

	debtor := PartyRef{Participant: a.ID, Account: alice}
	creditor := PartyRef{Participant: b.ID, Account: biller}
	m, err := sys.CreateMandate(ctx, debtor, creditor, 0)
	assertNoError(t, err)

	var pay Payment
	runCycle(t, sys, SchemeSEPADD, func() {
		pay, err = sys.InitiatePayment(ctx, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
			Debtor: debtor, Creditor: creditor, Description: "Electricity bill",
		})
		assertNoError(t, err)
		assertEqual(t, "value date T+2", pay.ValueDate, fixedTime.Add(48*time.Hour))
	})

	assertEqual(t, "alice", customerBalance(t, a, alice), 75000)
	assertEqual(t, "biller", customerBalance(t, b, biller), 25000)
	assertReserveMirror(t, sys, a)
	assertReserveMirror(t, sys, b)
}

func TestSDD_MandateValidation(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)
	debtor := PartyRef{Participant: a.ID, Account: alice}
	creditor := PartyRef{Participant: b.ID, Account: biller}

	limited, err := sys.CreateMandate(ctx, debtor, creditor, 5000)
	assertNoError(t, err)
	revoked, err := sys.CreateMandate(ctx, debtor, creditor, 0)
	assertNoError(t, err)
	assertNoError(t, sys.RevokeMandate(ctx, revoked.ID))

	cases := []struct {
		name      string
		mandateID MandateID
		amount    ledger.Amount
		want      error
	}{
		{"no mandate", "", 1000, ErrMandateRequired},
		{"unknown mandate", "mnd_999", 1000, ErrMandateNotFound},
		{"revoked mandate", revoked.ID, 1000, ErrMandateRevoked},
		{"exceeds max", limited.ID, 6000, ErrMandateExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sys.OpenCycle(ctx, SchemeSEPADD)
			assertNoError(t, err)
			_, err = sys.InitiatePayment(ctx, InitiatePaymentRequest{
				Scheme: SchemeSEPADD, Amount: tc.amount, MandateID: tc.mandateID,
				Debtor: debtor, Creditor: creditor,
			})
			assertError(t, err, tc.want)
			// Tidy up the open cycle for the next sub-test.
			cyc, err := sys.OpenCycleID(ctx, SchemeSEPADD)
			assertNoError(t, err)
			_, _ = sys.CloseCycle(ctx, cyc)
		})
	}
}

func TestSDD_Return(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)
	debtor := PartyRef{Participant: a.ID, Account: alice}
	creditor := PartyRef{Participant: b.ID, Account: biller}
	m, err := sys.CreateMandate(ctx, debtor, creditor, 0)
	assertNoError(t, err)

	var pay Payment
	runCycle(t, sys, SchemeSEPADD, func() {
		pay, err = sys.InitiatePayment(ctx, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
			Debtor: debtor, Creditor: creditor,
		})
		assertNoError(t, err)
	})
	assertEqual(t, "alice after collection", customerBalance(t, a, alice), 75000)

	returned, err := sys.ReturnPayment(ctx, pay.ID, "insufficient funds at debtor")
	assertNoError(t, err)
	assertEqual(t, "status", returned.Status, Returned)

	// Money fully unwound across all three ledgers.
	assertEqual(t, "alice refunded", customerBalance(t, a, alice), 100000)
	assertEqual(t, "biller clawed back", customerBalance(t, b, biller), 0)
	assertReserveMirror(t, sys, a)
	assertReserveMirror(t, sys, b)
}

// ---------------------------------------------------------------------------
// Settlement atomicity
// ---------------------------------------------------------------------------

// SettleCycle posts across every participant's book and the central bank's. One
// store means one transaction: a failure partway must leave no postings at all.
func TestSettleCycleIsAtomic(t *testing.T) {
	ctx := context.Background()
	net, cycleID := newClosedCycleWithUnderfundedMember(t) // one member lacks reserves

	before := reserveBalances(t, ctx, net)

	_, err := net.SettleCycle(ctx, cycleID)
	if err == nil {
		t.Fatal("SettleCycle succeeded, want failure on the underfunded member")
	}

	after := reserveBalances(t, ctx, net)
	for id, want := range before {
		assertEqual(t, "reserve balance for "+string(id), after[id], want)
	}
}

// TestSettleCycleRollsBackEveryLayer is the assertion the reserve balances
// above cannot make on their own.
//
// Reserve balances are read at the central bank, so they catch a committed
// central-bank settlement transaction — which is exactly what the old
// implementation left behind — but they say nothing about the participants'
// own books, the payments' statuses or the settlement record. A two-transaction
// implementation that happened to post the participant legs first would slip
// past the test above; it cannot slip past this one.
func TestSettleCycleRollsBackEveryLayer(t *testing.T) {
	ctx := context.Background()
	net, cycleID := newClosedCycleWithUnderfundedMember(t)

	cycle, err := net.GetCycle(ctx, cycleID)
	assertNoError(t, err)

	participants, err := net.ListParticipants(ctx)
	assertNoError(t, err)

	// Snapshot every layer the settlement would touch.
	suspenseBefore := map[ParticipantID]ledger.Amount{}
	reserveBefore := map[ParticipantID]ledger.Amount{}
	txCountBefore := map[ParticipantID]int{}
	for _, p := range participants {
		suspenseBefore[p.ID] = bookBalance(t, p.Ledger, accountsOf(t, p).Suspense)
		reserveBefore[p.ID] = bookBalance(t, p.Ledger, accountsOf(t, p).Reserve)
		txs, err := p.Ledger.ListTransactions(ctx)
		assertNoError(t, err)
		txCountBefore[p.ID] = len(txs)
	}
	cbTxBefore, err := net.CentralBank().ListTransactions(ctx)
	assertNoError(t, err)

	_, err = net.SettleCycle(ctx, cycleID)
	if err == nil {
		t.Fatal("SettleCycle succeeded, want failure on the underfunded member")
	}

	// No participant's own book moved, and none of them gained a transaction.
	for _, p := range participants {
		assertEqual(t, "suspense at "+p.Name, bookBalance(t, p.Ledger, accountsOf(t, p).Suspense), suspenseBefore[p.ID])
		assertEqual(t, "reserve at "+p.Name, bookBalance(t, p.Ledger, accountsOf(t, p).Reserve), reserveBefore[p.ID])
		txs, err := p.Ledger.ListTransactions(ctx)
		assertNoError(t, err)
		assertEqual(t, "transaction count at "+p.Name, len(txs), txCountBefore[p.ID])
	}

	// The central bank did not gain the settlement transaction.
	cbTxAfter, err := net.CentralBank().ListTransactions(ctx)
	assertNoError(t, err)
	assertEqual(t, "central bank transaction count", len(cbTxAfter), len(cbTxBefore))

	// No settlement was recorded, and the cycle is still Closed rather than
	// Settled, so the operation can be retried once the member is funded.
	settlements, err := net.ListSettlements(ctx)
	assertNoError(t, err)
	assertEqual(t, "settlements recorded", len(settlements), 0)

	after, err := net.GetCycle(ctx, cycleID)
	assertNoError(t, err)
	assertEqual(t, "cycle status", after.Status, CycleClosed)

	// The payments are still Cleared: none of them was marked Settled.
	for _, pid := range cycle.PaymentIDs {
		p, err := net.GetPayment(ctx, pid)
		assertNoError(t, err)
		assertEqual(t, "status of "+string(pid), p.Status, Cleared)
	}
}

// newClosedCycleWithUnderfundedMember builds a closed cycle in which one net
// payer cannot cover its position at the central bank.
//
// The gap is opened with an overdraft: Carol's account at Bank C may go
// negative, so her payment passes the deposit layer's funds check, but Bank C's
// reserve at the central bank is a plain asset with no overdraft — so the
// mirror posting that draws it down fails. That is the real shape of the
// failure a settlement window exists to contain: the instruction is valid, the
// member's liquidity is not.
func newClosedCycleWithUnderfundedMember(t *testing.T) (*Network, CycleID) {
	t.Helper()
	ctx := context.Background()
	sys := testNetwork(t)

	a, err := sys.AddParticipant(ctx, "Bank A", euroOnly) // net receiver
	assertNoError(t, err)
	b, err := sys.AddParticipant(ctx, "Bank B", euroOnly) // solvent net payer
	assertNoError(t, err)
	c, err := sys.AddParticipant(ctx, "Bank C", euroOnly) // underfunded net payer
	assertNoError(t, err)

	alice, err := a.OpenCustomerAccount(ctx, "Alice", testAsset)
	assertNoError(t, err)
	bob, err := b.OpenCustomerAccount(ctx, "Bob", testAsset)
	assertNoError(t, err)
	carol, err := c.Deposit.OpenAccount(ctx, c.CustomerSubledger, "Carol", testAsset, 100000)
	assertNoError(t, err)

	assertNoError(t, sys.Deposit(ctx, a.ID, alice.ID, 100000, "Alice opening deposit"))
	assertNoError(t, sys.Deposit(ctx, b.ID, bob.ID, 100000, "Bob opening deposit"))
	assertNoError(t, sys.Deposit(ctx, c.ID, carol.ID, 10000, "Carol opening deposit"))

	cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)

	// Bank B pays 20000 it has the reserves for.
	_, err = sys.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 20000,
		Debtor: PartyRef{Participant: b.ID, Account: bob.ID}, Creditor: PartyRef{Participant: a.ID, Account: alice.ID},
	})
	assertNoError(t, err)
	// Bank C pays 60000 its customer can afford on overdraft and it cannot.
	_, err = sys.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 60000,
		Debtor: PartyRef{Participant: c.ID, Account: carol.ID}, Creditor: PartyRef{Participant: a.ID, Account: alice.ID},
	})
	assertNoError(t, err)

	_, err = sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)
	return sys, cyc.ID
}

// reserveBalances reads every participant's reserve as held at the central bank.
func reserveBalances(t *testing.T, ctx context.Context, sys *Network) map[ParticipantID]ledger.Amount {
	t.Helper()
	participants, err := sys.ListParticipants(ctx)
	assertNoError(t, err)

	out := make(map[ParticipantID]ledger.Amount, len(participants))
	for _, p := range participants {
		bal, err := sys.ReserveBalance(ctx, p.ID, testAsset)
		assertNoError(t, err)
		out[p.ID] = bal
	}
	return out
}

// SettleCycle writes the central bank's settlement entries in the order the
// participants were registered. Go randomises map iteration, so a settlement
// built by ranging over the net positions produced a different entry order on
// every run — and that order is persisted.
func TestSettlementEntryOrderIsDeterministic(t *testing.T) {
	ctx := context.Background()

	order := func() string {
		sys := testNetwork(t)
		var banks []*Participant
		var accounts []deposit.Account
		for _, name := range []string{"Bank A", "Bank B", "Bank C", "Bank D"} {
			p, err := sys.AddParticipant(ctx, name, euroOnly)
			assertNoError(t, err)
			acct, err := p.OpenCustomerAccount(ctx, "Customer at "+name, testAsset)
			assertNoError(t, err)
			assertNoError(t, sys.Deposit(ctx, p.ID, acct.ID, 100000, "opening"))
			banks = append(banks, p)
			accounts = append(accounts, acct)
		}

		// Every bank ends up with a non-zero net position, so every one of them
		// contributes an entry to the settlement transaction.
		st := runCycle(t, sys, SchemeSEPACT, func() {
			for i := range banks {
				j := (i + 1) % len(banks)
				_, err := sys.InitiatePayment(ctx, InitiatePaymentRequest{
					Scheme: SchemeSEPACT, Amount: ledger.Amount(1000 * (i + 1)),
					Debtor:   PartyRef{Participant: banks[i].ID, Account: accounts[i].ID},
					Creditor: PartyRef{Participant: banks[j].ID, Account: accounts[j].ID},
				})
				assertNoError(t, err)
			}
		})

		tx, err := sys.CentralBank().GetTransaction(ctx, st.SettlementTx)
		assertNoError(t, err)
		assertEqual(t, "settlement entry count", len(tx.Entries), 4)

		var s string
		for _, e := range tx.Entries {
			s += string(e.AccountID) + " "
		}
		return s
	}

	// Five runs, because a map with four keys iterated in a random order
	// repeats itself only about one time in twenty-four.
	want := order()
	for range 5 {
		assertEqual(t, "settlement entry order", order(), want)
	}
}

// ---------------------------------------------------------------------------
// State machine, idempotency, and validation guards
// ---------------------------------------------------------------------------

func TestStateMachineGuards(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	mkPayment := func() (Payment, CycleID) {
		cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
		assertNoError(t, err)
		p, err := sys.InitiatePayment(ctx, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 10000,
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		})
		assertNoError(t, err)
		return p, cyc.ID
	}

	t.Run("settle before close", func(t *testing.T) {
		_, cyc := mkPayment()
		_, err := sys.SettleCycle(ctx, cyc)
		assertError(t, err, ErrCycleNotClosed)
		_, _ = sys.CloseCycle(ctx, cyc)
		_, _ = sys.SettleCycle(ctx, cyc)
	})

	t.Run("double settle", func(t *testing.T) {
		_, cyc := mkPayment()
		_, err := sys.CloseCycle(ctx, cyc)
		assertNoError(t, err)
		_, err = sys.SettleCycle(ctx, cyc)
		assertNoError(t, err)
		_, err = sys.SettleCycle(ctx, cyc)
		assertError(t, err, ErrCycleNotClosed)
	})

	t.Run("return before settle", func(t *testing.T) {
		p, cyc := mkPayment()
		_, err := sys.ReturnPayment(ctx, p.ID, "too early")
		assertError(t, err, ErrInvalidStateTransition)
		_, _ = sys.CloseCycle(ctx, cyc)
		_, _ = sys.SettleCycle(ctx, cyc)
	})

	t.Run("reject after settle", func(t *testing.T) {
		p, cyc := mkPayment()
		_, err := sys.CloseCycle(ctx, cyc)
		assertNoError(t, err)
		_, err = sys.SettleCycle(ctx, cyc)
		assertNoError(t, err)
		_, err = sys.RejectPayment(ctx, p.ID, "too late")
		assertError(t, err, ErrInvalidStateTransition)
	})
}

func TestRejectPayment_ReversesDebtorLeg(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	p, err := sys.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 40000,
		Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
	})
	assertNoError(t, err)
	assertEqual(t, "alice debited", customerBalance(t, a, alice), 60000)

	rejected, err := sys.RejectPayment(ctx, p.ID, "operator cancelled")
	assertNoError(t, err)
	assertEqual(t, "status", rejected.Status, Rejected)
	assertEqual(t, "alice restored", customerBalance(t, a, alice), 100000)
	assertEqual(t, "suspense flat", bookBalance(t, a.Ledger, accountsOf(t, a).Suspense), 0)
}

func TestDuplicateEndToEndID(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	req := InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 1000, EndToEndID: "e2e-1",
		Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
	}
	_, err = sys.InitiatePayment(ctx, req)
	assertNoError(t, err)
	_, err = sys.InitiatePayment(ctx, req)
	assertError(t, err, ErrDuplicateEndToEndID)
}

func TestInitiatePayment_Validation(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)

	t.Run("unknown scheme", func(t *testing.T) {
		_, err := sys.InitiatePayment(ctx, InitiatePaymentRequest{
			Scheme: "nope", Amount: 1000,
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		})
		assertError(t, err, ErrSchemeNotFound)
	})
	t.Run("non-positive amount", func(t *testing.T) {
		_, err := sys.InitiatePayment(ctx, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 0,
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		})
		assertError(t, err, ErrInvalidPaymentAmount)
	})
	t.Run("account not in participant", func(t *testing.T) {
		_, err := sys.InitiatePayment(ctx, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 1000,
			Debtor: PartyRef{Participant: a.ID, Account: "999.999.999"}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		})
		assertError(t, err, ErrAccountNotInParticipant)
	})
	t.Run("no open cycle", func(t *testing.T) {
		_, err := sys.InitiatePayment(ctx, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 1000, // no SDD cycle open
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		})
		assertError(t, err, ErrCycleNotOpen)
	})
}

func TestOpenCycle_AlreadyOpen(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	_, err = sys.OpenCycle(ctx, SchemeSEPACT)
	assertError(t, err, ErrCycleAlreadyOpen)
}

// ---------------------------------------------------------------------------
// Assets
// ---------------------------------------------------------------------------

func TestParticipantHasAccountsPerAsset(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	p, err := sys.AddParticipant(ctx, "Alpha", []ledger.AssetCode{"EUR", "USD"})
	assertNoError(t, err)

	for _, asset := range []ledger.AssetCode{"EUR", "USD"} {
		accts, err := p.AccountsFor(asset)
		assertNoError(t, err)
		for name, id := range map[string]ledger.AccountID{
			"suspense": accts.Suspense, "reserve": accts.Reserve, "settlement": accts.Settlement,
		} {
			if id == "" {
				t.Errorf("%s account for %s is empty", name, asset)
			}
		}
		// Each of the three accounts must itself be denominated in that asset,
		// two of them in the bank's book and one in the central bank's.
		suspense, err := p.Ledger.GetAccount(ctx, accts.Suspense)
		assertNoError(t, err)
		assertEqual(t, "suspense asset", suspense.Asset, asset)
		reserve, err := p.Ledger.GetAccount(ctx, accts.Reserve)
		assertNoError(t, err)
		assertEqual(t, "reserve asset", reserve.Asset, asset)
		settlement, err := sys.CentralBank().GetAccount(ctx, accts.Settlement)
		assertNoError(t, err)
		assertEqual(t, "settlement asset", settlement.Asset, asset)
	}

	// And they survive the store, rather than only the value AddParticipant
	// returned.
	reloaded, err := sys.GetParticipant(ctx, p.ID)
	assertNoError(t, err)
	assertEqual(t, "assets after a reload", len(reloaded.Assets), 2)
}

func TestAccountsForUnknownAssetFails(t *testing.T) {
	sys := testNetwork(t)

	p, err := sys.AddParticipant(context.Background(), "Alpha", nil) // defaults to EUR
	assertNoError(t, err)
	assertEqual(t, "assets a bank joins with by default", len(p.Assets), 1)

	_, err = p.AccountsFor("BTC")
	assertError(t, err, ErrParticipantAssetNotFound)
}

// Settlement must never fall back to a base currency when a member does not
// hold the cycle's asset. Taking the euro away from a member that is about to
// settle simulates the state a future non-euro scheme would produce naturally.
func TestSettleCycleFailsWhenParticipantLacksTheAsset(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	_, err = sys.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 30000,
		Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
	})
	assertNoError(t, err)
	_, err = sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)

	stored, err := sys.GetParticipant(ctx, b.ID)
	assertNoError(t, err)
	delete(stored.Assets, testAsset)
	assertNoError(t, sys.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		return tx.PutParticipant(ctx, *stored)
	}))

	_, err = sys.SettleCycle(ctx, cyc.ID)
	assertError(t, err, ErrParticipantAssetNotFound)

	// And nothing was posted: the batch fails whole, exactly as it does for a
	// member that cannot cover its position. The payer's money is still in its
	// bank's suspense, where the debtor leg left it.
	settlements, err := sys.ListSettlements(ctx)
	assertNoError(t, err)
	assertEqual(t, "settlements recorded", len(settlements), 0)

	after, err := sys.GetCycle(ctx, cyc.ID)
	assertNoError(t, err)
	assertEqual(t, "cycle status", after.Status, CycleClosed)
	assertEqual(t, "bank A suspense", bookBalance(t, a.Ledger, accountsOf(t, a).Suspense), 30000)
	assertEqual(t, "bob was not credited", customerBalance(t, b, bob), 0)
}

// ---------------------------------------------------------------------------
// Enum String() methods
// ---------------------------------------------------------------------------

func TestStringers(t *testing.T) {
	assertEqual(t, "push", Push.String(), "Push")
	assertEqual(t, "pull", Pull.String(), "Pull")
	assertEqual(t, "dir unknown", SchemeDirection(99).String(), "Unknown")
	assertEqual(t, "net", Net.String(), "Net")
	assertEqual(t, "gross", Gross.String(), "Gross")
	assertEqual(t, "model unknown", SettlementModel(99).String(), "Unknown")
	assertEqual(t, "settled", Settled.String(), "Settled")
	assertEqual(t, "returned", Returned.String(), "Returned")
	assertEqual(t, "status unknown", PaymentStatus(99).String(), "Unknown")
	assertEqual(t, "mandate active", MandateActive.String(), "Active")
	assertEqual(t, "mandate unknown", MandateStatus(99).String(), "Unknown")
	assertEqual(t, "cycle closed", CycleClosed.String(), "Closed")
	assertEqual(t, "cycle unknown", CycleStatus(99).String(), "Unknown")
}

// ---------------------------------------------------------------------------
// Test assertion helpers (mirroring the ledger package's style)
// ---------------------------------------------------------------------------

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertError(t *testing.T, err, target error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %v, got nil", target)
	}
	if !errors.Is(err, target) {
		t.Fatalf("expected error %v, got %v", target, err)
	}
}

func assertEqual[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
}
