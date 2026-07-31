package payment_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
	. "github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
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
//
// Both accounts carry an IBAN — not because this fixture is about addressing,
// but because most of it is not: every scheme in play here is SEPA, so an
// account it cannot address is not a usable test fixture at all.
func setupTwoBanks(t *testing.T, sys *Network) (a, b *Participant, alice, bob deposit.AccountID) {
	t.Helper()
	ctx := context.Background()

	a, err := sys.AddParticipant(ctx, "Bank A", euroOnly)
	assertNoError(t, err)
	b, err = sys.AddParticipant(ctx, "Bank B", euroOnly)
	assertNoError(t, err)

	aliceAcct := openCustomer(t, ctx, a, "Alice", "SE89-BANKA-0001")
	bobAcct := openCustomer(t, ctx, b, "Bob", "SE89-BANKB-0001")

	assertNoError(t, sys.Deposit(ctx, a.ID, aliceAcct.ID, 100000, "Alice opening deposit"))
	return a, b, aliceAcct.ID, bobAcct.ID
}

// addParticipant adds a euro-only participant bank, failing the test on
// error. It is setupTwoBanks' AddParticipant call, factored out for tests
// that want more than two banks or want to name them themselves.
func addParticipant(t *testing.T, ctx context.Context, sys *Network, name string) *Participant {
	t.Helper()
	p, err := sys.AddParticipant(ctx, name, euroOnly)
	assertNoError(t, err)
	return p
}

// openCustomer opens a customer deposit account at p, addressed by the given
// IBAN. It goes through p.Deposit.OpenAccount directly, rather than
// p.OpenCustomerAccount, because the identifier is a variadic argument that
// only the register method takes — mirroring how seed.go attaches an IBAN to
// every sample account.
func openCustomer(t *testing.T, ctx context.Context, p *Participant, name, iban string) deposit.Account {
	t.Helper()
	ident := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: iban}
	acct, err := p.Deposit.OpenAccount(ctx, p.CustomerSubledger, name, testAsset, p.ProductID, 0, ident)
	assertNoError(t, err)
	return acct
}

// openCustomerWithoutIdentifier opens a customer deposit account at p with no
// address at all — the fixture for proving a scheme refuses to route to an
// account it cannot address, rather than merely one whose quoted address is
// wrong.
func openCustomerWithoutIdentifier(t *testing.T, ctx context.Context, p *Participant, name string) deposit.Account {
	t.Helper()
	acct, err := p.OpenCustomerAccount(ctx, name, testAsset)
	assertNoError(t, err)
	return acct
}

// openCycle opens a clearing cycle for the given scheme, failing the test on
// error. It is runCycle's opening step, factored out for tests that only need
// a cycle open to initiate into — not the full open/close/settle round trip.
func openCycle(t *testing.T, ctx context.Context, sys *Network, scheme SchemeID) {
	t.Helper()
	_, err := sys.OpenCycle(ctx, scheme)
	assertNoError(t, err)
}

