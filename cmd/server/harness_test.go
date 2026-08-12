package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/raphi011/cbs/calendar"
	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/provision"
	"github.com/raphi011/cbs/store/storetest"
	"github.com/raphi011/cbs/store/testenv"
)

// The harness every flow test in this package is written against.

// harnessAmount is what every transfer in this package moves, and harnessFunding
// is what the payer starts with. Funding is larger, so the payer is good for the
// money and a rejection is never confused with a refusal for lack of funds.
const (
	harnessAmount  ledger.Amount = 250_000
	harnessFunding ledger.Amount = 500_000
)

// The addresses this fixture's customers hold are not written down: a bank
// mints them out of the bank code it was allocated, so the only place one
// exists is on the account.

// unknownIBANAt is an address in one bank's own range that the bank does not
// hold: a serial far above anything a fixture opens, under that bank's
// allocated code.
func unknownIBANAt(p *payment.Bank) string {
	return mustMint(p.Issuer.Country, p.Issuer.BankCode, 999_999)
}

func mustMint(c iban.Country, code iban.BankCode, serial uint64) string {
	a, err := iban.New(c, code, serial)
	if err != nil {
		panic(err)
	}
	return string(a)
}

// tappedMessage is one message an actor received: who from, who to, and the
// bytes.
type tappedMessage struct {
	from iso20022.BIC
	to   iso20022.BIC
	raw  []byte
}

// harness is a seeded two-bank network with a deployment running over it, and
// the two hosts really listening.
type harness struct {
	rec *recordingStores

	// nets mints one payment.Network per institution, and net is the CLEARING
	// HOUSE's.
	nets *payment.Networks
	net  *payment.ClearingHouseNetwork

	// clock is the deployment's business date, and it MOVES: day advances it,
	// and every row written afterwards carries the new date. The frozen clock
	// this replaces could not tell an accrual from a no-op.
	clock *calendar.Clock

	dep *Deployment
	cfg Config

	debtor   *payment.Bank
	creditor *payment.Bank
	// The addresses the fixture's customers were minted, read off their accounts
	// as they were opened. A test quoting one on an instruction reads it from
	// here; nothing can write one down in advance.
	debtorIBAN      string
	creditorIBAN    string
	debtorUSDIBAN   string
	creditorUSDIBAN string

	debtorAcct   deposit.Account
	creditorAcct deposit.Account

	debtorBIC    iso20022.BIC
	creditorBIC  iso20022.BIC
	debtorPID    payment.ParticipantID
	creditorPID  payment.ParticipantID
	debtorBook   ledger.BookID
	creditorBook ledger.BookID

	// The same two customers' dollar accounts. Zero values unless the fixture
	// was built by newHarnessWithTwoAssets, which is the only one that opens
	// them; submitCreditTransferInUSD is the only thing that reads them.
	debtorUSDAcct   deposit.Account
	creditorUSDAcct deposit.Account

	// mandate is the creditor's authority to collect from the debtor's account.
	mandate payment.Mandate

	mu   sync.Mutex
	seen []tappedMessage

	// lodgeSeq numbers the fixture's lodgements, because LodgeReservesTx keys its
	// posting on the message id and a bank may be funded more than once. See
	// newHarnessWithASecondDepositor.
	lodgeSeq int
}

// harnessOptions is what the named constructors below vary, and nothing else
// does. It is unexported and passed by value so that a variation is a field
// with a name rather than a positional bool nobody can read at the call site.
type harnessOptions struct {
	// openCycles opens a cut-off window for BOTH schemes. Both, because a
	// fixture that cleared credit transfers and not collections would answer
	// TM01 to every direct debit for a reason the test did not ask for.
	openCycles bool
	// fundTheDebtor pays the opening deposit in. Withheld, the debtor's account
	// exists, is addressable and is denominated in euro, and has nothing in it — the
	// condition that produces AM04 from the debtor's own BANK, on its funds check.
	fundTheDebtor bool
	// revokeMandate revokes the mandate after creating it. The mandate still
	// EXISTS, so the collection is refused for being unauthorised rather than
	// for naming a mandate this network has never heard of.
	revokeMandate bool
	// lendToTheDebtor opens the payer's account with an overdraft limit instead of
	// paying an opening deposit in, which is what leaves the payer's BANK without
	// reserves while the payer can still pay.
	lendToTheDebtor bool
	// fundTheDebtorsBank gives the payer's bank reserves without giving the payer
	// a balance, by depositing and lodging a SECOND customer's money.
	fundTheDebtorsBank bool
	// twoAssets gives both banks and both customers a dollar side beside the
	// euro one, and registers a dollar push scheme with a cut-off window of its
	// own. See newHarnessWithTwoAssets.
	twoAssets bool
}

// newHarness builds the network and opens a clearing cycle for each scheme.
func newHarness(t *testing.T) *harness {
	t.Helper()
	return build(t, harnessOptions{openCycles: true, fundTheDebtor: true})
}

// newHarnessWithNoOpenCycle is the same network with no cut-off window open.
func newHarnessWithNoOpenCycle(t *testing.T) *harness {
	t.Helper()
	return build(t, harnessOptions{fundTheDebtor: true})
}

// newHarnessWithAnUnfundedDebtor is the same network with the payer's own
// account left empty and the payer's BANK able to settle anyway.
func newHarnessWithAnUnfundedDebtor(t *testing.T) *harness {
	t.Helper()
	return build(t, harnessOptions{openCycles: true, fundTheDebtorsBank: true})
}

// newHarnessWithASecondDepositor is the same network with another customer's
// money on the payer's bank's reserve beside its payer's.
func newHarnessWithASecondDepositor(t *testing.T) *harness {
	t.Helper()
	return build(t, harnessOptions{openCycles: true, fundTheDebtor: true, fundTheDebtorsBank: true})
}

// newHarnessWithARevokedMandate is the same network with the debtor's authority
// withdrawn.
func newHarnessWithARevokedMandate(t *testing.T) *harness {
	t.Helper()
	return build(t, harnessOptions{openCycles: true, fundTheDebtor: true, revokeMandate: true})
}

// newHarnessWithAnUnfundedReserve is the same network with the payer's bank
// holding no central-bank reserves.
func newHarnessWithAnUnfundedReserve(t *testing.T) *harness {
	t.Helper()
	return build(t, harnessOptions{openCycles: true, lendToTheDebtor: true})
}

// newHarnessWithTwoAssets is the same network operating in dollars as well as
// euro: both banks hold both, both customers have an account in each, and there
// is a dollar push scheme with a cut-off window of its own.
func newHarnessWithTwoAssets(t *testing.T) *harness {
	t.Helper()
	return build(t, harnessOptions{openCycles: true, fundTheDebtor: true, twoAssets: true})
}

