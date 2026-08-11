package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/cbs/api"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/seed"
	"github.com/raphi011/cbs/store/testenv"
)

// What this file is for: the seam between HTTP and the mesh.
//
// Every other file here drives the three surfaces over a store. These tests drive
// one over a store AND a running mesh, because what they are about is the thing
// that is not synchronous: a request returns while three other actors are still
// working, and the answer to "what happened" arrives at a different actor
// afterwards. No test here waits for a duration to find out. Mesh.Drain blocks
// until nothing is in flight, and that is the only way any of them looks.

// newAPIHarness is a seeded deployment with a mesh running over the same network.
//
// Seeded, because the mesh routes by BIC: a payment needs two banks that can
// address each other, an open cut-off window for its scheme, and a payer with
// money. seed.Populate is the dataset the running system serves, so these tests
// exercise the same rows a reader will see in the app. They name them by IBAN
// rather than by id — see seededParty.
//
// The mesh is started BEFORE the seed, which is the order cmd/server uses and
// for the same reason: the seed gives each bank it provisions an actor and pulls
// each one's routing directory, both through the mesh's own doors, so the
// transport has to be running first. Mesh.Start's roster read finds nothing here
// — the banks it would have registered are the ones Populate is about to
// provision, and each of those gets its actor from Mesh.AddBank as it is built.
//
// Drain FIRST, then Stop, at cleanup. Stop closes every inbox in one step before
// it joins anybody, so a conversation still in flight when it runs is cut — the
// payer debited and the pacs.002 that would have said so never sent. Both hand
// back dead letters and both are reported: a shutdown that swallowed a handler's
// failure would let one of these tests pass over the very thing it asserts.
func newAPIHarness(t *testing.T) (*server, *Mesh) {
	t.Helper()
	ctx := context.Background()

	data := seed.New()
	nets := payment.NewNetworks(testenv.NewSet(t, data.Now), data.Now)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := testMeshConfig
	gate := newMeshGate()
	cfg.Observe = gate.observe
	msh, err := NewMesh(nets, cfg, log)
	if err != nil {
		t.Fatalf("NewMesh: %v", err)
	}
	if err := msh.Start(ctx); err != nil {
		t.Fatalf("Mesh.Start: %v", err)
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

	// Seeded LAST, and after both cleanups are registered: the seed's four
	// admissions are conversations through this mesh, so a Populate that failed
	// halfway would otherwise leave actor goroutines nobody joins. They pass the
	// gate like every other message and go straight through, because no test has
	// run yet and so no gate is held.
	if err := data.Populate(ctx, nets, msh); err != nil {
		t.Fatalf("populate: %v", err)
	}
	return &server{dep: NewDeployment(nets, msh, data.Populate, log), nets: nets, mesh: msh}, msh
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
// MeshConfig.Observe runs on the receiving actor's goroutine before its
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
func holdMessagesTo(t *testing.T, msh *Mesh, bic iso20022.BIC) func() {
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
var testMeshConfig = MeshConfig{
	CentralBankBIC:   "CBSEDEFFXXX",
	ClearingHouseBIC: "CSMXFRPPXXX",
}

// seededParty is the participant and account behind one of the seed's IBANs.
//
// The ids are not written down. Every id in a book comes from one counter, so an
// act that allocates one more id moves every id after it — and the seed builds
// the same things in the same order only for as long as nobody adds an act.
//
// An IBAN is the seed's own stable name for a customer and is what these tests
// ask by. It is stable for a durable reason: the seed CHOSE it, where an id is
// whatever the counter had reached.
//
// It SWEEPS, and it has to do the sweeping itself: no bank can answer "whose
// IBAN is this" (payment.ResolveIdentifier), so this helper asks each bank about
// its own register in turn. That is the test harness standing outside the network
// and looking at all of it, which is a thing a test may do and no institution
// may — the same standing payment/recon is built on.
//
// It returns the BANK as well as the ref, because a payment.PartyRef names none
// (see payment.PartyRef) and every caller here needs both: the account to quote
// on an instruction, and the address to bind a listener to or to put in an agent
// field. The sweep already knows which bank answered.
func seededParty(t *testing.T, s *server, iban string) (iso20022.BIC, payment.PartyRef) {
	t.Helper()
	ctx := context.Background()
	ident := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: iban}
	// Every bank in the system, which is every bank with a DATABASE — asked of
	// the store set rather than of any institution, because no institution holds
	// a list of the others. See payment.Stores.Banks.
	bics, err := s.nets.Stores().Banks(ctx)
	if err != nil {
		t.Fatalf("listing the seed's banks: %v", err)
	}
	for _, bic := range bics {
		net, err := s.nets.Bank(ctx, payment.ParticipantID(bic))
		if err != nil {
			t.Fatalf("opening %s's store: %v", bic, err)
		}
		switch ref, err := net.ResolveIdentifier(ctx, ident); {
		case err == nil:
			return bic, ref
		case errors.Is(err, deposit.ErrIdentifierNotFound):
		default:
			t.Fatalf("asking %s about the seed's %s: %v", bic, iban, err)
		}
	}
	t.Fatalf("no seeded bank holds %s", iban)
	return "", payment.PartyRef{}
}

// payerRoutes is the bank router of whichever bank holds the seed's Alice — the
// payer every submission below is made by.
func payerRoutes(t *testing.T, s *server) http.Handler {
	t.Helper()
	bic, _ := seededParty(t, s, aliceIBAN)
	h, err := s.BankRoutes(context.Background(), payment.ParticipantID(bic))
	if err != nil {
		t.Fatalf("binding %s's surface: %v", bic, err)
	}
	return h
}

// The seed's two customers these tests move money between, named by address.
//
// Derived rather than written out, because a bank MINTS its customers'
// addresses: these are the allocations the settlement agent gives those two
// banks and the order the seed opens their accounts in, which is the only thing
// about them a test can depend on. A literal would say what the check digits are
// today and go stale silently the first time an account is inserted above.
//
// The CODES are still written out and are the one thing here that could drift.
// A registry allocates from the top of each country's range downwards, so the
// first bank admitted in Germany gets 99999999 and the first in Italy 99999 —
// which holds for as long as the seed admits those two banks first in their
// countries. See payment.allocateBankCodeTx.
//
// Note that they are in different countries, of different lengths, with the
// bank code at a different offset in each. That is the seed's point, and it is
// what makes the routing directory a lookup rather than a substring.
var (
	aliceIBAN = mustMint(iban.DE, "99999999", 1) // Aurora Bank's first account
	bellaIBAN = mustMint(iban.IT, "99999", 2)    // Banca Verde's second
)

// validSubmission is Alice at Aurora Bank paying Bella at Banca Verde: a credit
// transfer the seeded network can carry the whole way.
//
// The IBANs are quoted in the body as well as used to look the ids up, because
// SEPA credit transfer is addressed BY iban (payment.Scheme.AddressedBy):
// without them the payee's bank has no address to resolve and answers AC01.
//
// The amount is small enough that Alice can afford it whatever else the seeded
// scenario has already taken out of her account.
func validSubmission(t *testing.T, s *server) string {
	t.Helper()
	_, payer := seededParty(t, s, aliceIBAN)
	_, payee := seededParty(t, s, bellaIBAN)
	// NO agent on either side, and none to give: the instruction carries an
	// address and a name, and the payer's bank derives the routing element from
	// the address through its own copy of the scheme's directory. See api's
	// InitiatePaymentRequest and payment.SubmitPaymentTx.
	//
	// A party object carries no "participant" either. Which bank a side is at is
	// something this request never says; see api.PartyRefDTO.
	return fmt.Sprintf(`{
		"scheme":"sepa.ct",
		"debtor":{"account":%q,"identifier":{"scheme":"IBAN","value":%q}},
		"creditor":{"account":%q,"identifier":{"scheme":"IBAN","value":%q}},
		"amount":1000,
		"description":"mesh handoff",
		"creditorName":"Bella Bruno"
	}`,
		payer.Account, aliceIBAN,
		payee.Account, bellaIBAN)
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
// it — see SubmittedPaymentDTO.
func decodePaymentID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var out api.SubmittedPaymentDTO
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
func getPayment(t *testing.T, s *server, id string) api.PaymentDTO {
	t.Helper()
	var out api.PaymentDTO
	getJSON(t, csmSurface(s), "/payments/"+id, &out)
	return out
}

// drain waits for every message in flight to be handled and fails on a dead
// letter. No sleep, no polling: Drain blocks until nothing is in flight.
func drain(t *testing.T, msh *Mesh) {
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
	rec := postJSON(t, payerRoutes(t, srv), "/payments", validSubmission(t, srv))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	id := decodePaymentID(t, rec)

	// Read on the PAYER'S BANK's surface, because with the clearing house's door
	// shut that bank is the only institution that has a row for this payment at
	// all — the instruction has not been relayed, so the clearing house has not
	// been told it exists and answers 404 rather than "Initiated". Which is the
	// same claim in a stronger form: the response was written before any other
	// institution had heard of the payment.
	var before api.PaymentDTO
	getJSON(t, payerRoutes(t, srv), "/payments/"+id, &before)
	if before.Status != "Initiated" {
		t.Errorf("status before draining = %q, want Initiated", before.Status)
	}
	assertStatus(t, csmSurface(srv), "GET", "/payments/"+id, "", http.StatusNotFound)

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
	postJSON(t, payerRoutes(t, srv), "/payments", validSubmission(t, srv))
	rec := post(t, srv.CentralBankRoutes(), "/admin/reset")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d", rec.Code)
	}
	if err := msh.Drain(context.Background()); err != nil {
		t.Fatalf("messages were still in flight after reset returned: %v", err)
	}
}

// reserveRows reads the central bank's reserve report: the whole list when pid
// is empty, one bank's when it is not.
func reserveRows(t *testing.T, s *server, pid string) []map[string]any {
	t.Helper()
	path := "/reserves"
	if pid != "" {
		path += "/" + pid
	}
	var out []map[string]any
	getJSON(t, cbSurface(s), path, &out)
	return out
}

// TestAnAddressNoMemberIsPublishedUnderIsUnprocessable is the clearing house's
// console meeting an address it cannot hand to anybody.
//
// An instruction names no bank at all. On a bank's own port the submitting bank
// is the port; on THIS one it is read out of the payer's address, through the
// roster this institution publishes — so an address under a bank code no member
// holds is one this console cannot act on, and 422 is the answer.
//
// It is the clearing house's own version of the refusal a member meets from its
// copy (payment.ErrBankCodeUnknown), and the difference between the two is the
// whole of the subscription model: this institution CAN tell that no member holds
// the code, because the roster is where a member comes into existence. A member
// asking the same question of its own snapshot cannot.
//
// The 422 rather than a 404 is the older ruling, unchanged: an address nobody is
// published under is a well-formed field this system will not act on, not a
// missing resource.
//
// It is asked of this console because that is the only surface that can ask it. A
// bank's own POST /payments is bound to one bank and takes the submitting side
// from that binding, so it has no way to name a stranger as the payer.
func TestAnAddressNoMemberIsPublishedUnderIsUnprocessable(t *testing.T) {
	srv, _ := newAPIHarness(t)
	// A well-formed German address under a code this scheme has allocated to
	// nobody. It passes mod-97, so what refuses it is the roster and not the
	// check digits.
	body := strings.Replace(validSubmission(t, srv), aliceIBAN, mustMint(iban.DE, "10000000", 1), 1)
	rec := postJSON(t, csmSurface(srv), "/payments", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a payer's address no member is published under = %d, want 422 (body: %s)", rec.Code, rec.Body)
	}
}

// TestAReseededNetworkCanStillBePaidThrough is the guarantee that moved when
// Reset stopped calling JoinRoster.
//
// A reset forgets every bank actor, truncates and reseeds. The actors are not put
// back in a step of the reset's own: the reseed admits its banks through the mesh,
// so each one gets its actor in the call that founds it. That is a better division
// of labour and a weaker guarantee, because nothing in Reset can check that a
// reseed did it. This is what checks it, from the outside, in the only way that matters:
// a payment between two reseeded banks reaches the far side.
//
// A row with no actor answers every read, so an assertion on the participant
// list would pass over exactly the failure this exists to catch. It has to be a
// payment, and it has to be carried to Accepted, because that is the state only
// the PAYEE's bank can put it in.
func TestAReseededNetworkCanStillBePaidThrough(t *testing.T) {
	srv, _ := newAPIHarness(t)

	rec := post(t, srv.CentralBankRoutes(), "/admin/reset")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d (body: %s)", rec.Code, rec.Body)
	}

	// The seed's own two customers, resolved again: the reseed rebuilt them, and
	// their ids are not the ones the first build had reason to be.
	pay := postJSON(t, payerRoutes(t, srv), "/payments", validSubmission(t, srv))
	if pay.Code != http.StatusAccepted {
		t.Fatalf("submitting after a reset = %d (body: %s)", pay.Code, pay.Body)
	}
	id := decodePaymentID(t, pay)
	drain(t, srv.mesh)
	if got := getPayment(t, srv, id); got.Status != "Accepted" {
		t.Fatalf("a payment through a reseeded network is %q, want Accepted", got.Status)
	}
}

// A reset replaces the network, so it has to replace the mesh's picture of it.
//
// The sequence that fails without it: provision a bank, reset, provision the same
// BIC again. If the mesh is never told the first bank is gone, an actor still
// answers to that address, the second attempt is refused, and the row is left
// behind anyway — the BIC unusable for the life of the process, on a system whose
// roster is empty.
//
// Every assertion below is one of the three things that go wrong: the address is
// free again, the bank that comes back can actually be paid through — a row with
// no actor answers every read and carries no payment — and the refusal that IS
// still reachable says something true.
func TestResetRebuildsTheMeshSoAReadmittedBankCanPay(t *testing.T) {
	h := newServer(t, nil)

	provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	provisionMember(t, h, "BNKBDEFFXXX", "Bank B")

	assertStatus(t, cbSurface(h), "POST", "/admin/reset", "", http.StatusOK)

	var members []api.ParticipantDTO
	getJSON(t, cbSurface(h), "/members", &members)
	if len(members) != 0 {
		t.Fatalf("the roster holds %d banks after a reset with no reseed, want 0", len(members))
	}

	// The same two addresses, on a system that has never heard of them.
	a := provisionMember(t, h, "BNKADEFFXXX", "Bank A again")
	b := provisionMember(t, h, "BNKBDEFFXXX", "Bank B again")

	alice := doJSON(t, bankSurface(h, a), "POST", "/deposit-accounts",
		`{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`"}`,
		http.StatusCreated)["id"].(string)
	bob := doJSON(t, bankSurface(h, b), "POST", "/deposit-accounts",
		`{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`,
		http.StatusCreated)["id"].(string)
	doJSON(t, bankSurface(h, a), "POST", "/deposits", `{"account":"`+alice+`","amount":100000,"description":"opening"}`, http.StatusOK)
	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	// The whole point: these banks are routable in both directions. A stale
	// actor would have made this "no bank actor", and an actor that was never
	// registered would have made it RC01 at the clearing house.
	pay := doJSON(t, bankSurface(h, a), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+alice+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, alice)+`"}},
		"creditor":{"account":"`+bob+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, b, bob)+`"}},
		"amount":25000,
		"creditorName":"Bob"
	}`, http.StatusAccepted)["paymentId"].(string)
	drainServer(t, h)
	if got := doJSON(t, csmSurface(h), "GET", "/payments/"+pay, "", http.StatusOK); got["status"].(string) != "Accepted" {
		t.Fatalf("a payment between two re-admitted banks is %q, want Accepted", got["status"])
	}

	// Bank A's own payment survives everything above, which is the half worth
	// stating separately: a reset that rebuilt the mesh but lost the book would
	// pass every assertion so far.
	var after []api.PaymentDTO
	getJSON(t, bankSurface(h, a), "/payments", &after)
	if len(after) != 1 {
		t.Errorf("Bank A sees %d of its own payments, want 1", len(after))
	}
}

