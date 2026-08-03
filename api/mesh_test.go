package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/mesh"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/seed"
	"github.com/raphi011/cbs/store/testenv"
)

// What this file is for: the seam between HTTP and the mesh.
//
// Everything else in api's suite drives a Server over a store. These tests drive
// one over a store AND a running mesh, because what they are about is the thing
// that is no longer synchronous: a request returns while three other actors are
// still working, and the answer to "what happened" arrives at a different actor
// afterwards. No test here waits for a duration to find out. mesh.Drain blocks
// until nothing is in flight, and that is the only way any of them looks.

// newAPIHarness is a seeded Server with a mesh running over the same network.
//
// Seeded, because the mesh routes by participant and BIC: a payment needs two
// banks that can address each other, an open cut-off window for its scheme, and
// a payer with money. seed.Populate is the dataset the running system serves, so
// these tests exercise the same ids a reader will see in the app — bank_1 is
// Aurora Bank and dep_22 is Alice's account in it.
//
// The mesh is started AFTER the seed, which is the order cmd/server uses and for
// the same reason: mesh.Start reads the participant roster once, so a mesh
// started over an empty store would have no member banks in it at all.
//
// Drain FIRST, then Stop, at cleanup. Stop closes every inbox in one step before
// it joins anybody, so a conversation still in flight when it runs is cut — the
// payer debited and the pacs.002 that would have said so never sent. Both hand
// back dead letters and both are reported: a shutdown that swallowed a handler's
// failure would let one of these tests pass over the very thing it asserts.
func newAPIHarness(t *testing.T) (*Server, *mesh.Mesh) {
	t.Helper()
	ctx := context.Background()

	data := seed.New()
	store := testenv.New(t, data.Now)
	net := payment.NewNetwork(store.Payment(), data.Now)
	if err := data.Populate(ctx, net); err != nil {
		t.Fatalf("populate: %v", err)
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := testMeshConfig
	gate := newMeshGate()
	cfg.Observe = gate.observe
	msh, err := mesh.New(net, cfg, log)
	if err != nil {
		t.Fatalf("mesh.New: %v", err)
	}
	if err := msh.Start(ctx); err != nil {
		t.Fatalf("mesh.Start: %v", err)
	}
	gates.Store(msh, gate)
	// Registered FIRST so it runs LAST, because t.Cleanup is LIFO: the drain
	// below must not be the thing that discovers a gate a test forgot to open.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := msh.Drain(ctx); err != nil {
			t.Errorf("draining at shutdown: %v", err)
		}
		if err := msh.Stop(ctx); err != nil {
			t.Errorf("stopping: %v", err)
		}
	})
	t.Cleanup(func() {
		gate.openAll()
		gates.Delete(msh)
	})
	return NewServer(net, msh, data.Populate, log), msh
}

// The gate: how a test reads the world at a moment the mesh would otherwise
// have run past.
//
// Every assertion in this file about what has NOT happened yet needs one, and
// the reason is that draining is the only other tool available. Drain waits for
// a conversation to FINISH; nothing in the mesh's own API can hold one still.
// Reading the store while four actor goroutines run is a race, and a race that
// mostly wins — TestSubmitAnswers202AndThePaymentIsNotYetAccepted passed 300
// runs in a quiet process and failed 4 of 480 under contention, which is exactly
// the shape of a test that will fail on a busy machine and nowhere else. Worse,
// its failure message was byte-identical to the one a real regression produces,
// so the flake and the bug were indistinguishable.
//
// mesh.Config.Observe runs on the receiving actor's goroutine before its
// handler, so an Observe that blocks holds that actor. Hold the clearing house
// and a submitted payment provably cannot leave Initiated, whatever the
// scheduler does.
//
// # The one rule
//
// Open the gate before anything waits on the mesh. A gate held while the
// goroutine that would open it is inside Drain — or inside an HTTP handler that
// drains — deadlocks, and the test dies by timeout rather than by assertion.
// That is a real property rather than a hazard to route around: an api handler
// that invented a synchronous wait would deadlock here, which is one of the ways
// this file refuses to let one exist.
type meshGate struct {
	mu   sync.Mutex
	held map[iso20022.BIC]chan struct{}
}

// gates maps a mesh to the gate installed in front of it. A registry rather than
// a return value, because the brief's test bodies call newAPIHarness(t) and take
// two values, and the seam must not change what they say.
var gates sync.Map

