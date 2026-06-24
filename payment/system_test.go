package payment

import (
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
)

// fixedTime is the instant returned by the test clock, matching the ledger
// package's own test clock.
var fixedTime = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

func testNetwork(t *testing.T) *Network {
	t.Helper()
	return NewNetworkWithClock(func() time.Time { return fixedTime })
}

// setupTwoBanks creates two participant banks, opens a customer account at
// each (Alice at Bank A, Bob at Bank B), and funds Alice with 100000. The
// returned account IDs are deposit account IDs; their backing GL account IDs
// are resolved when checking book balances.
func setupTwoBanks(t *testing.T, sys *Network) (a, b *Participant, alice, bob deposit.AccountID) {
	t.Helper()

	a, err := sys.AddParticipant("Bank A")
	assertNoError(t, err)
	b, err = sys.AddParticipant("Bank B")
	assertNoError(t, err)

	aliceAcct, err := a.OpenCustomerAccount("Alice")
	assertNoError(t, err)
	bobAcct, err := b.OpenCustomerAccount("Bob")
	assertNoError(t, err)

	assertNoError(t, sys.Deposit(a.ID, aliceAcct.ID, 100000, "Alice opening deposit"))
	return a, b, aliceAcct.ID, bobAcct.ID
}

// runCycle opens, closes, and settles a cycle for the given scheme, returning
// the settled settlement.
func runCycle(t *testing.T, sys *Network, scheme SchemeID, submit func()) Settlement {
	t.Helper()
	cyc, err := sys.OpenCycle(scheme)
	assertNoError(t, err)
	submit()
	_, err = sys.CloseCycle(cyc.ID)
	assertNoError(t, err)
	st, err := sys.SettleCycle(cyc.ID)
	assertNoError(t, err)
	return st
}

// bookBalance returns the GL book balance of an arbitrary ledger account.
func bookBalance(t *testing.T, l *ledger.Book, acct ledger.AccountID) ledger.Amount {
	t.Helper()
	bal, err := l.BookBalance(acct)
	assertNoError(t, err)
	return bal
}

// customerBalance returns the book balance of a customer deposit account at a
// participant, resolved through the participant's deposit layer.
func customerBalance(t *testing.T, p *Participant, acct deposit.AccountID) ledger.Amount {
	t.Helper()
	bal, err := p.Deposit.GetBalance(acct)
	assertNoError(t, err)
	return bal.Book
}

// assertReserveMirror checks that a bank's own reserve asset equals the
// central bank's view of that bank's reserve.
func assertReserveMirror(t *testing.T, sys *Network, p *Participant) {
	t.Helper()
	own := bookBalance(t, p.Ledger, p.ReserveAccount)
	cb, err := sys.ReserveBalance(p.ID)
	assertNoError(t, err)
	assertEqual(t, "reserve mirror for "+p.Name, own, cb)
}

// ---------------------------------------------------------------------------
// SEPA Credit Transfer
// ---------------------------------------------------------------------------

