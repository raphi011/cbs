package api

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/mesh"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/testenv"
)

var fixedTime = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

// The three operator surfaces a test drives. Which one a request goes to is
// decided by the route, exactly as a port decides it in the running system —
// so a test that asks the wrong operator gets a 404, which is the point.

func cb(s *Server) http.Handler  { return s.CentralBankRoutes() }
func csm(s *Server) http.Handler { return s.ClearingHouseRoutes() }

func bank(s *Server, pid string) http.Handler {
	return s.BankRoutes(payment.ParticipantID(pid))
}

// newServer builds a Server over an empty in-memory store, with a mesh running
// over the same network. populate is the reseed function — the tests' stand-in
// for the sample dataset — and is called once now, as the process would at boot,
// and again on every reset. Pass nil for a server that resets to an empty system.
//
// The mesh is not optional and every test here gets one, because since
// sub-project 7b it is what carries a payment past the bank it was handed to.
// Started after populate, which is the order cmd/server uses: mesh.Start reads
// the participant roster once, and a bank admitted afterwards — which for most
// tests here is every bank — registers itself through POST /members. That is why
// two banks in one test may not share a BIC: two actors on one address is a
// routing table with one entry, and the mesh refuses it at admission.
//
// Drain FIRST, then Stop, at cleanup, and both errors are reported. See
// newAPIHarness in mesh_test.go, which says why at length.
func newServer(t *testing.T, populate func(context.Context, *payment.Network) error) *Server {
	t.Helper()
	clock := func() time.Time { return fixedTime }
	store := testenv.New(t, clock)
	net := payment.NewNetwork(store.Payment(), clock)
	if populate != nil {
		if err := populate(context.Background(), net); err != nil {
			t.Fatalf("populate: %v", err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	msh, err := mesh.New(net, testMeshConfig, log)
	if err != nil {
		t.Fatalf("mesh.New: %v", err)
	}
	if err := msh.Start(context.Background()); err != nil {
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
	return NewServer(net, msh, populate, log)
}

// drainServer waits for the mesh behind a Server built by newServer to go quiet.
//
// It exists because a payment is no longer finished when POST /payments answers:
// the counterparty's acceptance and the clearing house's are two more actors and
// two more messages. Every test below that submits a payment and then asserts on
// what became of it calls this in between, and none of them waits for a duration
// to do it.
func drainServer(t *testing.T, s *Server) {
	t.Helper()
	drain(t, s.mesh)
}

// do runs a request through the handler and returns the recorder.
func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// doJSON runs a request, asserts the status, and decodes the JSON body into a
// map.
func doJSON(t *testing.T, h http.Handler, method, path, body string, wantStatus int) map[string]any {
	t.Helper()
	rec := do(t, h, method, path, body)
	if rec.Code != wantStatus {
		t.Fatalf("%s %s: got status %d, want %d (body: %s)", method, path, rec.Code, wantStatus, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding body %q: %v", rec.Body.String(), err)
	}
	return out
}

// doJSONArray is doJSON's counterpart for an endpoint that returns a JSON
// array rather than an object — a timeline or a list.
func doJSONArray(t *testing.T, h http.Handler, method, path, body string, wantStatus int) []any {
	t.Helper()
	rec := do(t, h, method, path, body)
	if rec.Code != wantStatus {
		t.Fatalf("%s %s: got status %d, want %d (body: %s)", method, path, rec.Code, wantStatus, rec.Body.String())
	}
	if rec.Body.Len() == 0 {
		return nil
	}
	var out []any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding body %q: %v", rec.Body.String(), err)
	}
	return out
}

// getJSON runs a GET, asserts 200, and decodes the body into out — which may be
// a slice, unlike doJSON's map.
func getJSON(t *testing.T, h http.Handler, path string, out any) {
	t.Helper()
	rec := do(t, h, "GET", path, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: got status %d, want 200 (body: %s)", path, rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decoding body %q: %v", rec.Body.String(), err)
	}
}

func assertEqual[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

func assertStatus(t *testing.T, h http.Handler, method, path, body string, want int) {
	t.Helper()
	rec := do(t, h, method, path, body)
	if rec.Code != want {
		t.Fatalf("%s %s: got status %d, want %d (body: %s)", method, path, rec.Code, want, rec.Body.String())
	}
}

// prdOf is the participant's default deposit product, read back from the
// participant resource.
//
// Every deposit account is opened FROM a product, and a bank's default one is
// created with its chart of accounts at onboarding — so a client that has just
// created a bank learns which product it may sell by reading the bank.
func prdOf(t *testing.T, h *Server, pid string) string {
	t.Helper()
	return doJSON(t, bank(h, pid), "GET", "/me", "", http.StatusOK)["productId"].(string)
}

func TestDepositFlow(t *testing.T) {
	h := newServer(t, nil)

	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)

	// Fund Alice and confirm the returned balance.
	bal := doJSON(t, bank(h, pid), "POST", "/deposits", `{"account":"`+did+`","amount":100000,"description":"opening"}`, http.StatusOK)
	if got := int64(bal["available"].(float64)); got != 100000 {
		t.Fatalf("available after funding = %d, want 100000", got)
	}

	// Read the balance back.
	bal = doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did+"/balance", "", http.StatusOK)
	if got := int64(bal["book"].(float64)); got != 100000 {
		t.Fatalf("book balance = %d, want 100000", got)
	}
}

func TestSCTEndToEnd(t *testing.T) {
	h := newServer(t, nil)

	a := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	b := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKBDEFFXXX","name":"Bank B"}`, http.StatusCreated)["id"].(string)
	// SCT addresses both legs by IBAN (payment.Scheme.AddressedBy), so both
	// accounts need one before submission will accept them.
	alice := doJSON(t, bank(h, a), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`","identifiers":[{"scheme":"IBAN","value":"SE89-SCT-ALICE-0001"}]}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, bank(h, b), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`","identifiers":[{"scheme":"IBAN","value":"SE89-SCT-BOB-0001"}]}`, http.StatusCreated)["id"].(string)

	doJSON(t, bank(h, a), "POST", "/deposits", `{"account":"`+alice+`","amount":100000,"description":"opening"}`, http.StatusOK)

	cyc := doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)["id"].(string)

	// The payee's IBAN is quoted, because on a push it is the payer who knows
	// it: their own bank has no way to look up an address in another bank's
	// register, and the pacs.008 it is about to build has to carry one.
	pay := doJSON(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`","identifier":{"scheme":"IBAN","value":"SE89-SCT-BOB-0001"}},
		"amount":25000,
		"endToEndId":"e2e-1",
		"creditorAgent":"BNKBDEFFXXX",
		"creditorName":"Bob"
	}`, http.StatusAccepted)
	// Initiated, not Accepted: the payee's bank has not seen it yet. Draining is
	// what carries the conversation to its end.
	if pay["status"].(string) != "Initiated" {
		t.Fatalf("payment status in the 202 = %q, want Initiated", pay["status"])
	}
	payID := pay["id"].(string)
	drainServer(t, h)
	if got := doJSON(t, csm(h), "GET", "/payments/"+payID, "", http.StatusOK); got["status"].(string) != "Accepted" {
		t.Fatalf("payment status after draining = %q, want Accepted", got["status"])
	}

	// The cut-off, which is now also the settlement instruction: the clearing
	// house nets the batch and sends a pacs.009, and the central bank discharges
	// it in an actor of its own.
	assertStatus(t, csm(h), "POST", "/cycles/"+cyc+"/close", "", http.StatusOK)
	drainServer(t, h)

	aliceBal := doJSON(t, bank(h, a), "GET", "/deposit-accounts/"+alice+"/balance", "", http.StatusOK)
	if got := int64(aliceBal["book"].(float64)); got != 75000 {
		t.Fatalf("alice book = %d, want 75000", got)
	}
	bobBal := doJSON(t, bank(h, b), "GET", "/deposit-accounts/"+bob+"/balance", "", http.StatusOK)
	if got := int64(bobBal["book"].(float64)); got != 25000 {
		t.Fatalf("bob book = %d, want 25000", got)
	}

	// Reserves are reported one row per asset, so a euro bank has exactly one.
	var reserveA []map[string]any
	getJSON(t, cb(h), "/reserves/"+a, &reserveA)
	if len(reserveA) != 1 || reserveA[0]["asset"] != "EUR" {
		t.Fatalf("bank A reserves = %v, want one EUR row", reserveA)
	}
	if got := int64(reserveA[0]["reserve"].(float64)); got != 75000 {
		t.Fatalf("bank A reserve = %d, want 75000", got)
	}

	got := doJSON(t, csm(h), "GET", "/payments/"+payID, "", http.StatusOK)
	if got["status"].(string) != "Settled" {
		t.Fatalf("payment status after settle = %q, want Settled", got["status"])
	}
}

// An account without an asset is refused rather than opened in euro. The whole
// point of the asset dimension is that nothing picks a currency on the
// caller's behalf, and a request body that forgot to say is exactly the case
// where a default would go unnoticed.
func TestCreateAccountRequiresAsset(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bank(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, bank(h, pid), "POST", "/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, bank(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"No Asset","type":"Liability"}`, http.StatusBadRequest)

	// An asset the system does not know is a 400 — a bad field value, like an
	// unparseable account type — not a silent substitution, and not a 404,
	// which on this route would read as "participant not found".
	assertStatus(t, bank(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Dogecoin","type":"Liability","asset":"DOGE"}`, http.StatusBadRequest)
}

func TestOpenDepositAccountRequiresAsset(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"No Asset"}`, http.StatusBadRequest)
}

// GET /assets lists the assets the system knows, with the scale a client needs
// to render an amount. Read-only and network-wide: assets are defined in code,
// like schemes, so there is nothing to create and nothing per-participant.
func TestListAssets(t *testing.T) {
	h := newServer(t, nil)

	var assets []assetDTO
	getJSON(t, csm(h), "/assets", &assets)

	byCode := make(map[string]assetDTO, len(assets))
	for _, a := range assets {
		byCode[a.Code] = a
	}
	eur, ok := byCode["EUR"]
	if !ok {
		t.Fatalf("GET /assets did not list EUR: %v", assets)
	}
	assertEqual(t, "EUR scale", int(eur.Scale), 2)
	assertEqual(t, "EUR class", eur.Class, "Fiat")

	btc, ok := byCode["BTC"]
	if !ok {
		t.Fatalf("GET /assets did not list BTC: %v", assets)
	}
	assertEqual(t, "BTC scale", int(btc.Scale), 8)
	assertEqual(t, "BTC class", btc.Class, "Crypto")
}

// A participant cannot be created in an asset the system does not know. The
// assets array names the set of assets a bank joins with, and each one has to
// resolve before any of its plumbing accounts are written.
func TestCreateParticipantRejectsUnknownAsset(t *testing.T) {
	h := newServer(t, nil)

	assertStatus(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A","assets":["DOGE"]}`, http.StatusBadRequest)
}

// TestCreateParticipantBICIsValidatedAndRendered pins two things about the
// BIC the mesh routes on: a structurally malformed one (including an absent
// or empty field, which is malformed the same way) is refused with 422 — a
// business-rule refusal, not a decoding failure — and a well-formed one comes
// back on the created participant, and on every later read of it.
func TestCreateParticipantBICIsValidatedAndRendered(t *testing.T) {
	h := newServer(t, nil)

	for _, tc := range []struct {
		what string
		body string
	}{
		{"absent", `{"name":"Bank A"}`},
		{"empty", `{"bic":"","name":"Bank A"}`},
		{"too short", `{"bic":"BANKDE","name":"Bank A"}`},
		{"lower case", `{"bic":"bankdeffxxx","name":"Bank A"}`},
	} {
		t.Run(tc.what, func(t *testing.T) {
			assertStatus(t, cb(h), "POST", "/members", tc.body, http.StatusUnprocessableEntity)
		})
	}

	p := doJSON(t, cb(h), "POST", "/members", `{"bic":"AURODEFFXXX","name":"Aurora Bank"}`, http.StatusCreated)
	assertEqual(t, "bic on creation", p["bic"].(string), "AURODEFFXXX")

	reread := doJSON(t, bank(h, p["id"].(string)), "GET", "/me", "", http.StatusOK)
	assertEqual(t, "bic on a later read", reread["bic"].(string), "AURODEFFXXX")
}

// TestAccountResponseIncludesAsset pins that the account, deposit-account and
// entry DTOs all carry the asset they are denominated in.
func TestAccountResponseIncludesAsset(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)

	res := do(t, bank(h, pid), "GET", "/deposit-accounts", "")
	if !strings.Contains(res.Body.String(), `"asset":"EUR"`) {
		t.Errorf("deposit-account listing has no asset field: %s", res.Body)
	}

	gl := doJSON(t, bank(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, bank(h, pid), "POST", "/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, bank(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)
	assertEqual(t, "account asset", acct["asset"].(string), "EUR")

	other := doJSON(t, bank(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	tx := doJSON(t, bank(h, pid), "POST", "/transactions", `{
		"entries":[
			{"accountId":"`+acct["id"].(string)+`","amount":100,"direction":"Debit"},
			{"accountId":"`+other+`","amount":100,"direction":"Credit"}
		]
	}`, http.StatusCreated)
	entries := tx["entries"].([]any)
	for _, e := range entries {
		entry := e.(map[string]any)
		assertEqual(t, "entry asset", entry["asset"].(string), "EUR")
	}
}

// TestBalanceEndpointReportsTheValueDatedBalance pins Task 8: the balance
// endpoint now reports the value-dated balance alongside the book balance,
// and the two diverge whenever a posting's value date is forward of its
// booking date. Two movements land on the same account — one value-dated
// today, one three days out — so the book balance carries both but the
// value-dated balance, read back with ?asOf=today, carries only the first.
func TestBalanceEndpointReportsTheValueDatedBalance(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bank(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, bank(h, pid), "POST", "/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, bank(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	other := doJSON(t, bank(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	today := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	post := func(amount int, valueDate time.Time) {
		doJSON(t, bank(h, pid), "POST", "/transactions", fmt.Sprintf(`{
			"entries":[
				{"accountId":%q,"amount":%d,"direction":"Debit"},
				{"accountId":%q,"amount":%d,"direction":"Credit"}
			],
			"valueDate":%q
		}`, acct, amount, other, amount, valueDate.Format(time.RFC3339)), http.StatusCreated)
	}
	post(10_000, today)
	post(5_000, today.AddDate(0, 0, 3))

	var got struct {
		Balance          int64 `json:"balance"`
		ValueDateBalance int64 `json:"valueDateBalance"`
	}
	getJSON(t, bank(h, pid), "/accounts/"+acct+"/balance?asOf="+today.Format(time.RFC3339), &got)

	if got.Balance != 15_000 {
		t.Errorf("balance = %d, want 15000", got.Balance)
	}
	if got.ValueDateBalance != 10_000 {
		t.Errorf("valueDateBalance = %d, want 10000 (the forward-dated movement has not taken effect)", got.ValueDateBalance)
	}
}

// TestBalanceEndpointDefaultsAsOfToNow covers the other half of the parameter:
// omitted entirely, asOf is now, so a movement value-dated in the past counts
// and one value-dated in the future does not. The book balance carries both
// either way, which is what makes the two figures distinguishable here.
func TestBalanceEndpointDefaultsAsOfToNow(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bank(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, bank(h, pid), "POST", "/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, bank(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	other := doJSON(t, bank(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	post := func(amount int, valueDate time.Time) {
		doJSON(t, bank(h, pid), "POST", "/transactions", fmt.Sprintf(`{
			"entries":[
				{"accountId":%q,"amount":%d,"direction":"Debit"},
				{"accountId":%q,"amount":%d,"direction":"Credit"}
			],
			"valueDate":%q
		}`, acct, amount, other, amount, valueDate.Format(time.RFC3339)), http.StatusCreated)
	}
	now := time.Now()
	post(10_000, now.AddDate(0, 0, -3))
	post(5_000, now.AddDate(0, 0, 30))

	var got struct {
		Balance          int64 `json:"balance"`
		ValueDateBalance int64 `json:"valueDateBalance"`
	}
	getJSON(t, bank(h, pid), "/accounts/"+acct+"/balance", &got)

	if got.Balance != 15_000 {
		t.Errorf("balance = %d, want 15000", got.Balance)
	}
	if got.ValueDateBalance != 10_000 {
		t.Errorf("valueDateBalance with no asOf = %d, want 10000 (asOf defaults to now)", got.ValueDateBalance)
	}
}

// TestBalanceEndpointRejectsAnUnparseableAsOf pins the 400 on a malformed
// ?asOf=: it must not panic and must not silently fall back to now.
func TestBalanceEndpointRejectsAnUnparseableAsOf(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bank(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, bank(h, pid), "POST", "/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, bank(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, bank(h, pid), "GET", "/accounts/"+acct+"/balance?asOf=not-a-date", "", http.StatusBadRequest)

	// And the parse happens before the account is read: an unknown account with
	// a malformed asOf is still a 400, not the 404 the first store call would
	// have produced. That ordering is what keeps a bad request from costing two
	// store round trips before being refused.
	assertStatus(t, bank(h, pid), "GET", "/accounts/nope/balance?asOf=not-a-date", "", http.StatusBadRequest)
}

// TestCreateParticipantWithAssets pins the optional assets array on
// participant creation: given, it is forwarded to AddParticipant instead of
// the default EUR-only set.
func TestCreateParticipantWithAssets(t *testing.T) {
	h := newServer(t, nil)
	p := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A","assets":["USD"]}`, http.StatusCreated)
	assets := p["assets"].([]any)
	if len(assets) != 1 {
		t.Fatalf("participant assets = %v, want exactly one (USD)", assets)
	}
	row := assets[0].(map[string]any)
	assertEqual(t, "participant asset", row["asset"].(string), "USD")
}

// The other half of that contract: the two ways of not naming any asset must
// behave identically. `{"assets":[]}` and an absent field are distinguishable
// on the wire — one decodes to an empty slice, the other leaves the field nil —
// and only a `len == 0` test collapses them. That test is right, and it was
// right by inspection alone, which is the kind of correctness that stops being
// true without anything going red. Both mean "join with EUR".
func TestCreateParticipantDefaultsToEuroForEmptyAndAbsentAssets(t *testing.T) {
	h := newServer(t, nil)

	for _, tc := range []struct{ name, body string }{
		{"absent", `{"bic":"BNKADEFFXXX","name":"Bank A"}`},
		{"explicitly empty", `{"bic":"BNKBDEFFXXX","name":"Bank B","assets":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := doJSON(t, cb(h), "POST", "/members", tc.body, http.StatusCreated)
			assets := p["assets"].([]any)
			if len(assets) != 1 {
				t.Fatalf("participant assets = %v, want exactly one (EUR)", assets)
			}
			row := assets[0].(map[string]any)
			assertEqual(t, "participant asset", row["asset"].(string), "EUR")
		})
	}
}

// TestCrossAssetPaymentReturns422 is the HTTP-layer half of Task 5's
// payment.ErrAssetMismatch mapping: initiating a payment through a scheme
// whose asset does not match the debtor account's asset must answer 422, not
// 500. A bank joined with USD only has no EUR account to collide with
// sepa.ct's EUR, so the mismatch is reached without needing to fund anything
// or open a cycle — checkPartyTx and the asset check both run before either
// would matter.
func TestCrossAssetPaymentReturns422(t *testing.T) {
	h := newServer(t, nil)
	a := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A","assets":["USD"]}`, http.StatusCreated)["id"].(string)
	b := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKBDEFFXXX","name":"Bank B"}`, http.StatusCreated)["id"].(string)
	alice := doJSON(t, bank(h, a), "POST", "/deposit-accounts", `{"name":"Alice","asset":"USD","productId":"`+prdOf(t, h, a)+`"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, bank(h, b), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`"},
		"amount":1000
	}`, http.StatusUnprocessableEntity)
}

// TestPaymentDTOsCarryAsset pins the fix for the gap flagged in task-7's web
// review: mandateDTO, clearingCycleDTO, settlementDTO and schemeDTO used to
// carry no asset at all, which forced the frontend to hardcode "EUR"
// constants with nothing to notice when that stopped being true (see
// api/dto_payment.go's toMandateDTO/toClearingCycleDTO/toSettlementDTO/
// toSchemeDTO). Each now carries its asset, resolved server-side.
//
// The mandate case is asserted against a USD debtor, not EUR, specifically to
// prove the value is genuinely derived from the debtor's own deposit account
// rather than a value that merely happens to match every other DTO's EUR —
// a test that only ever saw EUR would look identical to the hardcoded
// constants it replaces.
func TestPaymentDTOsCarryAsset(t *testing.T) {
	h := newServer(t, nil)

	a := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A","assets":["USD","EUR"]}`, http.StatusCreated)["id"].(string)
	b := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKBDEFFXXX","name":"Bank B","assets":["EUR","USD"]}`, http.StatusCreated)["id"].(string)
	aliceUSD := doJSON(t, bank(h, a), "POST", "/deposit-accounts", `{"name":"AliceUSD","asset":"USD","productId":"`+prdOf(t, h, a)+`"}`, http.StatusCreated)["id"].(string)
	// aliceEUR and bob feed the SCT payment below, which addresses both legs by
	// IBAN (payment.Scheme.AddressedBy) — aliceUSD and bobUSD only ever back a
	// mandate, which carries no scheme and so is never addressed.
	aliceEUR := doJSON(t, bank(h, a), "POST", "/deposit-accounts", `{"name":"AliceEUR","asset":"EUR","productId":"`+prdOf(t, h, a)+`","identifiers":[{"scheme":"IBAN","value":"SE89-DTO-ALICE-0001"}]}`, http.StatusCreated)["id"].(string)
	bobUSD := doJSON(t, bank(h, b), "POST", "/deposit-accounts", `{"name":"BobUSD","asset":"USD","productId":"`+prdOf(t, h, b)+`"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, bank(h, b), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`","identifiers":[{"scheme":"IBAN","value":"SE89-DTO-BOB-0001"}]}`, http.StatusCreated)["id"].(string)

	// Mandate: derived from the (non-EUR) debtor account. Both ends are USD —
	// CreateMandate refuses a mandate whose two accounts disagree.
	mandate := doJSON(t, csm(h), "POST", "/mandates", `{
		"debtor":{"participant":"`+a+`","account":"`+aliceUSD+`"},
		"creditor":{"participant":"`+b+`","account":"`+bobUSD+`"},
		"maxAmount":50000
	}`, http.StatusCreated)
	assertEqual(t, "created mandate asset", mandate["asset"].(string), "USD")
	mid := mandate["id"].(string)

	var mandates []mandateDTO
	getJSON(t, csm(h), "/mandates", &mandates)
	if len(mandates) != 1 || mandates[0].Asset != "USD" {
		t.Fatalf("GET /mandates asset = %+v, want one USD mandate", mandates)
	}

	got := doJSON(t, csm(h), "GET", "/mandates/"+mid, "", http.StatusOK)
	assertEqual(t, "GET mandate asset", got["asset"].(string), "USD")

	revoked := doJSON(t, csm(h), "POST", "/mandates/"+mid+"/revoke", "", http.StatusOK)
	assertEqual(t, "revoked mandate asset", revoked["asset"].(string), "USD")

	// Scheme: every scheme implemented today settles in EUR (see
	// payment/scheme.go's SCT/SDD) — the field must still be populated, not
	// merely absent-and-unnoticed.
	var schemes []schemeDTO
	getJSON(t, csm(h), "/schemes", &schemes)
	if len(schemes) == 0 {
		t.Fatal("no schemes returned")
	}
	for _, sc := range schemes {
		assertEqual(t, "scheme "+sc.ID+" asset", sc.Asset, "EUR")
	}

	// Cycle and settlement: derived from the scheme.
	doJSON(t, bank(h, a), "POST", "/deposits", `{"account":"`+aliceEUR+`","amount":100000,"description":"opening"}`, http.StatusOK)

	cyc := doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	assertEqual(t, "created cycle asset", cyc["asset"].(string), "EUR")
	cid := cyc["id"].(string)

	var cycles []clearingCycleDTO
	getJSON(t, csm(h), "/cycles", &cycles)
	if len(cycles) != 1 || cycles[0].Asset != "EUR" {
		t.Fatalf("GET /cycles asset = %+v, want one EUR cycle", cycles)
	}

	doJSON(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+aliceEUR+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`","identifier":{"scheme":"IBAN","value":"SE89-DTO-BOB-0001"}},
		"amount":1000,
		"creditorAgent":"BNKBDEFFXXX",
		"creditorName":"Bob"
	}`, http.StatusAccepted)
	drainServer(t, h)

	assertStatus(t, csm(h), "POST", "/cycles/"+cid+"/close", "", http.StatusOK)
	drainServer(t, h)
	sid := settlementOfCycle(t, h, cid)

	var settlements []settlementDTO
	getJSON(t, csm(h), "/settlements", &settlements)
	if len(settlements) != 1 || settlements[0].Asset != "EUR" {
		t.Fatalf("GET /settlements asset = %+v, want one EUR settlement", settlements)
	}

	gotSettlement := doJSON(t, csm(h), "GET", "/settlements/"+sid, "", http.StatusOK)
	assertEqual(t, "GET settlement asset", gotSettlement["asset"].(string), "EUR")
}

// settle discharges a closed cycle, driving the network directly.
//
// There is no HTTP route for it any more, and that is the change rather than a
// gap in this harness. Settling is performed on INSTRUCTION now: the clearing
// house reaches a cut-off, sends a pacs.009, and the central bank's actor
// answers (mesh.centralBank). POST /settlements was the console button that did
// it out of band, and it is gone, because a human settling a cycle beside the
// instruction would be a second way to settle the same one.
//
// The Server does not hold a mesh yet — Task 14 is what puts one behind these
// handlers — so a test that needs a SETTLED cycle to read reaches past the API
// to the network the API is over. Every test that used this used the route for
// the same reason: to reach the state, not to exercise the button.
// settlementOfCycle is the settlement a cut-off produced, read back off the
// cycle.
//
// It replaced a helper that CALLED SettleCycle. Nothing may do that any more:
// closing a cycle is what instructs settlement, and a second discharge beside it
// would be one nobody asked for, racing the one the pacs.009 provoked. Every
// caller drains first, because the central bank answers in an actor of its own.
func settlementOfCycle(t *testing.T, h *Server, cid string) string {
	t.Helper()
	var c clearingCycleDTO
	getJSON(t, csm(h), "/cycles/"+cid, &c)
	if c.Status != "Settled" {
		t.Fatalf("cycle %s is %q, want Settled — the central bank refused the instruction or was never asked", cid, c.Status)
	}
	if c.SettlementID == "" {
		t.Fatalf("cycle %s is Settled and names no settlement", cid)
	}
	return c.SettlementID
}

// TestNoRouteSettlesACycle is the pin on the deleted route, and on the
// difference between the one that replaced it and the one that was deleted.
//
// It is worth a test of its own because nothing else would notice: the console
// that called it addressed a path built from a STRING, so deleting the route
// left every compiler and linter on both sides of the wire perfectly happy and
// the screen 404ing at runtime. A status code asserted here is what makes the
// deletion a decision rather than an accident, on the surface that used to serve
// it and on the two that never did.
//
// It was TestSettlementIsNoLongerAnHTTPAction, and the rename is the finding.
// There IS a POST that leads to a settlement again — POST /cycles/{cid}/settle
// on the clearing house, which re-sends the pacs.009 for a cycle the central
// bank refused, because without it a refusal was terminal. What is still true,
// and is the whole claim, is that no HTTP handler SETTLES: that route moves
// nothing and instructs the one institution that may.
func TestNoRouteSettlesACycle(t *testing.T) {
	h := newServer(t, nil)
	cid := settledCycle(t, h)

	// 405 and not 404 on the two /settlements, and the difference is the point
	// rather than a quirk of the mux: GET /settlements is still there on both
	// operators, so the PATH exists and the METHOD does not. That is exactly
	// what "keep the reads, drop the action" means, said in a status code.
	assertStatus(t, cb(h), "POST", "/settlements", `{"cycleId":"`+cid+`"}`, http.StatusMethodNotAllowed)
	assertStatus(t, csm(h), "POST", "/settlements", `{"cycleId":"`+cid+`"}`, http.StatusMethodNotAllowed)
	// The central bank has no settle route at all: instructing is the clearing
	// house's act, and settling is this operator's but is not an HTTP one.
	assertStatus(t, cb(h), "POST", "/cycles/"+cid+"/settle", "", http.StatusNotFound)
	// And on the clearing house it exists and refuses THIS cycle, which is the
	// assertion that separates it from the route it shares a path with. The
	// pre-split POST /cycles/{cid}/settle settled whatever it was pointed at;
	// this one only re-instructs a cycle that is still Closed, so a cycle that
	// has already settled is 422 and no second pacs.009 is built.
	assertStatus(t, csm(h), "POST", "/cycles/"+cid+"/settle", "", http.StatusUnprocessableEntity)
	var settlementsAfter []settlementDTO
	getJSON(t, csm(h), "/settlements", &settlementsAfter)
	if len(settlementsAfter) != 1 {
		t.Fatalf("asking a settled cycle to settle again produced %d settlements, want the original 1", len(settlementsAfter))
	}

	// The reads it left behind still answer, on both operators. They are what
	// the console watches settlement with now.
	var cycles []clearingCycleDTO
	getJSON(t, cb(h), "/cycles", &cycles)
	if len(cycles) != 1 || cycles[0].ID != cid {
		t.Fatalf("the central bank sees %v, want the one closed cycle %s", cycles, cid)
	}
	// And a settlement it can read, which no request in this test asked for: the
	// cut-off instructed it. That is the shape the deleted POST leaves behind —
	// the console watches settlement rather than performing it.
	var settlements []settlementDTO
	getJSON(t, cb(h), "/settlements", &settlements)
	if len(settlements) != 1 {
		t.Fatalf("the central bank sees %d settlements, want the one the cut-off instructed", len(settlements))
	}
}

// TestARefusedSettlementIsRecoverableOverHTTP walks the whole way out of a
// refused settlement through the console, which is where an operator is.
//
// The mesh-level walk is TestARefusedSettlementCanBeInstructedAgain; this is the
// half that says the operator can actually reach it. Before this route the
// state below was terminal: a cycle Closed with no settlement, its payments
// Cleared, Alice's money in her own bank's clearing suspense and Bob unpaid,
// with no transition out for any object through any route on any surface.
//
// Bank A is short of RESERVES rather than Alice being short of money, and the
// overdraft is how the two are prised apart: cash paid in raises a customer's
// balance and their bank's reserve together, so a funded Alice would mean a
// funded Aurora. Lending her the money instead leaves the bank a net payer of
// 25,000 against a reserve of nothing — a real bank in a real morning.
func TestARefusedSettlementIsRecoverableOverHTTP(t *testing.T) {
	h := newServer(t, nil)
	a := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	b := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKBDEFFXXX","name":"Bank B"}`, http.StatusCreated)["id"].(string)
	alice := doJSON(t, bank(h, a), "POST", "/deposit-accounts",
		`{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`","identifiers":[{"scheme":"IBAN","value":"SE89-SHORT-ALICE-0001"}]}`,
		http.StatusCreated)["id"].(string)
	bob := doJSON(t, bank(h, b), "POST", "/deposit-accounts",
		`{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`","identifiers":[{"scheme":"IBAN","value":"SE89-SHORT-BOB-0001"}]}`,
		http.StatusCreated)["id"].(string)
	doJSON(t, bank(h, a), "POST", "/deposit-accounts/"+alice+"/overdraft-limit", `{"limit":100000}`, http.StatusOK)

	cyc := doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)["id"].(string)
	pay := doJSON(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`","identifier":{"scheme":"IBAN","value":"SE89-SHORT-BOB-0001"}},
		"amount":25000,
		"endToEndId":"short-reserve",
		"creditorAgent":"BNKBDEFFXXX",
		"creditorName":"Bob"
	}`, http.StatusAccepted)["id"].(string)
	drainServer(t, h)
	assertStatus(t, csm(h), "POST", "/cycles/"+cyc+"/close", "", http.StatusOK)
	drainServer(t, h)

	// The stuck state, as the console sees it. The AM04 is nowhere here — it
	// travelled between two actors in a pacs.002 and is stored nowhere — so what
	// an operator reads is a closed cycle with no settlement against it.
	stuck := doJSON(t, csm(h), "GET", "/cycles/"+cyc, "", http.StatusOK)
	if stuck["status"] != "Closed" || stuck["settlementId"] != nil {
		t.Fatalf("cycle = %v, want Closed with no settlement", stuck)
	}
	if got := doJSON(t, csm(h), "GET", "/payments/"+pay, "", http.StatusOK)["status"]; got != "Cleared" {
		t.Fatalf("payment status = %v, want Cleared", got)
	}
	bobBal := doJSON(t, bank(h, b), "GET", "/deposit-accounts/"+bob+"/balance", "", http.StatusOK)
	if got := int64(bobBal["book"].(float64)); got != 0 {
		t.Fatalf("Bob holds %d before settlement, want 0", got)
	}

	// The remedy: fund the short member, then ask the clearing house to instruct
	// settlement again.
	doJSON(t, bank(h, a), "POST", "/deposits", `{"account":"`+alice+`","amount":25000,"description":"reserve top-up"}`, http.StatusOK)
	assertStatus(t, csm(h), "POST", "/cycles/"+cyc+"/settle", "", http.StatusAccepted)
	drainServer(t, h)

	settled := doJSON(t, csm(h), "GET", "/cycles/"+cyc, "", http.StatusOK)
	if settled["status"] != "Settled" || settled["settlementId"] == nil {
		t.Fatalf("cycle = %v, want Settled with a settlement", settled)
	}
	if got := doJSON(t, csm(h), "GET", "/payments/"+pay, "", http.StatusOK)["status"]; got != "Settled" {
		t.Fatalf("payment status = %v, want Settled", got)
	}
	// The assertion that says money arrived rather than that a status changed.
	bobBal = doJSON(t, bank(h, b), "GET", "/deposit-accounts/"+bob+"/balance", "", http.StatusOK)
	if got := int64(bobBal["book"].(float64)); got != 25000 {
		t.Fatalf("Bob holds %d after settlement, want 25000", got)
	}
}

// TestErrorMapping locks one error per HTTP status class.
func TestErrorMapping(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)

	// 404: unknown participant.
	assertStatus(t, bank(h, "nope"), "GET", "/me", "", http.StatusNotFound)

	// 422: withdrawal hold exceeding available balance (account has no funds).
	assertStatus(t, bank(h, pid), "POST", "/deposit-accounts/"+did+"/holds", `{"amount":5000}`, http.StatusUnprocessableEntity)

	// 400: unbalanced transaction.
	gl := doJSON(t, bank(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, bank(h, pid), "POST", "/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, bank(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	other := doJSON(t, bank(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	assertStatus(t, bank(h, pid), "POST", "/transactions", `{
		"entries":[
			{"accountId":"`+acct+`","amount":100,"direction":"Debit"},
			{"accountId":"`+other+`","amount":50,"direction":"Credit"}
		]
	}`, http.StatusBadRequest)

	// 409: duplicate idempotency key.
	body := `{
		"idempotencyKey":"dup-1",
		"entries":[
			{"accountId":"` + acct + `","amount":100,"direction":"Debit"},
			{"accountId":"` + other + `","amount":100,"direction":"Credit"}
		]
	}`
	assertStatus(t, bank(h, pid), "POST", "/transactions", body, http.StatusCreated)
	assertStatus(t, bank(h, pid), "POST", "/transactions", body, http.StatusConflict)

	// 400: invalid enum value.
	assertStatus(t, bank(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Bad","type":"Nonsense"}`, http.StatusBadRequest)

	// 400: a lending sentinel in the malformed-input category. A term past
	// lending.MaxTermMonths is a bad field value, not a business-state
	// conflict, so it maps beside a non-positive amount.
	assertStatus(t, bank(h, pid), "POST", "/facilities", `{
		"kind":"TermLoan","name":"Absurd","asset":"EUR","commitment":1000000,
		"rate":60000,"dayCount":"ACT/365","method":"Annuity","termMonths":100000000
	}`, http.StatusBadRequest)

	// 422: a lending sentinel in the wrong-state category — charging a term
	// loan, which settles interest through its scheduled instalments.
	loan := openTermLoan(t, h, pid, 12)
	assertStatus(t, bank(h, pid), "POST", "/facilities/"+loan["id"].(string)+"/interest-charge", `{"date":"2025-02-15"}`, http.StatusUnprocessableEntity)

	// 409: a billing cycle already on the schedule. The request is valid and
	// the state already reflects it, which is the already-applied category a
	// duplicate idempotency key is in — not a 422.
	line := openRevolvingLine(t, h, pid, 500000)
	lid := line["id"].(string)
	doJSON(t, bank(h, pid), "POST", "/facilities/"+lid+"/draws", `{
		"counterparty":"`+other+`","amount":200000,"description":"draw"
	}`, http.StatusCreated)
	doJSON(t, bank(h, pid), "POST", "/facilities/"+lid+"/interest-charge", `{"date":"2025-02-15"}`, http.StatusOK)
	assertStatus(t, bank(h, pid), "POST", "/facilities/"+lid+"/interest-charge", `{"date":"2025-02-15"}`, http.StatusConflict)
}

// TestAuditEndpointIncludesPayloadAndSeq guards against the DTO silently
// dropping fields again: seq, payload and scope must reach the wire.
func TestAuditEndpointIncludesPayloadAndSeq(t *testing.T) {
	h := newServer(t, nil)

	// AddParticipant posts several ledger audit events as a side effect
	// (ledger + subledgers + accounts), so no further setup is needed.
	doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)

	rec := do(t, bank(h, "bank_1"), "GET", "/audit", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET audit: got status %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var events []auditEventDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &events); err != nil {
		t.Fatalf("decoding body %q: %v", rec.Body.String(), err)
	}

	if len(events) == 0 {
		t.Fatal("no audit events returned")
	}
	if events[0].Seq == 0 {
		t.Error("seq not populated on the wire")
	}
	if len(events[0].Payload) == 0 {
		t.Error("payload not populated on the wire")
	}
	if events[0].Scope == "" {
		t.Error("scope not populated on the wire")
	}
}

func TestAdminReset(t *testing.T) {
	h := newServer(t, nil)

	// emptyList reports whether the /participants body is the empty array.
	emptyList := func() bool {
		b := strings.TrimSpace(do(t, csm(h), "GET", "/members", "").Body.String())
		return b == "[]"
	}

	// Create a participant, confirm it's present.
	doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)
	if emptyList() {
		t.Fatal("expected one participant before reset, got empty list")
	}

	// Reset rebuilds state from the factory (empty in tests).
	doJSON(t, cb(h), "POST", "/admin/reset", "", http.StatusOK)

	if !emptyList() {
		t.Fatal("expected empty participants after reset")
	}
}

// TestResetEmptiesState pins that POST /admin/reset clears the store rather
// than replacing an in-memory object graph.
//
// The distinction is invisible with an empty reseed — everything is gone either
// way — so the server here reseeds a known baseline. A reset that only swapped
// the network would leave "Temp" in the store and the test would see it.
func TestResetEmptiesState(t *testing.T) {
	// The tests' sample dataset: one bank with one customer. Idempotent, like
	// the real one, so booting and resetting are the same call.
	baseline := func(ctx context.Context, net *payment.Network) error {
		existing, err := net.ListParticipants(ctx)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return nil
		}
		p, err := net.AddParticipant(ctx, "Bank A", "BANKDEFFXXX", nil)
		if err != nil {
			return err
		}
		_, err = p.OpenCustomerAccount(ctx, "Baseline", "EUR")
		return err
	}

	srv := newServer(t, baseline)

	names := func() []string {
		var accounts []depositAccountDTO
		getJSON(t, bank(srv, "bank_1"), "/deposit-accounts", &accounts)
		out := make([]string, len(accounts))
		for i, a := range accounts {
			out[i] = a.Name
		}
		return out
	}

	if got := names(); len(got) != 1 || got[0] != "Baseline" {
		t.Fatalf("accounts before the mutation = %v, want [Baseline]", got)
	}

	// Mutate, reset, then assert the mutation is gone and the seed is back.
	doJSON(t, bank(srv, "bank_1"), "POST", "/deposit-accounts", `{"name":"Temp","asset":"EUR","overdraftLimit":0,"productId":"`+prdOf(t, srv, "bank_1")+`"}`, http.StatusCreated)
	if got := names(); len(got) != 2 {
		t.Fatalf("accounts after the mutation = %v, want two", got)
	}

	doJSON(t, cb(srv), "POST", "/admin/reset", "", http.StatusOK)

	got := names()
	for _, name := range got {
		if name == "Temp" {
			t.Fatal("account survived reset")
		}
	}
	if len(got) != 1 || got[0] != "Baseline" {
		t.Fatalf("accounts after reset = %v, want [Baseline]", got)
	}
}

// TestResetSurvivesAClientDisconnect pins that a reset is finished on the
// server's own terms once it has started.
//
// POST /admin/reset TRUNCATEs and then re-seeds. Against store/pg both halves
// are durable, so a client that hangs up in between leaves a permanently
// half-seeded database — and seed.Populate's own idempotency probe then sees
// participants and declines to repair it, so restarting does not help either.
// Under store/mem the old pointer swap made that impossible; durability is what
// makes it stick. The handler therefore detaches the work from the request's
// cancellation.
//
// A pre-cancelled request context is the deterministic form of "the client went
// away": it is the same signal a disconnect delivers, arriving at the earliest
// possible moment.
func TestResetSurvivesAClientDisconnect(t *testing.T) {
	var (
		populateRan bool
		populateCtx error
	)
	baseline := func(ctx context.Context, net *payment.Network) error {
		populateRan = true
		populateCtx = ctx.Err()
		existing, err := net.ListParticipants(ctx)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return nil
		}
		_, err = net.AddParticipant(ctx, "Bank A", "BANKDEFFXXX", nil)
		return err
	}

	srv := newServer(t, baseline)
	reset := cb(srv)

	populateRan, populateCtx = false, nil

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "/admin/reset", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	reset.ServeHTTP(rec, req)

	assertEqual(t, "status", rec.Code, http.StatusOK)
	assertEqual(t, "the reseed ran", populateRan, true)
	if populateCtx != nil {
		t.Fatalf("the reseed inherited the cancelled request context: %v", populateCtx)
	}

	// And the store is whole rather than truncated-but-not-reseeded.
	var parts []participantDTO
	getJSON(t, csm(srv), "/members", &parts)
	assertEqual(t, "participants after the reset", len(parts), 1)
	assertEqual(t, "participant name", parts[0].Name, "Bank A")
}

// TestConcurrentResetsLeaveExactlyOneDataset pins that resets serialize.
//
// A reset is a TRUNCATE followed by a rebuild, and neither half is inside the
// other's unit of work — the rebuild is dozens of separate ones. Two resets
// overlapping therefore interleave: the second truncates over the first's
// half-built scenario, and the first then finishes writing on top of the
// second's, leaving several copies of some entities and none of others.
//
// It became reachable when the reset stopped inheriting the request's
// cancellation: before that, a client that clicked twice produced one reset and
// one cancellation, and the interleaving was hidden by the very bug that made
// a single reset unsafe. Eight resets against a durable store produced twelve
// participants where there should have been four.
func TestConcurrentResetsLeaveExactlyOneDataset(t *testing.T) {
	baseline := func(ctx context.Context, net *payment.Network) error {
		existing, err := net.ListParticipants(ctx)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return nil
		}
		p, err := net.AddParticipant(ctx, "Bank A", "BANKDEFFXXX", nil)
		if err != nil {
			return err
		}
		_, err = p.OpenCustomerAccount(ctx, "Baseline", "EUR")
		return err
	}

	h := newServer(t, baseline)
	reset := cb(h)

	// A start barrier, so the requests genuinely overlap rather than each
	// finishing before the next goroutine is scheduled — the failure mode that
	// makes a concurrency test pass while proving nothing.
	const resets = 8
	start := make(chan struct{})
	codes := make([]int, resets)
	var wg sync.WaitGroup
	for i := range resets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			rec := httptest.NewRecorder()
			reset.ServeHTTP(rec, httptest.NewRequest("POST", "/admin/reset", nil))
			codes[i] = rec.Code
		}()
	}
	close(start)
	wg.Wait()

	for i, code := range codes {
		assertEqual(t, fmt.Sprintf("reset %d status", i), code, http.StatusOK)
	}

	var parts []participantDTO
	getJSON(t, csm(h), "/members", &parts)
	assertEqual(t, "participants after concurrent resets", len(parts), 1)

	var accounts []depositAccountDTO
	getJSON(t, bank(h, parts[0].ID), "/deposit-accounts", &accounts)
	assertEqual(t, "deposit accounts after concurrent resets", len(accounts), 1)
	assertEqual(t, "deposit account name", accounts[0].Name, "Baseline")
}