// schemeUSDCT and usdCT are a second push scheme, identical to SEPA credit
// transfer except in the unit it settles in. A scheme rather than a flag on the
// payment, because that is where this system puts the asset.
const schemeUSDCT payment.SchemeID = "usd.ct"

type usdCT struct{ payment.SCT }

func (usdCT) ID() payment.SchemeID    { return schemeUSDCT }
func (usdCT) Asset() ledger.AssetCode { return "USD" }

// euroAndDollar is the asset set a bank in the two-asset fixture joins with.
// Founding gives it a suspense and a reserve account in each, and the admission
// asks the central bank for a settlement account in each — one request per asset.
var euroAndDollar = []ledger.AssetCode{"EUR", "USD"}

// bank is one member's own network, keyed by its ADDRESS. cb is the settlement
// agent's.
func (h *harness) bank(bic iso20022.BIC) *payment.BankNetwork {
	net, err := h.nets.Bank(context.Background(), payment.ParticipantID(bic))
	if err != nil {
		panic("harness_test: opening " + string(bic) + "'s store: " + err.Error())
	}
	return net
}
func (h *harness) cb() *payment.CentralBankNetwork { return h.nets.CentralBank() }

// cbBook is the central bank's book of accounts. Network.CentralBank returns an
// error — every other institution's network has no such book — and here it
// cannot fire, so the assertion is what says so.
func (h *harness) cbBook(t *testing.T) *ledger.Book {
	t.Helper()
	book, err := h.cb().CentralBank()
	if err != nil {
		t.Fatalf("the central bank's book: %v", err)
	}
	return book
}

// build assembles the fixture: two hosts really listening, a deployment over
// them, two provisioned banks, and the customers, funding and mandate every
// flow needs.
func build(t *testing.T, opts harnessOptions) *harness {
	t.Helper()
	ctx := context.Background()

	h := &harness{clock: calendar.NewClock(testTime)}
	set := testenv.NewSet(t, h.clock.Now)
	h.rec = newRecordingStores(set)
	h.nets = payment.NewNetworks(h.rec, h.clock.Now)
	h.net = h.nets.ClearingHouse()

	assets := euroOnly
	if opts.twoAssets {
		assets = euroAndDollar
		h.net.RegisterScheme(usdCT{})
	}

	csmHost := httptest.NewServer(h.tap(testConfig.ClearingHouseBIC, func() http.Handler {
		return h.dep.ClearingHouse().EBICS()
	}))
	cbHost := httptest.NewServer(h.tap(testConfig.CentralBankBIC, func() http.Handler {
		return h.dep.CentralBank().EBICS()
	}))
	t.Cleanup(csmHost.Close)
	t.Cleanup(cbHost.Close)

	h.cfg = testConfig
	h.cfg.ClearingHouseURL = csmHost.URL
	h.cfg.CentralBankURL = cbHost.URL

	// No populate: this fixture builds its own two banks, and a deployment that
	// reseeded on Reset would rebuild a scenario none of these tests describes.
	var err error
	if h.dep, err = NewDeployment(ctx, h.nets, set, h.clock, h.cfg, nil, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("NewDeployment: %v", err)
	}

	// Both banks' rows are written first, because everything after this point — a
	// customer account with a settlement reference behind it, a deposit that credits
	// a reserve — needs a bank the other two institutions have already answered about.
	h.debtor = h.provision(t, "Aurora Bank", "AURODEFFXXX", assets)
	h.creditor = h.provision(t, "Banca Verde", "VERDITMMXXX", assets)
	// And a separate act no provisioning performs: each pulls the scheme's routing
	// directory. Being in the roster makes a bank reachable; holding a copy makes
	// it able to reach anybody, and nothing publishes one.
	h.subscribeAll(t)
	for _, p := range []*payment.Bank{h.debtor, h.creditor} {
		for asset, accts := range p.Assets {
			if accts.Settlement == "" {
				t.Fatalf("%s operates in %s and holds no settlement account for it once provisioned", p.BIC, asset)
			}
		}
	}

	// The payer's account is opened with an overdraft limit exactly when the
	// fixture is withholding its bank's reserves, and never otherwise: an
	// overdraft is a fact about this account, and giving every payer one would
	// make "the payer can pay" mean something different everywhere.
	var limit ledger.Amount
	if opts.lendToTheDebtor {
		limit = harnessFunding
	}
	h.debtorAcct = h.openCustomer(t, h.debtor, "Alice", "EUR", limit)
	h.debtorIBAN = addressOf(t, h.debtorAcct)
	h.creditorAcct = h.openCustomer(t, h.creditor, "Bruno", "EUR", 0)
	h.creditorIBAN = addressOf(t, h.creditorAcct)
	// Funding is TWO acts and the fixture runs both. A deposit gives the customer
	// a balance and leaves the bank holding vault cash; it does not raise the
	// bank's reserve, because a bank cannot write in the central bank's book.
	if opts.fundTheDebtor {
		if err := h.bank(h.debtor.BIC).Deposit(ctx, h.debtor.ID, h.debtorAcct.ID, harnessFunding, "Opening deposit"); err != nil {
			t.Fatalf("Deposit: %v", err)
		}
		h.lodge(t, h.debtor.BIC, "EUR", harnessFunding)
	}
	if opts.fundTheDebtorsBank {
		other := h.openCustomer(t, h.debtor, "Dario", "EUR", 0)
		if err := h.bank(h.debtor.BIC).Deposit(ctx, h.debtor.ID, other.ID, harnessFunding, "Opening deposit"); err != nil {
			t.Fatalf("Deposit: %v", err)
		}
		h.lodge(t, h.debtor.BIC, "EUR", harnessFunding)
	}
	if opts.twoAssets {
		h.debtorUSDAcct = h.openCustomer(t, h.debtor, "Alice", "USD", 0)
		h.debtorUSDIBAN = addressOf(t, h.debtorUSDAcct)
		h.creditorUSDAcct = h.openCustomer(t, h.creditor, "Bruno", "USD", 0)
		h.creditorUSDIBAN = addressOf(t, h.creditorUSDAcct)
		// Funded and lodged on the same terms as the euro side, separately in both
		// halves because each is per asset: a lodgement moves one asset's vault onto
		// that asset's reserve, and a euro balance is no use to a dollar cycle.
		if err := h.bank(h.debtor.BIC).Deposit(ctx, h.debtor.ID, h.debtorUSDAcct.ID, harnessFunding, "Opening deposit"); err != nil {
			t.Fatalf("Deposit USD: %v", err)
		}
		h.lodge(t, h.debtor.BIC, "USD", harnessFunding)
	}

	h.debtorBIC, h.creditorBIC = h.debtor.BIC, h.creditor.BIC
	h.debtorPID, h.creditorPID = h.debtor.ID, h.creditor.ID
	h.debtorBook, h.creditorBook = h.debtor.BookID, h.creditor.BookID

	// The mandate is created for every fixture, funded or not. It names the two
	// parties by the same refs the request quotes, because SDD.ValidateMandate
	// compares them party by party. MaxAmount 0 is unlimited.
	if h.mandate, err = h.bank(h.creditor.BIC).CreateMandate(ctx, h.debtorBIC, h.debtorRef(), h.creditorRef(h.creditorIBAN), 0); err != nil {
		t.Fatalf("CreateMandate: %v", err)
	}
	if opts.revokeMandate {
		if err := h.bank(h.creditor.BIC).RevokeMandate(ctx, h.mandate.ID); err != nil {
			t.Fatalf("RevokeMandate: %v", err)
		}
	}

	// The fixture's own traffic is forgotten, so that a test counting messages
	// counts its own. It is the same forgetting h.rec.reset() does for books.
	h.forgetMessages()

	if opts.openCycles {
		schemes := []payment.SchemeID{payment.SchemeSEPACT, payment.SchemeSEPADD}
		if opts.twoAssets {
			schemes = append(schemes, schemeUSDCT)
		}
		for _, scheme := range schemes {
			if _, err := h.net.OpenCycle(ctx, scheme); err != nil {
				t.Fatalf("OpenCycle %s: %v", scheme, err)
			}
		}
	}

	return h
}