func newMeshGate() *meshGate {
	return &meshGate{held: map[iso20022.BIC]chan struct{}{}}
}

func (g *meshGate) observe(to, from iso20022.BIC, raw []byte) {
	g.mu.Lock()
	ch := g.held[to]
	g.mu.Unlock()
	if ch != nil {
		<-ch
	}
}

func (g *meshGate) openAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for bic, ch := range g.held {
		close(ch)
		delete(g.held, bic)
	}
}

// holdMessagesTo shuts one actor's door and returns the function that opens it.
// Everything addressed to that actor waits, unhandled, until it is called.
func holdMessagesTo(t *testing.T, msh *mesh.Mesh, bic iso20022.BIC) func() {
	t.Helper()
	v, ok := gates.Load(msh)
	if !ok {
		t.Fatal("this mesh has no gate; it was not built by newAPIHarness")
	}
	g := v.(*meshGate)

	ch := make(chan struct{})
	g.mu.Lock()
	if _, already := g.held[bic]; already {
		g.mu.Unlock()
		t.Fatalf("%s is already held", bic)
	}
	g.held[bic] = ch
	g.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			delete(g.held, bic)
			g.mu.Unlock()
			close(ch)
		})
	}
}

// testMeshConfig names the two institutions, exactly as cmd/server's meshConfig
// does. They have no participant row — they ARE the configuration — and they are
// different from each other and from every seeded bank's BIC.
var testMeshConfig = mesh.Config{
	CentralBankBIC:   "CBSEDEFFXXX",
	ClearingHouseBIC: "CSMXFRPPXXX",
}

// validSubmission is Alice at Aurora Bank paying Bella at Banca Verde: a credit
// transfer the seeded network can carry the whole way.
//
// The ids are the seed's own and are deterministic — the store's id sequences
// start from nothing, so bank_1/dep_22 is Alice on every run and after every
// reset. The IBANs are quoted because SEPA credit transfer is addressed BY iban
// (payment.Scheme.AddressedBy): without them the payee's bank has no address to
// resolve and answers AC01.
//
// The amount is small enough that Alice can afford it whatever else the seeded
// scenario has already taken out of her account.
func validSubmission() string {
	return `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"bank_1","account":"dep_22","identifier":{"scheme":"IBAN","value":"SE89-AURORA-1001"}},
		"creditor":{"participant":"bank_3","account":"dep_25","identifier":{"scheme":"IBAN","value":"IT60-VERDE-2002"}},
		"amount":1000,
		"description":"mesh handoff",
		"creditorName":"Bella Bruno"
	}`
}

func postJSON(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, "POST", path, body)
}

func post(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, h, "POST", path, "")
}

// decodePaymentID reads the identifier a 202 answers with. It is the whole of
// what a submission returns, and the second half of the exchange is asking about
// it — see submittedPaymentDTO.
func decodePaymentID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out submittedPaymentDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
	if out.PaymentID == "" {
		t.Fatalf("the response carries no paymentId: %s", rec.Body.String())
	}
	return out.PaymentID
}

// getPayment reads a payment back through the clearing house's surface, which is
// the one operator that can see every payment in the network.
func getPayment(t *testing.T, s *Server, id string) paymentDTO {
	t.Helper()
	var out paymentDTO
	getJSON(t, csm(s), "/payments/"+id, &out)
	return out
}

// drain waits for every message in flight to be handled and fails on a dead
// letter. No sleep, no polling: Drain blocks until nothing is in flight.
func drain(t *testing.T, msh *mesh.Mesh) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := msh.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

// 202 was already the shape 6a chose, because a real CSM answers later. Now it
// is true rather than anticipatory: the payment really is Initiated when the
// response is written.
func TestSubmitAnswers202AndThePaymentIsNotYetAccepted(t *testing.T) {
	srv, msh := newAPIHarness(t)
	// The clearing house's door is shut before the instruction is sent, so the
	// payment CANNOT be carried past Initiated while the read below happens. See
	// meshGate: without it this assertion was true by scheduling luck, and its
	// failure was indistinguishable from a regression's.
	open := holdMessagesTo(t, msh, testMeshConfig.ClearingHouseBIC)
	rec := postJSON(t, srv.BankRoutes("bank_1"), "/payments", validSubmission())
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	id := decodePaymentID(t, rec)

	before := getPayment(t, srv, id)
	if before.Status != "Initiated" {
		t.Errorf("status before draining = %q, want Initiated", before.Status)
	}

	open()
	drain(t, msh)

	after := getPayment(t, srv, id)
	if after.Status != "Accepted" {
		t.Errorf("status after draining = %q, want Accepted", after.Status)
	}
}

