package mesh

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/storetest"
	"github.com/raphi011/cbs/store/testenv"
)

// euroOnly is the asset set a SEPA bank joins with.
var euroOnly = []ledger.AssetCode{"EUR"}

// rosterNetwork is a payment network holding exactly the banks it is given,
// each already admitted.
//
// It builds them WITHOUT a mesh, through storetest.Admit, which is the point of
// the fixture: what these two tests are about is a mesh reading a roster that
// was there before it started, so the banks have to exist before any actor does.
// Mesh.AddBank is the other door and gives one bank an actor without reading
// anything.
func rosterNetwork(t *testing.T, bics map[string]iso20022.BIC) *payment.Networks {
	t.Helper()
	clock := func() time.Time { return testTime }
	nets := payment.NewNetworks(testenv.NewSet(t, clock), clock)
	for name, bic := range bics {
		if _, err := storetest.Admit(context.Background(), nets, name, bic, euroOnly); err != nil {
			t.Fatalf("admitting %s: %v", name, err)
		}
	}
	return nets
}

// The mesh is N+2: one actor per member bank, plus the clearing house and the
// central bank. The banks come from the CLEARING HOUSE's roster, which is the
// store, which is why this is the one test in the package that needs one.
//
// The roster and not the bank rows, and TestStartGivesAHalfProvisionedBankNoActor
// is the other half of that: a bank that exists and has not been admitted gets
// nothing here.
func TestStartGivesEveryParticipantAnActor(t *testing.T) {
	net := rosterNetwork(t, map[string]iso20022.BIC{
		"Aurora Bank": "AURODEFFXXX",
		"Banca Verde": "VERDITMMXXX",
	})
	m, err := New(net, testConfig, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	want := []iso20022.BIC{"AURODEFFXXX", "VERDITMMXXX", testConfig.ClearingHouseBIC, testConfig.CentralBankBIC}
	for _, bic := range want {
		if !m.bus.Has(bic) {
			t.Errorf("no actor for %s", bic)
		}
	}
	if got := len(m.bus.Addresses()); got != len(want) {
		t.Errorf("mesh has %d actors, want %d", got, len(want))
	}

	// Present in the map is not the same as reading its inbox. Send to one of
	// the banks and drain: its goroutine has to be the thing that produces the
	// dead letter, because nothing else runs a handler.
	//
	// A REJECTION, and of a payment that does not exist. An acceptance is a message
	// a payer's bank has nothing to do about — it would be handled successfully and
	// leave no trace at all. A rejection naming a payment this network has never
	// issued is work the handler cannot complete, which is what makes the actor's
	// own goroutine the visible source of the failure.
	if err := m.send(testConfig.ClearingHouseBIC, "AURODEFFXXX",
		testRejection(testConfig.ClearingHouseBIC, "AURODEFFXXX", "x")); err != nil {
		t.Fatalf("send: %v", err)
	}
	err = m.Drain(drainCtx(t))
	if err == nil {
		t.Fatal("Drain was clean; the bank's actor never ran its handler")
	}
	// By its ADDRESS and not by its name. joinRoster reads the clearing house's
	// roster and nothing else, and a roster entry names nobody — it carries a BIC
	// and no name (payment.RosterEntry) — so the actor registered
	// at startup is labelled with its address. The name is on the bank's own row
	// in the bank's own database, which no other institution reads.
	if !strings.Contains(err.Error(), "AURODEFFXXX") {
		t.Errorf("dead letter %q does not come from the bank's own actor", err)
	}
}

// A bank whose provisioning stopped halfway gets no actor at startup.
//
// The ROSTER is what says who is a member, and this bank is in none: it has a
// licence, a book, a product and customers, and neither the settlement agent nor
// the clearing house has heard of it. So it cannot pay and cannot be paid, which
// is the truth about it rather than a limitation.
//
// It is the state a crash between the acts leaves — provisioning is four units
// of work at three institutions and no transaction spans them — and it is a
// provisioning failure to retry rather than a state the domain names. What this
// pins is that the mesh does not paper over it: an actor answering for a bank
// with no settlement account would let a payment reach a bank that cannot settle.
func TestStartGivesAHalfProvisionedBankNoActor(t *testing.T) {
	clock := func() time.Time { return testTime }
	nets := payment.NewNetworks(testenv.NewSet(t, clock), clock)
	ctx := context.Background()
	if _, err := storetest.Admit(ctx, nets, "Aurora Bank", "AURODEFFXXX", euroOnly); err != nil {
		t.Fatalf("provisioning Aurora: %v", err)
	}
	// Founded and no further, through its OWN network, over its own database,
	// because founding is a member bank's own act — payment.ErrNotThisInstitutionsAct
	// is what a clearing house asking for it gets. Asking nets.Bank for the
	// address is what creates that database; see payment.Stores.
	nordhavenNet, err := nets.Bank(ctx, "NORDSESSXXX")
	if err != nil {
		t.Fatalf("Nordhaven's own network: %v", err)
	}
	nordhaven, err := nordhavenNet.FoundBank(ctx, "Nordhaven Bank", "NORDSESSXXX", storetest.FixtureCountry, euroOnly)
	if err != nil {
		t.Fatalf("FoundBank Nordhaven: %v", err)
	}

	m, err := New(nets, testConfig, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	if !m.bus.Has("AURODEFFXXX") {
		t.Error("the provisioned bank has no actor")
	}
	if m.bus.Has("NORDSESSXXX") {
		t.Error("a half-provisioned bank was given an actor; the roster is what says who is a member")
	}
	// And its address is free, which is what makes the state recoverable: an
	// actor already answering to it would make the retry that finishes the job
	// unroutable for the life of the process.
	if err := m.AddBank(ctx, nordhaven); err != nil {
		t.Errorf("the half-provisioned bank's address is not free: %v", err)
	}
}
