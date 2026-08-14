package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/payment/recon"
)

// What this file is for: a scenario returning nil having produced no file is the
// defect the arrangement exists to prevent, and only a test can tell. Each case
// asserts what was PRODUCED, then holds all N+2 databases against each other.

// baseState is a deployment holding what a fresh boot holds and nothing else.
// Every scenario runs against exactly this.
func baseState(t *testing.T) *server {
	t.Helper()
	return newBaseHarness(t)
}

// runScenario triggers one through the operator's own door and hands back what
// it moved, failing the test if the deployment refused it.
func runScenario(t *testing.T, srv *server, id string) api.ScenarioReportDTO {
	t.Helper()
	rec := post(t, srv.CentralBankRoutes(), "/scenarios/"+id)
	if rec.Code != http.StatusOK {
		t.Fatalf("running %q = %d (body: %s)", id, rec.Code, rec.Body)
	}
	var out api.ScenarioReportDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %q's report: %v", id, err)
	}
	return out
}

// Every scenario is listed, and the listing is what the picker is built from.
func TestEveryScenarioIsListedWithSomethingToShow(t *testing.T) {
	srv := baseState(t)

	var listed []api.ScenarioDTO
	getJSON(t, srv.CentralBankRoutes(), "/scenarios", &listed)
	if len(listed) != len(scenarios) {
		t.Fatalf("the surface lists %d scenarios and the deployment holds %d", len(listed), len(scenarios))
	}
	for _, s := range listed {
		if s.ID == "" || s.Name == "" || s.Description == "" {
			t.Errorf("scenario %+v is missing something a picker needs to render it", s)
		}
	}
}

func TestAScenarioNobodyWroteIsRefused(t *testing.T) {
	srv := baseState(t)
	if rec := post(t, srv.CentralBankRoutes(), "/scenarios/no-such-thing"); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown scenario = %d, want 400 (body: %s)", rec.Code, rec.Body)
	}
}

// The guard against a scenario drifting back onto payment/flow, which produces a
// payment that reads as settled and appears in no message log and on no graph.
// It is asserted for every scenario, including the ones whose subject is not a
// payment: a business day moves files whatever else it does.
func TestEveryScenarioMovesAFile(t *testing.T) {
	for _, s := range scenarios {
		t.Run(s.ID, func(t *testing.T) {
			srv := baseState(t)
			report := runScenario(t, srv, s.ID)

			if len(report.Files) == 0 {
				t.Fatalf("%q moved no file at all; nothing it did crossed a wire, which is what building on payment/flow looks like", s.ID)
			}
			for _, p := range report.Problems {
				t.Errorf("%s could not process %s: %s", p.Institution, p.OrderID, p.Detail)
			}
		})
	}
}

// The instrument no institution may run for itself, over every scenario: if one
// left two institutions' books disagreeing, this is what says so.
func TestEveryScenarioLeavesTheDeploymentReconciled(t *testing.T) {
	for _, s := range scenarios {
		t.Run(s.ID, func(t *testing.T) {
			srv := baseState(t)
			runScenario(t, srv, s.ID)
			recon.Check(t, srv.nets)
		})
	}
}

// A scenario advances the shared clock, and the report says so. It is the cost
// the picker warns about, so it has to be true.
func TestAScenarioSaysWhereItLeftTheClock(t *testing.T) {
	srv := baseState(t)
	report := runScenario(t, srv, scenarioPayment.ID)

	if report.Ran.Date == report.Next.Date {
		t.Errorf("the payment scenario ran and left on %s; it carries a payment to settlement, which takes a day",
			report.Next.Date)
	}
	var clock api.BusinessDateDTO
	getJSON(t, srv.CentralBankRoutes(), "/clock", &clock)
	if clock.Date != report.Next.Date {
		t.Errorf("the deployment stands on %s and the report says it left on %s", clock.Date, report.Next.Date)
	}
}

// ---------------------------------------------------------------------------
// What each one produced
// ---------------------------------------------------------------------------

