package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/raphi011/cbs/payment"
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
		"central-bank":   s.centralBankRouter().Patterns(),
		"clearing-house": s.clearingHouseRouter().Patterns(),
		"bank":           mustForBank(t, s, testBankBIC).bankRouter().Patterns(),
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
		if !slices.Contains(got[op], want) {
			t.Errorf("%q should have become %q on the %s, which does not serve it",
				old, want, op)
		}
	}
}

// movedTo maps a pre-split pattern to the operator that serves it now and the
// pattern it became.
//
// EVERY pattern lands somewhere, which was not true while the split was in
// progress and is not a coincidence now. This used to answer with an empty
// operator for two kinds of route — one a later task had not moved yet, and one
// that was deleted outright — and the caller skipped those. Both kinds are
// gone: the last unmoved route landed with the operator split, and
// POST /cycles/{cid}/settle is back on the clearing house with a different act
// behind the same path. So the skip is gone too, rather than left as a branch
// nothing can reach. A route that is deliberately deleted in future needs it
// back, and needs the arm that says so — with the skip, an arm returning "" is
// silent, and without it the route fails against an operator named "" that
// serves nothing. Loud is the right way round for a case nobody has decided on.
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
		// On the CENTRAL BANK's listener, beside POST /members, and both are the
		// OPERATOR's rather than that institution's — see centralBankRouter. It
		// was the clearing house's while that institution was thought to hold a
		// list of banks; the csm shape holds a roster of addresses, which is
		// exactly the list that omits the founded bank this read exists to show.
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
		// The cycles are the CLEARING HOUSE's rows and are served there alone.
		// The pre-split server had one store, so the central bank's console
		// carried the same two reads; since Task 18d that institution's database
		// has no cycles table and the routes answered a missing table rather
		// than a list. See centralBankRouter.
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
		// (mesh.csm.settle). Settling itself is still no operator's act on any
		// surface, and the intermediate POST /settlements on the central bank
		// stays deleted; TestNoRouteSettlesACycle is the pin on both halves.
		return "clearing-house", old

	case path == "/assets":
		// On every operator; the disjointness allowlist is what records that.
		return "clearing-house", old

	case strings.HasPrefix(path, "/mandates"):
		// It MOVED, and the move is the second of Task 18's two directory-shaped
		// corrections — the first being /directory below.
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
		// It MOVED, rather than staying where the pre-split server had it, and
		// the move is the whole of Task 18a's directory change.
		//
		// The one server answered "who holds this IBAN" by sweeping every bank's
		// register, which is why the route sat with payments and cycles on the
		// clearing house. No institution can answer that question any more —
		// see payment.ResolveIdentifier — so what is left is "is this address
		// one of MINE", and the only operator that can ask it is a bank. The
		// clearing house got GET /roster in its place, which is the routing
		// directory it does own; that route is not in this golden list because
		// the pre-split server had no such thing.
		return "bank", old

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
	aurora := admitMember(t, s, `{"bic":"AURODEFFXXX","name":"Aurora Bank"}`, http.StatusAccepted)["id"].(string)
	verde := admitMember(t, s, `{"bic":"VERDITMMXXX","name":"Banca Verde"}`, http.StatusAccepted)["id"].(string)

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

