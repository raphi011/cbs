package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/calendar"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/payment/recon"
	"github.com/raphi011/cbs/seed"
	"github.com/raphi011/cbs/store/sqlite"
)

// The one test in this repository that can fail for the reason the held-files
// sub-project exists.
//
// Everything else here runs inside one process, so an institution keeping its
// obligations in a map and an institution keeping them in a database look
// exactly alike. This drops the process between a cut-off and its settlement and
// asks whether the receiving banks were still handed their instructions.

// restartable is an API harness over FILE-backed databases, which can end the
// process holding them and open a second one over the same files.
//
// The httptest hosts are the seam that makes it possible: each closes over the
// harness rather than over a deployment, so a restart re-points both at the new
// institutions without rebinding anything. That is also what a restart really
// is here — the addresses survive, the state behind them does not.
type restartable struct {
	*server
	dir string
	cfg Config
}

// newRestartable boots a deployment over a directory of SQLite files and seeds
// it.
//
// A directory rather than testenv.NewSet, which is ephemeral: an in-memory
// database is named for the life of a handle, so closing the set is the same act
// as deleting it and there would be nothing for the second process to open.
func newRestartable(t *testing.T) *restartable {
	t.Helper()

	r := &restartable{dir: t.TempDir(), server: &server{}}
	csmHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.dep.ClearingHouse().EBICS().ServeHTTP(w, req)
	}))
	cbHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.dep.CentralBank().EBICS().ServeHTTP(w, req)
	}))
	t.Cleanup(csmHost.Close)
	t.Cleanup(cbHost.Close)

	r.cfg = testConfig
	r.cfg.ClearingHouseURL = csmHost.URL
	r.cfg.CentralBankURL = cbHost.URL
	r.boot(t)
	return r
}

// boot opens the databases and builds a deployment over them. The seed runs and
// finds its own work already done on every boot after the first, so what the
// second process sees is what the first one left.
func (r *restartable) boot(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	clock := calendar.NewClock(seed.BaseDate)
	set, err := sqlite.OpenSet(ctx, r.dir, clock.Now)
	if err != nil {
		t.Fatalf("OpenSet: %v", err)
	}
	nets := payment.NewNetworks(set, clock.Now)
	data := seed.New(clock)

	dep, err := NewDeployment(ctx, nets, clock, r.cfg, data.Populate, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewDeployment: %v", err)
	}
	// The deployment is published BEFORE the seed runs, because the seed uploads
	// for real and the two hosts serve whatever this field points at.
	r.server.nets, r.server.clock, r.server.dep = nets, clock, dep
	if err := data.Populate(ctx, nets, dep); err != nil {
		t.Fatalf("populate: %v", err)
	}
}

// restart ends this process and starts another over the same files.
//
// Closing the set is the whole of the ending. Everything an institution holds in
// memory goes with it — both EBICS hosts and their queues, every bank's hub, the
// clock's position — and everything in the N+2 databases stays. What the test
// around it measures is which of the two the clearing house's obligations were
// in.
func (r *restartable) restart(t *testing.T) {
	t.Helper()
	if err := r.nets.Stores().Close(); err != nil {
		t.Fatalf("closing the stores: %v", err)
	}
	r.boot(t)
}

