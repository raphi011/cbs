package api

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

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
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
// that is not synchronous: a request returns while three other actors are still
// working, and the answer to "what happened" arrives at a different actor
// afterwards. No test here waits for a duration to find out. mesh.Drain blocks
// until nothing is in flight, and that is the only way any of them looks.

// newAPIHarness is a seeded Server with a mesh running over the same network.
//
// Seeded, because the mesh routes by BIC: a payment needs two banks that can
// address each other, an open cut-off window for its scheme, and a payer with
// money. seed.Populate is the dataset the running system serves, so these tests
// exercise the same rows a reader will see in the app. They name them by IBAN
// rather than by id — see seededParty.
//
// The mesh is started BEFORE the seed, which is the order cmd/server uses and
// for the same reason: the seed admits its banks through the mesh's own door, so
// the two institutions that answer an application have to be running before
// there is an application to answer. mesh.Start's roster read finds nothing here
// — the banks it would have registered are the ones Populate is about to admit,
// and each of those gets its actor from Mesh.Admit as it is founded.
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
	nets := payment.NewNetworks(testenv.NewSet(t, data.Now), data.Now)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := testMeshConfig
	gate := newMeshGate()
	cfg.Observe = gate.observe
	msh, err := mesh.New(nets, cfg, log)
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

	// Seeded LAST, and after both cleanups are registered: the seed's four
	// admissions are conversations through this mesh, so a Populate that failed
	// halfway would otherwise leave actor goroutines nobody joins. They pass the
	// gate like every other message and go straight through, because no test has
	// run yet and so no gate is held.
	if err := data.Populate(ctx, nets, msh); err != nil {
		t.Fatalf("populate: %v", err)
	}
	// Bound to the CLEARING HOUSE, which is what makes s.network() answer at all:
	// NewServer returns an unbound Server, and the reads these tests make of it —
	// payments, cycles, bank rows — are that institution's. A test that wants a
	// bank's surface calls s.forBank, exactly as BankRoutes does.
	return NewServer(nets, msh, data.Populate, log).as(nets.ClearingHouse()), msh
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

