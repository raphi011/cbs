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
// about a settlement: did the bank that has to move a customer's money get
// told?
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
	books := recon.Check(t, srv.nets)
	for _, u := range books.Unreconciled {
		t.Logf("unreconciled: %s (%s) suspense %d, %d in flight, unbooked %v",
			u.Bank, u.Asset, u.Suspense, len(u.InFlight), u.Unbooked)
	}
}
