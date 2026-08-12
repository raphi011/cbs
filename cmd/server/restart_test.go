package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/calendar"
	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/payment/recon"
	"github.com/raphi011/cbs/seed"
	"github.com/raphi011/cbs/store/sqlite"
)

// The two tests in this repository that can fail for the reason the held-files
// sub-project exists.
//
// Everything else here runs inside one process, so an institution keeping its
// obligations in a map and an institution keeping them in a database look
// exactly alike. These drop the process at the two moments where something is
// owed and not yet handed over — between a cut-off and its settlement, and
// between a settlement and the banks collecting what it released — and ask
// whether the receiving banks were still told.

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

	// set is the open handle on the N+2 databases, kept because one assertion
	// here is about the TRANSPORT's rows and the domain has no way to reach them.
	// See queuedForTheMembers.
	set *sqlite.Set
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
	r.set = set

	dep, err := NewDeployment(ctx, nets, set, clock, r.cfg, data.Populate, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
// Closing the set is the whole of the ending. What goes with it is what an
// institution keeps outside its database — each host's enrolments, every bank's
// hub, the clock's position — and everything in the N+2 databases stays. The
// enrolments come back because provisioning admits every member again from the
// roster; the hub does not, and it is its own sub-project.
func (r *restartable) restart(t *testing.T) {
	t.Helper()
	if err := r.nets.Stores().Close(); err != nil {
		t.Fatalf("closing the stores: %v", err)
	}
	r.boot(t)
}

// closeEveryCycleWithPaymentsInIt is the operator reaching a cut-off out of
// turn, on every window the seeded dataset left open with something in it.
//
// It is what puts a settlement in flight: the cycles are Closed, a pacs.009 is
// waiting in the settlement agent's download queue, and the clearing house is
// holding a share for every payment in them.
func (r *restartable) closeEveryCycleWithPaymentsInIt(t *testing.T) []payment.CycleID {
	t.Helper()
	ctx := context.Background()

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
	return closed
}

// A cut-off that settles after a restart still hands every receiving bank its
// instructions.
//
// # What it is measuring
//
// Between a cut-off and its settlement the clearing house is holding each
// receiving bank's share of every file it took in, and the settlement agent is
// holding the instruction that will make the batch final. An institution that
// had lost the shares would settle finally and hand over nothing: reserves
// moved, and a payee whose bank was never told the payment exists, with the
// money standing in that bank's clearing suspense and no act in this system able
// to move it. One that had lost the instruction would leave the batch waiting
// for an answer nobody is composing.
//
// # Nothing is re-instructed, and that is the assertion
//
// The cut-off uploaded a pacs.009 into the settlement agent's download queue and
// the restart leaves it there, so the next business day works through it exactly
// as it would have without the interruption. There is no call to Settle here.
// That route still exists and is still the remedy for a cut-off the agent
// REFUSED, which is a different thing from one nobody has looked at yet.
//
// # The guard is expected to stay silent
//
// unhandable refuses a cut-off whose payments have no share behind them, which
// is reachable only from a process that ends between accepting a file's
// transactions and recording the share. Nothing here composes that, so a refusal
// from it is this test failing rather than this test's subject.
func TestACutOffSettledAfterARestartStillReachesEveryReceivingBank(t *testing.T) {
	ctx := context.Background()
	r := newRestartable(t)

	closed := r.closeEveryCycleWithPaymentsInIt(t)

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
	// And the settlement agent is still holding the instruction, which is the
	// half no guard could have covered: nothing refuses a batch whose
	// instruction has simply vanished, because no institution is waiting for
	// anything.
	pending, err := r.dep.CentralBank().host.Pending(ctx)
	if err != nil {
		t.Fatalf("reading the settlement agent's work list: %v", err)
	}
	if len(pending) == 0 {
		t.Fatal("the settlement agent has nothing to work through after the restart; the instruction the cut-off uploaded went with the process")
	}

	// The day that settles it and releases what the settlement made final.
	report, err := r.dep.AdvanceDay(ctx)
	if err != nil {
		t.Fatalf("AdvanceDay: %v", err)
	}
	for _, p := range report.Problems {
		t.Logf("problem: %s could not process %s: %s", p.Institution, p.OrderID, p.Detail)
	}

	r.everyPaymentReachedItsReceivingBank(t, closed)
}

// Files released before a restart are still collected after it.
//
// # What it is measuring
//
// The loss on the FAR side of the release, which the shares alone could never
// close. The settlement has happened, the reserves are final, and each receiving
// bank's instructions are sitting in that bank's download queue at the clearing
// house — collected by nobody yet, because a bank collects when it runs its own
// day. A host that lost its queues there would lose the files with the money
// already moved and nothing would refuse anything: the shares were present, the
// release happened, and the queue is simply gone.
//
// # Why the phases are driven by hand
//
// AdvanceDay runs the release and the members' collection in one call, so there
// is no seam inside it to drop the process at. What is selected below is the
// day's own settlement and release, which leaves the deployment exactly where a
// process ending between two business days would. See beforeClock, and
// workThrough, which selects from the same list for the flow tests.
var releaseWithoutCollectionPhases = only(beforeClock, phaseSettlement, phaseRelease)

func TestFilesReleasedBeforeARestartAreStillCollectedAfterIt(t *testing.T) {
	ctx := context.Background()
	r := newRestartable(t)

	closed := r.closeEveryCycleWithPaymentsInIt(t)

	// The settlement agent discharges the batch, then the clearing house collects
	// the answer and releases every share into its bank's queue. The members'
	// collection is deliberately not selected.
	problems := runPhases(ctx, r.dep, releaseWithoutCollectionPhases)
	for _, p := range problems {
		t.Logf("problem: %s could not process %s: %s", p.Institution, p.OrderID, p.Detail)
	}

	// The state this test is about: settled, released, and in nobody's hands. The
	// count is asked for first, because a test that restarted across an empty
	// queue would pass without measuring anything at all.
	if waiting := r.queuedForTheMembers(t); waiting == 0 {
		t.Fatal("no file is waiting in any member's queue at the clearing house; the release put nothing there to be lost")
	}
	for _, id := range closed {
		cycle, err := r.nets.ClearingHouse().GetCycle(ctx, id)
		if err != nil {
			t.Fatalf("GetCycle %s: %v", id, err)
		}
		if cycle.Status != payment.CycleSettled {
			t.Fatalf("%s is %v before the restart; this test needs a batch whose reserves have already moved", id, cycle.Status)
		}
	}

	r.restart(t)

	// The next business day, on which each member collects what was waiting for
	// it. Nothing releases anything here — the shares were dropped when they were
	// handed over — so a file that did not survive the restart is one no act in
	// this system can produce again.
	report, err := r.dep.AdvanceDay(ctx)
	if err != nil {
		t.Fatalf("AdvanceDay: %v", err)
	}
	for _, p := range report.Problems {
		t.Logf("problem: %s could not process %s: %s", p.Institution, p.OrderID, p.Detail)
	}

	r.everyPaymentReachedItsReceivingBank(t, closed)
}

// queuedForTheMembers is how many files are waiting for the members to collect
// at the clearing house.
//
// It reads the transport's own rows, which is a thing no institution in this
// system can do: the queues are reached through ebics.Server and its vocabulary
// has no way to ask how many. That is the point of the port rather than a gap in
// it, and this is a test holding the store to what it is holding — the same
// licence payment/recon has for opening all N+2 databases at once.
func (r *restartable) queuedForTheMembers(t *testing.T) int {
	t.Helper()
	ctx := context.Background()

	var n int
	err := r.set.ClearingHouseEBICS().View(ctx, func(ctx context.Context, tx ebics.Tx) error {
		for _, b := range r.dep.banksInOrder() {
			files, err := tx.ListQueuedFiles(ctx, ebics.SubscriberID(b.bic))
			if err != nil {
				return err
			}
			n += len(files)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading what is waiting for the members: %v", err)
	}
	return n
}

// everyPaymentReachedItsReceivingBank is the question the whole sub-project is
// about: did the bank that has to move a customer's money get told?
//
// Read the way TestSeededInFlightPaymentsAreAppliedWhenTheirCycleSettles reads
// it — the payment as the clearing house holds it, then the same payment as the
// bank that owes its customer the money holds it — over the payments the given
// cut-offs carried.
func (r *restartable) everyPaymentReachedItsReceivingBank(t *testing.T, closed []payment.CycleID) {
	t.Helper()
	ctx := context.Background()

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
			t.Errorf("%s (%s) is %q at the clearing house, want Settled", p.ID, p.Scheme, p.Status)
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
		// Read raw, because the interesting failure is a 404: a bank that was
		// never handed the file has no row for the payment at all, and the money
		// behind it is already final in that bank's clearing suspense. A shared
		// helper would report that as a status code.
		rec := do(t, routes, "GET", "/payments/"+p.ID, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s (%s, e2e %q): the cut-off carrying it settled across a restart, and %s has never heard of it (HTTP %d); the reserves behind it have moved",
				p.ID, p.Scheme, p.EndToEndID, receiver, rec.Code)
			continue
		}
		var theirs api.PaymentDTO
		if err := json.Unmarshal(rec.Body.Bytes(), &theirs); err != nil {
			t.Fatalf("decoding %s's copy of %s: %v", receiver, p.ID, err)
		}
		if theirs.Status != payment.Settled.String() || leg(theirs) == "" {
			t.Errorf("%s (%s, e2e %q): the cut-off carrying it settled across a restart, and %s holds it as %q with leg %q",
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