// Reset drains first. Truncating with messages in flight would leave handlers
// writing into a store the reset had already emptied, and the seed would race
// its own leftovers.
func TestResetDrainsBeforeTruncating(t *testing.T) {
	srv, msh := newAPIHarness(t)
	postJSON(t, srv.BankRoutes("bank_1"), "/payments", validSubmission())
	rec := post(t, srv.CentralBankRoutes(), "/admin/reset")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d", rec.Code)
	}
	if err := msh.Drain(context.Background()); err != nil {
		t.Fatalf("messages were still in flight after reset returned: %v", err)
	}
}

// A reset replaces the network, so it has to replace the mesh's picture of it.
//
// This is the sequence the reviewer walked over HTTP and it failed at step 4:
// admit a bank, reset, admit the same BIC again. The mesh had never been told
// the first bank was gone, so an actor still answered to that address, the
// re-admission was refused, and the participant row was left behind anyway. The
// BIC was unusable for the life of the process, on a system whose roster was
// empty.
//
// Every assertion below is one of the three things that were wrong: the
// re-admission is accepted, the bank that comes back can actually be paid
// through — which is the part a 201 alone would not prove, since a row with no
// actor answers every read and carries no payment — and the refusal that IS
// still reachable says something true.
func TestResetRebuildsTheMeshSoAReadmittedBankCanPay(t *testing.T) {
	h := newServer(t, nil)

	admit := func(name, bic string, want int) map[string]any {
		t.Helper()
		return doJSON(t, cb(h), "POST", "/members", `{"bic":"`+bic+`","name":"`+name+`"}`, want)
	}
	admit("Bank A", "BNKADEFFXXX", http.StatusCreated)
	admit("Bank B", "BNKBDEFFXXX", http.StatusCreated)

	assertStatus(t, cb(h), "POST", "/admin/reset", "", http.StatusOK)

	var members []participantDTO
	getJSON(t, csm(h), "/members", &members)
	if len(members) != 0 {
		t.Fatalf("the roster holds %d banks after a reset with no reseed, want 0", len(members))
	}

	// The same two addresses, on a system that has never heard of them.
	a := admit("Bank A again", "BNKADEFFXXX", http.StatusCreated)["id"].(string)
	b := admit("Bank B again", "BNKBDEFFXXX", http.StatusCreated)["id"].(string)

	alice := doJSON(t, bank(h, a), "POST", "/deposit-accounts",
		`{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`","identifiers":[{"scheme":"IBAN","value":"SE89-AFTER-RESET-01"}]}`,
		http.StatusCreated)["id"].(string)
	bob := doJSON(t, bank(h, b), "POST", "/deposit-accounts",
		`{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`","identifiers":[{"scheme":"IBAN","value":"IT60-AFTER-RESET-01"}]}`,
		http.StatusCreated)["id"].(string)
	doJSON(t, bank(h, a), "POST", "/deposits", `{"account":"`+alice+`","amount":100000,"description":"opening"}`, http.StatusOK)
	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	// The whole point: these banks are routable in both directions. A stale
	// actor would have made this "no bank actor", and an actor that was never
	// registered would have made it RC01 at the clearing house.
	pay := doJSON(t, bank(h, a), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`","identifier":{"scheme":"IBAN","value":"IT60-AFTER-RESET-01"}},
		"amount":25000,
		"creditorName":"Bob"
	}`, http.StatusAccepted)["paymentId"].(string)
	drainServer(t, h)
	if got := doJSON(t, csm(h), "GET", "/payments/"+pay, "", http.StatusOK); got["status"].(string) != "Accepted" {
		t.Fatalf("a payment between two re-admitted banks is %q, want Accepted", got["status"])
	}

	// And the refusal that is still reachable — a third bank on an address one
	// of these already answers to — says what is actually true of it.
	rec := do(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A yet again"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("admitting a third bank on Bank A's BIC = %d, want 422", rec.Code)
	}
	msg := rec.Body.String()
	if !strings.Contains(msg, "no actor of its own") || !strings.Contains(msg, "two actors for BNKADEFFXXX") {
		t.Errorf("the refusal reads %q; it must say the row exists and name the clash", msg)
	}
	// And on THIS branch the remedy applies, which is the other half of the
	// split: a clashing address is fixed by choosing another one.
	// TestAdmissionDuringAShutdownIsRefusedWithoutTheRemedy is the branch where
	// that advice would send the operator to make a second orphan.
	if !strings.Contains(msg, "admit it on a BIC no other bank answers to") {
		t.Errorf("the refusal reads %q; a clashing address has a remedy and it is missing", msg)
	}
	// The bank that owns the address is untouched by the refusal, which is the
	// half the old message got wrong: it called the ADMITTED bank unroutable
	// while the clash's owner went on working.
	var after []paymentDTO
	getJSON(t, bank(h, a), "/payments", &after)
	if len(after) != 1 {
		t.Errorf("Bank A sees %d of its own payments after the refusal, want 1", len(after))
	}
}

