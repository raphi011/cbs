package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/cbs/api"
	bankapi "github.com/raphi011/cbs/api/bank"
	cbapi "github.com/raphi011/cbs/api/centralbank"
	csmapi "github.com/raphi011/cbs/api/csm"
	"github.com/raphi011/cbs/calendar"
	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/provision"
	"github.com/raphi011/cbs/seed"
	"github.com/raphi011/cbs/store/storetest"
	"github.com/raphi011/cbs/store/testenv"
)

var fixedTime = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

// The three operator surfaces a test drives. Which one a request goes to is
// decided by the route, exactly as a port decides it in the running system —
// so a test that asks the wrong operator gets a 404, which is the point.

// server is the deployment under test: the three surfaces, and the two handles
// a fixture reaches behind them.
type server struct {
	dep  *Deployment
	nets *payment.Networks
	// clock is the deployment's business date. It MOVES: POST /clock/day
	// advances it, and every row written afterwards carries the new date.
	clock *calendar.Clock
}

func (s *server) CentralBankRoutes() http.Handler {
	return cbapi.Routes(s.dep.CentralBank(), operator{s.dep}).Handler(s.dep.Log())
}

func (s *server) ClearingHouseRoutes() http.Handler {
	return csmapi.Routes(s.dep.ClearingHouse(), operator{s.dep}).Handler(s.dep.Log())
}

// BankRoutes binds one member bank's surface. It takes a context and can fail
// where the two institutions' cannot, because it opens that bank's own database.
func (s *server) BankRoutes(ctx context.Context, pid payment.ParticipantID) (http.Handler, error) {
	b, err := s.dep.Bank(ctx, pid)
	if err != nil {
		return nil, err
	}
	return bankapi.Routes(b).Handler(s.dep.Log()), nil
}

func cbSurface(s *server) http.Handler  { return s.CentralBankRoutes() }
func csmSurface(s *server) http.Handler { return s.ClearingHouseRoutes() }

// bankSurface binds one member bank's surface.
func bankSurface(s *server, pid string) http.Handler {
	h, err := s.BankRoutes(context.Background(), payment.ParticipantID(pid))
	if err != nil {
		panic("server_test: binding " + pid + "'s surface: " + err.Error())
	}
	return h
}

// newServer builds a deployment over an empty ephemeral store, with both hosts
// really listening. populate is the reseed function, called once now as the
// process would at boot and again on every reset; pass nil to reset to an empty
// system.
func newServer(t *testing.T, populate func(context.Context, *payment.Networks, seed.Deployment) error) *server {
	t.Helper()
	clock := calendar.NewClock(fixedTime)
	set := testenv.NewSet(t, clock.Now)
	nets := payment.NewNetworks(set, clock.Now)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	s := &server{nets: nets, clock: clock}
	// The two hosts, on real listeners. The handler is looked up per REQUEST
	// because the deployment does not exist yet: its config names these two URLs,
	// so the listeners have to be standing before it is built.
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

	dep, err := NewDeployment(context.Background(), nets, set, clock, cfg, populate, log)
	if err != nil {
		t.Fatalf("NewDeployment: %v", err)
	}
	s.dep = dep

	// The sample dataset LAST, and over the deployment the listeners are already
	// serving — which is the order main uses and for the same reason: a reseed
	// admits its banks through this deployment's own doors.
	if populate != nil {
		if err := populate(context.Background(), nets, dep); err != nil {
			t.Fatalf("populate: %v", err)
		}
	}
	return s
}

// admitForPopulate is what a reseed here uses to make a bank: its three rows,
// then its place in the network.
func admitForPopulate(ctx context.Context, nets *payment.Networks, dep seed.Deployment,
	name string, bic iso20022.BIC) (*payment.Bank, error) {

	bank, err := provision.Bank(ctx, nets, provision.BankSpec{
		Name: name, BIC: bic, Country: storetest.FixtureCountry,
	})
	if err != nil {
		return nil, err
	}
	if err := dep.AddBank(ctx, bank); err != nil {
		return nil, err
	}
	return bank, nil
}

// carry runs every institution behind a server through everything queued for
// it.
func carry(t *testing.T, s *server) {
	t.Helper()
	if err := joinProblems(workThrough(s.dep)); err != nil {
		t.Fatalf("working through what has arrived: %v", err)
	}
}

// settle reaches the clearing house's cut-off on every open cycle and carries
// what it produces, which is what a fixture needs when it wants the RECEIVING
// side of a payment to exist at all. carry alone stops short of finality on
// purpose: the output files are held until the cycle settles.
func settle(t *testing.T, s *server) {
	t.Helper()
	var cycles []api.ClearingCycleDTO
	getJSON(t, csmSurface(s), "/cycles", &cycles)
	var closed int
	for _, c := range cycles {
		if c.Status != payment.CycleOpen.String() {
			continue
		}
		assertStatus(t, csmSurface(s), "POST", "/cycles/"+c.ID+"/close", "", http.StatusOK)
		closed++
	}
	if closed == 0 {
		t.Fatalf("no cycle was open to cut off; the fixture has nothing to settle (cycles: %+v)", cycles)
	}
	carry(t, s)
}

// provisionMember writes one bank's three rows, gives it its place in this
// deployment, and has every member pull the routing directory. It returns the
// participant id the routes below are keyed by.
func provisionMember(t *testing.T, s *server, bic, name string, assets ...string) string {
	t.Helper()
	ctx := context.Background()
	codes := make([]ledger.AssetCode, len(assets))
	for i, a := range assets {
		codes[i] = ledger.AssetCode(a)
	}
	p, err := provision.Bank(ctx, s.nets, provision.BankSpec{
		Name: name, BIC: iso20022.BIC(bic), Country: storetest.FixtureCountry, Assets: codes,
	})
	if err != nil {
		t.Fatalf("provisioning %s (%s): %v", name, bic, err)
	}
	if err := s.dep.AddBank(ctx, p); err != nil {
		t.Fatalf("AddBank %s: %v", bic, err)
	}
	subscribeAll(t, s)
	return string(p.ID)
}

// subscribeAll has every member pull the scheme's routing directory, over the
// route a bank's operator would use.
func subscribeAll(t *testing.T, s *server) {
	t.Helper()
	var members []map[string]any
	getJSON(t, cbSurface(s), "/members", &members)
	for _, m := range members {
		pid, _ := m["id"].(string)
		if pid == "" {
			continue
		}
		assertStatus(t, bankSurface(s, pid), "POST", "/directory/banks/refresh", "", http.StatusOK)
	}
}

// fundAndLodge gives a bank's customer a balance AND puts the cash on reserve,
// over HTTP.
func fundAndLodge(t *testing.T, s *server, pid, did string, amount int64) {
	t.Helper()
	doJSON(t, bankSurface(s, pid), "POST", "/deposits",
		fmt.Sprintf(`{"account":%q,"amount":%d,"description":"opening"}`, did, amount), http.StatusOK)
	lodge(t, s, pid, "EUR", amount)
}

// lodge puts one bank's vault cash on reserve over HTTP and carries the file.
// Separate from fundAndLodge for the fixtures that top a reserve up without a
// customer paying anything in.
func lodge(t *testing.T, s *server, pid, asset string, amount int64) {
	t.Helper()
	doJSON(t, bankSurface(s, pid), "POST", "/lodgements",
		fmt.Sprintf(`{"asset":%q,"amount":%d}`, asset, amount), http.StatusAccepted)
	carry(t, s)
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
// participant resource. Every deposit account is opened FROM a product, and a
// bank's default one is created with its chart of accounts at onboarding.
func prdOf(t *testing.T, h *server, pid string) string {
	t.Helper()
	return doJSON(t, bankSurface(h, pid), "GET", "/me", "", http.StatusOK)["productId"].(string)
}

func TestDepositFlow(t *testing.T) {
	h := newServer(t, nil)

	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	did := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)

	// Fund Alice and confirm the returned balance.
	bal := doJSON(t, bankSurface(h, pid), "POST", "/deposits", `{"account":"`+did+`","amount":100000,"description":"opening"}`, http.StatusOK)
	if got := int64(bal["available"].(float64)); got != 100000 {
		t.Fatalf("available after funding = %d, want 100000", got)
	}

	// Read the balance back.
	bal = doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did+"/balance", "", http.StatusOK)
	if got := int64(bal["book"].(float64)); got != 100000 {
		t.Fatalf("book balance = %d, want 100000", got)
	}
}

