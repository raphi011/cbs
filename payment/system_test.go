package payment_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/iso20022"
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

// testBIC is a structurally valid ISO 9362 BIC used as the default across
// these tests. There is no uniqueness constraint on it (see participants.bic's
// column comment), so a test bank sharing it with another is not automatically
// a fixture bug — except where a test's own assertion turns on telling two
// banks' BICs apart, which is what testBIC2 is for.
const testBIC iso20022.BIC = "BANKDEFFXXX"

// testBIC2 is a second, distinct BIC for fixtures where two banks' BICs must
// be tellable apart — setupTwoBanks uses it for Bank B so that a test planting
// one bank's BIC where the other's belongs (as
// TestSubmitDerivesTheCounterpartyAgentFromTheRoster does) can actually catch
// a derivation that silently passes the wrong value through.
const testBIC2 iso20022.BIC = "BANKGB2LXXX"

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

// initiate runs all three halves of an initiation — the submitting bank's, the
// receiving bank's and the clearing house's — in one unit of work, and returns
// the payment Accepted in its scheme's open cycle.
//
// It is what InitiatePayment used to be, and it is a TEST helper rather than a
// method because there is deliberately no such method any more: a single call
// that validates both ends of a payment is precisely the thing sub-project 7b
// removed. Every test below that is not about the split uses this, so those
// tests keep asserting the end state the whole choreography produces; the
// tests that ARE about the split call the halves directly.
//
// One Tx for all three, so a payment the far side refuses leaves nothing
// behind — which is what these tests were written against. In the mesh the
// three halves are three actors and three units of work, and a refusal after
// the debtor leg is posted is a reversal (Task 9).
func initiate(ctx context.Context, sys *Network, req InitiatePaymentRequest) (Payment, error) {
	var out Payment
	err := sys.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		p, err := sys.SubmitPaymentTx(ctx, tx, req)
		if err != nil {
			return err
		}
		if err := sys.AcceptInboundTx(ctx, tx, p.ID); err != nil {
			return err
		}
		out, err = sys.AcceptAtCSMTx(ctx, tx, p.ID)
		return err
	})
	return out, err
}

// reject runs both halves of a rejection — the clearing house's transition and
// the debtor bank's reversal of its own leg — in one unit of work, and returns
// the Rejected payment.
//
// It is what RejectPayment used to be, and it is a TEST helper for the same
// reason initiate is: there is deliberately no method that plays both actors.
// The tests below that are not about the split use this, so they keep asserting
// the end state a whole rejection produces; the tests that ARE about the split
// call the halves directly and see the state between them.
func reject(ctx context.Context, sys *Network, id PaymentID, code iso20022.StatusReason, reason string) (Payment, error) {
	var out Payment
	err := sys.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = sys.RejectAtCSMTx(ctx, tx, id, code, reason)
		if err != nil {
			return err
		}
		return sys.ReverseDebtorLegTx(ctx, tx, out, reason)
	})
	return out, err
}

// setupTwoBanks creates two participant banks, opens a customer account at
// each (Alice at Bank A, Bob at Bank B), and funds Alice with 100000. The
// returned account IDs are deposit account IDs; their backing GL account IDs
// are resolved when checking book balances.
//
// Both accounts carry an IBAN — not because this fixture is about addressing,
// but because most of it is not: every scheme in play here is SEPA, so an
// account it cannot address is not a usable test fixture at all.
//
// Bank A and Bank B are given DISTINCT BICs (testBIC and testBIC2) rather than
// sharing testBIC the way single-bank fixtures do: this is the two-bank
// fixture that TestSubmitDerivesTheCounterpartyAgentFromTheRoster builds on,
// and a test that plants one bank's BIC where the other's belongs needs the
// two to actually differ or the plant is indistinguishable from the roster
// value it is meant to be wrong against.
func setupTwoBanks(t *testing.T, sys *Network) (a, b *Participant, alice, bob deposit.AccountID) {
	t.Helper()
	ctx := context.Background()

	a, err := sys.AddParticipant(ctx, "Bank A", testBIC, euroOnly)
	assertNoError(t, err)
	b, err = sys.AddParticipant(ctx, "Bank B", testBIC2, euroOnly)
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
	p, err := sys.AddParticipant(ctx, name, testBIC, euroOnly)
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

// suspenseBalance returns a bank's clearing-suspense balance in the test asset,
// looked up by participant id. It is where a payer's money sits between the
// debtor leg and settlement, so it is the balance that says whether an actor
// touched the debtor bank's own book.
func suspenseBalance(t *testing.T, n *Network, id ParticipantID) ledger.Amount {
	t.Helper()
	p, err := n.GetParticipant(context.Background(), id)
	assertNoError(t, err)
	return bookBalance(t, p.Ledger, accountsOf(t, p).Suspense)
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
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme:          SchemeSEPACT,
			Debtor:          PartyRef{Participant: a.ID, Account: alice},
			Creditor:        PartyRef{Participant: b.ID, Account: bob},
			Amount:          30000,
			Description:     "Invoice 42",
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
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
	p, err := initiate(ctx, sys, InitiatePaymentRequest{
		Scheme:          SchemeSEPACT,
		Debtor:          PartyRef{Participant: a.ID, Account: alice},
		Creditor:        PartyRef{Participant: b.ID, Account: bob},
		Amount:          10000,
		Description:     "Rent",
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
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
		_, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 30000,
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		})
		assertNoError(t, err)
		_, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 10000,
			Debtor: PartyRef{Participant: b.ID, Account: bob}, Creditor: PartyRef{Participant: a.ID, Account: alice},
			CreditorDetails: PartyDetails{Agent: a.BIC, Name: "Alice"},
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
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 150000, // more than Alice has
		Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
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
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
			Debtor: debtor, Creditor: creditor, Description: "Electricity bill",
			DebtorDetails: PartyDetails{Agent: a.BIC, Name: "Alice"},
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
			_, err = initiate(ctx, sys, InitiatePaymentRequest{
				Scheme: SchemeSEPADD, Amount: tc.amount, MandateID: tc.mandateID,
				Debtor: debtor, Creditor: creditor,
				DebtorDetails: PartyDetails{Agent: a.BIC, Name: "Alice"},
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
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
			Debtor:        debtor,
			Creditor:      creditor,
			DebtorDetails: PartyDetails{Agent: a.BIC, Name: "Alice"},
		})
		assertNoError(t, err)
	})
	assertEqual(t, "alice after collection", customerBalance(t, a, alice), 75000)

	returned, err := sys.ReturnPayment(ctx, pay.ID, "insufficient funds at debtor")
	assertNoError(t, err)
	assertEqual(t, "status", returned.Status, Returned)
	// A return is not a rejection: it carries no StatusReason. pacs.004 draws
	// its reason from iso20022.ReturnReason instead — a different external
	// code set — and ReturnPaymentTx does not set RejectCode, only the free
	// text. See the RejectCode doc comment on payment.Payment.
	assertEqual(t, "return sets no reject code", string(returned.RejectCode), "")

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
	// Settled — which is what leaves the operation retriable once the member is
	// funded. Retrying it is not this layer's act: in the mesh the clearing
	// house re-sends the pacs.009 (POST /cycles/{cid}/settle, mesh.csm.settle),
	// and SettleCycleTx's CycleClosed guard is what makes asking twice safe.
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

	a, err := sys.AddParticipant(ctx, "Bank A", testBIC, euroOnly) // net receiver
	assertNoError(t, err)
	b, err := sys.AddParticipant(ctx, "Bank B", testBIC, euroOnly) // solvent net payer
	assertNoError(t, err)
	c, err := sys.AddParticipant(ctx, "Bank C", testBIC, euroOnly) // underfunded net payer
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
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 20000,
		Debtor: PartyRef{Participant: b.ID, Account: bob.ID}, Creditor: PartyRef{Participant: a.ID, Account: alice.ID},
		CreditorDetails: PartyDetails{Agent: a.BIC, Name: alice.Name},
	})
	assertNoError(t, err)
	// Bank C pays 60000 its customer can afford on overdraft and it cannot.
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 60000,
		Debtor: PartyRef{Participant: c.ID, Account: carol.ID}, Creditor: PartyRef{Participant: a.ID, Account: alice.ID},
		CreditorDetails: PartyDetails{Agent: a.BIC, Name: alice.Name},
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
			p, err := sys.AddParticipant(ctx, name, testBIC, euroOnly)
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
				_, err := initiate(ctx, sys, InitiatePaymentRequest{
					Scheme: SchemeSEPACT, Amount: ledger.Amount(1000 * (i + 1)),
					Debtor:          PartyRef{Participant: banks[i].ID, Account: accounts[i].ID},
					Creditor:        PartyRef{Participant: banks[j].ID, Account: accounts[j].ID},
					CreditorDetails: PartyDetails{Agent: banks[j].BIC, Name: accounts[j].Name},
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
		p, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 10000,
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
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
		_, err = reject(ctx, sys, p.ID, iso20022.StatusReasonNotSpecifiedAgentGenerated, "too late")
		assertError(t, err, ErrInvalidStateTransition)
	})
}