// Admission during a shutdown is refused WITHOUT the remedy, and that is the
// point of telling the two refusals apart.
//
// Both branches leave the same thing behind — a participant row whose bank has
// no actor — so the invariant half of the message is true in both. The advice is
// not. "Admit it on a BIC no other bank answers to" is the fix for a clash and
// is actively harmful here: the operator who follows it gets the same 422 and a
// second orphaned row, because what refused them was the mesh going down and not
// the address they chose.
func TestAdmissionDuringAShutdownIsRefusedWithoutTheRemedy(t *testing.T) {
	h := newServer(t, nil)

	// The mesh's cleanup will Stop it again; a second Stop only re-joins, and on
	// a mesh that is already stopped it does nothing at all.
	if err := h.mesh.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	rec := do(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("admitting a bank into a stopped mesh = %d, want 422 (body: %s)", rec.Code, rec.Body.String())
	}
	msg := rec.Body.String()
	if !strings.Contains(msg, "no actor of its own") {
		t.Errorf("the refusal reads %q; the half that is true in every branch is missing", msg)
	}
	if strings.Contains(msg, "admit it on a BIC") {
		t.Errorf("the refusal reads %q; that advice is for a clashing address and there is none here", msg)
	}
}

// The reseeded banks are rejoined, not just recreated.
//
// The sample dataset is rebuilt by seed.Populate, which drives payment.Network
// directly and knows nothing about the mesh — so every bank it creates is a row
// with no actor until Reset rejoins the roster. Without that, a reset leaves a
// system that answers every read and cannot carry a payment, which is the worst
// shape a demo can be in: nothing looks broken until somebody tries to pay.
func TestPayingAfterAResetGoesThroughTheReseededBanks(t *testing.T) {
	srv, msh := newAPIHarness(t)

	rec := post(t, srv.CentralBankRoutes(), "/admin/reset")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d (body: %s)", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, srv.BankRoutes("bank_1"), "/payments", validSubmission())
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submitting after a reset = %d (body: %s)", rec.Code, rec.Body.String())
	}
	id := decodePaymentID(t, rec)
	drain(t, msh)
	if got := getPayment(t, srv, id); got.Status != "Accepted" {
		t.Fatalf("a payment submitted after a reset is %q, want Accepted", got.Status)
	}
}

// The two kinds of failure a mesh-driven handler has, and the line between them.
//
// A submission the payer's own bank refuses is decided inside its own unit of
// work, before anything is sent, so it comes back as a status code in the
// response to the request that caused it. A payment the FAR SIDE refuses cannot:
// by the time the payee's bank has looked at the address, this handler has
// answered 202 and the connection is gone. The refusal lands on the payment's
// own row instead, and the caller finds it by asking again.
//
// Both halves are asserted here because the split is what ruling 4 of this task
// is about, and either half alone would be satisfied by a system that had got the
// other one wrong.
func TestWhichRefusalsReachTheCallerAndWhichDoNot(t *testing.T) {
	srv, msh := newAPIHarness(t)

	// Answerable: Alice cannot pay a hundred million. Her own bank says so,
	// inside Submit's unit of work, and nothing is sent.
	tooMuch := strings.Replace(validSubmission(), `"amount":1000`, `"amount":100000000`, 1)
	rec := postJSON(t, srv.BankRoutes("bank_1"), "/payments", tooMuch)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a payment the payer's own bank refuses answered %d, want 422 (body: %s)", rec.Code, rec.Body.String())
	}

	// Unanswerable: the IBAN is well-formed and belongs to nobody. Only the
	// payee's bank can know that, and it is not this request's to report.
	unknown := strings.Replace(validSubmission(), "IT60-VERDE-2002", "IT60-VERDE-9999", 1)
	rec = postJSON(t, srv.BankRoutes("bank_1"), "/payments", unknown)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("a payment only the far side can refuse answered %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	id := decodePaymentID(t, rec)

	drain(t, msh)

	got := getPayment(t, srv, id)
	if got.Status != "Rejected" {
		t.Errorf("status after draining = %q, want Rejected — the payee's bank cannot resolve that address", got.Status)
	}
	if got.RejectCode != "AC01" {
		t.Errorf("reject code = %q, want AC01 — the refusal reached the payment even though it could not reach the caller", got.RejectCode)
	}
}