func TestSCTEndToEnd(t *testing.T) {
	h := newServer(t, nil)

	a := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	b := provisionMember(t, h, "BNKBDEFFXXX", "Bank B")
	// SCT addresses both legs by IBAN (payment.Scheme.AddressedBy), so both
	// accounts need one before submission will accept them.
	alice := doJSON(t, bankSurface(h, a), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, bankSurface(h, b), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`, http.StatusCreated)["id"].(string)

	fundAndLodge(t, h, a, alice, 100000)

	cyc := doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)["id"].(string)

	// The payee's IBAN is quoted, because on a push it is the payer who knows
	// it: their own bank has no way to look up an address in another bank's
	// register, and the pacs.008 it is about to build has to carry one.
	pay := doJSON(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+alice+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, alice)+`"}},
		"creditor":{"account":"`+bob+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, b, bob)+`"}},
		"amount":25000,
		"endToEndId":"e2e-1",
		"creditorName":"Bob"
	}`, http.StatusAccepted)
	// Initiated, not Accepted: the payee's bank has not seen it yet. Draining is
	// what carries the conversation to its end.
	if pay["status"].(string) != "Initiated" {
		t.Fatalf("payment status in the 202 = %q, want Initiated", pay["status"])
	}
	payID := pay["id"].(string)
	carry(t, h)
	if got := doJSON(t, csmSurface(h), "GET", "/payments/"+payID, "", http.StatusOK); got["status"].(string) != "Accepted" {
		t.Fatalf("payment status after draining = %q, want Accepted", got["status"])
	}

	// The cut-off, which is now also the settlement instruction: the clearing
	// house nets the batch and sends a pacs.009, and the central bank discharges
	// it in an actor of its own.
	assertStatus(t, csmSurface(h), "POST", "/cycles/"+cyc+"/close", "", http.StatusOK)
	carry(t, h)

	aliceBal := doJSON(t, bankSurface(h, a), "GET", "/deposit-accounts/"+alice+"/balance", "", http.StatusOK)
	if got := int64(aliceBal["book"].(float64)); got != 75000 {
		t.Fatalf("alice book = %d, want 75000", got)
	}
	bobBal := doJSON(t, bankSurface(h, b), "GET", "/deposit-accounts/"+bob+"/balance", "", http.StatusOK)
	if got := int64(bobBal["book"].(float64)); got != 25000 {
		t.Fatalf("bob book = %d, want 25000", got)
	}

	// Reserves are reported one row per asset, so a euro bank has exactly one.
	var reserveA []map[string]any
	getJSON(t, cbSurface(h), "/reserves/"+a, &reserveA)
	if len(reserveA) != 1 || reserveA[0]["asset"] != "EUR" {
		t.Fatalf("bank A reserves = %v, want one EUR row", reserveA)
	}
	if got := int64(reserveA[0]["reserve"].(float64)); got != 75000 {
		t.Fatalf("bank A reserve = %d, want 75000", got)
	}

	got := doJSON(t, csmSurface(h), "GET", "/payments/"+payID, "", http.StatusOK)
	if got["status"].(string) != "Settled" {
		t.Fatalf("payment status after settle = %q, want Settled", got["status"])
	}
}

// An account without an asset is refused rather than opened in euro: nothing picks
// a currency on the caller's behalf, and a request body that forgot to say is where
// a default would go unnoticed.
func TestCreateAccountRequiresAsset(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	gl := doJSON(t, bankSurface(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, bankSurface(h, pid), "POST", "/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, bankSurface(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"No Asset","type":"Liability"}`, http.StatusBadRequest)

	// An asset the system does not know is a 400 — a bad field value, like an
	// unparseable account type — not a silent substitution, and not a 404,
	// which on this route would read as "participant not found".
	assertStatus(t, bankSurface(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Dogecoin","type":"Liability","asset":"DOGE"}`, http.StatusBadRequest)
}

func TestOpenDepositAccountRequiresAsset(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")

	assertStatus(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"No Asset"}`, http.StatusBadRequest)
}

// GET /assets lists the assets the system knows, with the scale a client needs
// to render an amount. Read-only and network-wide: assets are defined in code,
// like schemes, so there is nothing to create and nothing per-participant.
func TestListAssets(t *testing.T) {
	h := newServer(t, nil)

	var assets []api.AssetDTO
	getJSON(t, csmSurface(h), "/assets", &assets)

	byCode := make(map[string]api.AssetDTO, len(assets))
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

// TestAnAssetTheAgentHasNotAnsweredForYetIsAMissingRowNotA422 pins the second
// way a reserve can be absent, the one that is neither rare nor broken.
func TestAnAssetTheAgentHasNotAnsweredForYetIsAMissingRowNotA422(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A", "EUR", "USD")

	// Admitted in both, so both rows are there to begin with.
	var full []map[string]any
	getJSON(t, cbSurface(h), "/reserves/"+pid, &full)
	if len(full) != 2 {
		t.Fatalf("a two-asset member reports %v, want a row per asset", full)
	}

	// Both records put back as the window between the two acknowledgements leaves
	// them: euro answered, dollars not.
	if err := h.nets.CentralBank().Store().Update(context.Background(), func(ctx context.Context, tx payment.CentralBankTx) error {
		member, err := tx.GetSettlementMember(ctx, "BNKADEFFXXX")
		if err != nil {
			return err
		}
		member.Accounts = map[ledger.AssetCode]ledger.AccountID{"EUR": member.Accounts["EUR"]}
		return tx.PutSettlementMember(ctx, member)
	}); err != nil {
		t.Fatalf("rewinding the agent's register to the window between the two acknowledgements: %v", err)
	}
	bankNet, err := h.nets.Bank(context.Background(), payment.ParticipantID(pid))
	if err != nil {
		t.Fatalf("opening the applicant's store: %v", err)
	}
	if err := bankNet.Store().Update(context.Background(), func(ctx context.Context, tx payment.BankTx) error {
		b, err := tx.GetBank(ctx, payment.ParticipantID(pid))
		if err != nil {
			return err
		}
		usd := b.Assets["USD"]
		usd.Settlement = ""
		b.Assets["USD"] = usd
		return tx.PutBank(ctx, b)
	}); err != nil {
		t.Fatalf("rewinding the applicant's own row: %v", err)
	}

	// One row, not a refusal — and the bank still says it operates in both,
	// which is what leaves an operator able to see the difference.
	var partial []map[string]any
	getJSON(t, cbSurface(h), "/reserves/"+pid, &partial)
	if len(partial) != 1 || partial[0]["asset"] != "EUR" {
		t.Fatalf("mid-admission reserves = %v, want the euro row alone", partial)
	}
	me := doJSON(t, bankSurface(h, pid), "GET", "/me", "", http.StatusOK)
	if got := len(me["assets"].([]any)); got != 2 {
		t.Errorf("the bank reports %d assets, want 2; the missing row is the agent's answer, not the bank's", got)
	}

	// And the list route reports the network rather than failing on one bank.
	var all []map[string]any
	getJSON(t, cbSurface(h), "/reserves", &all)
	if len(all) != 1 {
		t.Fatalf("the reserve list = %v, want the one row the agent can answer for", all)
	}
}

// TestAccountResponseIncludesAsset pins that the account, deposit-account and
// entry DTOs all carry the asset they are denominated in.
func TestAccountResponseIncludesAsset(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)

	res := do(t, bankSurface(h, pid), "GET", "/deposit-accounts", "")
	if !strings.Contains(res.Body.String(), `"asset":"EUR"`) {
		t.Errorf("deposit-account listing has no asset field: %s", res.Body)
	}

	gl := doJSON(t, bankSurface(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, bankSurface(h, pid), "POST", "/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, bankSurface(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)
	assertEqual(t, "account asset", acct["asset"].(string), "EUR")

	other := doJSON(t, bankSurface(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	tx := doJSON(t, bankSurface(h, pid), "POST", "/transactions", `{
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

// TestBalanceEndpointReportsTheValueDatedBalance: the endpoint reports the
// value-dated balance alongside the book balance, and the two diverge whenever a
// posting's value date is forward of its booking date.
func TestBalanceEndpointReportsTheValueDatedBalance(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	gl := doJSON(t, bankSurface(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, bankSurface(h, pid), "POST", "/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, bankSurface(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	other := doJSON(t, bankSurface(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	today := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	post := func(amount int, valueDate time.Time) {
		doJSON(t, bankSurface(h, pid), "POST", "/transactions", fmt.Sprintf(`{
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
	getJSON(t, bankSurface(h, pid), "/accounts/"+acct+"/balance?asOf="+today.Format(time.RFC3339), &got)

	if got.Balance != 15_000 {
		t.Errorf("balance = %d, want 15000", got.Balance)
	}
	if got.ValueDateBalance != 10_000 {
		t.Errorf("valueDateBalance = %d, want 10000 (the forward-dated movement has not taken effect)", got.ValueDateBalance)
	}
}

// TestBalanceEndpointDefaultsAsOfToNow is the other half of the parameter: omitted
// entirely, asOf is now, so a movement value-dated in the past counts and one in
// the future does not.
func TestBalanceEndpointDefaultsAsOfToNow(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	gl := doJSON(t, bankSurface(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, bankSurface(h, pid), "POST", "/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, bankSurface(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	other := doJSON(t, bankSurface(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	post := func(amount int, valueDate time.Time) {
		doJSON(t, bankSurface(h, pid), "POST", "/transactions", fmt.Sprintf(`{
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
	getJSON(t, bankSurface(h, pid), "/accounts/"+acct+"/balance", &got)

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
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	gl := doJSON(t, bankSurface(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, bankSurface(h, pid), "POST", "/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, bankSurface(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, bankSurface(h, pid), "GET", "/accounts/"+acct+"/balance?asOf=not-a-date", "", http.StatusBadRequest)

	// And the parse happens before the account is read: an unknown account with a
	// malformed asOf is still a 400, which keeps a bad request from costing two store
	// round trips before being refused.
	assertStatus(t, bankSurface(h, pid), "GET", "/accounts/nope/balance?asOf=not-a-date", "", http.StatusBadRequest)
}

// TestCrossAssetPaymentReturns422 is the HTTP half of
// payment.ErrAssetMismatch's mapping.
func TestCrossAssetPaymentReturns422(t *testing.T) {
	h := newServer(t, nil)
	a := provisionMember(t, h, "BNKADEFFXXX", "Bank A", "USD")
	b := provisionMember(t, h, "BNKBDEFFXXX", "Bank B")
	alice := doJSON(t, bankSurface(h, a), "POST", "/deposit-accounts", `{"name":"Alice","asset":"USD","productId":"`+prdOf(t, h, a)+`"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, bankSurface(h, b), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`, http.StatusCreated)["id"].(string)

	assertStatus(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+alice+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, alice)+`"}},
		"creditor":{"account":"`+bob+`"},
		"amount":1000,
		"creditorName":"Bob"
	}`, http.StatusUnprocessableEntity)
}

// TestPaymentDTOsCarryAsset: MandateDTO, ClearingCycleDTO, SettlementDTO and
// SchemeDTO each carry their asset, resolved server-side.
func TestPaymentDTOsCarryAsset(t *testing.T) {
	h := newServer(t, nil)

	a := provisionMember(t, h, "BNKADEFFXXX", "Bank A", "USD", "EUR")
	b := provisionMember(t, h, "BNKBDEFFXXX", "Bank B", "EUR", "USD")
	aliceUSD := doJSON(t, bankSurface(h, a), "POST", "/deposit-accounts", `{"name":"AliceUSD","asset":"USD","productId":"`+prdOf(t, h, a)+`"}`, http.StatusCreated)["id"].(string)
	// aliceEUR and bob feed the SCT payment below, which addresses both legs by
	// IBAN (payment.Scheme.AddressedBy) — aliceUSD and bobUSD only ever back a
	// mandate, which carries no scheme and so is never addressed.
	aliceEUR := doJSON(t, bankSurface(h, a), "POST", "/deposit-accounts", `{"name":"AliceEUR","asset":"EUR","productId":"`+prdOf(t, h, a)+`"}`, http.StatusCreated)["id"].(string)
	bobUSD := doJSON(t, bankSurface(h, b), "POST", "/deposit-accounts", `{"name":"BobUSD","asset":"USD","productId":"`+prdOf(t, h, b)+`"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, bankSurface(h, b), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`, http.StatusCreated)["id"].(string)

	// Mandate: the asset comes off the CREDITOR's account, the one the recording
	// bank holds.
	mandate := doJSON(t, bankSurface(h, b), "POST", "/mandates", `{
		"debtor":{"account":"`+aliceUSD+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, aliceUSD)+`"}},
		"creditor":{"account":"`+bobUSD+`"},
		"maxAmount":50000
	}`, http.StatusCreated)
	assertEqual(t, "created mandate asset", mandate["asset"].(string), "USD")
	mid := mandate["id"].(string)

	var mandates []api.MandateDTO
	getJSON(t, bankSurface(h, b), "/mandates", &mandates)
	if len(mandates) != 1 || mandates[0].Asset != "USD" {
		t.Fatalf("GET /mandates asset = %+v, want one USD mandate", mandates)
	}

	got := doJSON(t, bankSurface(h, b), "GET", "/mandates/"+mid, "", http.StatusOK)
	assertEqual(t, "GET mandate asset", got["asset"].(string), "USD")

	revoked := doJSON(t, bankSurface(h, b), "POST", "/mandates/"+mid+"/revoke", "", http.StatusOK)
	assertEqual(t, "revoked mandate asset", revoked["asset"].(string), "USD")

	// Scheme: every scheme implemented today settles in EUR (see
	// payment/scheme.go's SCT/SDD) — the field must still be populated, not
	// merely absent-and-unnoticed.
	var schemes []api.SchemeDTO
	getJSON(t, csmSurface(h), "/schemes", &schemes)
	if len(schemes) == 0 {
		t.Fatal("no schemes returned")
	}
	for _, sc := range schemes {
		assertEqual(t, "scheme "+sc.ID+" asset", sc.Asset, "EUR")
	}

	// Cycle and settlement: derived from the scheme.
	fundAndLodge(t, h, a, aliceEUR, 100000)

	cyc := doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	assertEqual(t, "created cycle asset", cyc["asset"].(string), "EUR")
	cid := cyc["id"].(string)

	var cycles []api.ClearingCycleDTO
	getJSON(t, csmSurface(h), "/cycles", &cycles)
	if len(cycles) != 1 || cycles[0].Asset != "EUR" {
		t.Fatalf("GET /cycles asset = %+v, want one EUR cycle", cycles)
	}

	doJSON(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+aliceEUR+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, aliceEUR)+`"}},
		"creditor":{"account":"`+bob+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, b, bob)+`"}},
		"amount":1000,
		"creditorName":"Bob"
	}`, http.StatusAccepted)
	carry(t, h)

	assertStatus(t, csmSurface(h), "POST", "/cycles/"+cid+"/close", "", http.StatusOK)
	carry(t, h)
	sid := settlementOfCycle(t, h, cid)

	// On the SETTLEMENT AGENT's console, which is whose rows these are, and the
	// asset is on the row rather than joined from the cycle's scheme. See
	// payment.Settlement.Asset.
	var settlements []api.SettlementDTO
	getJSON(t, cbSurface(h), "/settlements", &settlements)
	if len(settlements) != 1 || settlements[0].Asset != "EUR" {
		t.Fatalf("GET /settlements asset = %+v, want one EUR settlement", settlements)
	}

	gotSettlement := doJSON(t, cbSurface(h), "GET", "/settlements/"+sid, "", http.StatusOK)
	assertEqual(t, "GET settlement asset", gotSettlement["asset"].(string), "EUR")
}

// settlementOfCycle is the settlement a cut-off produced, found at the
// SETTLEMENT AGENT by the cycle it names.
func settlementOfCycle(t *testing.T, h *server, cid string) string {
	t.Helper()
	var c api.ClearingCycleDTO
	getJSON(t, csmSurface(h), "/cycles/"+cid, &c)
	if c.Status != "Settled" {
		t.Fatalf("cycle %s is %q, want Settled — the central bank refused the instruction or was never asked", cid, c.Status)
	}
	var settlements []api.SettlementDTO
	getJSON(t, cbSurface(h), "/settlements", &settlements)
	for _, st := range settlements {
		if st.CycleID == cid {
			return st.ID
		}
	}
	t.Fatalf("cycle %s is Settled and the settlement agent lists no settlement against it", cid)
	return ""
}

// TestNoRouteSettlesACycle pins the absent route.
func TestNoRouteSettlesACycle(t *testing.T) {
	h := newServer(t, nil)
	cid := settledCycle(t, h)

	// 405 and not 404 on the central bank's /settlements: GET /settlements is still
	// there, so the PATH exists and the METHOD does not. That is what "keep the
	// reads, drop the action" means, said in a status code.
	assertStatus(t, cbSurface(h), "POST", "/settlements", `{"cycleId":"`+cid+`"}`, http.StatusMethodNotAllowed)
	// On the CLEARING HOUSE the path is gone entirely: a settlement is the settlement
	// agent's row and that institution's database has no table for one, so there is
	// nothing to say 405 about.
	assertStatus(t, csmSurface(h), "POST", "/settlements", `{"cycleId":"`+cid+`"}`, http.StatusNotFound)
	assertStatus(t, csmSurface(h), "GET", "/settlements", "", http.StatusNotFound)
	// The central bank has no settle route at all: instructing is the clearing
	// house's act, and settling is this operator's but is not an HTTP one.
	assertStatus(t, cbSurface(h), "POST", "/cycles/"+cid+"/settle", "", http.StatusNotFound)
	// And on the clearing house it exists and refuses THIS cycle, which separates it
	// from the route it shares a path with: it only re-instructs a cycle that is still
	// Closed, so an already-settled one is 422 and no second pacs.009 is built.
	assertStatus(t, csmSurface(h), "POST", "/cycles/"+cid+"/settle", "", http.StatusUnprocessableEntity)
	var settlementsAfter []api.SettlementDTO
	getJSON(t, cbSurface(h), "/settlements", &settlementsAfter)
	if len(settlementsAfter) != 1 {
		t.Fatalf("asking a settled cycle to settle again produced %d settlements, want the original 1", len(settlementsAfter))
	}

	// The reads still answer, each on the operator whose rows they are. The CYCLES
	// are the clearing house's and the central bank's listener does not serve them:
	// that institution's database has no cycles table.
	var cycles []api.ClearingCycleDTO
	getJSON(t, csmSurface(h), "/cycles", &cycles)
	if len(cycles) != 1 || cycles[0].ID != cid {
		t.Fatalf("the clearing house sees %v, want the one closed cycle %s", cycles, cid)
	}
	assertStatus(t, cbSurface(h), "GET", "/cycles", "", http.StatusNotFound)
	// And a settlement it can read, which no request in this test asked for: the
	// cut-off instructed it. That is the shape the deleted POST leaves behind —
	// the console watches settlement rather than performing it.
	var settlements []api.SettlementDTO
	getJSON(t, cbSurface(h), "/settlements", &settlements)
	if len(settlements) != 1 {
		t.Fatalf("the central bank sees %d settlements, want the one the cut-off instructed", len(settlements))
	}
}

// TestARefusedSettlementIsRecoverableOverHTTP walks the whole way out of a
// refused settlement through the console, where an operator is.
func TestARefusedSettlementIsRecoverableOverHTTP(t *testing.T) {
	h := newServer(t, nil)
	a := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	b := provisionMember(t, h, "BNKBDEFFXXX", "Bank B")
	alice := doJSON(t, bankSurface(h, a), "POST", "/deposit-accounts",
		`{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`"}`,
		http.StatusCreated)["id"].(string)
	bob := doJSON(t, bankSurface(h, b), "POST", "/deposit-accounts",
		`{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`,
		http.StatusCreated)["id"].(string)
	doJSON(t, bankSurface(h, a), "POST", "/deposit-accounts/"+alice+"/overdraft-limit", `{"limit":100000}`, http.StatusOK)

	cyc := doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)["id"].(string)
	pay := doJSON(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+alice+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, alice)+`"}},
		"creditor":{"account":"`+bob+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, b, bob)+`"}},
		"amount":25000,
		"endToEndId":"short-reserve",
		"creditorName":"Bob"
	}`, http.StatusAccepted)["id"].(string)
	carry(t, h)
	assertStatus(t, csmSurface(h), "POST", "/cycles/"+cyc+"/close", "", http.StatusOK)
	carry(t, h)

	// The stuck state, as the console sees it. The AM04 is nowhere here — it
	// travelled between two actors in a pacs.002 and is stored nowhere — so what
	// an operator reads is a closed cycle with no settlement against it.
	stuck := doJSON(t, csmSurface(h), "GET", "/cycles/"+cyc, "", http.StatusOK)
	if stuck["status"] != "Closed" || stuck["settlementId"] != nil {
		t.Fatalf("cycle = %v, want Closed with no settlement", stuck)
	}
	if got := doJSON(t, csmSurface(h), "GET", "/payments/"+pay, "", http.StatusOK)["status"]; got != "Cleared" {
		t.Fatalf("payment status = %v, want Cleared", got)
	}
	bobBal := doJSON(t, bankSurface(h, b), "GET", "/deposit-accounts/"+bob+"/balance", "", http.StatusOK)
	if got := int64(bobBal["book"].(float64)); got != 0 {
		t.Fatalf("Bob holds %d before settlement, want 0", got)
	}

	// The remedy: fund the short member, then ask the clearing house to instruct
	// settlement again.
	fundAndLodge(t, h, a, alice, 25000)
	assertStatus(t, csmSurface(h), "POST", "/cycles/"+cyc+"/settle", "", http.StatusAccepted)
	carry(t, h)

	settled := doJSON(t, csmSurface(h), "GET", "/cycles/"+cyc, "", http.StatusOK)
	if settled["status"] != "Settled" {
		t.Fatalf("cycle = %v, want Settled", settled)
	}
	// And the settlement itself, on the SETTLEMENT AGENT's console, matched by the
	// cut-off it names. The cycle carries no settlement id: that id is allocated
	// inside the agent's own unit of work, and the clearing house is never told it.
	var settlements []api.SettlementDTO
	getJSON(t, cbSurface(h), "/settlements", &settlements)
	var found bool
	for _, st := range settlements {
		found = found || st.CycleID == cyc
	}
	if !found {
		t.Fatalf("the settlement agent has no settlement against %s: %v", cyc, settlements)
	}
	if got := doJSON(t, csmSurface(h), "GET", "/payments/"+pay, "", http.StatusOK)["status"]; got != "Settled" {
		t.Fatalf("payment status = %v, want Settled", got)
	}
	// The assertion that says money arrived rather than that a status changed.
	bobBal = doJSON(t, bankSurface(h, b), "GET", "/deposit-accounts/"+bob+"/balance", "", http.StatusOK)
	if got := int64(bobBal["book"].(float64)); got != 25000 {
		t.Fatalf("Bob holds %d after settlement, want 25000", got)
	}
}

// TestErrorMapping locks one error per HTTP status class.
func TestErrorMapping(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	did := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)

	// 404: unknown participant. A well-formed address nobody founded, not the string
	// "nope" — binding a bank's surface OPENS that bank's database, and an address
	// that is not a BIC has no database to open.
	assertStatus(t, bankSurface(h, "NOSUCHBKXXX"), "GET", "/me", "", http.StatusNotFound)

	// 422: withdrawal hold exceeding available balance (account has no funds).
	assertStatus(t, bankSurface(h, pid), "POST", "/deposit-accounts/"+did+"/holds", `{"amount":5000}`, http.StatusUnprocessableEntity)

	// 400: unbalanced transaction.
	gl := doJSON(t, bankSurface(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, bankSurface(h, pid), "POST", "/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, bankSurface(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	other := doJSON(t, bankSurface(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	assertStatus(t, bankSurface(h, pid), "POST", "/transactions", `{
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
	assertStatus(t, bankSurface(h, pid), "POST", "/transactions", body, http.StatusCreated)
	assertStatus(t, bankSurface(h, pid), "POST", "/transactions", body, http.StatusConflict)

	// 400: invalid enum value.
	assertStatus(t, bankSurface(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Bad","type":"Nonsense"}`, http.StatusBadRequest)

	// 400: a lending sentinel in the malformed-input category. A term past
	// lending.MaxTermMonths is a bad field value, not a business-state
	// conflict, so it maps beside a non-positive amount.
	assertStatus(t, bankSurface(h, pid), "POST", "/facilities", `{
		"kind":"TermLoan","name":"Absurd","asset":"EUR","commitment":1000000,
		"rate":60000,"dayCount":"ACT/365","method":"Annuity","termMonths":100000000
	}`, http.StatusBadRequest)

	// 422: a lending sentinel in the wrong-state category — charging a term
	// loan, which settles interest through its scheduled instalments.
	loan := openTermLoan(t, h, pid, 12)
	assertStatus(t, bankSurface(h, pid), "POST", "/facilities/"+loan["id"].(string)+"/interest-charge", `{"date":"2025-02-15"}`, http.StatusUnprocessableEntity)

	// 409: a billing cycle already on the schedule. The request is valid and
	// the state already reflects it, which is the already-applied category a
	// duplicate idempotency key is in — not a 422.
	line := openRevolvingLine(t, h, pid, 500000)
	lid := line["id"].(string)
	doJSON(t, bankSurface(h, pid), "POST", "/facilities/"+lid+"/draws", `{
		"counterparty":"`+other+`","amount":200000,"description":"draw"
	}`, http.StatusCreated)
	doJSON(t, bankSurface(h, pid), "POST", "/facilities/"+lid+"/interest-charge", `{"date":"2025-02-15"}`, http.StatusOK)
	assertStatus(t, bankSurface(h, pid), "POST", "/facilities/"+lid+"/interest-charge", `{"date":"2025-02-15"}`, http.StatusConflict)
}

// TestAuditEndpointIncludesPayloadAndSeq guards against the DTO silently
// dropping fields again: seq, payload and scope must reach the wire.
func TestAuditEndpointIncludesPayloadAndSeq(t *testing.T) {
	h := newServer(t, nil)

	// Founding a bank posts several ledger audit events as a side effect
	// (ledger + subledgers + accounts), so no further setup is needed.
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")

	// The id the admission answered with, rather than a literal. A bank's
	// ParticipantID is its BIC and its database is opened by it, so a hard-coded
	// sequence id has no store behind it.
	rec := do(t, bankSurface(h, pid), "GET", "/audit", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET audit: got status %d, want %d (body: %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var events []api.AuditEventDTO
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
		b := strings.TrimSpace(do(t, cbSurface(h), "GET", "/members", "").Body.String())
		return b == "[]"
	}

	// Create a participant, confirm it's present.
	provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	if emptyList() {
		t.Fatal("expected one participant before reset, got empty list")
	}

	// Reset rebuilds state from the factory (empty in tests).
	doJSON(t, cbSurface(h), "POST", "/admin/reset", "", http.StatusOK)

	if !emptyList() {
		t.Fatal("expected empty participants after reset")
	}
}

// TestResetEmptiesState pins that POST /admin/reset clears the store rather
// than replacing an in-memory object graph.
func TestResetEmptiesState(t *testing.T) {
	// The tests' sample dataset: one bank with one customer. Idempotent, so booting
	// and resetting are the same call — and it asks "has anything been built here"
	// of the DEPLOYMENT, no institution holding a list of banks.
	baseline := func(ctx context.Context, nets *payment.Networks, msh seed.Deployment) error {
		existing, err := nets.Stores().Banks(ctx)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return nil
		}
		p, err := admitForPopulate(ctx, nets, msh, "Bank A", "BANKDEFFXXX")
		if err != nil {
			return err
		}
		_, err = p.OpenCustomerAccount(ctx, "Baseline", "EUR")
		return err
	}

	srv := newServer(t, baseline)

	names := func() []string {
		var accounts []api.DepositAccountDTO
		getJSON(t, bankSurface(srv, "BANKDEFFXXX"), "/deposit-accounts", &accounts)
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
	doJSON(t, bankSurface(srv, "BANKDEFFXXX"), "POST", "/deposit-accounts", `{"name":"Temp","asset":"EUR","overdraftLimit":0,"productId":"`+prdOf(t, srv, "BANKDEFFXXX")+`"}`, http.StatusCreated)
	if got := names(); len(got) != 2 {
		t.Fatalf("accounts after the mutation = %v, want two", got)
	}

	doJSON(t, cbSurface(srv), "POST", "/admin/reset", "", http.StatusOK)

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
// server's own terms once started.
func TestResetSurvivesAClientDisconnect(t *testing.T) {
	var (
		populateRan bool
		populateCtx error
	)
	baseline := func(ctx context.Context, nets *payment.Networks, msh seed.Deployment) error {
		populateRan = true
		populateCtx = ctx.Err()
		existing, err := nets.Stores().Banks(ctx)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return nil
		}
		_, err = admitForPopulate(ctx, nets, msh, "Bank A", "BANKDEFFXXX")
		return err
	}

	srv := newServer(t, baseline)
	reset := cbSurface(srv)

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
	var parts []api.ParticipantDTO
	getJSON(t, cbSurface(srv), "/members", &parts)
	assertEqual(t, "participants after the reset", len(parts), 1)
	assertEqual(t, "participant name", parts[0].Name, "Bank A")
}

// TestConcurrentResetsLeaveExactlyOneDataset pins that resets serialize.
func TestConcurrentResetsLeaveExactlyOneDataset(t *testing.T) {
	baseline := func(ctx context.Context, nets *payment.Networks, msh seed.Deployment) error {
		existing, err := nets.Stores().Banks(ctx)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return nil
		}
		p, err := admitForPopulate(ctx, nets, msh, "Bank A", "BANKDEFFXXX")
		if err != nil {
			return err
		}
		_, err = p.OpenCustomerAccount(ctx, "Baseline", "EUR")
		return err
	}

	h := newServer(t, baseline)
	reset := cbSurface(h)

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

	var parts []api.ParticipantDTO
	getJSON(t, cbSurface(h), "/members", &parts)
	assertEqual(t, "participants after concurrent resets", len(parts), 1)

	var accounts []api.DepositAccountDTO
	getJSON(t, bankSurface(h, parts[0].ID), "/deposit-accounts", &accounts)
	assertEqual(t, "deposit accounts after concurrent resets", len(accounts), 1)
	assertEqual(t, "deposit account name", accounts[0].Name, "Baseline")
}

// ---------------------------------------------------------------------------
// Text validation
// ---------------------------------------------------------------------------

// TestControlCharactersAreRefusedNotStored pins ledger.ValidateText where a client
// can reach it. `{"name":"Ban\u0000k"}` is legal JSON and the store would hold it
// happily, so the 400 comes from the domain.
func TestControlCharactersAreRefusedNotStored(t *testing.T) {
	h := newServer(t, nil)

	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	did := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	doJSON(t, bankSurface(h, pid), "POST", "/deposits", `{"account":"`+did+`","amount":100000,"description":"opening"}`, http.StatusOK)
	gl := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["controlAccount"].(string)

	// nul and esc are JSON escapes — six characters here, one control character once
	// encoding/json is done. That is what a client actually sends, and why no
	// byte-level screening of the request body would catch it.
	const (
		nul = `\u0000`
		esc = `\u001b`
	)

	entries := func(first string) string {
		return `"entries":[{"accountId":"` + first + `","amount":100,"direction":"Debit"},` +
			`{"accountId":"` + gl + `","subsidiary":"` + did + `","amount":100,"direction":"Credit"}]`
	}

	for _, tc := range []struct{ what, method, path, body string }{
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
		target := bankSurface(h, pid)
		if strings.HasPrefix(tc.path, "/members") {
			target = cbSurface(h)
		}
		rec := do(t, target, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got status %d, want 400 (body: %s)", tc.what, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}

	// Nothing was created by any of the refused requests.
	var parts []api.ParticipantDTO
	getJSON(t, cbSurface(h), "/members", &parts)
	assertEqual(t, "participants after the refusals", len(parts), 1)

	var accounts []api.DepositAccountDTO
	getJSON(t, bankSurface(h, pid), "/deposit-accounts", &accounts)
	assertEqual(t, "deposit accounts after the refusals", len(accounts), 1)
}

// TestControlCharactersInAPathAreRefused is the read half of the same rule: a
// URL-encoded NUL is decoded into the path parameter and handed straight to the
// store as a lookup key.
func TestControlCharactersInAPathAreRefused(t *testing.T) {
	h := newServer(t, nil)

	// A well-formed but unknown id is still an honest 404.
	assertStatus(t, bankSurface(h, "NOSUCHBKXXX"), "GET", "/me", "", http.StatusNotFound)

	assertStatus(t, csmSurface(h), "GET", "/participants/bank%001", "", http.StatusBadRequest)
	assertStatus(t, bankSurface(h, "BNKADEFFXXX"), "GET", "/deposit-accounts/dep%001", "", http.StatusBadRequest)
	assertStatus(t, csmSurface(h), "GET", "/payments/audit?entity=pay%001", "", http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// Audit endpoints
// ---------------------------------------------------------------------------

// auditFixture runs one payment end to end, so the log holds every scope: the
// network's payment-scope events, both banks' ledger- and deposit-scope events, and
// the central bank's. It returns the two participant IDs and the payment ID.
func auditFixture(t *testing.T, h *server) (bankA, bankB, payID string) {
	t.Helper()
	a := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	b := provisionMember(t, h, "BNKBDEFFXXX", "Bank B")
	// SCT addresses both legs by IBAN (payment.Scheme.AddressedBy), so both
	// accounts need one before submission will accept them.
	alice := doJSON(t, bankSurface(h, a), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, bankSurface(h, b), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`, http.StatusCreated)["id"].(string)
	fundAndLodge(t, h, a, alice, 100000)

	cyc := doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)["id"].(string)
	pay := doJSON(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+alice+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, alice)+`"}},
		"creditor":{"account":"`+bob+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, b, bob)+`"}},
		"amount":25000,
		"creditorName":"Bob"
	}`, http.StatusAccepted)["id"].(string)
	carry(t, h)
	assertStatus(t, csmSurface(h), "POST", "/cycles/"+cyc+"/close", "", http.StatusOK)
	carry(t, h)
	return a, b, pay
}

func auditTypes(events []api.AuditEventDTO) []string {
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
		return api.AuditFilterFrom(r, payment.ClearingHouseBook, ledger.ScopePayment)
	}

	// The route decides the book and the scope; the client never can.
	base := filterFor("")
	assertEqual(t, "book", base.BookID, payment.ClearingHouseBook)
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

	var events []api.AuditEventDTO
	getJSON(t, csmSurface(h), "/payments/audit?limit=1000", &events)

	// THIS INSTITUTION'S log, and only its own.
	want := []string{
		ledger.EventMemberAdmitted, // Bank A into the roster
		ledger.EventMemberAdmitted, // Bank B
		ledger.EventCycleOpened,
		ledger.EventPaymentInitiated,
		ledger.EventPaymentAccepted,
		ledger.EventPaymentCleared, // one per payment in the cycle
		ledger.EventCycleClosed,
		ledger.EventPaymentSettled, // one per payment in the cycle
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
	var forPayment []api.AuditEventDTO
	getJSON(t, csmSurface(h), "/payments/audit?entity="+payID, &forPayment)
	assertEqual(t, "events for the payment", strings.Join(auditTypes(forPayment), " "),
		strings.Join([]string{
			ledger.EventPaymentInitiated,
			ledger.EventPaymentAccepted,
			ledger.EventPaymentCleared,
			ledger.EventPaymentSettled,
		}, " "))

	// ?type= narrows to one event type.
	var cleared []api.AuditEventDTO
	getJSON(t, csmSurface(h), "/payments/audit?type="+ledger.EventPaymentCleared, &cleared)
	assertEqual(t, "cleared events", len(cleared), 1)
	assertEqual(t, "cleared entity", cleared[0].EntityID, payID)
}

// TestAuditRejectedAndReturnedPayments covers the two lifecycle branches the
// happy path never reaches.
func TestAuditRejectedAndReturnedPayments(t *testing.T) {
	h := newServer(t, nil)
	a, b, payID := auditFixture(t, h)

	// A settled payment can be returned.
	doJSON(t, csmSurface(h), "POST", "/payments/"+payID+"/return", `{"reason":"AC04"}`, http.StatusAccepted)
	carry(t, h)

	// A fresh payment in a fresh cycle can be rejected before it clears.
	var aAccounts, bAccounts []api.DepositAccountDTO
	getJSON(t, bankSurface(h, a), "/deposit-accounts", &aAccounts)
	getJSON(t, bankSurface(h, b), "/deposit-accounts", &bAccounts)
	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	second := doJSON(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+aAccounts[0].ID+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, aAccounts[0].ID)+`"}},
		"creditor":{"account":"`+bAccounts[0].ID+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, b, bAccounts[0].ID)+`"}},
		"amount":1000,
		"creditorName":"Bob"
	}`, http.StatusAccepted)["id"].(string)
	carry(t, h)
	doJSON(t, csmSurface(h), "POST", "/payments/"+second+"/reject", `{"reason":"AM05"}`, http.StatusAccepted)
	carry(t, h)

	var returned []api.AuditEventDTO
	getJSON(t, csmSurface(h), "/payments/audit?type="+ledger.EventPaymentReturned, &returned)
	assertEqual(t, "returned events", len(returned), 1)
	assertEqual(t, "returned entity", returned[0].EntityID, payID)

	var rejected []api.AuditEventDTO
	getJSON(t, csmSurface(h), "/payments/audit?type="+ledger.EventPaymentRejected, &rejected)
	assertEqual(t, "rejected events", len(rejected), 1)
	assertEqual(t, "rejected entity", rejected[0].EntityID, second)
}

// TestRejectPaymentRendersItsCode pins that a rejection's external
// status-reason code — not just its free text — reaches the wire, on the reject
// response and on every later read. handleRejectPayment always attaches MS03,
// the API exposing no way for a caller to name a more specific one.
func TestRejectPaymentRendersItsCode(t *testing.T) {
	h := newServer(t, nil)
	a, b, _ := auditFixture(t, h)

	var aAccounts, bAccounts []api.DepositAccountDTO
	getJSON(t, bankSurface(h, a), "/deposit-accounts", &aAccounts)
	getJSON(t, bankSurface(h, b), "/deposit-accounts", &bAccounts)
	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	payID := doJSON(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+aAccounts[0].ID+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, aAccounts[0].ID)+`"}},
		"creditor":{"account":"`+bAccounts[0].ID+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, b, bAccounts[0].ID)+`"}},
		"amount":1000,
		"creditorName":"Bob"
	}`, http.StatusAccepted)["id"].(string)
	carry(t, h)

	rejected := doJSON(t, csmSurface(h), "POST", "/payments/"+payID+"/reject", `{"reason":"card lost"}`, http.StatusAccepted)
	assertEqual(t, "reject code on the reject response", rejected["rejectCode"].(string), "MS03")
	assertEqual(t, "reject reason on the reject response", rejected["rejectReason"].(string), "card lost")

	reread := doJSON(t, csmSurface(h), "GET", "/payments/"+payID, "", http.StatusOK)
	assertEqual(t, "reject code on a later read", reread["rejectCode"].(string), "MS03")
	assertEqual(t, "reject reason on a later read", reread["rejectReason"].(string), "card lost")
}

// TestRejectPaymentGivesThePayerTheirMoneyBack pins the half of a rejection
// that is not the clearing house's: the payer's bank reverses the leg it
// posted, in its own book, when the pacs.002 gets there.
func TestRejectPaymentGivesThePayerTheirMoneyBack(t *testing.T) {
	h := newServer(t, nil)
	a, b, _ := auditFixture(t, h)

	var aAccounts, bAccounts []api.DepositAccountDTO
	getJSON(t, bankSurface(h, a), "/deposit-accounts", &aAccounts)
	getJSON(t, bankSurface(h, b), "/deposit-accounts", &bAccounts)
	payer := aAccounts[0].ID
	bookOf := func() int64 {
		t.Helper()
		bal := doJSON(t, bankSurface(h, a), "GET", "/deposit-accounts/"+payer+"/balance", "", http.StatusOK)
		return int64(bal["book"].(float64))
	}
	before := bookOf()

	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	payID := doJSON(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+aAccounts[0].ID+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, aAccounts[0].ID)+`"}},
		"creditor":{"account":"`+bAccounts[0].ID+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, b, bAccounts[0].ID)+`"}},
		"amount":1000,
		"creditorName":"Bob"
	}`, http.StatusAccepted)["id"].(string)
	carry(t, h)
	assertEqual(t, "payer's book balance after submission", bookOf(), before-1000)

	doJSON(t, csmSurface(h), "POST", "/payments/"+payID+"/reject", `{"reason":"card lost"}`, http.StatusAccepted)
	carry(t, h)
	assertEqual(t, "payer's book balance after the rejection", bookOf(), before)
}

// TestARejectionWhoseRefundFailsStandsAndIsReported: two institutions cannot
// share a transaction.
func TestARejectionWhoseRefundFailsStandsAndIsReported(t *testing.T) {
	ctx := context.Background()
	h := newServer(t, nil)
	a, b, _ := auditFixture(t, h)

	var aAccounts, bAccounts []api.DepositAccountDTO
	getJSON(t, bankSurface(h, a), "/deposit-accounts", &aAccounts)
	getJSON(t, bankSurface(h, b), "/deposit-accounts", &bAccounts)
	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	payID := doJSON(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+aAccounts[0].ID+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, aAccounts[0].ID)+`"}},
		"creditor":{"account":"`+bAccounts[0].ID+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, b, bAccounts[0].ID)+`"}},
		"amount":1000,
		"creditorName":"Bob"
	}`, http.StatusAccepted)["id"].(string)
	carry(t, h)

	// The PAYER'S BANK's own copy and its own network. The leg this reverses is on
	// that copy — the clearing house's row has no leg columns — and through the
	// clearing house the act is refused as another institution's.
	payer := mustForBank(t, h, payment.ParticipantID(a)).Network()
	p, err := payer.GetPayment(ctx, payment.PaymentID(payID))
	if err != nil {
		t.Fatalf("get payment: %v", err)
	}
	if err := payer.ReverseDebtorLeg(ctx, p, "reversed already"); err != nil {
		t.Fatalf("reverse the leg out from under the payer's bank: %v", err)
	}

	// The clearing house's half is answerable and succeeds, so the operator is
	// told 202 and the payment really is Rejected.
	rejected := doJSON(t, csmSurface(h), "POST", "/payments/"+payID+"/reject", `{"reason":"card lost"}`, http.StatusAccepted)
	assertEqual(t, "status in the rejection's response", rejected["status"].(string), "Rejected")

	// The payer's bank's half is not, and fails. It reaches nobody over HTTP and
	// everybody through the day's report: the pacs.002 was collected, the bank
	// could not act on it, and there is nobody to answer.
	err = joinProblems(workThrough(h.dep))
	if err == nil {
		t.Fatal("the refund failed and the day reported nothing; a swallowed failure is a payer silently left short")
	}
	if !strings.Contains(err.Error(), ledger.ErrTransactionAlreadyReversed.Error()) {
		t.Fatalf("the day's problem is %v, want one naming the already-reversed leg", err)
	}

	// And the rejection stands, because it happened.
	reread := doJSON(t, csmSurface(h), "GET", "/payments/"+payID, "", http.StatusOK)
	assertEqual(t, "status after the failed refund", reread["status"].(string), "Rejected")
	assertEqual(t, "reason after the failed refund", reread["rejectReason"].(string), "card lost")
}

// TestAuditMandateEvents covers the two mandate events, which no payment flow
// emits on its own.
func TestAuditMandateEvents(t *testing.T) {
	h := newServer(t, nil)
	a, b, _ := auditFixture(t, h)

	var aAccounts, bAccounts []api.DepositAccountDTO
	getJSON(t, bankSurface(h, a), "/deposit-accounts", &aAccounts)
	getJSON(t, bankSurface(h, b), "/deposit-accounts", &bAccounts)

	mid := doJSON(t, bankSurface(h, b), "POST", "/mandates", `{
		"debtor":{"account":"`+aAccounts[0].ID+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, aAccounts[0].ID)+`"}},
		"creditor":{"account":"`+bAccounts[0].ID+`"},
		"maxAmount":50000
	}`, http.StatusCreated)["id"].(string)
	doJSON(t, bankSurface(h, b), "POST", "/mandates/"+mid+"/revoke", "", http.StatusOK)

	// In the CREDITOR BANK's own log, because a mandate is that bank's row and
	// both events are its own acts. The clearing house has never been told the
	// mandate exists.
	var events []api.AuditEventDTO
	getJSON(t, bankSurface(h, b), "/payments/audit?entity="+mid, &events)
	assertEqual(t, "mandate events", strings.Join(auditTypes(events), " "),
		strings.Join([]string{ledger.EventMandateCreated, ledger.EventMandateRevoked}, " "))
}

// TestAuditRoutesAreScoped pins that the four endpoints are four filters over
// one log: each returns its own scope and its own book, and nothing else.
func TestAuditRoutesAreScoped(t *testing.T) {
	h := newServer(t, nil)
	a, b, _ := auditFixture(t, h)

	// Four filters over one log, on three different operators: a bank's two are its
	// own, the reserve movements are the central bank's, and the payment log is the
	// clearing house's. That each still answers with exactly one scope is the pin.
	cases := []struct {
		op        http.Handler
		path      string
		wantScope string
	}{
		{bankSurface(h, a), "/audit", string(ledger.ScopeLedger)},
		{bankSurface(h, a), "/deposit-audit", string(ledger.ScopeDeposit)},
		{cbSurface(h), "/audit", string(ledger.ScopeLedger)},
		{csmSurface(h), "/payments/audit", string(ledger.ScopePayment)},
	}
	for _, c := range cases {
		var events []api.AuditEventDTO
		getJSON(t, c.op, c.path+"?limit=1000", &events)
		if len(events) == 0 {
			t.Fatalf("%s returned no events", c.path)
		}
		for _, e := range events {
			assertEqual(t, c.path+" scope", e.Scope, c.wantScope)
		}
	}

	// Scope alone does not identify a route: bank A's log, bank B's and the
	// central bank's are all ledger-scoped, so a route serving the wrong BOOK
	// would still pass the loop above.
	logs := map[string][]api.AuditEventDTO{}
	// Keyed by a name rather than by the path, because "/audit" now names three
	// different logs depending on which operator you ask.
	for _, c := range []struct {
		name string
		op   http.Handler
		path string
	}{
		{"bank A ledger", bankSurface(h, a), "/audit"},
		{"bank A deposit", bankSurface(h, a), "/deposit-audit"},
		{"bank B ledger", bankSurface(h, b), "/audit"},
		{"central bank", cbSurface(h), "/audit"},
		{"payments", csmSurface(h), "/payments/audit"},
	} {
		var events []api.AuditEventDTO
		getJSON(t, c.op, c.path+"?limit=1000", &events)
		if len(events) == 0 {
			t.Fatalf("%s returned no events", c.name)
		}
		logs[c.name] = events
	}
	books := map[string]string{}
	for name, events := range logs {
		for _, e := range events {
			if e.BookID == "" {
				t.Fatalf("%s serves %s with no book on it", name, e.Type)
			}
			if seen, ok := books[name]; ok && seen != e.BookID {
				t.Fatalf("%s serves events from two books, %s and %s", name, seen, e.BookID)
			}
			books[name] = e.BookID
		}
	}
	// Bank A's ledger log and its deposit log are the same book and differ by
	// scope, which the loop above already asserted; every OTHER pair is two
	// institutions and must be two books.
	for lhs, left := range books {
		for rhs, right := range books {
			if lhs == rhs || (lhs == "bank A ledger" && rhs == "bank A deposit") ||
				(lhs == "bank A deposit" && rhs == "bank A ledger") {
				continue
			}
			if left == right {
				t.Fatalf("%s and %s both serve book %s", lhs, rhs, left)
			}
		}
	}

	// A positive identification of the central bank's book, so the disjointness above
	// cannot be satisfied by some other log that merely happens not to overlap: the
	// cycle's settlement transaction is posted there and nowhere else.
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
func TestAuditPaginationByCursor(t *testing.T) {
	h := newServer(t, nil)
	auditFixture(t, h)

	var all []api.AuditEventDTO
	getJSON(t, csmSurface(h), "/payments/audit?limit=1000", &all)
	if len(all) < 5 {
		t.Fatalf("fixture produced %d payment events, want at least 5", len(all))
	}

	// Walk the whole log backwards two at a time and reassemble it.
	var walked []api.AuditEventDTO
	cursor := ""
	for range len(all) {
		var page []api.AuditEventDTO
		getJSON(t, csmSurface(h), "/payments/audit?limit=2"+cursor, &page)
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
	var empty []api.AuditEventDTO
	getJSON(t, csmSurface(h), fmt.Sprintf("/payments/audit?limit=2&before=%d", all[0].Seq), &empty)
	assertEqual(t, "page below the oldest event", len(empty), 0)
}

// TestAuditDefaultLimitApplies pins that a log longer than one page is
// truncated to the newest 100 events rather than returned whole.
func TestAuditDefaultLimitApplies(t *testing.T) {
	h := newServer(t, nil)

	// Each open/close pair is two payment-scope events; 51 pairs clears 100.
	for range 51 {
		cyc := doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)["id"].(string)
		assertStatus(t, csmSurface(h), "POST", "/cycles/"+cyc+"/close", "", http.StatusOK)
	}

	var full []api.AuditEventDTO
	getJSON(t, csmSurface(h), "/payments/audit?limit=1000", &full)
	assertEqual(t, "events in the log", len(full), 102)

	var page []api.AuditEventDTO
	getJSON(t, csmSurface(h), "/payments/audit", &page)
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

	var events []api.AuditEventDTO
	getJSON(t, csmSurface(h), "/payments/audit?limit=99999", &events)
	if len(events) == 0 {
		t.Fatal("no events returned")
	}
	if len(events) > 1000 {
		t.Fatalf("returned %d events, want <= 1000", len(events))
	}
}

// ---------------------------------------------------------------------------
// Lending and overdraft credit
// ---------------------------------------------------------------------------

// openTermLoan opens a EUR term loan at 6% ACT/365, annuity method, over
// termMonths instalments, and returns the decoded facility.
func openTermLoan(t *testing.T, h *server, pid string, termMonths int) map[string]any {
	t.Helper()
	return doJSON(t, bankSurface(h, pid), "POST", "/facilities", fmt.Sprintf(`{
		"kind":"TermLoan","name":"Home Loan","asset":"EUR","commitment":1000000,
		"rate":60000,"dayCount":"ACT/365","method":"Annuity","termMonths":%d
	}`, termMonths), http.StatusCreated)
}

// openRevolvingLine opens a EUR revolving line at 18% ACT/365 with a 10%
// minimum-payment fraction, and returns the decoded facility.
func openRevolvingLine(t *testing.T, h *server, pid string, commitment int64) map[string]any {
	t.Helper()
	return doJSON(t, bankSurface(h, pid), "POST", "/facilities", fmt.Sprintf(`{
		"kind":"RevolvingLine","name":"Credit Line","asset":"EUR","commitment":%d,
		"rate":180000,"dayCount":"ACT/365","minPayment":100000
	}`, commitment), http.StatusCreated)
}

// TestOpenTermLoanAndRevolvingLine pins that one route opens either product,
// dispatched by the request's Kind field, and that a freshly-opened facility
// reports zero drawn and zero accrued without a further round trip.
func TestOpenTermLoanAndRevolvingLine(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")

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

	var facilities []api.FacilityDTO
	getJSON(t, bankSurface(h, pid), "/facilities", &facilities)
	if len(facilities) != 2 {
		t.Fatalf("facilities = %v, want 2", facilities)
	}
}

// TestOpenFacilityRequiresAsset mirrors TestOpenDepositAccountRequiresAsset: a
// facility's asset decides which asset its two GL accounts are denominated in,
// and nothing here may pick one on the caller's behalf.
func TestOpenFacilityRequiresAsset(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")

	assertStatus(t, bankSurface(h, pid), "POST", "/facilities", `{
		"kind":"TermLoan","name":"No Asset","commitment":100000,"rate":50000,
		"dayCount":"ACT/365","method":"Annuity","termMonths":12
	}`, http.StatusBadRequest)
}

// TestOpenFacilityRejectsUnknownEnums pins that Kind, DayCount and Method are
// parsed from their string forms exactly as the ledger handlers parse an
// account type: an unknown value is a 400 before the domain is ever called.
func TestOpenFacilityRejectsUnknownEnums(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")

	assertStatus(t, bankSurface(h, pid), "POST", "/facilities", `{
		"kind":"Nonsense","name":"X","asset":"EUR","commitment":100000,"rate":50000,"dayCount":"ACT/365"
	}`, http.StatusBadRequest)

	assertStatus(t, bankSurface(h, pid), "POST", "/facilities", `{
		"kind":"TermLoan","name":"X","asset":"EUR","commitment":100000,"rate":50000,
		"dayCount":"Nonsense","method":"Annuity","termMonths":12
	}`, http.StatusBadRequest)

	assertStatus(t, bankSurface(h, pid), "POST", "/facilities", `{
		"kind":"TermLoan","name":"X","asset":"EUR","commitment":100000,"rate":50000,
		"dayCount":"ACT/365","method":"Nonsense","termMonths":12
	}`, http.StatusBadRequest)
}

// TestDisburseTermLoanGeneratesSixtyRowSchedule covers disbursement end to end: the
// counterparty's balance moves, the facility becomes Active and reports its drawn
// principal, a 60-month term generates 60 instalments, and a second is refused.
func TestDisburseTermLoanGeneratesSixtyRowSchedule(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	did := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["controlAccount"].(string)

	loan := openTermLoan(t, h, pid, 60)
	fid := loan["id"].(string)

	tx := doJSON(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/disbursement", `{
		"counterparty":"`+gl+`","subsidiary":"`+did+`","firstDue":"2025-02-15","description":"payout"
	}`, http.StatusCreated)
	if tx["id"] == nil || tx["id"].(string) == "" {
		t.Fatalf("disbursement did not return a transaction: %v", tx)
	}

	var schedule []api.InstallmentDTO
	getJSON(t, bankSurface(h, pid), "/facilities/"+fid+"/schedule", &schedule)
	assertEqual(t, "schedule length", len(schedule), 60)

	bal := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did+"/balance", "", http.StatusOK)
	assertEqual(t, "book balance after disbursement", int64(bal["book"].(float64)), int64(1000000))

	got := doJSON(t, bankSurface(h, pid), "GET", "/facilities/"+fid, "", http.StatusOK)
	assertEqual(t, "drawn after disbursement", int64(got["drawn"].(float64)), int64(1000000))
	assertEqual(t, "status after disbursement", got["status"].(string), "Active")

	assertStatus(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/disbursement", `{
		"counterparty":"`+gl+`","subsidiary":"`+did+`","firstDue":"2025-02-15","description":"again"
	}`, http.StatusUnprocessableEntity)
}

// TestDrawPastCommitmentReturns422 covers a revolving line's draw path: a draw
// within the commitment succeeds, a term loan refuses to be drawn at all, and a
// draw exceeding the commitment is refused without moving the drawn balance.
func TestDrawPastCommitmentReturns422(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	did := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["controlAccount"].(string)

	line := openRevolvingLine(t, h, pid, 100000)
	fid := line["id"].(string)

	assertStatus(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/draws", `{
		"counterparty":"`+gl+`","subsidiary":"`+did+`","amount":60000,"description":"draw 1"
	}`, http.StatusCreated)

	loan := openTermLoan(t, h, pid, 12)
	assertStatus(t, bankSurface(h, pid), "POST", "/facilities/"+loan["id"].(string)+"/draws", `{
		"counterparty":"`+gl+`","subsidiary":"`+did+`","amount":1000,"description":"nope"
	}`, http.StatusUnprocessableEntity)

	assertStatus(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/draws", `{
		"counterparty":"`+gl+`","subsidiary":"`+did+`","amount":60000,"description":"draw 2"
	}`, http.StatusUnprocessableEntity)

	got := doJSON(t, bankSurface(h, pid), "GET", "/facilities/"+fid, "", http.StatusOK)
	assertEqual(t, "drawn after the refused draw", int64(got["drawn"].(float64)), int64(60000))
}

// TestRepayFromDepositAccount covers handleRepay, the one handler spanning the
// deposit and lending layers: a repayment debits the customer's account and
// credits the facility's drawn balance in the same request.
func TestRepayFromDepositAccount(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	did := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["controlAccount"].(string)

	loan := openTermLoan(t, h, pid, 12)
	fid := loan["id"].(string)
	doJSON(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/disbursement", `{
		"counterparty":"`+gl+`","subsidiary":"`+did+`","firstDue":"2025-02-15","description":"payout"
	}`, http.StatusCreated)

	tx := doJSON(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/repayments", `{
		"accountId":"`+did+`","amount":100000,"date":"2025-02-01","description":"first payment"
	}`, http.StatusCreated)
	if tx["id"] == nil || tx["id"].(string) == "" {
		t.Fatalf("repayment did not return a transaction: %v", tx)
	}

	bal := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did+"/balance", "", http.StatusOK)
	assertEqual(t, "book balance after repayment", int64(bal["book"].(float64)), int64(900000))

	got := doJSON(t, bankSurface(h, pid), "GET", "/facilities/"+fid, "", http.StatusOK)
	assertEqual(t, "drawn after repayment", int64(got["drawn"].(float64)), int64(900000))
}

// TestRepayExceedingAvailableBalanceReturns422 pins that the funds check runs
// BEFORE the facility is touched: a repayment larger than the customer's available
// balance is refused and nothing moves on either side.
func TestRepayExceedingAvailableBalanceReturns422(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	did := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["controlAccount"].(string)

	loan := openTermLoan(t, h, pid, 12)
	fid := loan["id"].(string)
	doJSON(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/disbursement", `{
		"counterparty":"`+gl+`","subsidiary":"`+did+`","firstDue":"2025-02-15","description":"payout"
	}`, http.StatusCreated)

	// Alice's account holds exactly the disbursed €10,000 and no overdraft —
	// a repayment of €20,000 exceeds her available balance.
	assertStatus(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/repayments", `{
		"accountId":"`+did+`","amount":2000000,"date":"2025-02-01","description":"too much"
	}`, http.StatusUnprocessableEntity)

	bal := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did+"/balance", "", http.StatusOK)
	assertEqual(t, "book balance unchanged", int64(bal["book"].(float64)), int64(1000000))

	got := doJSON(t, bankSurface(h, pid), "GET", "/facilities/"+fid, "", http.StatusOK)
	assertEqual(t, "drawn unchanged", int64(got["drawn"].(float64)), int64(1000000))
}

// TestChargeInterestOnRevolvingLine covers billing a revolving line's cycle:
// interest is capitalized into drawn principal and one instalment (the
// minimum payment) is appended to the schedule.
func TestChargeInterestOnRevolvingLine(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	did := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["controlAccount"].(string)

	line := openRevolvingLine(t, h, pid, 500000)
	fid := line["id"].(string)
	doJSON(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/draws", `{
		"counterparty":"`+gl+`","subsidiary":"`+did+`","amount":200000,"description":"draw"
	}`, http.StatusCreated)

	// Accrue over a month, then bill the cycle. The response carries BOTH
	// halves — the capitalization posting and the instalment billed — because
	// a cycle can bill without posting; see ChargeDTO.
	assertStatus(t, bankSurface(h, pid), "POST", "/end-of-day", `{"date":"2025-02-15"}`, http.StatusNoContent)
	charged := doJSON(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/interest-charge", `{"date":"2025-02-15"}`, http.StatusOK)
	posting, ok := charged["transaction"].(map[string]any)
	if !ok || posting["id"] == "" {
		t.Fatalf("charge response = %v, want a posted transaction", charged)
	}
	billed, ok := charged["installment"].(map[string]any)
	if !ok || int(billed["seq"].(float64)) != 1 {
		t.Fatalf("charge response = %v, want the first cycle billed", charged)
	}

	got := doJSON(t, bankSurface(h, pid), "GET", "/facilities/"+fid, "", http.StatusOK)
	if drawn := int64(got["drawn"].(float64)); drawn <= 200000 {
		t.Fatalf("drawn after charging = %d, want > 200000 (interest capitalized into principal)", drawn)
	}

	var schedule []api.InstallmentDTO
	getJSON(t, bankSurface(h, pid), "/facilities/"+fid+"/schedule", &schedule)
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
	assertStatus(t, bankSurface(h, pid), "POST", "/facilities/"+loan["id"].(string)+"/interest-charge", `{"date":"2025-02-15"}`, http.StatusUnprocessableEntity)
}

// TestChargeInterestBillsACycleWithNothingToPost: a line drawn and charged
// before any accrual has ticked posts nothing and still bills a cycle.
func TestChargeInterestBillsACycleWithNothingToPost(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	did := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["controlAccount"].(string)

	line := openRevolvingLine(t, h, pid, 500000)
	fid := line["id"].(string)
	doJSON(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/draws", `{
		"counterparty":"`+gl+`","subsidiary":"`+did+`","amount":200000,"description":"draw"
	}`, http.StatusCreated)

	// No end-of-day has run, so nothing has accrued.
	charged := doJSON(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/interest-charge", `{"date":"2025-01-15"}`, http.StatusOK)
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

	var schedule []api.InstallmentDTO
	getJSON(t, bankSurface(h, pid), "/facilities/"+fid+"/schedule", &schedule)
	if len(schedule) != 1 {
		t.Fatalf("schedule = %v, want the one billed cycle", schedule)
	}

	// An undrawn line does neither, and THAT is 204 — the empty outcome the
	// rest of this API already expresses that way.
	empty := openRevolvingLine(t, h, pid, 500000)
	assertStatus(t, bankSurface(h, pid), "POST", "/facilities/"+empty["id"].(string)+"/interest-charge", `{"date":"2025-01-15"}`, http.StatusNoContent)
}

// TestChargeOverdraftInterestEndpoint covers the monthly capitalization a customer
// actually sees, on the deposit side. Nothing accrued is 204 — nothing posts and,
// unlike a revolving line's cycle, nothing is billed either.
func TestChargeOverdraftInterestEndpoint(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	did := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["controlAccount"].(string)

	// Nothing has accrued yet — an account that has never been overdrawn.
	assertStatus(t, bankSurface(h, pid), "POST", "/deposit-accounts/"+did+"/interest-charge", `{"date":"2025-01-31"}`, http.StatusNoContent)

	doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-limit", `{
		"limit":100000
	}`, http.StatusOK)
	doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-pricing", `{
		"pricing":{"rate":120000,"unarrangedRate":0,"dayCount":"ACT/365"}
	}`, http.StatusOK)

	glLedger := doJSON(t, bankSurface(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	sub := doJSON(t, bankSurface(h, pid), "POST", "/ledgers/"+glLedger+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	equity := doJSON(t, bankSurface(h, pid), "POST", "/subledgers/"+sub+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	// Draw €500 into the overdraft with a direct GL posting, the mechanism
	// deposit.overdraftAccrual's own doc describes.
	doJSON(t, bankSurface(h, pid), "POST", "/transactions", `{
		"entries":[
			{"accountId":"`+gl+`","subsidiary":"`+did+`","amount":50000,"direction":"Debit"},
			{"accountId":"`+equity+`","amount":50000,"direction":"Credit"}
		]
	}`, http.StatusCreated)

	// The accrual window opens when the ACCOUNT is opened, not on the first
	// end-of-day, so both runs below accrue.
	assertStatus(t, bankSurface(h, pid), "POST", "/end-of-day", `{"date":"2025-01-16"}`, http.StatusNoContent)
	assertStatus(t, bankSurface(h, pid), "POST", "/end-of-day", `{"date":"2025-01-17"}`, http.StatusNoContent)

	acct := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)
	assertEqual(t, "accrued before charging", int64(acct["accruedInterest"].(float64)), int64(33))

	tx := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts/"+did+"/interest-charge", `{"date":"2025-01-17"}`, http.StatusOK)
	if tx["id"] == nil || tx["id"].(string) == "" {
		t.Fatalf("charge did not return a transaction: %v", tx)
	}
	entries, ok := tx["entries"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("charge posted %v, want two entries", tx["entries"])
	}

	// Capitalized: the customer now owes the interest as part of the balance,
	// and the receivable is back to nothing.
	bal := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did+"/balance", "", http.StatusOK)
	assertEqual(t, "book balance after charging", int64(bal["book"].(float64)), int64(-50033))

	after := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)
	assertEqual(t, "receivable after charging", int64(after["accruedInterest"].(float64)), int64(0))

	// And charging again, with the receivable cleared, is the empty outcome.
	assertStatus(t, bankSurface(h, pid), "POST", "/deposit-accounts/"+did+"/interest-charge", `{"date":"2025-01-17"}`, http.StatusNoContent)
}

// TestOverdraftTermsTimelineEndpoint covers the account's whole effective-dated
// timeline, oldest first, including the opening row every account gets at
// OpenAccount.
func TestOverdraftTermsTimelineEndpoint(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	prd := prdOf(t, h, pid)
	did := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","overdraftLimit":50000,"productId":"`+prd+`"}`, http.StatusCreated)["id"].(string)

	// The opening row alone: FLOATING, because an account is opened onto its
	// product's price rather than a copy of it. Its three rate fields are zero because
	// the row holds no price, which is what floating means and is not "interest-free".
	first := doJSONArray(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did+"/overdraft-terms", "", http.StatusOK)
	assertEqual(t, "opening timeline length", len(first), 1)
	opening := first[0].(map[string]any)
	assertEqual(t, "opening limit", int64(opening["overdraftLimit"].(float64)), int64(50_000))
	assertEqual(t, "the opening row floats", opening["floating"].(bool), true)
	assertEqual(t, "opening product", opening["productId"].(string), prd)

	// A change effective before the account existed is refused: each row is a
	// complete statement of the account's terms carried forward from the row in
	// force, and there is no row in force before the opening one.
	assertStatus(t, bankSurface(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-limit", `{"limit":50000,"effectiveFrom":"2025-01-01T00:00:00Z"}`, http.StatusInternalServerError)

	// A row priced today and a FUTURE-dated one. The test clock is 2025-01-15, the day
	// the account was opened, so the first shares the opening row's day key and
	// REPLACES it — one row per (account, day) — rather than appending.
	doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-pricing", `{
		"pricing":{"rate":150000,"unarrangedRate":350000,"dayCount":"ACT/365"},
		"effectiveFrom":"2025-01-15T00:00:00Z"
	}`, http.StatusOK)
	doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-pricing", `{
		"pricing":{"rate":180000,"unarrangedRate":350000,"dayCount":"ACT/365"},
		"effectiveFrom":"2099-01-01T00:00:00Z"
	}`, http.StatusOK)

	rows := doJSONArray(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did+"/overdraft-terms", "", http.StatusOK)
	assertEqual(t, "timeline length", len(rows), 2)

	today := rows[0].(map[string]any)
	assertEqual(t, "accountId is on the wire", today["accountId"].(string), did)
	assertEqual(t, "today's effectiveFrom", today["effectiveFrom"].(string), "2025-01-15T00:00:00Z")
	assertEqual(t, "today's rate", int64(today["rate"].(float64)), int64(150_000))
	assertEqual(t, "an overlaid row does not float", today["floating"].(bool), false)
	assertEqual(t, "the limit was carried forward", int64(today["overdraftLimit"].(float64)), int64(50_000))

	// createdAt is the OTHER half of the two-dates pair, and the clock is frozen, so
	// it is fixedTime regardless of effectiveFrom. The future row is where the gap is
	// largest, and is what would catch the two being swapped in the DTO mapping.
	last := rows[len(rows)-1].(map[string]any)
	assertEqual(t, "the future row is on the timeline", int64(last["rate"].(float64)), int64(180_000))
	assertEqual(t, "rate scale is on the wire", int64(last["rateScale"].(float64)), int64(interest.RateScale))
	assertEqual(t, "future effectiveFrom", last["effectiveFrom"].(string), "2099-01-01T00:00:00Z")
	assertEqual(t, "future dayCount", last["dayCount"].(string), "ACT/365")
	assertEqual(t, "future unarrangedRate", int64(last["unarrangedRate"].(float64)), int64(350_000))
	assertEqual(t, "future createdAt", last["createdAt"].(string), "2025-01-15T12:00:00Z")

	// …but the account itself still reports the rate in force TODAY, and says
	// where it came from: this one is negotiated, not the product's list price.
	acct := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)
	assertEqual(t, "resolved as of today", int64(acct["overdraftRate"].(float64)), int64(150_000))
	assertEqual(t, "and its source", acct["pricingSource"].(string), "negotiated")
	assertEqual(t, "and its product", acct["productId"].(string), prd)
}

// TestEndOfDayAccruesBothFacilityAndOverdraftInterest is the HTTP half of
// payment.Bank.RunEndOfDay: one POST /end-of-day drives both credit batches.
func TestEndOfDayAccruesBothFacilityAndOverdraftInterest(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	did := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	gl := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["controlAccount"].(string)

	doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-limit", `{
		"limit":100000
	}`, http.StatusOK)
	doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-pricing", `{
		"pricing":{"rate":120000,"unarrangedRate":0,"dayCount":"ACT/365"}
	}`, http.StatusOK)

	glLedger := doJSON(t, bankSurface(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	sub := doJSON(t, bankSurface(h, pid), "POST", "/ledgers/"+glLedger+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	equity := doJSON(t, bankSurface(h, pid), "POST", "/subledgers/"+sub+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	// Draw Alice's account into an overdraft via a direct GL posting — the
	// same mechanism deposit.overdraftAccrual's own doc describes, since
	// CheckWithdrawal is not the only way an account ends up overdrawn.
	doJSON(t, bankSurface(h, pid), "POST", "/transactions", `{
		"entries":[
			{"accountId":"`+gl+`","subsidiary":"`+did+`","amount":50000,"direction":"Debit"},
			{"accountId":"`+equity+`","amount":50000,"direction":"Credit"}
		]
	}`, http.StatusCreated)

	loan := openTermLoan(t, h, pid, 12)
	fid := loan["id"].(string)
	doJSON(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/disbursement", `{
		"counterparty":"`+equity+`","firstDue":"2025-02-15","description":"payout"
	}`, http.StatusCreated)

	assertStatus(t, bankSurface(h, pid), "POST", "/end-of-day", `{"date":"2025-01-16"}`, http.StatusNoContent)
	assertStatus(t, bankSurface(h, pid), "POST", "/end-of-day", `{"date":"2025-01-17"}`, http.StatusNoContent)

	acct := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)
	if got := int64(acct["accruedInterest"].(float64)); got <= 0 {
		t.Fatalf("deposit account accrued interest = %d, want > 0", got)
	}

	got := doJSON(t, bankSurface(h, pid), "GET", "/facilities/"+fid, "", http.StatusOK)
	if accrued := int64(got["accruedInterest"].(float64)); accrued <= 0 {
		t.Fatalf("facility accrued interest = %d, want > 0", accrued)
	}
}

// TestTotalsReportsDerivedOverdraft covers GET /totals: one bank with one account
// in credit and one drawn into an overdraft reports both figures, split by sign and
// keyed by asset — no journal posts this number anywhere.
func TestTotalsReportsDerivedOverdraft(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")

	alice := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","overdraftLimit":100000,"productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	aliceGL := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+alice, "", http.StatusOK)["controlAccount"].(string)

	doJSON(t, bankSurface(h, pid), "POST", "/deposits", `{"account":"`+bob+`","amount":100000,"description":"opening"}`, http.StatusOK)

	glLedger := doJSON(t, bankSurface(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	sub := doJSON(t, bankSurface(h, pid), "POST", "/ledgers/"+glLedger+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	equity := doJSON(t, bankSurface(h, pid), "POST", "/subledgers/"+sub+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	doJSON(t, bankSurface(h, pid), "POST", "/transactions", `{
		"entries":[
			{"accountId":"`+aliceGL+`","subsidiary":"`+alice+`","amount":30000,"direction":"Debit"},
			{"accountId":"`+equity+`","amount":30000,"direction":"Credit"}
		]
	}`, http.StatusCreated)

	var totals []api.TotalsDTO
	getJSON(t, bankSurface(h, pid), "/totals", &totals)
	if len(totals) != 1 {
		t.Fatalf("totals = %v, want exactly one EUR row", totals)
	}
	assertEqual(t, "asset", totals[0].Asset, "EUR")
	assertEqual(t, "deposits", totals[0].Deposits, int64(100000))
	assertEqual(t, "overdrafts", totals[0].Overdrafts, int64(30000))
}

// TestUnknownFacilityReturns404 covers the read and mutating routes alike. The
// schedule route is deliberately excluded: ListInstallments on an unknown facility
// is an empty listing by contract, not an error.
func TestUnknownFacilityReturns404(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	// A real, funded deposit account, so the repayments case below clears the
	// available-funds check and 404s on the FACILITY lookup, not on the
	// account or on insufficient funds.
	did := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	doJSON(t, bankSurface(h, pid), "POST", "/deposits", `{"account":"`+did+`","amount":100000,"description":"opening"}`, http.StatusOK)

	assertStatus(t, bankSurface(h, pid), "GET", "/facilities/nope", "", http.StatusNotFound)
	assertStatus(t, bankSurface(h, pid), "POST", "/facilities/nope/draws", `{"counterparty":"x","amount":100,"description":"x"}`, http.StatusNotFound)
	assertStatus(t, bankSurface(h, pid), "POST", "/facilities/nope/repayments", `{"accountId":"`+did+`","amount":100,"date":"2025-02-01","description":"x"}`, http.StatusNotFound)
	assertStatus(t, bankSurface(h, pid), "DELETE", "/facilities/nope", "", http.StatusNotFound)

	var schedule []api.InstallmentDTO
	getJSON(t, bankSurface(h, pid), "/facilities/nope/schedule", &schedule)
	assertEqual(t, "schedule of an unknown facility", len(schedule), 0)
}

// TestEntryDTOCarriesItsOwnValueDate pins the per-leg value date in both
// directions, the one place the ledger's central claim about value dating was
// invisible from outside Go.
func TestEntryDTOCarriesItsOwnValueDate(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	gl := doJSON(t, bankSurface(h, pid), "POST", "/ledgers", `{"name":"GL"}`, http.StatusCreated)["id"].(string)
	slid := doJSON(t, bankSurface(h, pid), "POST", "/ledgers/"+gl+"/subledgers", `{"name":"Sub"}`, http.StatusCreated)["id"].(string)
	acct := doJSON(t, bankSurface(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Cash","type":"Asset","asset":"EUR"}`, http.StatusCreated)["id"].(string)
	other := doJSON(t, bankSurface(h, pid), "POST", "/subledgers/"+slid+"/accounts", `{"name":"Equity","type":"Equity","asset":"EUR"}`, http.StatusCreated)["id"].(string)

	today := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	settles := today.AddDate(0, 0, 3)
	tx := doJSON(t, bankSurface(h, pid), "POST", "/transactions", fmt.Sprintf(`{
		"entries":[
			{"accountId":%q,"amount":100,"direction":"Debit"},
			{"accountId":%q,"amount":100,"direction":"Credit","valueDate":%q}
		],
		"valueDate":%q
	}`, acct, other, settles.Format(time.RFC3339), today.Format(time.RFC3339)), http.StatusCreated)

	// Read it back through the listing rather than trusting the create
	// response: the point is that a client polling for postings can see the
	// split, and the listing is the route that renders many at once.
	var listed []api.TransactionDTO
	getJSON(t, bankSurface(h, pid), "/transactions", &listed)
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
// actually depends on it: initiating an SCT value-dates the payer's leg to the
// debit and the suspense leg to settlement.
func TestSEPADebtorLegsValueDateApart(t *testing.T) {
	h := newServer(t, nil)
	a := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	b := provisionMember(t, h, "BNKBDEFFXXX", "Bank B")
	// SCT addresses both legs by IBAN (payment.Scheme.AddressedBy), so both
	// accounts need one before submission will accept them.
	alice := doJSON(t, bankSurface(h, a), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`"}`, http.StatusCreated)
	bob := doJSON(t, bankSurface(h, b), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`, http.StatusCreated)["id"].(string)
	doJSON(t, bankSurface(h, a), "POST", "/deposits", `{"account":"`+alice["id"].(string)+`","amount":100000,"description":"opening"}`, http.StatusOK)

	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	doJSON(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+alice["id"].(string)+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, alice["id"].(string))+`"}},
		"creditor":{"account":"`+bob+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, b, bob)+`"}},
		"amount":25000,
		"endToEndId":"e2e-1",
		"creditorName":"Bob"
	}`, http.StatusAccepted)
	carry(t, h)

	var listed []api.TransactionDTO
	getJSON(t, bankSurface(h, a), "/transactions", &listed)

	// Find the debtor posting: the one that debits Alice's GL account. The
	// opening deposit touches it too, so match on having a leg that is NOT
	// value-dated with the rest.
	payerGL := alice["controlAccount"].(string)
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
		// The transaction-level date is the SUSPENSE leg's — the payment's own value
		// date, settlement — and the payer's leg is pulled back to the debit date.
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
// borrower interest, through the HTTP API only.
func overpaidFacilityViaAPI(t *testing.T, h *server) (pid, fid, did string, owed int64) {
	t.Helper()
	pid = provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	acct := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)
	did = acct["id"].(string)
	aliceGL := acct["controlAccount"].(string)
	doJSON(t, bankSurface(h, pid), "POST", "/deposits", `{"account":"`+did+`","amount":100000,"description":"opening"}`, http.StatusOK)

	loan := doJSON(t, bankSurface(h, pid), "POST", "/facilities", `{
		"kind":"TermLoan","name":"Alice Home Loan","asset":"EUR",
		"commitment":1000000,"rate":60000,"dayCount":"ACT/365",
		"method":"Annuity","termMonths":60
	}`, http.StatusCreated)
	fid = loan["id"].(string)
	doJSON(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/disbursement", `{"counterparty":"`+aliceGL+`","subsidiary":"`+did+`","firstDue":"2025-02-15","description":"advance"}`, http.StatusCreated)

	// Ten days of interest, then the borrower pays exactly that in cash — so the
	// receivable is empty and the money has left the record.
	drawdown := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 10; i++ {
		assertStatus(t, bankSurface(h, pid), "POST", "/end-of-day", `{"date":"`+drawdown.AddDate(0, 0, i).Format("2006-01-02")+`"}`, http.StatusNoContent)
	}
	var facility api.FacilityDTO
	getJSON(t, bankSurface(h, pid), "/facilities/"+fid, &facility)
	if facility.AccruedInterest <= 0 {
		t.Fatalf("accrued interest after ten days = %d, want positive", facility.AccruedInterest)
	}
	settled := facility.AccruedInterest
	doJSON(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/repayments", fmt.Sprintf(
		`{"accountId":%q,"amount":%d,"date":"2025-01-25","description":"Interest"}`, did, settled), http.StatusCreated)

	// A posting backdated to the drawdown, clearing the principal: the loan was
	// repaid on day one and no interest was ever owed. Nothing is left to absorb
	// the correction, so the whole of it becomes a refund the bank owes.
	doJSON(t, bankSurface(h, pid), "POST", "/transactions", fmt.Sprintf(`{
		"description":"backdated settlement",
		"valueDate":%q,
		"entries":[
			{"accountId":%q,"subsidiary":%q,"amount":1000000,"direction":"Credit"},
			{"accountId":%q,"subsidiary":%q,"amount":1000000,"direction":"Debit"}
		]
	}`, drawdown.Format(time.RFC3339), facility.PrincipalAccount, fid, aliceGL, did), http.StatusCreated)
	assertStatus(t, bankSurface(h, pid), "POST", "/end-of-day", `{"date":"`+drawdown.AddDate(0, 0, 11).Format("2006-01-02")+`"}`, http.StatusNoContent)

	getJSON(t, bankSurface(h, pid), "/facilities/"+fid, &facility)
	if facility.RefundPayable <= 0 {
		t.Fatalf("refundPayable = %d after the correction, want positive; the fixture needs a debt to discharge",
			facility.RefundPayable)
	}
	if facility.RefundAccount == "" {
		t.Error("refundAccount is empty on a facility with an outstanding refund")
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
	aliceGL := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)["controlAccount"].(string)

	var list []api.RefundPayableDTO
	getJSON(t, bankSurface(h, pid), "/interest-refunds-payable", &list)
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
	assertStatus(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/interest-refunds", fmt.Sprintf(
		`{"counterparty":%q,"subsidiary":%q,"amount":%d,"date":"2025-02-01"}`, aliceGL, did, owed+1), http.StatusBadRequest)
	var facility api.FacilityDTO
	getJSON(t, bankSurface(h, pid), "/facilities/"+fid, &facility)
	if facility.RefundPayable != owed {
		t.Fatalf("refundPayable = %d after a refused refund, want %d untouched", facility.RefundPayable, owed)
	}

	// Pay it, and the obligation is gone from both the facility and the listing.
	tx := doJSON(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/interest-refunds", fmt.Sprintf(
		`{"counterparty":%q,"subsidiary":%q,"amount":%d,"date":"2025-02-01","description":"overcharged interest"}`, aliceGL, did, owed), http.StatusCreated)
	if len(tx["entries"].([]any)) != 2 {
		t.Errorf("refund posting has %d entries, want 2", len(tx["entries"].([]any)))
	}
	getJSON(t, bankSurface(h, pid), "/facilities/"+fid, &facility)
	if facility.RefundPayable != 0 {
		t.Errorf("refundPayable = %d after paying it, want 0", facility.RefundPayable)
	}
	getJSON(t, bankSurface(h, pid), "/interest-refunds-payable", &list)
	if len(list) != 0 {
		t.Errorf("listed %+v after settling, want nothing owed", list)
	}

	// And a second attempt is 422: the same status as its mirror, a repayment
	// against a facility that owes nothing.
	assertStatus(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/interest-refunds", fmt.Sprintf(
		`{"counterparty":%q,"amount":1,"date":"2025-02-01"}`, aliceGL), http.StatusUnprocessableEntity)
}

// TestInterestRefundsPayableIsEmptyForAnOrdinaryBank pins the common case: the
// listing is an empty array, not an error and not null, and an ordinary facility
// reports no refund — while still naming the line one would sit in.
func TestInterestRefundsPayableIsEmptyForAnOrdinaryBank(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	loan := doJSON(t, bankSurface(h, pid), "POST", "/facilities", `{
		"kind":"TermLoan","name":"Bob Loan","asset":"EUR",
		"commitment":500000,"rate":60000,"dayCount":"ACT/365",
		"method":"Annuity","termMonths":24
	}`, http.StatusCreated)

	if got := loan["refundPayable"]; got != float64(0) {
		t.Errorf("refundPayable on a fresh facility = %v, want 0", got)
	}
	if got, _ := loan["refundAccount"].(string); got == "" {
		t.Error("refundAccount is empty; the line exists from the first facility in the asset")
	}

	res := do(t, bankSurface(h, pid), "GET", "/interest-refunds-payable", "")
	if got := strings.TrimSpace(res.Body.String()); got != "[]" {
		t.Errorf("listing = %s, want []", got)
	}

	// And refunding one is 422, not a 500 and not a silent no-op.
	assertStatus(t, bankSurface(h, pid), "POST", "/facilities/"+loan["id"].(string)+"/interest-refunds", `{"counterparty":"x","amount":100,"date":"2025-02-01"}`, http.StatusUnprocessableEntity)
}

// TestFundingRespectsAccountStatus pins the status matrix from the receiving
// side, over HTTP.
func TestFundingRespectsAccountStatus(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")

	open := func(name string) string {
		return doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"`+name+`","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	}
	fund := func(did string, amount int) *httptest.ResponseRecorder {
		return do(t, bankSurface(h, pid), "POST", "/deposits", fmt.Sprintf(`{"account":%q,"amount":%d}`, did, amount))
	}

	// Active, dormant and frozen all take a credit.
	active := open("Active")
	assertEqual(t, "credit into an active account", fund(active, 500).Code, http.StatusOK)

	dormant := open("Dormant")
	doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts/"+dormant+"/status", `{"action":"markDormant"}`, http.StatusOK)
	assertEqual(t, "credit into a dormant account", fund(dormant, 500).Code, http.StatusOK)

	frozen := open("Frozen")
	doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts/"+frozen+"/status", `{"action":"freeze"}`, http.StatusOK)
	assertEqual(t, "credit into a frozen account", fund(frozen, 500).Code, http.StatusOK)

	// A closed account does not, and the refusal names the status.
	closed := open("Closed")
	assertStatus(t, bankSurface(h, pid), "DELETE", "/deposit-accounts/"+closed, "", http.StatusNoContent)
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
	getJSON(t, bankSurface(h, pid), "/deposit-accounts/"+closed+"/balance", &bal)
	if bal.Book != 0 {
		t.Errorf("closed account book balance = %d, want 0", bal.Book)
	}
}

// TestDormantDebitNamesDormancy is the error-text half, over HTTP: a repayment
// out of a dormant account is refused as dormant rather than as an "invalid
// account status transition", and it is a 422 like its frozen sibling.
func TestDormantDebitNamesDormancy(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	did := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	doJSON(t, bankSurface(h, pid), "POST", "/deposits", `{"account":"`+did+`","amount":100000}`, http.StatusOK)
	fid := doJSON(t, bankSurface(h, pid), "POST", "/facilities", `{
		"kind":"TermLoan","name":"L","asset":"EUR","commitment":1000,
		"rate":60000,"dayCount":"ACT/365","method":"Annuity","termMonths":12
	}`, http.StatusCreated)["id"].(string)
	doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts/"+did+"/status", `{"action":"markDormant"}`, http.StatusOK)

	res := do(t, bankSurface(h, pid), "POST", "/facilities/"+fid+"/repayments", `{"accountId":"`+did+`","amount":100,"date":"2025-02-01"}`)
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("repayment from a dormant account = %d, want 422: %s", res.Code, res.Body)
	}
	if !strings.Contains(res.Body.String(), "dormant") {
		t.Errorf("refusal body = %s, want it to name dormancy", res.Body)
	}
}

// The catalogue over the wire, end to end: create, draft, publish, open an
// account from it, and see the resolved rate on the account.
func TestProductCatalogueRoutes(t *testing.T) {
	h := newServer(t, nil)
	pid := provisionMember(t, h, "BNKADEFFXXX", "Bank A")

	created := doJSON(t, bankSurface(h, pid), "POST", "/products", `{"name":"Basic Current Account","kind":"CurrentAccount"}`, http.StatusCreated)
	prd := created["id"].(string)
	if prd == "" {
		t.Fatal("no product id")
	}
	assertEqual(t, "kind on the wire", created["kind"].(string), "CurrentAccount")
	assertEqual(t, "a new product is on sale", created["retired"].(bool), false)

	// The bank's own default product is in the listing beside it: onboarding
	// creates one, because a bank with no product cannot open an account.
	list := doJSONArray(t, bankSurface(h, pid), "GET", "/products", "", http.StatusOK)
	assertEqual(t, "catalogue length", len(list), 2)

	// A draft prices nothing, so an account cannot be opened from it yet.
	drafted := doJSON(t, bankSurface(h, pid), "POST", "/products/"+prd+"/versions", `{
		"effectiveFrom":"2025-01-15T00:00:00Z","rate":120000,"dayCount":"ACT/365"
	}`, http.StatusCreated)
	assertEqual(t, "a draft is not published", drafted["published"].(bool), false)
	assertEqual(t, "and carries no hash", drafted["hash"].(string), "")
	assertStatus(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Bruno","asset":"EUR","productId":"`+prd+`"}`, http.StatusNotFound)

	published := doJSON(t, bankSurface(h, pid), "POST", "/products/"+prd+"/versions/2025-01-15/publish", "", http.StatusOK)
	assertEqual(t, "published", published["published"].(bool), true)
	if published["hash"].(string) == "" {
		t.Error("publishing stamped no hash")
	}

	// Republishing is refused, and the status says "state, not syntax".
	assertStatus(t, bankSurface(h, pid), "POST", "/products/"+prd+"/versions/2025-01-15/publish", "", http.StatusUnprocessableEntity)

	acct := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Bruno","asset":"EUR","productId":"`+prd+`","overdraftLimit":50000}`, http.StatusCreated)
	did := acct["id"].(string)
	assertEqual(t, "the account names its product", acct["productId"].(string), prd)
	assertEqual(t, "and is priced by it", int64(acct["overdraftRate"].(float64)), int64(120_000))
	assertEqual(t, "from the product", acct["pricingSource"].(string), "product")

	// A negotiated rate shows as such, which is the distinction a customer
	// service screen needs.
	negotiated := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts/"+did+"/overdraft-pricing", `{
		"effectiveFrom":"2025-01-15T00:00:00Z",
		"pricing":{"rate":90000,"unarrangedRate":0,"dayCount":"ACT/365"}
	}`, http.StatusOK)
	assertEqual(t, "the negotiated rate", int64(negotiated["overdraftRate"].(float64)), int64(90_000))
	assertEqual(t, "and its source", negotiated["pricingSource"].(string), "negotiated")

	// Retroactive publication is refused over the wire too. Drafting it is
	// allowed — only publication is forward-only.
	doJSON(t, bankSurface(h, pid), "POST", "/products/"+prd+"/versions", `{
		"effectiveFrom":"2020-01-01T00:00:00Z","rate":1,"dayCount":"ACT/365"
	}`, http.StatusCreated)
	assertStatus(t, bankSurface(h, pid), "POST", "/products/"+prd+"/versions/2020-01-01/publish", "", http.StatusUnprocessableEntity)

	// The timeline lists drafts alongside published rows: an operator view has
	// to be able to see what is queued.
	versions := doJSONArray(t, bankSurface(h, pid), "GET", "/products/"+prd+"/versions", "", http.StatusOK)
	assertEqual(t, "timeline length", len(versions), 2)
	assertEqual(t, "oldest first", versions[0].(map[string]any)["effectiveFrom"].(string), "2020-01-01T00:00:00Z")

	// Retiring takes it off sale without unpricing the account already on it.
	retired := doJSON(t, bankSurface(h, pid), "POST", "/products/"+prd+"/retire", "", http.StatusOK)
	assertEqual(t, "retired", retired["retired"].(bool), true)
	assertStatus(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Bella","asset":"EUR","productId":"`+prd+`"}`, http.StatusUnprocessableEntity)
	still := doJSON(t, bankSurface(h, pid), "GET", "/deposit-accounts/"+did, "", http.StatusOK)
	assertEqual(t, "the account on it still resolves", int64(still["overdraftRate"].(float64)), int64(90_000))

	// An unknown kind is a bad field value, not a silent CurrentAccount.
	assertStatus(t, bankSurface(h, pid), "POST", "/products", `{"name":"Odd","kind":"SavingsAccount"}`, http.StatusBadRequest)
}

