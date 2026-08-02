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
//   - The three payment routes are on a bank and the clearing house with
//     different handlers: the bank's reads are narrowed to its own legs, and its
//     POST accepts a customer instruction and answers 202 with an identifier.
//     Same pattern, different operator, different answer.
//   - The four cycle and settlement reads are on the central bank as well as the
//     clearing house, with the same handlers and the same answers. They are not
//     two views of one thing: the clearing house reads them because it closed the
//     cycle and sent the instruction and wants to know whether it was discharged,
//     and the central bank reads them because they are the only record its own
//     operator has of what it answered — a cycle still Closed is one it refused.
var allowedOverlaps = []string{
	"GET /assets",
	"GET /directory",
	"GET /audit",
	"GET /payments",
	"GET /payments/{payid}",
	"POST /payments",
	"GET /cycles",
	"GET /cycles/{cid}",
	"GET /settlements",
	"GET /settlements/{sid}",
}

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
// moves, and for the one route that has been deleted outright.
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
		// GONE, and not moved. Settling is no longer an operator's act on any
		// surface: the clearing house reaches a cut-off, sends a pacs.009, and
		// the central bank's actor answers it (mesh.centralBank). This route
		// moved once already, to POST /settlements on the central bank, and that
		// one is deleted too — see TestSettlementIsNoLongerAnHTTPAction, which is
		// the pin on its absence rather than on its whereabouts.
		//
		// The empty operator is the same "not here" this switch uses for a route
		// a later task moves, and the difference is worth stating: those come
		// back, and this one does not.
		return "", ""

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
	aurora := doJSON(t, cb(s), "POST", "/members", `{"bic":"AURODEFFXXX","name":"Aurora Bank"}`, http.StatusCreated)["id"].(string)
	verde := doJSON(t, cb(s), "POST", "/members", `{"bic":"VERDITMMXXX","name":"Banca Verde"}`, http.StatusCreated)["id"].(string)

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

// TestTheClearingHouseReadsTheSettlementItDidNotPerform is what is left of the
// operator split's settlement assertion, and the rename is part of the finding.
//
// It was TestSettlingIsTheCentralBanksAct, and it pinned that by driving POST
// /settlements on one operator and getting a 404 on the other. Neither half is
// available now: the route is gone from every surface, so there is no HTTP act
// left to attribute to anybody, and a test still called that would be naming an
// assertion it no longer makes. TestSettlementIsNoLongerAnHTTPAction is where
// the absence is pinned.
//
// What survives is the READ, and it is the half that keeps mattering: the
// clearing house closed the cycle and sent the instruction, so it has to be able
// to find out whether the central bank discharged it. Reading is not doing, and
// after Task 12 that sentence is the whole of the clearing house's relationship
// with settlement.
func TestTheClearingHouseReadsTheSettlementItDidNotPerform(t *testing.T) {
	h := newServer(t, nil)
	cid := settledCycle(t, h)

	sid := settlementOfCycle(t, h, cid)
	var settlement settlementDTO
	getJSON(t, csm(h), "/settlements/"+sid, &settlement)
	if settlement.CycleID != cid {
		t.Fatalf("settlement cycleId = %v, want %s", settlement.CycleID, cid)
	}

	var settlements []settlementDTO
	getJSON(t, csm(h), "/settlements", &settlements)
	if len(settlements) != 1 {
		t.Fatalf("the clearing house sees %d settlements, want 1", len(settlements))
	}
}