func TestSCT_HappyPath(t *testing.T) {
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	var pay Payment
	runCycle(t, sys, SchemeSEPACT, func() {
		var err error
		pay, err = sys.InitiatePayment(InitiatePaymentRequest{
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
	assertEqual(t, "bank A suspense", bookBalance(t, a.Ledger, a.SuspenseAccount), 0)
	assertEqual(t, "bank B suspense", bookBalance(t, b.Ledger, b.SuspenseAccount), 0)
	assertEqual(t, "bank A reserve", bookBalance(t, a.Ledger, a.ReserveAccount), 70000)
	assertEqual(t, "bank B reserve", bookBalance(t, b.Ledger, b.ReserveAccount), 30000)
	assertReserveMirror(t, sys, a)
	assertReserveMirror(t, sys, b)

	got, err := sys.GetPayment(pay.ID)
	assertNoError(t, err)
	assertEqual(t, "status settled", got.Status, Settled)
}

func TestSCT_Netting(t *testing.T) {
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	// Fund Bob so Bank B can also be a payer.
	assertNoError(t, sys.Deposit(b.ID, bob, 50000, "Bob opening deposit"))

	st := runCycle(t, sys, SchemeSEPACT, func() {
		_, err := sys.InitiatePayment(InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 30000,
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		})
		assertNoError(t, err)
		_, err = sys.InitiatePayment(InitiatePaymentRequest{
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
	assertEqual(t, "bank A reserve", bookBalance(t, a.Ledger, a.ReserveAccount), 80000)
	assertEqual(t, "bank B reserve", bookBalance(t, b.Ledger, b.ReserveAccount), 70000)
	assertReserveMirror(t, sys, a)
	assertReserveMirror(t, sys, b)
}

func TestSCT_InsufficientFunds(t *testing.T) {
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	_, err := sys.OpenCycle(SchemeSEPACT)
	assertNoError(t, err)
	_, err = sys.InitiatePayment(InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 150000, // more than Alice has
		Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
	})
	assertError(t, err, deposit.ErrInsufficientAvailable)
}

// ---------------------------------------------------------------------------
// SEPA Direct Debit
// ---------------------------------------------------------------------------

func TestSDD_HappyPath(t *testing.T) {
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)

	debtor := PartyRef{Participant: a.ID, Account: alice}
	creditor := PartyRef{Participant: b.ID, Account: biller}
	m, err := sys.CreateMandate(debtor, creditor, 0)
	assertNoError(t, err)

	var pay Payment
	runCycle(t, sys, SchemeSEPADD, func() {
		pay, err = sys.InitiatePayment(InitiatePaymentRequest{
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
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)
	debtor := PartyRef{Participant: a.ID, Account: alice}
	creditor := PartyRef{Participant: b.ID, Account: biller}

	limited, err := sys.CreateMandate(debtor, creditor, 5000)
	assertNoError(t, err)
	revoked, err := sys.CreateMandate(debtor, creditor, 0)
	assertNoError(t, err)
	assertNoError(t, sys.RevokeMandate(revoked.ID))

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
			_, err := sys.OpenCycle(SchemeSEPADD)
			assertNoError(t, err)
			_, err = sys.InitiatePayment(InitiatePaymentRequest{
				Scheme: SchemeSEPADD, Amount: tc.amount, MandateID: tc.mandateID,
				Debtor: debtor, Creditor: creditor,
			})
			assertError(t, err, tc.want)
			// Tidy up the open cycle for the next sub-test.
			cyc := sys.openCycle[SchemeSEPADD]
			_, _ = sys.CloseCycle(cyc)
		})
	}
}

func TestSDD_Return(t *testing.T) {
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)
	debtor := PartyRef{Participant: a.ID, Account: alice}
	creditor := PartyRef{Participant: b.ID, Account: biller}
	m, err := sys.CreateMandate(debtor, creditor, 0)
	assertNoError(t, err)

	var pay Payment
	runCycle(t, sys, SchemeSEPADD, func() {
		pay, err = sys.InitiatePayment(InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
			Debtor: debtor, Creditor: creditor,
		})
		assertNoError(t, err)
	})
	assertEqual(t, "alice after collection", customerBalance(t, a, alice), 75000)

	returned, err := sys.ReturnPayment(pay.ID, "insufficient funds at debtor")
	assertNoError(t, err)
	assertEqual(t, "status", returned.Status, Returned)

	// Money fully unwound across all three ledgers.
	assertEqual(t, "alice refunded", customerBalance(t, a, alice), 100000)
	assertEqual(t, "biller clawed back", customerBalance(t, b, biller), 0)
	assertReserveMirror(t, sys, a)
	assertReserveMirror(t, sys, b)
}

// ---------------------------------------------------------------------------
// State machine, idempotency, and validation guards
// ---------------------------------------------------------------------------

func TestStateMachineGuards(t *testing.T) {
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	mkPayment := func() (Payment, CycleID) {
		cyc, err := sys.OpenCycle(SchemeSEPACT)
		assertNoError(t, err)
		p, err := sys.InitiatePayment(InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 10000,
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		})
		assertNoError(t, err)
		return p, cyc.ID
	}

	t.Run("settle before close", func(t *testing.T) {
		_, cyc := mkPayment()
		_, err := sys.SettleCycle(cyc)
		assertError(t, err, ErrCycleNotClosed)
		_, _ = sys.CloseCycle(cyc)
		_, _ = sys.SettleCycle(cyc)
	})

	t.Run("double settle", func(t *testing.T) {
		_, cyc := mkPayment()
		_, err := sys.CloseCycle(cyc)
		assertNoError(t, err)
		_, err = sys.SettleCycle(cyc)
		assertNoError(t, err)
		_, err = sys.SettleCycle(cyc)
		assertError(t, err, ErrCycleNotClosed)
	})

	t.Run("return before settle", func(t *testing.T) {
		p, cyc := mkPayment()
		_, err := sys.ReturnPayment(p.ID, "too early")
		assertError(t, err, ErrInvalidStateTransition)
		_, _ = sys.CloseCycle(cyc)
		_, _ = sys.SettleCycle(cyc)
	})

	t.Run("reject after settle", func(t *testing.T) {
		p, cyc := mkPayment()
		_, err := sys.CloseCycle(cyc)
		assertNoError(t, err)
		_, err = sys.SettleCycle(cyc)
		assertNoError(t, err)
		_, err = sys.RejectPayment(p.ID, "too late")
		assertError(t, err, ErrInvalidStateTransition)
	})
}

func TestRejectPayment_ReversesDebtorLeg(t *testing.T) {
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	_, err := sys.OpenCycle(SchemeSEPACT)
	assertNoError(t, err)
	p, err := sys.InitiatePayment(InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 40000,
		Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
	})
	assertNoError(t, err)
	assertEqual(t, "alice debited", customerBalance(t, a, alice), 60000)

	rejected, err := sys.RejectPayment(p.ID, "operator cancelled")
	assertNoError(t, err)
	assertEqual(t, "status", rejected.Status, Rejected)
	assertEqual(t, "alice restored", customerBalance(t, a, alice), 100000)
	assertEqual(t, "suspense flat", bookBalance(t, a.Ledger, a.SuspenseAccount), 0)
}

func TestDuplicateEndToEndID(t *testing.T) {
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	_, err := sys.OpenCycle(SchemeSEPACT)
	assertNoError(t, err)
	req := InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 1000, EndToEndID: "e2e-1",
		Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
	}
	_, err = sys.InitiatePayment(req)
	assertNoError(t, err)
	_, err = sys.InitiatePayment(req)
	assertError(t, err, ErrDuplicateEndToEndID)
}