func TestRejectionReversesTheDebtorLeg(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	p, err := initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 40000,
		Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
	})
	assertNoError(t, err)
	assertEqual(t, "alice debited", customerBalance(t, a, alice), 60000)

	rejected, err := reject(ctx, sys, p.ID, iso20022.StatusReasonNotSpecifiedAgentGenerated, "operator cancelled")
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
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
	}
	_, err = initiate(ctx, sys, req)
	assertNoError(t, err)
	_, err = initiate(ctx, sys, req)
	assertError(t, err, ErrDuplicateEndToEndID)
}

func TestInitiatePayment_Validation(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)

	t.Run("unknown scheme", func(t *testing.T) {
		_, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: "nope", Amount: 1000,
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		})
		assertError(t, err, ErrSchemeNotFound)
	})
	t.Run("non-positive amount", func(t *testing.T) {
		_, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 0,
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		})
		assertError(t, err, ErrInvalidPaymentAmount)
	})
	t.Run("account not in participant", func(t *testing.T) {
		_, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 1000,
			Debtor: PartyRef{Participant: a.ID, Account: "999.999.999"}, Creditor: PartyRef{Participant: b.ID, Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		})
		assertError(t, err, ErrAccountNotInParticipant)
	})
	t.Run("no open cycle", func(t *testing.T) {
		// The cut-off belongs to the clearing house, so this refusal comes
		// from its half and not from either bank's: both banks accept a
		// collection they are perfectly happy with, and AcceptAtCSMTx is what
		// finds no window open for sepa.dd. A mandate is needed to get that
		// far at all — without one the creditor's own bank refuses first.
		m, err := sys.CreateMandate(ctx,
			PartyRef{Participant: a.ID, Account: alice},
			PartyRef{Participant: b.ID, Account: bob}, 0)
		assertNoError(t, err)

		_, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 1000, MandateID: m.ID, // no SDD cycle open
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
			DebtorDetails: PartyDetails{Agent: a.BIC, Name: "Alice"},
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

	p, err := sys.AddParticipant(ctx, "Alpha", testBIC, []ledger.AssetCode{"EUR", "USD"})
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

	p, err := sys.AddParticipant(context.Background(), "Alpha", testBIC, nil) // defaults to EUR
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
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 30000,
		Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
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

	alpha, err := sys.AddParticipant(ctx, "Alpha", testBIC, euroOnly)
	assertNoError(t, err)
	beta, err := sys.AddParticipant(ctx, "Beta", testBIC, euroOnly)
	assertNoError(t, err)

	// The payer's leg has to be flawless for this test to be about the payee's.
	// Since the split, the debtor's own bank runs its whole half — account,
	// asset, address, funds — before the creditor's bank sees the payment at
	// all, so a payer with no IBAN would fail with ErrUnaddressableAccount and
	// the asset check under test would never run.
	from := openCustomer(t, ctx, alpha, "Anna", "SE89-ALPHA-0001")
	fundAccount(t, ctx, sys, alpha, from, 100000)
	to, err := beta.OpenCustomerAccount(ctx, "Bruno", "BTC")
	assertNoError(t, err)
	assertNoError(t, beta.Deposit.AddIdentifier(ctx, to.ID,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "IT60-BETA-0001"}))

	_, err = sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)

	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme:          SchemeSEPACT,
		Amount:          1000,
		Debtor:          PartyRef{Participant: alpha.ID, Account: from.ID},
		Creditor:        PartyRef{Participant: beta.ID, Account: to.ID},
		CreditorDetails: PartyDetails{Agent: beta.BIC, Name: to.Name},
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

	alpha, err := sys.AddParticipant(ctx, "Alpha", testBIC, euroOnly)
	assertNoError(t, err)
	beta, err := sys.AddParticipant(ctx, "Beta", testBIC, euroOnly)
	assertNoError(t, err)

	// Both accounts are addressable, so the only thing wrong with this payment
	// is the payer's asset: with the asset check removed the payment gets
	// further rather than failing for a second reason, which is what makes the
	// counterfactual on this test sharp.
	from, err := alpha.OpenCustomerAccount(ctx, "Anna", "BTC")
	assertNoError(t, err)
	assertNoError(t, alpha.Deposit.AddIdentifier(ctx, from.ID,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-ALPHA-0001"}))
	to := openCustomer(t, ctx, beta, "Bruno", "IT60-BETA-0001")

	_, err = sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)

	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme:          SchemeSEPACT,
		Amount:          1000,
		Debtor:          PartyRef{Participant: alpha.ID, Account: from.ID},
		Creditor:        PartyRef{Participant: beta.ID, Account: to.ID},
		CreditorDetails: PartyDetails{Agent: beta.BIC, Name: to.Name},
	})
	assertError(t, err, ErrAssetMismatch)
}