// ---------------------------------------------------------------------------
// Account addressing: the directory and per-account identifier endpoints
// ---------------------------------------------------------------------------

// someAccount opens one participant with one deposit account that already carries
// an IBAN, and returns both ids. The other tests in this section build on it rather
// than opening their own bare account.
func someAccount(t *testing.T, h *server) (pid, did string) {
	t.Helper()
	pid = provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	did = doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
	return pid, did
}

// anotherAccountAtSameBank opens a second deposit account at pid, with no
// identifier of its own — the fixture for proving AddIdentifier's uniqueness
// check is per-bank rather than per-account.
func anotherAccountAtSameBank(t *testing.T, h *server, pid string) string {
	t.Helper()
	return doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts", `{"name":"Bruno","asset":"EUR","productId":"`+prdOf(t, h, pid)+`"}`, http.StatusCreated)["id"].(string)
}

// TestDirectoryResolvesItsOwnCustomer pins GET /directory's happy path on the
// surface it survives on: a BANK's, resolving an address in its own register.
func TestDirectoryResolvesItsOwnCustomer(t *testing.T) {
	var pid payment.ParticipantID
	var aliceAddress string
	srv := newServer(t, func(ctx context.Context, nets *payment.Networks, msh seed.Deployment) error {
		p, err := admitForPopulate(ctx, nets, msh, "Aurora Bank", "BANKDEFFXXX")
		if err != nil {
			return err
		}
		pid = p.ID
		acct, err := p.Deposit.OpenAccount(ctx, "Alice", "EUR", p.ProductID, 0)
		aliceAddress = acct.Identifiers[0].Value
		return err
	})

	// The response names the bank as an AGENT and not as a participant: a resolved
	// party is an account plus the address of the bank holding it. See
	// api.directoryEntryDTO.
	var got struct {
		Agent   string `json:"agent"`
		Account string `json:"account"`
	}
	getJSON(t, bankSurface(srv, string(pid)), "/directory/accounts?scheme=IBAN&value="+aliceAddress, &got)
	if got.Agent != string(pid) || got.Account == "" {
		t.Fatalf("directory response = %#v, want this bank's own account", got)
	}
}

