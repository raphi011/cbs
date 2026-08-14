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
	"github.com/raphi011/cbs/node/csm"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/seed"
	"github.com/raphi011/cbs/store/testenv"
)

// What this file is for: the seam between HTTP and the business day.

// newAPIHarness is a deployment holding the demo network, with both hosts really
// listening. It is newBaseHarness plus the one scenario that fills it.
func newAPIHarness(t *testing.T) *server {
	t.Helper()
	srv := newBaseHarness(t)
	// Boot leaves a base state, so the dataset every test below reads is a
	// SCENARIO — which is the whole point: it was built through the same doors an
	// operator has, so its payments name the files that carried them.
	if _, err := srv.dep.RunScenario(context.Background(), scenarioDemoNetwork.ID); err != nil {
		t.Fatalf("the demo network scenario: %v", err)
	}
	return srv
}

// newBaseHarness is what a fresh deployment holds and nothing else: four banks
// founded, admitted, subscribed, priced and prefunded, one depositor each, and
// no payment anywhere.
func newBaseHarness(t *testing.T) *server {
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

// buildDemoNetwork triggers the demo network through the operator's own door,
// which is the only way anything gets into a deployment that boots holding a
// base state. A reset rewinds to that base state, so every test below that
// resets and then expects customers runs this afterwards.
func buildDemoNetwork(t *testing.T, srv *server) {
	t.Helper()
	rec := post(t, srv.CentralBankRoutes(), "/scenarios/"+scenarioDemoNetwork.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("running the demo network scenario = %d (body: %s)", rec.Code, rec.Body)
	}
}

// seededParty is the participant and account behind one of the seed's IBANs.
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
var (
	aliceIBAN = mustMint(iban.DE, "99999999", 1) // Aurora Bank's first account
	bellaIBAN = mustMint(iban.IT, "99999", 2)    // Banca Verde's second
)

// validSubmission is Alice at Aurora Bank paying Bella at Banca Verde: a credit
// transfer the seeded network can carry the whole way.
func validSubmission(t *testing.T, s *server) string {
	t.Helper()
	_, payer := seededParty(t, s, aliceIBAN)
	_, payee := seededParty(t, s, bellaIBAN)
	// NO agent on either side, and none to give: the instruction carries an
	// address and a name, and the payer's bank derives the routing element from
	// the address through its own copy of the scheme's directory.
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
	// empties at a cut-off.
	rec := postJSON(t, payerRoutes(t, srv), "/payments", validSubmission(t, srv))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	id := decodePaymentID(t, rec)

	// Read on the PAYER'S BANK's surface, because until the file is carried that
	// bank is the only institution with a row for this payment at all: the
	// instruction has not been sent, so the clearing house answers 404 rather than
	// "Initiated".
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

// The two routes the hub added, over HTTP, on the bank's own port: what is
// waiting, and the act that sends it.
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

// A reset throws the queues away with the rows, and the file a cut-off left
// behind is one of them.
func TestResetThrowsTheQueuesAwayWithTheRows(t *testing.T) {
	srv := newAPIHarness(t)
	payer := payerRoutes(t, srv)
	postJSON(t, payer, "/payments", validSubmission(t, srv))
	doJSON(t, payer, "POST", "/payments/cutoff", "", http.StatusAccepted)
	if pending := pendingAt(t, srv.dep.ClearingHouse().Host()); pending == 0 {
		t.Fatal("the cut-off left no file at the clearing house, so this test would pass on nothing")
	}
	rec := post(t, srv.CentralBankRoutes(), "/admin/reset")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d", rec.Code)
	}
	if pending := pendingAt(t, srv.dep.ClearingHouse().Host()); pending != 0 {
		t.Fatalf("%d files survived the reset; each describes a payment no institution now holds", pending)
	}
}

// TestAnAddressNoMemberIsPublishedUnderIsUnprocessable is the clearing house's
// console meeting an address it cannot hand to anybody.
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

// TestAReseededNetworkCanStillBePaidThrough checks from the outside that a
// reseed enrols the banks it founds.
func TestAReseededNetworkCanStillBePaidThrough(t *testing.T) {
	srv := newAPIHarness(t)

	rec := post(t, srv.CentralBankRoutes(), "/admin/reset")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d (body: %s)", rec.Code, rec.Body)
	}
	buildDemoNetwork(t, srv)

	// The scenario's own two customers, resolved again: the rebuild made them,
	// and their ids are not the ones the first build had reason to be.
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

// A reset empties the network, so it has to empty the deployment's picture of
// it. The sequence that fails without it: provision a bank, reset, provision
// the same BIC again.
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
// payment.Network directly and knows nothing about a deployment, so every bank
// it creates is a row with no actor until Reset rejoins the roster — without
// which a reset leaves a base state that answers every read and that no
// scenario can build on.
func TestPayingAfterAResetGoesThroughTheReseededBanks(t *testing.T) {
	srv := newAPIHarness(t)

	rec := post(t, srv.CentralBankRoutes(), "/admin/reset")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d (body: %s)", rec.Code, rec.Body.String())
	}
	buildDemoNetwork(t, srv)

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

// The two kinds of failure a file-driven handler has, and the line between
// them.
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
	// payee's bank can know.
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

// The cut-off instructs settlement, and the console can reach it. Closing a
// cycle sends a pacs.009, and there is no second console button that discharges
// it.
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

	// And the settlement is found at the SETTLEMENT AGENT, by the cycle it names
	// rather than by an id the cycle names.
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
// half, and only the first is in the response.
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

// A return goes round the network, and the reason code it carries is how that
// is visible from here.
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
	// An identifier and nothing else.
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

// A reset leaves every binding a listener made at startup pointing at the
// institution that is still running.
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
	// house can see.
	client := ebics.NewClient(ebics.SubscriberID(bic), mounted.URL)
	if _, err := client.Upload(ctx, ebics.CCT, []byte("<a file this test never asks anybody to read/>")); err != nil {
		t.Fatalf("%s uploading to the host its listener mounts: %v", bic, err)
	}
	if got := pendingAt(t, csm.Host()); got != 1 {
		t.Errorf("the clearing house has %d files to work through, and one was just uploaded to the host its listener mounts", got)
	}
}