// SEPA Direct Debit used to inherit the asset check only because SDD.Validate
// happened to call validateFunds. It now runs unconditionally in each bank's
// own half, before any scheme's Validate is reached, so it applies to SDD
// structurally rather than by that scheme's choice. A valid mandate keeps SDD's
// own mandate guards out of the way, so this isolates the asset check.
//
// Both ends are in BTC, so either half would catch it; a pull is submitted by
// the creditor's bank, so the half that does is the creditor's.
func TestSDDPaymentRejectsAccountNotInSchemeAsset(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	alpha, err := sys.AddParticipant(ctx, "Alpha", testBIC, euroOnly)
	assertNoError(t, err)
	beta, err := sys.AddParticipant(ctx, "Beta", testBIC, euroOnly)
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

	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme:        SchemeSEPADD,
		Amount:        1000,
		MandateID:     m.ID,
		Debtor:        debtor,
		Creditor:      creditor,
		DebtorDetails: PartyDetails{Agent: alpha.BIC, Name: debtorAcct.Name},
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

	alpha, err := sys.AddParticipant(ctx, "Alpha", testBIC, euroOnly)
	assertNoError(t, err)
	beta, err := sys.AddParticipant(ctx, "Beta", testBIC, euroOnly)
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
	pay, err := initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 30000,
		Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
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

	bank, err := net.AddParticipant(ctx, "Aurora Bank", testBIC, euroOnly)
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

	bank, err := net.AddParticipant(ctx, "Quiet Bank", testBIC, euroOnly)
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

	_, err := initiate(ctx, net, InitiatePaymentRequest{
		Scheme:          SchemeSEPACT,
		Debtor:          PartyRef{Participant: aurora.ID, Account: alice.ID},
		Creditor:        PartyRef{Participant: verde.ID, Account: bruno.ID},
		Amount:          10_00,
		CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
	})
	if !errors.Is(err, ErrUnaddressableAccount) {
		t.Fatalf("initiation = %v, want ErrUnaddressableAccount", err)
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

	_, err := initiate(ctx, net, InitiatePaymentRequest{
		Scheme: SchemeSEPACT,
		Debtor: PartyRef{Participant: aurora.ID, Account: alice.ID},
		Creditor: PartyRef{
			Participant: verde.ID, Account: bruno.ID,
			// Somebody else's address, pointing at Bruno's account.
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"},
		},
		Amount:          10_00,
		CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
	})
	if !errors.Is(err, ErrIdentifierMismatch) {
		t.Fatalf("initiation = %v, want ErrIdentifierMismatch", err)
	}
}

func TestInitiateRefusesAQuotedIdentifierOnTheDebtorLeg(t *testing.T) {
	// The creditor-leg case above and this one are separate tests on purpose:
	// addressFor is called once per leg, and a check wired to only one of them
	// passes every creditor-leg test in the file.
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank")
	verde := addParticipant(t, ctx, net, "Banca Verde")
	openCycle(t, ctx, net, SchemeSEPACT)

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno", "IT60-VERDE-2001")

	_, err := initiate(ctx, net, InitiatePaymentRequest{
		Scheme: SchemeSEPACT,
		Debtor: PartyRef{
			Participant: aurora.ID, Account: alice.ID,
			// Bruno's address, pointing at Alice's account.
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "IT60-VERDE-2001"},
		},
		Creditor:        PartyRef{Participant: verde.ID, Account: bruno.ID},
		Amount:          10_00,
		CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
	})
	if !errors.Is(err, ErrIdentifierMismatch) {
		t.Fatalf("initiation = %v, want ErrIdentifierMismatch", err)
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
	_, err := initiate(ctx, net, InitiatePaymentRequest{
		Scheme:          SchemeSEPACT,
		Debtor:          PartyRef{Participant: aurora.ID, Account: alice.ID},
		Creditor:        PartyRef{Participant: verde.ID, Account: bruno.ID, Identifier: brunoPAN},
		Amount:          10_00,
		CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
	})
	if !errors.Is(err, ErrIdentifierMismatch) {
		t.Fatalf("creditor quoting a PAN for an SCT = %v, want ErrIdentifierMismatch", err)
	}

	// Debtor leg.
	_, err = initiate(ctx, net, InitiatePaymentRequest{
		Scheme:          SchemeSEPACT,
		Debtor:          PartyRef{Participant: aurora.ID, Account: alice.ID, Identifier: alicePAN},
		Creditor:        PartyRef{Participant: verde.ID, Account: bruno.ID},
		Amount:          10_00,
		CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
	})
	if !errors.Is(err, ErrIdentifierMismatch) {
		t.Fatalf("debtor quoting a PAN for an SCT = %v, want ErrIdentifierMismatch", err)
	}

	// And the PAN does not disturb the back-fill: one IBAN each is still one
	// candidate each, so the ordinary payment goes through and records IBANs.
	var pay Payment
	runCycle(t, net, SchemeSEPACT, func() {
		pay, err = initiate(ctx, net, InitiatePaymentRequest{
			Scheme:          SchemeSEPACT,
			Debtor:          PartyRef{Participant: aurora.ID, Account: alice.ID},
			Creditor:        PartyRef{Participant: verde.ID, Account: bruno.ID},
			Amount:          10_00,
			CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
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
		pay, err = initiate(ctx, net, InitiatePaymentRequest{
			Scheme:          SchemeSEPACT,
			Debtor:          PartyRef{Participant: aurora.ID, Account: alice.ID},
			Creditor:        PartyRef{Participant: verde.ID, Account: bruno.ID},
			Amount:          10_00,
			CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
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

	_, err := initiate(ctx, net, InitiatePaymentRequest{
		Scheme:          SchemeSEPACT,
		Debtor:          PartyRef{Participant: aurora.ID, Account: alice.ID},
		Creditor:        PartyRef{Participant: verde.ID, Account: bruno.ID},
		Amount:          10_00,
		CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
	})
	if !errors.Is(err, ErrAmbiguousAddress) {
		t.Fatalf("initiation = %v, want ErrAmbiguousAddress", err)
	}

	// Naming one of them is how the caller gets past it — the refusal is a
	// question, not a dead end.
	runCycle(t, net, SchemeSEPACT, func() {
		pay, err := initiate(ctx, net, InitiatePaymentRequest{
			Scheme: SchemeSEPACT,
			Debtor: PartyRef{
				Participant: aurora.ID, Account: alice.ID,
				Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1002"},
			},
			Creditor:        PartyRef{Participant: verde.ID, Account: bruno.ID},
			Amount:          10_00,
			CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
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
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
			Debtor: debtor, Creditor: creditor, Description: "Electricity bill",
			DebtorDetails: PartyDetails{Agent: a.BIC, Name: "Alice"},
		})
		assertNoError(t, err)
	})

	// The collection went through, and it records the NEW address — the mandate
	// authorises the account, and each payment records how it was reached.
	assertEqual(t, "reissued debtor address on the payment", pay.Debtor.Identifier, reissued)
	assertEqual(t, "alice", customerBalance(t, a, alice), 75000)
	assertEqual(t, "biller", customerBalance(t, b, biller), 25000)
}

// The other direction of the same change, and the one that has to keep holding.
//
// SameParty deliberately LOOSENS the mandate comparison — it stops comparing
// the quoted address — so what a reader wants proof of is that it did not
// loosen the part that matters: a mandate still authorises exactly one debtor
// account paying exactly one creditor account, and nothing else. Substituting
// either end is ErrMandateMismatch, which before this wave had no test at all.
func TestMandateStillRefusesADifferentParty(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)

	debtor := PartyRef{Participant: a.ID, Account: alice}
	creditor := PartyRef{Participant: b.ID, Account: biller}
	m, err := sys.CreateMandate(ctx, debtor, creditor, 0)
	assertNoError(t, err)

	// A second customer at Alice's own bank, funded, addressable, and party to
	// nothing. Same bank on purpose: the participant matching is the easy half,
	// and an implementation that compared only Participant would pass a
	// cross-bank version of this test.
	carla := openCustomer(t, ctx, a, "Carla", "SE89-BANKA-0009")
	fundAccount(t, ctx, sys, a, carla, 100000)
	// And a second biller at bank B, for the creditor half.
	other := openCustomer(t, ctx, b, "Other Biller", "SE89-BANKB-0009")

	openCycle(t, ctx, sys, SchemeSEPADD)

	// Someone else's account, drawn on under Alice's mandate.
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
		Debtor: PartyRef{Participant: a.ID, Account: carla.ID}, Creditor: creditor,
		DebtorDetails: PartyDetails{Agent: a.BIC, Name: "Carla"},
	})
	if !errors.Is(err, ErrMandateMismatch) {
		t.Fatalf("substituted debtor = %v, want ErrMandateMismatch", err)
	}

	// Alice's account, collected by a creditor she never authorised.
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
		Debtor: debtor, Creditor: PartyRef{Participant: b.ID, Account: other.ID},
		DebtorDetails: PartyDetails{Agent: a.BIC, Name: "Alice"},
	})
	if !errors.Is(err, ErrMandateMismatch) {
		t.Fatalf("substituted creditor = %v, want ErrMandateMismatch", err)
	}

	// Nothing moved on either attempt: a refused instruction rolls back whole.
	assertEqual(t, "alice", customerBalance(t, a, alice), 100000)
	assertEqual(t, "carla", customerBalance(t, a, carla.ID), 100000)
	assertEqual(t, "biller", customerBalance(t, b, biller), 0)
}

func TestSchemesDeclareTheirIdentifierScheme(t *testing.T) {
	if got := (SCT{}).AddressedBy(); got != deposit.IdentifierIBAN {
		t.Fatalf("SCT.AddressedBy() = %q, want %q", got, deposit.IdentifierIBAN)
	}
	if got := (SDD{}).AddressedBy(); got != deposit.IdentifierIBAN {
		t.Fatalf("SDD.AddressedBy() = %q, want %q", got, deposit.IdentifierIBAN)
	}
}

// ---------------------------------------------------------------------------
// The split: a submitting bank's half and a receiving bank's half
// ---------------------------------------------------------------------------

// networkWithTwoBanks is the split's credit-transfer fixture: two addressed
// banks, a funded payer at the first, a payee at the second, and the request
// between them.
//
// No cycle is opened, deliberately. Submission no longer looks for one — which
// cycle a payment joins is the clearing house's business, and every test below
// that submits without opening one is evidence of that.
func networkWithTwoBanks(t *testing.T) (*Network, InitiatePaymentRequest) {
	t.Helper()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	return sys, InitiatePaymentRequest{
		Scheme:      SchemeSEPACT,
		Amount:      25000,
		Debtor:      PartyRef{Participant: a.ID, Account: alice},
		Creditor:    PartyRef{Participant: b.ID, Account: bob},
		Description: "Invoice 42",
		// Push: the creditor is the counterparty, so the request must name it —
		// the NAME. Its BIC is derived from the roster, so setting one here would
		// be setting a field SubmitPaymentTx discards.
		CreditorDetails: PartyDetails{Name: "Bob"},
	}
}

// networkWithACollection is the direct-debit fixture: two addressed banks, a
// mandate the payee holds over the payer, and the collection request under it.
// fund is what the payer's account holds, so a caller can build both a
// collectable and an uncollectable one.
//
// Bank A and Bank B get distinct BICs (testBIC / testBIC2), not addParticipant's
// shared testBIC — same reason as setupTwoBanks: this is the pull direction's
// two-bank fixture, and TestSubmitDerivesTheCounterpartyAgentFromTheRoster's
// pull subtest plants one bank's BIC where the other's belongs.
func networkWithACollection(t *testing.T, fund ledger.Amount) (*Network, InitiatePaymentRequest, MandateID) {
	t.Helper()
	ctx := context.Background()
	sys := testNetwork(t)
	a, err := sys.AddParticipant(ctx, "Bank A", testBIC, euroOnly)
	assertNoError(t, err)
	b, err := sys.AddParticipant(ctx, "Bank B", testBIC2, euroOnly)
	assertNoError(t, err)
	payer := openCustomer(t, ctx, a, "Alice", "SE89-BANKA-0001")
	payee := openCustomer(t, ctx, b, "Biller", "SE89-BANKB-0001")
	if fund > 0 {
		fundAccount(t, ctx, sys, a, payer, fund)
	}
	debtor := PartyRef{Participant: a.ID, Account: payer.ID}
	creditor := PartyRef{Participant: b.ID, Account: payee.ID}
	m, err := sys.CreateMandate(ctx, debtor, creditor, 0)
	assertNoError(t, err)
	return sys, InitiatePaymentRequest{
		Scheme:      SchemeSEPADD,
		Amount:      25000,
		MandateID:   m.ID,
		Debtor:      debtor,
		Creditor:    creditor,
		Description: "Electricity bill",
		// Pull: the debtor is the counterparty, so the request must name it. See
		// networkWithTwoBanks on why no Agent is set.
		DebtorDetails: PartyDetails{Name: payer.Name},
	}, m.ID
}

func networkWithAMandate(t *testing.T) (*Network, InitiatePaymentRequest) {
	t.Helper()
	sys, req, _ := networkWithACollection(t, 100000)
	return sys, req
}

func networkWithARevokedMandate(t *testing.T) (*Network, InitiatePaymentRequest) {
	t.Helper()
	sys, req, id := networkWithACollection(t, 100000)
	assertNoError(t, sys.RevokeMandate(context.Background(), id))
	return sys, req
}

// networkWithAnUnfundedDebtor is the collection fixture whose payer cannot
// cover it. The creditor's bank cannot see that, which is the point.
func networkWithAnUnfundedDebtor(t *testing.T) (*Network, InitiatePaymentRequest) {
	t.Helper()
	sys, req, _ := networkWithACollection(t, 0)
	return sys, req
}

// networkWithASubmittedPayment is a push payment that has been submitted and
// nothing more: Initiated, its debtor leg posted, in no cycle, and not yet
// answered by the creditor's bank.
func networkWithASubmittedPayment(t *testing.T) (*Network, Payment) {
	t.Helper()
	n, req := networkWithTwoBanks(t)
	p, err := n.SubmitPayment(context.Background(), req)
	assertNoError(t, err)
	return n, p
}

// closeCreditorAccount closes the payee's account at the payee's own bank.
func closeCreditorAccount(t *testing.T, n *Network, p Payment) {
	t.Helper()
	ctx := context.Background()
	bank, err := n.GetParticipant(ctx, p.Creditor.Participant)
	assertNoError(t, err)
	assertNoError(t, bank.Deposit.Close(ctx, p.Creditor.Account))
}

// Initiated becomes an observable state for the first time. Today
// InitiatePaymentTx transitions straight to Accepted inside the submitting
// transaction, so no caller has ever seen it; the mesh needs the payment to
// exist, with its debtor leg posted, while the creditor's bank has not yet
// answered.
func TestSubmitLeavesAPushPaymentInitiatedAndOutOfAnyCycle(t *testing.T) {
	n, req := networkWithTwoBanks(t)

	p, err := n.SubmitPayment(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitPayment: %v", err)
	}
	if p.Status != Initiated {
		t.Errorf("status = %v, want Initiated", p.Status)
	}
	if p.CycleID != "" {
		t.Errorf("cycle = %q, want empty — adding to a cycle is the CSM's act", p.CycleID)
	}
	// The debtor leg IS posted: the payer's money has left, which is what makes
	// a later rejection a reversal rather than a cancellation.
	if p.DebtorLegTx == "" {
		t.Error("no debtor leg posted; a push payment's money leaves at submission")
	}
}

// A pull posts nothing at submission. The creditor's bank has no access to the
// debtor's account and the money has not moved; the debtor's bank posts the
// leg when it accepts the collection.
func TestSubmitPostsNothingForAPullPayment(t *testing.T) {
	n, req := networkWithAMandate(t)
	p, err := n.SubmitPayment(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitPayment: %v", err)
	}
	if p.DebtorLegTx != "" {
		t.Error("a direct debit posted a debtor leg at submission; the debtor's bank has not seen it yet")
	}
	if p.Status != Initiated {
		t.Errorf("status = %v, want Initiated", p.Status)
	}
}

// The submitting half must NOT check the far side. This is the assertion that
// makes the whole sub-project real: before it, InitiatePaymentTx read both
// banks' books in one transaction.
func TestSubmitDoesNotCheckTheCreditorAccount(t *testing.T) {
	n, req := networkWithTwoBanks(t)
	// A creditor account that does not exist. Before the split this was a
	// synchronous ErrAccountNotInParticipant; now it is the creditor bank's to
	// discover and answer with AC01.
	req.Creditor.Account = "no-such-account"

	p, err := n.SubmitPayment(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitPayment refused a payment whose far side it cannot see: %v", err)
	}

	// And the check did not VANISH, it moved: the creditor's bank is the one
	// that discovers the account does not exist, which is the AC01 the mesh
	// answers with. Without this half the inventory's "nothing was dropped"
	// would be a claim no test could contradict — a creditorSideTx that
	// tolerated a missing account passed the whole suite before this line
	// existed.
	if err := n.AcceptInbound(context.Background(), p.ID); !errors.Is(err, ErrAccountNotInParticipant) {
		t.Fatalf("AcceptInbound on an account the creditor's bank does not hold = %v, want ErrAccountNotInParticipant", err)
	}
}

// TestSubmitTakesTheCounterpartyNameFromTheRequest pins the direction rule: the
// submitting bank fills in its OWN side from its own register and is TOLD the
// counterparty's NAME — the agent is derived from the roster instead, not taken
// from the request (see TestSubmitDerivesTheCounterpartyAgentFromTheRoster).
// It never reads the counterparty's register for either.
func TestSubmitTakesTheCounterpartyNameFromTheRequest(t *testing.T) {
	ctx := context.Background()
	n, req := networkWithTwoBanks(t)
	// No Agent set: this test is about the name, and the agent this bank would
	// plant here is discarded and re-derived from the roster regardless — see
	// networkWithTwoBanks on why setting one would just be dead input.
	req.CreditorDetails = PartyDetails{Name: "Whoever The Payer Typed"}
	// A WRONG name on the bank's own side. A merge that copied req.DebtorDetails
	// onto the payment unchanged would pass this test's name check; only an
	// overwrite from the register catches it.
	req.DebtorDetails = PartyDetails{Agent: "WRONGDEFFXXX", Name: "Not Alice At All"}

	p, err := n.SubmitPayment(ctx, req)
	if err != nil {
		t.Fatalf("SubmitPayment: %v", err)
	}
	if got := p.CreditorDetails.Name; got != "Whoever The Payer Typed" {
		t.Errorf("creditor name is %q, want the name the request carried", got)
	}
	// The debtor is this bank's own customer, so its name comes from its own
	// register and NOT from the request — a payer does not get to rename
	// themselves on an instruction. setupTwoBanks names the debtor's customer
	// "Alice", so this is not merely non-empty but exactly the register value.
	if p.DebtorDetails.Name != "Alice" {
		t.Errorf("debtor name is %q, want the submitting bank's own register value %q, not what the request carried", p.DebtorDetails.Name, "Alice")
	}
	debtorBank, err := n.GetParticipant(ctx, req.Debtor.Participant)
	assertNoError(t, err)
	if p.DebtorDetails.Agent != debtorBank.BIC {
		t.Errorf("debtor agent is %q, want the submitting bank's own BIC %q", p.DebtorDetails.Agent, debtorBank.BIC)
	}
}

// TestSubmitRefusesAnUnnamedCounterparty pins that the instruction must carry
// what the message will need. Before this, a request that named no counterparty
// was accepted and the failure surfaced later, when the message was built, out of
// a bank's own register — which is exactly the read being removed.
//
// It is the NAME and only the name. There used to be a third subtest here, "no
// agent", and it went with the guard that produced it: the counterparty's agent
// is derived from the roster now (see TestSubmitDerivesTheCounterpartyAgentFromTheRoster
// below), so an instruction that supplies none is not incomplete — it is
// ordinary, and every request in this file is one. Keeping the subtest would
// have meant keeping a disjunct that can no longer fire for the reason it was
// written.
func TestSubmitRefusesAnUnnamedCounterparty(t *testing.T) {
	for _, tc := range []struct {
		name    string
		details PartyDetails
	}{
		{"no name", PartyDetails{}},
		{"no name, and an agent supplied anyway", PartyDetails{Agent: testBIC}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, req := networkWithTwoBanks(t)
			req.CreditorDetails = tc.details
			if _, err := n.SubmitPayment(context.Background(), req); !errors.Is(err, ErrCounterpartyNotNamed) {
				t.Errorf("got %v, want ErrCounterpartyNotNamed", err)
			}
		})
	}
}

// TestSubmitDerivesTheCounterpartyAgentFromTheRoster is the domain half of
// mesh/books_test.go's TestAWrongCounterpartyAgentDoesNotMisroute: the mesh test
// measures that the payment reaches the right bank, and this one measures what
// makes it so.
//
// The instruction names a BIC that is not the counterparty's — the SUBMITTING
// bank's own, the worst case, because a message routed on it comes straight back
// to its sender — and it is discarded rather than compared. Comparing would be
// the weaker fix in a way worth stating: it would make a wrong BIC a refusal, so
// a payer who typed one would have their payment rejected instead of routed,
// which is not what SEPA does. IBAN-only since 2016 means the originating bank
// derives routing and the payer never supplies it at all.
//
// Both directions, because which side is the counterparty follows the scheme's
// direction and nothing else — a fix applied to the push arm alone would leave
// the pull arm's collecting bank posting the debit in the payer's bank's book.
//
// The two fixtures this test uses (setupTwoBanks, networkWithACollection) give
// their two banks DISTINCT BICs for exactly this test's sake: with both banks
// on the same BIC, "the submitting bank's own" and "the roster's" are the same
// byte string, so the assertion below passes whether or not the agent is
// actually derived. Measured directly — deleting SubmitPaymentTx's
// `counterparty.Agent = counterpartyBank.BIC` line passed both subtests before
// the fixtures were split; giving Bank A and Bank B distinct BICs is what makes
// the derivation this test names something the test can actually catch the
// absence of.
func TestSubmitDerivesTheCounterpartyAgentFromTheRoster(t *testing.T) {
	t.Run("push: the creditor is the counterparty", func(t *testing.T) {
		ctx := context.Background()
		n, req := networkWithTwoBanks(t)
		debtorBank, err := n.GetParticipant(ctx, req.Debtor.Participant)
		assertNoError(t, err)
		creditorBank, err := n.GetParticipant(ctx, req.Creditor.Participant)
		assertNoError(t, err)
		req.CreditorDetails = PartyDetails{Agent: debtorBank.BIC, Name: "Whoever The Payer Typed"}

		p, err := n.SubmitPayment(ctx, req)
		assertNoError(t, err)
		if p.CreditorDetails.Agent != creditorBank.BIC {
			t.Errorf("creditor agent is %q, want the roster's %q for %s", p.CreditorDetails.Agent, creditorBank.BIC, req.Creditor.Participant)
		}
		if p.CreditorDetails.Name != "Whoever The Payer Typed" {
			t.Errorf("creditor name is %q, want the name the instruction carried — only the agent is derived", p.CreditorDetails.Name)
		}
	})

	t.Run("pull: the debtor is the counterparty", func(t *testing.T) {
		ctx := context.Background()
		n, req, _ := networkWithACollection(t, 100000)
		debtorBank, err := n.GetParticipant(ctx, req.Debtor.Participant)
		assertNoError(t, err)
		creditorBank, err := n.GetParticipant(ctx, req.Creditor.Participant)
		assertNoError(t, err)
		req.DebtorDetails = PartyDetails{Agent: creditorBank.BIC, Name: "Whoever The Biller Typed"}

		p, err := n.SubmitPayment(ctx, req)
		assertNoError(t, err)
		if p.DebtorDetails.Agent != debtorBank.BIC {
			t.Errorf("debtor agent is %q, want the roster's %q for %s", p.DebtorDetails.Agent, debtorBank.BIC, req.Debtor.Participant)
		}
		if p.DebtorDetails.Name != "Whoever The Biller Typed" {
			t.Errorf("debtor name is %q, want the name the instruction carried — only the agent is derived", p.DebtorDetails.Name)
		}
	})
}

// TestSubmitRefusesACounterpartyAtNoSuchBank is the other side of deriving the
// agent: there has to be a roster row to derive it FROM.
//
// ErrParticipantNotFound and not ErrCounterpartyNotNamed, which is the accurate
// one of the two — the instruction did name a bank, and the trouble is that no
// such bank is a member. It is also what the wire needs: ReasonFor maps it to
// RC01 "bank identifier incorrect", which is exactly the statement being made.
//
// This is a real widening of what the submitting bank checks, and it is worth
// being explicit that it does not undo the split. The roster is network-scoped —
// tx.GetParticipant takes no BookID — so this reads no bank's book, and the
// creditor's ACCOUNT is still none of the submitting bank's business:
// TestSubmitDoesNotCheckTheCreditorAccount above still passes with an account
// that does not exist. A bank that is not a member cannot be routed to by
// anybody, which is not a fact about the payee's bank at all.
func TestSubmitRefusesACounterpartyAtNoSuchBank(t *testing.T) {
	n, req := networkWithTwoBanks(t)
	req.Creditor.Participant = "no-such-bank"

	if _, err := n.SubmitPayment(context.Background(), req); !errors.Is(err, ErrParticipantNotFound) {
		t.Errorf("got %v, want ErrParticipantNotFound", err)
	}
}

// TestSubmitRefusesItsOwnPartyAtNoSuchBank pins checkPartyTx's PARTICIPANT arm,
// which had no test at all: TestSubmitDoesNotCheckTheCreditorAccount covers the
// account half of the same function, and nothing covered the half above it.
//
// It goes through the SUBMITTING bank's own side deliberately. The counterparty
// arm is TestSubmitRefusesACounterpartyAtNoSuchBank's and reaches a different
// function (the roster read in SubmitPaymentTx); this one reaches checkPartyTx,
// which is also what AcceptInboundTx runs on the receiving side.
func TestSubmitRefusesItsOwnPartyAtNoSuchBank(t *testing.T) {
	n, req := networkWithTwoBanks(t)
	req.Debtor.Participant = "no-such-bank"

	if _, err := n.SubmitPayment(context.Background(), req); !errors.Is(err, ErrParticipantNotFound) {
		t.Errorf("got %v, want ErrParticipantNotFound", err)
	}
}

// TestAcceptInboundDoesNotRewriteEitherPartysDetails pins a fix, not merely a
// property: debtorSideTx and creditorSideTx run from AcceptInboundTx as well as
// from SubmitPaymentTx, and in AcceptInboundTx the bank executing them is the
// RECEIVING bank for that direction — the creditor's on a push, the debtor's on
// a pull — not the submitting one. Filling PartyDetails from the register
// there would silently overwrite what the submitting bank already stored (and
// already sent, in the message SubmitAndInstruct built in the same unit of
// work) with the receiving bank's own record of the same account, which need
// not agree with what the payer typed. Checked on both directions, because the
// two arms fail differently if this regresses: a pull always posts its debtor
// leg in AcceptInboundTx, so its overwrite would be unconditional, while a
// push's is gated behind AcceptInboundTx's dirty check and only shows up when
// something else (an address back-fill) also changed.
//
// # The push subtest's precondition is asserted, not assumed
//
// That dirty check is `if p.Debtor == before.Debtor && p.Creditor ==
// before.Creditor && p.DebtorLegTx == before.DebtorLegTx { return nil }`
// (AcceptInboundTx). On a push nothing here changes DebtorLegTx, so the ONLY
// thing that can make this half write at all is the creditor address being
// back-filled — and that happens only because networkWithTwoBanks quotes no
// Creditor.Identifier. Give that fixture an identifier and this subtest keeps
// passing while pinning nothing: AcceptInboundTx would return before the write,
// so an overwrite of CreditorDetails would never be persisted for the assertion
// to catch.
//
// So the write is asserted. It is not a second subject — it is this subject's
// precondition, and a precondition that lives in another function's fixture is
// exactly the kind that rots silently.
func TestAcceptInboundDoesNotRewriteEitherPartysDetails(t *testing.T) {
	t.Run("push", func(t *testing.T) {
		ctx := context.Background()
		n, req := networkWithTwoBanks(t)
		creditorBank, err := n.GetParticipant(ctx, req.Creditor.Participant)
		assertNoError(t, err)
		// Deliberately NOT "Bob" — the real name on the creditor's own
		// register (setupTwoBanks). If AcceptInboundTx's creditorSideTx (the
		// creditor's bank, here the RECEIVING bank) overwrote CreditorDetails
		// from its register, this would come back as "Bob".
		req.CreditorDetails = PartyDetails{Agent: creditorBank.BIC, Name: "Whoever The Payer Typed"}

		p, err := n.SubmitPayment(ctx, req)
		assertNoError(t, err)
		if p.Creditor.Identifier != (deposit.Identifier{}) {
			t.Fatalf("the submitted payment already carries a creditor address (%+v), so AcceptInbound has nothing to change and this subtest can no longer fail; see the doc above", p.Creditor.Identifier)
		}
		assertNoError(t, n.AcceptInbound(ctx, p.ID))

		after, err := n.GetPayment(ctx, p.ID)
		assertNoError(t, err)
		if after.Creditor == p.Creditor {
			t.Fatalf("AcceptInbound wrote nothing back: the creditor is still %+v, so nothing this subtest asserts about CreditorDetails had to survive a PutPayment", after.Creditor)
		}
		if after.CreditorDetails != p.CreditorDetails {
			t.Errorf("creditor details after AcceptInbound = %+v, want unchanged from submission %+v",
				after.CreditorDetails, p.CreditorDetails)
		}
		if after.DebtorDetails != p.DebtorDetails {
			t.Errorf("debtor details after AcceptInbound = %+v, want unchanged from submission %+v",
				after.DebtorDetails, p.DebtorDetails)
		}
	})

	t.Run("pull", func(t *testing.T) {
		ctx := context.Background()
		n, req, _ := networkWithACollection(t, 100000)
		debtorBank, err := n.GetParticipant(ctx, req.Debtor.Participant)
		assertNoError(t, err)
		// Deliberately NOT "Alice" — the real name on the debtor's own
		// register (networkWithACollection). If AcceptInboundTx's
		// debtorSideTx (the debtor's bank, here the RECEIVING bank) overwrote
		// DebtorDetails from its register, this would come back as "Alice".
		req.DebtorDetails = PartyDetails{Agent: debtorBank.BIC, Name: "Whoever The Payee Typed"}

		p, err := n.SubmitPayment(ctx, req)
		assertNoError(t, err)
		assertNoError(t, n.AcceptInbound(ctx, p.ID))

		after, err := n.GetPayment(ctx, p.ID)
		assertNoError(t, err)
		if after.DebtorDetails != p.DebtorDetails {
			t.Errorf("debtor details after AcceptInbound = %+v, want unchanged from submission %+v",
				after.DebtorDetails, p.DebtorDetails)
		}
		if after.CreditorDetails != p.CreditorDetails {
			t.Errorf("creditor details after AcceptInbound = %+v, want unchanged from submission %+v",
				after.CreditorDetails, p.CreditorDetails)
		}
	})
}

// failingUpdateTx is a Tx whose PARTY lookups fail with an error of the caller's
// choosing. Everything else is the real transaction, promoted by embedding.
//
// It is the Update-side counterpart of message_test.go's failingTx, and it has
// to be a second type rather than a bigger first one: failingStore decorates
// View alone, and checkPartyTx — the function these tests are about — runs
// inside an Update, on the receiving bank's money path. failingTx's own doc
// records that its GetDepositAccount override was deleted for exactly this
// reason, that nothing reached it through a View. Nothing has changed about
// that; what is added here is the other seam.
//
// The two failures are separate fields rather than one, because checkPartyTx
// makes two reads and the first one failing hides the second. A single error
// would leave the deposit-account arm untested while looking as though it were
// covered.
type failingUpdateTx struct {
	Tx
	participantErr error
	accountErr     error
}

func (t failingUpdateTx) GetParticipant(ctx context.Context, id ParticipantID) (Participant, error) {
	if t.participantErr != nil {
		return Participant{}, t.participantErr
	}
	return t.Tx.GetParticipant(ctx, id)
}

func (t failingUpdateTx) GetDepositAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) (deposit.Account, error) {
	if t.accountErr != nil {
		return deposit.Account{}, t.accountErr
	}
	return t.Tx.GetDepositAccount(ctx, book, id)
}