// participants is every bank this system holds, read through the clearing
// house's own listing.
func participants(t *testing.T, h *server) []api.ParticipantDTO {
	t.Helper()
	var out []api.ParticipantDTO
	getJSON(t, cbSurface(h), "/members", &out)
	return out
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

	rec = postJSON(t, payerRoutes(t, srv), "/payments", validSubmission(t, srv))
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
	tooMuch := strings.Replace(validSubmission(t, srv), `"amount":1000`, `"amount":100000000`, 1)
	rec := postJSON(t, payerRoutes(t, srv), "/payments", tooMuch)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a payment the payer's own bank refuses answered %d, want 422 (body: %s)", rec.Code, rec.Body.String())
	}

	// Unanswerable: the IBAN is well-formed and belongs to nobody. Only the
	// payee's bank can know that, and it is not this request's to report.
	// Under VERDE's OWN bank code, so it routes: the payer's bank resolves the
	// code and sends, and the address is one Verde does not hold. An address under
	// some other code would be refused here instead, by the payer's own directory,
	// which is the case the test above is about.
	unknown := strings.Replace(validSubmission(t, srv), bellaIBAN, mustMint(iban.IT, "99999", 9999), 1)
	rec = postJSON(t, payerRoutes(t, srv), "/payments", unknown)
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
// Closing a cycle sends a pacs.009. There is no second console button that
// discharges it, and that is the whole of what "the mesh is wired into api"
// buys.
//
// What this test asserts, exactly: 200 with a Closed cycle carrying no
// settlement, then a drain, then — through the CENTRAL BANK's surface and only
// that one — the cycle read back as Settled and the settlement it names. Two of
// its four reads, one operator.
//
// The rest of the 6b read surface is covered where it was covered before, and
// deliberately not duplicated here: TestTheCentralBankCanReadTheCycleItSettles
// (surface_test.go) walks all four of the central bank's, and
// TestTheClearingHouseReadsTheSettlementItDidNotPerform walks the clearing
// house's side of the same settlement. This test is about the INSTRUCTION, not
// about who may read what.
func TestClosingACycleThroughTheAPIInstructsSettlement(t *testing.T) {
	srv, msh := newAPIHarness(t)

	rec := postJSON(t, payerRoutes(t, srv), "/payments", validSubmission(t, srv))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit = %d (body: %s)", rec.Code, rec.Body.String())
	}
	id := decodePaymentID(t, rec)
	drain(t, msh)

	cid := getPayment(t, srv, id).CycleID
	if cid == "" {
		t.Fatal("the accepted payment is in no cycle; there is nothing to close")
	}

	var closed api.ClearingCycleDTO
	crec := post(t, csmSurface(srv), "/cycles/"+cid+"/close")
	if crec.Code != http.StatusOK {
		t.Fatalf("close = %d (body: %s)", crec.Code, crec.Body.String())
	}
	if err := json.Unmarshal(crec.Body.Bytes(), &closed); err != nil {
		t.Fatalf("decoding the closed cycle: %v", err)
	}
	if closed.Status != "Closed" {
		t.Errorf("cycle status in the response = %q, want Closed", closed.Status)
	}
	drain(t, msh)

	// The cycle is read back from the CLEARING HOUSE, which is the institution
	// that has one: there is no cycles table in the central bank's schema, and
	// asking it is not a wrong answer but a missing table.
	var settled api.ClearingCycleDTO
	getJSON(t, csmSurface(srv), "/cycles/"+cid, &settled)
	if settled.Status != "Settled" {
		t.Fatalf("cycle is %q after draining, want Settled — the pacs.009 is what discharges it", settled.Status)
	}

	// And the settlement is found at the SETTLEMENT AGENT, by the cycle it names
	// rather than by an id the cycle names.
	//
	// The cycle carries no settlementId: the settlement's id is allocated inside
	// the settlement agent's own unit of work in its own database, and what comes
	// back to the clearing house is a pacs.002 quoting the CYCLE. So the link is
	// asserted from the end that can hold it. That closing a cycle does not settle
	// it is the Status above and the empty listing below.
	var settlements []api.SettlementDTO
	getJSON(t, cbSurface(srv), "/settlements", &settlements)
	var found api.SettlementDTO
	for _, st := range settlements {
		if st.CycleID == cid {
			found = st
		}
	}
	if found.ID == "" {
		t.Fatalf("the settlement agent lists no settlement against cycle %q", cid)
	}
	var st api.SettlementDTO
	getJSON(t, cbSurface(srv), "/settlements/"+found.ID, &st)
	if st.CycleID != cid {
		t.Errorf("settlement %s is against cycle %q, want %q", st.ID, st.CycleID, cid)
	}
}

