package main

import (
	"context"
	"strings"
	"testing"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/seed"
	"github.com/raphi011/cbs/store/mem"
)

// TestRedactNeverEchoesAPassword is the one thing worth pinning here: a DSN
// reaches this process from the environment, and the log line that records
// which database was opened is the easiest place to leak the credential in it.
func TestRedactNeverEchoesAPassword(t *testing.T) {
	const secret = "hunter2"

	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{
			name: "userinfo password",
			dsn:  "postgres://cbs:" + secret + "@localhost:5432/cbs?sslmode=disable",
			want: "postgres://cbs:xxxxx@localhost:5432/cbs?sslmode=disable",
		},
		{
			name: "password query parameter",
			dsn:  "postgres://cbs@localhost:5432/cbs?password=" + secret + "&sslmode=disable",
			want: "postgres://cbs@localhost:5432/cbs?password=xxxxx&sslmode=disable",
		},
		{
			// libpq's keyword form is not a URL. Rather than guess at its
			// shape, redact refuses to echo it at all.
			name: "keyword form is not echoed",
			dsn:  "host=localhost user=cbs password=" + secret + " dbname=cbs",
			want: "<redacted>",
		},
		{
			name: "no password at all is left readable",
			dsn:  "postgres://cbs@localhost:5432/cbs?sslmode=disable",
			want: "postgres://cbs@localhost:5432/cbs?sslmode=disable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redact(tc.dsn)
			if strings.Contains(got, secret) {
				t.Fatalf("redact leaked the password: %q", got)
			}
			if got != tc.want {
				t.Errorf("redact(%q)\n got %q\nwant %q", tc.dsn, got, tc.want)
			}
		})
	}
}

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
// The property holds by CONSTRUCTION, and this test does not drain anything.
// The seed is not a mesh participant: it drives payment.Network directly and
// synchronously, playing every institution itself, and it composes each
// payment's halves inside ONE unit of work (seed.builder.initiate, and
// seed.builder.reject for the rejection's two). A unit of work has no
// observable middle, so there is no instant at which a seeded payment is
// half-processed — not merely no instant after some drain. main then starts the
// mesh and binds the listeners only after Populate has returned, so nothing the
// seed did is in flight when the first request can arrive.
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
	st := mem.New(data.Now)
	net := payment.NewNetwork(st.Payment(), data.Now)
	if err := data.Populate(ctx, net); err != nil {
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