// failingUpdateStore wraps a real Store and hands every UPDATE a failingUpdateTx.
// See message_test.go's failingStore for why a synthetic error at the seam is
// the only way to provoke a store failure on demand that works on both stores.
type failingUpdateStore struct {
	Store
	participantErr error
	accountErr     error
}

func (s failingUpdateStore) Update(ctx context.Context, fn func(context.Context, Tx) error) error {
	return s.Store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return fn(ctx, failingUpdateTx{Tx: tx, participantErr: s.participantErr, accountErr: s.accountErr})
	})
}

// TestAcceptInboundDoesNotBlameTheSenderForAStoreFailure is checkPartyTx's half
// of the property addressedPartyTx has pinned all along, and it is the half that
// was missing while the hazard sat in the code.
//
// checkPartyTx used to collapse every error from either of its two reads into a
// domain sentinel — `if err != nil { return ErrAccountNotInParticipant }`, and
// the same shape one line up for the participant. That is on the MONEY path:
// AcceptInboundTx runs it through creditorSideTx/debtorSideTx, mesh/bank.go's
// answer hands whatever comes back to ReasonFor, and AC01 "incorrect account
// number" goes out in a pacs.002. So a dropped connection at the RECEIVING bank
// told the SENDING bank its customer's IBAN was wrong — and on a push the
// payer's debit is then reversed, so a fault that a retry would have cleared
// became a permanent rejection carrying a false reason.
//
// Both reads and both directions, which is four subtests for two lines of fix,
// and the count is the point rather than thoroughness for its own sake: the
// participant arm and the account arm are separate collapses that were both
// there, and creditorSideTx and debtorSideTx are separate call sites that reach
// the same function on opposite sides of the money.
//
// What is asserted is not merely "an error came back". The sentinel that must
// NOT come back is named, because an error that is technically non-nil and still
// maps to AC01 is the whole bug; and ReasonFor is called directly, because the
// wire code is the thing that reaches the counterparty.
func TestAcceptInboundDoesNotBlameTheSenderForAStoreFailure(t *testing.T) {
	dropped := errors.New("connection reset by peer")

	for _, tc := range []struct {
		name           string
		participantErr error
		accountErr     error
	}{
		{"the participant read fails", dropped, nil},
		{"the deposit-account read fails", nil, dropped},
	} {
		for _, dir := range []struct {
			name  string
			setup func(t *testing.T) (*Network, InitiatePaymentRequest)
		}{
			{"push", func(t *testing.T) (*Network, InitiatePaymentRequest) {
				n, req := networkWithTwoBanks(t)
				return n, req
			}},
			{"pull", func(t *testing.T) (*Network, InitiatePaymentRequest) {
				n, req, _ := networkWithACollection(t, 100000)
				req.DebtorDetails = PartyDetails{Name: "Alice"}
				return n, req
			}},
		} {
			t.Run(tc.name+", "+dir.name, func(t *testing.T) {
				ctx := context.Background()
				n, req := dir.setup(t)
				p, err := n.SubmitPayment(ctx, req)
				assertNoError(t, err)

				// The same store, decorated only from here on: the payment had
				// to be submitted successfully for there to be one to answer.
				broken := NewNetwork(failingUpdateStore{
					Store:          n.Store(),
					participantErr: tc.participantErr,
					accountErr:     tc.accountErr,
				}, func() time.Time { return fixedTime })

				err = broken.AcceptInbound(ctx, p.ID)
				if !errors.Is(err, dropped) {
					t.Fatalf("AcceptInbound over a broken store = %v, want the store's own error", err)
				}
				if errors.Is(err, ErrAccountNotInParticipant) {
					t.Error("a store failure surfaced as ErrAccountNotInParticipant, which reaches the sender as AC01 \"incorrect account number\" — the number is not the problem")
				}
				if errors.Is(err, ErrParticipantNotFound) {
					t.Error("a store failure surfaced as ErrParticipantNotFound, which reaches the sender as RC01 \"bank identifier incorrect\" — the bank is not the problem")
				}
				if got := ReasonFor(err); got != iso20022.StatusReasonNotSpecifiedAgentGenerated {
					t.Errorf("ReasonFor(a store failure) = %q, want MS03: the only true thing to say is that this agent could not carry the instruction out", got)
				}
			})
		}
	}
}