// An operator's rejection is the clearing house's half and the payer's bank's
// half, and only the first one is in the response.
//
// The refund is another institution's act in another institution's book, and it
// happens when the pacs.002 gets there — so the 200 cannot describe a payer who
// has already been refunded.
//
// What this test can honestly assert is the RESPONSE — which describes the
// clearing house's half and only that — and the balance after the drain. It
// deliberately does not read the balance in between: the payer's bank actor runs
// concurrently with this goroutine, so "not yet refunded" would be a race dressed
// up as an assertion. mesh's own
// TestAnOperatorRejectionRefundsThePayerOnlyOnceTheMessageArrives measures which
// actor posted the refund, without a clock, and
// TestARejectionWhoseRefundFailsStandsAndIsDeadLettered pins that the two halves
// are not one unit of work.
func TestRejectingThroughTheAPIRefundsThePayerOnlyAfterTheMessageArrives(t *testing.T) {
	srv, msh := newAPIHarness(t)

	rec := postJSON(t, payerRoutes(t, srv), "/payments", validSubmission(t, srv))
	id := decodePaymentID(t, rec)
	drain(t, msh)
	before := aliceBalance(t, srv)

	rrec := postJSON(t, csmSurface(srv), "/payments/"+id+"/reject", `{"reason":"card lost"}`)
	if rrec.Code != http.StatusAccepted {
		t.Fatalf("reject = %d (body: %s)", rrec.Code, rrec.Body.String())
	}
	var rejected api.PaymentDTO
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
// The distinction this pins is not "did the money come back" — a synchronous
// domain call would do that too. It is WHO did it and WITH WHAT. Mesh.Return
// hands the instruction to the bank that RECEIVED the original — the payee's bank
// on a push — which posts its own clawback and sends a pacs.004; the refund
// asserted below is a DIFFERENT bank's posting, made from that same message after
// the settlement agent has reversed the reserves, and the reason travels the
// whole way as a code and a text. The refund's own description in the payer's
// ledger is where both surface.
//
// The payment is a SETTLED one out of the seeded dataset, because finality is a
// return's precondition: PostReturnLegTx refuses anything else, and the
// returning bank checks it again before any message exists.
func TestReturningThroughTheAPIGoesRoundTheMesh(t *testing.T) {
	srv, msh := newAPIHarness(t)

	var payments []api.PaymentDTO
	getJSON(t, csmSurface(srv), "/payments", &payments)
	var settled api.PaymentDTO
	for _, p := range payments {
		if p.Status == "Settled" && p.Scheme == "sepa.ct" {
			settled = p
			break
		}
	}
	if settled.ID == "" {
		t.Fatal("the seeded dataset holds no settled credit transfer to return")
	}

	rec := postJSON(t, csmSurface(srv), "/payments/"+settled.ID+"/return", `{"reason":"account closed"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("return = %d (body: %s)", rec.Code, rec.Body.String())
	}
	// An identifier and nothing else. There is no intermediate resource to
	// describe: the returning bank has posted its own leg, but the payment is
	// still Settled — the return is not finished until the other bank posts, and
	// that happens after this response is written.
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
	// A handler that called the domain directly would describe it with the text
	// alone: there would have been no message to put a code on.
	var txns []api.TransactionDTO
	getJSON(t, bankSurface(srv, settled.DebtorAgent), "/transactions", &txns)
	want := settled.ID + ":return-refund"
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
func aliceBalance(t *testing.T, s *server) int64 {
	t.Helper()
	aliceBIC, alice := seededParty(t, s, aliceIBAN)
	bal := doJSON(t, bankSurface(s, string(aliceBIC)), "GET",
		"/deposit-accounts/"+string(alice.Account)+"/balance", "", http.StatusOK)
	return int64(bal["book"].(float64))
}