// The cut-off instructs settlement, and the console can reach it.
//
// Closing a cycle used to be a call into payment.Network that netted the batch
// and told nobody; POST /settlements was the second console button that
// discharged it, and it is gone. What closes a cycle now sends a pacs.009, and
// that is the whole of what "the mesh is wired into api" buys.
//
// What this test asserts, exactly: 200 with a Closed cycle carrying no
// settlement, then a drain, then — through the CENTRAL BANK's surface and only
// that one — the cycle read back as Settled and the settlement it names. Two of
// its four reads, one operator.
//
// The rest of the 6b read surface is covered where it was covered before, and
// deliberately not duplicated here: TestTheCentralBankCanReadTheCycleItSettles
// (api/surface_test.go) walks all four of the central bank's, and
// TestTheClearingHouseReadsTheSettlementItDidNotPerform walks the clearing
// house's side of the same settlement. This test is about the INSTRUCTION, not
// about who may read what.
func TestClosingACycleThroughTheAPIInstructsSettlement(t *testing.T) {
	srv, msh := newAPIHarness(t)

	rec := postJSON(t, srv.BankRoutes("bank_1"), "/payments", validSubmission())
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit = %d (body: %s)", rec.Code, rec.Body.String())
	}
	id := decodePaymentID(t, rec)
	drain(t, msh)

	cid := getPayment(t, srv, id).CycleID
	if cid == "" {
		t.Fatal("the accepted payment is in no cycle; there is nothing to close")
	}

	var closed clearingCycleDTO
	crec := post(t, csm(srv), "/cycles/"+cid+"/close")
	if crec.Code != http.StatusOK {
		t.Fatalf("close = %d (body: %s)", crec.Code, crec.Body.String())
	}
	if err := json.Unmarshal(crec.Body.Bytes(), &closed); err != nil {
		t.Fatalf("decoding the closed cycle: %v", err)
	}
	if closed.Status != "Closed" {
		t.Errorf("cycle status in the response = %q, want Closed", closed.Status)
	}
	if closed.SettlementID != "" {
		t.Errorf("the close answered with settlement %q; the central bank had not been asked yet", closed.SettlementID)
	}

	drain(t, msh)

	var settled clearingCycleDTO
	getJSON(t, cb(srv), "/cycles/"+cid, &settled)
	if settled.Status != "Settled" {
		t.Fatalf("cycle is %q after draining, want Settled — the pacs.009 is what discharges it", settled.Status)
	}
	if settled.SettlementID == "" {
		t.Fatal("the settled cycle names no settlement")
	}
	var st settlementDTO
	getJSON(t, cb(srv), "/settlements/"+settled.SettlementID, &st)
	if st.CycleID != cid {
		t.Errorf("settlement %s is against cycle %q, want %q", st.ID, st.CycleID, cid)
	}
}