// fundAccount deposits amount into a customer account, failing the test on
// error.
func fundAccount(t *testing.T, ctx context.Context, sys *Network, p *Participant, acct deposit.Account, amount ledger.Amount) {
	t.Helper()
	assertNoError(t, sys.Deposit(ctx, p.ID, acct.ID, amount, "opening deposit"))
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

// PSD2 Art. 87(2): the payer's debit value date may be no earlier than the
// moment the amount leaves the account, so the customer's leg must not share
// the suspense leg's settlement-date value date.
func TestInitiateValueDatesTheCustomerLegToTheDebit(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	p, err := sys.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme:      SchemeSEPACT,
		Debtor:      PartyRef{Participant: a.ID, Account: alice},
		Creditor:    PartyRef{Participant: b.ID, Account: bob},
		Amount:      10000,
		Description: "Rent",
	})
	assertNoError(t, err)

	posted, err := a.Ledger.GetTransaction(ctx, p.DebtorLegTx)
	assertNoError(t, err)
	assertEqual(t, "transaction value date is the settlement date", posted.ValueDate, p.ValueDate)

	debtorAcct, err := a.Deposit.GetAccount(ctx, alice)
	assertNoError(t, err)
	debtorGL := debtorAcct.GLAccount

	// The two-way split below only names the legs correctly if there are
	// exactly two of them: with a third, "not the debtor's account" would
	// silently pick whichever came last.
	if len(posted.Entries) != 2 {
		t.Fatalf("debtor leg has %d entries, want 2 (customer and suspense)", len(posted.Entries))
	}
	var customer, suspense ledger.Entry
	for _, e := range posted.Entries {
		if e.AccountID == debtorGL {
			customer = e
		} else {
			suspense = e
		}
	}

	if !customer.ValueDate.Equal(posted.BookingDate) {
		t.Errorf("customer leg value date = %v, want the booking date %v (PSD2 Art. 87(2))",
			customer.ValueDate, posted.BookingDate)
	}
	if !suspense.ValueDate.Equal(p.ValueDate) {
		t.Errorf("suspense leg value date = %v, want the settlement date %v", suspense.ValueDate, p.ValueDate)
	}
	if customer.ValueDate.Equal(suspense.ValueDate) {
		t.Fatal("the two legs must not share a value date; that is the whole point")
	}
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

	alice := openCustomer(t, ctx, a, "Alice", "SE89-BANKA-0001")
	bob := openCustomer(t, ctx, b, "Bob", "SE89-BANKB-0001")
	carol, err := c.Deposit.OpenAccount(ctx, c.CustomerSubledger, "Carol", testAsset, c.ProductID, 100000,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-BANKC-0001"})
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
		for i, name := range []string{"Bank A", "Bank B", "Bank C", "Bank D"} {
			p, err := sys.AddParticipant(ctx, name, euroOnly)
			assertNoError(t, err)
			acct := openCustomer(t, ctx, p, "Customer at "+name, fmt.Sprintf("SE89-BANK%d-0001", i))
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

// The ledger cannot catch this at initiation: the debtor leg alone is a EUR
// debit against a EUR credit, valid double-entry that says nothing about the
// creditor's account. It surfaces only at settlement, and then as a failure of
// the whole cycle — see
// TestCrossAssetPaymentSurvivesInitiationAndFailsTheWholeCycle. The scheme
// check is what makes the refusal immediate and attributable.
func TestPaymentRejectsCreditorAccountNotInSchemeAsset(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	alpha, err := sys.AddParticipant(ctx, "Alpha", euroOnly)
	assertNoError(t, err)
	beta, err := sys.AddParticipant(ctx, "Beta", euroOnly)
	assertNoError(t, err)

	from, err := alpha.OpenCustomerAccount(ctx, "Anna", testAsset)
	assertNoError(t, err)
	to, err := beta.OpenCustomerAccount(ctx, "Bruno", "BTC")
	assertNoError(t, err)

	_, err = sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)

	_, err = sys.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 1000,
		Debtor:   PartyRef{Participant: alpha.ID, Account: from.ID},
		Creditor: PartyRef{Participant: beta.ID, Account: to.ID},
	})
	assertError(t, err, ErrAssetMismatch)
}

// The creditor leg above proves the check reaches an account that may live in a
// different participant's book — nothing forbids a payment whose debtor and
// creditor are in the same one. This proves the debtor leg is checked too: a
// scheme that only validated the creditor would let this one through.
func TestPaymentRejectsDebtorAccountNotInSchemeAsset(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	alpha, err := sys.AddParticipant(ctx, "Alpha", euroOnly)
	assertNoError(t, err)
	beta, err := sys.AddParticipant(ctx, "Beta", euroOnly)
	assertNoError(t, err)

	from, err := alpha.OpenCustomerAccount(ctx, "Anna", "BTC")
	assertNoError(t, err)
	to, err := beta.OpenCustomerAccount(ctx, "Bruno", testAsset)
	assertNoError(t, err)

	_, err = sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)

	_, err = sys.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 1000,
		Debtor:   PartyRef{Participant: alpha.ID, Account: from.ID},
		Creditor: PartyRef{Participant: beta.ID, Account: to.ID},
	})
	assertError(t, err, ErrAssetMismatch)
}

