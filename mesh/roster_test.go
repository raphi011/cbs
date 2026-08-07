package mesh

import (
	"context"
	"errors"
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
	// By its ADDRESS and not by its name. joinRoster reads the clearing house's
	// roster and nothing else, and a roster entry names nobody — an acmt.010
	// carries a BIC and no name (payment.RosterEntry) — so the actor registered
	// at startup is labelled with its address. The name is on the bank's own row
	// in the bank's own database, which no other institution reads.
	if !strings.Contains(err.Error(), "AURODEFFXXX") {
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
	nets := payment.NewNetworks(testenv.NewSet(t, clock), clock)
	ctx := context.Background()
	if _, err := storetest.Admit(ctx, nets, "Aurora Bank", "AURODEFFXXX", euroOnly); err != nil {
		t.Fatalf("admitting Aurora: %v", err)
	}
	// Founded through its OWN network, over its own database, because founding
	// is a member bank's own act — payment.ErrNotThisInstitutionsAct is what a
	// clearing house asking for it gets. Asking nets.Bank for the address is
	// what creates that database; see payment.Stores.
	nordhaven, err := nets.Bank(ctx, "NORDSESSXXX")
	if err != nil {
		t.Fatalf("Nordhaven's own network: %v", err)
	}
	if _, err := nordhaven.FoundBank(ctx, "Nordhaven Bank", "NORDSESSXXX", euroOnly); err != nil {
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

// A batch of actors with two on one address registers NOBODY.
//
// Two actors sharing a BIC is a routing table that cannot say which one a
// message is for, and one of the two would end up with a goroutine reading an
// inbox nothing could address. addActors refuses the whole batch rather than
// stopping where it noticed: registering the first and failing on the second
// leaves a half-populated mesh that a retry cannot fix, because the retry
// collides with what the failed attempt itself created.
//
// # It was TestStartRefusesTwoParticipantsWithOneBIC, and Start cannot reach it
//
// The state it built was two BANK ROWS claiming one address, both admitted, with
// the clearing house's roster holding ONE entry for them — which is what a
// second acknowledgement quoting the same admission reference really does
// (payment.AdmitMemberTx extends rather than refusing). Start read the bank rows
// to turn the roster's ids into addresses, so it saw the pair and had to choose.
//
// Neither half of that survives Task 18. A bank's database is NAMED by its
// address (store/sqlite.Set), so founding a second bank on a taken BIC opens the
// first one's database and renames it — there is no pair of rows to have. And
// joinRoster reads the roster and NOTHING else, because a participant's id IS
// its address now, so there is no bank row left in that path to disagree with
// the roster about. The fixture that used to build the clash builds one bank.
//
// The GUARD is untouched and is still the only thing standing between this mesh
// and an unreachable actor, so it is provoked where it lives instead. Its other
// entry point is measured next door: TestAddingABankOnAnotherBanksBICIsRefused-
// AndChangesNothing is the same refusal arriving one bank at a time.
func TestActorRegistrationIsAllOrNoneOnAClashingBatch(t *testing.T) {
	net := rosterNetwork(t, map[string]iso20022.BIC{"Aurora Bank": "AURODEFFXXX"})
	m, err := New(net, testConfig, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	before := len(m.actors)

	nothing := func(context.Context, iso20022.BIC, []byte) error { return nil }
	err = m.addActors(
		actorSpec{bic: "NORDSESSXXX", name: "Nordhaven Bank", handle: nothing},
		actorSpec{bic: "BANKDEFFXXX", name: "Bankhaus Meridian", handle: nothing},
		actorSpec{bic: "NORDSESSXXX", name: "Nordhaven Bank (again)", handle: nothing},
	)
	if !errors.Is(err, ErrAddressTaken) {
		t.Fatalf("addActors = %v, want ErrAddressTaken", err)
	}
	// Not one of the three, and that includes the one with no clash at all: a
	// batch is refused whole.
	if got := len(m.actors); got != before {
		t.Errorf("after the refusal the mesh holds %d actors, want the %d it started with", got, before)
	}
	for _, bic := range []iso20022.BIC{"NORDSESSXXX", "BANKDEFFXXX"} {
		if _, ok := m.actors[bic]; ok {
			t.Errorf("%s was registered out of a batch that was refused", bic)
		}
	}
}