func TestInitiatePayment_Validation(t *testing.T) {
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	_, err := sys.OpenCycle(SchemeSEPACT)
	assertNoError(t, err)

	t.Run("unknown scheme", func(t *testing.T) {
		_, err := sys.InitiatePayment(InitiatePaymentRequest{
			Scheme: "nope", Amount: 1000,
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		})
		assertError(t, err, ErrSchemeNotFound)
	})
	t.Run("non-positive amount", func(t *testing.T) {
		_, err := sys.InitiatePayment(InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 0,
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		})
		assertError(t, err, ErrInvalidPaymentAmount)
	})
	t.Run("account not in participant", func(t *testing.T) {
		_, err := sys.InitiatePayment(InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 1000,
			Debtor: PartyRef{Participant: a.ID, Account: "acct_999"}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		})
		assertError(t, err, ErrAccountNotInParticipant)
	})
	t.Run("no open cycle", func(t *testing.T) {
		_, err := sys.InitiatePayment(InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 1000, // no SDD cycle open
			Debtor: PartyRef{Participant: a.ID, Account: alice}, Creditor: PartyRef{Participant: b.ID, Account: bob},
		})
		assertError(t, err, ErrCycleNotOpen)
	})
}

func TestOpenCycle_AlreadyOpen(t *testing.T) {
	sys := testNetwork(t)
	_, err := sys.OpenCycle(SchemeSEPACT)
	assertNoError(t, err)
	_, err = sys.OpenCycle(SchemeSEPACT)
	assertError(t, err, ErrCycleAlreadyOpen)
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