// A payment that is no longer Initiated must not be revived by an answer that
// was in flight when it stopped being one.
//
// This is why AcceptInboundTx takes an ID and loads: given the payment by
// value it wrote the caller's copy back, and this sequence — which Task 9 makes
// routine and which RejectAtCSMTx already permits, since it accepts an
// Initiated payment — returned a rejected payment to Initiated with its
// DebtorLegTx still naming the transaction the rejection had reversed. The
// clearing house would then have accepted and settled it, paying the creditor
// out of a suspense position that no longer existed.
func TestAcceptInboundRefusesAPaymentThatIsNoLongerInitiated(t *testing.T) {
	ctx := context.Background()
	n, p := networkWithASubmittedPayment(t)

	rejected, err := reject(ctx, n, p.ID, iso20022.StatusReasonDuplication, "cancelled by the payer")
	assertNoError(t, err)
	assertEqual(t, "status after rejection", rejected.Status, Rejected)

	// p is the copy submission returned: Initiated, with a debtor leg that has
	// since been reversed. Exactly what a bank's handler would still be
	// holding.
	if err := n.AcceptInbound(ctx, p.ID); !errors.Is(err, ErrInvalidStateTransition) {
		t.Fatalf("AcceptInbound on a rejected payment = %v, want ErrInvalidStateTransition", err)
	}

	stored, err := n.GetPayment(ctx, p.ID)
	assertNoError(t, err)
	assertEqual(t, "status after the late answer", stored.Status, Rejected)
}

