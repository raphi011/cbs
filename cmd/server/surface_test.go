package main

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/raphi011/cbs/api"
	bankapi "github.com/raphi011/cbs/api/bank"
	cbapi "github.com/raphi011/cbs/api/centralbank"
	csmapi "github.com/raphi011/cbs/api/csm"
	"github.com/raphi011/cbs/iso20022"

	"github.com/raphi011/cbs/payment"
)

// The deliberate overlaps, as an allowlist rather than a tolerance, so a third
// accidental one fails.
//
//   - GET /assets is a compiled-in constant every operator needs to render money
//     at the right scale. Duplicating a constant is not duplicating state.
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
	"GET /audit",
	"GET /payments",
	"GET /payments/{payid}",
	// Same pattern, same handler family, two logs. A bank's answers about its
	// own book and the clearing house's about its own — one payment-scope trail
	// per institution, because there is no shared one left. See
	// handleBankPaymentAudit.
	"GET /payments/audit",
	"POST /payments",
}

func surfaces(t *testing.T) map[string][]string {
	t.Helper()
	s := newServer(t, nil)
	return map[string][]string{
		"central-bank":   cbapi.Routes(s.dep.CentralBank()).Patterns(),
		"clearing-house": csmapi.Routes(s.dep.ClearingHouse()).Patterns(),
		"bank":           bankapi.Routes(mustForBank(t, s, testBankBIC)).Patterns(),
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
		if op == routeDeleted {
			continue
		}
		if !slices.Contains(got[op], want) {
			t.Errorf("%q should have become %q on the %s, which does not serve it",
				old, want, op)
		}
	}
}

// movedTo maps a pre-split pattern to the operator that serves it now and the
// pattern it became.
//
// EVERY pattern lands somewhere, and no arm returns "". A route deliberately
// deleted in future needs an arm that says so: an empty operator would be silent,
// where a route with no arm fails loudly against an operator named "" that serves
// nothing. Loud is the right way round for a case nobody has decided on.
//
// Returning the operator as well as the pattern matters: a bank's audit log and
// the central bank's both become "GET /audit", so a flat set of landed patterns
// would let either one mask the other's absence.
// routeDeleted is the operator a pre-split route lands on when it is GONE rather
// than moved, and it is spelled rather than left empty for the reason above: an
// empty operator is what a route nobody has decided on produces, and a deletion
// somebody chose must not look like one.
const routeDeleted = "deleted"