// The whole chain, asserted as the DOCUMENTS that carried it rather than as the
// status it ended on: a status can be written by anything.
func TestThePaymentScenarioCarriesTheWholeChain(t *testing.T) {
	srv := baseState(t)
	runScenario(t, srv, scenarioPayment.ID)

	p := onlyPayment(t, srv)
	if p.Status != payment.Settled.String() {
		t.Fatalf("the scenario's payment is %q, want Settled", p.Status)
	}
	// The instruction up, the acceptance back, the settlement instruction, the
	// statement, and the share released to the payee's bank.
	for _, want := range []string{
		"pacs.008.001.08", // the payer's bank's file, at its cut-off
		"pacs.002.001.10", // the clearing house's answer to it
		"pacs.009.001.08", // the cut-off, instructed to the settlement agent
		"camt.053.001.08", // the statement each member is advised with
	} {
		if !crossedCarrying(t, srv, want) {
			t.Errorf("no %s crossed a wire; the payment reached Settled without the file that does it", want)
		}
	}
	// And the payee really has the money, which is what the released share is
	// for: a settlement nobody was told about credits nobody.
	if credited := creditedTo(t, srv, p); credited == 0 {
		t.Error("the payee's account was never credited; the cycle settled and the output file reached nobody")
	}
}

// The rejection's test asserts a real reason code reached the payer's bank, not
// that the status is Rejected.
func TestTheRejectionScenarioSendsBackARealReasonCode(t *testing.T) {
	srv := baseState(t)
	runScenario(t, srv, scenarioRejected.ID)

	p := onlyPayment(t, srv)
	if p.Status != payment.Rejected.String() {
		t.Fatalf("the scenario's payment is %q, want Rejected", p.Status)
	}
	// TM01: the window was shut. The clearing house decided it out of its own
	// rows — nothing in the scenario names a code — and it is the ONE fact a
	// reader of this screen wants.
	if p.RejectCode != string(iso20022.StatusReasonInvalidCutOffTime) {
		t.Errorf("the payment carries reason %q, want %s — the refusal was not the cut-off's",
			p.RejectCode, iso20022.StatusReasonInvalidCutOffTime)
	}
	if !crossedCarrying(t, srv, "pacs.002.001.10") {
		t.Error("no pacs.002 crossed a wire; the payer's bank was never told why")
	}
	// And the money is back: a rejection nobody acted on is a status and nothing
	// more.
	if held := suspenseAcross(t, srv); held != 0 {
		t.Errorf("EUR %d is still in a clearing suspense after the rejection; the payer was not refunded", held)
	}
}

// A return is the second hop, and the assertion is the pacs.004 rather than the
// status: the money going back is a different act from the payment coming in.
func TestTheReturnScenarioSendsThePaymentBack(t *testing.T) {
	srv := baseState(t)
	runScenario(t, srv, scenarioReturned.ID)

	p := onlyPayment(t, srv)
	if p.Status != payment.Returned.String() {
		t.Fatalf("the scenario's payment is %q, want Returned", p.Status)
	}
	if !crossedCarrying(t, srv, "pacs.004.001.09") {
		t.Error("no pacs.004 crossed a wire; the payment reads as Returned and nobody sent it back")
	}
	// Both customers end where they started, which no status says.
	if credited := creditedTo(t, srv, p); credited != 0 {
		t.Errorf("the payee still holds %d of a returned payment", credited)
	}
	if held := suspenseAcross(t, srv); held != 0 {
		t.Errorf("EUR %d is still in a clearing suspense after the return", held)
	}
}