// SEPA Direct Debit used to inherit the asset check only because SDD.Validate
// happened to call validateFunds. Now that the check runs unconditionally in
// InitiatePaymentTx, before any scheme's Validate is reached, it applies to
// SDD structurally rather than by that scheme's choice. A valid mandate keeps
// SDD's own mandate guards out of the way, so this isolates the asset check.
func TestSDDPaymentRejectsAccountNotInSchemeAsset(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	alpha, err := sys.AddParticipant(ctx, "Alpha", euroOnly)
	assertNoError(t, err)
	beta, err := sys.AddParticipant(ctx, "Beta", euroOnly)
	assertNoError(t, err)

	// Both ends in BTC: a mandate's two accounts have to agree (see
	// CreateMandateTx), so the mismatch under test is between the mandate's
	// asset and the SEPA scheme's, not between the two accounts.
	debtorAcct, err := alpha.OpenCustomerAccount(ctx, "Anna", "BTC")
	assertNoError(t, err)
	creditorAcct, err := beta.OpenCustomerAccount(ctx, "Bruno", "BTC")
	assertNoError(t, err)

	debtor := PartyRef{Participant: alpha.ID, Account: debtorAcct.ID}
	creditor := PartyRef{Participant: beta.ID, Account: creditorAcct.ID}
	m, err := sys.CreateMandate(ctx, debtor, creditor, 0)
	assertNoError(t, err)

	_, err = sys.OpenCycle(ctx, SchemeSEPADD)
	assertNoError(t, err)

	_, err = sys.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme: SchemeSEPADD, Amount: 1000, MandateID: m.ID,
		Debtor: debtor, Creditor: creditor,
	})
	assertError(t, err, ErrAssetMismatch)
}

// A mandate holds two accounts and one MaxAmount. An integer that means 50.00
// at the debtor's scale and 0.0000005 at the creditor's is not a ceiling on
// anything, so the two accounts have to agree before the mandate exists —
// rather than every payment it could authorize failing later.
func TestCreateMandateRejectsMismatchedAssets(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	alpha, err := sys.AddParticipant(ctx, "Alpha", euroOnly)
	assertNoError(t, err)
	beta, err := sys.AddParticipant(ctx, "Beta", euroOnly)
	assertNoError(t, err)

	debtorAcct, err := alpha.OpenCustomerAccount(ctx, "Anna", testAsset)
	assertNoError(t, err)
	creditorAcct, err := beta.OpenCustomerAccount(ctx, "Bruno", "BTC")
	assertNoError(t, err)

	_, err = sys.CreateMandate(ctx,
		PartyRef{Participant: alpha.ID, Account: debtorAcct.ID},
		PartyRef{Participant: beta.ID, Account: creditorAcct.ID}, 50000)
	assertError(t, err, ErrAssetMismatch)

	// And nothing was written: a refused mandate is not a mandate.
	mandates, err := sys.ListMandates(ctx)
	assertNoError(t, err)
	assertEqual(t, "mandates recorded", len(mandates), 0)
}