func TestAcceptInboundRefusesAClosedCreditorAccount(t *testing.T) {
	n, p := networkWithASubmittedPayment(t)
	closeCreditorAccount(t, n, p)

	err := n.AcceptInbound(context.Background(), p.ID)
	if !errors.Is(err, deposit.ErrAccountClosed) {
		t.Fatalf("AcceptInbound = %v, want the closed-account error", err)
	}
}

// The creditor's bank holds the mandate, so it validates it — synchronously,
// at submission. The debtor's bank validates funds, asynchronously, on
// receipt. That is who holds what in SEPA, and it is the seam that survives
// the eventual store split.
//
// The funds half here is also the pull mirror of
// TestSubmitDoesNotCheckTheCreditorAccount, as far as one can be written. The
// account half of that mirror is not constructible: pointing the request's
// debtor at an account that does not exist fails the mandate's SameParty
// comparison with ErrMandateMismatch — a comparison of two stored refs, not a
// look in the debtor's register — before the absence of any debtor-book read
// could be observed. The property holds by construction: creditorSideTx never
// calls checkPartyTx for the debtor.
func TestMandateIsCheckedAtSubmissionAndFundsOnReceipt(t *testing.T) {
	n, req := networkWithARevokedMandate(t)
	if _, err := n.SubmitPayment(context.Background(), req); !errors.Is(err, ErrMandateRevoked) {
		t.Fatalf("SubmitPayment with a revoked mandate = %v, want ErrMandateRevoked", err)
	}

	n2, req2 := networkWithAnUnfundedDebtor(t)
	p, err := n2.SubmitPayment(context.Background(), req2)
	if err != nil {
		t.Fatalf("SubmitPayment refused for lack of funds it cannot see: %v", err)
	}
	if err := n2.AcceptInbound(context.Background(), p.ID); !errors.Is(err, deposit.ErrInsufficientAvailable) {
		t.Fatalf("AcceptInbound = %v, want insufficient funds", err)
	}
}

