package main

import (
	"context"
	"testing"

	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// TestTheSeedLeavesNoPaymentHalfProcessed pins what a reader sees on the first
// page load: the scenario `make dev` presents contains no payment caught
// between a debit and the answer to it.
func TestTheSeedLeavesNoPaymentHalfProcessed(t *testing.T) {
	ctx := context.Background()

	// A deployment with hosts that answer, which the seed now needs: its
	// in-flight payments are uploaded for real. See seed.Deployment.
	srv := newAPIHarness(t)
	nets := srv.nets
	stores := nets.Stores()
	// The clearing house's view, for the network-scoped reads this test makes.
	net := nets.ClearingHouse()

	for _, b := range srv.dep.banksInOrder() {
		got, err := b.Pending(ctx)
		if err != nil {
			t.Fatalf("%s reading its hub: %v", b.BIC(), err)
		}
		if len(got) != 0 {
			t.Errorf("%s's hub holds %d instructions the build never uploaded: %v; their payers are debited and no file carries them",
				b.BIC(), len(got), paymentIDs(got))
		}
	}
	for _, host := range []struct {
		at iso20022.BIC
		s  *ebics.Server
	}{
		{srv.dep.cfg.ClearingHouseBIC, srv.dep.ClearingHouse().Host()},
		{srv.dep.cfg.CentralBankBIC, srv.dep.CentralBank().Host()},
	} {
		if got := pendingAt(t, host.s); got != 0 {
			t.Errorf("%d files are waiting unworked at %s; the build uploaded them and left before that institution read them",
				got, host.at)
		}
	}

	payments, err := net.ListPayments(ctx)
	if err != nil {
		t.Fatalf("listing payments: %v", err)
	}
	if len(payments) == 0 {
		t.Fatal("the seed produced no payments; this test would pass on an empty store")
	}

	// mid holds, per asset, the money the accepted-but-unsettled payments have
	// taken out of a customer's account and not yet delivered. That is exactly
	// what a clearing suspense account is for.
	mid := map[ledger.AssetCode]ledger.Amount{}
	for _, p := range payments {
		if p.Status == payment.Initiated {
			t.Errorf("payment %s (%s) is still Initiated: debited, and no institution has answered",
				p.ID, p.EndToEndID)
		}
		if p.Status != payment.Accepted && p.Status != payment.Cleared {
			continue
		}
		sc, ok := net.Scheme(p.Scheme)
		if !ok {
			t.Fatalf("payment %s names scheme %q, which the network does not have", p.ID, p.Scheme)
		}
		if sc.Direction() == payment.Pull {
			continue
		}
		mid[sc.Asset()] += p.Amount
	}

	// Every bank in the system is every bank with a DATABASE, asked of the store
	// set rather than of an institution: the clearing house holds a roster and no
	// bank rows, and the roster omits a bank that was founded and never admitted.
	bics, err := stores.Banks(ctx)
	if err != nil {
		t.Fatalf("listing the banks: %v", err)
	}
	parts := make([]*payment.Bank, 0, len(bics))
	for _, bic := range bics {
		pid := payment.ParticipantID(bic)
		bankNet, err := nets.Bank(ctx, pid)
		if err != nil {
			t.Fatalf("opening %s's store: %v", bic, err)
		}
		part, err := bankNet.GetBank(ctx, pid)
		if err != nil {
			t.Fatalf("reading %s's own row: %v", bic, err)
		}
		parts = append(parts, part)
	}
	// Every asset either side knows about, so that money parked in an asset no
	// payment is mid-clearing in is caught by the same comparison rather than
	// by a second branch nothing can reach.
	held := map[ledger.AssetCode]ledger.Amount{}
	assets := map[ledger.AssetCode]bool{}
	for asset := range mid {
		assets[asset] = true
	}
	for _, part := range parts {
		for asset, accts := range part.Assets {
			bal, err := part.Ledger.BookBalance(ctx, accts.Suspense.Total())
			if err != nil {
				t.Fatalf("reading %s's %s clearing suspense: %v", part.Name, asset, err)
			}
			held[asset] += bal
			assets[asset] = true
		}
	}

	for asset := range assets {
		if held[asset] != mid[asset] {
			t.Errorf("%s clearing suspense holds %d across all banks; the payments mid-clearing account for %d",
				asset, held[asset], mid[asset])
		}
	}
}
