package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/raphi011/cbs/calendar"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/seed"
	"github.com/raphi011/cbs/store/testenv"
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
// The property holds by CONSTRUCTION, and this test carries nothing of its own.
// Every PAYMENT in the scenario is composed inside ONE unit of work
// (seed.builder.initiate, and seed.builder.reject for the rejection's two), with
// the seed playing each institution in turn rather than uploading anything: a
// unit of work has no observable middle, so there is no instant at which a
// seeded payment is half-processed.
//
// Nor is anything left waiting in a queue. The seed uploads no file at all — its
// three deployment acts are an admission, a directory pull and a read of the
// settlement agent's address, none of which is a file — so Populate returns a
// scenario with nothing in flight and nothing pending.
//
// Two things are checked, because a status is only half the story. Money is the
// other half: a rejection that transitioned the payment but never reversed the
// payer's leg would leave a Rejected payment reading correctly while the
// customer's money sat in their bank's clearing suspense. So the suspense
// accounts are made to account for themselves — across every bank, they hold
// exactly the money of the payments that are legitimately mid-clearing.
func TestTheSeedLeavesNoPaymentHalfProcessed(t *testing.T) {
	ctx := context.Background()

	// The store, the network and the seed, exactly as main builds them.
	clock := calendar.NewClock(seed.BaseDate)
	data := seed.New(clock)
	stores := testenv.NewSet(t, clock.Now)
	nets := payment.NewNetworks(stores, clock.Now)
	// The clearing house's view, for the network-scoped reads this test makes.
	net := nets.ClearingHouse()
	cfg := testConfig
	// Two URLs that reach nothing, and nothing dials them. The seed composes both
	// halves of every conversation it builds, so this deployment exists only to
	// give Populate the three acts it asks for — an admission, a directory pull,
	// and the settlement agent's address. A configuration with no URLs at all is
	// refused, which is the point: an institution with nowhere to dial can neither
	// send nor collect.
	cfg.CentralBankURL = "http://127.0.0.1:1/ebics"
	cfg.ClearingHouseURL = "http://127.0.0.1:1/ebics"
	dep, err := NewDeployment(ctx, nets, clock, cfg, data.Populate, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("building the deployment: %v", err)
	}
	if err := data.Populate(ctx, nets, dep); err != nil {
		t.Fatalf("populating the sample dataset: %v", err)
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
