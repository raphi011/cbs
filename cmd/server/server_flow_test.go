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
	"testing"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/calendar"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/seed"
	"github.com/raphi011/cbs/store/testenv"
)

// What this file is for: the seam between HTTP and the business day.
//
// Every other file here drives the three surfaces over a store. These drive one over
// a store AND the real transport, because what they are about is what is not answered
// synchronously: a request returns while the file it produced is still in another
// institution's order log. Nothing runs in the background, so "later" means "the next
// time the day is carried".

// newAPIHarness is a seeded deployment with both hosts really listening. Seeded,
// because a payment needs two banks that can address each other, an open cut-off
// window for its scheme, and a payer with money — and seed.Populate is the dataset
// the running system serves, so these tests exercise the same rows a reader sees in
// the app. They name them by IBAN rather than by id.
//
// The listeners are bound BEFORE the seed, the order cmd/server uses: the seed admits
// its banks through this deployment's own doors. Under a synchronous day a payment
// submitted and not yet carried is PROVABLY still Initiated, so there is no gate and
// nothing to hold still.
func newAPIHarness(t *testing.T) *server {
	t.Helper()
	ctx := context.Background()

	clock := calendar.NewClock(seed.BaseDate)
	data := seed.New(clock)
	set := testenv.NewSet(t, clock.Now)
	nets := payment.NewNetworks(set, clock.Now)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	s := &server{nets: nets, clock: clock}
	csmHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.dep.ClearingHouse().EBICS().ServeHTTP(w, r)
	}))
	cbHost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.dep.CentralBank().EBICS().ServeHTTP(w, r)
	}))
	t.Cleanup(csmHost.Close)
	t.Cleanup(cbHost.Close)

	cfg := testConfig
	cfg.ClearingHouseURL = csmHost.URL
	cfg.CentralBankURL = cbHost.URL

	dep, err := NewDeployment(ctx, nets, set, clock, cfg, data.Populate, log)
	if err != nil {
		t.Fatalf("NewDeployment: %v", err)
	}
	s.dep = dep

	if err := data.Populate(ctx, nets, dep); err != nil {
		t.Fatalf("populate: %v", err)
	}
	return s
}

// seededParty is the participant and account behind one of the seed's IBANs.
//
// The ids are not written down: every id in a book comes from one counter, so an act
// that allocates one more moves every id after it. An IBAN is the seed's own stable
// name for a customer, stable for a durable reason — the seed CHOSE it, where an id
// is whatever the counter had reached.
//
// It SWEEPS, and has to do the sweeping itself: no bank can answer "whose IBAN is
// this", so this helper asks each bank about its own register in turn. That is the
// harness standing outside the network, which a test may do and no institution may.
//
// It returns the BANK as well as the ref, because a payment.PartyRef names none and
// every caller needs both.
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
// Derived rather than written out, because a bank MINTS its customers' addresses:
// these are the allocations the settlement agent gives those two banks and the order
// the seed opens their accounts in. A literal would say what the check digits are
// today and go stale silently the first time an account is inserted above.
//
// The CODES are still written out and are the one thing that could drift: a registry
// allocates from the top of each country's range downwards, so the first bank
// admitted in Germany gets 99999999 and the first in Italy 99999. They are in
// different countries, of different lengths, with the bank code at a different offset
// in each — which is what makes the routing directory a lookup rather than a
// substring.
var (
	aliceIBAN = mustMint(iban.DE, "99999999", 1) // Aurora Bank's first account
	bellaIBAN = mustMint(iban.IT, "99999", 2)    // Banca Verde's second
)