func movedTo(old string) (operator, pattern string) {
	method, path, _ := strings.Cut(old, " ")
	switch {
	case path == "/participants" && method == "POST":
		// Deleted rather than moved. Which banks exist is a deployment's to
		// decide, and no request adds one: a bank with rows and no listener
		// could not be reached whatever a route answered. See provision.Bank.
		return routeDeleted, ""
	case path == "/participants" && method == "GET":
		// On the CENTRAL BANK's listener, and the OPERATOR's rather than that
		// institution's — see centralBankRouter. Not the clearing house's: the
		// csm shape holds a roster of addresses and no banks table at all.
		return "central-bank", "GET /members"
	case path == "/participants/{pid}":
		return "bank", "GET /me"
	case strings.HasPrefix(path, "/participants/{pid}/"):
		return "bank", method + " /" + strings.TrimPrefix(path, "/participants/{pid}/")

	case path == "/central-bank/reserves/{pid}":
		// The path parameter is a BIC now and not a bank id, which is what the
		// settlement agent's own records are keyed by and the only name for a
		// bank it is ever told. See handleGetReserve.
		return "central-bank", "GET /reserves/{bic}"
	case strings.HasPrefix(path, "/central-bank/"):
		return "central-bank", method + " /" + strings.TrimPrefix(path, "/central-bank/")

	case path == "/cycles" || path == "/cycles/{cid}":
		// The cycles are the CLEARING HOUSE's rows and are served there alone. The
		// central bank's database has no cycles table, so a route it carried would
		// answer a missing table rather than a list. See centralBankRouter.
		return "clearing-house", old
	case path == "/settlements" || path == "/settlements/{sid}":
		// And the mirror of it: a settlement is the SETTLEMENT AGENT's row, so
		// these two moved off the clearing house's surface and onto its own.
		return "central-bank", old
	case path == "/admin/reset":
		return "central-bank", old

	case path == "/cycles/{cid}/settle":
		// Back on the clearing house, with the path unchanged and the act
		// changed, which is why this arm is spelt out rather than falling to the
		// default. The pre-split route SETTLED: it called SettleCycle and moved
		// reserves. This one instructs — it re-sends the pacs.009 for a cycle
		// the central bank refused, because otherwise a refusal is terminal
		// (csm.settle). Settling itself is still no operator's act on any
		// surface, and the intermediate POST /settlements on the central bank
		// stays deleted; TestNoRouteSettlesACycle is the pin on both halves.
		return "clearing-house", old

	case path == "/assets":
		// On every operator; the disjointness allowlist is what records that.
		return "clearing-house", old

	case strings.HasPrefix(path, "/mandates"):
		// It is the BANK's, not the clearing house's: a bank resolves an address in
		// its own register.
		//
		// The one server put mandates with payments and cycles, on the reading
		// that a mandate is network infrastructure. It is not: in SEPA the
		// CREDITOR holds the mandate, payment.SDD.ValidateMandate has said so
		// since the pull flow landed, and the bank that checks one at submission
		// is the creditor's. What the clearing house held was every member's
		// authorisations over every other member's customers' accounts on one
		// page — and rendering each row meant loading the DEBTOR's bank and
		// listing its deposit register for the asset, one institution reading
		// another's over HTTP. The `csm` shape has no deposit register, so that
		// read had no answer coming.
		//
		// The listing is narrowed as well as moved: a bank sees the mandates
		// whose creditor is its own customer and no others
		// (payment.Network.ListMandates), so this is not the same page on a
		// different port.
		return "bank", old

	case path == "/directory":
		// It is the BANK's own, and it is narrower in two ways.
		//
		// The one server answered "who holds this IBAN" by sweeping every bank's
		// register, which is why the route sat with payments and cycles on the
		// clearing house. No institution can answer that question any more — see
		// payment.ResolveIdentifier — so what is left is "is this address one of
		// MINE", and the only operator that can ask it is a bank.
		//
		// And the PATH moved, because "directory" stopped being one question the
		// moment a second directory arrived: a bank now also holds a copy of the
		// scheme's routing directory, answering which INSTITUTION a bank code
		// belongs to, on GET /directory/banks. Neither of those routes is in this
		// golden list, along with GET /roster, because the pre-split server had no
		// such thing.
		return "bank", "GET /directory/accounts"

	default:
		// Payments, cycles, settlements, schemes.
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
	"GET /participants/{pid}/accounts/{aid}/subsidiaries",
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
	aurora := provisionMember(t, s, "AURODEFFXXX", "Aurora Bank")
	verde := provisionMember(t, s, "VERDITMMXXX", "Banca Verde")

	h := bankSurface(s, aurora)
	assertStatus(t, h, "GET", "/participants/"+verde+"/deposit-accounts", "", http.StatusNotFound)
	assertStatus(t, h, "GET", "/participants/"+aurora+"/deposit-accounts", "", http.StatusNotFound)

	// And its own list is reached by asking for it, with no id anywhere.
	var mine []api.DepositAccountDTO
	getJSON(t, h, "/deposit-accounts", &mine)

	// The bank a listener is bound to is the bank it answers as.
	got := doJSON(t, h, "GET", "/me", "", http.StatusOK)
	if got["id"] != aurora {
		t.Fatalf("GET /me on Aurora's listener = %v, want %s", got["id"], aurora)
	}
	got = doJSON(t, bankSurface(s, verde), "GET", "/me", "", http.StatusOK)
	if got["id"] != verde {
		t.Fatalf("GET /me on Verde's listener = %v, want %s", got["id"], verde)
	}
}

// TestTheClearingHouseLearnsSettlementFromItsOwnCycle is what is left of the
// operator split's settlement assertion, and it has now been renamed twice for
// two different reasons.
//
// It was TestSettlingIsTheCentralBanksAct, and it pinned that by driving POST
// /settlements on one operator and getting a 404 on the other. Neither half is
// available: no route settles, so there is no HTTP act left to attribute to
// anybody. TestNoRouteSettlesACycle is where that is pinned.
//
// TestTheClearingHouseReadsTheSettlementItDidNotPerform is about the READ: this
// institution closed the cycle and sent the instruction, so it has to be able to
// find out whether the central bank discharged it. It reads it at the SETTLEMENT
// AGENT, because the clearing house's database has no settlements table.
//
// What it finds out with instead is its OWN cycle, whose status the agent's ACSC
// moved. That is a better shape than the borrowed read was: the clearing house
// learns the outcome from the message it was sent and records it on the row it
// owns, rather than reaching into another institution's book to look. The
// settlement's own row stays where it was written, on the agent's console —
// TestTheCentralBankCanReadTheCycleItSettles is that half.
func TestTheClearingHouseLearnsSettlementFromItsOwnCycle(t *testing.T) {
	h := newServer(t, nil)
	cid := settledCycle(t, h)

	got := doJSON(t, csmSurface(h), "GET", "/cycles/"+cid, "", http.StatusOK)
	if got["status"] != "Settled" {
		t.Fatalf("cycle status = %v, want Settled", got["status"])
	}
	// And it cannot read the settlement itself, which is the boundary rather
	// than a gap: that row is the agent's record of its own act, and its id was
	// allocated in the agent's own database. See payment.ClearingCycle, which
	// carries no settlement id for the same reason.
	assertStatus(t, csmSurface(h), "GET", "/settlements", "", http.StatusNotFound)
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

	// The CYCLE is read on the clearing house's console: that row is its own and
	// this institution has no cycles table. What the central bank's operator
	// finds a refused instruction with is a cut-off with NO SETTLEMENT of its
	// own against it, which is the read below.
	var cycles []api.ClearingCycleDTO
	getJSON(t, csmSurface(h), "/cycles", &cycles)
	if len(cycles) != 1 || cycles[0].ID != cid {
		t.Fatalf("the clearing house sees %v, want the one cycle %s", cycles, cid)
	}

	// Settled, and by this institution: the clearing house sent a pacs.009 and
	// this actor discharged it. Before the mesh a cycle sat Closed until a human
	// pressed a second button on this console.
	got := doJSON(t, csmSurface(h), "GET", "/cycles/"+cid, "", http.StatusOK)
	if got["status"] != "Settled" {
		t.Fatalf("cycle status = %v, want Settled", got["status"])
	}
	assertStatus(t, cbSurface(h), "GET", "/cycles/"+cid, "", http.StatusNotFound)

	sid := settlementOfCycle(t, h, cid)

	// And it can read back what it did, without asking the clearing house.
	var settlements []api.SettlementDTO
	getJSON(t, cbSurface(h), "/settlements", &settlements)
	if len(settlements) != 1 {
		t.Fatalf("the central bank sees %d settlements, want 1", len(settlements))
	}
	doJSON(t, cbSurface(h), "GET", "/settlements/"+sid, "", http.StatusOK)

	// The boundary that matters is untouched: an individual payment is still
	// the clearing house's, and a central bank does not see one.
	assertStatus(t, cbSurface(h), "GET", "/payments", "", http.StatusNotFound)
}

// settledCycle builds the smallest thing a settlement can come out of: two
// banks, one funded payment between them, cleared into a cycle that is then
// closed — and, because the cut-off is what instructs settlement, discharged by
// the central bank once the conversation has finished.
//
// After the drain below this cycle is Settled: the cut-off sends the pacs.009,
// and there is no second console button that discharges it.
func settledCycle(t *testing.T, h *server) string {
	t.Helper()
	a := provisionMember(t, h, "BNKADEFFXXX", "Bank A")
	b := provisionMember(t, h, "BNKBDEFFXXX", "Bank B")
	alice := doJSON(t, bankSurface(h, a), "POST", "/deposit-accounts",
		`{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`"}`,
		http.StatusCreated)["id"].(string)
	bob := doJSON(t, bankSurface(h, b), "POST", "/deposit-accounts",
		`{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`"}`,
		http.StatusCreated)["id"].(string)
	fundAndLodge(t, h, a, alice, 100000)

	cyc := doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)["id"].(string)
	// Both sides quote an ADDRESS and neither names a bank. This is the clearing
	// house's console, which is no bank, so it reads the submitting bank out of the
	// payer's address and the payer's bank derives the payee's from theirs.
	doJSON(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+alice+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, a, alice)+`"}},
		"creditor":{"account":"`+bob+`","identifier":{"scheme":"IBAN","value":"`+ibanFor(t, h, b, bob)+`"}},
		"amount":25000,
		"endToEndId":"settle-e2e",
		"creditorName":"Bob"
	}`, http.StatusAccepted)
	drainServer(t, h)
	assertStatus(t, csmSurface(h), "POST", "/cycles/"+cyc+"/close", "", http.StatusOK)
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
	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	mine := sct(t, h, a, b, "own-leg")
	theirs := sct(t, h, b, c, "not-mine")

	// Aurora is the debtor on one and a party to no other.
	var list []api.PaymentDTO
	getJSON(t, bankSurface(h, a.pid), "/payments", &list)
	if len(list) != 1 || list[0].ID != mine {
		t.Fatalf("bank A sees %d payments (%+v), want only its own leg %s", len(list), list, mine)
	}

	doJSON(t, bankSurface(h, a.pid), "GET", "/payments/"+mine, "", http.StatusOK)

	// 404 and not 403: a payment this bank is not party to does not exist as
	// far as its API is concerned, and a 403 would confirm that the id names
	// something real.
	doJSON(t, bankSurface(h, a.pid), "GET", "/payments/"+theirs, "", http.StatusNotFound)

	// The creditor sees it too — "its own" is either leg, not just the debit.
	var bList []api.PaymentDTO
	getJSON(t, bankSurface(h, b.pid), "/payments", &bList)
	if len(bList) != 2 {
		t.Fatalf("bank B is a party to both payments but sees %d", len(bList))
	}

	// The clearing house is the CSM. Seeing every payment is its job, not a leak.
	var all []api.PaymentDTO
	getJSON(t, csmSurface(h), "/payments", &all)
	if len(all) != 2 {
		t.Fatalf("the clearing house sees %d payments, want both", len(all))
	}
}