// Arrears are computed by an end of day, so the assertion is that days really
// ran rather than that a field was written.
func TestTheArrearsScenarioLeavesABorrowerBehind(t *testing.T) {
	srv := baseState(t)
	runScenario(t, srv, scenarioArrears.ID)

	ctx := context.Background()
	bank, err := srv.nets.Bank(ctx, payment.ParticipantID(verdeBIC))
	if err != nil {
		t.Fatalf("opening %s's store: %v", verdeBIC, err)
	}
	p, err := bank.GetBank(ctx, payment.ParticipantID(verdeBIC))
	if err != nil {
		t.Fatalf("%s's own row: %v", verdeBIC, err)
	}
	facilities, err := p.Lending.ListFacilities(ctx)
	if err != nil {
		t.Fatalf("listing %s's facilities: %v", verdeBIC, err)
	}
	if len(facilities) != 1 {
		t.Fatalf("%s holds %d facilities, want the one the scenario opened", verdeBIC, len(facilities))
	}
	f := facilities[0]
	if f.Arrears.DaysPastDue < 30 {
		t.Errorf("the loan is %d days past due, want at least 30 — the days between were not run",
			f.Arrears.DaysPastDue)
	}
	if f.Status != lending.Active {
		t.Errorf("the loan is %v, want Active — it was never disbursed", f.Status)
	}
}

// ---------------------------------------------------------------------------
// The reads these assertions are made of
// ---------------------------------------------------------------------------

// onlyPayment is the one payment a single-payment scenario made. Asserting there
// is exactly one is half the point: a scenario that made three would make every
// assertion below ambiguous.
func onlyPayment(t *testing.T, srv *server) api.PaymentDTO {
	t.Helper()
	var payments []api.PaymentDTO
	getJSON(t, csmSurface(srv), "/payments", &payments)
	if len(payments) != 1 {
		t.Fatalf("the clearing house holds %d payments, want the one the scenario made", len(payments))
	}
	return payments[0]
}

// crossedCarrying reports whether a file of one message definition really left
// one institution for another. It is read off the MESH — both ends' own halves,
// paired — which is the only place a crossing exists.
func crossedCarrying(t *testing.T, srv *server, msgDef string) bool {
	t.Helper()
	flow, err := srv.dep.NetworkFlow(context.Background(), 0)
	if err != nil {
		t.Fatalf("reading the mesh: %v", err)
	}
	for _, c := range flow.Crossings {
		if c.MsgDefIdr == msgDef && c.ReceivedAt != nil {
			return true
		}
	}
	return false
}

// creditedTo is what the payee's account holds, read through their own bank.
func creditedTo(t *testing.T, srv *server, p api.PaymentDTO) ledger.Amount {
	t.Helper()
	ctx := context.Background()

	agent := iso20022.BIC(p.CreditorAgent)
	net, err := srv.nets.Bank(ctx, payment.ParticipantID(agent))
	if err != nil {
		t.Fatalf("opening %s's store: %v", agent, err)
	}
	bank, err := net.GetBank(ctx, payment.ParticipantID(agent))
	if err != nil {
		t.Fatalf("%s's own row: %v", agent, err)
	}
	// Resolved by ADDRESS: the clearing house's copy carries the IBAN the
	// instruction quoted and no account id, an internal id being the payee bank's
	// own and nobody else's.
	if p.Creditor.Identifier == nil {
		t.Fatalf("payment %s quotes no creditor address", p.ID)
	}
	ref, err := net.ResolveIdentifier(ctx, deposit.Identifier{
		Scheme: deposit.IdentifierScheme(p.Creditor.Identifier.Scheme),
		Value:  p.Creditor.Identifier.Value,
	})
	if err != nil {
		t.Fatalf("%s resolving %s: %v", agent, p.Creditor.Identifier.Value, err)
	}
	bal, err := bank.Deposit.GetBalance(ctx, ref.Account)
	if err != nil {
		t.Fatalf("the payee's balance: %v", err)
	}
	return bal.Book
}

