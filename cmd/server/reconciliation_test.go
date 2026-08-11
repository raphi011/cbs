package main

import (
	"net/http"
	"testing"

	"github.com/raphi011/cbs/api"
)

// A bank checking its own books over HTTP.
//
// The domain's own suite is where the instrument is calibrated: payment's
// reconcile_test.go damages one thing at a time behind the bank's back and holds
// the run to naming it. Nothing here repeats that. What these tests are for is
// the surface — that the four routes are one bank's and no other operator's,
// that the run is a POST because it writes, and that the two grades of answer the
// domain draws survive the wire.

// TestABankReconcilesItsOwnBooksOverHTTP is the control, and it is the shape an
// operator console renders: reserve, the newest statement, and nothing wrong.
func TestABankReconcilesItsOwnBooksOverHTTP(t *testing.T) {
	h := newServer(t, nil)
	cid := settledCycle(t, h)

	// Bank A funded 100000 onto reserve and paid 25000 away, so the agent's
	// statement leaves it at 75000 and this bank's own book has to agree.
	got := doJSON(t, bankSurface(h, "BNKADEFFXXX"), "POST", "/reconciliation?asset=EUR", "", http.StatusOK)
	assertEqual(t, "reconciled", got["reconciled"], any(true))
	assertEqual(t, "reserve", int64(got["reserve"].(float64)), int64(75000))
	assertEqual(t, "the closing balance it was advised", int64(got["advised"].(float64)), int64(75000))
	assertEqual(t, "what the statement quoted", got["reference"], any(cid))
	assertEqual(t, "lodged since that statement", int64(got["lodgedSince"].(float64)), int64(0))
	if n := len(got["breaks"].([]any)); n != 0 {
		t.Fatalf("a network that settled cleanly reports %d breaks: %v", n, got["breaks"])
	}
	// And no position either: the cut-off settled, so the suspense that carried
	// the payment has returned to zero. A position here would be money in flight
	// and not a defect — see the unclaimed test below, which has one.
	if n := len(got["positions"].([]any)); n != 0 {
		t.Fatalf("nothing is in flight and the run reports %d positions: %v", n, got["positions"])
	}

	// The statement it checked against, read back. This is where a break naming a
	// reference sends its reader.
	var advices []api.SettlementAdviceDTO
	getJSON(t, bankSurface(h, "BNKADEFFXXX"), "/settlement-advices", &advices)
	if len(advices) != 1 {
		t.Fatalf("bank A holds %d statements, want the one from the cut-off", len(advices))
	}
	assertEqual(t, "reference", advices[0].Reference, cid)
	assertEqual(t, "movement", advices[0].Movement, int64(-25000))
	assertEqual(t, "closing balance", advices[0].ClosingBalance, int64(75000))
	assertEqual(t, "status", advices[0].Status, "Posted")
	if advices[0].MirrorTransaction == "" {
		t.Error("the statement names no leg; a row claiming a posting that is not there is what the run calls a break")
	}

	// The two ageing reports answer, and both are empty for the same reason the
	// positions above are.
	var suspense, unclaimed api.AgeingReportDTO
	getJSON(t, bankSurface(h, "BNKADEFFXXX"), "/clearing-suspense/ageing?asset=EUR", &suspense)
	getJSON(t, bankSurface(h, "BNKADEFFXXX"), "/unclaimed-balances/ageing?asset=EUR", &unclaimed)
	assertEqual(t, "suspense balance", suspense.Balance, int64(0))
	assertEqual(t, "unclaimed balance", unclaimed.Balance, int64(0))
}