// A reset empties what the clearing house is holding between files: the returns
// waiting for finality, and the receiving banks' shares of every file taken in.
func TestAResetThrowsAwayTheSharesTheClearingHouseHolds(t *testing.T) {
	srv := newAPIHarness(t)
	ctx := context.Background()
	csm := srv.dep.ClearingHouse()
	held := heldTransactions(t, csm)

	// A return placed by hand, because the seed carries none and a return this
	// deployment really relays is answered in the same business day — there is no
	// moment a test could catch one waiting.
	if err := csm.Network().HoldReturn(ctx, payment.HeldReturn{
		PaymentID: "pay_sentinel", ReturnedBy: srv.dep.cfg.ClearingHouseBIC, File: []byte("<Envelope/>"),
	}); err != nil {
		t.Fatalf("HoldReturn: %v", err)
	}

	postJSON(t, payerRoutes(t, srv), "/payments", validSubmission(t, srv))
	doJSON(t, payerRoutes(t, srv), "POST", "/payments/cutoff", "", http.StatusAccepted)
	carry(t, srv)
	if got := heldTransactions(t, csm); got <= held {
		t.Fatalf("the clearing house holds %d held transactions and held %d before this test's own; it added no share, so this test would pass on nothing", got, held)
	}

	rec := post(t, srv.CentralBankRoutes(), "/admin/reset")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d (body: %s)", rec.Code, rec.Body)
	}

	// A reset rewinds to the base state, which holds no file and no share at all.
	if got := heldTransactions(t, csm); got != 0 {
		t.Errorf("the clearing house holds shares over %d transactions after the reset and the base state holds none; the difference survived, addressed against cycle ids the store will mint again", got)
	}
	if _, err := csm.Network().GetHeldReturn(ctx, "pay_sentinel"); !errors.Is(err, payment.ErrHeldReturnNotFound) {
		t.Errorf("a held return survived the reset (%v); it names a payment no institution now holds", err)
	}
}

// heldTransactions counts the transactions the clearing house is holding shares
// for, across every cycle.
func heldTransactions(t *testing.T, c *csm.ClearingHouse) int {
	t.Helper()

	ctx := context.Background()
	cycles, err := c.Network().ListCycles(ctx)
	if err != nil {
		t.Fatalf("ListCycles: %v", err)
	}
	var n int
	for _, cycle := range cycles {
		files, err := c.Network().ListHeldFiles(ctx, cycle.ID)
		if err != nil {
			t.Fatalf("ListHeldFiles(%s): %v", cycle.ID, err)
		}
		for _, f := range files {
			n += len(f.Transactions)
		}
	}
	return n
}

// A bank's console, bound before a reset, still reads and empties that bank's
// own hub afterwards.
func TestABankConsoleBoundBeforeAResetStillHoldsItsOwnHub(t *testing.T) {
	srv := newAPIHarness(t)
	payer := payerRoutes(t, srv)

	rec := post(t, srv.CentralBankRoutes(), "/admin/reset")
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d (body: %s)", rec.Code, rec.Body)
	}
	buildDemoNetwork(t, srv)

	postJSON(t, payerRoutes(t, srv), "/payments", validSubmission(t, srv))
	if got := doJSONArray(t, payer, "GET", "/payments/pending", "", http.StatusOK); len(got) != 1 {
		t.Fatalf("the console bound before the reset holds %d instructions, want the 1 just submitted", len(got))
	}
	out := doJSON(t, payer, "POST", "/payments/cutoff", "", http.StatusAccepted)
	if orders, _ := out["orderIds"].([]any); len(orders) != 1 {
		t.Fatalf("its cut-off uploaded %v, want one file", out["orderIds"])
	}
}