// What the ledger does and does not catch about a euro-to-bitcoin payment.
//
// The claim this test exists to keep honest is a narrow one, and it was stated
// too broadly once already. The ledger DOES catch a cross-asset payment — at
// settlement. SettleCycleTx resolves the creditor's suspense account with
// creditor.AccountsFor(scheme.Asset()), so the creditor leg comes out as a EUR
// suspense debit against a BTC credit, and validateBalance refuses it with
// ErrUnbalancedAsset.
//
// What the ledger cannot catch is the payment at INITIATION. The debtor leg on
// its own is impeccable double-entry within one asset, and nothing in that
// posting says a second posting elsewhere is its other half.
//
// The cost of finding out late is what ErrAssetMismatch buys: settlement is
// all-or-nothing, so one bad payment takes down the whole clearing cycle, and
// the error names an imbalance rather than the payment that caused it.
//
// Constructing the state needs the store directly, because ErrAssetMismatch
// now refuses such a payment at initiation — which is the point.
func TestCrossAssetPaymentSurvivesInitiationAndFailsTheWholeCycle(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	// A BTC account in a euro-only bank: allowed, because an account's asset
	// is validated against the known assets, not against what its bank
	// clears in.
	bobBTC, err := b.OpenCustomerAccount(ctx, "Bob BTC", "BTC")
	assertNoError(t, err)

	cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	pay, err := sys.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 30000,
		Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
	})
	assertNoError(t, err)

	// The debtor leg is already posted and already balanced — in EUR, on its
	// own, with nothing wrong with it.
	assertEqual(t, "bank A suspense after initiation", bookBalance(t, a.Ledger, accountsOf(t, a).Suspense), 30000)

	// Point the creditor end at the bitcoin account, the state ErrAssetMismatch
	// exists to prevent.
	assertNoError(t, sys.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		stored, err := tx.GetPayment(ctx, pay.ID)
		if err != nil {
			return err
		}
		stored.Creditor.Account = bobBTC.ID
		return tx.PutPayment(ctx, stored)
	}))

	_, err = sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)

	_, err = sys.SettleCycle(ctx, cyc.ID)
	assertError(t, err, ledger.ErrUnbalancedAsset)

	// The whole batch fails, not the one payment: nothing settled, and Alice's
	// money is still where the debtor leg left it.
	settlements, err := sys.ListSettlements(ctx)
	assertNoError(t, err)
	assertEqual(t, "settlements recorded", len(settlements), 0)
	assertEqual(t, "bank A suspense", bookBalance(t, a.Ledger, accountsOf(t, a).Suspense), 30000)
	assertEqual(t, "bob was not credited", customerBalance(t, b, bob), 0)
}

// SEPA is a euro scheme, not a scheme that happens to be tested with EUR
// accounts. This pins that fact directly on the scheme types.
func TestSEPASchemesAreEuroSchemes(t *testing.T) {
	for _, sc := range []Scheme{SCT{}, SDD{}} {
		if sc.Asset() != "EUR" {
			t.Errorf("%s asset = %q, want EUR", sc.ID(), sc.Asset())
		}
	}
}

// ---------------------------------------------------------------------------
// Participant.RunEndOfDay: driving deposit and lending together
// ---------------------------------------------------------------------------

func TestParticipantRunEndOfDay_DrivesBothLayers(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	bank, err := net.AddParticipant(ctx, "Aurora Bank", euroOnly)
	assertNoError(t, err)

	// An overdrawn current account with a priced overdraft.
	bruno, err := bank.OpenCustomerAccount(ctx, "Bruno Bianchi", testAsset)
	assertNoError(t, err)
	_, err = bank.Deposit.SetOverdraftLimit(ctx, bruno.ID, 50_000, time.Time{})
	assertNoError(t, err)
	_, err = bank.Deposit.SetOverdraftPricingOverlay(ctx, bruno.ID,
		&product.OverdraftPricing{Rate: 150_000, DayCount: interest.ACT365}, time.Time{})
	assertNoError(t, err)
	assertNoError(t, net.Deposit(ctx, bank.ID, bruno.ID, 5_000, "Opening deposit"))

	// And a drawn revolving line.
	line, err := bank.Lending.OpenRevolvingLine(ctx, bank.CustomerSubledger, "Bruno Line", testAsset, 250_000, 180_000, interest.ACT365, 20_000)
	assertNoError(t, err)
	brunoGL, err := bank.Deposit.GetAccount(ctx, bruno.ID)
	assertNoError(t, err)
	_, err = bank.Lending.Draw(ctx, line.ID, brunoGL.GLAccount, 100_000, "Draw")
	assertNoError(t, err)

	// Spend the account into overdraft.
	merchant, err := bank.OpenCustomerAccount(ctx, "Merchant", testAsset)
	assertNoError(t, err)
	merchantGL, err := bank.Deposit.GetAccount(ctx, merchant.ID)
	assertNoError(t, err)
	_, err = bank.Ledger.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "Card payment",
		Entries: []ledger.Entry{
			{AccountID: brunoGL.GLAccount, Amount: 110_000, Direction: ledger.Debit},
			{AccountID: merchantGL.GLAccount, Amount: 110_000, Direction: ledger.Credit},
		},
	})
	assertNoError(t, err)

	// One call runs both batches.
	start := fixedTime
	assertNoError(t, bank.RunEndOfDay(ctx, start))
	assertNoError(t, bank.RunEndOfDay(ctx, start.AddDate(0, 0, 1)))

	account, err := bank.Deposit.GetAccount(ctx, bruno.ID)
	assertNoError(t, err)
	if account.Accrued <= 0 {
		t.Errorf("the overdraft did not accrue: %d", account.Accrued)
	}

	facility, err := bank.Lending.GetFacility(ctx, line.ID)
	assertNoError(t, err)
	if facility.Accrued <= 0 {
		t.Errorf("the line did not accrue: %d", facility.Accrued)
	}
}

