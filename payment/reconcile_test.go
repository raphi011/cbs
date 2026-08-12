package payment_test

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	. "github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/sqlite"
)

// ---------------------------------------------------------------------------
// Calibrating one bank's own reconciliation
// ---------------------------------------------------------------------------

// settledPair carries one payment between two banks to finality, books every
// advice and pays every creditor, and hands back the two banks and the cycle.
// It is the state each test below damages exactly once.
func settledPair(t *testing.T, sys *testSystem) (a, b *Bank, payer deposit.AccountID, cycle CycleID) {
	t.Helper()
	ctx := context.Background()
	a, b, alice, bob := setupTwoBanks(t, sys)

	cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 30000,
		Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	assertNoError(t, err)

	_, err = sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)
	_, statements, err := sys.settleCycle(ctx, cyc.ID)
	assertNoError(t, err)
	bookTheAdvices(t, sys, statements)
	payTheCreditors(t, sys, cyc.ID)
	return a, b, alice, cyc.ID
}

// refillTheVault puts cash back in the drawer, because setupTwoBanks lodges
// every note it took in and LodgeReservesTx will not lodge what is not there —
// Vault Cash is an Asset and the ledger refuses one below zero.
func refillTheVault(t *testing.T, sys *testSystem, bank *Bank, acct deposit.AccountID, amount ledger.Amount) {
	t.Helper()
	ctx := context.Background()
	takeCashIn(t, ctx, sys, bank, depositAccount(t, ctx, bank, acct), amount)
}

// reconcile runs one bank's own reconciliation in the test asset.
func reconcile(t *testing.T, sys *testSystem, bic iso20022.BIC) Reconciliation {
	t.Helper()
	rec, err := sys.bank(bic).Reconcile(context.Background(), testAsset)
	assertNoError(t, err)
	return rec
}

// assertBreaks fails unless the run found exactly the expected number, and
// prints what it did find when it did not — a count on its own tells whoever
// broke this nothing about which check fired.
func assertBreaks(t *testing.T, rec Reconciliation, want int) {
	t.Helper()
	if len(rec.Breaks) != want {
		t.Fatalf("breaks: got %d, want %d: %v", len(rec.Breaks), want, rec.Breaks)
	}
}

// assertBreakMentions fails unless some break's sentence contains the substring,
// which is how each test names the check it means without pinning the wording.
func assertBreakMentions(t *testing.T, rec Reconciliation, substr string) {
	t.Helper()
	for _, b := range rec.Breaks {
		if strings.Contains(b.What, substr) {
			return
		}
	}
	t.Fatalf("no break mentioning %q; found %v", substr, rec.Breaks)
}

// damageAdvice rewrites one of a bank's own advice rows behind its back.
func damageAdvice(t *testing.T, sys *testSystem, bank *Bank, reference string, edit func(*SettlementAdvice)) {
	t.Helper()
	ctx := context.Background()
	store, err := sys.stores.Bank(ctx, bank.BIC)
	assertNoError(t, err)
	assertNoError(t, store.Update(ctx, func(ctx context.Context, tx BankTx) error {
		advice, err := tx.GetSettlementAdvice(ctx, bank.BookID, reference, testAsset)
		if err != nil {
			return err
		}
		edit(&advice)
		return tx.PutSettlementAdvice(ctx, bank.BookID, advice)
	}))
}

// TestABanksOwnBooksReconcile is the control, and without it every test below
// proves nothing: an instrument that reported a break on a healthy bank would
// "catch" all of them.
func TestABanksOwnBooksReconcile(t *testing.T) {
	sys := testNetwork(t)
	a, b, _, _ := settledPair(t, sys)

	for _, bank := range []*Bank{a, b} {
		rec := reconcile(t, sys, bank.BIC)
		assertEqual(t, string(bank.BIC)+" reconciled", rec.Reconciled(), true)
		assertEqual(t, string(bank.BIC)+" positions", len(rec.Positions), 0)
	}

	// And the two figures the whole instrument turns on agree, with nothing
	// lodged in between them.
	rec := reconcile(t, sys, b.BIC)
	assertEqual(t, "own reserve", rec.Reserve, 30000)
	assertEqual(t, "what the last statement said it would be", rec.Advised, 30000)
	assertEqual(t, "lodged since", rec.LodgedSince, 0)
}

// TestABankCatchesItsMirrorLegPostedAgainstTheWrongAmount is the defect class
// the closing balance was stored for: a bank that booked a movement other than
// the one it was advised of.
func TestABankCatchesItsMirrorLegPostedAgainstTheWrongAmount(t *testing.T) {
	sys := testNetwork(t)
	_, b, _, cycle := settledPair(t, sys)

	// The advice now claims a movement the leg behind it did not make. Equivalent
	// to a mirror leg posted at the wrong figure, and reachable from the row
	// rather than from the ledger because a posted entry cannot be edited.
	damageAdvice(t, sys, b, string(cycle), func(a *SettlementAdvice) { a.Movement = 25000 })

	rec := reconcile(t, sys, b.BIC)
	assertEqual(t, "reconciled", rec.Reconciled(), false)
	assertBreakMentions(t, rec, "advised a movement of 25000")
}