// ---------------------------------------------------------------------------
// Text validation
// ---------------------------------------------------------------------------

// TestControlCharactersAreRefusedNotStored pins the one rule store/storetest
// exists to enforce, at the level a client can actually reach it: store/pg must
// never accept or refuse a write that store/mem handles differently.
//
// `{"name":"Ban\u0000k"}` is legal JSON. store/mem stored it and answered 201;
// store/pg could not (a NUL is SQLSTATE 22021 in a text column and 22P05 inside
// jsonb) and answered 500 with the raw SQLSTATE in the body. The rule is now a
// domain rule — see ledger.ValidateText — so both stores answer 400.
//
// This test runs against whichever store TEST_DATABASE_URL selects, so "both
// stores agree" is checked by running it twice rather than asserted once.
func TestControlCharactersAreRefusedNotStored(t *testing.T) {
	h := newServer(t, nil)

	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	doJSON(t, bank(h, pid), "POST", "/deposits", `{"account":"`+did+`","amount":100000,"description":"opening"}`, http.StatusOK)
	gl := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	// nul and esc are JSON escapes — six characters in this source file, one
	// control character by the time encoding/json is done with them. That is
	// what a client actually sends, and it is why no amount of byte-level
	// screening of the request body would catch it.
	const (
		nul = `\u0000`
		esc = `\u001b`
	)

	entries := func(first string) string {
		return `"entries":[{"accountId":"` + first + `","amount":100,"direction":"Debit"},` +
			`{"accountId":"` + gl + `","amount":100,"direction":"Credit"}]`
	}

	for _, tc := range []struct{ what, method, path, body string }{
		{"a participant name", "POST", "/members", `{"bic":"BANKDEFFXXX","name":"Ban` + nul + `k"}`},
		{"a participant name with an escape sequence", "POST", "/members", `{"bic":"BANKDEFFXXX","name":"Bank` + esc + `[31m"}`},
		{"a participant name with a newline", "POST", "/members", `{"bic":"BANKDEFFXXX","name":"Bank\nof Nowhere"}`},
		{"a deposit account name", "POST", "/deposit-accounts",
			`{"name":"Al` + nul + `ice","overdraftLimit":0}`},
		{"a ledger name", "POST", "/ledgers", `{"name":"G` + nul + `L"}`},
		{"a funding description", "POST", "/deposits",
			`{"account":"` + did + `","amount":1000,"description":"op` + nul + `ening"}`},
		{"a funding account id", "POST", "/deposits",
			`{"account":"` + did + nul + `","amount":1000,"description":"opening"}`},
		{"a hold description", "POST", "/deposit-accounts/" + did + "/holds",
			`{"amount":1000,"description":"pre-a` + nul + `uth"}`},
		{"a transaction description", "POST", "/transactions",
			`{"description":"re` + nul + `nt",` + entries(gl) + `}`},
		{"a transaction idempotency key", "POST", "/transactions",
			`{"idempotencyKey":"key` + nul + `-1",` + entries(gl) + `}`},
		{"a transaction metadata value", "POST", "/transactions",
			`{"metadata":{"ref":"INV` + nul + `"},` + entries(gl) + `}`},
		{"an entry account id", "POST", "/transactions",
			`{` + entries(gl+nul) + `}`},
	} {
		// Admission is the central bank's; every other path here is this
		// bank's own, and its port has already named it.
		target := bank(h, pid)
		if strings.HasPrefix(tc.path, "/members") {
			target = cb(h)
		}
		rec := do(t, target, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got status %d, want 400 (body: %s)", tc.what, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}

	// Nothing was created by any of the refused requests.
	var parts []participantDTO
	getJSON(t, csm(h), "/members", &parts)
	assertEqual(t, "participants after the refusals", len(parts), 1)

	var accounts []depositAccountDTO
	getJSON(t, bank(h, pid), "/deposit-accounts", &accounts)
	assertEqual(t, "deposit accounts after the refusals", len(accounts), 1)
}

// TestControlCharactersInAPathAreRefused pins the read half of the same rule.
// A URL-encoded NUL is decoded into the path parameter and handed straight to
// the store as a lookup key: store/mem answers 404, store/pg answers 500 with a
// raw SQLSTATE. Identifiers here are system-generated and never contain a
// control character, so such a request cannot name anything and is refused at
// the edge.
func TestControlCharactersInAPathAreRefused(t *testing.T) {
	h := newServer(t, nil)

	// A well-formed but unknown id is still an honest 404.
	assertStatus(t, bank(h, "bank_404"), "GET", "/me", "", http.StatusNotFound)

	assertStatus(t, csm(h), "GET", "/participants/bank%001", "", http.StatusBadRequest)
	assertStatus(t, bank(h, "bank_1"), "GET", "/deposit-accounts/dep%001", "", http.StatusBadRequest)
	assertStatus(t, csm(h), "GET", "/payments/audit?entity=pay%001", "", http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// Audit endpoints
// ---------------------------------------------------------------------------

// auditFixture runs one payment end to end, so the log holds every scope: the
// network's own payment-scope events, both banks' ledger-scope events, their
// deposit-scope events and the central bank's. It returns the two participant
// IDs and the payment ID.
func auditFixture(t *testing.T, h *Server) (bankA, bankB, payID string) {
	t.Helper()
	a := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	b := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKBDEFFXXX","name":"Bank B"}`, http.StatusCreated)["id"].(string)
	// SCT addresses both legs by IBAN (payment.Scheme.AddressedBy), so both
	// accounts need one before submission will accept them.
	alice := doJSON(t, bank(h, a), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`","identifiers":[{"scheme":"IBAN","value":"SE89-AUDIT-ALICE-0001"}]}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, bank(h, b), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`","identifiers":[{"scheme":"IBAN","value":"SE89-AUDIT-BOB-0001"}]}`, http.StatusCreated)["id"].(string)
	doJSON(t, bank(h, a), "POST", "/deposits", `{"account":"`+alice+`","amount":100000,"description":"opening"}`, http.StatusOK)

	cyc := doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)["id"].(string)
	pay := doJSON(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`","identifier":{"scheme":"IBAN","value":"SE89-AUDIT-BOB-0001"}},
		"amount":25000,
		"creditorAgent":"BNKBDEFFXXX",
		"creditorName":"Bob"
	}`, http.StatusAccepted)["id"].(string)
	drainServer(t, h)
	assertStatus(t, csm(h), "POST", "/cycles/"+cyc+"/close", "", http.StatusOK)
	drainServer(t, h)
	return a, b, pay
}

func auditTypes(events []auditEventDTO) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}

// TestAuditFilterParsesAndCapsLimits pins the query-parameter contract at the
// one place it is implemented. The cap matters: a durable log is unbounded, so
// an unbounded ?limit= is a way to ask the server to read the whole table.
func TestAuditFilterParsesAndCapsLimits(t *testing.T) {
	filterFor := func(query string) ledger.AuditFilter {
		r := httptest.NewRequest("GET", "/payments/audit"+query, nil)
		return auditFilter(r, ledger.NetworkBook, ledger.ScopePayment)
	}

	// The route decides the book and the scope; the client never can.
	base := filterFor("")
	assertEqual(t, "book", base.BookID, ledger.NetworkBook)
	assertEqual(t, "scope", base.Scope, ledger.ScopePayment)
	assertEqual(t, "default limit", base.Limit, 100)
	assertEqual(t, "default before", base.Before, int64(0))
	assertEqual(t, "default type", base.Type, "")
	assertEqual(t, "default entity", base.EntityID, "")

	assertEqual(t, "explicit limit", filterFor("?limit=7").Limit, 7)
	assertEqual(t, "limit at the cap", filterFor("?limit=1000").Limit, 1000)
	assertEqual(t, "limit above the cap", filterFor("?limit=1001").Limit, 1000)
	assertEqual(t, "absurd limit", filterFor("?limit=99999").Limit, 1000)

	// Anything that is not a positive integer falls back to the default rather
	// than becoming an unbounded read.
	assertEqual(t, "zero limit", filterFor("?limit=0").Limit, 100)
	assertEqual(t, "negative limit", filterFor("?limit=-5").Limit, 100)
	assertEqual(t, "garbage limit", filterFor("?limit=lots").Limit, 100)
	assertEqual(t, "empty limit", filterFor("?limit=").Limit, 100)

	assertEqual(t, "before", filterFor("?before=42").Before, int64(42))
	assertEqual(t, "negative before", filterFor("?before=-1").Before, int64(0))
	assertEqual(t, "garbage before", filterFor("?before=soon").Before, int64(0))

	assertEqual(t, "type", filterFor("?type=cycle.closed").Type, ledger.EventCycleClosed)
	assertEqual(t, "entity", filterFor("?entity=pay_1").EntityID, "pay_1")
}

// TestPaymentAuditRecordsTheLifecycle pins the payment layer's event stream:
// the layer had no audit trail at all, so this is the whole of it.
func TestPaymentAuditRecordsTheLifecycle(t *testing.T) {
	h := newServer(t, nil)
	_, _, payID := auditFixture(t, h)

	var events []auditEventDTO
	getJSON(t, csm(h), "/payments/audit?limit=1000", &events)

	want := []string{
		ledger.EventParticipantAdded, // Bank A
		ledger.EventParticipantAdded, // Bank B
		ledger.EventCycleOpened,
		ledger.EventPaymentInitiated,
		ledger.EventPaymentAccepted,
		ledger.EventPaymentCleared, // one per payment in the cycle
		ledger.EventCycleClosed,
		ledger.EventPaymentSettled, // one per payment in the cycle
		ledger.EventCycleSettled,
	}
	got := auditTypes(events)
	assertEqual(t, "event stream", strings.Join(got, " "), strings.Join(want, " "))

	// Every event is network-scoped and carries a payload and a Seq.
	for _, e := range events {
		assertEqual(t, "scope of "+e.Type, e.Scope, string(ledger.ScopePayment))
		if e.Seq == 0 {
			t.Fatalf("%s has no seq", e.Type)
		}
		if len(e.Payload) == 0 {
			t.Fatalf("%s has no payload", e.Type)
		}
	}

	// Entity IDs point at the thing that changed, so ?entity= works.
	var forPayment []auditEventDTO
	getJSON(t, csm(h), "/payments/audit?entity="+payID, &forPayment)
	assertEqual(t, "events for the payment", strings.Join(auditTypes(forPayment), " "),
		strings.Join([]string{
			ledger.EventPaymentInitiated,
			ledger.EventPaymentAccepted,
			ledger.EventPaymentCleared,
			ledger.EventPaymentSettled,
		}, " "))

	// ?type= narrows to one event type.
	var cleared []auditEventDTO
	getJSON(t, csm(h), "/payments/audit?type="+ledger.EventPaymentCleared, &cleared)
	assertEqual(t, "cleared events", len(cleared), 1)
	assertEqual(t, "cleared entity", cleared[0].EntityID, payID)
}

// TestAuditRejectedAndReturnedPayments covers the two lifecycle branches the
// happy path never reaches.
func TestAuditRejectedAndReturnedPayments(t *testing.T) {
	h := newServer(t, nil)
	a, b, payID := auditFixture(t, h)

	// A settled payment can be returned.
	doJSON(t, csm(h), "POST", "/payments/"+payID+"/return", `{"reason":"AC04"}`, http.StatusAccepted)
	drainServer(t, h)

	// A fresh payment in a fresh cycle can be rejected before it clears.
	var aAccounts, bAccounts []depositAccountDTO
	getJSON(t, bank(h, a), "/deposit-accounts", &aAccounts)
	getJSON(t, bank(h, b), "/deposit-accounts", &bAccounts)
	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	second := doJSON(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+aAccounts[0].ID+`"},
		"creditor":{"participant":"`+b+`","account":"`+bAccounts[0].ID+`","identifier":{"scheme":"IBAN","value":"SE89-AUDIT-BOB-0001"}},
		"amount":1000,
		"creditorAgent":"BNKBDEFFXXX",
		"creditorName":"Bob"
	}`, http.StatusAccepted)["id"].(string)
	drainServer(t, h)
	doJSON(t, csm(h), "POST", "/payments/"+second+"/reject", `{"reason":"AM05"}`, http.StatusAccepted)
	drainServer(t, h)

	var returned []auditEventDTO
	getJSON(t, csm(h), "/payments/audit?type="+ledger.EventPaymentReturned, &returned)
	assertEqual(t, "returned events", len(returned), 1)
	assertEqual(t, "returned entity", returned[0].EntityID, payID)

	var rejected []auditEventDTO
	getJSON(t, csm(h), "/payments/audit?type="+ledger.EventPaymentRejected, &rejected)
	assertEqual(t, "rejected events", len(rejected), 1)
	assertEqual(t, "rejected entity", rejected[0].EntityID, second)
}

// TestRejectPaymentRendersItsCode pins that a rejection's external
// status-reason code — not just its free text — reaches the wire: on the
// reject response itself, and on every later read of the payment.
// handleRejectPayment always attaches MS03 (StatusReasonNotSpecifiedAgentGenerated) —
// the API exposes no way for a caller to name a more specific one — so that is
// the value pinned here.
func TestRejectPaymentRendersItsCode(t *testing.T) {
	h := newServer(t, nil)
	a, b, _ := auditFixture(t, h)

	var aAccounts, bAccounts []depositAccountDTO
	getJSON(t, bank(h, a), "/deposit-accounts", &aAccounts)
	getJSON(t, bank(h, b), "/deposit-accounts", &bAccounts)
	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	payID := doJSON(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+aAccounts[0].ID+`"},
		"creditor":{"participant":"`+b+`","account":"`+bAccounts[0].ID+`","identifier":{"scheme":"IBAN","value":"SE89-AUDIT-BOB-0001"}},
		"amount":1000,
		"creditorAgent":"BNKBDEFFXXX",
		"creditorName":"Bob"
	}`, http.StatusAccepted)["id"].(string)
	drainServer(t, h)

	rejected := doJSON(t, csm(h), "POST", "/payments/"+payID+"/reject", `{"reason":"card lost"}`, http.StatusAccepted)
	assertEqual(t, "reject code on the reject response", rejected["rejectCode"].(string), "MS03")
	assertEqual(t, "reject reason on the reject response", rejected["rejectReason"].(string), "card lost")

	reread := doJSON(t, csm(h), "GET", "/payments/"+payID, "", http.StatusOK)
	assertEqual(t, "reject code on a later read", reread["rejectCode"].(string), "MS03")
	assertEqual(t, "reject reason on a later read", reread["rejectReason"].(string), "card lost")
}

// TestRejectPaymentGivesThePayerTheirMoneyBack pins the half of a rejection
// that is not the clearing house's: the payer's bank reverses the leg it
// posted.
//
// It is a different half from the one it used to be. The route ran both in one
// unit of work, so the money was back before the response was written; the
// refund is now the payer's bank's own act, in its own book, and it happens when
// the pacs.002 gets there. The reading after the drain is what says so, and
// without this test the whole debtor-bank half could go missing and every other
// api assertion would still pass: the payment DTO the handler answers with is
// produced by the clearing house's half alone.
func TestRejectPaymentGivesThePayerTheirMoneyBack(t *testing.T) {
	h := newServer(t, nil)
	a, b, _ := auditFixture(t, h)

	var aAccounts, bAccounts []depositAccountDTO
	getJSON(t, bank(h, a), "/deposit-accounts", &aAccounts)
	getJSON(t, bank(h, b), "/deposit-accounts", &bAccounts)
	payer := aAccounts[0].ID
	bookOf := func() int64 {
		t.Helper()
		bal := doJSON(t, bank(h, a), "GET", "/deposit-accounts/"+payer+"/balance", "", http.StatusOK)
		return int64(bal["book"].(float64))
	}
	before := bookOf()

	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	payID := doJSON(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+aAccounts[0].ID+`"},
		"creditor":{"participant":"`+b+`","account":"`+bAccounts[0].ID+`","identifier":{"scheme":"IBAN","value":"SE89-AUDIT-BOB-0001"}},
		"amount":1000,
		"creditorAgent":"BNKBDEFFXXX",
		"creditorName":"Bob"
	}`, http.StatusAccepted)["id"].(string)
	drainServer(t, h)
	assertEqual(t, "payer's book balance after submission", bookOf(), before-1000)

	doJSON(t, csm(h), "POST", "/payments/"+payID+"/reject", `{"reason":"card lost"}`, http.StatusAccepted)
	drainServer(t, h)
	assertEqual(t, "payer's book balance after the rejection", bookOf(), before)
}

// TestARejectionWhoseRefundFailsStandsAndIsDeadLettered pins what replaced the
// route's composite, and it is the opposite property.
//
// api used to run both halves of a rejection on ONE transaction, so a reversal
// that failed took the clearing house's transition down with it and no caller
// could read a Rejected payment whose money was still in suspense. Two
// institutions cannot share a transaction. The clearing house rejects in its own
// unit of work and the payer's bank reverses in its own, an actor and a message
// later — so the half-happened outcome RejectAtCSMTx's doc describes is now
// REACHABLE, which is the honest shape and the thing to pin.
//
// What must not happen is that it passes quietly. Nobody is left to answer: the
// operator's request was answered 202 before the pacs.002 was even sent. So the
// refund's failure becomes a mesh dead letter, and Drain is what hands it back.
//
// The forced failure is a leg that has already been reversed, which is what a
// retried rejection produces. It is set up through the network because no route
// can reverse a leg on its own.
func TestARejectionWhoseRefundFailsStandsAndIsDeadLettered(t *testing.T) {
	ctx := context.Background()
	h := newServer(t, nil)
	a, b, _ := auditFixture(t, h)

	var aAccounts, bAccounts []depositAccountDTO
	getJSON(t, bank(h, a), "/deposit-accounts", &aAccounts)
	getJSON(t, bank(h, b), "/deposit-accounts", &bAccounts)
	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	payID := doJSON(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+aAccounts[0].ID+`"},
		"creditor":{"participant":"`+b+`","account":"`+bAccounts[0].ID+`","identifier":{"scheme":"IBAN","value":"SE89-AUDIT-BOB-0001"}},
		"amount":1000,
		"creditorAgent":"BNKBDEFFXXX",
		"creditorName":"Bob"
	}`, http.StatusAccepted)["id"].(string)
	drainServer(t, h)

	p, err := h.network().GetPayment(ctx, payment.PaymentID(payID))
	if err != nil {
		t.Fatalf("get payment: %v", err)
	}
	if err := h.network().ReverseDebtorLeg(ctx, p, "reversed already"); err != nil {
		t.Fatalf("reverse the leg out from under the payer's bank: %v", err)
	}

	// The clearing house's half is answerable and succeeds, so the operator is
	// told 202 and the payment really is Rejected.
	rejected := doJSON(t, csm(h), "POST", "/payments/"+payID+"/reject", `{"reason":"card lost"}`, http.StatusAccepted)
	assertEqual(t, "status in the rejection's response", rejected["status"].(string), "Rejected")

	// The payer's bank's half is not, and fails. It reaches nobody over HTTP
	// and everybody through Drain.
	err = h.mesh.Drain(ctx)
	if err == nil {
		t.Fatal("the refund failed and the mesh reported nothing; a swallowed dead letter is a payer silently left short")
	}
	if !strings.Contains(err.Error(), ledger.ErrTransactionAlreadyReversed.Error()) {
		t.Fatalf("dead letter = %v, want one naming the already-reversed leg", err)
	}

	// And the rejection stands, because it happened.
	reread := doJSON(t, csm(h), "GET", "/payments/"+payID, "", http.StatusOK)
	assertEqual(t, "status after the failed refund", reread["status"].(string), "Rejected")
	assertEqual(t, "reason after the failed refund", reread["rejectReason"].(string), "card lost")
}

// TestAuditMandateEvents covers the two mandate events, which no payment flow
// emits on its own.
func TestAuditMandateEvents(t *testing.T) {
	h := newServer(t, nil)
	a, b, _ := auditFixture(t, h)

	var aAccounts, bAccounts []depositAccountDTO
	getJSON(t, bank(h, a), "/deposit-accounts", &aAccounts)
	getJSON(t, bank(h, b), "/deposit-accounts", &bAccounts)

	mid := doJSON(t, csm(h), "POST", "/mandates", `{
		"debtor":{"participant":"`+a+`","account":"`+aAccounts[0].ID+`"},
		"creditor":{"participant":"`+b+`","account":"`+bAccounts[0].ID+`"},
		"maxAmount":50000
	}`, http.StatusCreated)["id"].(string)
	doJSON(t, csm(h), "POST", "/mandates/"+mid+"/revoke", "", http.StatusOK)

	var events []auditEventDTO
	getJSON(t, csm(h), "/payments/audit?entity="+mid, &events)
	assertEqual(t, "mandate events", strings.Join(auditTypes(events), " "),
		strings.Join([]string{ledger.EventMandateCreated, ledger.EventMandateRevoked}, " "))
}

// TestAuditRoutesAreScoped pins that the four endpoints are four filters over
// one log: each returns its own scope and its own book, and nothing else.
func TestAuditRoutesAreScoped(t *testing.T) {
	h := newServer(t, nil)
	a, b, _ := auditFixture(t, h)

	// Four filters over one log — and, since the split, on three different
	// operators: a bank's two are its own, the reserve movements are the
	// central bank's, and the payment log is the clearing house's. That each
	// still answers with exactly one scope is what this pins.
	cases := []struct {
		op        http.Handler
		path      string
		wantScope string
	}{
		{bank(h, a), "/audit", string(ledger.ScopeLedger)},
		{bank(h, a), "/deposit-audit", string(ledger.ScopeDeposit)},
		{cb(h), "/audit", string(ledger.ScopeLedger)},
		{csm(h), "/payments/audit", string(ledger.ScopePayment)},
	}
	for _, c := range cases {
		var events []auditEventDTO
		getJSON(t, c.op, c.path+"?limit=1000", &events)
		if len(events) == 0 {
			t.Fatalf("%s returned no events", c.path)
		}
		for _, e := range events {
			assertEqual(t, c.path+" scope", e.Scope, c.wantScope)
		}
	}

	// Scope alone does not identify a route: bank A's log, bank B's log and the
	// central bank's are all ledger-scoped, so a route that served the wrong
	// BOOK would still pass the loop above. Every pair of logs must be disjoint.
	logs := map[string][]auditEventDTO{}
	// Keyed by a name rather than by the path, because "/audit" now names three
	// different logs depending on which operator you ask.
	for _, c := range []struct {
		name string
		op   http.Handler
		path string
	}{
		{"bank A ledger", bank(h, a), "/audit"},
		{"bank A deposit", bank(h, a), "/deposit-audit"},
		{"bank B ledger", bank(h, b), "/audit"},
		{"central bank", cb(h), "/audit"},
		{"payments", csm(h), "/payments/audit"},
	} {
		var events []auditEventDTO
		getJSON(t, c.op, c.path+"?limit=1000", &events)
		if len(events) == 0 {
			t.Fatalf("%s returned no events", c.name)
		}
		logs[c.name] = events
	}
	for lhs, left := range logs {
		seen := map[int64]bool{}
		for _, e := range left {
			seen[e.Seq] = true
		}
		for rhs, right := range logs {
			if lhs == rhs {
				continue
			}
			for _, e := range right {
				if seen[e.Seq] {
					t.Fatalf("seq %d (%s) appears in both %s and %s", e.Seq, e.Type, lhs, rhs)
				}
			}
		}
	}

	// A positive identification of the central bank's book, so the disjointness
	// above cannot be satisfied by serving some other log that merely happens
	// not to overlap: the cycle's settlement transaction is posted in the
	// central bank's book and nowhere else, so exactly one log may contain it.
	const settlementMarker = "Settlement of clearing cycle"
	for name, events := range logs {
		found := false
		for _, e := range events {
			if strings.Contains(string(e.Payload), settlementMarker) {
				found = true
			}
		}
		if want := name == "central bank"; found != want {
			t.Fatalf("the %s log contains the settlement transaction: got %v, want %v", name, found, want)
		}
	}
}

// TestAuditPaginationByCursor pins the backwards pager.
//
// Seq is a store-GLOBAL total order, not per book or per scope, so this is the
// case that matters: the payment-scope events are interleaved with hundreds of
// ledger- and deposit-scope ones, and ?before= must mean "the next page of THIS
// filter", not "the next few sequence numbers".
func TestAuditPaginationByCursor(t *testing.T) {
	h := newServer(t, nil)
	auditFixture(t, h)

	var all []auditEventDTO
	getJSON(t, csm(h), "/payments/audit?limit=1000", &all)
	if len(all) < 5 {
		t.Fatalf("fixture produced %d payment events, want at least 5", len(all))
	}

	// Walk the whole log backwards two at a time and reassemble it.
	var walked []auditEventDTO
	cursor := ""
	for range len(all) {
		var page []auditEventDTO
		getJSON(t, csm(h), "/payments/audit?limit=2"+cursor, &page)
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			assertEqual(t, "page scope", e.Scope, string(ledger.ScopePayment))
		}
		for _, w := range walked {
			for _, e := range page {
				if e.Seq == w.Seq {
					t.Fatalf("seq %d appears on two pages", e.Seq)
				}
			}
		}
		walked = append(page, walked...)
		// Page backwards from the OLDEST event on the page: a page is handed
		// back oldest-first, and Before is an exclusive upper bound.
		cursor = fmt.Sprintf("&before=%d", page[0].Seq)
	}

	assertEqual(t, "walked count", len(walked), len(all))
	for i := range all {
		assertEqual(t, fmt.Sprintf("walked event %d", i), walked[i].Seq, all[i].Seq)
	}

	// A cursor below every match is an empty page, not an error and not a
	// wraparound to the newest events.
	var empty []auditEventDTO
	getJSON(t, csm(h), fmt.Sprintf("/payments/audit?limit=2&before=%d", all[0].Seq), &empty)
	assertEqual(t, "page below the oldest event", len(empty), 0)
}

// TestAuditDefaultLimitApplies pins that a log longer than one page is
// truncated to the newest 100 events rather than returned whole.
func TestAuditDefaultLimitApplies(t *testing.T) {
	h := newServer(t, nil)

	// Each open/close pair is two payment-scope events; 51 pairs clears 100.
	// No drain between closes, and the exact count at :1605 depends on that
	// being safe: every cycle here nets to nothing (no payment was ever put in
	// it), and instructSettlement declines to send a pacs.009 for an empty net
	// (mesh/csm.go:550-552) — so no settlement chain starts and no third event
	// per pair ever lands. A fixture change that puts one payment into any of
	// these cycles turns this exact count into a race against that chain.
	for range 51 {
		cyc := doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)["id"].(string)
		assertStatus(t, csm(h), "POST", "/cycles/"+cyc+"/close", "", http.StatusOK)
	}

	var full []auditEventDTO
	getJSON(t, csm(h), "/payments/audit?limit=1000", &full)
	assertEqual(t, "events in the log", len(full), 102)

	var page []auditEventDTO
	getJSON(t, csm(h), "/payments/audit", &page)
	assertEqual(t, "default page size", len(page), 100)
	// The default page is the NEWEST 100, still oldest-first.
	assertEqual(t, "default page ends at the newest event", page[99].Seq, full[101].Seq)
	assertEqual(t, "default page starts 100 back", page[0].Seq, full[2].Seq)
}

// TestAuditLimitIsCapped pins that an oversized ?limit= is bounded rather than
// honoured. The cap itself is asserted exactly in
// TestAuditFilterParsesAndCapsLimits; this is the end-to-end half.
func TestAuditLimitIsCapped(t *testing.T) {
	h := newServer(t, nil)
	auditFixture(t, h)

	var events []auditEventDTO
	getJSON(t, csm(h), "/payments/audit?limit=99999", &events)
	if len(events) == 0 {
		t.Fatal("no events returned")
	}
	if len(events) > 1000 {
		t.Fatalf("returned %d events, want <= 1000", len(events))
	}
}

// ---------------------------------------------------------------------------
// Lending and overdraft credit (Task 16)
// ---------------------------------------------------------------------------

// openTermLoan opens a EUR term loan at 6% ACT/365, annuity method, over
// termMonths instalments, and returns the decoded facility.
func openTermLoan(t *testing.T, h *Server, pid string, termMonths int) map[string]any {
	t.Helper()
	return doJSON(t, bank(h, pid), "POST", "/facilities", fmt.Sprintf(`{
		"kind":"TermLoan","name":"Home Loan","asset":"EUR","commitment":1000000,
		"rate":60000,"dayCount":"ACT/365","method":"Annuity","termMonths":%d
	}`, termMonths), http.StatusCreated)
}

// openRevolvingLine opens a EUR revolving line at 18% ACT/365 with a 10%
// minimum-payment fraction, and returns the decoded facility.
func openRevolvingLine(t *testing.T, h *Server, pid string, commitment int64) map[string]any {
	t.Helper()
	return doJSON(t, bank(h, pid), "POST", "/facilities", fmt.Sprintf(`{
		"kind":"RevolvingLine","name":"Credit Line","asset":"EUR","commitment":%d,
		"rate":180000,"dayCount":"ACT/365","minPayment":100000
	}`, commitment), http.StatusCreated)
}

// TestOpenTermLoanAndRevolvingLine pins that one route opens either product,
// dispatched by the request's Kind field, and that a freshly-opened facility
// reports zero drawn and zero accrued without a further round trip.
func TestOpenTermLoanAndRevolvingLine(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)

	loan := openTermLoan(t, h, pid, 12)
	assertEqual(t, "loan kind", loan["kind"].(string), "TermLoan")
	assertEqual(t, "loan status", loan["status"].(string), "Pending")
	assertEqual(t, "loan drawn", int64(loan["drawn"].(float64)), int64(0))
	assertEqual(t, "loan accruedInterest", int64(loan["accruedInterest"].(float64)), int64(0))
	assertEqual(t, "loan rateScale", int64(loan["rateScale"].(float64)), int64(1_000_000))
	assertEqual(t, "loan method", loan["method"].(string), "Annuity")

	line := openRevolvingLine(t, h, pid, 500000)
	assertEqual(t, "line kind", line["kind"].(string), "RevolvingLine")
	assertEqual(t, "line status", line["status"].(string), "Pending")
	if _, ok := line["method"]; ok {
		t.Errorf("revolving line carries a method field: %v", line["method"])
	}

	var facilities []facilityDTO
	getJSON(t, bank(h, pid), "/facilities", &facilities)
	if len(facilities) != 2 {
		t.Fatalf("facilities = %v, want 2", facilities)
	}
}

