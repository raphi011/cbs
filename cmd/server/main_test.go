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
//
// It matters because a half-processed payment reads as a bug in the payment
// rather than as a moment in its life. A credit transfer really does pass
// through that moment — the payer's bank debits, uploads its pacs.008, and
// waits — and Initiated is the status that names it: money gone from the payer,
// no institution having yet said accept or reject.
//
// Two things produce the property and they are different in kind. What the seed
// COMPOSES it composes one institution's half at a time, in its own unit of
// work, playing each in turn — so there is no instant at which one of those is
// half-processed. What it UPLOADS goes through a real cut-off and is worked all
// the way through before Populate returns, which is builder.build's closing act:
// nothing is left in a bank's hub and nothing waits in a download queue.
//
// The hubs and the hosts are therefore checked too, and that arm only exists now
// that the seed uploads at all. A payment sitting in a hub is one whose payer is
// debited and whose file has not been built; a file sitting unworked at a host
// is one an institution was told had arrived and has not read.
//
// Two things are checked, because a status is only half the story. Money is the
// other half: a rejection that transitioned the payment but never reversed the
// payer's leg would leave a Rejected payment reading correctly while the
// customer's money sat in their bank's clearing suspense. So the suspense
// accounts are made to account for themselves — across every bank, they hold
// exactly the money of the payments that are legitimately mid-clearing.
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
		if got := b.queued(); len(got) != 0 {
			t.Errorf("%s's hub holds %d instructions the build never uploaded: %v; their payers are debited and no file carries them",
				b.bic, len(got), got)
		}
	}
	for _, host := range []struct {
		at iso20022.BIC
		s  *ebics.Server
	}{
		{srv.dep.cfg.ClearingHouseBIC, srv.dep.ClearingHouse().host},
		{srv.dep.cfg.CentralBankBIC, srv.dep.CentralBank().host},
	} {
		if got := host.s.Pending(); len(got) != 0 {
			t.Errorf("%d files are waiting unworked at %s; the build uploaded them and left before that institution read them",
				len(got), host.at)
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
	//
	// A PUSH only, and the exclusion is settle-before-release rather than an
	// exemption. Before finality the submitting bank is the only bank that has
	// heard of the payment, and on a pull that bank is the payee's and posts
	// NOTHING at submission: the account being collected from is the payer's
	// bank's to look at, and that bank learns of the collection when its output
	// file is released. So an in-flight direct debit has taken nobody's money and
	// is in no suspense — counting it would demand a balance no bank should have.
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
	// See payment.Stores.Banks, and cmd/server's own listener plan, which asks
	// the same question for the same reason.
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