// TestTheCentralBankCanReadTheCycleItSettles is the other half: what the central
// bank's own console is left with.
//
// These four reads were added so that a human could see the cycle they were
// about to settle. Nobody settles by hand any more, and they are MORE load
// bearing rather than less: a closed cycle with no settlement against it is an
// instruction the central bank refused, and these reads plus GET /reserves are
// the only way an operator finds out. There is nothing else — the refusal's code
// travels in a pacs.002 between two actors and is stored nowhere.
//
// What the central bank still cannot see is an individual payment, which is the
// boundary that matters and which settling on instruction does not widen: the
// pacs.009 it acts on names a cycle and net positions, and no payment at all.
func TestTheCentralBankCanReadTheCycleItSettles(t *testing.T) {
	h := newServer(t, nil)
	cid := settledCycle(t, h)

	var cycles []clearingCycleDTO
	getJSON(t, cb(h), "/cycles", &cycles)
	if len(cycles) != 1 || cycles[0].ID != cid {
		t.Fatalf("the central bank sees %v, want the one cycle %s", cycles, cid)
	}

	// Settled, and by this institution: the clearing house sent a pacs.009 and
	// this actor discharged it. Before the mesh a cycle sat Closed until a human
	// pressed a second button on this console.
	got := doJSON(t, cb(h), "GET", "/cycles/"+cid, "", http.StatusOK)
	if got["status"] != "Settled" {
		t.Fatalf("cycle status = %v, want Settled", got["status"])
	}

	sid := settlementOfCycle(t, h, cid)

	// And it can read back what it did, without asking the clearing house.
	var settlements []settlementDTO
	getJSON(t, cb(h), "/settlements", &settlements)
	if len(settlements) != 1 {
		t.Fatalf("the central bank sees %d settlements, want 1", len(settlements))
	}
	doJSON(t, cb(h), "GET", "/settlements/"+sid, "", http.StatusOK)

	// The boundary that matters is untouched: an individual payment is still
	// the clearing house's, and a central bank does not see one.
	assertStatus(t, cb(h), "GET", "/payments", "", http.StatusNotFound)
}

// settledCycle builds the smallest thing a settlement can come out of: two
// banks, one funded payment between them, cleared into a cycle that is then
// closed — and, because the cut-off is what instructs settlement, discharged by
// the central bank once the conversation has finished.
//
// It was called closedCycle, and the rename is the change rather than tidying.
// Closing used to leave a Closed cycle and nothing else, and a second console
// button settled it. There is no such button: the cut-off sends the pacs.009,
// and after the drain below this cycle is Settled.
func settledCycle(t *testing.T, h *Server) string {
	t.Helper()
	a := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusCreated)["id"].(string)
	b := doJSON(t, cb(h), "POST", "/members", `{"bic":"BNKBDEFFXXX","name":"Bank B"}`, http.StatusCreated)["id"].(string)
	alice := doJSON(t, bank(h, a), "POST", "/deposit-accounts",
		`{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`","identifiers":[{"scheme":"IBAN","value":"SE89-SET-ALICE-0001"}]}`,
		http.StatusCreated)["id"].(string)
	bob := doJSON(t, bank(h, b), "POST", "/deposit-accounts",
		`{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`","identifiers":[{"scheme":"IBAN","value":"SE89-SET-BOB-0001"}]}`,
		http.StatusCreated)["id"].(string)
	doJSON(t, bank(h, a), "POST", "/deposits",
		`{"account":"`+alice+`","amount":100000,"description":"opening"}`, http.StatusOK)

	cyc := doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)["id"].(string)
	doJSON(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+a+`","account":"`+alice+`"},
		"creditor":{"participant":"`+b+`","account":"`+bob+`","identifier":{"scheme":"IBAN","value":"SE89-SET-BOB-0001"}},
		"amount":25000,
		"endToEndId":"settle-e2e"
	}`, http.StatusAccepted)
	drainServer(t, h)
	assertStatus(t, csm(h), "POST", "/cycles/"+cyc+"/close", "", http.StatusOK)
	drainServer(t, h)
	return cyc
}