// A bank with neither loans nor priced overdrafts must still run — over a real
// portfolio that is most banks on most days.
func TestParticipantRunEndOfDay_QuietBank(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	bank, err := net.AddParticipant(ctx, "Quiet Bank", euroOnly)
	assertNoError(t, err)
	assertNoError(t, bank.RunEndOfDay(ctx, fixedTime))
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

// ---------------------------------------------------------------------------
// ResolveIdentifier — the network's directory
// ---------------------------------------------------------------------------

func TestResolveIdentifierAcrossBanks(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank")
	verde := addParticipant(t, ctx, net, "Banca Verde")

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	_ = openCustomer(t, ctx, verde, "Bruno", "IT60-VERDE-2001")

	ref, err := net.ResolveIdentifier(ctx, deposit.Identifier{
		Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001",
	})
	if err != nil {
		t.Fatalf("ResolveIdentifier: %v", err)
	}
	if ref.Participant != aurora.ID || ref.Account != alice.ID {
		t.Fatalf("resolved %s/%s, want %s/%s", ref.Participant, ref.Account, aurora.ID, alice.ID)
	}
}

func TestResolveIdentifierNotFound(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	addParticipant(t, ctx, net, "Aurora Bank")

	_, err := net.ResolveIdentifier(ctx, deposit.Identifier{
		Scheme: deposit.IdentifierIBAN, Value: "NOBODY-0001",
	})
	if !errors.Is(err, deposit.ErrIdentifierNotFound) {
		t.Fatalf("ResolveIdentifier = %v, want ErrIdentifierNotFound", err)
	}
}

func TestResolveIdentifierRefusesACrossBankCollision(t *testing.T) {
	// Per-bank uniqueness makes this reachable, so the network must not pick
	// one. Two banks claiming one address is not an address.
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank")
	verde := addParticipant(t, ctx, net, "Banca Verde")

	openCustomer(t, ctx, aurora, "Alice", "SHARED-0001")
	openCustomer(t, ctx, verde, "Bruno", "SHARED-0001")

	_, err := net.ResolveIdentifier(ctx, deposit.Identifier{
		Scheme: deposit.IdentifierIBAN, Value: "SHARED-0001",
	})
	if !errors.Is(err, deposit.ErrIdentifierAmbiguous) {
		t.Fatalf("ResolveIdentifier = %v, want ErrIdentifierAmbiguous", err)
	}
}

// The other half of the same claim, and the half nothing held.
//
// The sweep accumulates hits ACROSS members with `hits += len(holders)`, so two
// accounts inside ONE bank are ambiguous through the network exactly as two
// banks are — which is what the README and the account-addressing hint assert,
// and what makes the missing UNIQUE constraint safe. Only the cross-bank half
// was tested, and this sentence has already had to be corrected once.
//
// The duplicate is written straight through the store, past the register's
// write-time check, because that is the only way it arises: a race between two
// AddIdentifier calls that both read before either wrote.
func TestResolveIdentifierRefusesAWithinBankCollision(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank")
	addParticipant(t, ctx, net, "Banca Verde")

	shared := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SHARED-0001"}
	openCustomer(t, ctx, aurora, "Alice", "SHARED-0001")
	aaron := openCustomer(t, ctx, aurora, "Aaron", "SE89-AURORA-0002")

	assertNoError(t, aurora.Deposit.Store().Update(ctx, func(ctx context.Context, tx deposit.Tx) error {
		a, err := tx.GetDepositAccount(ctx, aurora.Deposit.BookID(), aaron.ID)
		if err != nil {
			return err
		}
		a.Identifiers = append(a.Identifiers, shared)
		return tx.PutDepositAccount(ctx, aurora.Deposit.BookID(), a)
	}))

	if _, err := net.ResolveIdentifier(ctx, shared); !errors.Is(err, deposit.ErrIdentifierAmbiguous) {
		t.Fatalf("ResolveIdentifier = %v, want ErrIdentifierAmbiguous", err)
	}
}

// ---------------------------------------------------------------------------
// AddressedBy — the scheme declares what addresses it, initiation enforces it
// ---------------------------------------------------------------------------

func TestInitiateRefusesAnAccountWithNoIdentifierInTheSchemesScheme(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank")
	verde := addParticipant(t, ctx, net, "Banca Verde")
	openCycle(t, ctx, net, SchemeSEPACT)

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	// Bruno has no IBAN at all: an SCT cannot address him.
	bruno := openCustomerWithoutIdentifier(t, ctx, verde, "Bruno")

	_, err := net.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme:   SchemeSEPACT,
		Debtor:   PartyRef{Participant: aurora.ID, Account: alice.ID},
		Creditor: PartyRef{Participant: verde.ID, Account: bruno.ID},
		Amount:   10_00,
	})
	if !errors.Is(err, ErrUnaddressableAccount) {
		t.Fatalf("InitiatePayment = %v, want ErrUnaddressableAccount", err)
	}
}