// TestDirectoryUnknownIBANIs404 pins that an address this bank does not hold is a
// missing resource, not an empty success. "This bank does not hold" and not "nobody
// holds": no route in this system answers the second question.
func TestDirectoryUnknownIBANIs404(t *testing.T) {
	srv := newServer(t, nil)
	pid, _ := someAccount(t, srv)
	// A WELL-FORMED address this bank does not hold. That is the 404 — the
	// question was askable and the answer is no.
	doJSON(t, bankSurface(srv, pid), "GET", "/directory/accounts?scheme=IBAN&value="+mustMintDE(9_999_999), "", http.StatusNotFound)
	// Something that is not an address at all is a different answer: 422, because
	// the check digits refuse it before any register is asked.
	doJSON(t, bankSurface(srv, pid), "GET", "/directory/accounts?scheme=IBAN&value=NOBODY-0001", "", http.StatusUnprocessableEntity)
}

// TestDirectoryDoesNotAnswerForAnotherBanksCustomer is the narrowing itself, on the
// wire: a real address, really held, at a bank that is not the one being asked. A
// sweep would answer 200 with the other bank's participant, account, name and asset.
func TestDirectoryDoesNotAnswerForAnotherBanksCustomer(t *testing.T) {
	var asker payment.ParticipantID
	var aliceAddress string
	srv := newServer(t, func(ctx context.Context, nets *payment.Networks, msh seed.Deployment) error {
		holder, err := admitForPopulate(ctx, nets, msh, "Aurora Bank", "AURODEFFXXX")
		if err != nil {
			return err
		}
		alice, err := holder.Deposit.OpenAccount(ctx, "Alice", "EUR", holder.ProductID, 0)
		if err != nil {
			return err
		}
		aliceAddress = alice.Identifiers[0].Value
		other, err := admitForPopulate(ctx, nets, msh, "Banca Verde", "VERDITMMXXX")
		if err != nil {
			return err
		}
		asker = other.ID
		return nil
	})

	doJSON(t, bankSurface(srv, string(asker)), "GET", "/directory/accounts?scheme=IBAN&value="+aliceAddress, "",
		http.StatusNotFound)
}

