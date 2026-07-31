package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

// The deliberate overlaps, as an allowlist rather than a tolerance, so a third
// accidental one fails.
//
//   - GET /assets is a compiled-in constant every operator needs to render money
//     at the right scale. Duplicating a constant is not duplicating state.
//   - GET /directory is on the bank as well as the clearing house because a bank
//     is a scheme participant with directory access, and the alternative — a
//     customer's browser querying the CSM — gives a retail app a clearing-house
//     connection no retail app has.
//   - GET /audit means "this operator's own log" on every operator: a bank's
//     ledger events on a bank, the reserve movements on the central bank. Same
//     pattern, different operator, different answer — which is what the split is
//     for, and is worth the consistency.
var allowedOverlaps = []string{"GET /assets", "GET /directory", "GET /audit"}

func surfaces(t *testing.T) map[string][]string {
	t.Helper()
	s := newServer(t, nil)
	return map[string][]string{
		"central-bank":   s.centralBankRouter().Patterns(),
		"clearing-house": s.clearingHouseRouter().Patterns(),
		"bank":           s.forBank("bank_1").bankRouter().Patterns(),
	}
}

// TestSurfacesAreDisjoint is the guard against a route quietly appearing on two
// operators — which would put a bank's ledger back within reach of a
// central-bank operator with a URL bar.
func TestSurfacesAreDisjoint(t *testing.T) {
	seen := map[string]string{}
	for _, op := range []string{"bank", "central-bank", "clearing-house"} {
		for _, p := range surfaces(t)[op] {
			if other, dup := seen[p]; dup && !slices.Contains(allowedOverlaps, p) {
				t.Errorf("%q is on both %s and %s", p, other, op)
			}
			seen[p] = op
		}
	}
}

// TestEveryRouteLandsSomewhere is the guard against losing one. Every pattern
// the single server served must arrive on the right operator, with its
// /participants/{pid} prefix stripped where the port now carries it.
func TestEveryRouteLandsSomewhere(t *testing.T) {
	got := surfaces(t)

	for _, old := range preSplitRoutes {
		op, want := movedTo(old)
		if op == "" {
			continue // deliberately not moved yet; see the switch below
		}
		if !slices.Contains(got[op], want) {
			t.Errorf("%q should have become %q on the %s, which does not serve it",
				old, want, op)
		}
	}
}

// movedTo maps a pre-split pattern to the operator that serves it now and the
// pattern it became. It returns an empty operator for the routes a later task
// moves.
//
// Returning the operator as well as the pattern matters: a bank's audit log and
// the central bank's both become "GET /audit", so a flat set of landed patterns
// would let either one mask the other's absence.
func movedTo(old string) (operator, pattern string) {
	method, path, _ := strings.Cut(old, " ")
	switch {
	case path == "/participants" && method == "POST":
		return "central-bank", "POST /members"
	case path == "/participants" && method == "GET":
		return "clearing-house", "GET /members"
	case path == "/participants/{pid}":
		return "bank", "GET /me"
	case strings.HasPrefix(path, "/participants/{pid}/"):
		return "bank", method + " /" + strings.TrimPrefix(path, "/participants/{pid}/")

	case strings.HasPrefix(path, "/central-bank/"):
		return "central-bank", method + " /" + strings.TrimPrefix(path, "/central-bank/")
	case path == "/admin/reset":
		return "central-bank", old

	case path == "/cycles/{cid}/settle":
		// Settling moved operator as well as shape: the cycle is now the body
		// of POST /settlements on the central bank.
		return "central-bank", "POST /settlements"

	case path == "/assets":
		// On every operator; the disjointness allowlist is what records that.
		return "clearing-house", old

	default:
		// Payments, mandates, cycles, settlements, schemes, the directory.
		return "clearing-house", old
	}
}

