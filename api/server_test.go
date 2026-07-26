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
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/testenv"
)

var fixedTime = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	return newServer(t, nil).Routes()
}

// newServer builds a Server over an empty in-memory store. populate is the
// reseed function — the tests' stand-in for the sample dataset — and is called
// once now, as the process would at boot, and again on every reset. Pass nil
// for a server that resets to an empty system.
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
	return NewServer(net, populate, log)
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

func TestDepositFlow(t *testing.T) {
	h := newTestServer(t)

	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Alice"}`, http.StatusCreated)["id"].(string)

	// Fund Alice and confirm the returned balance.
	bal := doJSON(t, h, "POST", "/participants/"+pid+"/deposits",
		`{"account":"`+did+`","amount":100000,"description":"opening"}`, http.StatusOK)
	if got := int64(bal["available"].(float64)); got != 100000 {
		t.Fatalf("available after funding = %d, want 100000", got)
	}

	// Read the balance back.
	bal = doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did+"/balance", "", http.StatusOK)
	if got := int64(bal["book"].(float64)); got != 100000 {
		t.Fatalf("book balance = %d, want 100000", got)
	}
}

func TestSCTEndToEnd(t *testing.T) {
	h := newTestServer(t)

	a := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	b := doJSON(t, h, "POST", "/participants", `{"name":"Bank B"}`, http.StatusCreated)["id"].(string)
	alice := doJSON(t, h, "POST", "/participants/"+a+"/deposit-accounts", `{"name":"Alice"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, h, "POST", "/participants/"+b+"/deposit-accounts", `{"name":"Bob"}`, http.StatusCreated)["id"].(string)

	doJSON(t, h, "POST", "/participants/"+a+"/deposits",
		`{"account":"`+alice+`","amount":100000,"description":"opening"}`, http.StatusOK)

	cyc := doJSON(t, h, "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)["id"].(string)

	pay := doJSON(t, h, "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`"},
		"amount":25000,
		"endToEndId":"e2e-1"
	}`, http.StatusCreated)
	if pay["status"].(string) != "Accepted" {
		t.Fatalf("payment status after init = %q, want Accepted", pay["status"])
	}
	payID := pay["id"].(string)

	assertStatus(t, h, "POST", "/cycles/"+cyc+"/close", "", http.StatusOK)
	assertStatus(t, h, "POST", "/cycles/"+cyc+"/settle", "", http.StatusOK)

	aliceBal := doJSON(t, h, "GET", "/participants/"+a+"/deposit-accounts/"+alice+"/balance", "", http.StatusOK)
	if got := int64(aliceBal["book"].(float64)); got != 75000 {
		t.Fatalf("alice book = %d, want 75000", got)
	}
	bobBal := doJSON(t, h, "GET", "/participants/"+b+"/deposit-accounts/"+bob+"/balance", "", http.StatusOK)
	if got := int64(bobBal["book"].(float64)); got != 25000 {
		t.Fatalf("bob book = %d, want 25000", got)
	}

	reserveA := doJSON(t, h, "GET", "/central-bank/reserves/"+a, "", http.StatusOK)
	if got := int64(reserveA["reserve"].(float64)); got != 75000 {
		t.Fatalf("bank A reserve = %d, want 75000", got)
	}

	got := doJSON(t, h, "GET", "/payments/"+payID, "", http.StatusOK)
	if got["status"].(string) != "Settled" {
		t.Fatalf("payment status after settle = %q, want Settled", got["status"])
	}
}

// TestErrorMapping locks one error per HTTP status class.
func TestErrorMapping(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Alice"}`, http.StatusCreated)["id"].(string)

	// 404: unknown participant.
	assertStatus(t, h, "GET", "/participants/nope", "", http.StatusNotFound)

	// 422: withdrawal hold exceeding available balance (account has no funds).
	assertStatus(t, h, "POST", "/participants/"+pid+"/deposit-accounts/"+did+"/holds",
		`{"amount":5000}`, http.StatusUnprocessableEntity)

	// 400: unbalanced transaction.
	gl := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, h, "POST", "/participants/"+pid+"/subledgers/"+slid+"/accounts", `{"name":"Cash","type":"Asset"}`, http.StatusCreated)["id"].(string)
	other := doJSON(t, h, "POST", "/participants/"+pid+"/subledgers/"+slid+"/accounts", `{"name":"Equity","type":"Equity"}`, http.StatusCreated)["id"].(string)
	assertStatus(t, h, "POST", "/participants/"+pid+"/transactions", `{
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
	assertStatus(t, h, "POST", "/participants/"+pid+"/transactions", body, http.StatusCreated)
	assertStatus(t, h, "POST", "/participants/"+pid+"/transactions", body, http.StatusConflict)

	// 400: invalid enum value.
	assertStatus(t, h, "POST", "/participants/"+pid+"/subledgers/"+slid+"/accounts",
		`{"name":"Bad","type":"Nonsense"}`, http.StatusBadRequest)
}

// TestAuditEndpointIncludesPayloadAndSeq guards against the DTO silently
// dropping fields again: seq, payload and scope must reach the wire.
func TestAuditEndpointIncludesPayloadAndSeq(t *testing.T) {
	h := newTestServer(t)

	// AddParticipant posts several ledger audit events as a side effect
	// (ledger + subledgers + accounts), so no further setup is needed.
	doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)

	rec := do(t, h, "GET", "/participants/bank_1/audit", "")
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
	h := newTestServer(t)

	// emptyList reports whether the /participants body is the empty array.
	emptyList := func() bool {
		b := strings.TrimSpace(do(t, h, "GET", "/participants", "").Body.String())
		return b == "[]"
	}

	// Create a participant, confirm it's present.
	doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)
	if emptyList() {
		t.Fatal("expected one participant before reset, got empty list")
	}

	// Reset rebuilds state from the factory (empty in tests).
	doJSON(t, h, "POST", "/admin/reset", "", http.StatusOK)

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
		p, err := net.AddParticipant(ctx, "Bank A")
		if err != nil {
			return err
		}
		_, err = p.OpenCustomerAccount(ctx, "Baseline")
		return err
	}

	srv := newServer(t, baseline).Routes()

	names := func() []string {
		var accounts []depositAccountDTO
		getJSON(t, srv, "/participants/bank_1/deposit-accounts", &accounts)
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
	doJSON(t, srv, "POST", "/participants/bank_1/deposit-accounts", `{"name":"Temp","overdraftLimit":0}`, http.StatusCreated)
	if got := names(); len(got) != 2 {
		t.Fatalf("accounts after the mutation = %v, want two", got)
	}

	doJSON(t, srv, "POST", "/admin/reset", "", http.StatusOK)

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

// ---------------------------------------------------------------------------
// Audit endpoints
// ---------------------------------------------------------------------------

// auditFixture runs one payment end to end, so the log holds every scope: the
// network's own payment-scope events, both banks' ledger-scope events, their
// deposit-scope events and the central bank's. It returns the two participant
// IDs and the payment ID.
func auditFixture(t *testing.T, h http.Handler) (bankA, bankB, payID string) {
	t.Helper()
	a := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	b := doJSON(t, h, "POST", "/participants", `{"name":"Bank B"}`, http.StatusCreated)["id"].(string)
	alice := doJSON(t, h, "POST", "/participants/"+a+"/deposit-accounts", `{"name":"Alice"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, h, "POST", "/participants/"+b+"/deposit-accounts", `{"name":"Bob"}`, http.StatusCreated)["id"].(string)
	doJSON(t, h, "POST", "/participants/"+a+"/deposits",
		`{"account":"`+alice+`","amount":100000,"description":"opening"}`, http.StatusOK)

	cyc := doJSON(t, h, "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)["id"].(string)
	pay := doJSON(t, h, "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`"},
		"amount":25000
	}`, http.StatusCreated)["id"].(string)
	assertStatus(t, h, "POST", "/cycles/"+cyc+"/close", "", http.StatusOK)
	assertStatus(t, h, "POST", "/cycles/"+cyc+"/settle", "", http.StatusOK)
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
	h := newTestServer(t)
	_, _, payID := auditFixture(t, h)

	var events []auditEventDTO
	getJSON(t, h, "/payments/audit?limit=1000", &events)

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
	getJSON(t, h, "/payments/audit?entity="+payID, &forPayment)
	assertEqual(t, "events for the payment", strings.Join(auditTypes(forPayment), " "),
		strings.Join([]string{
			ledger.EventPaymentInitiated,
			ledger.EventPaymentAccepted,
			ledger.EventPaymentCleared,
			ledger.EventPaymentSettled,
		}, " "))

	// ?type= narrows to one event type.
	var cleared []auditEventDTO
	getJSON(t, h, "/payments/audit?type="+ledger.EventPaymentCleared, &cleared)
	assertEqual(t, "cleared events", len(cleared), 1)
	assertEqual(t, "cleared entity", cleared[0].EntityID, payID)
}

// TestAuditRejectedAndReturnedPayments covers the two lifecycle branches the
// happy path never reaches.
func TestAuditRejectedAndReturnedPayments(t *testing.T) {
	h := newTestServer(t)
	a, b, payID := auditFixture(t, h)

	// A settled payment can be returned.
	doJSON(t, h, "POST", "/payments/"+payID+"/return", `{"reason":"AC04"}`, http.StatusOK)

	// A fresh payment in a fresh cycle can be rejected before it clears.
	var aAccounts, bAccounts []depositAccountDTO
	getJSON(t, h, "/participants/"+a+"/deposit-accounts", &aAccounts)
	getJSON(t, h, "/participants/"+b+"/deposit-accounts", &bAccounts)
	doJSON(t, h, "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	second := doJSON(t, h, "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+aAccounts[0].ID+`"},
		"creditor":{"participant":"`+b+`","account":"`+bAccounts[0].ID+`"},
		"amount":1000
	}`, http.StatusCreated)["id"].(string)
	doJSON(t, h, "POST", "/payments/"+second+"/reject", `{"reason":"AM05"}`, http.StatusOK)

	var returned []auditEventDTO
	getJSON(t, h, "/payments/audit?type="+ledger.EventPaymentReturned, &returned)
	assertEqual(t, "returned events", len(returned), 1)
	assertEqual(t, "returned entity", returned[0].EntityID, payID)

	var rejected []auditEventDTO
	getJSON(t, h, "/payments/audit?type="+ledger.EventPaymentRejected, &rejected)
	assertEqual(t, "rejected events", len(rejected), 1)
	assertEqual(t, "rejected entity", rejected[0].EntityID, second)
}

// TestAuditMandateEvents covers the two mandate events, which no payment flow
// emits on its own.
func TestAuditMandateEvents(t *testing.T) {
	h := newTestServer(t)
	a, b, _ := auditFixture(t, h)

	var aAccounts, bAccounts []depositAccountDTO
	getJSON(t, h, "/participants/"+a+"/deposit-accounts", &aAccounts)
	getJSON(t, h, "/participants/"+b+"/deposit-accounts", &bAccounts)

	mid := doJSON(t, h, "POST", "/mandates", `{
		"debtor":{"participant":"`+a+`","account":"`+aAccounts[0].ID+`"},
		"creditor":{"participant":"`+b+`","account":"`+bAccounts[0].ID+`"},
		"maxAmount":50000
	}`, http.StatusCreated)["id"].(string)
	doJSON(t, h, "POST", "/mandates/"+mid+"/revoke", "", http.StatusOK)

	var events []auditEventDTO
	getJSON(t, h, "/payments/audit?entity="+mid, &events)
	assertEqual(t, "mandate events", strings.Join(auditTypes(events), " "),
		strings.Join([]string{ledger.EventMandateCreated, ledger.EventMandateRevoked}, " "))
}

// TestAuditRoutesAreScoped pins that the four endpoints are four filters over
// one log: each returns its own scope and its own book, and nothing else.
func TestAuditRoutesAreScoped(t *testing.T) {
	h := newTestServer(t)
	a, b, _ := auditFixture(t, h)

	cases := []struct {
		path      string
		wantScope string
	}{
		{"/participants/" + a + "/audit", string(ledger.ScopeLedger)},
		{"/participants/" + a + "/deposit-audit", string(ledger.ScopeDeposit)},
		{"/central-bank/audit", string(ledger.ScopeLedger)},
		{"/payments/audit", string(ledger.ScopePayment)},
	}
	for _, c := range cases {
		var events []auditEventDTO
		getJSON(t, h, c.path+"?limit=1000", &events)
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
	for _, path := range []string{
		"/participants/" + a + "/audit",
		"/participants/" + a + "/deposit-audit",
		"/participants/" + b + "/audit",
		"/central-bank/audit",
		"/payments/audit",
	} {
		var events []auditEventDTO
		getJSON(t, h, path+"?limit=1000", &events)
		if len(events) == 0 {
			t.Fatalf("%s returned no events", path)
		}
		logs[path] = events
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
	for path, events := range logs {
		found := false
		for _, e := range events {
			if strings.Contains(string(e.Payload), settlementMarker) {
				found = true
			}
		}
		if want := path == "/central-bank/audit"; found != want {
			t.Fatalf("%s contains the settlement transaction: got %v, want %v", path, found, want)
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
	h := newTestServer(t)
	auditFixture(t, h)

	var all []auditEventDTO
	getJSON(t, h, "/payments/audit?limit=1000", &all)
	if len(all) < 5 {
		t.Fatalf("fixture produced %d payment events, want at least 5", len(all))
	}

	// Walk the whole log backwards two at a time and reassemble it.
	var walked []auditEventDTO
	cursor := ""
	for range len(all) {
		var page []auditEventDTO
		getJSON(t, h, "/payments/audit?limit=2"+cursor, &page)
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
	getJSON(t, h, fmt.Sprintf("/payments/audit?limit=2&before=%d", all[0].Seq), &empty)
	assertEqual(t, "page below the oldest event", len(empty), 0)
}

// TestAuditDefaultLimitApplies pins that a log longer than one page is
// truncated to the newest 100 events rather than returned whole.
func TestAuditDefaultLimitApplies(t *testing.T) {
	h := newTestServer(t)

	// Each open/close pair is two payment-scope events; 51 pairs clears 100.
	for range 51 {
		cyc := doJSON(t, h, "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)["id"].(string)
		assertStatus(t, h, "POST", "/cycles/"+cyc+"/close", "", http.StatusOK)
	}

	var full []auditEventDTO
	getJSON(t, h, "/payments/audit?limit=1000", &full)
	assertEqual(t, "events in the log", len(full), 102)

	var page []auditEventDTO
	getJSON(t, h, "/payments/audit", &page)
	assertEqual(t, "default page size", len(page), 100)
	// The default page is the NEWEST 100, still oldest-first.
	assertEqual(t, "default page ends at the newest event", page[99].Seq, full[101].Seq)
	assertEqual(t, "default page starts 100 back", page[0].Seq, full[2].Seq)
}

// TestAuditLimitIsCapped pins that an oversized ?limit= is bounded rather than
// honoured. The cap itself is asserted exactly in
// TestAuditFilterParsesAndCapsLimits; this is the end-to-end half.
func TestAuditLimitIsCapped(t *testing.T) {
	h := newTestServer(t)
	auditFixture(t, h)

	var events []auditEventDTO
	getJSON(t, h, "/payments/audit?limit=99999", &events)
	if len(events) == 0 {
		t.Fatal("no events returned")
	}
	if len(events) > 1000 {
		t.Fatalf("returned %d events, want <= 1000", len(events))
	}
}