// TestDirectoryMissingParamsIs400 pins that scheme and value are both
// required: this is a malformed request, not a lookup that could ever
// succeed.
func TestDirectoryMissingParamsIs400(t *testing.T) {
	srv := newServer(t, nil)
	pid, _ := someAccount(t, srv)
	doJSON(t, bankSurface(srv, pid), "GET", "/directory/accounts?scheme=IBAN", "", http.StatusBadRequest)
	doJSON(t, bankSurface(srv, pid), "GET", "/directory/accounts?value=X", "", http.StatusBadRequest)
}

// TestAddAndRemoveIdentifierEndpoints covers the per-account identifier lifecycle
// over HTTP: adding one makes the account resolvable, a second account at the same
// bank cannot take an address already in use, and removing it forgets it again.
func TestAddAndRemoveIdentifierEndpoints(t *testing.T) {
	srv := newServer(t, nil)
	pid, did := someAccount(t, srv)

	base := "/deposit-accounts/"
	doJSON(t, bankSurface(srv, pid), "POST", base+did+"/identifiers",
		`{"scheme":"PAN","value":"4000000000000001"}`, http.StatusNoContent)

	// Now resolvable, by its own bank.
	doJSON(t, bankSurface(srv, pid), "GET", "/directory/accounts?scheme=PAN&value=4000000000000001", "", http.StatusOK)

	// A second account at the same bank cannot take it.
	other := anotherAccountAtSameBank(t, srv, pid)
	doJSON(t, bankSurface(srv, pid), "POST", base+other+"/identifiers",
		`{"scheme":"PAN","value":"4000000000000001"}`, http.StatusConflict)

	doJSON(t, bankSurface(srv, pid), "DELETE", base+did+"/identifiers/PAN/4000000000000001", "", http.StatusNoContent)
	doJSON(t, bankSurface(srv, pid), "GET", "/directory/accounts?scheme=PAN&value=4000000000000001", "", http.StatusNotFound)
}