// preSplitRoutes is the API as one server served it, captured before the split.
// It is a golden list rather than something derived from the code, because a
// list derived from the code after the fact could only ever agree with itself:
// the whole question is whether the three surfaces still serve what the one
// server did.
var preSplitRoutes = []string{
	"DELETE /participants/{pid}/deposit-accounts/{did}",
	"DELETE /participants/{pid}/deposit-accounts/{did}/identifiers/{scheme}/{value}",
	"DELETE /participants/{pid}/facilities/{fid}",
	"GET /assets",
	"GET /central-bank/audit",
	"GET /central-bank/reserves",
	"GET /central-bank/reserves/{pid}",
	"GET /cycles",
	"GET /cycles/{cid}",
	"GET /directory",
	"GET /mandates",
	"GET /mandates/{mid}",
	"GET /participants",
	"GET /participants/{pid}",
	"GET /participants/{pid}/accounts/{aid}",
	"GET /participants/{pid}/accounts/{aid}/balance",
	"GET /participants/{pid}/audit",
	"GET /participants/{pid}/deposit-accounts",
	"GET /participants/{pid}/deposit-accounts/{did}",
	"GET /participants/{pid}/deposit-accounts/{did}/balance",
	"GET /participants/{pid}/deposit-accounts/{did}/holds",
	"GET /participants/{pid}/deposit-accounts/{did}/overdraft-terms",
	"GET /participants/{pid}/deposit-accounts/{did}/snapshots",
	"GET /participants/{pid}/deposit-audit",
	"GET /participants/{pid}/facilities",
	"GET /participants/{pid}/facilities/{fid}",
	"GET /participants/{pid}/facilities/{fid}/schedule",
	"GET /participants/{pid}/holds/{hid}",
	"GET /participants/{pid}/interest-refunds-payable",
	"GET /participants/{pid}/ledgers",
	"GET /participants/{pid}/ledgers/{lid}",
	"GET /participants/{pid}/ledgers/{lid}/subledgers",
	"GET /participants/{pid}/products",
	"GET /participants/{pid}/products/{prid}",
	"GET /participants/{pid}/products/{prid}/versions",
	"GET /participants/{pid}/subledgers/{sid}",
	"GET /participants/{pid}/subledgers/{sid}/accounts",
	"GET /participants/{pid}/totals",
	"GET /participants/{pid}/transactions",
	"GET /participants/{pid}/transactions/{tid}",
	"GET /payments",
	"GET /payments/{payid}",
	"GET /payments/audit",
	"GET /schemes",
	"GET /settlements",
	"GET /settlements/{sid}",
	"POST /admin/reset",
	"POST /cycles",
	"POST /cycles/{cid}/close",
	"POST /cycles/{cid}/settle",
	"POST /mandates",
	"POST /mandates/{mid}/revoke",
	"POST /participants",
	"POST /participants/{pid}/deposit-accounts",
	"POST /participants/{pid}/deposit-accounts/{did}/holds",
	"POST /participants/{pid}/deposit-accounts/{did}/identifiers",
	"POST /participants/{pid}/deposit-accounts/{did}/interest-charge",
	"POST /participants/{pid}/deposit-accounts/{did}/overdraft-limit",
	"POST /participants/{pid}/deposit-accounts/{did}/overdraft-pricing",
	"POST /participants/{pid}/deposit-accounts/{did}/product",
	"POST /participants/{pid}/deposit-accounts/{did}/snapshots",
	"POST /participants/{pid}/deposit-accounts/{did}/status",
	"POST /participants/{pid}/deposits",
	"POST /participants/{pid}/end-of-day",
	"POST /participants/{pid}/facilities",
	"POST /participants/{pid}/facilities/{fid}/disbursement",
	"POST /participants/{pid}/facilities/{fid}/draws",
	"POST /participants/{pid}/facilities/{fid}/interest-charge",
	"POST /participants/{pid}/facilities/{fid}/interest-refunds",
	"POST /participants/{pid}/facilities/{fid}/repayments",
	"POST /participants/{pid}/holds/{hid}/capture",
	"POST /participants/{pid}/holds/{hid}/release",
	"POST /participants/{pid}/ledgers",
	"POST /participants/{pid}/ledgers/{lid}/subledgers",
	"POST /participants/{pid}/products",
	"POST /participants/{pid}/products/{prid}/retire",
	"POST /participants/{pid}/products/{prid}/versions",
	"POST /participants/{pid}/products/{prid}/versions/{day}/publish",
	"POST /participants/{pid}/subledgers/{sid}/accounts",
	"POST /participants/{pid}/transactions",
	"POST /participants/{pid}/transactions/{tid}/reversal",
	"POST /payments",
	"POST /payments/{payid}/reject",
	"POST /payments/{payid}/return",
}