// TestOpenFacilityRequiresAsset mirrors TestOpenDepositAccountRequiresAsset: a
// facility's asset decides which asset its two GL accounts are denominated in,
// and nothing here may pick one on the caller's behalf.
func TestOpenFacilityRequiresAsset(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, bank(h, pid), "POST", "/facilities", `{
		"kind":"TermLoan","name":"No Asset","commitment":100000,"rate":50000,
		"dayCount":"ACT/365","method":"Annuity","termMonths":12
	}`, http.StatusBadRequest)
}

// TestOpenFacilityRejectsUnknownEnums pins that Kind, DayCount and Method are
// parsed from their string forms exactly as the ledger handlers parse an
// account type: an unknown value is a 400 before the domain is ever called.
func TestOpenFacilityRejectsUnknownEnums(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, bank(h, pid), "POST", "/facilities", `{
		"kind":"Nonsense","name":"X","asset":"EUR","commitment":100000,"rate":50000,"dayCount":"ACT/365"
	}`, http.StatusBadRequest)

	assertStatus(t, bank(h, pid), "POST", "/facilities", `{
		"kind":"TermLoan","name":"X","asset":"EUR","commitment":100000,"rate":50000,
		"dayCount":"Nonsense","method":"Annuity","termMonths":12
	}`, http.StatusBadRequest)

	assertStatus(t, bank(h, pid), "POST", "/facilities", `{
		"kind":"TermLoan","name":"X","asset":"EUR","commitment":100000,"rate":50000,
		"dayCount":"ACT/365","method":"Nonsense","termMonths":12
	}`, http.StatusBadRequest)
}