// TestDirectoryAmbiguousIdentifierIs409 pins deposit.ErrIdentifierAmbiguous's
// status code.
func TestDirectoryAmbiguousIdentifierIs409(t *testing.T) {
	srv := newServer(t, nil)
	pid, did := someAccount(t, srv)
	other := anotherAccountAtSameBank(t, srv, pid)

	// A card, not an address: an IBAN cannot reach this state through the API at
	// all now, because a caller cannot supply one. The refusal being tested is
	// the LOOKUP's, and it is the same refusal whatever scheme collided.
	shared := deposit.Identifier{Scheme: deposit.IdentifierScheme("PAN"), Value: "4000000000000009"}
	doJSON(t, bankSurface(srv, pid), "POST", "/deposit-accounts/"+did+"/identifiers",
		`{"scheme":"PAN","value":"4000000000000009"}`, http.StatusNoContent)

	// The bank's own network, because the row and the register are both its own
	// and the clearing house has no table for either.
	p, err := mustForBank(t, srv, payment.ParticipantID(pid)).
		Network().GetBank(context.Background(), payment.ParticipantID(pid))
	if err != nil {
		t.Fatalf("reading the bank: %v", err)
	}
	if err := p.Deposit.Store().Update(context.Background(), func(ctx context.Context, tx deposit.Tx) error {
		a, err := tx.GetDepositAccount(ctx, p.BookID, deposit.AccountID(other))
		if err != nil {
			return err
		}
		a.Identifiers = append(a.Identifiers, shared)
		return tx.PutDepositAccount(ctx, p.BookID, a)
	}); err != nil {
		t.Fatalf("planting the duplicate: %v", err)
	}

	doJSON(t, bankSurface(srv, pid), "GET", "/directory/accounts?scheme=PAN&value=4000000000000009", "", http.StatusConflict)
}