// Rejection is two units of work in two actors: the CSM transitions the
// payment and drops it from its cycle, the debtor's bank reverses its own leg.
//
// This is the FIRST operation in this repository that can half-happen, and the
// test says so rather than letting it be discovered: after RejectAtCSMTx alone,
// the payment is Rejected and the customer's money is still in suspense.
func TestRejectionIsTwoHalvesAndTheFirstHalfLeavesMoneyInSuspense(t *testing.T) {
	n, p := networkWithASubmittedPayment(t)
	before := suspenseBalance(t, n, p.Debtor.Participant)

	rejected, err := n.RejectAtCSM(context.Background(), p.ID, "AC01", "no such account")
	if err != nil {
		t.Fatalf("RejectAtCSM: %v", err)
	}
	if rejected.Status != Rejected || rejected.RejectCode != "AC01" {
		t.Fatalf("got %v/%q, want Rejected/AC01", rejected.Status, rejected.RejectCode)
	}
	if got := suspenseBalance(t, n, p.Debtor.Participant); got != before {
		t.Error("the CSM's half moved money; only the debtor's bank may touch its own book")
	}

	if err := n.ReverseDebtorLeg(context.Background(), rejected, "no such account"); err != nil {
		t.Fatalf("ReverseDebtorLeg: %v", err)
	}
	if got := suspenseBalance(t, n, p.Debtor.Participant); got != before-p.Amount {
		t.Errorf("suspense = %d after the reversal, want %d", got, before-p.Amount)
	}
}