// tap is the wire, and everything on it is recorded here. It sits in front of a
// host's EBICS endpoint and reads both directions out of the envelope, so a
// message is tapped exactly ONCE, at the moment it crosses.
func (h *harness) tap(host iso20022.BIC, of func() http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		rec := httptest.NewRecorder()
		of().ServeHTTP(rec, r)

		sub := iso20022.BIC(r.Header.Get(ebics.SubscriberHeader))
		var req ebics.Request
		if err := json.Unmarshal(body, &req); err == nil && len(req.Payload) > 0 {
			h.record(host, sub, req.Payload)
		}
		var resp ebics.Response
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err == nil {
			for _, f := range resp.Files {
				h.record(sub, host, f.Payload)
			}
		}

		for k, v := range rec.Result().Header {
			w.Header()[k] = v
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

// provision writes one bank's three rows and gives it its place in the network.
func (h *harness) provision(t *testing.T, name string, bic iso20022.BIC, assets []ledger.AssetCode) *payment.Bank {
	t.Helper()
	ctx := context.Background()
	p, err := provision.Bank(ctx, h.nets, provision.BankSpec{
		Name: name, BIC: bic, Country: storetest.FixtureCountry, Assets: assets,
	})
	if err != nil {
		t.Fatalf("provisioning %s (%s): %v", name, bic, err)
	}
	if err := h.dep.AddBank(ctx, p); err != nil {
		t.Fatalf("AddBank %s: %v", bic, err)
	}
	return p
}

// subscribeAll has every member pull the routing directory as it stands, through
// the same call an operator's refresh makes. It reads the roster to find the
// members, which is what a deployment can do and no institution may.
func (h *harness) subscribeAll(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	entries, err := h.net.ListRosterEntries(ctx)
	if err != nil {
		t.Fatalf("ListRosterEntries: %v", err)
	}
	for _, e := range entries {
		if _, err := h.dep.RefreshDirectory(ctx, e.BIC); err != nil {
			t.Fatalf("RefreshDirectory %s: %v", e.BIC, err)
		}
	}
}

// admitWithoutTheRoster builds a bank the SETTLEMENT AGENT has answered and the
// CLEARING HOUSE has never admitted: it holds a bank code, so it can address
// its customers' accounts, and no member can be told where to send anything for
// it.
func (h *harness) admitWithoutTheRoster(t *testing.T, name string, bic iso20022.BIC) *payment.Bank {
	t.Helper()
	ctx := context.Background()
	applicant := h.bank(bic)
	if _, err := applicant.FoundBank(ctx, name, bic, storetest.FixtureCountry, euroOnly); err != nil {
		t.Fatalf("FoundBank %s: %v", bic, err)
	}
	ref := "unrostered-" + string(bic)
	member, issuer, err := h.cb().OpenSettlementAccount(ctx, payment.AdmissionRequest{
		Name: name, BIC: bic, Country: storetest.FixtureCountry, Asset: "EUR", Ref: ref,
	})
	if err != nil {
		t.Fatalf("OpenSettlementAccount %s: %v", bic, err)
	}
	b, err := applicant.RecordMembership(ctx, payment.AdmissionAcknowledgement{
		BIC: bic, Issuer: issuer, Accounts: member.Accounts, Ref: ref,
	})
	if err != nil {
		t.Fatalf("RecordMembership %s: %v", bic, err)
	}
	if err := h.dep.AddBank(ctx, b); err != nil {
		t.Fatalf("AddBank %s: %v", bic, err)
	}
	return b
}

// getBank re-reads a bank from the store, out of the BANK's own database: the csm
// shape has no banks table, and a bank's own row is the one thing about it no other
// institution keeps.
func (h *harness) getBank(t *testing.T, id payment.ParticipantID) *payment.Bank {
	t.Helper()
	p, err := h.bank(iso20022.BIC(id)).GetBank(context.Background(), id)
	if err != nil {
		t.Fatalf("GetBank %s: %v", id, err)
	}
	return p
}

// centralBankTransactionCount is how many transactions stand in the settlement
// agent's own book. It exists because the book recorder cannot tell a READ of a
// book from a POSTING into it, and one measurement here needs the second.
func (h *harness) centralBankTransactionCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.cb().Store().View(context.Background(), func(ctx context.Context, tx payment.CentralBankTx) error {
		txs, err := tx.ListTransactions(ctx, payment.CentralBankBook)
		n = len(txs)
		return err
	}); err != nil {
		t.Fatalf("counting the central bank's transactions: %v", err)
	}
	return n
}

// getSettlementMember is the CENTRAL BANK's own record of a member: the row a
// settlement agent would post from, read by BIC because that is the only
// identifier it has, through that institution's own store — no other schema has
// a settlement_members table.
func (h *harness) getSettlementMember(t *testing.T, bic iso20022.BIC) payment.SettlementMember {
	t.Helper()
	var out payment.SettlementMember
	if err := h.cb().Store().View(context.Background(), func(ctx context.Context, tx payment.CentralBankTx) error {
		var err error
		out, err = tx.GetSettlementMember(ctx, bic)
		return err
	}); err != nil {
		t.Fatalf("GetSettlementMember %s: %v", bic, err)
	}
	return out
}

// openCustomer opens a deposit account in one asset, addressable by an IBAN, with
// an overdraft limit of the caller's choosing.
func (h *harness) openCustomer(t *testing.T, p *payment.Bank, name string,
	asset ledger.AssetCode, overdraft ledger.Amount) deposit.Account {

	t.Helper()
	acct, err := p.Deposit.OpenAccount(context.Background(), name, asset, p.ProductID, overdraft)
	if err != nil {
		t.Fatalf("OpenAccount %s (%s): %v", name, asset, err)
	}
	return acct
}

// addressOf is the IBAN a register minted for an account.
func addressOf(t *testing.T, a deposit.Account) string {
	t.Helper()
	for _, i := range a.Identifiers {
		if i.Scheme == deposit.IdentifierIBAN {
			return i.Value
		}
	}
	t.Fatalf("account %s holds no IBAN", a.ID)
	return ""
}

// record notes one file crossing the wire: who from, who to, and the bytes. See
// tap, which is the only caller.
func (h *harness) record(to, from iso20022.BIC, raw []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seen = append(h.seen, tappedMessage{from: from, to: to, raw: raw})
}

// forgetMessages drops what the tap has recorded so far. It is called once, at
// the end of building the fixture; see build for why.
func (h *harness) forgetMessages() {
	h.mu.Lock()
	h.seen = nil
	h.mu.Unlock()
}

// creditTransferRequest is the instruction the harness's payer gives its bank:
// the whole of the happy path, as a value a test can alter one field of.
func (h *harness) creditTransferRequest(t *testing.T) payment.InitiatePaymentRequest {
	t.Helper()
	return h.creditTransferRequestTo(t, h.creditorIBAN)
}

// creditTransferRequestTo is the same instruction addressed somewhere else.
func (h *harness) creditTransferRequestTo(t *testing.T, iban string) payment.InitiatePaymentRequest {
	t.Helper()
	return payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPACT,
		Debtor:      h.debtorRef(),
		Creditor:    h.creditorRef(iban),
		Amount:      harnessAmount,
		Description: "invoice 42",
		// Push: the creditor is the counterparty, and the NAME is the whole of what this
		// instruction says about it. No agent, because the submitting bank derives it
		// from the address above through its own copy of the routing directory.
		CreditorDetails: payment.PartyDetails{Name: h.creditorAcct.Name},
		// And the payer's own bank, which this fixture has to name and a customer's
		// instruction does not.
		DebtorDetails: payment.PartyDetails{Agent: h.debtorBIC, Name: h.debtorAcct.Name},
	}
}