// TestTheRosterPublishesTheAllocationAndAMemberCopiesIt pins the two surfaces
// the subscription is made of, and that they carry the same fact.
func TestTheRosterPublishesTheAllocationAndAMemberCopiesIt(t *testing.T) {
	h := newServer(t, nil)
	a := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	b := provisionMember(t, h, "BNKBDEFFXXX", "Bank B")

	var published []struct {
		BIC      string `json:"bic"`
		Country  string `json:"country"`
		BankCode string `json:"bankCode"`
	}
	getJSON(t, csmSurface(h), "/roster", &published)
	if len(published) != 2 {
		t.Fatalf("the roster publishes %d members, want 2", len(published))
	}
	byBIC := map[string]string{}
	for _, e := range published {
		if e.Country == "" || e.BankCode == "" {
			t.Fatalf("%s is published with no allocation; a member copying this row has nothing to route on", e.BIC)
		}
		byBIC[e.BIC] = e.Country + " " + e.BankCode
	}
	if byBIC[a] == byBIC[b] {
		t.Fatalf("both members are published under %s; one address would be ambiguous for the whole scheme", byBIC[a])
	}

	// Bank A's own copy, whole. It holds every member the roster published,
	// including itself: a directory is the scheme's, not a list of counterparties.
	var copied []map[string]any
	getJSON(t, bankSurface(h, a), "/directory/banks", &copied)
	if len(copied) != len(published) {
		t.Fatalf("the copy holds %d entries and the roster publishes %d", len(copied), len(published))
	}
	for _, e := range copied {
		if e["refreshedAt"] == nil || e["refreshedAt"] == "" {
			t.Errorf("a copied entry carries no refresh instant: %v", e)
		}
		if _, leaked := e["name"]; leaked {
			t.Errorf("a copied entry carries a name: %v — the roster was never told one", e)
		}
	}

	// And narrowed, which is the question a send form asks: this allocation, which
	// bank. The answer is a BIC and it is Bank B's.
	country, code, _ := strings.Cut(byBIC[b], " ")
	var one map[string]any
	getJSON(t, bankSurface(h, a), "/directory/banks?country="+country+"&bankCode="+code, &one)
	if one["bic"] != b {
		t.Fatalf("%s %s resolves to %v in Bank A's copy, want %s", country, code, one["bic"], b)
	}

	// An allocation this scheme has given nobody is 422 and not 404, and the
	// message is the one a subscriber can honestly give: this copy holds no entry.
	// It cannot say whether there is no such member or whether it is behind.
	miss := do(t, bankSurface(h, a), "GET", "/directory/banks?country=DE&bankCode=10000000", "")
	if miss.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an unallocated code = %d, want 422 (%s)", miss.Code, miss.Body.String())
	}
	if !strings.Contains(miss.Body.String(), "routing directory holds no entry") {
		t.Errorf("the refusal does not name the copy: %s", miss.Body.String())
	}

	// Both parameters or neither: a narrowing on the country alone would answer
	// every German member, which is not a question this route has.
	doJSON(t, bankSurface(h, a), "GET", "/directory/banks?country=DE", "", http.StatusBadRequest)

	// And the narrowing a send form actually has, which is an ADDRESS. It must answer
	// what the pair above answered, because pulling the allocation out of an IBAN is
	// the country table's job and a browser holds no copy of that table.
	bAcct := doJSON(t, bankSurface(h, b), "POST", "/deposit-accounts",
		`{"name":"Bella","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`, http.StatusCreated)["id"].(string)
	address := ibanFor(t, h, b, bAcct)
	var byAddress map[string]any
	getJSON(t, bankSurface(h, a), "/directory/banks?iban="+address, &byAddress)
	if byAddress["bic"] != b {
		t.Fatalf("%s routes to %v in Bank A's copy, want %s", address, byAddress["bic"], b)
	}
	if byAddress["bankCode"] != one["bankCode"] {
		t.Errorf("the address narrowing reads code %v and the pair narrowing %v; one address, one allocation",
			byAddress["bankCode"], one["bankCode"])
	}

	// A grouped address off a statement is the same address: Parse is the front
	// door and it normalises before it validates.
	var grouped map[string]any
	getJSON(t, bankSurface(h, a), "/directory/banks?iban="+url.QueryEscape(groupsOfFour(address)), &grouped)
	if grouped["bic"] != b {
		t.Errorf("the grouped spelling routes to %v, want %s — a person types fours", grouped["bic"], b)
	}

	// A mistyped address is 422 and not 404: the parameter is present and
	// well-typed, and what is wrong with it is a rule. It is the same refusal
	// submission gives, which is the point of resolving through this route at all.
	typo := do(t, bankSurface(h, a), "GET", "/directory/banks?iban="+mistype(address), "")
	if typo.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a mistyped address = %d, want 422 (%s)", typo.Code, typo.Body.String())
	}

	// Two narrowings at once is the caller asking two questions, and there is no
	// answer that is both.
	doJSON(t, bankSurface(h, a), "GET", "/directory/banks?iban="+address+"&country=DE&bankCode="+code,
		"", http.StatusBadRequest)
}