// validSubmission is Alice at Aurora Bank paying Bella at Banca Verde: a credit
// transfer the seeded network can carry the whole way. The IBANs are quoted in the
// body as well as used to look the ids up, because SEPA credit transfer is addressed
// BY iban — without them the payee's bank has no address to resolve and answers AC01.
// The amount is small enough that Alice can afford it whatever else the scenario has
// taken out of her account.
func validSubmission(t *testing.T, s *server) string {
	t.Helper()
	_, payer := seededParty(t, s, aliceIBAN)
	_, payee := seededParty(t, s, bellaIBAN)
	// NO agent on either side, and none to give: the instruction carries an address
	// and a name, and the payer's bank derives the routing element from the address
	// through its own copy of the scheme's directory. A party object carries no
	// "participant" either — which bank a side is at is something this request never
	// says.
	return fmt.Sprintf(`{
		"scheme":"sepa.ct",
		"debtor":{"account":%q,"identifier":{"scheme":"IBAN","value":%q}},
		"creditor":{"account":%q,"identifier":{"scheme":"IBAN","value":%q}},
		"amount":1000,
		"description":"interbank handoff",
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

// 202 was already the shape 6a chose, because a real CSM answers later. Now it
// is true rather than anticipatory: the payment really is Initiated when the
// response is written.
func TestSubmitAnswers202AndThePaymentIsNotYetAccepted(t *testing.T) {
	srv := newAPIHarness(t)
	// Nothing holds the clearing house still, and nothing needs to: no file has
	// been built, because the instruction is in the payer's bank's hub and a hub
	// empties at a cut-off. The payment is Initiated because the only thing that
	// would change that is the call this test has not made yet.
	rec := postJSON(t, payerRoutes(t, srv), "/payments", validSubmission(t, srv))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	id := decodePaymentID(t, rec)

	// Read on the PAYER'S BANK's surface, because until the file is carried that bank
	// is the only institution with a row for this payment at all: the instruction has
	// not been sent, so the clearing house answers 404 rather than "Initiated". Which
	// is the same claim in a stronger form.
	var before api.PaymentDTO
	getJSON(t, payerRoutes(t, srv), "/payments/"+id, &before)
	if before.Status != "Initiated" {
		t.Errorf("status before the file is carried = %q, want Initiated", before.Status)
	}
	assertStatus(t, csmSurface(srv), "GET", "/payments/"+id, "", http.StatusNotFound)

	carry(t, srv)

	after := getPayment(t, srv, id)
	if after.Status != "Accepted" {
		t.Errorf("status after the file is carried = %q, want Accepted", after.Status)
	}
}

// The two routes the hub added, over HTTP, on the bank's own port: what is waiting,
// and the act that sends it. The assertion that matters is the ORDER ID — a cut-off
// answers with a receipt from the transport and not with an outcome, because what the
// clearing house makes of the file comes back on a later download.
func TestPendingAndCutoffOnABanksOwnPort(t *testing.T) {
	srv := newAPIHarness(t)
	payer := payerRoutes(t, srv)

	id := decodePaymentID(t, postJSON(t, payer, "/payments", validSubmission(t, srv)))
	pending := doJSONArray(t, payer, "GET", "/payments/pending", "", http.StatusOK)
	if len(pending) != 1 {
		t.Fatalf("the hub is holding %d instructions after one submission, want 1", len(pending))
	}
	if got := pending[0].(map[string]any)["id"]; got != id {
		t.Errorf("the hub is holding %v, want the payment the 202 named (%s)", got, id)
	}

	out := doJSON(t, payer, "POST", "/payments/cutoff", "", http.StatusAccepted)
	orders, _ := out["orderIds"].([]any)
	if len(orders) != 1 {
		t.Fatalf("the cut-off answered with %v, want one order id", out["orderIds"])
	}
	if s, _ := orders[0].(string); s == "" {
		t.Errorf("the cut-off answered with an empty order id; an upload is acknowledged by one")
	}
	// Emptied, and a second cut-off is not an error — it is a bank with nothing
	// to send, which is the ordinary answer on a quiet morning.
	if got := doJSONArray(t, payer, "GET", "/payments/pending", "", http.StatusOK); len(got) != 0 {
		t.Errorf("the hub still holds %d instructions after its cut-off", len(got))
	}
	again := doJSON(t, payer, "POST", "/payments/cutoff", "", http.StatusAccepted)
	if orders, _ := again["orderIds"].([]any); len(orders) != 0 {
		t.Errorf("an empty hub uploaded %v; there was nothing to put in a file", again["orderIds"])
	}
}

// A reset throws the queues away with the rows, and the file a cut-off left behind is
// one of them. There is nothing to drain, so the only way a file survives is if the
// reset left the queue standing — and a file about a payment no institution now holds
// a row for would be worked through on the first day after it, against tables the
// reset has emptied. The cut-off is what puts a file there at all: a submission on its
// own leaves the instruction in the payer's bank's hub.
func TestResetThrowsTheQueuesAwayWithTheRows(t *testing.T) {
	srv := newAPIHarness(t)
	payer := payerRoutes(t, srv)
	postJSON(t, payer, "/payments", validSubmission(t, srv))
	doJSON(t, payer, "POST", "/payments/cutoff", "", http.StatusAccepted)
	if pending := pendingAt(t, srv.dep.ClearingHouse().host); pending == 0 {
		t.Fatal("the cut-off left no file at the clearing house, so this test would pass on nothing")
	}
	rec := post(t, srv.CentralBankRoutes(), "/admin/reset")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d", rec.Code)
	}
	if pending := pendingAt(t, srv.dep.ClearingHouse().host); pending != 0 {
		t.Fatalf("%d files survived the reset; each describes a payment no institution now holds", pending)
	}
}

// TestAnAddressNoMemberIsPublishedUnderIsUnprocessable is the clearing house's console
// meeting an address it cannot hand to anybody. On a bank's own port the submitting
// bank is the port; on THIS one it is read out of the payer's address through the
// roster this institution publishes, so an address under a bank code no member holds
// is one this console cannot act on.
//
// It is the clearing house's own version of the refusal a member meets from its copy,
// and the difference is the whole of the subscription model: this institution CAN tell
// that no member holds the code, because the roster is where a member comes into
// existence. The 422 rather than a 404 is the older ruling: an address nobody is
// published under is a well-formed field this system will not act on.
func TestAnAddressNoMemberIsPublishedUnderIsUnprocessable(t *testing.T) {
	srv := newAPIHarness(t)
	// A well-formed German address under a code this scheme has allocated to
	// nobody. It passes mod-97, so what refuses it is the roster and not the
	// check digits.
	body := strings.Replace(validSubmission(t, srv), aliceIBAN, mustMint(iban.DE, "10000000", 1), 1)
	rec := postJSON(t, csmSurface(srv), "/payments", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a payer's address no member is published under = %d, want 422 (body: %s)", rec.Code, rec.Body)
	}
}

// TestAReseededNetworkCanStillBePaidThrough checks from the outside that a reseed
// enrols the banks it founds. A reset forgets every bank actor, truncates and
// reseeds; the actors are not put back in a step of the reset's own, because the
// reseed admits its banks through the deployment — a better division of labour and a
// weaker guarantee, since nothing in Reset can check that a reseed did it.
//
// A row with no actor answers every read, so an assertion on the participant list
// would pass over exactly the failure this exists to catch. It has to be a payment
// carried to Accepted, the state only the PAYEE's bank can put it in.
func TestAReseededNetworkCanStillBePaidThrough(t *testing.T) {
	srv := newAPIHarness(t)

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
	carry(t, srv)
	if got := getPayment(t, srv, id); got.Status != "Accepted" {
		t.Fatalf("a payment through a reseeded network is %q, want Accepted", got.Status)
	}
}

// A reset empties the network, so it has to empty the deployment's picture of it. The
// sequence that fails without it: provision a bank, reset, provision the same BIC
// again. If the deployment keeps the first bank's queues, a file addressed to that BIC
// lands in a queue about a payment no institution holds a row for, on a system whose
// roster is empty. Every assertion below is one of the three things that go wrong.
func TestAReadmittedBankCanBePaidThroughAfterAReset(t *testing.T) {
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
	carry(t, h)
	if got := doJSON(t, csmSurface(h), "GET", "/payments/"+pay, "", http.StatusOK); got["status"].(string) != "Accepted" {
		t.Fatalf("a payment between two re-admitted banks is %q, want Accepted", got["status"])
	}

	// Bank A's own payment survives everything above, which is the half worth
	// stating separately: a reset that rebuilt the deployment but lost the book would
	// pass every assertion so far.
	var after []api.PaymentDTO
	getJSON(t, bankSurface(h, a), "/payments", &after)
	if len(after) != 1 {
		t.Errorf("Bank A sees %d of its own payments, want 1", len(after))
	}
}

// The reseeded banks are rejoined, not just recreated. seed.Populate drives
// payment.Network directly and knows nothing about a deployment, so every bank it
// creates is a row with no actor until Reset rejoins the roster — without which a
// reset leaves a system that answers every read and cannot carry a payment.
func TestPayingAfterAResetGoesThroughTheReseededBanks(t *testing.T) {
	srv := newAPIHarness(t)

	rec := post(t, srv.CentralBankRoutes(), "/admin/reset")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d (body: %s)", rec.Code, rec.Body.String())
	}

	rec = postJSON(t, payerRoutes(t, srv), "/payments", validSubmission(t, srv))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submitting after a reset = %d (body: %s)", rec.Code, rec.Body.String())
	}
	id := decodePaymentID(t, rec)
	carry(t, srv)
	if got := getPayment(t, srv, id); got.Status != "Accepted" {
		t.Fatalf("a payment submitted after a reset is %q, want Accepted", got.Status)
	}
}

// The two kinds of failure a file-driven handler has, and the line between them.
//
// A submission the payer's own bank refuses is decided inside its own unit of work,
// before anything is sent, so it comes back as a status code. A payment the FAR SIDE
// refuses cannot: by the time the payee's bank has looked at the address, this handler
// has answered 202 and the connection is gone.
//
// What the far side's refusal IS follows from settling before releasing: it is handed
// the instruction only after the cycle is final, so it cannot reject — the payment
// settles, the payee's bank sends it back, and the row ends up Returned. The caller's
// experience is the same shape either way.
func TestWhichRefusalsReachTheCallerAndWhichDoNot(t *testing.T) {
	srv := newAPIHarness(t)

	// Answerable: Alice cannot pay a hundred million. Her own bank says so,
	// inside Submit's unit of work, and nothing is sent.
	tooMuch := strings.Replace(validSubmission(t, srv), `"amount":1000`, `"amount":100000000`, 1)
	rec := postJSON(t, payerRoutes(t, srv), "/payments", tooMuch)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a payment the payer's own bank refuses answered %d, want 422 (body: %s)", rec.Code, rec.Body.String())
	}

	// Unanswerable: the IBAN is well-formed and belongs to nobody, which only the
	// payee's bank can know. Under VERDE's OWN bank code, so it routes — an address
	// under some other code would be refused here instead, by the payer's own
	// directory.
	unknown := strings.Replace(validSubmission(t, srv), bellaIBAN, mustMint(iban.IT, "99999", 9999), 1)
	rec = postJSON(t, payerRoutes(t, srv), "/payments", unknown)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("a payment only the far side can refuse answered %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	id := decodePaymentID(t, rec)

	carry(t, srv)
	if got := getPayment(t, srv, id); got.Status != "Accepted" {
		t.Fatalf("status before the cut-off = %q, want Accepted — nothing the far side says can reach it yet", got.Status)
	}

	// Through the cut-off and the return it provokes. The payee's bank is handed
	// the instruction, cannot resolve the address, and sends the money back; the
	// second pass is the return's own trip through the settlement agent.
	settle(t, srv)
	carry(t, srv)

	got := getPayment(t, srv, id)
	if got.Status != "Returned" {
		t.Errorf("status after the cut-off = %q, want Returned — the payee's bank cannot resolve that address", got.Status)
	}
}

// The cut-off instructs settlement, and the console can reach it. Closing a cycle
// sends a pacs.009, and there is no second console button that discharges it.
//
// Exactly: 200 with a Closed cycle carrying no settlement, then a drain, then —
// through the CENTRAL BANK's surface and only that one — the cycle read back as
// Settled and the settlement it names. The rest of the read surface is covered
// elsewhere and deliberately not duplicated: this test is about the INSTRUCTION.
func TestClosingACycleThroughTheAPIInstructsSettlement(t *testing.T) {
	srv := newAPIHarness(t)

	rec := postJSON(t, payerRoutes(t, srv), "/payments", validSubmission(t, srv))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit = %d (body: %s)", rec.Code, rec.Body.String())
	}
	id := decodePaymentID(t, rec)
	carry(t, srv)

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
	carry(t, srv)

	// The cycle is read back from the CLEARING HOUSE, which is the institution
	// that has one: there is no cycles table in the central bank's schema, and
	// asking it is not a wrong answer but a missing table.
	var settled api.ClearingCycleDTO
	getJSON(t, csmSurface(srv), "/cycles/"+cid, &settled)
	if settled.Status != "Settled" {
		t.Fatalf("cycle is %q after draining, want Settled — the pacs.009 is what discharges it", settled.Status)
	}

	// And the settlement is found at the SETTLEMENT AGENT, by the cycle it names rather
	// than by an id the cycle names. The cycle carries no settlementId: that id is
	// allocated inside the agent's own unit of work, and what comes back to the clearing
	// house is a pacs.002 quoting the CYCLE.
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

// An operator's rejection is the clearing house's half and the payer's bank's half,
// and only the first is in the response. The refund is another institution's act in
// another institution's book and happens when the pacs.002 gets there, so the 200
// cannot describe a payer who has already been refunded.
//
// It asserts the RESPONSE and the balance after the drain, and deliberately does not
// read the balance in between: the payer's bank actor runs concurrently with this
// goroutine, so "not yet refunded" would be a race dressed up as an assertion.
func TestRejectingThroughTheAPIRefundsThePayerOnlyAfterTheMessageArrives(t *testing.T) {
	srv := newAPIHarness(t)

	rec := postJSON(t, payerRoutes(t, srv), "/payments", validSubmission(t, srv))
	id := decodePaymentID(t, rec)
	carry(t, srv)
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
	carry(t, srv)

	if got := aliceBalance(t, srv); got != before+1000 {
		t.Errorf("payer's balance after draining = %d, want %d — the pacs.002 is what gives the money back", got, before+1000)
	}
}

// A return goes round the network, and the reason code it carries is how that is
// visible from here. The distinction is not "did the money come back" but WHO did it
// and WITH WHAT: Deployment.Return hands the instruction to the bank that RECEIVED the
// original, which posts its own clawback and sends a pacs.004, and the refund asserted
// below is a DIFFERENT bank's posting made from that same message.
//
// The payment is a SETTLED one out of the seeded dataset, because finality is a
// return's precondition.
func TestReturningThroughTheAPIGoesRoundTheNetwork(t *testing.T) {
	srv := newAPIHarness(t)

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

	carry(t, srv)

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

// A reset leaves every binding a listener made at startup pointing at the institution
// that is still running.
//
// serve binds each surface and each EBICS host ONCE, before the first request, and
// there cannot be a lookup per request because http.Server takes a handler. So a reset
// that REPLACED an institution would leave a deployment nobody could reach — and every
// test in this package would pass, because the fixtures ask the deployment for its
// institutions again after the reset and a real listener never does. The mounted host
// below is captured exactly as serve captures it.
func TestAResetKeepsEveryBindingAListenerMadeAtStartup(t *testing.T) {
	srv := newAPIHarness(t)
	ctx := context.Background()

	csm := srv.dep.ClearingHouse()
	csmHost, cbHost := csm.EBICS(), srv.dep.CentralBank().EBICS()
	mounted := httptest.NewServer(csmHost)
	t.Cleanup(mounted.Close)
	bic, _ := seededParty(t, srv, aliceIBAN)
	before, err := srv.dep.Bank(ctx, payment.ParticipantID(bic))
	if err != nil {
		t.Fatalf("binding %s before the reset: %v", bic, err)
	}

	rec := post(t, srv.CentralBankRoutes(), "/admin/reset")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d (body: %s)", rec.Code, rec.Body)
	}

	if got := srv.dep.ClearingHouse().EBICS(); got != csmHost {
		t.Error("the reset replaced the clearing house's EBICS side; the listener serving it is still on the old one")
	}
	if got := srv.dep.CentralBank().EBICS(); got != cbHost {
		t.Error("the reset replaced the settlement agent's EBICS side; the listener serving it is still on the old one")
	}
	after, err := srv.dep.Bank(ctx, payment.ParticipantID(bic))
	if err != nil {
		t.Fatalf("binding %s after the reset: %v", bic, err)
	}
	if after != before {
		t.Errorf("the reset replaced %s; that bank's own listener is still serving the old one", bic)
	}

	// And a file uploaded to the host the listener is serving is one the clearing
	// house can see. A stale host answers EBICS_OK out of a queue this
	// institution never reads, which is the worse half of the same defect: the
	// bank is told its file arrived and no cut-off ever contains it.
	client := ebics.NewClient(ebics.SubscriberID(bic), mounted.URL)
	if _, err := client.Upload(ctx, ebics.CCT, []byte("<a file this test never asks anybody to read/>")); err != nil {
		t.Fatalf("%s uploading to the host its listener mounts: %v", bic, err)
	}
	if got := pendingAt(t, csm.host); got != 1 {
		t.Errorf("the clearing house has %d files to work through, and one was just uploaded to the host its listener mounts", got)
	}
}

// A reset empties what the clearing house is holding between files: the returns
// waiting for finality, and the receiving banks' shares of every file taken in. Both
// are keyed by ids the store mints, and a reset restarts those sequences — so a share
// that survived one would be released into a bank's queue by the next cycle given the
// same id, carrying transactions about payments no institution holds a row for.
//
// Both are rows, so what makes this true is Reset's table list, which is why it is
// still measured here: a table added to the schema and left out of the shape would be
// invisible to every other test in this package.
//
// The count is not zero. A reset REBUILDS, and the sample dataset carries its own
// in-flight payments through a real cut-off, so the clearing house legitimately holds
// shares afterwards — what must hold is that it holds exactly what a fresh build
// holds. Counting transactions rather than cycles is what makes an extra share
// visible: the id it would be filed under is one the restarted sequence mints again.
func TestAResetThrowsAwayTheSharesTheClearingHouseHolds(t *testing.T) {
	srv := newAPIHarness(t)
	ctx := context.Background()
	csm := srv.dep.ClearingHouse()
	seeded := heldTransactions(t, csm)

	// A return placed by hand, because the seed carries none and a return this
	// deployment really relays is answered in the same business day — there is no
	// moment a test could catch one waiting.
	if err := csm.ops.HoldReturn(ctx, payment.HeldReturn{
		PaymentID: "pay_sentinel", ReturnedBy: srv.dep.cfg.ClearingHouseBIC, File: []byte("<Envelope/>"),
	}); err != nil {
		t.Fatalf("HoldReturn: %v", err)
	}

	postJSON(t, payerRoutes(t, srv), "/payments", validSubmission(t, srv))
	doJSON(t, payerRoutes(t, srv), "POST", "/payments/cutoff", "", http.StatusAccepted)
	carry(t, srv)
	if got := heldTransactions(t, csm); got <= seeded {
		t.Fatalf("the clearing house holds %d held transactions and held %d before this test's own; it added no share, so this test would pass on nothing", got, seeded)
	}

	rec := post(t, srv.CentralBankRoutes(), "/admin/reset")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d (body: %s)", rec.Code, rec.Body)
	}

	if got := heldTransactions(t, csm); got != seeded {
		t.Errorf("the clearing house holds shares over %d transactions after the reset and a fresh build leaves %d; the difference survived, addressed against cycle ids the store will mint again", got, seeded)
	}
	if _, err := csm.ops.GetHeldReturn(ctx, "pay_sentinel"); !errors.Is(err, payment.ErrHeldReturnNotFound) {
		t.Errorf("a held return survived the reset (%v); it names a payment no institution now holds", err)
	}
}

// heldTransactions counts the transactions the clearing house is holding shares
// for, across every cycle.
func heldTransactions(t *testing.T, c *ClearingHouse) int {
	t.Helper()

	ctx := context.Background()
	cycles, err := c.ops.ListCycles(ctx)
	if err != nil {
		t.Fatalf("ListCycles: %v", err)
	}
	var n int
	for _, cycle := range cycles {
		files, err := c.ops.ListHeldFiles(ctx, cycle.ID)
		if err != nil {
			t.Fatalf("ListHeldFiles(%s): %v", cycle.ID, err)
		}
		for _, f := range files {
			n += len(f.Transactions)
		}
	}
	return n
}

// A bank's console, bound before a reset, still reads and empties that bank's own hub
// afterwards. The hub is the one thing a bank holds outside its database, and a
// submission goes to the bank the DEPLOYMENT routes it to while the pending and
// cut-off routes answer out of the bank the LISTENER was given. A reset that made those
// two different banks would leave a console reporting an empty hub for ever, with
// every row in the database looking exactly right.
func TestABankConsoleBoundBeforeAResetStillHoldsItsOwnHub(t *testing.T) {
	srv := newAPIHarness(t)
	payer := payerRoutes(t, srv)

	rec := post(t, srv.CentralBankRoutes(), "/admin/reset")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d (body: %s)", rec.Code, rec.Body)
	}

	postJSON(t, payerRoutes(t, srv), "/payments", validSubmission(t, srv))
	if got := doJSONArray(t, payer, "GET", "/payments/pending", "", http.StatusOK); len(got) != 1 {
		t.Fatalf("the console bound before the reset holds %d instructions, want the 1 just submitted", len(got))
	}
	out := doJSON(t, payer, "POST", "/payments/cutoff", "", http.StatusAccepted)
	if orders, _ := out["orderIds"].([]any); len(orders) != 1 {
		t.Fatalf("its cut-off uploaded %v, want one file", out["orderIds"])
	}
}