// TestDisburseTermLoanGeneratesSixtyRowSchedule covers disbursement end to
// end: the counterparty's balance moves, the facility becomes Active and
// reports its drawn principal, and a 60-month term generates 60 instalments.
// A second disbursement is refused.
func TestDisburseTermLoanGeneratesSixtyRowSchedule(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	loan := openTermLoan(t, h, pid, 60)
	fid := loan["id"].(string)

	tx := doJSON(t, bank(h, pid), "POST", "/facilities/"+fid+"/disbursement", `{
		"counterparty":"`+gl+`","firstDue":"2025-02-15","description":"payout"
	}`, http.StatusCreated)
	if tx["id"] == nil || tx["id"].(string) == "" {
		t.Fatalf("disbursement did not return a transaction: %v", tx)
	}

	var schedule []installmentDTO
	getJSON(t, bank(h, pid), "/facilities/"+fid+"/schedule", &schedule)
	assertEqual(t, "schedule length", len(schedule), 60)

	bal := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did+"/balance", "", http.StatusOK)
	assertEqual(t, "book balance after disbursement", int64(bal["book"].(float64)), int64(1000000))

	got := doJSON(t, bank(h, pid), "GET", "/facilities/"+fid, "", http.StatusOK)
	assertEqual(t, "drawn after disbursement", int64(got["drawn"].(float64)), int64(1000000))
	assertEqual(t, "status after disbursement", got["status"].(string), "Active")

	assertStatus(t, bank(h, pid), "POST", "/facilities/"+fid+"/disbursement", `{
		"counterparty":"`+gl+`","firstDue":"2025-02-15","description":"again"
	}`, http.StatusUnprocessableEntity)
}