// TestTheClearingHouseLearnsSettlementFromItsOwnCycle is what is left of the
// operator split's settlement assertion, and it has now been renamed twice for
// two different reasons.
//
// It was TestSettlingIsTheCentralBanksAct, and it pinned that by driving POST
// /settlements on one operator and getting a 404 on the other. Neither half is
// available: no route settles, so there is no HTTP act left to attribute to
// anybody. TestNoRouteSettlesACycle is where that is pinned.
//
// It then became TestTheClearingHouseReadsTheSettlementItDidNotPerform, on the
// READ: this institution closed the cycle and sent the instruction, so it has to
// be able to find out whether the central bank discharged it. That is still
// exactly right and the ROUTE it used is gone — a settlement is the settlement
// agent's row and Task 18d gives the clearing house a database with no
// settlements table in it, so GET /settlements on this listener answered a
// missing table.
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

	got := doJSON(t, csm(h), "GET", "/cycles/"+cid, "", http.StatusOK)
	if got["status"] != "Settled" {
		t.Fatalf("cycle status = %v, want Settled", got["status"])
	}
	// And it cannot read the settlement itself, which is the boundary rather
	// than a gap: that row is the agent's record of its own act, and its id was
	// allocated in the agent's own database. See payment.ClearingCycle, which
	// carries no settlement id for the same reason.
	assertStatus(t, csm(h), "GET", "/settlements", "", http.StatusNotFound)
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
	var cycles []clearingCycleDTO
	getJSON(t, csm(h), "/cycles", &cycles)
	if len(cycles) != 1 || cycles[0].ID != cid {
		t.Fatalf("the clearing house sees %v, want the one cycle %s", cycles, cid)
	}

	// Settled, and by this institution: the clearing house sent a pacs.009 and
	// this actor discharged it. Before the mesh a cycle sat Closed until a human
	// pressed a second button on this console.
	got := doJSON(t, csm(h), "GET", "/cycles/"+cid, "", http.StatusOK)
	if got["status"] != "Settled" {
		t.Fatalf("cycle status = %v, want Settled", got["status"])
	}
	assertStatus(t, cb(h), "GET", "/cycles/"+cid, "", http.StatusNotFound)

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
	a := admitMember(t, h, `{"bic":"BNKADEFFXXX","name":"Bank A"}`, http.StatusAccepted)["id"].(string)
	b := admitMember(t, h, `{"bic":"BNKBDEFFXXX","name":"Bank B"}`, http.StatusAccepted)["id"].(string)
	alice := doJSON(t, bank(h, a), "POST", "/deposit-accounts",
		`{"name":"Alice","asset":"EUR","productId":"`+prdOf(t, h, a)+`","identifiers":[{"scheme":"IBAN","value":"SE89-SET-ALICE-0001"}]}`,
		http.StatusCreated)["id"].(string)
	bob := doJSON(t, bank(h, b), "POST", "/deposit-accounts",
		`{"name":"Bob","asset":"EUR","productId":"`+prdOf(t, h, b)+`","identifiers":[{"scheme":"IBAN","value":"SE89-SET-BOB-0001"}]}`,
		http.StatusCreated)["id"].(string)
	fundAndLodge(t, h, a, alice, 100000)

	cyc := doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)["id"].(string)
	doJSON(t, csm(h), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtorAgent":"`+a+`","debtor":{"account":"`+alice+`"},
		"creditorAgent":"`+b+`","creditor":{"account":"`+bob+`","identifier":{"scheme":"IBAN","value":"SE89-SET-BOB-0001"}},
		"amount":25000,
		"endToEndId":"settle-e2e",
		"creditorName":"Bob"
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
	pid         string
	account     string
	iban        string
	bic         string
	accountName string
}