// suspenseAcross is every bank's euro clearing suspense added up: money that has
// left a customer and has not settled between banks. It is zero when nothing is
// in flight, and it is what says a refund really happened.
func suspenseAcross(t *testing.T, srv *server) ledger.Amount {
	t.Helper()
	ctx := context.Background()

	bics, err := srv.nets.Stores().Banks(ctx)
	if err != nil {
		t.Fatalf("listing the deployment's banks: %v", err)
	}
	var total ledger.Amount
	for _, bic := range bics {
		net, err := srv.nets.Bank(ctx, payment.ParticipantID(bic))
		if err != nil {
			t.Fatalf("opening %s's store: %v", bic, err)
		}
		bank, err := net.GetBank(ctx, payment.ParticipantID(bic))
		if err != nil {
			t.Fatalf("%s's own row: %v", bic, err)
		}
		accts, err := bank.AccountsFor("EUR")
		if err != nil {
			t.Fatalf("%s's EUR accounts: %v", bic, err)
		}
		bal, err := bank.Ledger.BookBalance(ctx, accts.Suspense.Total())
		if err != nil {
			t.Fatalf("%s's suspense: %v", bic, err)
		}
		total += bal
	}
	return total
}

// scenarioIDs are unique, because the route keys on one and a duplicate would
// make the second unreachable.
func TestNoTwoScenariosShareAnID(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range scenarios {
		if seen[s.ID] {
			t.Errorf("two scenarios are named %q; the second is unreachable", s.ID)
		}
		if strings.TrimSpace(s.ID) != s.ID || s.ID == "" {
			t.Errorf("scenario id %q is not a path segment", s.ID)
		}
		seen[s.ID] = true
	}
}

// The defect this whole arrangement exists to remove: a payment the dataset
// placed was never sent by anybody, so its own screen could name no file. Every
// settled payment in the demo network now names the files that carried it, at
// EVERY institution that was party to it.
func TestEveryDemoNetworkPaymentNamesTheFilesThatCarriedIt(t *testing.T) {
	srv := baseState(t)
	runScenario(t, srv, scenarioDemoNetwork.ID)

	ctx := context.Background()
	var payments []api.PaymentDTO
	getJSON(t, csmSurface(srv), "/payments", &payments)
	if len(payments) == 0 {
		t.Fatal("the demo network made no payment; this test would pass on an empty store")
	}

	for _, p := range payments {
		// The clearing house's own log, which is the institution every payment
		// passes through whatever became of it.
		carried, err := srv.dep.ClearingHouse().Network().ListMessages(ctx, payment.MessageFilter{
			PaymentID: payment.PaymentID(p.ID), WithoutPayload: true,
		})
		if err != nil {
			t.Fatalf("the clearing house's log for %s: %v", p.ID, err)
		}
		if len(carried) == 0 {
			t.Errorf("%s (%s, %s) names no file at the clearing house; it was placed rather than sent",
				p.ID, p.EndToEndID, p.Status)
			continue
		}
		// And the SUBMITTING bank's, which is the log a reader on that bank's screen
		// is shown.
		submitter := iso20022.BIC(p.DebtorAgent)
		if p.Scheme == string(payment.SchemeSEPADD) {
			submitter = iso20022.BIC(p.CreditorAgent)
		}
		net, err := srv.nets.Bank(ctx, payment.ParticipantID(submitter))
		if err != nil {
			t.Fatalf("opening %s's store: %v", submitter, err)
		}
		own, err := net.ListMessages(ctx, payment.MessageFilter{
			PaymentID: payment.PaymentID(p.ID), WithoutPayload: true,
		})
		if err != nil {
			t.Fatalf("%s's log for %s: %v", submitter, p.ID, err)
		}
		if len(own) == 0 {
			t.Errorf("%s (%s) names no file at %s, which submitted it", p.ID, p.EndToEndID, submitter)
		}
	}
}

// And what the demo network is FOR: every payment status a reader can be shown,
// present at once.
func TestTheDemoNetworkCoversEveryPaymentStatus(t *testing.T) {
	srv := baseState(t)
	runScenario(t, srv, scenarioDemoNetwork.ID)

	var payments []api.PaymentDTO
	getJSON(t, csmSurface(srv), "/payments", &payments)

	seen := map[string]int{}
	for _, p := range payments {
		seen[p.Status]++
	}
	for _, want := range []payment.PaymentStatus{
		payment.Accepted, payment.Settled, payment.Rejected, payment.Returned,
	} {
		if seen[want.String()] == 0 {
			t.Errorf("no payment in the demo network is %v; the statuses present are %v", want, seen)
		}
	}
}