// reverseCreditTransfer is the same push the other way: the payee's customer
// paying the payer's, out of the payee's own bank.
func (h *harness) reverseCreditTransfer(t *testing.T) payment.InitiatePaymentRequest {
	t.Helper()
	return payment.InitiatePaymentRequest{
		Scheme: payment.SchemeSEPACT,
		Debtor: payment.PartyRef{
			Account:    h.creditorAcct.ID,
			Identifier: h.creditorAcct.Identifiers[0],
		},
		Creditor: payment.PartyRef{
			Account:    h.debtorAcct.ID,
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: h.debtorIBAN},
		},
		Amount:          harnessAmount,
		Description:     "invoice 43",
		CreditorDetails: payment.PartyDetails{Name: h.debtorAcct.Name},
		DebtorDetails:   payment.PartyDetails{Agent: h.creditorBIC, Name: h.creditorAcct.Name},
	}
}

// debtorRef and creditorRef are the harness's two customers as a payment names
// them: which bank holds the account, which account, and the address quoted to
// reach it.
func (h *harness) debtorRef() payment.PartyRef {
	return payment.PartyRef{Account: h.debtorAcct.ID, Identifier: h.debtorAcct.Identifiers[0]}
}

func (h *harness) creditorRef(iban string) payment.PartyRef {
	return payment.PartyRef{
		Account:    h.creditorAcct.ID,
		Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: iban},
	}
}

// directDebitRequest is the instruction the harness's PAYEE gives its own bank:
// collect from Alice, under the mandate she signed.
func (h *harness) directDebitRequest(t *testing.T) payment.InitiatePaymentRequest {
	t.Helper()
	return payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPADD,
		Debtor:      h.debtorRef(),
		Creditor:    h.creditorRef(h.creditorIBAN),
		Amount:      harnessAmount,
		MandateID:   h.mandate.ID,
		Description: "subscription 7",
		// Pull: the debtor is the counterparty, so the request names it — the NAME
		// and nothing else. See creditTransferRequestTo on why no agent sits beside
		// it.
		DebtorDetails: payment.PartyDetails{Name: h.debtorAcct.Name},
		// And the SUBMITTER, which on a pull is the payee's bank. Deployment.Submit picks
		// the actor out of the submitting side's agent before any bank's half runs, so a
		// request leaving it empty has nobody to hand the collection to.
		CreditorDetails: payment.PartyDetails{Agent: h.creditorBIC, Name: h.creditorAcct.Name},
	}
}

// submitDirectDebit runs the payee's collection through their own bank and
// returns what that bank answered.
func (h *harness) submitDirectDebit(t *testing.T) payment.Payment {
	t.Helper()
	p, err := h.dep.Submit(context.Background(), h.directDebitRequest(t))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return p
}

// submitCreditTransfer runs the payer's instruction through their own bank, and
// returns what that bank answered. It does NOT wait: the creditor's bank has not
// seen the payment yet, which is the whole point of a store-and-forward transport.
func (h *harness) submitCreditTransfer(t *testing.T) payment.Payment {
	t.Helper()
	return h.submitCreditTransferTo(t, h.creditorIBAN)
}

func (h *harness) submitCreditTransferTo(t *testing.T, iban string) payment.Payment {
	t.Helper()
	p, err := h.dep.Submit(context.Background(), h.creditTransferRequestTo(t, iban))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return p
}