// groupsOfFour is what a statement prints and a person copies.
func groupsOfFour(compact string) string {
	var b strings.Builder
	for i, r := range compact {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// mistype swaps the last two characters, which is the error mod-97 exists to
// catch and the one a payer actually makes.
func mistype(address string) string {
	n := len(address)
	return address[:n-2] + string(address[n-1]) + string(address[n-2])
}

// TestPaymentAddressingRefusalsAre422 pins the status codes of the three ways
// initiation can refuse a leg on addressing grounds, AND that a refusal takes
// no money.
func TestPaymentAddressingRefusalsAre422(t *testing.T) {
	h := newServer(t, nil)

	a := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	b := provisionMember(t, h, "BNKBDEFFXXX", "Bank B")
	alice := doJSON(t, bankSurface(h, a), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, bankSurface(h, b), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`, http.StatusCreated)["id"].(string)
	// A creditor with no address at all: an SCT cannot reach it.
	nobody := doJSON(t, bankSurface(h, b), "POST", "/deposit-accounts", `{"name":"Nobody","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`, http.StatusCreated)["id"].(string)

	fundAndLodge(t, h, a, alice, 100000)
	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	// assertAliceUntouched is the money half.
	assertAliceUntouched := func(what string) {
		t.Helper()
		bal := doJSON(t, bankSurface(h, a), "GET", "/deposit-accounts/"+alice+"/balance", "", http.StatusOK)
		if got := int64(bal["book"].(float64)); got != 100000 {
			t.Fatalf("%s: Alice's book balance is %d, want 100000 — a refused request took her money", what, got)
		}
	}

	// ErrUnaddressableAccount. creditorName is supplied and valid, so the
	// counterparty guard clears and the refusal reached is this case's.
	assertStatus(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+alice+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, alice)+`"}},
		"creditor":{"account":"`+nobody+`"},
		"amount":1000,
		"creditorName":"Nobody"
	}`, http.StatusUnprocessableEntity)
	assertAliceUntouched("after a payee with no address at all")

	// ErrIdentifierMismatch — the creditor's own address, on the debtor leg.
	assertStatus(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+alice+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, alice)+`"}},
		"creditor":{"account":"`+bob+`"},
		"amount":1000,
		"creditorName":"Bob"
	}`, http.StatusUnprocessableEntity)
	assertAliceUntouched("after the payee's address quoted on the payer's leg")

	// A push that quotes no payee address is refused, and the transport is what
	// made that so: the payer's bank has to put an IBAN in the pacs.008 it is
	// about to send, and an address in another bank's register is exactly what it
	// has no way to look up.
	assertStatus(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+alice+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, alice)+`"}},
		"creditor":{"account":"`+bob+`"},
		"amount":1000,
		"creditorName":"Bob"
	}`, http.StatusUnprocessableEntity)
	assertAliceUntouched("after a push that quoted no payee address")

	// The same instruction on the PAYER's own bank, which is the door a retail
	// client uses and the one that matters most: this is the request a phone app
	// sends, and the one a retrying app would send again.
	assertStatus(t, bankSurface(h, a), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+alice+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, alice)+`"}},
		"creditor":{"account":"`+bob+`"},
		"amount":1000,
		"creditorName":"Bob"
	}`, http.StatusUnprocessableEntity)
	assertAliceUntouched("after the same refusal on the payer's own bank surface")

	// The happy case: the payee's address quoted, the payer's left to the
	// back-fill.
	pay := doJSON(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+alice+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, alice)+`"}},
		"creditor":{"account":"`+bob+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, b, bob)+`"}},
		"amount":1000,
		"creditorName":"Bob"
	}`, http.StatusAccepted)
	assertEqual(t, "back-filled debtor address",
		pay["debtor"].(map[string]any)["identifier"].(map[string]any)["value"].(string), ibanFor(t, h, a, alice))
	// A drain first, because the clearing house's own copy is written when the
	// instruction reaches it and the 202 is answered before that.
	carry(t, h)
	// On the PAYER'S BANK's copy, the row the request was persisted into. It reads the
	// same on the clearing house's: a quoted address is canonicalised where the
	// request arrives, so what is stored and what the message carries are one string.
	carried := doJSON(t, bankSurface(h, a), "GET", "/payments/"+pay["id"].(string), "", http.StatusOK)
	assertEqual(t, "creditor address persisted from the request",
		carried["creditor"].(map[string]any)["identifier"].(map[string]any)["value"].(string), ibanFor(t, h, b, bob))

	// ErrAmbiguousAddress: give Alice a second IBAN and quote neither of hers.
	// PLANTED THROUGH THE STORE, because no call would do it — a bank issues one
	// address per account and AddIdentifier refuses the scheme outright.
	plantSecondAddress(t, h, a, alice)
	// On the PAYER'S BANK's port, because the payer's leg has to quote NO address for
	// the ambiguity to be reachable, and the clearing house's console reads the
	// submitting bank out of the payer's address. A bank's port takes it from the port.
	assertStatus(t, bankSurface(h, a), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+alice+`"},
		"creditor":{"account":"`+bob+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, b, bob)+`"}},
		"amount":1000,
		"creditorName":"Bob"
	}`, http.StatusUnprocessableEntity)

	// The aggregate, and the only thing covering the ErrAmbiguousAddress case
	// above. Six requests have gone out; the one that went through is the one that
	// moved money, and it is the only payment row in the network.
	carry(t, h)
	bal := doJSON(t, bankSurface(h, a), "GET", "/deposit-accounts/"+alice+"/balance", "", http.StatusOK)
	if got := int64(bal["book"].(float64)); got != 99000 {
		t.Fatalf("Alice's book balance is %d, want 99000 — exactly one of these six requests may take her money", got)
	}
	all := doJSONArray(t, csmSurface(h), "GET", "/payments", "", http.StatusOK)
	if len(all) != 1 {
		t.Fatalf("the network holds %d payments, want 1: a refused instruction leaves no row", len(all))
	}
}

// TestPostPaymentRefusesEachWayAnAddressFails is the dedicated pin for the
// three refusals a payer can meet on the other side of an instruction, over
// HTTP.
func TestPostPaymentRefusesEachWayAnAddressFails(t *testing.T) {
	h := newServer(t, nil)
	a := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	b := provisionMember(t, h, "BNKBDEFFXXX", "Bank B")
	alice := doJSON(t, bankSurface(h, a), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, bankSurface(h, b), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`, http.StatusCreated)["id"].(string)
	fundAndLodge(t, h, a, alice, 100000)
	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	// A well-formed German address under a bank code no member has been allocated,
	// so Bank A's copy of the directory answers nothing for it.
	unroutable := mustMint(iban.DE, "10000000", 1)

	creditor := func(ident string) string {
		return `"creditor":{"account":"` + bob + `","identifier":` + ident + `},`
	}
	asIBAN := func(v string) string { return `{"scheme":"IBAN","value":"` + v + `"}` }

	for _, tc := range []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"no name", `{"scheme":"sepa.ct","debtor":{"account":"` + alice + `"},` +
			creditor(asIBAN(ibanFor(t, h, b, bob))) + `"amount":1000}`, http.StatusUnprocessableEntity},
		{"a bank code this copy has no entry for", `{"scheme":"sepa.ct","debtor":{"account":"` + alice + `"},` +
			creditor(asIBAN(unroutable)) + `"amount":1000,"creditorName":"Bob"}`, http.StatusUnprocessableEntity},
		{"an address no directory here covers", `{"scheme":"sepa.ct","debtor":{"account":"` + alice + `"},` +
			creditor(`{"scheme":"PAN","value":"4000000000000001"}`) + `"amount":1000,"creditorName":"Bob"}`,
			http.StatusUnprocessableEntity},
		{"an address that resolves — the control", `{"scheme":"sepa.ct","debtor":{"account":"` + alice + `"},` +
			creditor(asIBAN(ibanFor(t, h, b, bob))) + `"amount":1000,"creditorName":"Bob"}`, http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The PAYER'S BANK's port: an instruction names no bank at all now, so
			// this is the only door that knows which bank is submitting without
			// reading the payer's address — and it is the door a customer uses.
			assertStatus(t, bankSurface(h, a), "POST", "/payments", tc.body, tc.wantStatus)
		})
	}
}

// TestAQuotedAddressIsCanonicalisedOnTheWayIn: an address a PERSON typed goes
// through, and reaches the wire canonical.
func TestAQuotedAddressIsCanonicalisedOnTheWayIn(t *testing.T) {
	h := newServer(t, nil)
	a := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	b := provisionMember(t, h, "BNKBDEFFXXX", "Bank B")
	alice := doJSON(t, bankSurface(h, a), "POST", "/deposit-accounts", `{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`"}`, http.StatusCreated)["id"].(string)
	bob := doJSON(t, bankSurface(h, b), "POST", "/deposit-accounts", `{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`, http.StatusCreated)["id"].(string)
	fundAndLodge(t, h, a, alice, 100000)
	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	stored := ibanFor(t, h, b, bob)
	grouped := iban.IBAN(stored).Grouped()

	for _, tc := range []struct {
		name       string
		quoted     string
		wantStatus int
	}{
		{"canonical, as a register stores it", stored, http.StatusAccepted},
		{"grouped, as a statement prints it", grouped, http.StatusAccepted},
		{"lower-cased, as a keyboard produces it", strings.ToLower(stored), http.StatusAccepted},
		{"hyphenated, as a form field invites", strings.ReplaceAll(grouped, " ", "-"), http.StatusAccepted},
		// Not an address at all: 00 is a remainder mod-97-10 never produces. Nothing
		// canonicalises this, and the refusal is the domain's own rather than a
		// complaint about a value the caller did not send.
		{"check digits no IBAN can carry", "DE00" + stored[4:], http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"scheme":"sepa.ct",
				"debtor":{"account":"` + alice + `"},
				"creditor":{"account":"` + bob + `","identifier":{"scheme":"IBAN","value":"` + tc.quoted + `"}},
				"amount":100,"creditorName":"Bob"}`
			// On the PAYER'S BANK's port, where a person's typing actually arrives: the
			// quoted address is the only one on this instruction, and the clearing
			// house's console would have to read a submitting bank out of the payer's leg.
			got := doJSON(t, bankSurface(h, a), "POST", "/payments", body, tc.wantStatus)
			if tc.wantStatus != http.StatusAccepted {
				return
			}
			// Read back off the PAYER's bank, which is the copy the request was
			// persisted into and the one the outbound message was built from.
			carry(t, h)
			// A bank's port answers an identifier and nothing else — the outcome is
			// a second request. See SubmittedPaymentDTO.
			carried := doJSON(t, bankSurface(h, a), "GET", "/payments/"+got["paymentId"].(string), "", http.StatusOK)
			assertEqual(t, "the creditor address persisted from a "+tc.name+" request",
				carried["creditor"].(map[string]any)["identifier"].(map[string]any)["value"].(string), stored)
		})
	}
}

// TestDepositAccountDTOCarriesIdentifiers pins that DepositAccountDTO renders
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
	getJSON(t, bankSurface(srv, pid), "/deposit-accounts/"+did, &got)
	if len(got.Identifiers) == 0 {
		t.Fatal("api.DepositAccountDTO carried no identifiers")
	}
}

// plantSecondAddress writes a second IBAN onto an account, past the register.
// Nothing in the domain does this: a bank issues one address per account.
func plantSecondAddress(t *testing.T, h *server, bic, did string) {
	t.Helper()
	ctx := context.Background()
	net := mustForBank(t, h, payment.ParticipantID(bic))
	p, err := net.Network().GetBank(ctx, payment.ParticipantID(bic))
	if err != nil {
		t.Fatalf("reading the bank: %v", err)
	}
	second, err := iban.New(iban.DE, iban.BankCode(p.Issuer.BankCode), 999_999)
	if err != nil {
		t.Fatalf("minting a second address: %v", err)
	}
	if err := p.Deposit.Store().Update(ctx, func(ctx context.Context, tx deposit.Tx) error {
		a, err := tx.GetDepositAccount(ctx, p.BookID, deposit.AccountID(did))
		if err != nil {
			return err
		}
		a.Identifiers = append(a.Identifiers,
			deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: string(second)})
		return tx.PutDepositAccount(ctx, p.BookID, a)
	}); err != nil {
		t.Fatalf("planting a second address: %v", err)
	}
}

// mustMintDE is a German address at the fixture allocation, for the cases that
// need a well-formed one nobody holds.
func mustMintDE(serial uint64) string {
	a, err := iban.New(iban.DE, "90000825", serial)
	if err != nil {
		panic(err)
	}
	return string(a)
}

// ibanFor is the address a bank minted for one of its accounts, read back off
// the account.
func ibanFor(t *testing.T, s *server, bic, did string) string {
	t.Helper()
	var acct api.DepositAccountDTO
	getJSON(t, bankSurface(s, bic), "/deposit-accounts/"+did, &acct)
	for _, i := range acct.Identifiers {
		if i.Scheme == string(deposit.IdentifierIBAN) {
			return i.Value
		}
	}
	t.Fatalf("account %s at %s holds no IBAN", did, bic)
	return ""
}
