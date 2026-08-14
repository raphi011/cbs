package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/calendar"
	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/payment/recon"
	"github.com/raphi011/cbs/seed"
	"github.com/raphi011/cbs/store/sqlite"
)

// The two tests in this repository that can fail for the reason an
// institution's unfinished obligations live in its own database.

// restartable is an API harness over FILE-backed databases, which can end the
// process holding them and open a second one over the same files.
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
//
// The clock is opened over the same directory, so the business date and how far
// into it the deployment had got survive the process too.
func (r *restartable) boot(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	clock, err := calendar.OpenClock(r.dir, seed.BaseDate)
	if err != nil {
		t.Fatalf("OpenClock: %v", err)
	}
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
func (r *restartable) restart(t *testing.T) {
	t.Helper()
	if err := r.nets.Stores().Close(); err != nil {
		t.Fatalf("closing the stores: %v", err)
	}
	r.boot(t)
}

// closeEveryCycleWithPaymentsInIt is the operator reaching a cut-off out of
// turn, on every window the seeded dataset left open with something in it.
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
func TestACutOffSettledAfterARestartStillReachesEveryReceivingBank(t *testing.T) {
	ctx := context.Background()
	r := newRestartable(t)

	closed := r.closeEveryCycleWithPaymentsInIt(t)

	r.restart(t)

	// The clearing house's own answer, before any of it is driven: it is still
	// holding a share for every payment in every cut-off it closed.
	for _, id := range closed {
		files, err := r.dep.ClearingHouse().Network().ListHeldFiles(ctx, id)
		if err != nil {
			t.Fatalf("ListHeldFiles %s: %v", id, err)
		}
		if len(files) == 0 {
			t.Fatalf("%s was closed before the restart and the clearing house now holds no share for it; the process took its obligations with it", id)
		}
	}
	// And the settlement agent is still holding the instruction, which is the half
	// no guard could have covered: nothing refuses a batch whose instruction has
	// simply vanished, because no institution is waiting for anything.
	pending, err := r.dep.CentralBank().Host().Pending(ctx)
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
	// it.
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
func (r *restartable) queuedForTheMembers(t *testing.T) int {
	t.Helper()
	ctx := context.Background()

	var n int
	err := r.set.ClearingHouseEBICS().View(ctx, func(ctx context.Context, tx ebics.Tx) error {
		for _, b := range r.dep.banksInOrder() {
			files, err := tx.ListQueuedFiles(ctx, ebics.SubscriberID(b.BIC()))
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

// everyPaymentReachedItsReceivingBank is the question a restart is about: did
// the bank that has to move a customer's money get told?
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
		// Read raw, because the interesting failure is a 404: a bank that was never
		// handed the file has no row for the payment at all, and the money behind it
		// is already final in that bank's clearing suspense.
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

	// And the same state read out of the books rather than off the statuses. recon
	// is the instrument that opens all N+2 databases at once, which is what no
	// institution in this system may do and what a claim about two of them
	// agreeing needs.
	books := recon.Check(t, r.nets)
	for _, u := range books.Unreconciled {
		t.Logf("unreconciled: %s (%s) suspense %d, %d in flight, unbooked %v",
			u.Bank, u.Asset, u.Suspense, len(u.InFlight), u.Unbooked)
	}
}

// A day stepped part of the way through is finished by the next process, not
// begun again. This is the case the marker is persisted for: without it, a
// deployment killed mid-day re-runs whatever it had already done.
func TestADayHalfSteppedBeforeARestartIsFinishedAfterIt(t *testing.T) {
	ctx := context.Background()
	r := newRestartable(t)

	stepped := []string{"publish", "refresh", "bank-cut-off", "clearing"}
	for _, key := range stepped {
		if _, err := r.dep.RunThrough(ctx, key); err != nil {
			t.Fatalf("RunThrough %s: %v", key, err)
		}
	}

	r.restart(t)

	// What the second process knows before it does anything: the same four.
	var completed []string
	for _, p := range r.dep.Phases() {
		if p.Completed {
			completed = append(completed, p.Key)
		}
	}
	if !slices.Equal(completed, stepped) {
		t.Errorf("the restarted deployment has run %s, want %s",
			strings.Join(completed, " → "), strings.Join(stepped, " → "))
	}

	report, err := r.dep.AdvanceDay(ctx)
	if err != nil {
		t.Fatalf("AdvanceDay: %v", err)
	}
	ran := make([]string, 0, len(report.Phases))
	for _, p := range report.Phases {
		ran = append(ran, phaseKey(p.name))
	}
	want := []string{
		"clearing-house-cut-off", "discharge", "settlement", "release", "collection",
		"end-of-day", "open-cycles",
	}
	if !slices.Equal(ran, want) {
		t.Errorf("the day after the restart ran %s, want %s",
			strings.Join(ran, " → "), strings.Join(want, " → "))
	}
}