// TestTheRunIsAnActAndLeavesTheTrailToProveIt is why the run is a POST.
//
// Reconcile appends to this bank's own audit log in the same unit of work as the
// read, so a run is a thing that happened and not a question that was asked. A
// GET here would be a lie about the method, and the event below is what makes
// that claim checkable from outside the domain.
func TestTheRunIsAnActAndLeavesTheTrailToProveIt(t *testing.T) {
	h := newServer(t, nil)
	settledCycle(t, h)

	var before []api.AuditEventDTO
	getJSON(t, bankSurface(h, "BNKADEFFXXX"), "/payments/audit?type=reconciliation.run", &before)
	assertEqual(t, "runs before anybody has run one", len(before), 0)

	doJSON(t, bankSurface(h, "BNKADEFFXXX"), "POST", "/reconciliation?asset=EUR", "", http.StatusOK)

	var after []api.AuditEventDTO
	getJSON(t, bankSurface(h, "BNKADEFFXXX"), "/payments/audit?type=reconciliation.run", &after)
	if len(after) != 1 {
		t.Fatalf("one run left %d audit events, want 1", len(after))
	}
	assertEqual(t, "what the event is about", after[0].EntityID, "EUR")
}

// TestAnUnclaimedBalanceIsReportedWithItsDeadline drives the whole of the case
// the account exists for: a payee whose account closes between their bank's
// acceptance and the cut-off.
//
// It is the stronger of the two ageing answers and the reason is a fact about the
// postings rather than about this route — every credit into unclaimed balances is
// one payment's diverted leg and carries its id, so the report says which payment,
// under which scheme, and that this bank may send it back.
func TestAnUnclaimedBalanceIsReportedWithItsDeadline(t *testing.T) {
	h := newServer(t, nil)
	a := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	b := provisionMember(t, h, "BNKBDEFFXXX", "Bank B")
	alice := doJSON(t, bankSurface(h, a), "POST", "/deposit-accounts",
		`{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`"}`,
		http.StatusCreated)["id"].(string)
	// Bob is opened and never funded, so his bank can close the account while the
	// payment to him is in the cut-off — which is the only way a credit reaches
	// unclaimed balances.
	bob := doJSON(t, bankSurface(h, b), "POST", "/deposit-accounts",
		`{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`,
		http.StatusCreated)["id"].(string)
	fundAndLodge(t, h, a, alice, 100000)

	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	pay := doJSON(t, bankSurface(h, a), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+alice+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, alice)+`"}},
		"creditor":{"account":"`+bob+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, b, bob)+`"}},
		"amount":25000,
		"endToEndId":"unclaimed-e2e",
		"creditorName":"Bob"
	}`, http.StatusAccepted)["paymentId"].(string)
	carry(t, h)

	// Bob closes his account after his bank has accepted the credit and before
	// the cut-off moves the money.
	assertStatus(t, bankSurface(h, b), "DELETE", "/deposit-accounts/"+bob, "", http.StatusNoContent)
	var cycles []api.ClearingCycleDTO
	getJSON(t, csmSurface(h), "/cycles", &cycles)
	assertStatus(t, csmSurface(h), "POST", "/cycles/"+cycles[0].ID+"/close", "", http.StatusOK)
	carry(t, h)

	var rep api.AgeingReportDTO
	getJSON(t, bankSurface(h, b), "/unclaimed-balances/ageing?asset=EUR", &rep)
	assertEqual(t, "balance", rep.Balance, int64(25000))
	if len(rep.Lots) != 1 {
		t.Fatalf("the report holds %d lots, want the one payment that could not be applied", len(rep.Lots))
	}
	lot := rep.Lots[0]
	assertEqual(t, "which payment put it there", lot.Payment, pay)
	assertEqual(t, "and under which scheme", lot.Scheme, "sepa.ct")
	assertEqual(t, "amount", lot.Amount, int64(25000))
	assertEqual(t, "age", lot.Days, 0)
	assertEqual(t, "the rulebook window", lot.Deadline, 3)
	assertEqual(t, "and it has not run out", lot.Overdue, false)
	assertEqual(t, "nothing stops this bank returning it", lot.Blocked, "")

	// A position and never a break: the money is where it should be, and the run
	// reporting it as a defect would be a run nobody could make against a network
	// with an unapplicable credit in it.
	got := doJSON(t, bankSurface(h, b), "POST", "/reconciliation?asset=EUR", "", http.StatusOK)
	assertEqual(t, "reconciled", got["reconciled"], any(true))
}