// submitCreditTransferInUSD is the same push between the same two banks, in the
// other asset and therefore under the other scheme.
func (h *harness) submitCreditTransferInUSD(t *testing.T) payment.Payment {
	t.Helper()
	p, err := h.dep.Submit(context.Background(), payment.InitiatePaymentRequest{
		Scheme: schemeUSDCT,
		Debtor: payment.PartyRef{
			Account:    h.debtorUSDAcct.ID,
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: h.debtorUSDIBAN},
		},
		Creditor: payment.PartyRef{
			Account:    h.creditorUSDAcct.ID,
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: h.creditorUSDIBAN},
		},
		DebtorDetails: payment.PartyDetails{Agent: h.debtorBIC, Name: h.debtorUSDAcct.Name},
		Amount:        harnessAmount,
		Description:   "invoice 43",
		// Push: the creditor is the counterparty, so the request names it. See
		// creditTransferRequestTo on why there is no Agent beside the name.
		CreditorDetails: payment.PartyDetails{Name: h.creditorUSDAcct.Name},
	})
	if err != nil {
		t.Fatalf("Submit in USD: %v", err)
	}
	return p
}

// dollarsTo is a dollar credit transfer to a payee at any bank, addressed by
// the account the register holds.
func (h *harness) dollarsTo(t *testing.T, acct deposit.Account) payment.InitiatePaymentRequest {
	t.Helper()
	return payment.InitiatePaymentRequest{
		Scheme: schemeUSDCT,
		Debtor: payment.PartyRef{
			Account:    h.debtorUSDAcct.ID,
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: h.debtorUSDIBAN},
		},
		Creditor:        payment.PartyRef{Account: acct.ID, Identifier: acct.Identifiers[0]},
		DebtorDetails:   payment.PartyDetails{Agent: h.debtorBIC, Name: h.debtorUSDAcct.Name},
		CreditorDetails: payment.PartyDetails{Name: acct.Name},
		Amount:          harnessAmount,
		Description:     "invoice 45",
	}
}

// aThirdBank admits one more member with a euro customer, and has every member
// pull the directory again so the new bank can be addressed.
func (h *harness) aThirdBank(t *testing.T) (*payment.Bank, deposit.Account) {
	t.Helper()
	p := h.provision(t, "Banco Tercero", "TERCESMMXXX", euroOnly)
	// Everybody re-subscribes, this one included: a member admitted since the last
	// refresh is in the roster and in nobody's copy of it, which is a state with a
	// test of its own and not the one being set up here.
	h.subscribeAll(t)
	return p, h.openCustomer(t, p, "Carla", "EUR", 0)
}

// creditTransferToAccount is an instruction to a payee at any bank, addressed by
// the account the register actually holds. creditTransferRequestTo varies the
// ADDRESS over one fixed account; this varies both.
func (h *harness) creditTransferToAccount(t *testing.T, acct deposit.Account, reference string) payment.InitiatePaymentRequest {
	t.Helper()
	req := h.creditTransferRequestTo(t, addressOf(t, acct))
	req.Creditor.Account = acct.ID
	req.CreditorDetails = payment.PartyDetails{Name: acct.Name}
	req.Description = reference
	return req
}

// submit hands one instruction to whichever bank the scheme says submits it, and
// fails the test if that bank refuses.
func (h *harness) submit(t *testing.T, req payment.InitiatePaymentRequest) payment.Payment {
	t.Helper()
	p, err := h.dep.Submit(context.Background(), req)
	if err != nil {
		t.Fatalf("Submit %q: %v", req.Description, err)
	}
	return p
}

// cutOff has one bank empty its hub into files and upload them, and fails the
// test if it could not.
func (h *harness) cutOff(t *testing.T, bic iso20022.BIC) []ebics.OrderID {
	t.Helper()
	b, err := h.dep.member(bic)
	if err != nil {
		t.Fatalf("member %s: %v", bic, err)
	}
	orders, err := b.Cutoff(context.Background())
	if err != nil {
		t.Fatalf("%s reaching its cut-off: %v", bic, err)
	}
	return orders
}

// pending is what one bank's hub is holding, through the same call its own
// GET /payments/pending answers with.
func (h *harness) pending(t *testing.T, bic iso20022.BIC) []payment.Payment {
	t.Helper()
	b, err := h.dep.member(bic)
	if err != nil {
		t.Fatalf("member %s: %v", bic, err)
	}
	ps, err := b.Pending(context.Background())
	if err != nil {
		t.Fatalf("%s reading its hub: %v", bic, err)
	}
	return ps
}

// filesOfTypeTo is every message of one definition handed to one institution, in
// arrival order, parsed. A bulk test asserts on how many files crossed and what is
// inside each, which is what lastMessageOfTypeTo cannot say.
func (h *harness) filesOfTypeTo(t *testing.T, to iso20022.BIC, msgDef string) []iso20022.Envelope {
	t.Helper()
	h.mu.Lock()
	seen := append([]tappedMessage(nil), h.seen...)
	h.mu.Unlock()

	var out []iso20022.Envelope
	for _, m := range seen {
		if m.to != to {
			continue
		}
		env, err := iso20022.Unmarshal(m.raw)
		if err != nil || env.AppHdr.MsgDefIdr != msgDef {
			continue
		}
		out = append(out, env)
	}
	return out
}

// ---------------------------------------------------------------------------
// The business day, and the piece of it a test can stand inside
// ---------------------------------------------------------------------------

// workThrough runs every institution through everything queued for it, in the
// day's own order, WITHOUT reaching the clearing house's cut-off and without
// moving the clock.
var workThroughPhases = only(beforeClock,
	phaseBankCutoff,
	phaseClearing,
	phaseSettlement,
	phaseRelease,
	phaseCollection,
)

// workThrough returns what could not be done rather than journalling it, which is
// the one way a test sequence differs from a day. The files and outcomes it moves DO
// reach the journal, so a later AdvanceDay reports them and never these problems.
func workThrough(d *Deployment) []Problem {
	return runPhases(context.Background(), d, workThroughPhases)
}

// work carries everything waiting and fails the test on anything an institution
// could not process.
func (h *harness) work(t *testing.T) {
	t.Helper()
	if err := h.workErr(t); err != nil {
		t.Fatalf("working through what has arrived: %v", err)
	}
}

// workErr is work for the tests that are ASSERTING on a failure: a file nobody
// could answer is recorded in the day's report and nowhere else, and a test
// about that path has to be able to read it.
func (h *harness) workErr(t *testing.T) error {
	t.Helper()
	return joinProblems(workThrough(h.dep))
}

// joinProblems renders a day's problems as one error, so that a test can match
// on what an institution said about a file it could not get through.
func joinProblems(ps []Problem) error {
	var errs []error
	for _, p := range ps {
		errs = append(errs, fmt.Errorf("%s could not process %s: %s", p.Institution, p.OrderID, p.Detail))
	}
	return errors.Join(errs...)
}