// A cut-off that settles after a restart still hands every receiving bank its
// instructions.
//
// # What it is measuring
//
// Between a cut-off and its settlement the clearing house is holding each
// receiving bank's share of every file it took in. Settling releases them, and
// an institution that had lost them in the meantime would settle finally at the
// agent and hand over nothing: reserves moved, and a payee whose bank was never
// told the payment exists, with the money standing in that bank's clearing
// suspense and no act in this system able to move it.
//
// # Why the operator has to re-instruct
//
// The cut-off uploads a pacs.009 into the settlement agent's download queue, and
// that queue is memory. So the restart takes the instruction with it, and no
// answer is ever coming for a file that no longer exists — which is exactly the
// state POST /cycles/{cid}/settle is the remedy for, and it is the honest way to
// carry on. The shares the release works from are what this test is about, and
// they come out of the database.
//
// That gap is the sub-project's next phase, and it is worth being plain about:
// this proves the clearing house's own obligations survive, not that the
// deployment resumes.
//
// # The guard is expected to stay silent
//
// unhandable refuses a cut-off whose payments have no share behind them. Before
// the shares were rows it would have fired here — which is the whole of what a
// restart used to cost, moved a few seconds earlier — so a refusal from it is
// this test failing rather than this test's subject.
func TestACutOffSettledAfterARestartStillReachesEveryReceivingBank(t *testing.T) {
	ctx := context.Background()
	r := newRestartable(t)

	// The seed leaves each scheme's cut-off open with payments in it, every one
	// uploaded for real and so with a share behind it. Closing one by hand is the
	// operator's cut-off out of turn, and it is what puts a settlement in flight
	// across the restart.
	cycles, err := r.nets.ClearingHouse().ListCycles(ctx)
	if err != nil {
		t.Fatalf("ListCycles: %v", err)
	}
	var closed []payment.CycleID
	for _, c := range cycles {
		if c.Status != payment.CycleOpen || len(c.PaymentIDs) == 0 {
			continue
		}
		if _, err := r.dep.ClearingHouse().CloseCycle(ctx, c.ID); err != nil {
			t.Fatalf("CloseCycle %s: %v", c.ID, err)
		}
		closed = append(closed, c.ID)
	}
	if len(closed) == 0 {
		t.Fatal("the seeded dataset holds no open cut-off with payments in it; this test has nothing in flight to restart across")
	}

	r.restart(t)

	// The clearing house's own answer, before any of it is driven: it is still
	// holding a share for every payment in every cut-off it closed. Asked here
	// rather than only inferred from the delivery below, because a deployment
	// that had lost them and rebuilt something would pass the delivery check for
	// the wrong reason.
	for _, id := range closed {
		files, err := r.dep.ClearingHouse().ops.ListHeldFiles(ctx, id)
		if err != nil {
			t.Fatalf("ListHeldFiles %s: %v", id, err)
		}
		if len(files) == 0 {
			t.Fatalf("%s was closed before the restart and the clearing house now holds no share for it; the process took its obligations with it", id)
		}
	}

	// The re-instruction, and then the day that collects the answer and releases
	// what the settlement made final.
	for _, id := range closed {
		if _, err := r.dep.ClearingHouse().Settle(ctx, id); err != nil {
			t.Fatalf("Settle %s after the restart: %v", id, err)
		}
	}
	report, err := r.dep.AdvanceDay(ctx)
	if err != nil {
		t.Fatalf("AdvanceDay: %v", err)
	}
	for _, p := range report.Problems {
		t.Logf("problem: %s could not process %s: %s", p.Institution, p.OrderID, p.Detail)
	}

	// And the question the whole sub-project is about: did the bank that has to
	// move a customer's money get told? Read the same way
	// TestSeededInFlightPaymentsAreAppliedWhenTheirCycleSettles reads it, over
	// the payments the cut-offs above carried.
	var network []api.PaymentDTO
	getJSON(t, csmSurface(r.server), "/payments", &network)

	inFlight := map[string]bool{}
	for _, id := range closed {
		c, err := r.nets.ClearingHouse().GetCycle(ctx, id)
		if err != nil {
			t.Fatalf("GetCycle %s: %v", id, err)
		}
		for _, pid := range c.PaymentIDs {
			inFlight[string(pid)] = true
		}
	}

	var checked int
	for _, p := range network {
		if !inFlight[p.ID] {
			continue
		}
		if p.Status != payment.Settled.String() {
			t.Errorf("%s (%s) is %q at the clearing house after its cut-off was re-instructed, want Settled", p.ID, p.Scheme, p.Status)
			continue
		}
		checked++
		// The bank that owes its customer the money: the payee's on a push, and
		// on a pull the payer's, whose account the collection debits.
		receiver := p.CreditorAgent
		leg := func(d api.PaymentDTO) string { return d.CreditorLegTx }
		if p.Scheme == string(payment.SchemeSEPADD) {
			receiver = p.DebtorAgent
			leg = func(d api.PaymentDTO) string { return d.DebtorLegTx }
		}
		routes, err := r.BankRoutes(ctx, payment.ParticipantID(receiver))
		if err != nil {
			t.Fatalf("binding %s's surface: %v", receiver, err)
		}
		var theirs api.PaymentDTO
		getJSON(t, routes, "/payments/"+p.ID, &theirs)
		if theirs.Status != payment.Settled.String() || leg(theirs) == "" {
			t.Errorf("%s (%s, e2e %q): the cut-off carrying it settled after a restart, and %s holds it as %q with leg %q",
				p.ID, p.Scheme, p.EndToEndID, receiver, theirs.Status, leg(theirs))
		}
	}
	if checked == 0 {
		t.Fatal("no payment from the restarted cut-offs is Settled; this test asserted nothing")
	}

	// And the same state read out of the books rather than off the statuses.
	// recon is the instrument that opens all N+2 databases at once, which is what
	// no institution in this system may do and what a claim about two of them
	// agreeing needs.
	books := recon.Check(t, r.nets)
	for _, u := range books.Unreconciled {
		t.Logf("unreconciled: %s (%s) suspense %d, %d in flight, unbooked %v",
			u.Bank, u.Asset, u.Suspense, len(u.InFlight), u.Unbooked)
	}
}