func TestInitiateRefusesAQuotedIdentifierTheAccountDoesNotHold(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank")
	verde := addParticipant(t, ctx, net, "Banca Verde")
	openCycle(t, ctx, net, SchemeSEPACT)

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno", "IT60-VERDE-2001")

	_, err := net.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme: SchemeSEPACT,
		Debtor: PartyRef{Participant: aurora.ID, Account: alice.ID},
		Creditor: PartyRef{
			Participant: verde.ID, Account: bruno.ID,
			// Somebody else's address, pointing at Bruno's account.
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"},
		},
		Amount: 10_00,
	})
	if !errors.Is(err, ErrIdentifierMismatch) {
		t.Fatalf("InitiatePayment = %v, want ErrIdentifierMismatch", err)
	}
}

func TestInitiateRefusesAQuotedIdentifierOnTheDebtorLeg(t *testing.T) {
	// The creditor-leg case above and this one are separate tests on purpose:
	// checkAddressable is called once per leg, and a check wired to only one of
	// them passes every creditor-leg test in the file.
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank")
	verde := addParticipant(t, ctx, net, "Banca Verde")
	openCycle(t, ctx, net, SchemeSEPACT)

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno", "IT60-VERDE-2001")

	_, err := net.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme: SchemeSEPACT,
		Debtor: PartyRef{
			Participant: aurora.ID, Account: alice.ID,
			// Bruno's address, pointing at Alice's account.
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "IT60-VERDE-2001"},
		},
		Creditor: PartyRef{Participant: verde.ID, Account: bruno.ID},
		Amount:   10_00,
	})
	if !errors.Is(err, ErrIdentifierMismatch) {
		t.Fatalf("InitiatePayment = %v, want ErrIdentifierMismatch", err)
	}
}