// TestABankSeesOnlyItsOwnPayments pins the narrowing the single server could
// not express.
//
// GET /payments listed every payment in the network to every caller —
// competitors' customers, their counterparties and their amounts. Narrowing it
// needs a caller identity, and there was none until the port became one.
func TestABankSeesOnlyItsOwnPayments(t *testing.T) {
	h := newServer(t, nil)
	a, b, c := threeBanks(t, h)
	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	mine := sct(t, h, a, b, "own-leg")
	theirs := sct(t, h, b, c, "not-mine")

	// Aurora is the debtor on one and a party to no other.
	var list []paymentDTO
	getJSON(t, bank(h, a.pid), "/payments", &list)
	if len(list) != 1 || list[0].ID != mine {
		t.Fatalf("bank A sees %d payments (%+v), want only its own leg %s", len(list), list, mine)
	}

	doJSON(t, bank(h, a.pid), "GET", "/payments/"+mine, "", http.StatusOK)

	// 404 and not 403: a payment this bank is not party to does not exist as
	// far as its API is concerned, and a 403 would confirm that the id names
	// something real.
	doJSON(t, bank(h, a.pid), "GET", "/payments/"+theirs, "", http.StatusNotFound)

	// The creditor sees it too — "its own" is either leg, not just the debit.
	var bList []paymentDTO
	getJSON(t, bank(h, b.pid), "/payments", &bList)
	if len(bList) != 2 {
		t.Fatalf("bank B is a party to both payments but sees %d", len(bList))
	}

	// The clearing house is the CSM. Seeing every payment is its job, not a leak.
	var all []paymentDTO
	getJSON(t, csm(h), "/payments", &all)
	if len(all) != 2 {
		t.Fatalf("the clearing house sees %d payments, want both", len(all))
	}
}

// seededBank is a bank plus one funded, IBAN-addressed customer account — the
// least a bank needs to be either end of an SCT.
type seededBank struct {
	pid     string
	account string
	iban    string
}

func threeBanks(t *testing.T, h *Server) (a, b, c seededBank) {
	t.Helper()
	// A BIC each: the mesh gives every bank an actor keyed by its address and
	// refuses two on one, so three banks that shared a BIC could not be admitted
	// at all — let alone tell each other apart on the wire.
	mk := func(name, bic, iban string) seededBank {
		pid := doJSON(t, cb(h), "POST", "/members", `{"bic":"`+bic+`","name":"`+name+`"}`, http.StatusCreated)["id"].(string)
		did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts",
			`{"name":"`+name+` customer","asset":"EUR","productId":"`+prdOf(t, h, pid)+
				`","identifiers":[{"scheme":"IBAN","value":"`+iban+`"}]}`,
			http.StatusCreated)["id"].(string)
		doJSON(t, bank(h, pid), "POST", "/deposits",
			`{"account":"`+did+`","amount":500000,"description":"opening"}`, http.StatusOK)
		return seededBank{pid: pid, account: did, iban: iban}
	}
	return mk("Bank A", "BNKADEFFXXX", "SE89-NARROW-A-0001"),
		mk("Bank B", "BNKBDEFFXXX", "IT60-NARROW-B-0001"),
		mk("Bank C", "BNKCDEFFXXX", "NO93-NARROW-C-0001")
}

// sct initiates a credit transfer between two seeded banks and returns its id.
func sct(t *testing.T, h *Server, from, to seededBank, e2e string) string {
	t.Helper()
	id := doJSON(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+from.pid+`","account":"`+from.account+`"},
		"creditor":{"participant":"`+to.pid+`","account":"`+to.account+`","identifier":{"scheme":"IBAN","value":"`+to.iban+`"}},
		"amount":10000,
		"endToEndId":"`+e2e+`"
	}`, http.StatusAccepted)["id"].(string)
	// The payment is Initiated when the 202 is written and the counterparty has
	// not seen it. Callers of this helper assert on what became of it, so the
	// conversation is carried to its end here.
	drainServer(t, h)
	return id
}

// TestABankAcceptsItsOwnCustomersInstruction pins both halves of retail
// submission: the debtor must be one of this bank's accounts, and the answer is
// an identifier rather than the payment.
func TestABankAcceptsItsOwnCustomersInstruction(t *testing.T) {
	h := newServer(t, nil)
	a, b, _ := threeBanks(t, h)
	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	instruction := `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"` + a.pid + `","account":"` + a.account + `"},
		"creditor":{"participant":"` + b.pid + `","account":"` + b.account + `","identifier":{"scheme":"IBAN","value":"` + b.iban + `"}},
		"amount":10000,
		"endToEndId":"retail-1"
	}`

	got := doJSON(t, bank(h, a.pid), "POST", "/payments", instruction, http.StatusAccepted)
	id, ok := got["paymentId"].(string)
	if !ok || id == "" {
		t.Fatalf("response = %v, want a paymentId", got)
	}
	if _, leaked := got["status"]; leaked {
		t.Errorf("the acceptance carries a status: %v — the outcome is a second request", got)
	}

	// The outcome arrives from asking again, which is the shape 7b needs.
	outcome := doJSON(t, bank(h, a.pid), "GET", "/payments/"+id, "", http.StatusOK)
	if outcome["id"] != id {
		t.Fatalf("GET /payments/%s returned %v", id, outcome["id"])
	}
}