// TestDrawPastCommitmentReturns422 covers a revolving line's draw path: a
// draw within the commitment succeeds, a term loan refuses to be drawn at all
// (ErrWrongFacilityKind), and a draw that would exceed the commitment is
// refused without moving the drawn balance.
func TestDrawPastCommitmentReturns422(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	line := openRevolvingLine(t, h, pid, 100000)
	fid := line["id"].(string)

	assertStatus(t, bank(h, pid), "POST", "/facilities/"+fid+"/draws", `{
		"counterparty":"`+gl+`","amount":60000,"description":"draw 1"
	}`, http.StatusCreated)

	loan := openTermLoan(t, h, pid, 12)
	assertStatus(t, bank(h, pid), "POST", "/facilities/"+loan["id"].(string)+"/draws", `{
		"counterparty":"`+gl+`","amount":1000,"description":"nope"
	}`, http.StatusUnprocessableEntity)

	assertStatus(t, bank(h, pid), "POST", "/facilities/"+fid+"/draws", `{
		"counterparty":"`+gl+`","amount":60000,"description":"draw 2"
	}`, http.StatusUnprocessableEntity)

	got := doJSON(t, bank(h, pid), "GET", "/facilities/"+fid, "", http.StatusOK)
	assertEqual(t, "drawn after the refused draw", int64(got["drawn"].(float64)), int64(60000))
}

// TestRepayFromDepositAccount covers handleRepay, the one handler spanning the
// deposit and lending layers: a repayment debits the customer's account and
// credits the facility's drawn balance in the same request.
func TestRepayFromDepositAccount(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	loan := openTermLoan(t, h, pid, 12)
	fid := loan["id"].(string)
	doJSON(t, bank(h, pid), "POST", "/facilities/"+fid+"/disbursement", `{
		"counterparty":"`+gl+`","firstDue":"2025-02-15","description":"payout"
	}`, http.StatusCreated)

	tx := doJSON(t, bank(h, pid), "POST", "/facilities/"+fid+"/repayments", `{
		"accountId":"`+did+`","amount":100000,"date":"2025-02-01","description":"first payment"
	}`, http.StatusCreated)
	if tx["id"] == nil || tx["id"].(string) == "" {
		t.Fatalf("repayment did not return a transaction: %v", tx)
	}

	bal := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did+"/balance", "", http.StatusOK)
	assertEqual(t, "book balance after repayment", int64(bal["book"].(float64)), int64(900000))

	got := doJSON(t, bank(h, pid), "GET", "/facilities/"+fid, "", http.StatusOK)
	assertEqual(t, "drawn after repayment", int64(got["drawn"].(float64)), int64(900000))
}

// TestRepayExceedingAvailableBalanceReturns422 pins that the funds check runs
// BEFORE the facility is ever touched: a repayment larger than the customer's
// available balance is refused with deposit.ErrInsufficientAvailable, and
// nothing moves on either side.
func TestRepayExceedingAvailableBalanceReturns422(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	loan := openTermLoan(t, h, pid, 12)
	fid := loan["id"].(string)
	doJSON(t, bank(h, pid), "POST", "/facilities/"+fid+"/disbursement", `{
		"counterparty":"`+gl+`","firstDue":"2025-02-15","description":"payout"
	}`, http.StatusCreated)

	// Alice's account holds exactly the disbursed €10,000 and no overdraft —
	// a repayment of €20,000 exceeds her available balance.
	assertStatus(t, bank(h, pid), "POST", "/facilities/"+fid+"/repayments", `{
		"accountId":"`+did+`","amount":2000000,"date":"2025-02-01","description":"too much"
	}`, http.StatusUnprocessableEntity)

	bal := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did+"/balance", "", http.StatusOK)
	assertEqual(t, "book balance unchanged", int64(bal["book"].(float64)), int64(1000000))

	got := doJSON(t, bank(h, pid), "GET", "/facilities/"+fid, "", http.StatusOK)
	assertEqual(t, "drawn unchanged", int64(got["drawn"].(float64)), int64(1000000))
}

// TestChargeInterestOnRevolvingLine covers billing a revolving line's cycle:
// interest is capitalized into drawn principal and one instalment (the
// minimum payment) is appended to the schedule.
func TestChargeInterestOnRevolvingLine(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	line := openRevolvingLine(t, h, pid, 500000)
	fid := line["id"].(string)
	doJSON(t, bank(h, pid), "POST", "/facilities/"+fid+"/draws", `{
		"counterparty":"`+gl+`","amount":200000,"description":"draw"
	}`, http.StatusCreated)

	// Accrue over a month, then bill the cycle. The response carries BOTH
	// halves — the capitalization posting and the instalment billed — because
	// a cycle can bill without posting; see chargeDTO.
	assertStatus(t, bank(h, pid), "POST", "/end-of-day", `{"date":"2025-02-15"}`, http.StatusNoContent)
	charged := doJSON(t, bank(h, pid), "POST", "/facilities/"+fid+"/interest-charge", `{"date":"2025-02-15"}`, http.StatusOK)
	posting, ok := charged["transaction"].(map[string]any)
	if !ok || posting["id"] == "" {
		t.Fatalf("charge response = %v, want a posted transaction", charged)
	}
	billed, ok := charged["installment"].(map[string]any)
	if !ok || int(billed["seq"].(float64)) != 1 {
		t.Fatalf("charge response = %v, want the first cycle billed", charged)
	}

	got := doJSON(t, bank(h, pid), "GET", "/facilities/"+fid, "", http.StatusOK)
	if drawn := int64(got["drawn"].(float64)); drawn <= 200000 {
		t.Fatalf("drawn after charging = %d, want > 200000 (interest capitalized into principal)", drawn)
	}

	var schedule []installmentDTO
	getJSON(t, bank(h, pid), "/facilities/"+fid+"/schedule", &schedule)
	if len(schedule) != 1 {
		t.Fatalf("schedule after charging = %v, want 1 row (the billed cycle)", schedule)
	}
	if schedule[0].Interest <= 0 {
		t.Fatalf("cycle interest = %d, want > 0", schedule[0].Interest)
	}
	if schedule[0].Principal <= 0 {
		t.Fatalf("cycle minimum principal = %d, want > 0 (10%% of drawn)", schedule[0].Principal)
	}

	// Charging a term loan is refused: it settles interest through its
	// scheduled instalments instead.
	loan := openTermLoan(t, h, pid, 12)
	assertStatus(t, bank(h, pid), "POST", "/facilities/"+loan["id"].(string)+"/interest-charge", `{"date":"2025-02-15"}`, http.StatusUnprocessableEntity)
}

// TestChargeInterestBillsACycleWithNothingToPost covers the outcome the old
// bare-transaction response could not express: a line drawn and charged before
// any accrual has ticked posts nothing and still bills a cycle. The response
// must carry the instalment and no transaction — a client that saw only an
// empty body would report "nothing to charge" while the schedule gained a row.
func TestChargeInterestBillsACycleWithNothingToPost(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	line := openRevolvingLine(t, h, pid, 500000)
	fid := line["id"].(string)
	doJSON(t, bank(h, pid), "POST", "/facilities/"+fid+"/draws", `{
		"counterparty":"`+gl+`","amount":200000,"description":"draw"
	}`, http.StatusCreated)

	// No end-of-day has run, so nothing has accrued.
	charged := doJSON(t, bank(h, pid), "POST", "/facilities/"+fid+"/interest-charge", `{"date":"2025-01-15"}`, http.StatusOK)
	if _, posted := charged["transaction"]; posted {
		t.Fatalf("charge response = %v, want no transaction", charged)
	}
	billed, ok := charged["installment"].(map[string]any)
	if !ok {
		t.Fatalf("charge response = %v, want a billed cycle", charged)
	}
	if int64(billed["interest"].(float64)) != 0 {
		t.Fatalf("cycle interest = %v, want 0", billed["interest"])
	}
	if int64(billed["principal"].(float64)) != 20000 {
		t.Fatalf("cycle minimum = %v, want 20000 (10%% of drawn)", billed["principal"])
	}

	var schedule []installmentDTO
	getJSON(t, bank(h, pid), "/facilities/"+fid+"/schedule", &schedule)
	if len(schedule) != 1 {
		t.Fatalf("schedule = %v, want the one billed cycle", schedule)
	}

	// An undrawn line does neither, and THAT is 204 — the empty outcome the
	// rest of this API already expresses that way.
	empty := openRevolvingLine(t, h, pid, 500000)
	assertStatus(t, bank(h, pid), "POST", "/facilities/"+empty["id"].(string)+"/interest-charge", `{"date":"2025-01-15"}`, http.StatusNoContent)
}