// TestABankCatchesAReserveMovedWithNoStatementBehindIt is the damage
// cmd/server/recon_test.go's diverged-mirror fixture injects, caught from
// inside ONE database.
func TestABankCatchesAReserveMovedWithNoStatementBehindIt(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	_, b, _, _ := settledPair(t, sys)
	accts := accountsOf(t, b)

	// Debit Reserve, Credit Unclaimed: this bank's claim on the central bank rises
	// by a thousand it was never advised of.
	_, err := b.Ledger.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "a movement nobody advised",
		Entries: []ledger.Entry{
			{AccountID: accts.Reserve, Amount: 1000, Direction: ledger.Debit},
			{AccountID: accts.Unclaimed, Subsidiary: "dep_nobody", Amount: 1000, Direction: ledger.Credit},
		},
	})
	assertNoError(t, err)

	rec := reconcile(t, sys, b.BIC)
	assertEqual(t, "reconciled", rec.Reconciled(), false)
	assertBreakMentions(t, rec, "no statement and no lodgement behind it")
	assertEqual(t, "the account the finding names", rec.Breaks[0].Account, accts.Reserve)
}

// TestABankCatchesAnAdviceClaimingAPostingThatIsNotThere is the other direction
// of the same walk, and it is the one that would catch a row written without
// its leg.
func TestABankCatchesAnAdviceClaimingAPostingThatIsNotThere(t *testing.T) {
	sys := testNetwork(t)
	_, b, _, cycle := settledPair(t, sys)

	damageAdvice(t, sys, b, string(cycle), func(a *SettlementAdvice) {
		a.MirrorTx = ledger.TransactionID("txn_that_is_not_there")
	})

	rec := reconcile(t, sys, b.BIC)
	assertEqual(t, "reconciled", rec.Reconciled(), false)
	assertBreakMentions(t, rec, "which is not on this account")
}

// TestAMissedStatementIsCaughtByTheNextOne is what the stored closing balance
// really buys, and it is a narrower claim than three layers of this repository
// currently make.
func TestAMissedStatementIsCaughtByTheNextOne(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	var refs []string
	for range 2 {
		cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
		assertNoError(t, err)
		_, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 10000,
			Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
			DebtorDetails:   PartyDetails{Agent: a.BIC}})
		assertNoError(t, err)
		_, err = sys.CloseCycle(ctx, cyc.ID)
		assertNoError(t, err)
		_, statements, err := sys.settleCycle(ctx, cyc.ID)
		assertNoError(t, err)
		refs = append(refs, string(cyc.ID))

		// B is told about the second cut-off and not the first: one camt.053
		// went missing, and the agent's closing balance on the second counts
		// both.
		for _, st := range statements {
			if st.Agent == b.BIC && len(refs) == 1 {
				continue
			}
			_, err := sys.bank(st.Agent).PostSettlementAdvice(ctx, AdvisedMovement{
				Account: st.Account, Asset: st.Asset, Movement: st.Movement,
				ClosingBalance: st.ClosingBalance, Reference: st.Reference, ValueDate: st.ValueDate,
			})
			assertNoError(t, err)
		}
		payTheCreditors(t, sys, cyc.ID)
	}

	rec := reconcile(t, sys, b.BIC)
	assertEqual(t, "reconciled", rec.Reconciled(), false)
	assertBreakMentions(t, rec, "10000 of movement is unaccounted for")

	// The payer's bank was told about both and is clean, which is what makes the
	// finding above about B rather than about the fixture.
	assertEqual(t, "the payer's bank", reconcile(t, sys, a.BIC).Reconciled(), true)
}

// TestTheLastStatementNeverArrivingIsUndetectableFromInside is a NEGATIVE
// result, asserted rather than written down, because a claim about what an
// instrument cannot see is worth as much as one about what it can and only a
// test keeps it true.
func TestTheLastStatementNeverArrivingIsUndetectableFromInside(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 30000,
		Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	assertNoError(t, err)
	_, err = sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)
	_, _, err = sys.settleCycle(ctx, cyc.ID)
	assertNoError(t, err)
	// Nobody books anything. The reserves have moved at the settlement agent and
	// neither member knows.

	rec := reconcile(t, sys, a.BIC)
	assertEqual(t, "the payer's bank finds nothing wrong", rec.Reconciled(), true)
	assertEqual(t, "and holds no statement at all", rec.Reference, "")

	// What it CAN see is that the money has not gone anywhere, and how long it
	// has been there. That is the position, and it is not a defect.
	assertEqual(t, "positions", len(rec.Positions), 1)
	assertEqual(t, "suspense", rec.Positions[0].Balance, 30000)
	assertEqual(t, "the account it names", rec.Positions[0].Account, accountsOf(t, a).Suspense)
	assertEqual(t, "and what it is made of", len(rec.Positions[0].Lots), 1)
}