// seededBank is a bank plus one funded, IBAN-addressed customer account — the
// least a bank needs to be either end of an SCT.
type seededBank struct {
	pid         string
	account     string
	iban        string
	bic         string
	accountName string
}

func threeBanks(t *testing.T, h *server) (a, b, c seededBank) {
	t.Helper()
	// A BIC each: the mesh gives every bank an actor keyed by its address and
	// refuses two on one, so three banks that shared a BIC could not be admitted
	// at all — let alone tell each other apart on the wire.
	mk := func(name, bic string) seededBank {
		pid := provisionMember(t, h, bic, name)
		accountName := name + " customer"
		did := doJSON(t, bankSurface(h, pid), "POST", "/deposit-accounts",
			`{"name":"`+accountName+`","asset":"EUR","productId":"`+prdOf(t, h, pid)+
				`"}`,
			http.StatusCreated)["id"].(string)
		// Read back rather than chosen: the bank minted it.
		iban := ibanFor(t, h, pid, did)
		doJSON(t, bankSurface(h, pid), "POST", "/deposits",
			`{"account":"`+did+`","amount":500000,"description":"opening"}`, http.StatusOK)
		return seededBank{pid: pid, account: did, iban: iban, bic: bic, accountName: accountName}
	}
	return mk("Bank A", "BNKADEFFXXX"),
		mk("Bank B", "BNKBDEFFXXX"),
		mk("Bank C", "BNKCDEFFXXX")
}