// An address the account really holds, but in the wrong identifier scheme.
//
// testPAN is declared here rather than shipped as a deposit constant, because
// the card scheme it would address does not exist yet — which is the whole
// reason this test matters. Nothing in the shipped system can reach this case
// while IBAN is the only scheme, so the check would have gone on being wrong
// until the day a PAN arrived, and on that day it would have been wrong
// silently: an account holding both would have had a SEPA payment accepted, and
// stored, quoting its card number. Scheme.AddressedBy() means the address is
// bound to the scheme, not merely to the account.
func TestInitiateRefusesAnAddressFromAnotherIdentifierScheme(t *testing.T) {
	const testPAN = deposit.IdentifierScheme("PAN")

	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank")
	verde := addParticipant(t, ctx, net, "Banca Verde")

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno", "IT60-VERDE-2001")

	// Both customers keep their IBAN and gain a card, exactly as the design
	// says a plural identifier set is for.
	alicePAN := deposit.Identifier{Scheme: testPAN, Value: "4000-0000-0000-0001"}
	brunoPAN := deposit.Identifier{Scheme: testPAN, Value: "4000-0000-0000-0002"}
	assertNoError(t, aurora.Deposit.AddIdentifier(ctx, alice.ID, alicePAN))
	assertNoError(t, verde.Deposit.AddIdentifier(ctx, bruno.ID, brunoPAN))

	// Creditor leg.
	_, err := net.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme:   SchemeSEPACT,
		Debtor:   PartyRef{Participant: aurora.ID, Account: alice.ID},
		Creditor: PartyRef{Participant: verde.ID, Account: bruno.ID, Identifier: brunoPAN},
		Amount:   10_00,
	})
	if !errors.Is(err, ErrIdentifierMismatch) {
		t.Fatalf("creditor quoting a PAN for an SCT = %v, want ErrIdentifierMismatch", err)
	}

	// Debtor leg.
	_, err = net.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme:   SchemeSEPACT,
		Debtor:   PartyRef{Participant: aurora.ID, Account: alice.ID, Identifier: alicePAN},
		Creditor: PartyRef{Participant: verde.ID, Account: bruno.ID},
		Amount:   10_00,
	})
	if !errors.Is(err, ErrIdentifierMismatch) {
		t.Fatalf("debtor quoting a PAN for an SCT = %v, want ErrIdentifierMismatch", err)
	}

	// And the PAN does not disturb the back-fill: one IBAN each is still one
	// candidate each, so the ordinary payment goes through and records IBANs.
	var pay Payment
	runCycle(t, net, SchemeSEPACT, func() {
		pay, err = net.InitiatePayment(ctx, InitiatePaymentRequest{
			Scheme:   SchemeSEPACT,
			Debtor:   PartyRef{Participant: aurora.ID, Account: alice.ID},
			Creditor: PartyRef{Participant: verde.ID, Account: bruno.ID},
			Amount:   10_00,
		})
		assertNoError(t, err)
	})
	assertEqual(t, "back-filled debtor address", pay.Debtor.Identifier,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"})
	assertEqual(t, "back-filled creditor address", pay.Creditor.Identifier,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "IT60-VERDE-2001"})
}

// A payment that quotes no address still records one. Before this, the
// identifier was optional on the way in and simply stayed empty on the way to
// storage, so the documented property — "a payment records the address it was
// sent to" — held only for callers who volunteered it, which the API's own
// tests never did.
func TestInitiateBackFillsTheAddressOnBothLegs(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank")
	verde := addParticipant(t, ctx, net, "Banca Verde")

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno", "IT60-VERDE-2001")

	var pay Payment
	runCycle(t, net, SchemeSEPACT, func() {
		var err error
		pay, err = net.InitiatePayment(ctx, InitiatePaymentRequest{
			Scheme:   SchemeSEPACT,
			Debtor:   PartyRef{Participant: aurora.ID, Account: alice.ID},
			Creditor: PartyRef{Participant: verde.ID, Account: bruno.ID},
			Amount:   10_00,
		})
		assertNoError(t, err)
	})

	assertEqual(t, "back-filled debtor address", pay.Debtor.Identifier,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"})
	assertEqual(t, "back-filled creditor address", pay.Creditor.Identifier,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "IT60-VERDE-2001"})

	// And the address that reached storage is the same one, not just the one
	// the call returned.
	stored, err := net.GetPayment(ctx, pay.ID)
	assertNoError(t, err)
	assertEqual(t, "stored debtor address", stored.Debtor.Identifier, pay.Debtor.Identifier)
	assertEqual(t, "stored creditor address", stored.Creditor.Identifier, pay.Creditor.Identifier)
}