// TestABankCannotNameAnotherBank is the whole point of the split.
//
// The old API took the bank as a path segment, so any caller could ask for any
// bank's ledger by editing a URL. There is now nowhere in a bank's API to put
// another bank's id — the port has already answered that question — so the old
// shape is not a route at all, and the bank's own data is reached without
// naming it.
func TestABankCannotNameAnotherBank(t *testing.T) {
	s := newServer(t, nil)
	aurora := doJSON(t, cb(s), "POST", "/members", `{"name":"Aurora Bank"}`, http.StatusCreated)["id"].(string)
	verde := doJSON(t, cb(s), "POST", "/members", `{"name":"Banca Verde"}`, http.StatusCreated)["id"].(string)

	h := bank(s, aurora)
	assertStatus(t, h, "GET", "/participants/"+verde+"/deposit-accounts", "", http.StatusNotFound)
	assertStatus(t, h, "GET", "/participants/"+aurora+"/deposit-accounts", "", http.StatusNotFound)

	// And its own list is reached by asking for it, with no id anywhere.
	var mine []depositAccountDTO
	getJSON(t, h, "/deposit-accounts", &mine)

	// The bank a listener is bound to is the bank it answers as.
	got := doJSON(t, h, "GET", "/me", "", http.StatusOK)
	if got["id"] != aurora {
		t.Fatalf("GET /me on Aurora's listener = %v, want %s", got["id"], aurora)
	}
	got = doJSON(t, bank(s, verde), "GET", "/me", "", http.StatusOK)
	if got["id"] != verde {
		t.Fatalf("GET /me on Verde's listener = %v, want %s", got["id"], verde)
	}
}

// TestSettlingIsTheCentralBanksAct pins the operator, not the mechanism.
//
// Settlement moves reserves between accounts in the central bank's own book; a
// clearing house that could do that would be a central bank. Before the split
// the CSM settled directly, because there was only one server to put the route
// on and nothing in the shape of the API could say otherwise.
//
// The clearing house keeps GET /settlements: it needs to know whether the cycle
// it closed has settled, and reading is not doing.
func TestSettlingIsTheCentralBanksAct(t *testing.T) {
	h := newServer(t, nil)
	cid := closedCycle(t, h)

	assertStatus(t, csm(h), "POST", "/cycles/"+cid+"/settle", "", http.StatusNotFound)

	settlement := doJSON(t, cb(h), "POST", "/settlements",
		`{"cycleId":"`+cid+`"}`, http.StatusOK)
	if settlement["cycleId"] != cid {
		t.Fatalf("settlement cycleId = %v, want %s", settlement["cycleId"], cid)
	}

	var settlements []settlementDTO
	getJSON(t, csm(h), "/settlements", &settlements)
	if len(settlements) != 1 {
		t.Fatalf("the clearing house sees %d settlements, want 1", len(settlements))
	}
}

// closedCycle builds the smallest thing that can be settled: two banks, one
// funded payment between them, cleared into a cycle that is then closed.
func closedCycle(t *testing.T, h *Server) string {
	t.Helper()
	a := doJSON(t, cb(h), "POST", "/members", `{"name":"Bank A"}`, http.StatusCreated)["id"].(string)
	b := doJSON(t, cb(h), "POST", "/members", `{"name":"Bank B"}`, http.StatusCreated)["id"].(string)
	alice := doJSON(t, bank(h, a), "POST", "/deposit-accounts",
		`{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`","identifiers":[{"scheme":"IBAN","value":"SET-ALICE-0001"}]}`,
		http.StatusCreated)["id"].(string)
	bob := doJSON(t, bank(h, b), "POST", "/deposit-accounts",
		`{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`","identifiers":[{"scheme":"IBAN","value":"SET-BOB-0001"}]}`,
		http.StatusCreated)["id"].(string)
	doJSON(t, bank(h, a), "POST", "/deposits",
		`{"account":"`+alice+`","amount":100000,"description":"opening"}`, http.StatusOK)

	cyc := doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)["id"].(string)
	doJSON(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`"},
		"amount":25000,
		"endToEndId":"settle-e2e"
	}`, http.StatusCreated)
	assertStatus(t, csm(h), "POST", "/cycles/"+cyc+"/close", "", http.StatusOK)
	return cyc
}