// sct initiates a credit transfer between two seeded banks and returns its id.
func sct(t *testing.T, h *server, from, to seededBank, e2e string) string {
	t.Helper()
	id := doJSON(t, csmSurface(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+from.account+`","identifier":{"scheme":"IBAN","value":"`+from.iban+`"}},
		"creditor":{"account":"`+to.account+`","identifier":{"scheme":"IBAN","value":"`+to.iban+`"}},
		"amount":10000,
		"endToEndId":"`+e2e+`",
		"creditorName":"`+to.accountName+`"
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
	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	instruction := `{
		"scheme":"sepa.ct",
		"debtor":{"account":"` + a.account + `"},
		"creditor":{"account":"` + b.account + `","identifier":{"scheme":"IBAN","value":"` + b.iban + `"}},
		"amount":10000,
		"endToEndId":"retail-1",
		"creditorName":"` + b.accountName + `"
	}`

	got := doJSON(t, bankSurface(h, a.pid), "POST", "/payments", instruction, http.StatusAccepted)
	id, ok := got["paymentId"].(string)
	if !ok || id == "" {
		t.Fatalf("response = %v, want a paymentId", got)
	}
	if _, leaked := got["status"]; leaked {
		t.Errorf("the acceptance carries a status: %v — the outcome is a second request", got)
	}

	// The outcome arrives from asking again, which is the shape 7b needs.
	outcome := doJSON(t, bankSurface(h, a.pid), "GET", "/payments/"+id, "", http.StatusOK)
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
	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.dd"}`, http.StatusCreated)

	// Recorded at the CREDITOR's bank, which on a pull is the collecting bank —
	// the same bank that submits below, and the only one that may hold the row.
	// The mandate names no bank either: the debtor's is derived from the debtor's
	// address, once, at signature. See api.CreateMandateRequest.
	mandate := doJSON(t, bankSurface(h, payeeBank.pid), "POST", "/mandates", `{
		"debtor":{"account":"`+payerBank.account+`","identifier":{"scheme":"IBAN","value":"`+payerBank.iban+`"}},
		"creditor":{"account":"`+payeeBank.account+`"},
		"maxAmount":0
	}`, http.StatusCreated)["id"].(string)

	collection := `{
		"scheme":"sepa.dd",
		"debtor":{"account":"` + payerBank.account + `","identifier":{"scheme":"IBAN","value":"` + payerBank.iban + `"}},
		"creditor":{"account":"` + payeeBank.account + `"},
		"amount":10000,
		"mandateId":"` + mandate + `",
		"endToEndId":"collection-1",
		"debtorName":"` + payerBank.accountName + `"
	}`

	// The payee's bank submits it, and is the right bank to.
	doJSON(t, bankSurface(h, payeeBank.pid), "POST", "/payments", collection, http.StatusAccepted)

	// The payer's bank submitting the same collection is refused: a bank does
	// not collect on somebody else's behalf.
	//
	// It is refused by the DOMAIN rather than by the door. The port fills in the
	// submitting agent, so this listener claims to be the collecting bank; the
	// creditor account it then has to resolve is the payee bank's, in a register
	// this bank does not hold, and that is where it stops.
	doJSON(t, bankSurface(h, payerBank.pid), "POST", "/payments", `{
		"scheme":"sepa.dd",
		"debtor":{"account":"`+payerBank.account+`","identifier":{"scheme":"IBAN","value":"`+payerBank.iban+`"}},
		"creditor":{"account":"`+payeeBank.account+`"},
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

	// Debtor at the OTHER bank, so the account resolution would refuse this with
	// 422 if it ran first. It must not run first.
	doJSON(t, bankSurface(h, a.pid), "POST", "/payments", `{
		"scheme":"nope",
		"debtor":{"account":"`+b.account+`"},
		"creditor":{"account":"`+a.account+`","identifier":{"scheme":"IBAN","value":"`+a.iban+`"}},
		"amount":10000,
		"endToEndId":"no-such-scheme"
	}`, http.StatusNotFound)
}

// There is no case here for an instruction that names the wrong bank, or names
// none, and there cannot be: an instruction carries an address and a name and has
// no field for a bank at all. The submitting side comes from the port, the
// counterparty's is derived from their address, and a request cannot disagree
// with either.
//
// What is left of "this address names nowhere to send it" is three refusals with
// three remedies, and they are pinned together on the payer's own port —
// TestPostPaymentRefusesEachWayAnAddressFails in server_test.go.

// A bank may not submit a payment drawn on somebody else's customer.
func TestABankRefusesAnInstructionItIsNotTheDebtorFor(t *testing.T) {
	h := newServer(t, nil)
	a, b, _ := threeBanks(t, h)
	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	// Bank A's listener, asked to debit Bank B's customer.
	doJSON(t, bankSurface(h, a.pid), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+b.account+`"},
		"creditor":{"account":"`+a.account+`","identifier":{"scheme":"IBAN","value":"`+a.iban+`"}},
		"amount":10000,
		"endToEndId":"not-mine-to-send"
	}`, http.StatusUnprocessableEntity)
}

// TestEachListenerActsAsItsOwnInstitution is the direct statement of the ruling
// the per-institution networks turn on.
//
// One shared *payment.Network has one register, so GET /directory/accounts on
// two banks' ports would resolve in the same one — one bank reading another
// bank's customers, one layer above where the recorder in books_test.go can
// see it, because this layer is not an actor.
//
// So each institution binds its own network. This reads that back off the three
// values a deployment hands out, rather than off a response, because the response
// is the consequence and this is the cause;
// TestDirectoryDoesNotAnswerForAnotherBanksCustomer is the consequence, and it is
// what fails if a deployment hands two banks one network.
func TestEachListenerActsAsItsOwnInstitution(t *testing.T) {
	srv := newServer(t, nil)

	// The two entities that are not banks: neither has a participant.
	for _, e := range []struct {
		who string
		net *payment.Network
	}{
		{"the central bank", srv.dep.CentralBank().Network()},
		{"the clearing house", srv.dep.ClearingHouse().Network()},
	} {
		if pid, ok := e.net.Identity().Participant(); ok {
			t.Errorf("%s's listener acts as member bank %q; it is not a member bank", e.who, pid)
		}
	}

	// And every bank's listener is its own bank, with the address its handlers
	// name agreeing with the identity the domain acts through. The two come off
	// one value now — a bank's ParticipantID IS its BIC — and this is what says
	// the network behind a surface is the one that value names: a listener whose
	// two disagreed would answer about one bank out of another's register.
	for _, pid := range []payment.ParticipantID{"BNKADEFFXXX", "BNKBDEFFXXX", "BNKCDEFFXXX"} {
		b := mustForBank(t, srv, pid)
		got, ok := b.Network().Identity().Participant()
		if !ok {
			t.Fatalf("the listener for %s does not act as a member bank at all", pid)
		}
		if got != pid {
			t.Errorf("the listener for %s acts as %s in the domain", pid, got)
		}
		if b.BIC() != iso20022.BIC(pid) {
			t.Errorf("the listener for %s names %s in its handlers", pid, b.BIC())
		}
	}
}

// testBankBIC is the address the surface tests bind a bank's listener to.
//
// It is a BIC, because a bank's ParticipantID IS its BIC (payment.AsBank) and
// binding a listener opens the database named by it. Nothing about these tests
// depends on the bank existing — a router's patterns are the same whether the
// database behind it holds a bank row or not — but the value has to be a
// well-formed address, because that is what names a file.
const testBankBIC payment.ParticipantID = "BNKADEFFXXX"

// mustForBank binds one bank out of the deployment, failing the test if its
// database will not open. See Deployment.Bank.
func mustForBank(t *testing.T, s *server, pid payment.ParticipantID) *Bank {
	t.Helper()
	b, err := s.dep.Bank(context.Background(), pid)
	if err != nil {
		t.Fatalf("binding %s's surface: %v", pid, err)
	}
	return b
}
