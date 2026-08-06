package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/mesh"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/seed"
	"github.com/raphi011/cbs/store/testenv"
)

// TestTheSeedLeavesNoPaymentHalfProcessed pins what a reader sees on the first
// page load: the scenario `make dev` presents contains no payment caught
// between a debit and the answer to it.
//
// It matters because a half-processed payment reads as a bug in the payment
// rather than as a moment in its life. In the mesh a credit transfer really
// does pass through that moment — the payer's bank debits, sends its pacs.008,
// and waits — and Initiated is the status that names it: money gone from the
// payer, no institution having yet said accept or reject.
//
// The property holds by CONSTRUCTION, and this test drains nothing of its own.
// Every PAYMENT in the scenario is composed inside ONE unit of work
// (seed.builder.initiate, and seed.builder.reject for the rejection's two), with
// the seed playing each institution in turn rather than sending anything: a unit
// of work has no observable middle, so there is no instant at which a seeded
// payment is half-processed — not merely no instant after some drain.
//
// The seed IS a mesh participant for one thing, and only one: admitting its
// banks, which is a conversation and cannot be composed. It drains each
// admission before the next bank applies (seed.builder.admit), so nothing from
// that is in flight either by the time Populate returns — and main binds the
// listeners only afterwards, so no request can arrive during it.
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
	data := seed.New()
	st := testenv.New(t, data.Now)
	net := payment.NewNetwork(st.Payment(), data.Now)
	msh, err := mesh.New(net, meshConfig, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("building the mesh: %v", err)
	}
	if err := msh.Start(ctx); err != nil {
		t.Fatalf("starting the mesh: %v", err)
	}
	// Drain then Stop, as main does at shutdown, and neither error is discarded:
	// the seed's admissions are the only conversations this test has, and one
	// that failed would leave the scenario it asserts on half built.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), meshShutdown)
		defer cancel()
		if err := msh.Drain(ctx); err != nil {
			t.Errorf("draining at shutdown: %v", err)
		}
		if err := msh.Stop(ctx); err != nil {
			t.Errorf("stopping: %v", err)
		}
	})
	if err := data.Populate(ctx, net, msh); err != nil {
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

	parts, err := net.ListBanks(ctx)
	if err != nil {
		t.Fatalf("listing participants: %v", err)
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
			bal, err := part.Ledger.BookBalance(ctx, accts.Suspense)
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