// day is the whole thing, exactly as an operator's button runs it, and it is
// what the tests about the DAY use rather than the tests about a flow.
func (h *harness) day(t *testing.T) DayReport {
	t.Helper()
	report, err := h.dep.AdvanceDay(context.Background())
	if err != nil {
		t.Fatalf("AdvanceDay: %v", err)
	}
	return report
}

// closeCycle reaches the cut-off on every window this fixture has open.
func (h *harness) closeCycle(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	cycles, err := h.net.ListCycles(ctx)
	if err != nil {
		t.Fatalf("ListCycles: %v", err)
	}
	var closed int
	for _, c := range cycles {
		if c.Status != payment.CycleOpen {
			continue
		}
		if _, err := h.dep.ClearingHouse().CloseCycle(ctx, c.ID); err != nil {
			t.Fatalf("CloseCycle %s (%s): %v", c.ID, c.Scheme, err)
		}
		closed++
	}
	if closed == 0 {
		t.Fatal("no open cycle to close; this fixture was built without one")
	}
}

// settledPayment is a credit transfer carried the whole way: submitted,
// accepted, cleared at the cut-off and discharged by the central bank. It is
// what a RETURN needs, finality being its precondition.
func (h *harness) settledPayment(t *testing.T) payment.Payment {
	t.Helper()
	return h.settle(t, h.submitCreditTransfer(t))
}

// settledCollection is the same thing in the other direction, because a return's
// direction is not the payment's: the bank that returns is the one that RECEIVED
// the instruction, so credit transfers alone would leave half that rule unmeasured.
func (h *harness) settledCollection(t *testing.T) payment.Payment {
	t.Helper()
	return h.settle(t, h.submitDirectDebit(t))
}

// settle carries a submitted payment through to finality: the counterparty's
// answer, the cut-off, and the central bank's discharge.
func (h *harness) settle(t *testing.T, p payment.Payment) payment.Payment {
	t.Helper()
	h.work(t)
	h.closeCycle(t)
	h.work(t)

	got := h.payment(t, p.ID)
	if got.Status != payment.Settled {
		t.Fatalf("the fixture payment is %v, want Settled — a return starts from finality", got.Status)
	}
	return got
}

// spendTheCredit is the payee sending the money straight back to the payer,
// carried to finality too.
func (h *harness) spendTheCredit(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := h.net.OpenCycle(ctx, payment.SchemeSEPACT); err != nil {
		t.Fatalf("OpenCycle for the payee to spend it in: %v", err)
	}
	p, err := h.dep.Submit(ctx, payment.InitiatePaymentRequest{
		Scheme:          payment.SchemeSEPACT,
		Debtor:          payment.PartyRef{Account: h.creditorAcct.ID, Identifier: h.creditorAcct.Identifiers[0]},
		Creditor:        h.debtorRef(),
		Amount:          harnessAmount,
		Description:     "spending what arrived",
		CreditorDetails: payment.PartyDetails{Agent: h.debtorBIC, Name: h.debtorAcct.Name},
		DebtorDetails:   payment.PartyDetails{Agent: h.creditorBIC}})
	if err != nil {
		t.Fatalf("the payee could not spend the credit: %v", err)
	}
	h.settle(t, p)
}

// returnPayment sends a settled payment back, failing the test if the bank that
// would send it refuses. Like submitCreditTransfer it does NOT wait: the returning
// bank's half builds a message, and the money moves later at the settlement agent.
func (h *harness) returnPayment(t *testing.T, id payment.PaymentID, reason iso20022.ReturnReason, text string) {
	t.Helper()
	if err := h.returnErr(id, reason, text); err != nil {
		t.Fatalf("Return: %v", err)
	}
}

func (h *harness) returnErr(id payment.PaymentID, reason iso20022.ReturnReason, text string) error {
	return h.dep.Return(context.Background(), id, reason, text)
}

// balance is what one customer's account holds, read through their own bank's
// deposit register. It is what says a return actually gave the money back — the
// suspense helper answers a question about a bank's own clearing position.
func (h *harness) balance(t *testing.T, id payment.ParticipantID, acct deposit.AccountID) ledger.Amount {
	t.Helper()
	ctx := context.Background()
	p, err := h.bank(iso20022.BIC(id)).GetBank(ctx, id)
	if err != nil {
		t.Fatalf("GetBank %s: %v", id, err)
	}
	bal, err := p.Deposit.GetBalance(ctx, acct)
	if err != nil {
		t.Fatalf("GetBalance %s: %v", acct, err)
	}
	return bal.Book
}

// postingByKey is one transaction out of one bank's book, found by the
// idempotency key the domain gave it: PostReturnLegTx posts
// "<payment>:return-refund" in the payer's bank's book and
// "<payment>:return-claw" in the payee's.
func (h *harness) postingByKey(t *testing.T, id payment.ParticipantID, key string) ledger.Transaction {
	t.Helper()
	ctx := context.Background()
	p, err := h.bank(iso20022.BIC(id)).GetBank(ctx, id)
	if err != nil {
		t.Fatalf("GetBank %s: %v", id, err)
	}
	txn, err := p.Ledger.GetTransactionByIdempotencyKey(ctx, key)
	if err != nil {
		t.Fatalf("no posting under %q in %s's book: %v", key, id, err)
	}
	return txn
}

// posting is one transaction out of one bank's book, found by the id the
// payment itself names. postingByKey's counterpart, for reads whose subject is
// an id a payment carries: a retried return leg is posted under a key derived
// from the attempt it replaces, so its key is not something a test can spell.
func (h *harness) posting(t *testing.T, id payment.ParticipantID, txID ledger.TransactionID) ledger.Transaction {
	t.Helper()
	ctx := context.Background()
	p, err := h.bank(iso20022.BIC(id)).GetBank(ctx, id)
	if err != nil {
		t.Fatalf("GetBank %s: %v", id, err)
	}
	txn, err := p.Ledger.GetTransaction(ctx, txID)
	if err != nil {
		t.Fatalf("no posting %s in %s's book: %v", txID, id, err)
	}
	return txn
}

// returnSentTo is the last pacs.004 an actor was handed, parsed. It is how a test
// reads what a return actually SAID, which no balance can show: two returns of the
// same payment for opposite reasons move exactly the same money.
func (h *harness) returnSentTo(t *testing.T, to iso20022.BIC) *iso20022.Pacs004 {
	t.Helper()
	env, err := iso20022.Unmarshal(h.lastMessageOfTypeTo(t, to, "pacs.004.001.09"))
	if err != nil {
		t.Fatalf("the last pacs.004 to %s does not parse: %v", to, err)
	}
	return env.Document.(*iso20022.Pacs004)
}