// Dropping the payment from its cycle is the clearing house's own act, and it
// is the half of a rejection with consequences beyond the payment row: a
// rejected payment left in an open cycle would be closed and settled with it,
// paying a creditor for an instruction the network refused.
func TestRejectAtCSMDropsThePaymentFromItsCycle(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)

	p, err := initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 5000,
		Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
	})
	assertNoError(t, err)
	before, err := sys.GetCycle(ctx, cyc.ID)
	assertNoError(t, err)
	assertEqual(t, "payments in the cycle after acceptance", len(before.PaymentIDs), 1)

	_, err = sys.RejectAtCSM(ctx, p.ID, iso20022.StatusReasonDuplication, "duplicate instruction")
	assertNoError(t, err)

	after, err := sys.GetCycle(ctx, cyc.ID)
	assertNoError(t, err)
	if slices.Contains(after.PaymentIDs, p.ID) {
		t.Errorf("the rejected payment is still in cycle %s; closing it would settle a refused instruction", cyc.ID)
	}
}

// The composition sites that stand in for the mesh run the two halves in ONE
// unit of work, and that is what keeps the half-happened outcome off the
// synchronous routes: a reversal that fails takes the transition down with it,
// so no caller ever reads a Rejected payment whose money is still in suspense.
//
// This pins the reject helper above, and it pins nothing else — the other two
// sites are other packages' code and carry their own tests,
// api.TestRejectWholePaymentIsOneUnitOfWork and seed.TestSeedRejectIsOneUnitOfWork.
//
// The mesh does not get this, and is not meant to: there the two halves are two
// actors, and a failed reversal is a dead letter.
func TestAFailedReversalRollsBackTheWholeRejection(t *testing.T) {
	ctx := context.Background()
	n, p := networkWithASubmittedPayment(t)

	// The payer's bank has already given the money back, so the composite's
	// second half will refuse. Anything that fails after the CSM's half would
	// do; this is the one a retried pacs.002 actually produces.
	assertNoError(t, n.ReverseDebtorLeg(ctx, p, "reversed already"))

	_, err := reject(ctx, n, p.ID, iso20022.StatusReasonDuplication, "cancelled by the payer")
	if !errors.Is(err, ledger.ErrTransactionAlreadyReversed) {
		t.Fatalf("reject = %v, want ErrTransactionAlreadyReversed", err)
	}

	stored, err := n.GetPayment(ctx, p.ID)
	assertNoError(t, err)
	assertEqual(t, "status after the failed rejection", stored.Status, Initiated)
	assertEqual(t, "reject reason after the failed rejection", stored.RejectReason, "")
}

// A collection the clearing house refuses before the payer's bank has answered
// it took nothing from the payer, so the debtor bank's half has nothing to give
// back. The pacs.002 still reaches that bank — it does not know whether it had
// answered — so the half has to be a clean no-op rather than an error.
func TestReverseDebtorLegIsANoOpWhenNoLegWasPosted(t *testing.T) {
	ctx := context.Background()
	n, req := networkWithAMandate(t)
	p, err := n.SubmitPayment(ctx, req)
	assertNoError(t, err)
	assertEqual(t, "debtor leg after submitting a collection", p.DebtorLegTx, "")

	rejected, err := n.RejectAtCSM(ctx, p.ID, iso20022.StatusReasonNoMandate, "no usable mandate")
	assertNoError(t, err)

	bank, err := n.GetParticipant(ctx, p.Debtor.Participant)
	assertNoError(t, err)
	before := customerBalance(t, bank, p.Debtor.Account)

	if err := n.ReverseDebtorLeg(ctx, rejected, "no usable mandate"); err != nil {
		t.Fatalf("ReverseDebtorLeg on a payment with no posted leg = %v, want a no-op", err)
	}
	assertEqual(t, "payer's balance", customerBalance(t, bank, p.Debtor.Account), before)
}

// Reversing the same leg twice would pay the payer twice, so the ledger refuses
// it rather than absorbing it: a redelivered pacs.002 fails loudly and becomes
// a dead letter instead of quietly crediting the payer again.
func TestReverseDebtorLegRefusesToRunTwice(t *testing.T) {
	ctx := context.Background()
	n, p := networkWithASubmittedPayment(t)

	rejected, err := n.RejectAtCSM(ctx, p.ID, iso20022.StatusReasonIncorrectAccountNumber, "no such account")
	assertNoError(t, err)
	assertNoError(t, n.ReverseDebtorLeg(ctx, rejected, "no such account"))

	err = n.ReverseDebtorLeg(ctx, rejected, "no such account")
	if !errors.Is(err, ledger.ErrTransactionAlreadyReversed) {
		t.Fatalf("second ReverseDebtorLeg = %v, want ErrTransactionAlreadyReversed", err)
	}
	assertEqual(t, "suspense after the refused second reversal",
		suspenseBalance(t, n, p.Debtor.Participant), 0)
}

// The reason text is stored by one half and only described by the other, and
// neither may put an unprintable byte where it will be persisted.
func TestRejectionRefusesAnUnsafeReasonInBothHalves(t *testing.T) {
	ctx := context.Background()
	n, p := networkWithASubmittedPayment(t)
	const unsafe = "cancelled\x00by the payer"

	// The CSM's half validates it itself: RejectReason is written onto the
	// payment and copied into the audit event.
	if _, err := n.RejectAtCSM(ctx, p.ID, iso20022.StatusReasonDuplication, unsafe); !errors.Is(err, ledger.ErrInvalidText) {
		t.Fatalf("RejectAtCSM with an unprintable reason = %v, want ErrInvalidText", err)
	}
	stored, err := n.GetPayment(ctx, p.ID)
	assertNoError(t, err)
	assertEqual(t, "status after the refused rejection", stored.Status, Initiated)

	// The debtor bank's half does not repeat the check, because the ledger
	// makes it: the reason travels in the reversal's description.
	rejected, err := n.RejectAtCSM(ctx, p.ID, iso20022.StatusReasonDuplication, "cancelled by the payer")
	assertNoError(t, err)
	if err := n.ReverseDebtorLeg(ctx, rejected, unsafe); !errors.Is(err, ledger.ErrInvalidText) {
		t.Fatalf("ReverseDebtorLeg with an unprintable reason = %v, want ErrInvalidText", err)
	}
	assertEqual(t, "suspense after the refused reversal",
		suspenseBalance(t, n, p.Debtor.Participant), p.Amount)
}

// A queue redelivers, so the debtor's bank sees the same pacs.003 twice while
// the payment is still Initiated. The second delivery must be a no-op.
//
// Before DebtorLegTx was used as the witness, it reached postDebtorLegTx again
// and came back with the ledger's ErrDuplicateIdempotencyKey — no double debit,
// because the key is the payment's own id, but an error with no entry in
// reasonTable, so ReasonFor turns it into MS03 and the bank rejects on the wire
// a collection it has in fact accepted.
func TestAcceptInboundIgnoresARedeliveredCollection(t *testing.T) {
	ctx := context.Background()
	n, req := networkWithAMandate(t)
	p, err := n.SubmitPayment(ctx, req)
	assertNoError(t, err)
	assertNoError(t, n.AcceptInbound(ctx, p.ID))

	answered, err := n.GetPayment(ctx, p.ID)
	assertNoError(t, err)
	bank, err := n.GetParticipant(ctx, p.Debtor.Participant)
	assertNoError(t, err)
	balance := customerBalance(t, bank, p.Debtor.Account)

	if err := n.AcceptInbound(ctx, p.ID); err != nil {
		t.Fatalf("redelivered collection = %v, want a no-op — the mesh would answer MS03 for a collection this bank accepted", err)
	}
	assertEqual(t, "payer's balance after the redelivery", customerBalance(t, bank, p.Debtor.Account), balance)

	again, err := n.GetPayment(ctx, p.ID)
	assertNoError(t, err)
	assertEqual(t, "debtor leg after the redelivery", again.DebtorLegTx, answered.DebtorLegTx)
	assertEqual(t, "suspense after the redelivery",
		suspenseBalance(t, n, p.Debtor.Participant), p.Amount)
}