// An operator's rejection is the clearing house's half and the payer's bank's
// half, and only the first one is in the response.
//
// api used to run both in one transaction, so the 200 it wrote described a payer
// who had already been refunded. It cannot any more: the refund is another
// institution's act in another institution's book, and it happens when the
// pacs.002 gets there.
//
// What this test can honestly assert is the RESPONSE — which describes the
// clearing house's half and only that — and the balance after the drain. It
// deliberately does not read the balance in between: the payer's bank actor runs
// concurrently with this goroutine, so "not yet refunded" would be a race
// dressed up as an assertion. mesh's own
// TestAnOperatorRejectionRefundsThePayerOnlyOnceTheMessageArrives measures which
// actor posted the refund, without a clock, and
// TestARejectionWhoseRefundFailsStandsAndIsDeadLettered pins that the two halves
// are no longer one unit of work.
func TestRejectingThroughTheAPIRefundsThePayerOnlyAfterTheMessageArrives(t *testing.T) {
	srv, msh := newAPIHarness(t)

	rec := postJSON(t, srv.BankRoutes("bank_1"), "/payments", validSubmission())
	id := decodePaymentID(t, rec)
	drain(t, msh)
	before := aliceBalance(t, srv)

	rrec := postJSON(t, csm(srv), "/payments/"+id+"/reject", `{"reason":"card lost"}`)
	if rrec.Code != http.StatusAccepted {
		t.Fatalf("reject = %d (body: %s)", rrec.Code, rrec.Body.String())
	}
	var rejected paymentDTO
	if err := json.Unmarshal(rrec.Body.Bytes(), &rejected); err != nil {
		t.Fatalf("decoding the rejected payment: %v", err)
	}
	if rejected.Status != "Rejected" {
		t.Errorf("the rejection answered with status %q, want Rejected", rejected.Status)
	}
	drain(t, msh)

	if got := aliceBalance(t, srv); got != before+1000 {
		t.Errorf("payer's balance after draining = %d, want %d — the pacs.002 is what gives the money back", got, before+1000)
	}
}

// A return goes round the mesh, and the reason code it carries is how that is
// visible from here.
//
// The distinction this pins is not "did the money come back" — the network's own
// ReturnPayment would do that too, synchronously, which is what this handler used
// to call. It is WHO did it and WITH WHAT. Mesh.Return hands the instruction to
// the bank that RECEIVED the original — the payee's bank on a push — which posts
// nothing and sends a pacs.004; the three compensating postings happen four hops
// later at the settlement agent, and the reason travels on the wire as a code and
// a text. The refund's own description in the payer's ledger is where both
// surface, and it is the one thing a synchronous call could not produce.
//
// The payment is a SETTLED one out of the seeded dataset, because finality is a
// return's precondition: ReturnPaymentTx refuses anything else.
func TestReturningThroughTheAPIGoesRoundTheMesh(t *testing.T) {
	srv, msh := newAPIHarness(t)

	var payments []paymentDTO
	getJSON(t, csm(srv), "/payments", &payments)
	var settled paymentDTO
	for _, p := range payments {
		if p.Status == "Settled" && p.Scheme == "sepa.ct" {
			settled = p
			break
		}
	}
	if settled.ID == "" {
		t.Fatal("the seeded dataset holds no settled credit transfer to return")
	}

	rec := postJSON(t, csm(srv), "/payments/"+settled.ID+"/return", `{"reason":"account closed"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("return = %d (body: %s)", rec.Code, rec.Body.String())
	}
	// An identifier and nothing else. There is no intermediate resource to
	// describe: the returning bank posted nothing and decided nothing beyond
	// whether there was a settled payment to send back.
	if id := decodePaymentID(t, rec); id != settled.ID {
		t.Errorf("the return answered with %q, want %q", id, settled.ID)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding the return response: %v", err)
	}
	if _, leaked := body["status"]; leaked {
		t.Errorf("the return carries a status: %v — its outcome is decided four hops away", body)
	}

	drain(t, msh)

	if got := getPayment(t, srv, settled.ID); got.Status != "Returned" {
		t.Fatalf("payment is %q after draining, want Returned", got.Status)
	}
	// The refund in the payer's own ledger, found by the key the domain gives it,
	// carries the code the pacs.004 travelled under beside the operator's text.
	// A handler that called the network's ReturnPayment directly would describe
	// it with the text alone: there would have been no message to put a code on.
	var txns []transactionDTO
	getJSON(t, bank(srv, string(settled.Debtor.Participant)), "/transactions", &txns)
	want := settled.ID + ":return-debit"
	for _, tx := range txns {
		if tx.IdempotencyKey != want {
			continue
		}
		if !strings.Contains(tx.Description, "MS03: account closed") {
			t.Errorf("the payer's refund is described %q, want it to carry MS03 and the operator's text", tx.Description)
		}
		return
	}
	t.Fatalf("no posting under %q in the payer's bank's book; the money never came back", want)
}

// aliceBalance is the seeded payer's book balance, read through her own bank.
func aliceBalance(t *testing.T, s *Server) int64 {
	t.Helper()
	bal := doJSON(t, bank(s, "bank_1"), "GET", "/deposit-accounts/dep_22/balance", "", http.StatusOK)
	return int64(bal["book"].(float64))
}
