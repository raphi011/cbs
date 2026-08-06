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
// Mesh.Admit is the other door and is exercised in admission_test.go.
func rosterNetwork(t *testing.T, bics map[string]iso20022.BIC) *payment.Network {
	t.Helper()
	clock := func() time.Time { return testTime }
	net := payment.NewNetwork(testenv.New(t, clock).Payment(), clock)
	for name, bic := range bics {
		if _, err := storetest.Admit(context.Background(), net, name, bic, euroOnly); err != nil {
			t.Fatalf("admitting %s: %v", name, err)
		}
	}
	return net
}

// The mesh is N+2: one actor per member bank, plus the clearing house and the
// central bank. The banks come from the CLEARING HOUSE's roster, which is the
// store, which is why this is the one test in the package that needs one. It
// used to run a second time against store/pg when TEST_DATABASE_URL was set;
// there is one store now and one run.
//
// The roster and not the bank rows, and TestStartGivesAFoundedBankNoActor is the
// other half of that: a bank that exists and has not been admitted gets nothing
// here.
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
		if _, ok := m.actors[bic]; !ok {
			t.Errorf("no actor for %s", bic)
		}
	}
	if got := len(m.actors); got != len(want) {
		t.Errorf("mesh has %d actors, want %d", got, len(want))
	}

	// Present in the map is not the same as reading its inbox. Send to one of
	// the banks and drain: its goroutine has to be the thing that produces the
	// dead letter, because nothing else runs a handler.
	//
	// A REJECTION, and of a payment that does not exist. Since Task 10 a bank's
	// handler is the real one, and an acceptance is a message a payer's bank has
	// nothing to do about — it would be handled successfully and leave no trace
	// at all. A rejection naming a payment this network has never issued is work
	// the handler cannot complete, which is what makes the actor's own goroutine
	// the visible source of the failure.
	if err := m.send(testConfig.ClearingHouseBIC, "AURODEFFXXX",
		testRejection(testConfig.ClearingHouseBIC, "AURODEFFXXX", "x")); err != nil {
		t.Fatalf("send: %v", err)
	}
	err = m.Drain(drainCtx(t))
	if err == nil {
		t.Fatal("Drain was clean; the bank's actor never ran its handler")
	}
	if !strings.Contains(err.Error(), "Aurora Bank") {
		t.Errorf("dead letter %q does not come from the bank's own actor", err)
	}
}

// A bank that has been founded and never admitted gets no actor at startup.
//
// The roster is what says who is a member, and this bank is in no roster: the
// settlement agent holds no account for it and the clearing house routes nothing
// to it. So it cannot pay and cannot be paid, which is the truth about it rather
// than a limitation — it has a licence, a book, a product and customers, and no
// scheme has admitted it.
//
// It is a behaviour change and it is measured rather than asserted: while
// founding and joining were one call there was no such bank to have, so joining
// the roster and listing the banks were the same list.
func TestStartGivesAFoundedBankNoActor(t *testing.T) {
	clock := func() time.Time { return testTime }
	net := payment.NewNetwork(testenv.New(t, clock).Payment(), clock)
	ctx := context.Background()
	if _, err := storetest.Admit(ctx, net, "Aurora Bank", "AURODEFFXXX", euroOnly); err != nil {
		t.Fatalf("admitting Aurora: %v", err)
	}
	if _, err := net.FoundBank(ctx, "Nordhaven Bank", "NORDSESSXXX", euroOnly); err != nil {
		t.Fatalf("FoundBank Nordhaven: %v", err)
	}

	m, err := New(net, testConfig, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	if _, ok := m.actors["AURODEFFXXX"]; !ok {
		t.Error("the admitted bank has no actor")
	}
	if _, ok := m.actors["NORDSESSXXX"]; ok {
		t.Error("a founded, unadmitted bank was given an actor; the roster is what says who is a member")
	}
	// And its address is free, which is what makes the state recoverable: an
	// actor answering to it would make the admission that finishes it
	// unroutable for the life of the process.
	if _, err := m.Admit(ctx, "Nordhaven Bank", "NORDSESSXXX", euroOnly); err != nil {
		t.Errorf("re-driving the founded bank's admission: %v", err)
	}
}

// Two banks sharing a BIC is a routing table that cannot say which one a
// message is for. The store permits it — banks.bic has no unique
// constraint, because a BIC identifies an institution and not a row — so the
// mesh is where it has to be refused, and it refuses at startup rather than at
// the first payment that goes to the wrong bank.
//
// Both are in the roster under ONE entry, which is what the clearing house's own
// act does with a second acknowledgement quoting the same admission reference:
// it extends rather than refusing (payment.AdmitMemberTx). So the roster says
// one address is a member and two bank rows claim it, and the mesh is the first
// thing that has to choose.
func TestStartRefusesTwoParticipantsWithOneBIC(t *testing.T) {
	net := rosterNetwork(t, map[string]iso20022.BIC{
		"Aurora Bank":   "AURODEFFXXX",
		"Aurora Bank 2": "AURODEFFXXX",
	})
	m, err := New(net, testConfig, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("Start accepted two participants under one BIC")
	}
	// And it left the mesh as it found it. Registering the first bank and then
	// failing on the second would leave a half-populated mesh that a retry
	// could not fix, because the retry would collide with what the failed
	// attempt had itself created.
	if got := len(m.actors); got != 2 {
		t.Errorf("after the refusal the mesh holds %d actors, want the 2 institutions only", got)
	}
}