// TestAClearingSuspenseIsAgedWithNoDeadlineOnIt is the contrast, out of a
// payment that has cleared and not yet settled.
//
// No rulebook puts a clock on a clearing suspense — what discharges it is a
// conversation — so the report ages it and judges nothing, and the netted mirror
// leg that will discharge it names no payment, which is why a lot here can come
// back without one.
func TestAClearingSuspenseIsAgedWithNoDeadlineOnIt(t *testing.T) {
	h := newServer(t, nil)
	a, b, _ := threeBanks(t, h)
	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	lodge(t, h, a.pid, "EUR", 100000)
	sct(t, h, a, b, "in-flight")

	var rep api.AgeingReportDTO
	getJSON(t, bankSurface(h, a.pid), "/clearing-suspense/ageing?asset=EUR", &rep)
	assertEqual(t, "balance", rep.Balance, int64(10000))
	if len(rep.Lots) != 1 {
		t.Fatalf("the report holds %d lots, want the one payment in flight", len(rep.Lots))
	}
	assertEqual(t, "no rulebook clock on it", rep.Lots[0].Deadline, 0)
	assertEqual(t, "so it is never overdue", rep.Lots[0].Overdue, false)

	// And the run says the same thing, as a position rather than a break.
	got := doJSON(t, bankSurface(h, a.pid), "POST", "/reconciliation?asset=EUR", "", http.StatusOK)
	assertEqual(t, "reconciled", got["reconciled"], any(true))
	if n := len(got["positions"].([]any)); n != 1 {
		t.Fatalf("a payment in flight and the run reports %d positions: %v", n, got["positions"])
	}
}

// TestEveryReportNamesTheAssetItIsAbout pins the one refusal these routes make
// themselves.
//
// A bank holds one reserve, one clearing suspense and one unclaimed-balances
// account per asset. A report with no asset named would have to add one money to
// another, and defaulting to EUR would answer confidently about an asset the
// caller did not ask about.
func TestEveryReportNamesTheAssetItIsAbout(t *testing.T) {
	h := newServer(t, nil)
	settledCycle(t, h)
	ab := bankSurface(h, "BNKADEFFXXX")

	assertStatus(t, ab, "POST", "/reconciliation", "", http.StatusBadRequest)
	assertStatus(t, ab, "GET", "/clearing-suspense/ageing", "", http.StatusBadRequest)
	assertStatus(t, ab, "GET", "/unclaimed-balances/ageing", "", http.StatusBadRequest)

	// GET /settlement-advices takes none, and that is not an inconsistency: the
	// three above read ONE account each and an account belongs to one asset, where
	// this reads a bank's whole correspondence and every row carries its own.
	assertStatus(t, ab, "GET", "/settlement-advices", "", http.StatusOK)
}

// TestOnlyABankChecksItsOwnBooks is the surface half of the whole task.
//
// These four reports read one database and the statements that arrived at it,
// which is what makes them a bank's own act. The instrument that holds every
// institution's books against each other is payment/recon, and it is a test
// harness rather than a route precisely because no institution may perform it —
// so there is no version of any of these on the other two operators' ports.
func TestOnlyABankChecksItsOwnBooks(t *testing.T) {
	h := newServer(t, nil)
	settledCycle(t, h)

	for _, route := range []struct{ method, path string }{
		{"POST", "/reconciliation?asset=EUR"},
		{"GET", "/settlement-advices"},
		{"GET", "/clearing-suspense/ageing?asset=EUR"},
		{"GET", "/unclaimed-balances/ageing?asset=EUR"},
	} {
		// The clearing house has no ledger at all, and the settlement agent holds
		// neither of the two accounts aged here — it holds its own reserve
		// register, which is a different book with a different question over it.
		assertStatus(t, csmSurface(h), route.method, route.path, "", http.StatusNotFound)
		assertStatus(t, cbSurface(h), route.method, route.path, "", http.StatusNotFound)
	}
}