// TestALodgementInFlightIsNotABreak is the one legitimate reason the two
// figures differ, and the instrument has to know it or it reports a bank doing
// the right thing as broken.
func TestALodgementInFlightIsNotABreak(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, _, alice, _ := settledPair(t, sys)
	refillTheVault(t, sys, a, alice, 5000)

	before := reconcile(t, sys, a.BIC)
	_, _, err := sys.bank(a.BIC).LodgeReserves(ctx, testAsset, 5000, MessageContext{
		From: a.BIC, To: testCentralBankBIC, MsgID: "msg-lodge-1", Now: fixedTime})
	assertNoError(t, err)

	rec := reconcile(t, sys, a.BIC)
	assertEqual(t, "reconciled", rec.Reconciled(), true)
	assertEqual(t, "the reserve moved", rec.Reserve, before.Reserve+5000)
	assertEqual(t, "the statement did not", rec.Advised, before.Advised)
	assertEqual(t, "and the difference is named", rec.LodgedSince, 5000)
	assertEqual(t, "which is the whole of it", rec.Reserve-rec.Advised, rec.LodgedSince)
}

// TestReconcileIsNotAnActTheOtherTwoInstitutionsCanPerform states the boundary
// as a refusal rather than as an empty answer.
func TestReconcileIsNotAnActTheOtherTwoInstitutionsCanPerform(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	settledPair(t, sys)

	asCSM := BankHandleOverClearingHouse(sys.bank(testBIC), sys.ClearingHouseNetwork)
	if _, err := asCSM.Reconcile(ctx, testAsset); err == nil {
		t.Fatal("the clearing house reconciled books it does not have")
	}
	asCB := BankHandleOverCentralBank(sys.bank(testBIC), sys.cb())
	if _, err := asCB.Reconcile(ctx, testAsset); err == nil {
		t.Fatal("the settlement agent reconciled a reserve it does not hold")
	}
}

// TestAReconciliationRunLeavesItsOwnAuditTrail is what replaces a findings
// table.
func TestAReconciliationRunLeavesItsOwnAuditTrail(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	_, b, _, cycle := settledPair(t, sys)
	damageAdvice(t, sys, b, string(cycle), func(a *SettlementAdvice) { a.Movement = 25000 })

	rec := reconcile(t, sys, b.BIC)
	assertBreaks(t, rec, 1)

	events, err := sys.bank(b.BIC).ListAudit(ctx, ledger.AuditFilter{
		BookID: b.BookID, Scope: ledger.ScopePayment})
	assertNoError(t, err)

	var runs, breaks int
	for _, e := range events {
		switch e.Type {
		case ledger.EventReconciliationRun:
			runs++
		case ledger.EventReconciliationBreak:
			breaks++
		}
	}
	assertEqual(t, "runs recorded", runs, 1)
	assertEqual(t, "breaks recorded", breaks, 1)
}

// TestAConcurrentSettlementDoesNotMakeAReconciliationRunLie OPENS A FILE, and
// that is the point of it.
func TestAConcurrentSettlementDoesNotMakeAReconciliationRunLie(t *testing.T) {
	ctx := context.Background()
	set, err := sqlite.OpenSet(ctx, filepath.Join(t.TempDir(), "recon"), func() time.Time { return fixedTime })
	assertNoError(t, err)
	t.Cleanup(func() { assertNoError(t, set.Close()) })

	nets := NewNetworks(set, func() time.Time { return fixedTime })
	sys := &testSystem{ClearingHouseNetwork: nets.ClearingHouse(), nets: nets, stores: set}
	a, _, alice, _ := settledPair(t, sys)
	refillTheVault(t, sys, a, alice, 5000)

	// Both handles are opened BEFORE the goroutines start.
	lodger, reconciler := sys.bank(a.BIC), sys.bank(a.BIC)

	const rounds = 8
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range rounds {
			if _, _, err := lodger.LodgeReserves(ctx, testAsset, 100, MessageContext{
				From: a.BIC, To: testCentralBankBIC, MsgID: "msg-lodge-" + strconv.Itoa(i), Now: fixedTime}); err != nil {
				t.Errorf("lodging: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for range rounds {
			rec, err := reconciler.Reconcile(ctx, testAsset)
			if err != nil {
				t.Errorf("reconciling: %v", err)
				return
			}
			if !rec.Reconciled() {
				t.Errorf("a run against a healthy bank reported %v", rec.Breaks)
				return
			}
			if rec.Reserve-rec.Advised != rec.LodgedSince {
				t.Errorf("one run read two snapshots: reserve %d, advised %d, lodged %d",
					rec.Reserve, rec.Advised, rec.LodgedSince)
				return
			}
		}
	}()
	wg.Wait()
}