// A direct debit is submitted by the CREDITOR's bank, and the bound-bank check
// has to know that.
//
// This is the live defect the split exposed: the handler demanded the debtor be
// at the submitting bank on every submission, which is the wrong bank for every
// collection. It was invisible while one process validated both ends regardless
// of who called — the payee's bank got a 422 for its own customer's collection,
// and the payer's bank could have collected on the payee's behalf.
func TestTheCreditorsBankSubmitsADirectDebit(t *testing.T) {
	h := newServer(t, nil)
	payerBank, payeeBank, _ := threeBanks(t, h)
	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.dd"}`, http.StatusCreated)

	mandate := doJSON(t, csm(h), "POST", "/mandates", `{
		"debtor":{"participant":"`+payerBank.pid+`","account":"`+payerBank.account+`"},
		"creditor":{"participant":"`+payeeBank.pid+`","account":"`+payeeBank.account+`"},
		"maxAmount":0
	}`, http.StatusCreated)["id"].(string)

	collection := `{
		"scheme":"sepa.dd",
		"debtor":{"participant":"` + payerBank.pid + `","account":"` + payerBank.account + `","identifier":{"scheme":"IBAN","value":"` + payerBank.iban + `"}},
		"creditor":{"participant":"` + payeeBank.pid + `","account":"` + payeeBank.account + `"},
		"amount":10000,
		"mandateId":"` + mandate + `",
		"endToEndId":"collection-1"
	}`

	// The payee's bank submits it, and is the right bank to.
	doJSON(t, bank(h, payeeBank.pid), "POST", "/payments", collection, http.StatusAccepted)

	// The payer's bank submitting the same collection is refused: a bank does
	// not collect on somebody else's behalf.
	doJSON(t, bank(h, payerBank.pid), "POST", "/payments", `{
		"scheme":"sepa.dd",
		"debtor":{"participant":"`+payerBank.pid+`","account":"`+payerBank.account+`"},
		"creditor":{"participant":"`+payeeBank.pid+`","account":"`+payeeBank.account+`"},
		"amount":10000,
		"mandateId":"`+mandate+`",
		"endToEndId":"collection-2"
	}`, http.StatusUnprocessableEntity)
}

// An unknown scheme is answered as an unknown scheme, not as the wrong bank.
//
// Which bank may submit is a question only a registered scheme can answer, so
// asking it first and falling through to the push rule when nobody answers
// produces "a credit transfer is submitted by the payer's bank and a direct
// debit by the payee's" — about a scheme that is neither.
func TestAnUnknownSchemeIsRefusedAsAnUnknownScheme(t *testing.T) {
	h := newServer(t, nil)
	a, b, _ := threeBanks(t, h)

	// Debtor at the OTHER bank, so the bound-bank check would refuse this with
	// 422 if it ran first. It must not run first.
	doJSON(t, bank(h, a.pid), "POST", "/payments", `{
		"scheme":"nope",
		"debtor":{"participant":"`+b.pid+`","account":"`+b.account+`"},
		"creditor":{"participant":"`+a.pid+`","account":"`+a.account+`"},
		"amount":10000,
		"endToEndId":"no-such-scheme"
	}`, http.StatusNotFound)
}

// A bank may not submit a payment drawn on somebody else's customer.
func TestABankRefusesAnInstructionItIsNotTheDebtorFor(t *testing.T) {
	h := newServer(t, nil)
	a, b, _ := threeBanks(t, h)
	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	// Bank A's listener, asked to debit Bank B's customer.
	doJSON(t, bank(h, a.pid), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"participant":"`+b.pid+`","account":"`+b.account+`"},
		"creditor":{"participant":"`+a.pid+`","account":"`+a.account+`"},
		"amount":10000,
		"endToEndId":"not-mine-to-send"
	}`, http.StatusUnprocessableEntity)
}