func threeBanks(t *testing.T, h *Server) (a, b, c seededBank) {
	t.Helper()
	// A BIC each: the mesh gives every bank an actor keyed by its address and
	// refuses two on one, so three banks that shared a BIC could not be admitted
	// at all — let alone tell each other apart on the wire.
	mk := func(name, bic, iban string) seededBank {
		pid := admitMember(t, h, `{"bic":"`+bic+`","name":"`+name+`"}`, http.StatusAccepted)["id"].(string)
		accountName := name + " customer"
		did := doJSON(t, bank(h, pid), "POST", "/deposit-accounts",
			`{"name":"`+accountName+`","asset":"EUR","productId":"`+prdOf(t, h, pid)+
				`","identifiers":[{"scheme":"IBAN","value":"`+iban+`"}]}`,
			http.StatusCreated)["id"].(string)
		doJSON(t, bank(h, pid), "POST", "/deposits",
			`{"account":"`+did+`","amount":500000,"description":"opening"}`, http.StatusOK)
		return seededBank{pid: pid, account: did, iban: iban, bic: bic, accountName: accountName}
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
		"debtorAgent":"`+from.pid+`","debtor":{"account":"`+from.account+`"},
		"creditorAgent":"`+to.pid+`","creditor":{"account":"`+to.account+`","identifier":{"scheme":"IBAN","value":"`+to.iban+`"}},
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
	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	instruction := `{
		"scheme":"sepa.ct",
		"debtorAgent":"` + a.pid + `","debtor":{"account":"` + a.account + `"},
		"creditorAgent":"` + b.pid + `","creditor":{"account":"` + b.account + `","identifier":{"scheme":"IBAN","value":"` + b.iban + `"}},
		"amount":10000,
		"endToEndId":"retail-1",
		"creditorName":"` + b.accountName + `"
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

	// Recorded at the CREDITOR's bank, which on a pull is the collecting bank —
	// the same bank that submits below, and the only one that may hold the row.
	mandate := doJSON(t, bank(h, payeeBank.pid), "POST", "/mandates", `{
		"debtorAgent":"`+payerBank.pid+`","debtor":{"account":"`+payerBank.account+`"},
		"creditor":{"account":"`+payeeBank.account+`"},
		"maxAmount":0
	}`, http.StatusCreated)["id"].(string)

	collection := `{
		"scheme":"sepa.dd",
		"debtorAgent":"` + payerBank.pid + `","debtor":{"account":"` + payerBank.account + `","identifier":{"scheme":"IBAN","value":"` + payerBank.iban + `"}},
		"creditorAgent":"` + payeeBank.pid + `","creditor":{"account":"` + payeeBank.account + `"},
		"amount":10000,
		"mandateId":"` + mandate + `",
		"endToEndId":"collection-1",
		"debtorName":"` + payerBank.accountName + `"
	}`

	// The payee's bank submits it, and is the right bank to.
	doJSON(t, bank(h, payeeBank.pid), "POST", "/payments", collection, http.StatusAccepted)

	// The payer's bank submitting the same collection is refused: a bank does
	// not collect on somebody else's behalf.
	doJSON(t, bank(h, payerBank.pid), "POST", "/payments", `{
		"scheme":"sepa.dd",
		"debtorAgent":"`+payerBank.pid+`","debtor":{"account":"`+payerBank.account+`"},
		"creditorAgent":"`+payeeBank.pid+`","creditor":{"account":"`+payeeBank.account+`"},
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
		"debtorAgent":"`+b.pid+`","debtor":{"account":"`+b.account+`"},
		"creditorAgent":"`+a.pid+`","creditor":{"account":"`+a.account+`"},
		"amount":10000,
		"endToEndId":"no-such-scheme"
	}`, http.StatusNotFound)
}

// A missing participant is answered as a missing field, not as the wrong bank.
//
// An omitted participant decodes to "", which never equals a non-empty bound
// id, so it falls through the direction rule and is answered "this bank does not
// submit this payment: a credit transfer is submitted by the payer's bank and a
// direct debit by the payee's" — a diagnosis of a direction violation about a
// request that names no direction to violate. Same shape as the unregistered
// scheme above, and the same fix: ask the question that has an answer first.
//
// Both directions, because the field that is missing is not the same one: it is
// the COUNTERPARTY's agent, and which side that is is the scheme's direction —
// the creditor's on a push, the debtor's on a pull. A message that said
// "debtor" for both would be wrong for every collection.
//
// It used to be the counterparty's PARTICIPANT, a field on the party ref. Task
// 18 deleted it: a ref names no bank any more, because a bank's id became its
// BIC and the agent beside the ref already carried it (see payment.PartyRef).
// The refusal is the same refusal about the value that survived.
func TestAnInstructionWithNoParticipantIsRefusedAsAMissingField(t *testing.T) {
	h := newServer(t, nil)
	a, b, _ := threeBanks(t, h)
	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.dd"}`, http.StatusCreated)

	push := do(t, bank(h, a.pid), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+a.account+`"},
		"creditor":{"account":"`+b.account+`","identifier":{"scheme":"IBAN","value":"`+b.iban+`"}},
		"creditorName":"Bob",
		"amount":10000
	}`)
	if push.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a push naming no creditor agent = %d, want 422 (%s)", push.Code, push.Body.String())
	}
	if !strings.Contains(push.Body.String(), "usable BIC for the counterparty's bank") {
		t.Fatalf("a push naming no creditor agent was refused as %q", push.Body.String())
	}

	mandate := doJSON(t, bank(h, b.pid), "POST", "/mandates", `{
		"debtorAgent":"`+a.pid+`","debtor":{"account":"`+a.account+`"},
		"creditor":{"account":"`+b.account+`"},
		"maxAmount":0
	}`, http.StatusCreated)["id"].(string)

	pull := do(t, bank(h, b.pid), "POST", "/payments", `{
		"scheme":"sepa.dd",
		"debtor":{"account":"`+a.account+`","identifier":{"scheme":"IBAN","value":"`+a.iban+`"}},
		"debtorName":"Alice",
		"creditor":{"account":"`+b.account+`"},
		"amount":10000,
		"mandateId":"`+mandate+`"
	}`)
	if pull.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a collection naming no debtor agent = %d, want 422 (%s)", pull.Code, pull.Body.String())
	}
	if !strings.Contains(pull.Body.String(), "usable BIC for the counterparty's bank") {
		t.Fatalf("a collection naming no debtor agent was refused as %q", pull.Body.String())
	}
}