// payment reads the CLEARING HOUSE's copy, which is the only way to learn what
// became of one: Submit answers with the payment as its own bank left it, and
// everything after happened at another actor.
func (h *harness) payment(t *testing.T, id payment.PaymentID) payment.Payment {
	t.Helper()
	p, err := h.net.GetPayment(context.Background(), id)
	if err != nil {
		t.Fatalf("GetPayment %s: %v", id, err)
	}
	return p
}

// bankPayment reads ONE BANK's own copy, which is where a leg is.
func (h *harness) bankPayment(t *testing.T, bic iso20022.BIC, id payment.PaymentID) payment.Payment {
	t.Helper()
	p, err := h.bank(bic).GetPayment(context.Background(), id)
	if err != nil {
		t.Fatalf("GetPayment %s at %s: %v", id, bic, err)
	}
	return p
}

// cycles is every clearing cycle this network holds, in listing order. It is
// how a test reads what a cut-off left behind, which is the STATUS and not an
// id: a settled cycle is CycleSettled and a refused one is still CycleClosed.
func (h *harness) cycles(t *testing.T) []payment.ClearingCycle {
	t.Helper()
	cycles, err := h.net.ListCycles(context.Background())
	if err != nil {
		t.Fatalf("ListCycles: %v", err)
	}
	return cycles
}

// instructionsTo is every settlement instruction an actor was handed, parsed, in
// arrival order. instructionsSentTo counts them; this is for the tests that read
// what is inside one.
func (h *harness) instructionsTo(t *testing.T, to iso20022.BIC) []*iso20022.Pacs009 {
	t.Helper()
	h.mu.Lock()
	seen := append([]tappedMessage(nil), h.seen...)
	h.mu.Unlock()

	var out []*iso20022.Pacs009
	for _, m := range seen {
		if m.to != to {
			continue
		}
		env, err := iso20022.Unmarshal(m.raw)
		if err != nil {
			t.Fatalf("a message to %s does not parse: %v", to, err)
		}
		if doc, ok := env.Document.(*iso20022.Pacs009); ok {
			out = append(out, doc)
		}
	}
	return out
}

// suspense is the balance of a bank's euro clearing suspense: money that has left a
// customer and has not yet settled between banks. It is what a rejection has to
// return to zero.
func (h *harness) suspense(t *testing.T, id payment.ParticipantID) ledger.Amount {
	t.Helper()
	ctx := context.Background()
	p, err := h.bank(iso20022.BIC(id)).GetBank(ctx, id)
	if err != nil {
		t.Fatalf("GetBank %s: %v", id, err)
	}
	accts, err := p.AccountsFor("EUR")
	if err != nil {
		t.Fatalf("AccountsFor EUR: %v", err)
	}
	bal, err := p.Ledger.BookBalance(ctx, accts.Suspense.Total())
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	return bal
}

// lodge puts one bank's vault cash on reserve, composing both halves.
func (h *harness) lodge(t *testing.T, id iso20022.BIC, asset ledger.AssetCode, amount ledger.Amount) {
	t.Helper()
	ctx := context.Background()
	h.lodgeSeq++
	in, _, err := h.bank(id).LodgeReserves(ctx, asset, amount, payment.MessageContext{
		From:  id,
		To:    h.cfg.CentralBankBIC,
		MsgID: fmt.Sprintf("fixture-lodge-%s-%s-%d", id, asset, h.lodgeSeq),
		Now:   h.clock.Now(),
	})
	if err != nil {
		t.Fatalf("LodgeReserves %s %s: %v", id, asset, err)
	}
	if _, err := h.cb().ReceiveLodgement(ctx, in); err != nil {
		t.Fatalf("ReceiveLodgement %s %s: %v", id, asset, err)
	}
}

// vaultCash is one bank's euro vault balance: the cash it is holding and has
// not placed on reserve.
func (h *harness) vaultCash(t *testing.T, id payment.ParticipantID) ledger.Amount {
	t.Helper()
	ctx := context.Background()
	p, err := h.bank(iso20022.BIC(id)).GetBank(ctx, id)
	if err != nil {
		t.Fatalf("GetBank %s: %v", id, err)
	}
	accts, err := p.AccountsFor("EUR")
	if err != nil {
		t.Fatalf("AccountsFor EUR: %v", err)
	}
	bal, err := p.Ledger.BookBalance(ctx, accts.VaultCash.Total())
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	return bal
}

// reserveMirror is one bank's own Reserve at Central Bank balance: what its own
// book says its claim on the central bank is.
func (h *harness) reserveMirror(t *testing.T, id payment.ParticipantID) ledger.Amount {
	t.Helper()
	ctx := context.Background()
	p, err := h.bank(iso20022.BIC(id)).GetBank(ctx, id)
	if err != nil {
		t.Fatalf("GetBank %s: %v", id, err)
	}
	accts, err := p.AccountsFor("EUR")
	if err != nil {
		t.Fatalf("AccountsFor EUR: %v", err)
	}
	bal, err := p.Ledger.BookBalance(ctx, accts.Reserve.Total())
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	return bal
}

// booksTouchedBy is every ledger book one actor's units of work reached, sorted.
func (h *harness) booksTouchedBy(who iso20022.BIC) []ledger.BookID {
	return h.rec.touchedBy(who)
}

// injectRaw puts bytes where an institution will collect them, without building
// a message.
func (h *harness) injectRaw(t *testing.T, from, to iso20022.BIC, raw []byte) {
	t.Helper()
	switch to {
	case h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC:
		client := ebics.NewClient(ebics.SubscriberID(from), h.urlOf(to))
		if _, err := client.Upload(context.Background(), ebics.CST, raw); err != nil {
			t.Fatalf("uploading raw bytes from %s to %s: %v", from, to, err)
		}
	default:
		if _, err := h.hostOf(t, from).Enqueue(context.Background(), ebics.SubscriberID(to), ebics.CST, raw); err != nil {
			t.Fatalf("queueing raw bytes at %s for %s: %v", from, to, err)
		}
	}
}

// upload puts one built message on the wire, exactly as the sending institution
// would. It is what a test needs to make an institution answer a message no
// fixture flow produces.
func (h *harness) upload(t *testing.T, from, to iso20022.BIC, env iso20022.Envelope) {
	t.Helper()
	raw, err := iso20022.Marshal(env)
	if err != nil {
		t.Fatalf("marshalling for %s: %v", to, err)
	}
	orderType, err := orderTypeOf(env.Document)
	if err != nil {
		t.Fatalf("no order type for a %T: %v", env.Document, err)
	}
	switch to {
	case h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC:
		client := ebics.NewClient(ebics.SubscriberID(from), h.urlOf(to))
		if _, err := client.Upload(context.Background(), orderType, raw); err != nil {
			t.Fatalf("uploading a %s from %s to %s: %v", orderType, from, to, err)
		}
	default:
		if _, err := h.hostOf(t, from).Enqueue(context.Background(), ebics.SubscriberID(to), orderType, raw); err != nil {
			t.Fatalf("queueing a %s at %s for %s: %v", orderType, from, to, err)
		}
	}
}