// TestChargeOverdraftInterestEndpoint covers POST
// /participants/{pid}/deposit-accounts/{did}/interest-charge: the monthly
// capitalization a customer actually sees, on the deposit side. Nothing
// accrued is 204 — nothing posts and, unlike a revolving line's cycle,
// nothing is billed either.
func TestChargeOverdraftInterestEndpoint(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	// Nothing has accrued yet — an account that has never been overdrawn.
	assertStatus(t, bank(h, pid), "POST", "/deposit-accounts/"+did+"/interest-charge", `{"date":"2025-01-31"}`, http.StatusNoContent)

	doJSON(t, bank(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-limit", `{
		"limit":100000
	}`, http.StatusOK)
	doJSON(t, bank(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-pricing", `{
		"pricing":{"rate":120000,"unarrangedRate":0,"dayCount":"ACT/365"}
	}`, http.StatusOK)

	glLedger := doJSON(t, bank(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	sub := doJSON(t, bank(h, pid), "POST", "/ledgers/"+glLedger+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	equity := doJSON(t, bank(h, pid), "POST", "/subledgers/"+sub+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	// Draw €500 into the overdraft with a direct GL posting, the mechanism
	// deposit.overdraftAccrual's own doc describes.
	doJSON(t, bank(h, pid), "POST", "/transactions", `{
		"entries":[
			{"accountId":"`+gl+`","amount":50000,"direction":"Debit"},
			{"accountId":"`+equity+`","amount":50000,"direction":"Credit"}
		]
	}`, http.StatusCreated)

	// The accrual window opens when the ACCOUNT is opened — the opening terms
	// row every account gets — not on the first end-of-day, so both runs below
	// accrue. Here that is the same 15 January the terms were set on, because
	// the test clock is frozen and the account was opened and priced on it; on
	// a real timeline the window would reach back further and the days before
	// pricing would accrue nothing, carrying the opening row's zero rate.
	// €500 at 12% ACT/365 is 50_000 × 120_000 / 365 = 16_438_356
	// micro-minor-units a day; two days is 32_876_712, which rounds to 33
	// cents. The value-dated recompute is what makes the first day count: the
	// increment it replaced took its first run as the baseline and charged
	// nothing for it, dropping a day of interest on every account ever priced.
	assertStatus(t, bank(h, pid), "POST", "/end-of-day", `{"date":"2025-01-16"}`, http.StatusNoContent)
	assertStatus(t, bank(h, pid), "POST", "/end-of-day", `{"date":"2025-01-17"}`, http.StatusNoContent)

	acct := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)
	assertEqual(t, "accrued before charging", int64(acct["accruedInterest"].(float64)), int64(33))

	tx := doJSON(t, bank(h, pid), "POST", "/deposit-accounts/"+did+"/interest-charge", `{"date":"2025-01-17"}`, http.StatusOK)
	if tx["id"] == nil || tx["id"].(string) == "" {
		t.Fatalf("charge did not return a transaction: %v", tx)
	}
	entries, ok := tx["entries"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("charge posted %v, want two entries", tx["entries"])
	}

	// Capitalized: the customer now owes the interest as part of the balance,
	// and the receivable is back to nothing.
	bal := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did+"/balance", "", http.StatusOK)
	assertEqual(t, "book balance after charging", int64(bal["book"].(float64)), int64(-50033))

	after := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)
	assertEqual(t, "receivable after charging", int64(after["accruedInterest"].(float64)), int64(0))

	// And charging again, with the receivable cleared, is the empty outcome.
	assertStatus(t, bank(h, pid), "POST", "/deposit-accounts/"+did+"/interest-charge", `{"date":"2025-01-17"}`, http.StatusNoContent)
}

// TestOverdraftTermsTimelineEndpoint covers GET
// /participants/{pid}/deposit-accounts/{did}/overdraft-terms: the account's
// whole effective-dated timeline, oldest first, including the opening row
// every account gets at OpenAccount.
//
// It is the endpoint the change exists to make possible — before terms were
// rows, "what did this account's product say on 15 July?" had no answer to
// serve — so it also asserts that a FUTURE-dated row is on the timeline, that
// rows are asserted on more than rate and length (effectiveFrom, dayCount,
// unarrangedRate, accountId, productId, floating), and that the account's own
// resolved-as-of-today fields still show the current terms, not the future ones.
func TestOverdraftTermsTimelineEndpoint(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	prd := prdOf(t, h, pid)
	did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","overdraftLimit":50000,"productId":"`+prd+`"}`, http.StatusCreated)["id"].(string)

	// The opening row alone: FLOATING, because an account is opened onto its
	// product's price rather than a copy of it. Its three rate fields are zero
	// because the row holds no price at all, which is what floating means and
	// is emphatically not "interest-free".
	first := doJSONArray(t, bank(h, pid), "GET", "/deposit-accounts/"+did+"/overdraft-terms", "", http.StatusOK)
	assertEqual(t, "opening timeline length", len(first), 1)
	opening := first[0].(map[string]any)
	assertEqual(t, "opening limit", int64(opening["overdraftLimit"].(float64)), int64(50_000))
	assertEqual(t, "the opening row floats", opening["floating"].(bool), true)
	assertEqual(t, "opening product", opening["productId"].(string), prd)

	// A change effective before the account existed is refused: each row is a
	// complete statement of the account's terms carried forward from the row in
	// force, and there is no row in force before the opening one.
	assertStatus(t, bank(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-limit", `{"limit":50000,"effectiveFrom":"2025-01-01T00:00:00Z"}`, http.StatusInternalServerError)

	// A row priced today and a FUTURE-dated one. fixedTime (the test clock) is
	// 2025-01-15, the same day the account above was just opened on, so the
	// first of these shares the opening row's day key and REPLACES it in place
	// — one row per (account, day) by construction, see deposit.TermsDayKey —
	// rather than appending. The timeline below is 2 long.
	doJSON(t, bank(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-pricing", `{
		"pricing":{"rate":150000,"unarrangedRate":350000,"dayCount":"ACT/365"},
		"effectiveFrom":"2025-01-15T00:00:00Z"
	}`, http.StatusOK)
	doJSON(t, bank(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-pricing", `{
		"pricing":{"rate":180000,"unarrangedRate":350000,"dayCount":"ACT/365"},
		"effectiveFrom":"2099-01-01T00:00:00Z"
	}`, http.StatusOK)

	rows := doJSONArray(t, bank(h, pid), "GET", "/deposit-accounts/"+did+"/overdraft-terms", "", http.StatusOK)
	assertEqual(t, "timeline length", len(rows), 2)

	today := rows[0].(map[string]any)
	assertEqual(t, "accountId is on the wire", today["accountId"].(string), did)
	assertEqual(t, "today's effectiveFrom", today["effectiveFrom"].(string), "2025-01-15T00:00:00Z")
	assertEqual(t, "today's rate", int64(today["rate"].(float64)), int64(150_000))
	assertEqual(t, "an overlaid row does not float", today["floating"].(bool), false)
	assertEqual(t, "the limit was carried forward", int64(today["overdraftLimit"].(float64)), int64(50_000))

	// createdAt is the OTHER half of the two-dates pair, and the test clock is
	// frozen, so it is fixedTime regardless of a row's effectiveFrom. The
	// future row is where the gap is largest, and it is what would catch the
	// two ever being swapped in the DTO mapping — the card renders them as
	// separate columns.
	last := rows[len(rows)-1].(map[string]any)
	assertEqual(t, "the future row is on the timeline", int64(last["rate"].(float64)), int64(180_000))
	assertEqual(t, "rate scale is on the wire", int64(last["rateScale"].(float64)), int64(interest.RateScale))
	assertEqual(t, "future effectiveFrom", last["effectiveFrom"].(string), "2099-01-01T00:00:00Z")
	assertEqual(t, "future dayCount", last["dayCount"].(string), "ACT/365")
	assertEqual(t, "future unarrangedRate", int64(last["unarrangedRate"].(float64)), int64(350_000))
	assertEqual(t, "future createdAt", last["createdAt"].(string), "2025-01-15T12:00:00Z")

	// …but the account itself still reports the rate in force TODAY, and says
	// where it came from: this one is negotiated, not the product's list price.
	acct := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)
	assertEqual(t, "resolved as of today", int64(acct["overdraftRate"].(float64)), int64(150_000))
	assertEqual(t, "and its source", acct["pricingSource"].(string), "negotiated")
	assertEqual(t, "and its product", acct["productId"].(string), prd)
}

// TestEndOfDayAccruesBothFacilityAndOverdraftInterest is the HTTP-layer half
// of payment.Participant.RunEndOfDay: one POST /end-of-day drives both credit
// batches, so a facility and an overdrawn deposit account both accrue from a
// single call. The deposit account's window opens at account opening — the
// opening terms row — and the facility's at disbursement, so both accrue on
// the first run; the second is here because the first covers a single day and
// this test is about both batches running, not about either figure.
func TestEndOfDayAccruesBothFacilityAndOverdraftInterest(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	doJSON(t, bank(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-limit", `{
		"limit":100000
	}`, http.StatusOK)
	doJSON(t, bank(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-pricing", `{
		"pricing":{"rate":120000,"unarrangedRate":0,"dayCount":"ACT/365"}
	}`, http.StatusOK)

	glLedger := doJSON(t, bank(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	sub := doJSON(t, bank(h, pid), "POST", "/ledgers/"+glLedger+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	equity := doJSON(t, bank(h, pid), "POST", "/subledgers/"+sub+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	// Draw Alice's account into an overdraft via a direct GL posting — the
	// same mechanism deposit.overdraftAccrual's own doc describes, since
	// CheckWithdrawal is not the only way an account ends up overdrawn.
	doJSON(t, bank(h, pid), "POST", "/transactions", `{
		"entries":[
			{"accountId":"`+gl+`","amount":50000,"direction":"Debit"},
			{"accountId":"`+equity+`","amount":50000,"direction":"Credit"}
		]
	}`, http.StatusCreated)

	loan := openTermLoan(t, h, pid, 12)
	fid := loan["id"].(string)
	doJSON(t, bank(h, pid), "POST", "/facilities/"+fid+"/disbursement", `{
		"counterparty":"`+equity+`","firstDue":"2025-02-15","description":"payout"
	}`, http.StatusCreated)

	assertStatus(t, bank(h, pid), "POST", "/end-of-day", `{"date":"2025-01-16"}`, http.StatusNoContent)
	assertStatus(t, bank(h, pid), "POST", "/end-of-day", `{"date":"2025-01-17"}`, http.StatusNoContent)

	acct := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)
	if got := int64(acct["accruedInterest"].(float64)); got <= 0 {
		t.Fatalf("deposit account accrued interest = %d, want > 0", got)
	}

	got := doJSON(t, bank(h, pid), "GET", "/facilities/"+fid, "", http.StatusOK)
	if accrued := int64(got["accruedInterest"].(float64)); accrued <= 0 {
		t.Fatalf("facility accrued interest = %d, want > 0", accrued)
	}
}

// TestTotalsReportsDerivedOverdraft covers GET /totals: one bank with one
// account in credit and one drawn into an overdraft (via a direct GL posting,
// bypassing CheckWithdrawal) reports both figures, split by sign and keyed by
// asset — no journal posts this number anywhere.
func TestTotalsReportsDerivedOverdraft(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)

	alice := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","overdraftLimit":100000,"productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	aliceGL := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+alice, "", http.StatusOK)["glAccount"].(string)

	doJSON(t, bank(h, pid), "POST", "/deposits", `{"account":"`+bob+`","amount":100000,"description":"opening"}`, http.StatusOK)

	glLedger := doJSON(t, bank(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	sub := doJSON(t, bank(h, pid), "POST", "/ledgers/"+glLedger+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	equity := doJSON(t, bank(h, pid), "POST", "/subledgers/"+sub+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	doJSON(t, bank(h, pid), "POST", "/transactions", `{
		"entries":[
			{"accountId":"`+aliceGL+`","amount":30000,"direction":"Debit"},
			{"accountId":"`+equity+`","amount":30000,"direction":"Credit"}
		]
	}`, http.StatusCreated)

	var totals []totalsDTO
	getJSON(t, bank(h, pid), "/totals", &totals)
	if len(totals) != 1 {
		t.Fatalf("totals = %v, want exactly one EUR row", totals)
	}
	assertEqual(t, "asset", totals[0].Asset, "EUR")
	assertEqual(t, "deposits", totals[0].Deposits, int64(100000))
	assertEqual(t, "overdrafts", totals[0].Overdrafts, int64(30000))
}

// TestUnknownFacilityReturns404 covers the read and mutating routes alike. The
// schedule route is deliberately excluded from the 404 set: ListInstallments
// on an unknown facility is an empty listing by contract (see lending.Tx's
// doc), not an error, the same rule an empty deposit-account holds list
// follows.
func TestUnknownFacilityReturns404(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	// A real, funded deposit account, so the repayments case below clears the
	// available-funds check and 404s on the FACILITY lookup, not on the
	// account or on insufficient funds.
	did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	doJSON(t, bank(h, pid), "POST", "/deposits", `{"account":"`+did+`","amount":100000,"description":"opening"}`, http.StatusOK)

	assertStatus(t, bank(h, pid), "GET", "/facilities/nope", "", http.StatusNotFound)
	assertStatus(t, bank(h, pid), "POST", "/facilities/nope/draws", `{"counterparty":"x","amount":100,"description":"x"}`, http.StatusNotFound)
	assertStatus(t, bank(h, pid), "POST", "/facilities/nope/repayments", `{"accountId":"`+did+`","amount":100,"date":"2025-02-01","description":"x"}`, http.StatusNotFound)
	assertStatus(t, bank(h, pid), "DELETE", "/facilities/nope", "", http.StatusNotFound)

	var schedule []installmentDTO
	getJSON(t, bank(h, pid), "/facilities/nope/schedule", &schedule)
	assertEqual(t, "schedule of an unknown facility", len(schedule), 0)
}

// TestEntryDTOCarriesItsOwnValueDate pins the per-leg value date in both
// directions, which is the one place the ledger's central claim about value
// dating was invisible from outside Go.
//
// The transaction is value-dated today and one of its two legs is pinned three
// days out. The request has to be able to say that, and the response has to
// report each leg's own date rather than the transaction's — a listing that
// rendered only the transaction-level date showed one date for a posting whose
// legs deliberately disagree.
func TestEntryDTOCarriesItsOwnValueDate(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bank(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, bank(h, pid), "POST", "/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, bank(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	other := doJSON(t, bank(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	today := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	settles := today.AddDate(0, 0, 3)
	tx := doJSON(t, bank(h, pid), "POST", "/transactions", fmt.Sprintf(`{
		"entries":[
			{"accountId":%q,"amount":100,"direction":"Debit"},
			{"accountId":%q,"amount":100,"direction":"Credit","valueDate":%q}
		],
		"valueDate":%q
	}`, acct, other, settles.Format(time.RFC3339), today.Format(time.RFC3339)), http.StatusCreated)

	// Read it back through the listing rather than trusting the create
	// response: the point is that a client polling for postings can see the
	// split, and the listing is the route that renders many at once.
	var listed []transactionDTO
	getJSON(t, bank(h, pid), "/transactions", &listed)
	if len(listed) != 1 {
		t.Fatalf("listed %d transactions, want 1", len(listed))
	}
	got := listed[0]
	if !got.ValueDate.Equal(today) {
		t.Errorf("transaction valueDate = %s, want %s", got.ValueDate, today)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(got.Entries))
	}
	for _, e := range got.Entries {
		if e.ValueDate == nil {
			t.Fatalf("entry %s has no valueDate; a stored leg always carries one", e.ID)
		}
	}
	// The leg that said nothing inherits the transaction's; the leg that pinned
	// a date keeps it. Both are concrete, and they differ.
	byAccount := map[string]time.Time{}
	for _, e := range got.Entries {
		byAccount[e.AccountID] = *e.ValueDate
	}
	if d := byAccount[acct]; !d.Equal(today) {
		t.Errorf("debit leg valueDate = %s, want the transaction's %s", d, today)
	}
	if d := byAccount[other]; !d.Equal(settles) {
		t.Errorf("credit leg valueDate = %s, want its own %s", d, settles)
	}
	if byAccount[acct].Equal(byAccount[other]) {
		t.Error("both legs render the same value date; the per-leg date is not on the wire")
	}
	// And the create response says the same thing as the listing.
	created := tx["entries"].([]any)
	for _, e := range created {
		if _, ok := e.(map[string]any)["valueDate"]; !ok {
			t.Error("create response omits an entry's valueDate")
		}
	}
}

// TestSEPADebtorLegsValueDateApart is the same property on the posting that
// actually depends on it, rather than one a test constructed. Initiating an SCT
// value-dates the payer's leg to the debit and the suspense leg to settlement,
// and until entryDTO carried a value date the API reported one date for both.
func TestSEPADebtorLegsValueDateApart(t *testing.T) {
	h := newServer(t, nil)
	a := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	b := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKBDEFFXXX","name":"Bank B"}`, http.StatusCreated)["id"].(string)
	// SCT addresses both legs by IBAN (payment.Scheme.AddressedBy), so both
	// accounts need one before submission will accept them.
	alice := doJSON(t, bank(h, a), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`","identifiers":[{"scheme":"IBAN","value":"SE89-VD-ALICE-0001"}]}`, http.StatusCreated)
	bob := doJSON(t, bank(h, b), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`","identifiers":[{"scheme":"IBAN","value":"SE89-VD-BOB-0001"}]}`, http.StatusCreated)["id"].(string)
	doJSON(t, bank(h, a), "POST", "/deposits", `{"account":"`+alice["id"].(string)+`","amount":100000,"description":"opening"}`, http.StatusOK)

	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	doJSON(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice["id"].(string)+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`","identifier":{"scheme":"IBAN","value":"SE89-VD-BOB-0001"}},
		"amount":25000,
		"endToEndId":"e2e-1",
		"creditorAgent":"BNKBDEFFXXX",
		"creditorName":"Bob"
	}`, http.StatusAccepted)
	drainServer(t, h)

	var listed []transactionDTO
	getJSON(t, bank(h, a), "/transactions", &listed)

	// Find the debtor posting: the one that debits Alice's GL account. The
	// opening deposit touches it too, so match on having a leg that is NOT
	// value-dated with the rest.
	payerGL := alice["glAccount"].(string)
	var found bool
	for _, tx := range listed {
		var payerDate, suspenseDate time.Time
		for _, e := range tx.Entries {
			if e.ValueDate == nil {
				t.Fatalf("entry %s has no valueDate", e.ID)
			}
			if e.AccountID == payerGL {
				payerDate = *e.ValueDate
			} else {
				suspenseDate = *e.ValueDate
			}
		}
		if payerDate.IsZero() || suspenseDate.IsZero() || payerDate.Equal(suspenseDate) {
			continue
		}
		found = true
		if !suspenseDate.After(payerDate) {
			t.Errorf("suspense leg value-dated %s, want after the payer's %s (the settlement delay)",
				suspenseDate, payerDate)
		}
		// The transaction-level date is the SUSPENSE leg's — the payment's own
		// value date, which is settlement — and the payer's leg is pulled back
		// to the debit date. So the transaction-level date is the WRONG answer
		// for the leg a customer cares about: reporting only it told a client
		// Alice's account takes effect at settlement when it took effect on the
		// day she was debited. That is what the per-leg field fixes.
		if !tx.ValueDate.Equal(suspenseDate) {
			t.Errorf("transaction valueDate = %s, want the suspense leg's %s", tx.ValueDate, suspenseDate)
		}
		if !tx.ValueDate.After(payerDate) {
			t.Errorf("transaction valueDate = %s is not after the payer leg's %s; the whole point is that it overstates when the debit takes effect",
				tx.ValueDate, payerDate)
		}
	}
	if !found {
		t.Error("no posting in the payer's book has legs value-dated apart; the SCT debtor leg should")
	}
}

// overpaidFacilityViaAPI drives the whole scenario that leaves the bank owing a
// borrower interest, through the HTTP API only, and returns the participant, the
// facility id, the borrower's deposit account id and what is owed.
//
// There is no shortcut: the payable is credited only by a backdated correction,
// which needs interest accrued, then settled in cash, then a posting backdated
// behind both. Doing it through the API is the point — it is what proves the
// obligation is reachable and dischargeable from outside Go.
func overpaidFacilityViaAPI(t *testing.T, h *Server) (pid, fid, did string, owed int64) {
	t.Helper()
	pid = doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)
	did = acct["id"].(string)
	aliceGL := acct["glAccount"].(string)
	doJSON(t, bank(h, pid), "POST", "/deposits", `{"account":"`+did+`","amount":100000,"description":"opening"}`, http.StatusOK)

	loan := doJSON(t, bank(h, pid), "POST", "/facilities", `{
		"kind":"TermLoan","name":"Alice Home Loan","asset":"EUR",
		"commitment":1000000,"rate":60000,"dayCount":"ACT/365",
		"method":"Annuity","termMonths":60
	}`, http.StatusCreated)
	fid = loan["id"].(string)
	doJSON(t, bank(h, pid), "POST", "/facilities/"+fid+"/disbursement", `{"counterparty":"`+aliceGL+`","firstDue":"2025-02-15","description":"advance"}`, http.StatusCreated)

	// Ten days of interest, then the borrower pays exactly that in cash — so the
	// receivable is empty and the money has left the record.
	drawdown := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 10; i++ {
		assertStatus(t, bank(h, pid), "POST", "/end-of-day", `{"date":"`+drawdown.AddDate(0, 0, i).Format("2006-01-02")+`"}`, http.StatusNoContent)
	}
	var facility facilityDTO
	getJSON(t, bank(h, pid), "/facilities/"+fid, &facility)
	if facility.AccruedInterest <= 0 {
		t.Fatalf("accrued interest after ten days = %d, want positive", facility.AccruedInterest)
	}
	settled := facility.AccruedInterest
	doJSON(t, bank(h, pid), "POST", "/facilities/"+fid+"/repayments", fmt.Sprintf(
		`{"accountId":%q,"amount":%d,"date":"2025-01-25","description":"Interest"}`, did, settled), http.StatusCreated)

	// A posting backdated to the drawdown, clearing the principal: the loan was
	// repaid on day one and no interest was ever owed. Nothing is left to absorb
	// the correction, so the whole of it becomes a refund the bank owes.
	doJSON(t, bank(h, pid), "POST", "/transactions", fmt.Sprintf(`{
		"description":"backdated settlement",
		"valueDate":%q,
		"entries":[
			{"accountId":%q,"amount":1000000,"direction":"Credit"},
			{"accountId":%q,"amount":1000000,"direction":"Debit"}
		]
	}`, drawdown.Format(time.RFC3339), facility.PrincipalGL, aliceGL), http.StatusCreated)
	assertStatus(t, bank(h, pid), "POST", "/end-of-day", `{"date":"`+drawdown.AddDate(0, 0, 11).Format("2006-01-02")+`"}`, http.StatusNoContent)

	getJSON(t, bank(h, pid), "/facilities/"+fid, &facility)
	if facility.RefundPayable <= 0 {
		t.Fatalf("refundPayable = %d after the correction, want positive; the fixture needs a debt to discharge",
			facility.RefundPayable)
	}
	if facility.RefundGL == "" {
		t.Error("refundGlAccount is empty on a facility with an outstanding refund")
	}
	// It is not netted into what the borrower owes: the money runs the other way.
	if facility.Outstanding != 0 {
		t.Errorf("outstanding = %d, want 0; a refund the bank owes is not part of it", facility.Outstanding)
	}
	return pid, fid, did, facility.RefundPayable
}

// TestInterestRefundIsListedAndDischargeable is the second gap this closes: the
// payable could be credited and no endpoint could see it or settle it.
func TestInterestRefundIsListedAndDischargeable(t *testing.T) {
	h := newServer(t, nil)
	pid, fid, did, owed := overpaidFacilityViaAPI(t, h)
	aliceGL := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	var list []refundPayableDTO
	getJSON(t, bank(h, pid), "/interest-refunds-payable", &list)
	if len(list) != 1 {
		t.Fatalf("listed %d refunds, want 1: %+v", len(list), list)
	}
	if list[0].FacilityID != fid || list[0].Amount != owed {
		t.Errorf("listed %+v, want facility %s owing %d", list[0], fid, owed)
	}
	if list[0].Asset != "EUR" || list[0].Name != "Alice Home Loan" {
		t.Errorf("listed %+v, want the facility's name and asset", list[0])
	}
	if list[0].FacilityStatus == "" {
		t.Error("facilityStatus is empty; a client must be able to tell a closed facility's refund apart")
	}

	// Paying out more than is owed is a 400, and leaves the obligation intact.
	assertStatus(t, bank(h, pid), "POST", "/facilities/"+fid+"/interest-refunds", fmt.Sprintf(
		`{"counterparty":%q,"amount":%d,"date":"2025-02-01"}`, aliceGL, owed+1), http.StatusBadRequest)
	var facility facilityDTO
	getJSON(t, bank(h, pid), "/facilities/"+fid, &facility)
	if facility.RefundPayable != owed {
		t.Fatalf("refundPayable = %d after a refused refund, want %d untouched", facility.RefundPayable, owed)
	}

	// Pay it, and the obligation is gone from both the facility and the listing.
	tx := doJSON(t, bank(h, pid), "POST", "/facilities/"+fid+"/interest-refunds", fmt.Sprintf(
		`{"counterparty":%q,"amount":%d,"date":"2025-02-01","description":"overcharged interest"}`, aliceGL, owed), http.StatusCreated)
	if len(tx["entries"].([]any)) != 2 {
		t.Errorf("refund posting has %d entries, want 2", len(tx["entries"].([]any)))
	}
	getJSON(t, bank(h, pid), "/facilities/"+fid, &facility)
	if facility.RefundPayable != 0 {
		t.Errorf("refundPayable = %d after paying it, want 0", facility.RefundPayable)
	}
	getJSON(t, bank(h, pid), "/interest-refunds-payable", &list)
	if len(list) != 0 {
		t.Errorf("listed %+v after settling, want nothing owed", list)
	}

	// And a second attempt is 422: the same status as its mirror, a repayment
	// against a facility that owes nothing.
	assertStatus(t, bank(h, pid), "POST", "/facilities/"+fid+"/interest-refunds", fmt.Sprintf(
		`{"counterparty":%q,"amount":1,"date":"2025-02-01"}`, aliceGL), http.StatusUnprocessableEntity)
}

// TestInterestRefundsPayableIsEmptyForAnOrdinaryBank pins the common case: the
// listing is an empty array, not an error and not null, and an ordinary facility
// reports no refund and no refund account.
func TestInterestRefundsPayableIsEmptyForAnOrdinaryBank(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	loan := doJSON(t, bank(h, pid), "POST", "/facilities", `{
		"kind":"TermLoan","name":"Bob Loan","asset":"EUR",
		"commitment":500000,"rate":60000,"dayCount":"ACT/365",
		"method":"Annuity","termMonths":24
	}`, http.StatusCreated)

	if got := loan["refundPayable"]; got != float64(0) {
		t.Errorf("refundPayable on a fresh facility = %v, want 0", got)
	}
	if _, ok := loan["refundGlAccount"]; ok {
		t.Error("refundGlAccount is rendered on a facility that has no such account")
	}

	res := do(t, bank(h, pid), "GET", "/interest-refunds-payable", "")
	if got := strings.TrimSpace(res.Body.String()); got != "[]" {
		t.Errorf("listing = %s, want []", got)
	}

	// And refunding one is 422, not a 500 and not a silent no-op.
	assertStatus(t, bank(h, pid), "POST", "/facilities/"+loan["id"].(string)+"/interest-refunds", `{"counterparty":"x","amount":100,"date":"2025-02-01"}`, http.StatusUnprocessableEntity)
}

// TestFundingRespectsAccountStatus pins the status matrix from the receiving
// side, over HTTP, and closes a hole that stranded money.
//
// Funding a CLOSED account used to return 200. Close requires a zero balance, so
// the credit landed in an account no withdrawal could reach (they report
// ErrAccountClosed), that closing again could not clear (Closed is terminal),
// and that contradicted the very invariant Close had just enforced.
//
// Dormant and Frozen still accept credits, deliberately. An incoming payment is
// what revives a dormant account, and a freeze here is a DEBIT block — the
// garnishment case, where money owed to the customer keeps arriving while they
// cannot take any out.
func TestFundingRespectsAccountStatus(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)

	open := func(name string) string {
		return doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"`+name+`","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	}
	fund := func(did string, amount int) *httptest.ResponseRecorder {
		return do(t, bank(h, pid), "POST", "/deposits", fmt.Sprintf(`{"account":%q,"amount":%d}`, did, amount))
	}

	// Active, dormant and frozen all take a credit.
	active := open("Active")
	assertEqual(t, "credit into an active account", fund(active, 500).Code, http.StatusOK)

	dormant := open("Dormant")
	doJSON(t, bank(h, pid), "POST", "/deposit-accounts/"+dormant+"/status", `{"action":"markDormant"}`, http.StatusOK)
	assertEqual(t, "credit into a dormant account", fund(dormant, 500).Code, http.StatusOK)

	frozen := open("Frozen")
	doJSON(t, bank(h, pid), "POST", "/deposit-accounts/"+frozen+"/status", `{"action":"freeze"}`, http.StatusOK)
	assertEqual(t, "credit into a frozen account", fund(frozen, 500).Code, http.StatusOK)

	// A closed account does not, and the refusal names the status.
	closed := open("Closed")
	assertStatus(t, bank(h, pid), "DELETE", "/deposit-accounts/"+closed, "", http.StatusNoContent)
	res := fund(closed, 500)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("credit into a closed account = %d, want 422; the money would strand: %s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), "closed") {
		t.Errorf("refusal body = %s, want it to name the closed account", res.Body)
	}

	// And nothing posted — the closed account is still at zero, which is the
	// invariant Close enforced and that a landed credit would have broken.
	var bal struct {
		Book int64 `json:"book"`
	}
	getJSON(t, bank(h, pid), "/deposit-accounts/"+closed+"/balance", &bal)
	if bal.Book != 0 {
		t.Errorf("closed account book balance = %d, want 0", bal.Book)
	}
}

// TestDormantDebitNamesDormancy is the error-text half, over HTTP: a repayment
// out of a dormant account is refused as dormant rather than as an "invalid
// account status transition", and it is a 422 like its frozen sibling.
func TestDormantDebitNamesDormancy(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	doJSON(t, bank(h, pid), "POST", "/deposits", `{"account":"`+did+`","amount":100000}`, http.StatusOK)
	fid := doJSON(t, bank(h, pid), "POST", "/facilities", `{
		"kind":"TermLoan","name":"L","asset":"EUR","commitment":1000,
		"rate":60000,"dayCount":"ACT/365","method":"Annuity","termMonths":12
	}`, http.StatusCreated)["id"].(string)
	doJSON(t, bank(h, pid), "POST", "/deposit-accounts/"+did+"/status", `{"action":"markDormant"}`, http.StatusOK)

	res := do(t, bank(h, pid), "POST", "/facilities/"+fid+"/repayments", `{"accountId":"`+did+`","amount":100,"date":"2025-02-01"}`)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("repayment from a dormant account = %d, want 422: %s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), "dormant") {
		t.Errorf("refusal body = %s, want it to name dormancy", res.Body)
	}
}

// The catalogue over the wire, end to end: create, draft, publish, open an
// account from it, and see the resolved rate on the account.
//
// It is also where the two refusals that make the catalogue trustworthy are
// pinned at the HTTP boundary — republishing and retroactive publication are
// both 422, because each is a well-formed request the STATE refuses rather than
// a malformed one.
func TestProductCatalogueRoutes(t *testing.T) {
	h := newServer(t, nil)
	pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)

	created := doJSON(t, bank(h, pid), "POST", "/products", `{"name":"Basic Current Account","kind":"CurrentAccount"}`, http.StatusCreated)
	prd := created["id"].(string)
	if prd == "" {
		t.Fatal("no product id")
	}
	assertEqual(t, "kind on the wire", created["kind"].(string), "CurrentAccount")
	assertEqual(t, "a new product is on sale", created["retired"].(bool), false)

	// The bank's own default product is in the listing beside it: onboarding
	// creates one, because a bank with no product cannot open an account.
	list := doJSONArray(t, bank(h, pid), "GET", "/products", "", http.StatusOK)
	assertEqual(t, "catalogue length", len(list), 2)

	// A draft prices nothing, so an account cannot be opened from it yet.
	drafted := doJSON(t, bank(h, pid), "POST", "/products/"+prd+"/versions", `{
		"effectiveFrom":"2025-01-15T00:00:00Z","rate":120000,"dayCount":"ACT/365"
	}`, http.StatusCreated)
	assertEqual(t, "a draft is not published", drafted["published"].(bool), false)
	assertEqual(t, "and carries no hash", drafted["hash"].(string), "")
	assertStatus(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Bruno","asset":"EUR","productId":"`+prd+`"}`, http.StatusNotFound)

	published := doJSON(t, bank(h, pid), "POST", "/products/"+prd+"/versions/2025-01-15/publish", "", http.StatusOK)
	assertEqual(t, "published", published["published"].(bool), true)
	if published["hash"].(string) == "" {
		t.Error("publishing stamped no hash")
	}

	// Republishing is refused, and the status says "state, not syntax".
	assertStatus(t, bank(h, pid), "POST", "/products/"+prd+"/versions/2025-01-15/publish", "", http.StatusUnprocessableEntity)

	acct := doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Bruno","asset":"EUR","productId":"`+prd+`","overdraftLimit":50000}`, http.StatusCreated)
	did := acct["id"].(string)
	assertEqual(t, "the account names its product", acct["productId"].(string), prd)
	assertEqual(t, "and is priced by it", int64(acct["overdraftRate"].(float64)), int64(120_000))
	assertEqual(t, "from the product", acct["pricingSource"].(string), "product")

	// A negotiated rate shows as such, which is the distinction a customer
	// service screen needs.
	negotiated := doJSON(t, bank(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-pricing", `{
		"effectiveFrom":"2025-01-15T00:00:00Z",
		"pricing":{"rate":90000,"unarrangedRate":0,"dayCount":"ACT/365"}
	}`, http.StatusOK)
	assertEqual(t, "the negotiated rate", int64(negotiated["overdraftRate"].(float64)), int64(90_000))
	assertEqual(t, "and its source", negotiated["pricingSource"].(string), "negotiated")

	// Retroactive publication is refused over the wire too. Drafting it is
	// allowed — only publication is forward-only.
	doJSON(t, bank(h, pid), "POST", "/products/"+prd+"/versions", `{
		"effectiveFrom":"2020-01-01T00:00:00Z","rate":1,"dayCount":"ACT/365"
	}`, http.StatusCreated)
	assertStatus(t, bank(h, pid), "POST", "/products/"+prd+"/versions/2020-01-01/publish", "", http.StatusUnprocessableEntity)

	// The timeline lists drafts alongside published rows: an operator view has
	// to be able to see what is queued.
	versions := doJSONArray(t, bank(h, pid), "GET", "/products/"+prd+"/versions", "", http.StatusOK)
	assertEqual(t, "timeline length", len(versions), 2)
	assertEqual(t, "oldest first", versions[0].(map[string]any)["effectiveFrom"].(string), "2020-01-01T00:00:00Z")

	// Retiring takes it off sale without unpricing the account already on it.
	retired := doJSON(t, bank(h, pid), "POST", "/products/"+prd+"/retire", "", http.StatusOK)
	assertEqual(t, "retired", retired["retired"].(bool), true)
	assertStatus(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Bella","asset":"EUR","productId":"`+prd+`"}`, http.StatusUnprocessableEntity)
	still := doJSON(t, bank(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)
	assertEqual(t, "the account on it still resolves", int64(still["overdraftRate"].(float64)), int64(90_000))

	// An unknown kind is a bad field value, not a silent CurrentAccount.
	assertStatus(t, bank(h, pid), "POST", "/products", `{"name":"Odd","kind":"SavingsAccount"}`, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// Account addressing: the directory and per-account identifier endpoints
// ---------------------------------------------------------------------------

// someAccount opens one participant with one deposit account that already
// carries an IBAN, and returns both ids. The identifier is what
// TestDepositAccountDTOCarriesIdentifiers checks for; the other tests in this
// section build on top of it rather than opening their own bare account.
func someAccount(t *testing.T, h *Server) (pid, did string) {
	t.Helper()
	pid = doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did = doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`",`+
		`"identifiers":[{"scheme":"IBAN","value":"XX00-SOME-0001"}]}`, http.StatusCreated)["id"].(string)
	return pid, did
}

// anotherAccountAtSameBank opens a second deposit account at pid, with no
// identifier of its own — the fixture for proving AddIdentifier's uniqueness
// check is per-bank rather than per-account.
func anotherAccountAtSameBank(t *testing.T, h *Server, pid string) string {
	t.Helper()
	return doJSON(t, bank(h, pid), "POST", "/deposit-accounts", `{"name":"Bruno","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
}

// TestDirectoryResolvesAnIBAN pins GET /directory's happy path: an account
// opened with an identifier is resolvable by it, and the response names the
// participant, the account, its display name and its asset — everything a
// caller needs to show who an address belongs to before paying it.
func TestDirectoryResolvesAnIBAN(t *testing.T) {
	srv := newServer(t, func(ctx context.Context, net *payment.Network) error {
		p, err := net.AddParticipant(ctx, "Aurora Bank", "BANKDEFFXXX", nil)
		if err != nil {
			return err
		}
		_, err = p.Deposit.OpenAccount(ctx, p.CustomerSubledger, "Alice", "EUR", p.ProductID, 0,
			deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"})
		return err
	})

	var got struct {
		Participant string `json:"participant"`
		Account     string `json:"account"`
		Name        string `json:"name"`
		Asset       string `json:"asset"`
	}
	getJSON(t, csm(srv), "/directory?scheme=IBAN&value=SE89-AURORA-1001", &got)
	if got.Participant == "" || got.Account == "" || got.Name == "" || got.Asset == "" {
		t.Fatalf("directory response = %#v, want it fully populated", got)
	}
}

// TestDirectoryUnknownIBANIs404 pins that an address nobody holds is a missing
// resource, not an empty success.
func TestDirectoryUnknownIBANIs404(t *testing.T) {
	srv := newServer(t, nil)
	doJSON(t, csm(srv), "GET", "/directory?scheme=IBAN&value=NOBODY-0001", "", http.StatusNotFound)
}

// TestDirectoryMissingParamsIs400 pins that scheme and value are both
// required: this is a malformed request, not a lookup that could ever
// succeed.
func TestDirectoryMissingParamsIs400(t *testing.T) {
	srv := newServer(t, nil)
	doJSON(t, csm(srv), "GET", "/directory?scheme=IBAN", "", http.StatusBadRequest)
	doJSON(t, csm(srv), "GET", "/directory?value=X", "", http.StatusBadRequest)
}

// TestAddAndRemoveIdentifierEndpoints covers the per-account identifier
// lifecycle over HTTP: adding one makes the account resolvable, a second
// account at the same bank cannot take an address already in use, and
// removing it makes the directory forget it again.
func TestAddAndRemoveIdentifierEndpoints(t *testing.T) {
	srv := newServer(t, nil)
	pid, did := someAccount(t, srv)

	base := "/deposit-accounts/"
	doJSON(t, bank(srv, pid), "POST", base+did+"/identifiers",
		`{"scheme":"IBAN","value":"XX00-TEST-0001"}`, http.StatusNoContent)

	// Now resolvable.
	doJSON(t, csm(srv), "GET", "/directory?scheme=IBAN&value=XX00-TEST-0001", "", http.StatusOK)

	// A second account at the same bank cannot take it.
	other := anotherAccountAtSameBank(t, srv, pid)
	doJSON(t, bank(srv, pid), "POST", base+other+"/identifiers",
		`{"scheme":"IBAN","value":"XX00-TEST-0001"}`, http.StatusConflict)

	doJSON(t, bank(srv, pid), "DELETE", base+did+"/identifiers/IBAN/XX00-TEST-0001", "", http.StatusNoContent)
	doJSON(t, csm(srv), "GET", "/directory?scheme=IBAN&value=XX00-TEST-0001", "", http.StatusNotFound)
}

// TestDirectoryAmbiguousIdentifierIs409 pins deposit.ErrIdentifierAmbiguous's
// status code. The mapping is a conflict IN THE DATA — the answer exists and is
// contested — and nothing else in this file held it there, so a future editor
// tidying the 409 arm into the 404 arm would break a client's retry logic
// silently.
//
// Two banks issuing one value is the only way to reach it without going around
// a register: per-bank uniqueness cannot see across banks, which is exactly why
// the network's sweep refuses rather than picking.
func TestDirectoryAmbiguousIdentifierIs409(t *testing.T) {
	srv := newServer(t, func(ctx context.Context, net *payment.Network) error {
		// A BIC each, because the mesh gives every bank an actor keyed by its
		// address and refuses two banks on one. The ambiguity this test is about
		// is in the two banks' IBANs, not in their BICs.
		for _, b := range []struct{ name, bic string }{
			{"Aurora Bank", "AURODEFFXXX"},
			{"Banca Verde", "VERDITMMXXX"},
		} {
			p, err := net.AddParticipant(ctx, b.name, iso20022.BIC(b.bic), nil)
			if err != nil {
				return err
			}
			if _, err := p.Deposit.OpenAccount(ctx, p.CustomerSubledger, "Holder", "EUR", p.ProductID, 0,
				deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SHARED-0001"}); err != nil {
				return err
			}
		}
		return nil
	})

	doJSON(t, csm(srv), "GET", "/directory?scheme=IBAN&value=SHARED-0001", "", http.StatusConflict)
}

// TestPaymentAddressingRefusalsAre422 pins the status codes of the three ways
// initiation can refuse a leg on addressing grounds, AND that a refusal takes
// no money. It drives FIVE refusals over those three sentinels —
// ErrUnaddressableAccount twice for two different shapes of missing address,
// plus once more on the payer's own bank surface, then ErrIdentifierMismatch
// and ErrAmbiguousAddress — and one request that goes through, six in all. Each
// is a well-formed request refused by state, the same category as a frozen
// account, and until now nothing in this file held any of them, so the whole
// arm could have drifted to 400 or 500 with every test still green.
//
// # The balance assertions are the half that caught a live money bug
//
// THREE of the five refusals below were answered 422 with the payer ALREADY
// DEBITED. The submitting bank committed the debtor leg in one unit of work and
// built its pacs.008 in a second, and an unaddressable payee fails the second —
// so a request the API reported as refused moved 1000 out of Alice's account,
// into a clearing suspense, against a payment nobody would ever answer. No
// message had been sent, so there was not even a dead letter; a client that
// retried the refusal drained the account. payment.SubmitAndInstruct is the fix,
// and these assertions are what would have seen it: the status codes alone were
// all green throughout.
//
// # Which cases carry a balance check, and which does not
//
// Four of the five refusals are followed by assertAliceUntouched. The
// ErrAmbiguousAddress case at the end is NOT, and that is stated rather than
// left to be counted: it runs after the happy case, so the opening balance the
// helper asserts is no longer the right number. It is covered by the aggregate
// below instead — exactly one of the six requests moved money, and the network
// holds exactly one payment row.
//
// Weaker per-case, and it is the case that needs it least. addressFor raises
// ErrAmbiguousAddress inside debtorSideTx (payment/system.go:2036), which
// SubmitPaymentTx runs BEFORE postDebtorLegTx, so this one never posted a leg
// even under the defect. The three per-case checks above cover the refusals
// that did.
//
// It also pins the DEBTOR back-fill over HTTP, which is where it matters: the
// DTO's identifier field is optional and this is the shape every other payment
// test in this file sends. It does NOT pin a creditor back-fill — see the
// comment on the happy case below for why there is nothing of that shape
// reachable through this route.
func TestPaymentAddressingRefusalsAre422(t *testing.T) {
	h := newServer(t, nil)

	a := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	b := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKBDEFFXXX","name":"Bank B"}`, http.StatusCreated)["id"].(string)
	alice := doJSON(t, bank(h, a), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`","identifiers":[{"scheme":"IBAN","value":"SE89-ADDR-ALICE-0001"}]}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, bank(h, b), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`","identifiers":[{"scheme":"IBAN","value":"SE89-ADDR-BOB-0001"}]}`, http.StatusCreated)["id"].(string)
	// A creditor with no address at all: an SCT cannot reach it.
	nobody := doJSON(t, bank(h, b), "POST", "/deposit-accounts", `{"name":"Nobody","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`, http.StatusCreated)["id"].(string)

	doJSON(t, bank(h, a), "POST", "/deposits", `{"account":"`+alice+`","amount":100000,"description":"opening"}`, http.StatusOK)
	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	// assertAliceUntouched is the money half. It reads the BOOK balance rather
	// than the available one, because the debit this test exists to catch is a
	// posted transaction and not a hold: an available balance narrowed by a hold
	// would recover on its own, and a booked debit into a clearing suspense
	// never does.
	assertAliceUntouched := func(what string) {
		t.Helper()
		bal := doJSON(t, bank(h, a), "GET", "/deposit-accounts/"+alice+"/balance", "", http.StatusOK)
		if got := int64(bal["book"].(float64)); got != 100000 {
			t.Fatalf("%s: Alice's book balance is %d, want 100000 — a refused request took her money", what, got)
		}
	}

	// ErrUnaddressableAccount.
	assertStatus(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice+`"},
		"creditor":{"participant":"`+b+`","account":"`+nobody+`"},
		"amount":1000
	}`, http.StatusUnprocessableEntity)
	assertAliceUntouched("after a payee with no address at all")

	// ErrIdentifierMismatch — the creditor's own address, on the debtor leg.
	assertStatus(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice+`","identifier":{"scheme":"IBAN","value":"SE89-ADDR-BOB-0001"}},
		"creditor":{"participant":"`+b+`","account":"`+bob+`"},
		"amount":1000
	}`, http.StatusUnprocessableEntity)
	assertAliceUntouched("after the payee's address quoted on the payer's leg")

	// A push that quotes no payee address is refused, and the mesh is what made
	// that so. The payer's bank has to put an IBAN in the pacs.008 it is about to
	// send, and it cannot invent one: an address in another bank's register is
	// exactly what it has no way to look up. Before the mesh nothing built a
	// message here, so an instruction naming only an account id went through and
	// the address was filled in later by the bank at the other end.
	assertStatus(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`"},
		"amount":1000
	}`, http.StatusUnprocessableEntity)
	assertAliceUntouched("after a push that quoted no payee address")

	// The same instruction on the PAYER's own bank, which is the door a retail
	// client uses and the one that matters most: this is the request a phone app
	// sends, and the one a retrying app would send again.
	assertStatus(t, bank(h, a), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`"},
		"amount":1000
	}`, http.StatusUnprocessableEntity)
	assertAliceUntouched("after the same refusal on the payer's own bank surface")

	// The happy case: the payee's address quoted, the payer's left to the
	// back-fill. The debtor's identifier is stamped by the payer's own bank at
	// submission, synchronously, and is on the 202 itself — that is the one
	// property this case pins.
	//
	// The creditor identifier is NOT a back-fill: it is simply what this
	// request already quoted, persisted by the payer's bank at submission and
	// left untouched when the pacs.008 reaches the payee's bank —
	// creditorSideTx (payment/system.go:1462) re-derives the same address and
	// AcceptInboundTx (payment/system.go:1306) skips the write when nothing
	// changed. The two cases above that quote no creditor address prove a push
	// of that shape is refused synchronously inside the mesh — mesh.Submit ->
	// bank.submit (mesh/bank.go:128) -> payment.SubmitAndInstruct, where the
	// message is now built in the same unit of work as the leg — so there is no
	// path through this route on which a creditor back-fill is ever attempted,
	// let alone reachable. There is no api-level test for that back-fill, and
	// this is not one either. No drain is needed before the GET below: nothing
	// on this path is still in flight, and the value read back is the one
	// already in the request.
	pay := doJSON(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`","identifier":{"scheme":"IBAN","value":"SE89-ADDR-BOB-0001"}},
		"amount":1000,
		"creditorAgent":"BNKBDEFFXXX",
		"creditorName":"Bob"
	}`, http.StatusAccepted)
	assertEqual(t, "back-filled debtor address",
		pay["debtor"].(map[string]any)["identifier"].(map[string]any)["value"].(string), "SE89-ADDR-ALICE-0001")
	carried := doJSON(t, csm(h), "GET", "/payments/"+pay["id"].(string), "", http.StatusOK)
	assertEqual(t, "creditor address persisted from the request",
		carried["creditor"].(map[string]any)["identifier"].(map[string]any)["value"].(string), "SE89-ADDR-BOB-0001")

	// ErrAmbiguousAddress: give Alice a second IBAN and quote neither of hers.
	doJSON(t, bank(h, a), "POST", "/deposit-accounts/"+alice+"/identifiers", `{"scheme":"IBAN","value":"SE89-ADDR-ALICE-0002"}`, http.StatusNoContent)
	assertStatus(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`","identifier":{"scheme":"IBAN","value":"SE89-ADDR-BOB-0001"}},
		"amount":1000
	}`, http.StatusUnprocessableEntity)

	// The aggregate, and the only thing covering the ErrAmbiguousAddress case
	// just above. Six requests have gone out; the one that went through is the
	// one that moved money, and it is the only payment row in the network. Five
	// refusals leaving five Initiated payments that nothing will ever answer is
	// the other shape of the same bug: the row outlives the request that was
	// told no.
	drainServer(t, h)
	bal := doJSON(t, bank(h, a), "GET", "/deposit-accounts/"+alice+"/balance", "", http.StatusOK)
	if got := int64(bal["book"].(float64)); got != 99000 {
		t.Fatalf("Alice's book balance is %d, want 99000 — exactly one of these six requests may take her money", got)
	}
	all := doJSONArray(t, csm(h), "GET", "/payments", "", http.StatusOK)
	if len(all) != 1 {
		t.Fatalf("the network holds %d payments, want 1: a refused instruction leaves no row", len(all))
	}
}

// TestDepositAccountDTOCarriesIdentifiers pins that depositAccountDTO renders
// the account's identifiers, not just what the register knows about it.
func TestDepositAccountDTOCarriesIdentifiers(t *testing.T) {
	srv := newServer(t, nil)
	pid, did := someAccount(t, srv)
	var got struct {
		Identifiers []struct {
			Scheme string `json:"scheme"`
			Value  string `json:"value"`
		} `json:"identifiers"`
	}
	getJSON(t, bank(srv, pid), "/deposit-accounts/"+did, &got)
	if len(got.Identifiers) == 0 {
		t.Fatal("depositAccountDTO carried no identifiers")
	}
}