// seededParty is the participant and account behind one of the seed's IBANs.
//
// The ids are not written down. Every id in a book comes from one counter, so an
// act that allocates one more than it used to moves every id after it — and the
// seed builds the same things in the same order only for as long as nobody adds
// an act.
//
// An IBAN is the seed's own stable name for a customer and is what these tests
// ask by. It is stable for a reason that will outlast this task: the seed CHOSE
// it, where an id is whatever the counter had reached.
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
func seededParty(t *testing.T, s *Server, iban string) (iso20022.BIC, payment.PartyRef) {
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
func payerRoutes(t *testing.T, s *Server) http.Handler {
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
// addresses now: these are the allocations the seed founds those two banks
// under and the order it opens their accounts in, which is the only thing about
// them a test can depend on. A literal would say what the check digits are
// today and go stale silently the first time an account is inserted above.
//
// Note that they are in different countries, of different lengths, with the
// bank code at a different offset in each. That is the seed's point, and it is
// what makes the routing directory a lookup rather than a substring.
var (
	aliceIBAN = mustMint(iban.DE, "99900001", 1) // Aurora Bank's first account
	bellaIBAN = mustMint(iban.IT, "99991", 2)    // Banca Verde's second
)

func mustMint(c iban.Country, code iban.BankCode, serial uint64) string {
	a, err := iban.New(c, code, serial)
	if err != nil {
		panic(err)
	}
	return string(a)
}

// validSubmission is Alice at Aurora Bank paying Bella at Banca Verde: a credit
// transfer the seeded network can carry the whole way.
//
// The IBANs are quoted in the body as well as used to look the ids up, because
// SEPA credit transfer is addressed BY iban (payment.Scheme.AddressedBy):
// without them the payee's bank has no address to resolve and answers AC01.
//
// The amount is small enough that Alice can afford it whatever else the seeded
// scenario has already taken out of her account.
func validSubmission(t *testing.T, s *Server) string {
	t.Helper()
	payerBIC, payer := seededParty(t, s, aliceIBAN)
	payeeBIC, payee := seededParty(t, s, bellaIBAN)
	// creditorAgent, because the instruction carries the routing element — see
	// api's initiatePaymentRequest and payment.SubmitPaymentTx. The fixture reads
	// it off the payee's bank the way a payer reads it off an invoice; nothing on
	// the submitting path looks it up.
	//
	// A party object carries no "participant": which bank a side is at is the
	// agent. See api.partyRefDTO.
	return fmt.Sprintf(`{
		"scheme":"sepa.ct",
		"debtor":{"account":%q,"identifier":{"scheme":"IBAN","value":%q}},
		"creditor":{"account":%q,"identifier":{"scheme":"IBAN","value":%q}},
		"amount":1000,
		"description":"mesh handoff",
		"creditorName":"Bella Bruno",
		"debtorAgent":%q,
		"creditorAgent":%q
	}`,
		payer.Account, aliceIBAN,
		payee.Account, bellaIBAN,
		payerBIC, payeeBIC)
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
	var before paymentDTO
	getJSON(t, payerRoutes(t, srv), "/payments/"+id, &before)
	if before.Status != "Initiated" {
		t.Errorf("status before draining = %q, want Initiated", before.Status)
	}
	assertStatus(t, csm(srv), "GET", "/payments/"+id, "", http.StatusNotFound)

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

// TestAdmissionAnswers202WithAFoundedBank pins what POST /members says about a
// bank that has applied and not yet been answered.
//
// Three things, and they are one claim seen three ways. The status code is 202
// rather than 201, because the resource an operator is asking for — a member of
// the scheme — does not exist yet and this call cannot make it exist. The DTO
// says Founded, which is the only field that distinguishes the two states. And
// the settlement account, which is the central bank's to open, is EMPTY in that
// answer and filled in once the conversation has finished.
//
// The reserves routes are the other half of the same moment. A founded bank has
// no settlement account, so ReserveBalance answers ErrSettlementMemberNotFound —
// and returning that for the whole participant would be a 500 for a bank in the
// state this system creates on every admission. It reports NO ROW instead. Not a
// zero: zero is a balance, and there is no account to have one.
//
// # It HOLDS the bank in Founded
//
// That version read the reserves straight after the 202 and depended on three
// actor goroutines not having finished yet. It passed almost always and it was
// not evidence: under GOMAXPROCS=2 at -count=40 it failed about once a run, with
// `a founded bank reports [map[asset:EUR reserve:0 …]]` — the conversation
// having completed between the POST and the GET, which is the system working. A
// test that asserts the headline behaviour of a task must not be able to pass
// because a background actor was slow.
//
// So the central bank's door is HELD. Its acmt.007 cannot be answered, the
// acknowledgement that fills in the settlement reference cannot arrive, and the
// bank is Founded for as long as this test wants it to be — provably, not
// probably. Opening the gate and draining is then the other half of the same
// measurement: the same three reads, after the conversation, all changed.
//
// It runs on the SEEDED harness, which the first version could not, and that is
// worth having: the list route has four members to report while this applicant
// is in it, so "passes over the applicant" is a real claim about a mixed network
// rather than a statement about an empty one.
func TestAdmissionAnswers202WithAFoundedBank(t *testing.T) {
	srv, msh := newAPIHarness(t)
	open := holdMessagesTo(t, msh, testMeshConfig.CentralBankBIC)
	defer open()

	members := reserveRows(t, srv, "")

	founded := doJSON(t, cb(srv), "POST", "/members", `{"bic":"BNKZDEFFXXX","name":"Bank Z","country":"DE","bankCode":"90000850"}`, http.StatusAccepted)
	pid := founded["id"].(string)
	assertEqual(t, "status in the 202", founded["status"].(string), "Founded")
	assets := founded["assets"].([]any)
	if len(assets) != 1 {
		t.Fatalf("a euro bank came back with %d asset rows, want 1", len(assets))
	}
	assertEqual(t, "settlement account in the 202",
		assets[0].(map[string]any)["settlement"].(string), "")

	// Nothing to report about it, and a 422 saying so rather than an empty list.
	//
	// An empty list would be a claim this institution cannot make: it presumes the
	// settlement agent knows of a bank and holds no accounts for it. The agent's own
	// register is the whole of what it knows — there is no banks table for it to
	// look the applicant up in — so a BIC it has admitted nobody at is not a member
	// with zero reserves but a member it has never heard of. See handleGetReserve.
	assertStatus(t, cb(srv), "GET", "/reserves/"+pid, "", http.StatusUnprocessableEntity)
	// And the list carries the members it has, with no row and no failure for
	// the applicant.
	if got := reserveRows(t, srv, ""); len(got) != len(members) {
		t.Errorf("the reserve list went from %d rows to %d while a bank was mid-admission",
			len(members), len(got))
	}
	for _, row := range reserveRows(t, srv, "") {
		if row["participant"] == pid {
			t.Errorf("the list reports %v for a bank the central bank holds no account for", row)
		}
	}

	// Opened before anything waits on the mesh, which is this file's one rule.
	open()
	drainServer(t, srv)

	member := doJSON(t, bank(srv, pid), "GET", "/me", "", http.StatusOK)
	assertEqual(t, "status after the conversation", member["status"].(string), "Member")
	if settlement := member["assets"].([]any)[0].(map[string]any)["settlement"].(string); settlement == "" {
		t.Error("the bank is a Member and carries no settlement account")
	}
	got := reserveRows(t, srv, pid)
	if len(got) != 1 || got[0]["asset"] != "EUR" {
		t.Fatalf("a member's reserves = %v, want one EUR row", got)
	}
	assertEqual(t, "the reserve of a member nobody has funded",
		int64(got[0]["reserve"].(float64)), int64(0))
	if after := reserveRows(t, srv, ""); len(after) != len(members)+1 {
		t.Errorf("the reserve list has %d rows once the admission finished, want %d",
			len(after), len(members)+1)
	}
}

// reserveRows reads the central bank's reserve report: the whole list when pid
// is empty, one bank's when it is not.
func reserveRows(t *testing.T, s *Server, pid string) []map[string]any {
	t.Helper()
	path := "/reserves"
	if pid != "" {
		path += "/" + pid
	}
	var out []map[string]any
	getJSON(t, cb(s), path, &out)
	return out
}

// TestABankTheSchemeHasNotAnsweredForCanTakeCashAndCannotLodgeIt is the successor
// to TestFundingABankTheSchemeHasNotAnsweredForIsRefusedByName, and it is a
// REVERSAL rather than a rename.
//
// That test asserted a 422 on POST /deposits: funding was the one operation that
// reached across to the settlement agent — cash paid in raised the customer's
// balance and the bank's reserve in one unit of work — so a bank with no
// settlement account had nowhere for the second leg to land, and the refusal
// named membership rather than coming back 404 about the customer's account.
//
// A bank's counter has nothing to do with its central bank account: a bank that
// has founded itself and joined no scheme can open its doors and take money, and
// it holds that money as vault cash. The refusal belongs on the LODGEMENT.
//
// So this asserts BOTH halves at once, which is what makes it a reversal and not
// two tests:
//
//   - POST /deposits succeeds, 200, with the customer's balance to show for it.
//   - POST /lodgements is refused, 422, with a sentence about the reserve account
//     this bank cannot name.
//
// The bank is provably still Founded while this runs: the central bank's door is
// held, so the acmt.007 cannot be answered and the acknowledgement that fills in
// the settlement reference cannot arrive. Without the gate this would be a race
// against three actors, and one that mostly loses.
func TestABankTheSchemeHasNotAnsweredForCanTakeCashAndCannotLodgeIt(t *testing.T) {
	srv, msh := newAPIHarness(t)
	open := holdMessagesTo(t, msh, testMeshConfig.CentralBankBIC)
	defer open()

	p := doJSON(t, cb(srv), "POST", "/members", `{"bic":"BNKZDEFFXXX","name":"Bank Z","country":"DE","bankCode":"90000850"}`, http.StatusAccepted)
	pid := p["id"].(string)
	if got := doJSON(t, bank(srv, pid), "GET", "/me", "", http.StatusOK)["status"].(string); got != "Founded" {
		t.Fatalf("the bank is %q; this test is about one the scheme has not answered for", got)
	}

	// A customer account it can open, and money it CAN now take in.
	acct := doJSON(t, bank(srv, pid), "POST", "/deposit-accounts",
		`{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, srv, pid)+`"}`, http.StatusCreated)["id"].(string)
	bal := doJSON(t, bank(srv, pid), "POST", "/deposits",
		`{"account":"`+acct+`","amount":1000,"description":"opening"}`, http.StatusOK)
	if got := int64(bal["book"].(float64)); got != 1000 {
		t.Errorf("the depositor's balance is %d, want 1000; a founded bank can take cash", got)
	}

	// And the cash it cannot place on reserve, because there is no reserve
	// account to place it in.
	rec := do(t, bank(srv, pid), "POST", "/lodgements", `{"asset":"EUR","amount":1000}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("lodging at a founded bank = %d, want 422 (body: %s)", rec.Code, rec.Body)
	}
	if msg := rec.Body.String(); !strings.Contains(msg, "holds no reserve account") {
		t.Errorf("the refusal does not say the bank has no reserve account to lodge into: %s", msg)
	}

	// Once the scheme has answered, the lodgement goes through — 202, because the
	// reserve credit is the central bank's to make and is still on the wire.
	open()
	drainServer(t, srv)
	out := doJSON(t, bank(srv, pid), "POST", "/lodgements", `{"asset":"EUR","amount":1000}`, http.StatusAccepted)
	if out["ref"].(string) == "" {
		t.Error("the accepted lodgement carries no reference, so nothing correlates its receipt")
	}
	if got := out["agent"].(string); got != string(testMeshConfig.CentralBankBIC) {
		t.Errorf("the lodgement names %q as the agent, want the central bank %q", got, testMeshConfig.CentralBankBIC)
	}

	// The reserve is real once the conversation has finished, which is what says
	// the central bank posted its own half rather than merely answering.
	drainServer(t, srv)
	rows := reserveRows(t, srv, pid)
	if len(rows) != 1 {
		t.Fatalf("a member's reserves = %v, want one row", rows)
	}
	if got := int64(rows[0]["reserve"].(float64)); got != 1000 {
		t.Errorf("the reserve after the lodgement is %d, want 1000", got)
	}
}

// TestAFoundedBankIsRefusedAsAPaymentPartyInEitherDirection is what a founded
// bank means to this API on the payments surface, and it is the half that used
// to answer 202.
//
// Both directions were measured before the guard existed. Being paid: POST
// /payments to a founded bank answered 202 and the payment reached Cleared.
// Paying: an ARRANGED OVERDRAFT — the field this very request carries — gives a
// founded bank's customer spendable money without any deposit, so its submission
// was accepted too and posted a debtor leg. Both ended at the same cut-off
// failure, which stranded every other member's payments in the cycle.
//
// 422 in both directions now, from the same door mesh.ErrOnUsPayment is refused
// at, and the state is temporary in exactly the way the funding refusal above is
// temporary: the same two instructions go through once the scheme has answered.
// That is why the central bank's door is held rather than raced — without the
// gate the bank would usually be a Member by the time the first request landed.
func TestAFoundedBankIsRefusedAsAPaymentPartyInEitherDirection(t *testing.T) {
	srv, msh := newAPIHarness(t)
	open := holdMessagesTo(t, msh, testMeshConfig.CentralBankBIC)
	defer open()

	pid := doJSON(t, cb(srv), "POST", "/members", `{"bic":"BNKZDEFFXXX","name":"Bank Z","country":"DE","bankCode":"90000850"}`,
		http.StatusAccepted)["id"].(string)
	if got := doJSON(t, bank(srv, pid), "GET", "/me", "", http.StatusOK)["status"].(string); got != "Founded" {
		t.Fatalf("the bank is %q; this test is about one the scheme has not answered for", got)
	}
	acct := doJSON(t, bank(srv, pid), "POST", "/deposit-accounts",
		`{"name":"Nora","asset":"EUR","productId":"`+prdOf(t, srv, pid)+`","overdraftLimit":100000}`,
		http.StatusCreated)["id"].(string)
	// Bank Z minted Nora's address when it opened her account, exactly as an
	// admitted bank would: founding is what gives a bank a bank code, and
	// joining a scheme is a separate thing it has not done.
	zIBAN := ibanFor(t, srv, pid, acct)

	aliceBIC, alice := seededParty(t, srv, aliceIBAN)
	party := func(ref payment.PartyRef, iban string) string {
		return fmt.Sprintf(`{"account":%q,"identifier":{"scheme":"IBAN","value":%q}}`,
			ref.Account, iban)
	}
	nora := payment.PartyRef{Account: deposit.AccountID(acct)}
	toBankZ := fmt.Sprintf(`{"scheme":"sepa.ct","debtor":%s,"creditor":%s,"amount":1000,`+
		`"description":"to a bank no scheme has admitted","creditorName":"Nora","creditorAgent":%q}`,
		party(alice, aliceIBAN), party(nora, zIBAN), "BNKZDEFFXXX")
	fromBankZ := fmt.Sprintf(`{"scheme":"sepa.ct","debtor":%s,"creditor":%s,"amount":1000,`+
		`"description":"from a bank no scheme has admitted","creditorName":"Alice Andersson","creditorAgent":%q}`,
		party(nora, zIBAN), party(alice, aliceIBAN), aliceBIC)

	directions := []struct {
		name  string
		by    http.Handler
		body  string
		names string
	}{
		{"paid", payerRoutes(t, srv), toBankZ, "payee's bank"},
		{"paying", bank(srv, pid), fromBankZ, "payer's bank"},
	}
	for _, tc := range directions {
		rec := postJSON(t, tc.by, "/payments", tc.body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("a founded bank %s = %d, want 422 (body: %s)", tc.name, rec.Code, rec.Body)
		}
		if msg := rec.Body.String(); !strings.Contains(msg, "not a member") || !strings.Contains(msg, tc.names) {
			t.Errorf("the refusal for %q does not name the %s: %s", tc.name, tc.names, msg)
		}
	}

	// Once the scheme has answered, both go through — which is what says this
	// refusal is about a state and not about the request.
	open()
	drain(t, msh)
	for _, tc := range directions {
		if rec := postJSON(t, tc.by, "/payments", tc.body); rec.Code != http.StatusAccepted {
			t.Errorf("after the admission, %s = %d, want 202 (body: %s)", tc.name, rec.Code, rec.Body)
		}
	}
	drain(t, msh)
}

// TestAPaymentNamingAParticipantThatDoesNotExistIsNotFound is a status code
// Mesh.Submit's membership guard changed on its way past, kept because nothing
// else holds it.
//
// A payment naming a bank that HAS NO ROW is a different request from the one the
// test above is about: that bank is founded and waiting for a scheme, this one
// was never founded and the id is a typo.
//
// A request names its parties by the very key the roster is keyed by, so there is
// no bank row in the way — see payment.Network.GetRosterEntryByBIC. An address
// nobody has been admitted on is ErrBankNotAdmitted, which is a 422.
//
// That is the more honest of the two answers rather than a regression. The
// clearing house has never been able to tell "no such institution" from "an
// institution I have not admitted": both are an address absent from its roster,
// and the only thing that could once distinguish them was a read into a database
// belonging to somebody else. What is lost is a distinction the reader was not
// entitled to make.
//
// So what is asserted here is still an ORDER and not a mapping. The membership
// read runs BEFORE the actor lookup, and anything that moves it later restores
// the bare "no bank actor" error, which has no case in errorStatus and comes back
// 500. Measured both ways: with the guard, 422; with it removed, 500.
//
// It is asked of the clearing house's console because that is the only surface
// that can ask it. A bank's own POST /payments is bound to one bank and fills its
// own side in from that binding, so it has no way to name a stranger as the payer.
func TestAPaymentNamingABankNoSchemeHasAdmittedIsUnprocessable(t *testing.T) {
	srv, _ := newAPIHarness(t)
	payerBIC, _ := seededParty(t, srv, aliceIBAN)
	body := strings.Replace(validSubmission(t, srv), string(payerBIC), "NOSUCHBKXXX", 1)
	rec := postJSON(t, csm(srv), "/payments", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a payment whose payer's bank is in no roster = %d, want 422 (body: %s)", rec.Code, rec.Body)
	}
}

// TestAReseededNetworkCanStillBePaidThrough is the guarantee that moved when
// Reset stopped calling JoinRoster.
//
// A reset forgets every bank actor, truncates and reseeds. The actors are not put
// back in a step of the reset's own: the reseed admits its banks through the mesh,
// so each one gets its actor in the call that founds it. That is a better division
// of labour and a weaker guarantee, because nothing in api can check that a reseed
// did it. This is what checks it, from the outside, in the only way that matters:
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
// This is the sequence the reviewer walked over HTTP and it failed at step 4:
// admit a bank, reset, admit the same BIC again. The mesh had never been told
// the first bank was gone, so an actor still answered to that address, the
// re-admission was refused, and the participant row was left behind anyway. The
// BIC was unusable for the life of the process, on a system whose roster was
// empty.
//
// Every assertion below is one of the three things that were wrong: the
// re-admission is accepted, the bank that comes back can actually be paid
// through — which is the part the 202 alone would not prove, since a row with no
// actor answers every read and carries no payment — and the refusal that IS
// still reachable says something true.
func TestResetRebuildsTheMeshSoAReadmittedBankCanPay(t *testing.T) {
	h := newServer(t, nil)

	admit := func(name, bic string, want int) map[string]any {
		t.Helper()
		return admitMember(t, h, `{"bic":"`+bic+`","name":"`+name+`"}`, want)
	}
	admit("Bank A", "BNKADEFFXXX", http.StatusAccepted)
	admit("Bank B", "BNKBDEFFXXX", http.StatusAccepted)

	assertStatus(t, cb(h), "POST", "/admin/reset", "", http.StatusOK)

	var members []participantDTO
	getJSON(t, cb(h), "/members", &members)
	if len(members) != 0 {
		t.Fatalf("the roster holds %d banks after a reset with no reseed, want 0", len(members))
	}

	// The same two addresses, on a system that has never heard of them.
	a := admit("Bank A again", "BNKADEFFXXX", http.StatusAccepted)["id"].(string)
	b := admit("Bank B again", "BNKBDEFFXXX", http.StatusAccepted)["id"].(string)

	alice := doJSON(t, bank(h, a), "POST", "/deposit-accounts",
		`{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`"}`,
		http.StatusCreated)["id"].(string)
	bob := doJSON(t, bank(h, b), "POST", "/deposit-accounts",
		`{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`,
		http.StatusCreated)["id"].(string)
	doJSON(t, bank(h, a), "POST", "/deposits", `{"account":"`+alice+`","amount":100000,"description":"opening"}`, http.StatusOK)
	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	// The whole point: these banks are routable in both directions. A stale
	// actor would have made this "no bank actor", and an actor that was never
	// registered would have made it RC01 at the clearing house.
	pay := doJSON(t, bank(h, a), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtorAgent":"`+a+`","debtor":{"account":"`+alice+`"},
		"creditorAgent":"`+b+`","creditor":{"account":"`+bob+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, b, bob)+`"}},
		"amount":25000,
		"creditorName":"Bob"
	}`, http.StatusAccepted)["paymentId"].(string)
	drainServer(t, h)
	if got := doJSON(t, csm(h), "GET", "/payments/"+pay, "", http.StatusOK); got["status"].(string) != "Accepted" {
		t.Fatalf("a payment between two re-admitted banks is %q, want Accepted", got["status"])
	}

	// And the refusal that is reachable — a third bank on an address one of these
	// already answers to — says what is true of it. Nothing is half-happened: the
	// address is claimed before anything is written, so a clash costs one error and
	// no row. The remedy is to pick an address of your own, which is the real-world
	// fix as much as this system's.
	before := len(participants(t, h))
	rec := do(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A yet again","country":"DE","bankCode":"90000825"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("admitting a third bank on Bank A's BIC = %d, want 422", rec.Code)
	}
	msg := rec.Body.String()
	if !strings.Contains(msg, "nothing was written") {
		t.Errorf("the refusal reads %q; the whole of what changed is that nothing was written", msg)
	}
	if !strings.Contains(msg, "admit this bank on an address of its own") {
		t.Errorf("the refusal reads %q; a clashing address has a remedy and it is missing", msg)
	}
	if after := len(participants(t, h)); after != before {
		t.Errorf("a refused admission wrote %d bank row(s); the address is claimed before the row is", after-before)
	}
	// The bank that owns the address is untouched by the refusal, which is the
	// half the old message got wrong twice over: it called the ADMITTED bank
	// unroutable while the clash's owner went on working.
	var after []paymentDTO
	getJSON(t, bank(h, a), "/payments", &after)
	if len(after) != 1 {
		t.Errorf("Bank A sees %d of its own payments after the refusal, want 1", len(after))
	}
}

// Admission during a shutdown is refused WITHOUT the remedy, and the two
// refusals are still worth telling apart — for a reason that has changed under
// them.
//
// The advice matters as much as the refusal: "admit it on a BIC no other bank
// answers to" is the fix for a clash and is actively harmful here, because the
// operator who follows it gets the same refusal.
//
// Neither branch leaves anything behind now. The address is claimed before the
// bank is written, so a shutdown refuses before there is anything to orphan —
// which is what this asserts, and which is what makes the advice's absence a
// matter of accuracy rather than of harm. It is a 500 rather than a 422 because
// it is not a statement about the request: the same request would have worked a
// moment earlier and will work again on a mesh that is running. A clash is the
// caller's to fix and says 422; a mesh going down is not.
func TestAdmissionDuringAShutdownIsRefusedWithoutTheRemedy(t *testing.T) {
	h := newServer(t, nil)

	// The mesh's cleanup will Stop it again; a second Stop only re-joins, and on
	// a mesh that is already stopped it does nothing at all.
	if err := h.mesh.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	rec := do(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A","country":"DE","bankCode":"90000825"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("admitting a bank into a stopped mesh = %d, want 500 (body: %s)", rec.Code, rec.Body.String())
	}
	msg := rec.Body.String()
	if !strings.Contains(msg, "stopping") {
		t.Errorf("the refusal reads %q; it must name the mesh going down as what refused", msg)
	}
	if strings.Contains(msg, "address of its own") {
		t.Errorf("the refusal reads %q; that advice is for a clashing address and there is none here", msg)
	}
	// And nothing was written: an admission the mesh refuses never reaches the
	// store.
	if n := len(participants(t, h)); n != 0 {
		t.Errorf("a refused admission left %d bank row(s) behind", n)
	}
}

// participants is every bank this system holds, read through the clearing
// house's own listing.
func participants(t *testing.T, h *Server) []participantDTO {
	t.Helper()
	var out []participantDTO
	getJSON(t, cb(h), "/members", &out)
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
	unknown := strings.Replace(validSubmission(t, srv), bellaIBAN, mustMint(iban.IT, "99991", 9999), 1)
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
// (api/surface_test.go) walks all four of the central bank's, and
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
	drain(t, msh)

	// The cycle is read back from the CLEARING HOUSE, which is the institution
	// that has one: there is no cycles table in the central bank's schema, and
	// asking it is not a wrong answer but a missing table.
	var settled clearingCycleDTO
	getJSON(t, csm(srv), "/cycles/"+cid, &settled)
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
	var settlements []settlementDTO
	getJSON(t, cb(srv), "/settlements", &settlements)
	var found settlementDTO
	for _, st := range settlements {
		if st.CycleID == cid {
			found = st
		}
	}
	if found.ID == "" {
		t.Fatalf("the settlement agent lists no settlement against cycle %q", cid)
	}
	var st settlementDTO
	getJSON(t, cb(srv), "/settlements/"+found.ID, &st)
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
	var txns []transactionDTO
	getJSON(t, bank(srv, settled.DebtorAgent), "/transactions", &txns)
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
func aliceBalance(t *testing.T, s *Server) int64 {
	t.Helper()
	aliceBIC, alice := seededParty(t, s, aliceIBAN)
	bal := doJSON(t, bank(s, string(aliceBIC)), "GET",
		"/deposit-accounts/"+string(alice.Account)+"/balance", "", http.StatusOK)
	return int64(bal["book"].(float64))
}
