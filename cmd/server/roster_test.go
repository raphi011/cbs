package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/raphi011/cbs/calendar"
	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/sqlite"
	"github.com/raphi011/cbs/store/storetest"
	"github.com/raphi011/cbs/store/testenv"
)

// euroOnly is the asset set a SEPA bank joins with.
var euroOnly = []ledger.AssetCode{"EUR"}

// rosterNetwork is a payment network holding exactly the banks it is given,
// each already admitted, and the clock they were written on.
func rosterNetwork(t *testing.T, bics map[string]iso20022.BIC) (*payment.Networks, *sqlite.Set, *calendar.Clock) {
	t.Helper()
	clock := calendar.NewClock(testTime)
	set := testenv.NewSet(t, clock.Now)
	nets := payment.NewNetworks(set, clock.Now)
	for name, bic := range bics {
		if _, err := storetest.Admit(context.Background(), nets, name, bic, euroOnly); err != nil {
			t.Fatalf("admitting %s: %v", name, err)
		}
	}
	return nets, set, clock
}

// unreachableConfig is testConfig with two URLs nothing listens on.
func unreachableConfig() Config {
	cfg := testConfig
	cfg.CentralBankURL = "http://127.0.0.1:1/ebics"
	cfg.ClearingHouseURL = "http://127.0.0.1:1/ebics"
	return cfg
}

func newRosterDeployment(t *testing.T, nets *payment.Networks, hosts Hosts, clock *calendar.Clock) *Deployment {
	t.Helper()
	dep, err := NewDeployment(context.Background(), nets, hosts, clock, unreachableConfig(), nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("NewDeployment: %v", err)
	}
	return dep
}

// A deployment is N+2, and it is TWO facts rather than one: every bank with a
// database gets a view of its own, and every bank the ROSTER names gets a
// download queue at each host.
func TestNewDeploymentEnrolsEveryMemberOfTheRoster(t *testing.T) {
	nets, set, clock := rosterNetwork(t, map[string]iso20022.BIC{
		"Aurora Bank": "AURODEFFXXX",
		"Banca Verde": "VERDITMMXXX",
	})
	dep := newRosterDeployment(t, nets, set, clock)

	for _, bic := range []iso20022.BIC{"AURODEFFXXX", "VERDITMMXXX"} {
		if _, err := dep.member(bic); err != nil {
			t.Errorf("no view of %s: %v", bic, err)
		}
		if !dep.ClearingHouse().Host().Enrolled(ebics.SubscriberID(bic)) {
			t.Errorf("%s has no download queue at the clearing house, so nothing can be addressed to it", bic)
		}
		if !dep.CentralBank().Host().Enrolled(ebics.SubscriberID(bic)) {
			t.Errorf("%s has no download queue at the settlement agent, so it can never be sent a statement", bic)
		}
	}

	// And the clearing house is a subscriber at the settlement agent, exactly as
	// a member bank is: it uploads settlement instructions and collects the
	// answers, and without a queue it would never hear one.
	if !dep.CentralBank().Host().Enrolled(ebics.SubscriberID(testConfig.ClearingHouseBIC)) {
		t.Error("the clearing house has no download queue at the settlement agent")
	}

	// Two banks and one clearing house, and nobody else. A queue for an address
	// the roster does not name would be a route to a bank no scheme has admitted.
	if got := len(dep.subscribers()); got != 2 {
		t.Errorf("the clearing house holds %d subscribers, want 2", got)
	}
}

// A bank whose provisioning stopped halfway gets a view of its own and no
// queue.
func TestABankTheRosterDoesNotNameGetsAViewAndNoQueue(t *testing.T) {
	nets, set, clock := rosterNetwork(t, map[string]iso20022.BIC{"Aurora Bank": "AURODEFFXXX"})
	ctx := context.Background()

	// Founded and no further, through its OWN network, over its own database,
	// because founding is a member bank's own act —
	// payment.ErrNotThisInstitutionsAct is what a clearing house asking for it
	// gets.
	nordhavenNet, err := nets.Bank(ctx, "NORDSESSXXX")
	if err != nil {
		t.Fatalf("Nordhaven's own network: %v", err)
	}
	nordhaven, err := nordhavenNet.FoundBank(ctx, "Nordhaven Bank", "NORDSESSXXX", storetest.FixtureCountry, euroOnly)
	if err != nil {
		t.Fatalf("FoundBank Nordhaven: %v", err)
	}

	dep := newRosterDeployment(t, nets, set, clock)

	if _, err := dep.member("NORDSESSXXX"); err != nil {
		t.Errorf("the half-provisioned bank has no view of its own: %v", err)
	}
	if dep.ClearingHouse().Host().Enrolled(ebics.SubscriberID("NORDSESSXXX")) {
		t.Error("a half-provisioned bank was given a download queue; the roster is what says who is a member")
	}
	// So a file addressed to it has nowhere to go, and the clearing house is the
	// institution that says so — off the queues it holds, which are the routing
	// table.
	if _, err := dep.ClearingHouse().Host().Enqueue(context.Background(), ebics.SubscriberID("NORDSESSXXX"), ebics.CCT, []byte("x")); err == nil {
		t.Error("a file was addressed to a bank no scheme has admitted")
	} else if ebics.CodeOf(err) != ebics.InvalidUserOrUserState {
		t.Errorf("the refusal is %v, want the transport's one membership answer", err)
	}

	// And its address is free, which is what makes the state recoverable: the
	// retry that finishes the job enrols it.
	if err := dep.AddBank(ctx, nordhaven); err != nil {
		t.Errorf("the half-provisioned bank cannot be given its place: %v", err)
	}
	if !dep.ClearingHouse().Host().Enrolled(ebics.SubscriberID("NORDSESSXXX")) {
		t.Error("AddBank did not enrol the bank it admitted")
	}
}
