package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
// Aurora Bank and dep_20 is Alice's account in it.
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
	msh, err := mesh.New(net, testMeshConfig, log)
	if err != nil {
		t.Fatalf("mesh.New: %v", err)
	}
	if err := msh.Start(ctx); err != nil {
		t.Fatalf("mesh.Start: %v", err)
	}
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
	return NewServer(net, msh, data.Populate, log), msh
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
// start from nothing, so bank_1/dep_20 is Alice on every run and after every
// reset. The IBANs are quoted because SEPA credit transfer is addressed BY iban
// (payment.Scheme.AddressedBy): without them the payee's bank has no address to
// resolve and answers AC01.
//
// The amount is small enough that Alice can afford it whatever else the seeded
// scenario has already taken out of her account.
func validSubmission() string {
	return `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"bank_1","account":"dep_20","identifier":{"scheme":"IBAN","value":"SE89-AURORA-1001"}},
		"creditor":{"participant":"bank_3","account":"dep_23","identifier":{"scheme":"IBAN","value":"IT60-VERDE-2002"}},
		"amount":1000,
		"description":"mesh handoff"
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
	rec := postJSON(t, srv.BankRoutes("bank_1"), "/payments", validSubmission())
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	id := decodePaymentID(t, rec)

	before := getPayment(t, srv, id)
	if before.Status != "Initiated" {
		t.Errorf("status before draining = %q, want Initiated", before.Status)
	}

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
// the central bank's four reads are how an operator watches the answer come
// back. That is the whole of what "the mesh is wired into api" buys, so it is
// asserted end to end: 200 with a Closed cycle and no settlement on it, then a
// drain, then a settlement that both operators can read.
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

// aliceBalance is the seeded payer's book balance, read through her own bank.
func aliceBalance(t *testing.T, s *Server) int64 {
	t.Helper()
	bal := doJSON(t, bank(s, "bank_1"), "GET", "/deposit-accounts/dep_20/balance", "", http.StatusOK)
	return int64(bal["book"].(float64))
}