// A bank may not submit a payment drawn on somebody else's customer.
func TestABankRefusesAnInstructionItIsNotTheDebtorFor(t *testing.T) {
	h := newServer(t, nil)
	a, b, _ := threeBanks(t, h)
	doJSON(t, csm(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	// Bank A's listener, asked to debit Bank B's customer.
	doJSON(t, bank(h, a.pid), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtorAgent":"`+b.pid+`","debtor":{"account":"`+b.account+`"},
		"creditorAgent":"`+a.pid+`","creditor":{"account":"`+a.account+`"},
		"amount":10000,
		"endToEndId":"not-mine-to-send"
	}`, http.StatusUnprocessableEntity)
}

// TestEachListenerActsAsItsOwnInstitution is the direct statement of the ruling
// Task 18b's api half turns on, and the reason that half exists at all.
//
// Until this task api held ONE *payment.Network for the whole process and told
// its bank listeners apart with boundPID alone. That was correct exactly while a
// bank's identity travelled as an argument: two ports, one object, two
// participants passed in. The moment payment.Network gained an identity it
// stopped being correct, because one shared object has one register — and
// GET /directory on two banks' ports would then resolve in the same one, which
// is the crossing Task 18a closed reappearing one layer up, where the recorder
// in mesh/books_test.go cannot see it because api is not an actor.
//
// So each surface binds its own institution. This reads that back off the
// Servers the three surface methods build, rather than off a response, because
// the response is the consequence and this is the cause;
// TestDirectoryDoesNotAnswerForAnotherBanksCustomer is the consequence, and it
// is what fails if forBank hands two banks one network.
func TestEachListenerActsAsItsOwnInstitution(t *testing.T) {
	srv := newServer(t, nil)

	// The two entities that are not banks: neither has a participant.
	for _, e := range []struct {
		who string
		srv *Server
	}{
		{"the central bank", srv.as(srv.nets.CentralBank())},
		{"the clearing house", srv.as(srv.nets.ClearingHouse())},
	} {
		if pid, ok := e.srv.network().Identity().Participant(); ok {
			t.Errorf("%s's listener acts as member bank %q; it is not a member bank", e.who, pid)
		}
	}

	// And every bank's listener is its own bank, with boundPID agreeing. The two
	// are set from one value in forBank and this is what says they cannot drift:
	// boundPID is what the handlers name in URLs and DTOs, the identity is what
	// the domain acts through, and a listener whose two disagreed would answer
	// about one bank out of another's register.
	for _, pid := range []payment.ParticipantID{"BNKADEFFXXX", "BNKBDEFFXXX", "BNKCDEFFXXX"} {
		b := mustForBank(t, srv, pid)
		got, ok := b.network().Identity().Participant()
		if !ok {
			t.Fatalf("the listener for %s does not act as a member bank at all", pid)
		}
		if got != pid {
			t.Errorf("the listener for %s acts as %s in the domain", pid, got)
		}
		if b.boundPID != pid {
			t.Errorf("the listener for %s names %s in its handlers", pid, b.boundPID)
		}
	}
}

// testBankBIC is the address the surface tests bind a bank's listener to.
//
// It is a BIC and no longer "bank_1", because a bank's ParticipantID IS its BIC
// (payment.AsBank) and binding a listener now opens the database named by it.
// Nothing about these tests depends on the bank existing — a router's patterns
// are the same whether the database behind it holds a bank row or not — but the
// value has to be a well-formed address, because that is what names a file.
const testBankBIC payment.ParticipantID = "BNKADEFFXXX"

// mustForBank binds a bank's surface, failing the test if its database will not
// open. See api.Server.forBank.
func mustForBank(t *testing.T, s *Server, pid payment.ParticipantID) *Server {
	t.Helper()
	b, err := s.forBank(context.Background(), pid)
	if err != nil {
		t.Fatalf("binding %s's surface: %v", pid, err)
	}
	return b
}
