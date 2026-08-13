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
	"github.com/raphi011/cbs/node/bank"

	"github.com/raphi011/cbs/payment"
)

// The deliberate overlaps, as an allowlist rather than a tolerance, so a third
// accidental one fails.
var allowedOverlaps = []string{
	"GET /assets",
	"GET /audit",
	"GET /payments",
	"GET /payments/{payid}",
	// Same pattern, same handler family, two logs. A bank's answers about its own
	// book and the clearing house's about its own — one payment-scope trail per
	// institution, because there is no shared one left.
	"GET /payments/audit",
	"POST /payments",
}

func surfaces(t *testing.T) map[string][]string {
	t.Helper()
	s := newServer(t, nil)
	return map[string][]string{
		"central-bank":   cbapi.Routes(s.dep.CentralBank(), operator{s.dep}).Patterns(),
		"clearing-house": csmapi.Routes(s.dep.ClearingHouse(), operator{s.dep}).Patterns(),
		"bank":           bankapi.Routes(bankConsole{mustForBank(t, s, testBankBIC), s.dep}).Patterns(),
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
		// Back on the clearing house, with the path unchanged and the act changed,
		// which is why this arm is spelt out rather than falling to the default. The
		// pre-split route SETTLED: it called SettleCycle and moved reserves.
		return "clearing-house", old

	case path == "/assets":
		// On every operator; the disjointness allowlist is what records that.
		return "clearing-house", old

	case strings.HasPrefix(path, "/mandates"):
		// It is the BANK's, not the clearing house's: a bank resolves an address in
		// its own register.
		return "bank", old

	case path == "/directory":
		// It is the BANK's own, and it is narrower in two ways.
		return "bank", "GET /directory/accounts"

	default:
		// Payments, cycles, settlements, schemes.
		return "clearing-house", old
	}
}

// preSplitRoutes is the API as one server served it, captured before the split.
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
func TestTheClearingHouseLearnsSettlementFromItsOwnCycle(t *testing.T) {
	h := newServer(t, nil)
	cid := settledCycle(t, h)

	got := doJSON(t, csmSurface(h), "GET", "/cycles/"+cid, "", http.StatusOK)
	if got["status"] != "Settled" {
		t.Fatalf("cycle status = %v, want Settled", got["status"])
	}
	// And it cannot read the settlement itself, which is the boundary rather than
	// a gap: that row is the agent's record of its own act, and its id was
	// allocated in the agent's own database.
	assertStatus(t, csmSurface(h), "GET", "/settlements", "", http.StatusNotFound)
}

// TestTheCentralBankCanReadTheCycleItSettles is the other half: what the
// central bank's own console is left with.
func TestTheCentralBankCanReadTheCycleItSettles(t *testing.T) {
	h := newServer(t, nil)
	cid := settledCycle(t, h)

	// The CYCLE is read on the clearing house's console: that row is its own and
	// this institution has no cycles table.
	var cycles []api.ClearingCycleDTO
	getJSON(t, csmSurface(h), "/cycles", &cycles)
	if len(cycles) != 1 || cycles[0].ID != cid {
		t.Fatalf("the clearing house sees %v, want the one cycle %s", cycles, cid)
	}

	// Settled, and by this institution: the clearing house sent a pacs.009 and
	// this institution discharged it. A cycle sits Closed until a human
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
	carry(t, h)
	assertStatus(t, csmSurface(h), "POST", "/cycles/"+cyc+"/close", "", http.StatusOK)
	carry(t, h)
	return cyc
}

// TestABankSeesOnlyItsOwnPayments pins the narrowing the single server could
// not express.
func TestABankSeesOnlyItsOwnPayments(t *testing.T) {
	h := newServer(t, nil)
	a, b, c := threeBanks(t, h)
	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	mine := sct(t, h, a, b, "own-leg")
	theirs := sct(t, h, b, c, "not-mine")
	// Through the cut-off, because a receiving bank holds no copy of a payment
	// until the cycle carrying it has settled and the file is released.
	settle(t, h)

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
	// A BIC each: a deployment gives every member a download queue keyed by its address and
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
		// Funded AND lodged, because a cut-off settles across reserves: a bank
		// holding its customers' money as vault cash is a bank that cannot pay
		// anybody, and every fixture here that carries a payment to finality needs
		// one that can.
		fundAndLodge(t, h, pid, did, 500000)
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
	carry(t, h)
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
func TestTheCreditorsBankSubmitsADirectDebit(t *testing.T) {
	h := newServer(t, nil)
	payerBank, payeeBank, _ := threeBanks(t, h)
	doJSON(t, csmSurface(h), "POST", "/cycles", `{"scheme":"sepa.dd"}`, http.StatusCreated)

	// Recorded at the CREDITOR's bank, which on a pull is the collecting bank —
	// the same bank that submits below, and the only one that may hold the row.
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

	// The payer's bank submitting the same collection is refused: a bank does not
	// collect on somebody else's behalf.
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
// none, and there cannot be: an instruction carries an address and a name and
// has no field for a bank at all.

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
func TestEachListenerActsAsItsOwnInstitution(t *testing.T) {
	srv := newServer(t, nil)

	// The two entities that are not banks: neither has a participant. They are
	// two types, so the row is held by the question being asked of both.
	for _, e := range []struct {
		who string
		net interface{ Identity() payment.Identity }
	}{
		{"the central bank", srv.dep.CentralBank().Network()},
		{"the clearing house", srv.dep.ClearingHouse().Network()},
	} {
		if pid, ok := e.net.Identity().Participant(); ok {
			t.Errorf("%s's listener acts as member bank %q; it is not a member bank", e.who, pid)
		}
	}

	// And every bank's listener is its own bank, with the address its handlers
	// name agreeing with the identity the domain acts through.
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
const testBankBIC payment.ParticipantID = "BNKADEFFXXX"

// mustForBank binds one bank out of the deployment, failing the test if its
// database will not open. See Deployment.Bank.
func mustForBank(t *testing.T, s *server, pid payment.ParticipantID) *bank.Bank {
	t.Helper()
	b, err := s.dep.Bank(context.Background(), pid)
	if err != nil {
		t.Fatalf("binding %s's surface: %v", pid, err)
	}
	return b
}
