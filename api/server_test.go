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
	did := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Alice","asset":"EUR"}`, http.StatusCreated)["id"].(string)

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
	alice := doJSON(t, h, "POST", "/participants/"+a+"/deposit-accounts", `{"name":"Alice","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, h, "POST", "/participants/"+b+"/deposit-accounts", `{"name":"Bob","asset":"EUR"}`, http.StatusCreated)["id"].(string)

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

	// Reserves are reported one row per asset, so a euro bank has exactly one.
	var reserveA []map[string]any
	getJSON(t, h, "/central-bank/reserves/"+a, &reserveA)
	if len(reserveA) != 1 || reserveA[0]["asset"] != "EUR" {
		t.Fatalf("bank A reserves = %v, want one EUR row", reserveA)
	}
	if got := int64(reserveA[0]["reserve"].(float64)); got != 75000 {
		t.Fatalf("bank A reserve = %d, want 75000", got)
	}

	got := doJSON(t, h, "GET", "/payments/"+payID, "", http.StatusOK)
	if got["status"].(string) != "Settled" {
		t.Fatalf("payment status after settle = %q, want Settled", got["status"])
	}
}

// An account without an asset is refused rather than opened in euro. The whole
// point of the asset dimension is that nothing picks a currency on the
// caller's behalf, and a request body that forgot to say is exactly the case
// where a default would go unnoticed.
func TestCreateAccountRequiresAsset(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, h, "POST", "/participants/"+pid+"/subledgers/"+slid+"/accounts",
		`{"name":"No Asset","type":"Liability"}`, http.StatusBadRequest)

	// An asset the system does not know is a 400 — a bad field value, like an
	// unparseable account type — not a silent substitution, and not a 404,
	// which on this route would read as "participant not found".
	assertStatus(t, h, "POST", "/participants/"+pid+"/subledgers/"+slid+"/accounts",
		`{"name":"Dogecoin","type":"Liability","asset":"DOGE"}`, http.StatusBadRequest)
}

func TestOpenDepositAccountRequiresAsset(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, h, "POST", "/participants/"+pid+"/deposit-accounts",
		`{"name":"No Asset"}`, http.StatusBadRequest)
}

// GET /assets lists the assets the system knows, with the scale a client needs
// to render an amount. Read-only and network-wide: assets are defined in code,
// like schemes, so there is nothing to create and nothing per-participant.
func TestListAssets(t *testing.T) {
	h := newTestServer(t)

	var assets []assetDTO
	getJSON(t, h, "/assets", &assets)

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
	h := newTestServer(t)

	assertStatus(t, h, "POST", "/participants",
		`{"name":"Bank A","assets":["DOGE"]}`, http.StatusBadRequest)
}

// TestAccountResponseIncludesAsset pins that the account, deposit-account and
// entry DTOs all carry the asset they are denominated in.
func TestAccountResponseIncludesAsset(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Alice","asset":"EUR"}`, http.StatusCreated)

	res := do(t, h, "GET", "/participants/"+pid+"/deposit-accounts", "")
	if !strings.Contains(res.Body.String(), `"asset":"EUR"`) {
		t.Errorf("deposit-account listing has no asset field: %s", res.Body)
	}

	gl := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, h, "POST", "/participants/"+pid+"/subledgers/"+slid+"/accounts",
		`{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)
	assertEqual(t, "account asset", acct["asset"].(string), "EUR")

	other := doJSON(t, h, "POST", "/participants/"+pid+"/subledgers/"+slid+"/accounts",
		`{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	tx := doJSON(t, h, "POST", "/participants/"+pid+"/transactions", `{
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
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, h, "POST", "/participants/"+pid+"/subledgers/"+slid+"/accounts",
		`{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	other := doJSON(t, h, "POST", "/participants/"+pid+"/subledgers/"+slid+"/accounts",
		`{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	today := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	post := func(amount int, valueDate time.Time) {
		doJSON(t, h, "POST", "/participants/"+pid+"/transactions", fmt.Sprintf(`{
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
	getJSON(t, h, "/participants/"+pid+"/accounts/"+acct+"/balance?asOf="+today.Format(time.RFC3339), &got)

	if got.Balance != 15_000 {
		t.Errorf("balance = %d, want 15000", got.Balance)
	}
	if got.ValueDateBalance != 10_000 {
		t.Errorf("valueDateBalance = %d, want 10000 (the forward-dated movement has not taken effect)", got.ValueDateBalance)
	}
}

// TestBalanceEndpointRejectsAnUnparseableAsOf pins the 400 on a malformed
// ?asOf=: it must not panic and must not silently fall back to now.
func TestBalanceEndpointRejectsAnUnparseableAsOf(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, h, "POST", "/participants/"+pid+"/subledgers/"+slid+"/accounts",
		`{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, h, "GET", "/participants/"+pid+"/accounts/"+acct+"/balance?asOf=not-a-date", "", http.StatusBadRequest)
}

// TestCreateParticipantWithAssets pins the optional assets array on
// participant creation: given, it is forwarded to AddParticipant instead of
// the default EUR-only set.
func TestCreateParticipantWithAssets(t *testing.T) {
	h := newTestServer(t)
	p := doJSON(t, h, "POST", "/participants", `{"name":"Bank A","assets":["USD"]}`, http.StatusCreated)
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
	h := newTestServer(t)

	for _, tc := range []struct{ name, body string }{
		{"absent", `{"name":"Bank A"}`},
		{"explicitly empty", `{"name":"Bank B","assets":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := doJSON(t, h, "POST", "/participants", tc.body, http.StatusCreated)
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
	h := newTestServer(t)
	a := doJSON(t, h, "POST", "/participants", `{"name":"Bank A","assets":["USD"]}`, http.StatusCreated)["id"].(string)
	b := doJSON(t, h, "POST", "/participants", `{"name":"Bank B"}`, http.StatusCreated)["id"].(string)
	alice := doJSON(t, h, "POST", "/participants/"+a+"/deposit-accounts", `{"name":"Alice","asset":"USD"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, h, "POST", "/participants/"+b+"/deposit-accounts", `{"name":"Bob","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, h, "POST", "/payments", `{
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
	h := newTestServer(t)

	a := doJSON(t, h, "POST", "/participants", `{"name":"Bank A","assets":["USD","EUR"]}`, http.StatusCreated)["id"].(string)
	b := doJSON(t, h, "POST", "/participants", `{"name":"Bank B","assets":["EUR","USD"]}`, http.StatusCreated)["id"].(string)
	aliceUSD := doJSON(t, h, "POST", "/participants/"+a+"/deposit-accounts", `{"name":"AliceUSD","asset":"USD"}`, http.StatusCreated)["id"].(string)
	aliceEUR := doJSON(t, h, "POST", "/participants/"+a+"/deposit-accounts", `{"name":"AliceEUR","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	bobUSD := doJSON(t, h, "POST", "/participants/"+b+"/deposit-accounts", `{"name":"BobUSD","asset":"USD"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, h, "POST", "/participants/"+b+"/deposit-accounts", `{"name":"Bob","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	// Mandate: derived from the (non-EUR) debtor account. Both ends are USD —
	// CreateMandate refuses a mandate whose two accounts disagree.
	mandate := doJSON(t, h, "POST", "/mandates", `{
		"debtor":{"participant":"`+a+`","account":"`+aliceUSD+`"},
		"creditor":{"participant":"`+b+`","account":"`+bobUSD+`"},
		"maxAmount":50000
	}`, http.StatusCreated)
	assertEqual(t, "created mandate asset", mandate["asset"].(string), "USD")
	mid := mandate["id"].(string)

	var mandates []mandateDTO
	getJSON(t, h, "/mandates", &mandates)
	if len(mandates) != 1 || mandates[0].Asset != "USD" {
		t.Fatalf("GET /mandates asset = %+v, want one USD mandate", mandates)
	}

	got := doJSON(t, h, "GET", "/mandates/"+mid, "", http.StatusOK)
	assertEqual(t, "GET mandate asset", got["asset"].(string), "USD")

	revoked := doJSON(t, h, "POST", "/mandates/"+mid+"/revoke", "", http.StatusOK)
	assertEqual(t, "revoked mandate asset", revoked["asset"].(string), "USD")

	// Scheme: every scheme implemented today settles in EUR (see
	// payment/scheme.go's SCT/SDD) — the field must still be populated, not
	// merely absent-and-unnoticed.
	var schemes []schemeDTO
	getJSON(t, h, "/schemes", &schemes)
	if len(schemes) == 0 {
		t.Fatal("no schemes returned")
	}
	for _, sc := range schemes {
		assertEqual(t, "scheme "+sc.ID+" asset", sc.Asset, "EUR")
	}

	// Cycle and settlement: derived from the scheme.
	doJSON(t, h, "POST", "/participants/"+a+"/deposits",
		`{"account":"`+aliceEUR+`","amount":100000,"description":"opening"}`, http.StatusOK)

	cyc := doJSON(t, h, "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	assertEqual(t, "created cycle asset", cyc["asset"].(string), "EUR")
	cid := cyc["id"].(string)

	var cycles []clearingCycleDTO
	getJSON(t, h, "/cycles", &cycles)
	if len(cycles) != 1 || cycles[0].Asset != "EUR" {
		t.Fatalf("GET /cycles asset = %+v, want one EUR cycle", cycles)
	}

	doJSON(t, h, "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+aliceEUR+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`"},
		"amount":1000
	}`, http.StatusCreated)

	assertStatus(t, h, "POST", "/cycles/"+cid+"/close", "", http.StatusOK)
	settlement := doJSON(t, h, "POST", "/cycles/"+cid+"/settle", "", http.StatusOK)
	assertEqual(t, "settled settlement asset", settlement["asset"].(string), "EUR")
	sid := settlement["id"].(string)

	var settlements []settlementDTO
	getJSON(t, h, "/settlements", &settlements)
	if len(settlements) != 1 || settlements[0].Asset != "EUR" {
		t.Fatalf("GET /settlements asset = %+v, want one EUR settlement", settlements)
	}

	gotSettlement := doJSON(t, h, "GET", "/settlements/"+sid, "", http.StatusOK)
	assertEqual(t, "GET settlement asset", gotSettlement["asset"].(string), "EUR")
}

// TestErrorMapping locks one error per HTTP status class.
func TestErrorMapping(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Alice","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	// 404: unknown participant.
	assertStatus(t, h, "GET", "/participants/nope", "", http.StatusNotFound)

	// 422: withdrawal hold exceeding available balance (account has no funds).
	assertStatus(t, h, "POST", "/participants/"+pid+"/deposit-accounts/"+did+"/holds",
		`{"amount":5000}`, http.StatusUnprocessableEntity)

	// 400: unbalanced transaction.
	gl := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, h, "POST", "/participants/"+pid+"/subledgers/"+slid+"/accounts", `{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	other := doJSON(t, h, "POST", "/participants/"+pid+"/subledgers/"+slid+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)
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

	// 400: a lending sentinel in the malformed-input category. A term past
	// lending.MaxTermMonths is a bad field value, not a business-state
	// conflict, so it maps beside a non-positive amount.
	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities", `{
		"kind":"TermLoan","name":"Absurd","asset":"EUR","commitment":1000000,
		"rate":60000,"dayCount":"ACT/365","method":"Annuity","termMonths":100000000
	}`, http.StatusBadRequest)

	// 422: a lending sentinel in the wrong-state category — charging a term
	// loan, which settles interest through its scheduled instalments.
	loan := openTermLoan(t, h, pid, 12)
	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities/"+loan["id"].(string)+"/interest-charge",
		`{"date":"2025-02-15"}`, http.StatusUnprocessableEntity)

	// 409: a billing cycle already on the schedule. The request is valid and
	// the state already reflects it, which is the already-applied category a
	// duplicate idempotency key is in — not a 422.
	line := openRevolvingLine(t, h, pid, 500000)
	lid := line["id"].(string)
	doJSON(t, h, "POST", "/participants/"+pid+"/facilities/"+lid+"/draws", `{
		"counterparty":"`+other+`","amount":200000,"description":"draw"
	}`, http.StatusCreated)
	doJSON(t, h, "POST", "/participants/"+pid+"/facilities/"+lid+"/interest-charge",
		`{"date":"2025-02-15"}`, http.StatusOK)
	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities/"+lid+"/interest-charge",
		`{"date":"2025-02-15"}`, http.StatusConflict)
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
		p, err := net.AddParticipant(ctx, "Bank A", nil)
		if err != nil {
			return err
		}
		_, err = p.OpenCustomerAccount(ctx, "Baseline", "EUR")
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
	doJSON(t, srv, "POST", "/participants/bank_1/deposit-accounts", `{"name":"Temp","asset":"EUR","overdraftLimit":0}`, http.StatusCreated)
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
		_, err = net.AddParticipant(ctx, "Bank A", nil)
		return err
	}

	srv := newServer(t, baseline)
	h := srv.Routes()

	populateRan, populateCtx = false, nil

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "/admin/reset", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assertEqual(t, "status", rec.Code, http.StatusOK)
	assertEqual(t, "the reseed ran", populateRan, true)
	if populateCtx != nil {
		t.Fatalf("the reseed inherited the cancelled request context: %v", populateCtx)
	}

	// And the store is whole rather than truncated-but-not-reseeded.
	var parts []participantDTO
	getJSON(t, h, "/participants", &parts)
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
		p, err := net.AddParticipant(ctx, "Bank A", nil)
		if err != nil {
			return err
		}
		_, err = p.OpenCustomerAccount(ctx, "Baseline", "EUR")
		return err
	}

	h := newServer(t, baseline).Routes()

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
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/admin/reset", nil))
			codes[i] = rec.Code
		}()
	}
	close(start)
	wg.Wait()

	for i, code := range codes {
		assertEqual(t, fmt.Sprintf("reset %d status", i), code, http.StatusOK)
	}

	var parts []participantDTO
	getJSON(t, h, "/participants", &parts)
	assertEqual(t, "participants after concurrent resets", len(parts), 1)

	var accounts []depositAccountDTO
	getJSON(t, h, "/participants/"+parts[0].ID+"/deposit-accounts", &accounts)
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
	h := newTestServer(t)

	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Alice","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	doJSON(t, h, "POST", "/participants/"+pid+"/deposits",
		`{"account":"`+did+`","amount":100000,"description":"opening"}`, http.StatusOK)
	gl := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

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
		{"a participant name", "POST", "/participants", `{"name":"Ban` + nul + `k"}`},
		{"a participant name with an escape sequence", "POST", "/participants", `{"name":"Bank` + esc + `[31m"}`},
		{"a participant name with a newline", "POST", "/participants", `{"name":"Bank\nof Nowhere"}`},
		{"a deposit account name", "POST", "/participants/" + pid + "/deposit-accounts",
			`{"name":"Al` + nul + `ice","overdraftLimit":0}`},
		{"a ledger name", "POST", "/participants/" + pid + "/ledgers", `{"name":"G` + nul + `L"}`},
		{"a funding description", "POST", "/participants/" + pid + "/deposits",
			`{"account":"` + did + `","amount":1000,"description":"op` + nul + `ening"}`},
		{"a funding account id", "POST", "/participants/" + pid + "/deposits",
			`{"account":"` + did + nul + `","amount":1000,"description":"opening"}`},
		{"a hold description", "POST", "/participants/" + pid + "/deposit-accounts/" + did + "/holds",
			`{"amount":1000,"description":"pre-a` + nul + `uth"}`},
		{"a transaction description", "POST", "/participants/" + pid + "/transactions",
			`{"description":"re` + nul + `nt",` + entries(gl) + `}`},
		{"a transaction idempotency key", "POST", "/participants/" + pid + "/transactions",
			`{"idempotencyKey":"key` + nul + `-1",` + entries(gl) + `}`},
		{"a transaction metadata value", "POST", "/participants/" + pid + "/transactions",
			`{"metadata":{"ref":"INV` + nul + `"},` + entries(gl) + `}`},
		{"an entry account id", "POST", "/participants/" + pid + "/transactions",
			`{` + entries(gl+nul) + `}`},
	} {
		rec := do(t, h, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got status %d, want 400 (body: %s)", tc.what, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}

	// Nothing was created by any of the refused requests.
	var parts []participantDTO
	getJSON(t, h, "/participants", &parts)
	assertEqual(t, "participants after the refusals", len(parts), 1)

	var accounts []depositAccountDTO
	getJSON(t, h, "/participants/"+pid+"/deposit-accounts", &accounts)
	assertEqual(t, "deposit accounts after the refusals", len(accounts), 1)
}

// TestControlCharactersInAPathAreRefused pins the read half of the same rule.
// A URL-encoded NUL is decoded into the path parameter and handed straight to
// the store as a lookup key: store/mem answers 404, store/pg answers 500 with a
// raw SQLSTATE. Identifiers here are system-generated and never contain a
// control character, so such a request cannot name anything and is refused at
// the edge.
func TestControlCharactersInAPathAreRefused(t *testing.T) {
	h := newTestServer(t)

	// A well-formed but unknown id is still an honest 404.
	assertStatus(t, h, "GET", "/participants/bank_404", "", http.StatusNotFound)

	assertStatus(t, h, "GET", "/participants/bank%001", "", http.StatusBadRequest)
	assertStatus(t, h, "GET", "/participants/bank_1/deposit-accounts/dep%001", "", http.StatusBadRequest)
	assertStatus(t, h, "GET", "/payments/audit?entity=pay%001", "", http.StatusBadRequest)
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
	alice := doJSON(t, h, "POST", "/participants/"+a+"/deposit-accounts", `{"name":"Alice","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, h, "POST", "/participants/"+b+"/deposit-accounts", `{"name":"Bob","asset":"EUR"}`, http.StatusCreated)["id"].(string)
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

// ---------------------------------------------------------------------------
// Lending and overdraft credit (Task 16)
// ---------------------------------------------------------------------------

// openTermLoan opens a EUR term loan at 6% ACT/365, annuity method, over
// termMonths instalments, and returns the decoded facility.
func openTermLoan(t *testing.T, h http.Handler, pid string, termMonths int) map[string]any {
	t.Helper()
	return doJSON(t, h, "POST", "/participants/"+pid+"/facilities", fmt.Sprintf(`{
		"kind":"TermLoan","name":"Home Loan","asset":"EUR","commitment":1000000,
		"rate":60000,"dayCount":"ACT/365","method":"Annuity","termMonths":%d
	}`, termMonths), http.StatusCreated)
}

// openRevolvingLine opens a EUR revolving line at 18% ACT/365 with a 10%
// minimum-payment fraction, and returns the decoded facility.
func openRevolvingLine(t *testing.T, h http.Handler, pid string, commitment int64) map[string]any {
	t.Helper()
	return doJSON(t, h, "POST", "/participants/"+pid+"/facilities", fmt.Sprintf(`{
		"kind":"RevolvingLine","name":"Credit Line","asset":"EUR","commitment":%d,
		"rate":180000,"dayCount":"ACT/365","minPayment":100000
	}`, commitment), http.StatusCreated)
}

// TestOpenTermLoanAndRevolvingLine pins that one route opens either product,
// dispatched by the request's Kind field, and that a freshly-opened facility
// reports zero drawn and zero accrued without a further round trip.
func TestOpenTermLoanAndRevolvingLine(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)

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
	getJSON(t, h, "/participants/"+pid+"/facilities", &facilities)
	if len(facilities) != 2 {
		t.Fatalf("facilities = %v, want 2", facilities)
	}
}

// TestOpenFacilityRequiresAsset mirrors TestOpenDepositAccountRequiresAsset: a
// facility's asset decides which asset its two GL accounts are denominated in,
// and nothing here may pick one on the caller's behalf.
func TestOpenFacilityRequiresAsset(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities", `{
		"kind":"TermLoan","name":"No Asset","commitment":100000,"rate":50000,
		"dayCount":"ACT/365","method":"Annuity","termMonths":12
	}`, http.StatusBadRequest)
}

// TestOpenFacilityRejectsUnknownEnums pins that Kind, DayCount and Method are
// parsed from their string forms exactly as the ledger handlers parse an
// account type: an unknown value is a 400 before the domain is ever called.
func TestOpenFacilityRejectsUnknownEnums(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities", `{
		"kind":"Nonsense","name":"X","asset":"EUR","commitment":100000,"rate":50000,"dayCount":"ACT/365"
	}`, http.StatusBadRequest)

	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities", `{
		"kind":"TermLoan","name":"X","asset":"EUR","commitment":100000,"rate":50000,
		"dayCount":"Nonsense","method":"Annuity","termMonths":12
	}`, http.StatusBadRequest)

	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities", `{
		"kind":"TermLoan","name":"X","asset":"EUR","commitment":100000,"rate":50000,
		"dayCount":"ACT/365","method":"Nonsense","termMonths":12
	}`, http.StatusBadRequest)
}

// TestDisburseTermLoanGeneratesSixtyRowSchedule covers disbursement end to
// end: the counterparty's balance moves, the facility becomes Active and
// reports its drawn principal, and a 60-month term generates 60 instalments.
// A second disbursement is refused.
func TestDisburseTermLoanGeneratesSixtyRowSchedule(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Alice","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	loan := openTermLoan(t, h, pid, 60)
	fid := loan["id"].(string)

	tx := doJSON(t, h, "POST", "/participants/"+pid+"/facilities/"+fid+"/disbursement", `{
		"counterparty":"`+gl+`","firstDue":"2025-02-15","description":"payout"
	}`, http.StatusCreated)
	if tx["id"] == nil || tx["id"].(string) == "" {
		t.Fatalf("disbursement did not return a transaction: %v", tx)
	}

	var schedule []installmentDTO
	getJSON(t, h, "/participants/"+pid+"/facilities/"+fid+"/schedule", &schedule)
	assertEqual(t, "schedule length", len(schedule), 60)

	bal := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did+"/balance", "", http.StatusOK)
	assertEqual(t, "book balance after disbursement", int64(bal["book"].(float64)), int64(1000000))

	got := doJSON(t, h, "GET", "/participants/"+pid+"/facilities/"+fid, "", http.StatusOK)
	assertEqual(t, "drawn after disbursement", int64(got["drawn"].(float64)), int64(1000000))
	assertEqual(t, "status after disbursement", got["status"].(string), "Active")

	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities/"+fid+"/disbursement", `{
		"counterparty":"`+gl+`","firstDue":"2025-02-15","description":"again"
	}`, http.StatusUnprocessableEntity)
}

// TestDrawPastCommitmentReturns422 covers a revolving line's draw path: a
// draw within the commitment succeeds, a term loan refuses to be drawn at all
// (ErrWrongFacilityKind), and a draw that would exceed the commitment is
// refused without moving the drawn balance.
func TestDrawPastCommitmentReturns422(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Alice","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	line := openRevolvingLine(t, h, pid, 100000)
	fid := line["id"].(string)

	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities/"+fid+"/draws", `{
		"counterparty":"`+gl+`","amount":60000,"description":"draw 1"
	}`, http.StatusCreated)

	loan := openTermLoan(t, h, pid, 12)
	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities/"+loan["id"].(string)+"/draws", `{
		"counterparty":"`+gl+`","amount":1000,"description":"nope"
	}`, http.StatusUnprocessableEntity)

	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities/"+fid+"/draws", `{
		"counterparty":"`+gl+`","amount":60000,"description":"draw 2"
	}`, http.StatusUnprocessableEntity)

	got := doJSON(t, h, "GET", "/participants/"+pid+"/facilities/"+fid, "", http.StatusOK)
	assertEqual(t, "drawn after the refused draw", int64(got["drawn"].(float64)), int64(60000))
}

// TestRepayFromDepositAccount covers handleRepay, the one handler spanning the
// deposit and lending layers: a repayment debits the customer's account and
// credits the facility's drawn balance in the same request.
func TestRepayFromDepositAccount(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Alice","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	loan := openTermLoan(t, h, pid, 12)
	fid := loan["id"].(string)
	doJSON(t, h, "POST", "/participants/"+pid+"/facilities/"+fid+"/disbursement", `{
		"counterparty":"`+gl+`","firstDue":"2025-02-15","description":"payout"
	}`, http.StatusCreated)

	tx := doJSON(t, h, "POST", "/participants/"+pid+"/facilities/"+fid+"/repayments", `{
		"accountId":"`+did+`","amount":100000,"date":"2025-02-01","description":"first payment"
	}`, http.StatusCreated)
	if tx["id"] == nil || tx["id"].(string) == "" {
		t.Fatalf("repayment did not return a transaction: %v", tx)
	}

	bal := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did+"/balance", "", http.StatusOK)
	assertEqual(t, "book balance after repayment", int64(bal["book"].(float64)), int64(900000))

	got := doJSON(t, h, "GET", "/participants/"+pid+"/facilities/"+fid, "", http.StatusOK)
	assertEqual(t, "drawn after repayment", int64(got["drawn"].(float64)), int64(900000))
}

// TestRepayExceedingAvailableBalanceReturns422 pins that the funds check runs
// BEFORE the facility is ever touched: a repayment larger than the customer's
// available balance is refused with deposit.ErrInsufficientAvailable, and
// nothing moves on either side.
func TestRepayExceedingAvailableBalanceReturns422(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Alice","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	loan := openTermLoan(t, h, pid, 12)
	fid := loan["id"].(string)
	doJSON(t, h, "POST", "/participants/"+pid+"/facilities/"+fid+"/disbursement", `{
		"counterparty":"`+gl+`","firstDue":"2025-02-15","description":"payout"
	}`, http.StatusCreated)

	// Alice's account holds exactly the disbursed €10,000 and no overdraft —
	// a repayment of €20,000 exceeds her available balance.
	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities/"+fid+"/repayments", `{
		"accountId":"`+did+`","amount":2000000,"date":"2025-02-01","description":"too much"
	}`, http.StatusUnprocessableEntity)

	bal := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did+"/balance", "", http.StatusOK)
	assertEqual(t, "book balance unchanged", int64(bal["book"].(float64)), int64(1000000))

	got := doJSON(t, h, "GET", "/participants/"+pid+"/facilities/"+fid, "", http.StatusOK)
	assertEqual(t, "drawn unchanged", int64(got["drawn"].(float64)), int64(1000000))
}

// TestChargeInterestOnRevolvingLine covers billing a revolving line's cycle:
// interest is capitalized into drawn principal and one instalment (the
// minimum payment) is appended to the schedule.
func TestChargeInterestOnRevolvingLine(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Alice","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	line := openRevolvingLine(t, h, pid, 500000)
	fid := line["id"].(string)
	doJSON(t, h, "POST", "/participants/"+pid+"/facilities/"+fid+"/draws", `{
		"counterparty":"`+gl+`","amount":200000,"description":"draw"
	}`, http.StatusCreated)

	// Accrue over a month, then bill the cycle. The response carries BOTH
	// halves — the capitalization posting and the instalment billed — because
	// a cycle can bill without posting; see chargeDTO.
	assertStatus(t, h, "POST", "/participants/"+pid+"/end-of-day", `{"date":"2025-02-15"}`, http.StatusNoContent)
	charged := doJSON(t, h, "POST", "/participants/"+pid+"/facilities/"+fid+"/interest-charge", `{"date":"2025-02-15"}`, http.StatusOK)
	posting, ok := charged["transaction"].(map[string]any)
	if !ok || posting["id"] == "" {
		t.Fatalf("charge response = %v, want a posted transaction", charged)
	}
	billed, ok := charged["installment"].(map[string]any)
	if !ok || int(billed["seq"].(float64)) != 1 {
		t.Fatalf("charge response = %v, want the first cycle billed", charged)
	}

	got := doJSON(t, h, "GET", "/participants/"+pid+"/facilities/"+fid, "", http.StatusOK)
	if drawn := int64(got["drawn"].(float64)); drawn <= 200000 {
		t.Fatalf("drawn after charging = %d, want > 200000 (interest capitalized into principal)", drawn)
	}

	var schedule []installmentDTO
	getJSON(t, h, "/participants/"+pid+"/facilities/"+fid+"/schedule", &schedule)
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
	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities/"+loan["id"].(string)+"/interest-charge",
		`{"date":"2025-02-15"}`, http.StatusUnprocessableEntity)
}

// TestChargeInterestBillsACycleWithNothingToPost covers the outcome the old
// bare-transaction response could not express: a line drawn and charged before
// any accrual has ticked posts nothing and still bills a cycle. The response
// must carry the instalment and no transaction — a client that saw only an
// empty body would report "nothing to charge" while the schedule gained a row.
func TestChargeInterestBillsACycleWithNothingToPost(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Alice","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	line := openRevolvingLine(t, h, pid, 500000)
	fid := line["id"].(string)
	doJSON(t, h, "POST", "/participants/"+pid+"/facilities/"+fid+"/draws", `{
		"counterparty":"`+gl+`","amount":200000,"description":"draw"
	}`, http.StatusCreated)

	// No end-of-day has run, so nothing has accrued.
	charged := doJSON(t, h, "POST", "/participants/"+pid+"/facilities/"+fid+"/interest-charge", `{"date":"2025-01-15"}`, http.StatusOK)
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
	getJSON(t, h, "/participants/"+pid+"/facilities/"+fid+"/schedule", &schedule)
	if len(schedule) != 1 {
		t.Fatalf("schedule = %v, want the one billed cycle", schedule)
	}

	// An undrawn line does neither, and THAT is 204 — the empty outcome the
	// rest of this API already expresses that way.
	empty := openRevolvingLine(t, h, pid, 500000)
	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities/"+empty["id"].(string)+"/interest-charge",
		`{"date":"2025-01-15"}`, http.StatusNoContent)
}

// TestChargeOverdraftInterestEndpoint covers POST
// /participants/{pid}/deposit-accounts/{did}/interest-charge: the monthly
// capitalization a customer actually sees, on the deposit side. Nothing
// accrued is 204 — nothing posts and, unlike a revolving line's cycle,
// nothing is billed either.
func TestChargeOverdraftInterestEndpoint(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Alice","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	// Nothing has accrued yet — an account that has never been overdrawn.
	assertStatus(t, h, "POST", "/participants/"+pid+"/deposit-accounts/"+did+"/interest-charge",
		`{"date":"2025-01-31"}`, http.StatusNoContent)

	doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts/"+did+"/overdraft-terms", `{
		"limit":100000,"rate":120000,"unarrangedRate":0,"dayCount":"ACT/365"
	}`, http.StatusOK)

	glLedger := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	sub := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers/"+glLedger+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	equity := doJSON(t, h, "POST", "/participants/"+pid+"/subledgers/"+sub+"/accounts",
		`{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	// Draw €500 into the overdraft with a direct GL posting, the mechanism
	// deposit.overdraftAccrual's own doc describes.
	doJSON(t, h, "POST", "/participants/"+pid+"/transactions", `{
		"entries":[
			{"accountId":"`+gl+`","amount":50000,"direction":"Debit"},
			{"accountId":"`+equity+`","amount":50000,"direction":"Credit"}
		]
	}`, http.StatusCreated)

	// The accrual window opens when the terms are set — here the test clock's
	// 15 January — not on the first end-of-day, so both runs below accrue.
	// €500 at 12% ACT/365 is 50_000 × 120_000 / 365 = 16_438_356
	// micro-minor-units a day; two days is 32_876_712, which rounds to 33
	// cents. The value-dated recompute is what makes the first day count: the
	// increment it replaced took its first run as the baseline and charged
	// nothing for it, dropping a day of interest on every account ever priced.
	assertStatus(t, h, "POST", "/participants/"+pid+"/end-of-day", `{"date":"2025-01-16"}`, http.StatusNoContent)
	assertStatus(t, h, "POST", "/participants/"+pid+"/end-of-day", `{"date":"2025-01-17"}`, http.StatusNoContent)

	acct := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did, "", http.StatusOK)
	assertEqual(t, "accrued before charging", int64(acct["accruedInterest"].(float64)), int64(33))

	tx := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts/"+did+"/interest-charge",
		`{"date":"2025-01-17"}`, http.StatusOK)
	if tx["id"] == nil || tx["id"].(string) == "" {
		t.Fatalf("charge did not return a transaction: %v", tx)
	}
	entries, ok := tx["entries"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("charge posted %v, want two entries", tx["entries"])
	}

	// Capitalized: the customer now owes the interest as part of the balance,
	// and the receivable is back to nothing.
	bal := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did+"/balance", "", http.StatusOK)
	assertEqual(t, "book balance after charging", int64(bal["book"].(float64)), int64(-50033))

	after := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did, "", http.StatusOK)
	assertEqual(t, "receivable after charging", int64(after["accruedInterest"].(float64)), int64(0))

	// And charging again, with the receivable cleared, is the empty outcome.
	assertStatus(t, h, "POST", "/participants/"+pid+"/deposit-accounts/"+did+"/interest-charge",
		`{"date":"2025-01-17"}`, http.StatusNoContent)
}

// TestEndOfDayAccruesBothFacilityAndOverdraftInterest is the HTTP-layer half
// of payment.Participant.RunEndOfDay: one POST /end-of-day drives both credit
// batches, so a facility and an overdrawn deposit account both accrue from a
// single call. The deposit account's accrual baseline starts zero (it is only
// set on its first end-of-day run), so a second run is what actually accrues
// its interest; the facility's baseline is set at disbursement, so it accrues
// on the first run already.
func TestEndOfDayAccruesBothFacilityAndOverdraftInterest(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	did := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Alice","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did, "", http.StatusOK)["glAccount"].(string)

	doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts/"+did+"/overdraft-terms", `{
		"limit":100000,"rate":120000,"unarrangedRate":0,"dayCount":"ACT/365"
	}`, http.StatusOK)

	glLedger := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	sub := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers/"+glLedger+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	equity := doJSON(t, h, "POST", "/participants/"+pid+"/subledgers/"+sub+"/accounts",
		`{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	// Draw Alice's account into an overdraft via a direct GL posting — the
	// same mechanism deposit.overdraftAccrual's own doc describes, since
	// CheckWithdrawal is not the only way an account ends up overdrawn.
	doJSON(t, h, "POST", "/participants/"+pid+"/transactions", `{
		"entries":[
			{"accountId":"`+gl+`","amount":50000,"direction":"Debit"},
			{"accountId":"`+equity+`","amount":50000,"direction":"Credit"}
		]
	}`, http.StatusCreated)

	loan := openTermLoan(t, h, pid, 12)
	fid := loan["id"].(string)
	doJSON(t, h, "POST", "/participants/"+pid+"/facilities/"+fid+"/disbursement", `{
		"counterparty":"`+equity+`","firstDue":"2025-02-15","description":"payout"
	}`, http.StatusCreated)

	assertStatus(t, h, "POST", "/participants/"+pid+"/end-of-day", `{"date":"2025-01-16"}`, http.StatusNoContent)
	assertStatus(t, h, "POST", "/participants/"+pid+"/end-of-day", `{"date":"2025-01-17"}`, http.StatusNoContent)

	acct := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+did, "", http.StatusOK)
	if got := int64(acct["accruedInterest"].(float64)); got <= 0 {
		t.Fatalf("deposit account accrued interest = %d, want > 0", got)
	}

	got := doJSON(t, h, "GET", "/participants/"+pid+"/facilities/"+fid, "", http.StatusOK)
	if accrued := int64(got["accruedInterest"].(float64)); accrued <= 0 {
		t.Fatalf("facility accrued interest = %d, want > 0", accrued)
	}
}

// TestTotalsReportsDerivedOverdraft covers GET /totals: one bank with one
// account in credit and one drawn into an overdraft (via a direct GL posting,
// bypassing CheckWithdrawal) reports both figures, split by sign and keyed by
// asset — no journal posts this number anywhere.
func TestTotalsReportsDerivedOverdraft(t *testing.T) {
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)

	alice := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts",
		`{"name":"Alice","asset":"EUR","overdraftLimit":100000}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Bob","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	aliceGL := doJSON(t, h, "GET", "/participants/"+pid+"/deposit-accounts/"+alice, "", http.StatusOK)["glAccount"].(string)

	doJSON(t, h, "POST", "/participants/"+pid+"/deposits",
		`{"account":"`+bob+`","amount":100000,"description":"opening"}`, http.StatusOK)

	glLedger := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	sub := doJSON(t, h, "POST", "/participants/"+pid+"/ledgers/"+glLedger+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	equity := doJSON(t, h, "POST", "/participants/"+pid+"/subledgers/"+sub+"/accounts",
		`{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	doJSON(t, h, "POST", "/participants/"+pid+"/transactions", `{
		"entries":[
			{"accountId":"`+aliceGL+`","amount":30000,"direction":"Debit"},
			{"accountId":"`+equity+`","amount":30000,"direction":"Credit"}
		]
	}`, http.StatusCreated)

	var totals []totalsDTO
	getJSON(t, h, "/participants/"+pid+"/totals", &totals)
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
	h := newTestServer(t)
	pid := doJSON(t, h, "POST", "/participants", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	// A real, funded deposit account, so the repayments case below clears the
	// available-funds check and 404s on the FACILITY lookup, not on the
	// account or on insufficient funds.
	did := doJSON(t, h, "POST", "/participants/"+pid+"/deposit-accounts", `{"name":"Alice","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	doJSON(t, h, "POST", "/participants/"+pid+"/deposits",
		`{"account":"`+did+`","amount":100000,"description":"opening"}`, http.StatusOK)

	assertStatus(t, h, "GET", "/participants/"+pid+"/facilities/nope", "", http.StatusNotFound)
	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities/nope/draws",
		`{"counterparty":"x","amount":100,"description":"x"}`, http.StatusNotFound)
	assertStatus(t, h, "POST", "/participants/"+pid+"/facilities/nope/repayments",
		`{"accountId":"`+did+`","amount":100,"date":"2025-02-01","description":"x"}`, http.StatusNotFound)
	assertStatus(t, h, "DELETE", "/participants/"+pid+"/facilities/nope", "", http.StatusNotFound)

	var schedule []installmentDTO
	getJSON(t, h, "/participants/"+pid+"/facilities/nope/schedule", &schedule)
	assertEqual(t, "schedule of an unknown facility", len(schedule), 0)
}
