package main

import (
	"context"
	"testing"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/payment/recon"
)

// TestSeededInFlightPaymentsAreAppliedWhenTheirCycleSettles advances one real
// business day over the sample dataset and asks the only question that matters
// about a settlement: did the bank that has to move a customer's money get told?
//
// # What it is for
//
// A cut-off settles a whole cycle at the settlement agent and then releases each
// receiving bank's file, in that order (ADR-0002). Release works from the share
// the clearing house BUILT when it took the uploaded file in, so anything that
// reaches a cycle without a file behind it settles with nothing to release —
// reserves move, and the receiving bank is never handed the instruction. The
// state is silent and permanent: the payee is never credited, the amount stops
// in that bank's clearing suspense, and its copy reads Initiated for ever.
//
// The seed is what could reach it. It plays every institution itself and uploads
// nothing, so a payment it leaves in an OPEN cycle is settled by the first
// advance and delivered to nobody. seed's own
// TestTheBuildLeavesNoPaymentInAnOpenCycle is the constraint that keeps it out;
// this is the same claim proved through the transport, over a real day, which is
// the only place the release path actually runs.
//
// # Why it advances the day rather than driving the phases
//
// Because the fault is in the ORDER — settle, then release — and a test that
// drove the phases itself would be asserting against its own arrangement of
// them. AdvanceDay is what an operator's button runs.
//
// The problems are logged and not asserted on: ClearingHouse.unhanded reports
// exactly this fault, so a regression prints its own diagnosis beside the
// failure rather than leaving a reader to find it.
func TestSeededInFlightPaymentsAreAppliedWhenTheirCycleSettles(t *testing.T) {
	ctx := context.Background()
	srv := newAPIHarness(t)

	report, err := srv.dep.AdvanceDay(ctx)
	if err != nil {
		t.Fatalf("AdvanceDay: %v", err)
	}
	for _, p := range report.Problems {
		t.Logf("problem: %s could not process %s: %s", p.Institution, p.OrderID, p.Detail)
	}

	var network []api.PaymentDTO
	getJSON(t, csmSurface(srv), "/payments", &network)

	var checked int
	for _, p := range network {
		if p.Status != payment.Settled.String() {
			continue
		}
		checked++
		// The bank that owes its customer the money: the payee's on a push, and
		// on a pull the payer's, whose account the collection debits.
		receiver := p.CreditorAgent
		if p.Scheme == string(payment.SchemeSEPADD) {
			receiver = p.DebtorAgent
		}
		routes, err := srv.BankRoutes(ctx, payment.ParticipantID(receiver))
		if err != nil {
			t.Fatalf("binding %s's surface: %v", receiver, err)
		}
		var theirs api.PaymentDTO
		getJSON(t, routes, "/payments/"+p.ID, &theirs)

		leg := theirs.CreditorLegTx
		if p.Scheme == string(payment.SchemeSEPADD) {
			leg = theirs.DebtorLegTx
		}
		if theirs.Status != payment.Settled.String() || leg == "" {
			t.Errorf("%s (%s, e2e %q): the clearing house settled it, %s holds it as %q with leg %q",
				p.ID, p.Scheme, p.EndToEndID, receiver, theirs.Status, leg)
		}
	}

	// A day that settled nothing would walk none of the above and pass, which is
	// the one way this test could stop meaning anything without failing.
	if checked == 0 {
		t.Fatal("no payment in the network is Settled after a business day; this test asserted nothing")
	}

	// And the same state read out of the books rather than off the statuses.
	// Check fails on any break of its own — the arm for this fault is recon's
	// partiesHoldTheirCopy — so what is left to log is the positions, which are
	// the seed's Cleared payments waiting in cycles nothing settles.
	books := recon.Check(t, srv.nets)
	for _, u := range books.Unreconciled {
		t.Logf("unreconciled: %s (%s) suspense %d, %d in flight, unbooked %v",
			u.Bank, u.Asset, u.Suspense, len(u.InFlight), u.Unbooked)
	}
}