// pendingAt is how many orders an institution still has to work through. A store
// failure is fatal rather than counted as zero: "the host is holding nothing" and
// "the host could not be asked" are opposite answers.
func pendingAt(t *testing.T, host *ebics.Server) int {
	t.Helper()
	pending, err := host.Pending(context.Background())
	if err != nil {
		t.Fatalf("reading a host's work list: %v", err)
	}
	return len(pending)
}

// hostOf is the EBICS side of one of the two institutions that has one, and fails
// on any other address: a member bank holds no host, because nothing is ever pushed
// at one.
func (h *harness) hostOf(t *testing.T, bic iso20022.BIC) *ebics.Server {
	t.Helper()
	switch bic {
	case h.cfg.ClearingHouseBIC:
		return h.dep.ClearingHouse().host
	case h.cfg.CentralBankBIC:
		return h.dep.CentralBank().host
	default:
		t.Fatalf("%s is a member bank and hosts nothing; nothing is pushed at one", bic)
		return nil
	}
}

// urlOf is where a subscriber dials one of the two hosts.
func (h *harness) urlOf(bic iso20022.BIC) string {
	if bic == h.cfg.CentralBankBIC {
		return h.cfg.CentralBankURL
	}
	return h.cfg.ClearingHouseURL
}

// messagesFrom is every message handed over since a mark taken with
// messagesSeen, in arrival order. It is how a test asserts on ONE conversation
// in a fixture that has already carried others.
func (h *harness) messagesFrom(mark int) []tappedMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]tappedMessage(nil), h.seen[mark:]...)
}

// lastMessageOfTypeTo is the raw bytes of the most recent message of one
// definition handed to one actor.
func (h *harness) lastMessageOfTypeTo(t *testing.T, to iso20022.BIC, msgDef string) []byte {
	t.Helper()
	h.mu.Lock()
	seen := append([]tappedMessage(nil), h.seen...)
	h.mu.Unlock()

	var last []byte
	for _, m := range seen {
		if m.to != to {
			continue
		}
		env, err := iso20022.Unmarshal(m.raw)
		if err != nil {
			continue
		}
		if env.AppHdr.MsgDefIdr == msgDef {
			last = m.raw
		}
	}
	if last == nil {
		t.Fatalf("%s was never sent a %s", to, msgDef)
	}
	return last
}

// messagesSeen is how many messages actors have been handed. After a Drain that is
// also how many were SENT, because Drain returns only when nothing is in flight.
func (h *harness) messagesSeen() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.seen)
}

// assertLastStatusTo checks that the last message an actor received was a
// pacs.002 carrying a given reason code — the LAST one, because a bank in a
// finished flow has received several.
func (h *harness) assertLastStatusTo(t *testing.T, to iso20022.BIC, want iso20022.StatusReason) {
	t.Helper()
	doc := h.lastStatusTo(t, to)
	if want == "" {
		for _, s := range doc.FIToFIPmtStsRpt.TxInfAndSts {
			if s.StsRsnInf != nil {
				t.Errorf("the last pacs.002 to %s carries a reason (%+v); an acceptance has none", to, s.StsRsnInf)
			}
		}
		return
	}
	for _, s := range doc.FIToFIPmtStsRpt.TxInfAndSts {
		if s.StsRsnInf != nil && s.StsRsnInf.Rsn.Cd != nil && *s.StsRsnInf.Rsn.Cd == want {
			return
		}
	}
	t.Errorf("the last pacs.002 to %s carries no %s; its statuses are %+v", to, want, doc.FIToFIPmtStsRpt.TxInfAndSts)
}

// assertLastTxStatusTo checks the TRANSACTION status of the last pacs.002 an
// actor received: ACCP, ACSC, RJCT.
func (h *harness) assertLastTxStatusTo(t *testing.T, to iso20022.BIC, want iso20022.TransactionStatus) {
	t.Helper()
	doc := h.lastStatusTo(t, to)
	for _, s := range doc.FIToFIPmtStsRpt.TxInfAndSts {
		if s.TxSts == want {
			return
		}
	}
	t.Errorf("the last pacs.002 to %s carries no %s; its statuses are %+v", to, want, doc.FIToFIPmtStsRpt.TxInfAndSts)
}

// lastStatusTo is the most recent message an actor was handed, parsed as the
// pacs.002 the caller expects it to be.
func (h *harness) lastStatusTo(t *testing.T, to iso20022.BIC) *iso20022.Pacs002 {
	t.Helper()
	h.mu.Lock()
	var last []byte
	for _, m := range h.seen {
		if m.to == to {
			last = m.raw
		}
	}
	h.mu.Unlock()
	if last == nil {
		t.Fatalf("%s received no message at all, so it was never told anything", to)
	}
	env, err := iso20022.Unmarshal(last)
	if err != nil {
		t.Fatalf("the last message to %s does not parse: %v", to, err)
	}
	doc, ok := env.Document.(*iso20022.Pacs002)
	if !ok {
		t.Fatalf("the last message to %s is a %s, want a pacs.002", to, env.AppHdr.MsgDefIdr)
	}
	return doc
}

// instructionsSentTo is how many settlement instructions an actor was handed.
func (h *harness) instructionsSentTo(to iso20022.BIC) int {
	return h.messagesSentTo(to, "pacs.009.001.08")
}

// statusesSentTo is how many pacs.002 an actor was handed. It is what says a
// party was told NOTHING, which is an assertion the last-message helpers cannot
// make: they need a message to look at.
func (h *harness) statusesSentTo(to iso20022.BIC) int {
	return h.messagesSentTo(to, "pacs.002.001.10")
}

// messagesSentTo counts one message definition delivered to one actor. Counted
// after a Drain, when what was handed over is also what was sent.
func (h *harness) messagesSentTo(to iso20022.BIC, msgDef string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	var n int
	for _, m := range h.seen {
		if m.to != to {
			continue
		}
		env, err := iso20022.Unmarshal(m.raw)
		if err != nil {
			continue
		}
		if env.AppHdr.MsgDefIdr == msgDef {
			n++
		}
	}
	return n
}
