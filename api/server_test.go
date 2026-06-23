package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/raphi011/ledger/payment"
)

var fixedTime = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	net := payment.NewNetworkWithClock(func() time.Time { return fixedTime })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewServer(net, log).Routes()
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