// Back-filling stops where choosing would start. Two IBANs on one account and
// nothing quoted is the same shape as an ambiguous resolution, and gets the
// same answer.
func TestInitiateRefusesToChooseBetweenTwoAddresses(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank")
	verde := addParticipant(t, ctx, net, "Banca Verde")

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno", "IT60-VERDE-2001")
	// A second IBAN on the debtor: legal, and it makes the debtor leg the one
	// that has to refuse. No cycle is open yet, which is harmless — the
	// addressing checks run before initiation looks for one.
	assertNoError(t, aurora.Deposit.AddIdentifier(ctx, alice.ID,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1002"}))

	_, err := net.InitiatePayment(ctx, InitiatePaymentRequest{
		Scheme:   SchemeSEPACT,
		Debtor:   PartyRef{Participant: aurora.ID, Account: alice.ID},
		Creditor: PartyRef{Participant: verde.ID, Account: bruno.ID},
		Amount:   10_00,
	})
	if !errors.Is(err, ErrAmbiguousAddress) {
		t.Fatalf("InitiatePayment = %v, want ErrAmbiguousAddress", err)
	}

	// Naming one of them is how the caller gets past it — the refusal is a
	// question, not a dead end.
	runCycle(t, net, SchemeSEPACT, func() {
		pay, err := net.InitiatePayment(ctx, InitiatePaymentRequest{
			Scheme: SchemeSEPACT,
			Debtor: PartyRef{
				Participant: aurora.ID, Account: alice.ID,
				Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1002"},
			},
			Creditor: PartyRef{Participant: verde.ID, Account: bruno.ID},
			Amount:   10_00,
		})
		assertNoError(t, err)
		assertEqual(t, "chosen debtor address", pay.Debtor.Identifier,
			deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1002"})
	})
}

// Reissuing an address must not kill the mandates on the account.
//
// A remove plus an add is the documented way to reissue a card, and it moves
// neither balance nor history. Before PartyRef.SameParty it moved something
// else: the mandate compared whole PartyRefs, so after the reissue there was NO
// address the payment could quote that worked — the new one differed from the
// mandate's, the old one no longer belonged to the account, and quoting nothing
// back-filled the new one. With no UpdateMandate, the mandate was dead for good.
func TestMandateSurvivesAReissuedDebtorIdentifier(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)

	debtor := PartyRef{Participant: a.ID, Account: alice}
	creditor := PartyRef{Participant: b.ID, Account: biller}
	m, err := sys.CreateMandate(ctx, debtor, creditor, 0)
	assertNoError(t, err)

	old := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-BANKA-0001"}
	reissued := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-BANKA-0002"}
	assertNoError(t, a.Deposit.RemoveIdentifier(ctx, alice, old))
	assertNoError(t, a.Deposit.AddIdentifier(ctx, alice, reissued))

	var pay Payment
	runCycle(t, sys, SchemeSEPADD, func() {
		pay, err = sys.InitiatePayment(ctx, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
			Debtor: debtor, Creditor: creditor, Description: "Electricity bill",
		})
		assertNoError(t, err)
	})

	// The collection went through, and it records the NEW address — the mandate
	// authorises the account, and each payment records how it was reached.
	assertEqual(t, "reissued debtor address on the payment", pay.Debtor.Identifier, reissued)
	assertEqual(t, "alice", customerBalance(t, a, alice), 75000)
	assertEqual(t, "biller", customerBalance(t, b, biller), 25000)
}

func TestSchemesDeclareTheirIdentifierScheme(t *testing.T) {
	if got := (SCT{}).AddressedBy(); got != deposit.IdentifierIBAN {
		t.Fatalf("SCT.AddressedBy() = %q, want %q", got, deposit.IdentifierIBAN)
	}
	if got := (SDD{}).AddressedBy(); got != deposit.IdentifierIBAN {
		t.Fatalf("SDD.AddressedBy() = %q, want %q", got, deposit.IdentifierIBAN)
	}
}
