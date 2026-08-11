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
//
// One harness, not one per test. The flows differ in what they assert, not in
// what they need: two banks that can address each other, a clearing house
// between them, both hosts really listening, a store that records which book
// each institution reached, and the phases of a business day to run. A second
// fixture beside this one would be a second answer to "what does this system
// look like", and the two would drift.
//
// # Nothing here waits
//
// Every phase is synchronous and there is no goroutine to join, so no test in
// this package sleeps, polls or takes a deadline. That is what a business day
// bought: work is queued by a request and performed by an advance, and when the
// advance returns it is done.
//
// # What it is NOT
//
// It does not stand in for the API. Deployment.Submit is what a bank's
// customer-facing layer calls, and the harness calls it directly.

// harnessAmount is what every transfer in this package moves, and harnessFunding
// is what the payer starts with. Funding is larger, so the payer is good for the
// money and a rejection is never confused with a refusal for lack of funds.
const (
	harnessAmount  ledger.Amount = 250_000
	harnessFunding ledger.Amount = 500_000
)

// The two IBANs the harness's customers are addressable by, and one nobody
// holds.
//
// Real, distinct, and in different countries, for the reason
// payment/message_test.go's addressedBanks gives: a test asserting which bank
// answered cannot use a fixture in which both banks look the same.
// The addresses this fixture's customers hold are NOT written down here any
// more, and could not be: a bank mints its customers' addresses out of the bank
// code it was allocated, so the only place one exists is on the account. They
// are read off the accounts as they are opened — see harness.debtorIBAN and
// the fields beside it.
//
// They remain real, distinct, and in different countries, for the reason
// payment/message_test.go's addressedBanks gives: a test asserting which bank
// answered cannot use a fixture in which both banks look the same. That now
// follows from the ALLOCATIONS the two banks are admitted under rather than from
// two literals — see harnessIssuers.

// unknownIBANAt is an address in one bank's own range that the bank does not
// hold: a serial far above anything a fixture opens, under that bank's allocated
// code.
//
// Both halves are what make it useful. It has to be well-formed twice over — an
// IBAN failing the schema's pattern is refused by the codec on the way out, and
// one failing mod-97 is refused by the payer's own bank — and it has to carry the
// RECIPIENT's bank code, because that is what routes it there. An address under
// some other code is refused by the payer's own directory
// (payment.ErrBankCodeUnknown) and the recipient never gets the chance to answer
// AC01, which is the answer these tests are about.
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
	// HOUSE's — the view the network-scoped reads below go through.
	//
	// A fixture cannot hold one Network and play every institution with it: a
	// member's own act needs that member's network and the central bank's book is
	// on the settlement agent's alone. See payment.Networks, and payment's
	// testSystem, which carries the same split for the same reason.
	nets *payment.Networks
	net  *payment.Network

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
	// Every fixture has one, including the ones whose collections are refused,
	// because a collection quoting no mandate at all is refused by
	// payment.SDD.ValidateMandate inside Submit — so none of the conditions these
	// tests exist to provoke would ever be reached.
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
	// exists, is addressable and is denominated in euro, and has nothing in it —
	// which is the condition that produces AM04 from the debtor's own BANK, on its
	// funds check. (There is a second route to the same code, from the central bank
	// and about a bank rather than a customer; see lendToTheDebtor.)
	fundTheDebtor bool
	// revokeMandate revokes the mandate after creating it. The mandate still
	// EXISTS, so the collection is refused for being unauthorised rather than
	// for naming a mandate this network has never heard of.
	revokeMandate bool
	// lendToTheDebtor opens the payer's account with an overdraft limit instead
	// of paying an opening deposit in. It is what leaves the payer's BANK
	// without reserves while the payer can still pay; see
	// newHarnessWithAnUnfundedReserve.
	lendToTheDebtor bool
	// fundTheDebtorsBank gives the payer's bank reserves without giving the
	// payer a balance, by depositing and lodging a SECOND customer's money. It
	// is the state fundTheDebtor deliberately will not produce, and it is what a
	// collection the payer cannot fund needs: the cut-off has to settle before
	// the payer's bank ever sees the account. See newHarnessWithAnUnfundedDebtor.
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
//
// Each variation gets a NAMED constructor rather than callers assembling their
// own harnessOptions, and the reason is in this one's name: "there is no open
// cycle" is not a knob, it is the condition the tests that use it exist to
// provoke — the clearing house refuses a payment it has nowhere to clear, and
// answers TM01. A test that spelt the options out at its call site would be
// stating a fixture where it means to state a hypothesis.
func newHarnessWithNoOpenCycle(t *testing.T) *harness {
	t.Helper()
	return build(t, harnessOptions{fundTheDebtor: true})
}

// newHarnessWithAnUnfundedDebtor is the same network with the payer's own
// account left empty and the payer's BANK able to settle anyway.
//
// It is the pull flow's counterpart to the unknown IBAN: the condition only the
// DEBTOR's bank can see, and therefore the one that proves which bank answered.
// A creditor's bank cannot know what is in the account it is collecting from —
// that is the whole asymmetry a direct debit is built on — so an empty account
// is a refusal that can only have come from the far side of the network.
//
// # The bank is funded and the customer is not, and prising them apart is the
// fixture
//
// The collection has to SETTLE before the payer's bank is handed it, so a
// fixture that left the bank without reserves would be refused by the settlement
// agent and never reach the customer's balance at all — which is a different
// condition, and newHarnessWithAnUnfundedReserve's. A deposit moves both at once
// (see fundTheDebtor), so the reserves come from a SECOND customer at the same
// bank: somebody else's money, lodged, which is what a bank's reserve is.
func newHarnessWithAnUnfundedDebtor(t *testing.T) *harness {
	t.Helper()
	return build(t, harnessOptions{openCycles: true, fundTheDebtorsBank: true})
}

// newHarnessWithASecondDepositor is the same network with another customer's
// money on the payer's bank's reserve beside its payer's.
//
// It is the fixture for a BATCH one of whose transactions the payer cannot fund.
// A bank whose reserve is exactly its payer's balance cannot settle such a batch
// at all — the cut-off nets more than the whole account holds — so the
// interesting outcome, one collection applied and one sent back, is unreachable
// behind a settlement refusal that is about the bank rather than the customer.
// A second depositor is how a real bank's reserve stops being one customer's
// balance.
func newHarnessWithASecondDepositor(t *testing.T) *harness {
	t.Helper()
	return build(t, harnessOptions{openCycles: true, fundTheDebtor: true, fundTheDebtorsBank: true})
}

// newHarnessWithARevokedMandate is the same network with the debtor's
// authority withdrawn.
//
// The creditor's bank holds the mandate in SEPA, so this is the one refusal in
// the pull flow that never reaches the wire: it happens inside Submit's own unit
// of work, at the bank the collection was handed to. See
// TestARevokedMandateIsRefusedSynchronously for what that costs.
func newHarnessWithARevokedMandate(t *testing.T) *harness {
	t.Helper()
	return build(t, harnessOptions{openCycles: true, fundTheDebtor: true, revokeMandate: true})
}

// newHarnessWithAnUnfundedReserve is the same network with the payer's bank
// holding no central-bank reserves.
//
// It is the one fixture in this package whose condition belongs to a BANK's
// balance sheet rather than a customer's account, and getting there needs the
// two to be prised apart. Network.Deposit moves both at once — cash paid in
// raises the customer's balance and the bank's reserve in one step, which is
// what a deposit IS — so a fixture that funded the payer normally would fund its
// bank too.
//
// So the payer's account is opened with an OVERDRAFT instead and never funded.
// The bank has lent its customer money it has no reserves behind, which is a
// real bank in a real morning: the payment passes its own funds check, the
// debtor leg posts, the collection clears — and at the cut-off the bank is a net
// payer of 250,000 against a reserve of nothing. Every earlier refusal in this
// package is a bank saying no about a customer; this is the settlement agent
// saying no about a bank.
func newHarnessWithAnUnfundedReserve(t *testing.T) *harness {
	t.Helper()
	return build(t, harnessOptions{openCycles: true, lendToTheDebtor: true})
}

// newHarnessWithTwoAssets is the same network operating in dollars as well
// as euro: both banks hold both, both customers have an account in each, and
// there is a dollar push scheme with a cut-off window of its own.
//
// It exists for the one claim a single-asset fixture cannot make. A cycle
// belongs to a scheme and a scheme settles in one asset, so "one settlement
// instruction per asset" is not observable until two cycles in two assets close
// together — and what it rules out is the arrangement that would net a euro
// position against a dollar one, or put both in one IntrBkSttlmAmt.
func newHarnessWithTwoAssets(t *testing.T) *harness {
	t.Helper()
	return build(t, harnessOptions{openCycles: true, fundTheDebtor: true, twoAssets: true})
}

// schemeUSDCT and usdCT are a second push scheme, identical to SEPA credit
// transfer in every respect except the unit it settles in.
//
// A scheme rather than a flag on the payment, because that is where this system
// puts the asset: payment.Scheme.Asset says a scheme settles in one unit, and a
// cross-currency payment is not one payment but a payment plus an FX trade. It
// embeds payment.SCT rather than reimplementing the interface, so a method added
// to Scheme tomorrow reaches this fixture without a compile error that says
// nothing.
const schemeUSDCT payment.SchemeID = "usd.ct"

type usdCT struct{ payment.SCT }

func (usdCT) ID() payment.SchemeID    { return schemeUSDCT }
func (usdCT) Asset() ledger.AssetCode { return "USD" }

// euroAndDollar is the asset set a bank in the two-asset fixture joins with.
// Founding gives it a suspense and a reserve account in each, and the admission
// that follows asks the central bank for a settlement account in each — one
// request per asset, because an account is denominated in one currency.
var euroAndDollar = []ledger.AssetCode{"EUR", "USD"}

// bank is one member's own network, keyed by its ADDRESS — a bank's
// ParticipantID is its BIC (see payment.AsBank), and every caller here holds an
// address. cb is the settlement agent's. See the nets field for why the harness
// needs all three.
//
// It panics on a failure to open: a bank's network is minted over that bank's
// own DATABASE, and opening one is I/O. Every caller here is one expression
// inside an assertion and every address they pass is a bank this harness
// founded, so a failure is a broken fixture rather than an outcome worth
// reporting through the test's own error path.
func (h *harness) bank(bic iso20022.BIC) *payment.Network {
	net, err := h.nets.Bank(context.Background(), payment.ParticipantID(bic))
	if err != nil {
		panic("harness_test: opening " + string(bic) + "'s store: " + err.Error())
	}
	return net
}
func (h *harness) cb() *payment.Network { return h.nets.CentralBank() }

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
// them, two provisioned banks, and the customers, funding and mandate every flow
// needs.
//
// # The two hosts are httptest servers, and that is decision 1 of the design
//
// The institutions talk over HTTP, on the listeners a deployment gives them.
// Anything cheaper — calling ebics.Server directly, or wiring the client to the
// server in memory — would make "the counterparty is unreachable" inexpressible
// again, which is exactly what this transport exists to make reachable.
//
// The handler is looked up per REQUEST rather than captured because the
// deployment does not exist yet: its config names these two URLs, so both
// listeners have to be standing before it is built.
//
// # The fixture never advances a day
//
// Building it lodges reserves and opens cycles, and both would be disturbed by a
// business day running through them — a day cuts every open cycle off and opens a
// fresh one, so a fixture that advanced could not offer a network with NO open
// cycle. Every act here is therefore composed or driven directly, and the day is
// left to the tests.
func build(t *testing.T, opts harnessOptions) *harness {
	t.Helper()
	ctx := context.Background()

	h := &harness{clock: calendar.NewClock(testTime)}
	h.rec = newRecordingStores(testenv.NewSet(t, h.clock.Now))
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
	if h.dep, err = NewDeployment(ctx, h.nets, h.clock, h.cfg, nil, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("NewDeployment: %v", err)
	}

	// Both banks' rows are written before anything else, because everything after
	// this point — a customer account with a settlement reference behind it, a
	// deposit that credits a reserve — needs a bank the other two institutions
	// have already answered about.
	h.debtor = h.provision(t, "Aurora Bank", "AURODEFFXXX", assets)
	h.creditor = h.provision(t, "Banca Verde", "VERDITMMXXX", assets)
	// And a separate act that no provisioning performs: each of them pulls the
	// scheme's routing directory. Being in the roster is what makes a bank
	// reachable; holding a copy of the roster is what makes it able to reach
	// anybody, and nothing publishes one. A fixture that skipped this would have
	// two members neither of which could address the other — which is a real state
	// and is what TestABankAdmittedAfterTheLastRefreshCannotBePaidUntilTheNextOne
	// is about.
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
	// overdraft is a fact about this account, so a fixture that gave every payer
	// one would make "the payer can pay" mean something different everywhere.
	var limit ledger.Amount
	if opts.lendToTheDebtor {
		limit = harnessFunding
	}
	h.debtorAcct = h.openCustomer(t, h.debtor, "Alice", "EUR", limit)
	h.debtorIBAN = addressOf(t, h.debtorAcct)
	h.creditorAcct = h.openCustomer(t, h.creditor, "Bruno", "EUR", 0)
	h.creditorIBAN = addressOf(t, h.creditorAcct)
	// Funding is TWO acts and the fixture has to run both. A deposit gives the
	// customer a balance and leaves the bank holding vault cash; it does not raise
	// the bank's reserve, because a bank cannot write in the central bank's book.
	// So every fixture that wants a settleable payment also lodges.
	//
	// Both are behind the SAME option, and that is deliberate rather than lazy.
	// fundTheDebtor has always meant "this bank can pay", and it still means
	// exactly that; splitting it into two options would let a fixture ask for a
	// bank with a funded customer and no reserve, which is a state no test in this
	// package wants and which would fail at settlement in a way that named neither
	// option. A test that wants that state can lodge for itself — see
	// TestABankCannotSettleOutOfVaultCash, which is the one that does.
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
		// Funded and lodged on the same terms as the euro side, and separately in
		// both halves, because each is per asset: a deposit lands in the funded
		// account's OWN vault, and a lodgement moves one asset's vault onto that
		// asset's reserve. A dollar cycle settles across dollar reserves and a euro
		// balance is no use to it.
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
	// compares them party by party and a mandate over a different account is a
	// mismatch rather than an authority. MaxAmount 0 is unlimited, so the amount
	// is never what refuses a collection here.
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

// tap is the wire, and everything on it is recorded here.
//
// It sits in front of a host's EBICS endpoint and reads both directions out of
// the envelope: an upload carries its payload up, and a download carries files
// back. So a message is tapped exactly ONCE, at the moment it crosses — which is
// what a message being handed to an actor used to mean, and is measured against
// the same helpers.
//
// The two ends are the CONNECTION's rather than the header's. A file's sender is
// the subscriber that dialled in, and its recipient is the host it dialled; on a
// download the two are the other way round. That is what the receiving
// institution itself goes by (see Bank.handle and ClearingHouse.handleFile), so
// a test reading the tap sees what the institution saw.
//
// A file that is queued and never collected is never tapped, and that is
// correct: nothing is pushed at a member bank, so a file waiting in a queue has
// not been delivered to anybody.
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
//
// The two steps are separate because they are different kinds of thing: the rows
// are what the three institutions hold, and the place is a download queue at each
// host. A deployment does the same in the same order — see provision.Bank and
// AddBank — and neither is a file, so there is nothing here to carry.
//
// It does NOT subscribe. What comes back is a bank the roster carries and no
// other member's copy has yet, which is the state
// TestABankAdmittedAfterTheLastRefreshCannotBePaidUntilTheNextOne is about. A
// fixture that wants members able to address each other calls subscribeAll.
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

// subscribeAll has every member of the scheme pull the routing directory as it
// stands, through the same call an operator's POST /directory/banks/refresh
// makes.
//
// It reads the roster to find the members, which is what a deployment can do and
// no institution may: each of them then pulls for itself.
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
// CLEARING HOUSE has never admitted: it holds a bank code, so it can address its
// customers' accounts, and no member of this scheme can be told where to send
// anything for it.
//
// It is three of admission's four acts driven directly, with AdmitMember left
// out, and provision.Bank cannot produce it — it runs all four in order. What
// makes it reachable is that the acts are separately callable, and what makes it
// worth building is that it is the only state in which a bank has customers with
// money and no route to anybody.
//
// A merely FOUNDED bank is not this. A bank no registry has answered has no
// address range at all, so it can open no customer account whatever
// (deposit.ErrNoIssuer) and has nobody to pay with.
//
// The bank is given its PLACE, because the tests using this are about what the
// network refuses rather than about who it can reach: without one, a submission
// is refused "this deployment holds no bank at", which is true and is not the
// refusal being measured.
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

// getBank re-reads a bank from the store.
//
// Out of the BANK's own database: the csm shape has no banks table, and a bank's
// own row is the one thing about it no other institution keeps.
func (h *harness) getBank(t *testing.T, id payment.ParticipantID) *payment.Bank {
	t.Helper()
	p, err := h.bank(iso20022.BIC(id)).GetBank(context.Background(), id)
	if err != nil {
		t.Fatalf("GetBank %s: %v", id, err)
	}
	return p
}

// centralBankTransactionCount is how many transactions stand in the settlement
// agent's own book.
//
// It exists because the book recorder cannot tell a READ of a book from a
// POSTING into it, and one measurement in this package needs to claim the
// second. See TestFundingAReserveReachesTwoBooks.
func (h *harness) centralBankTransactionCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.cb().Store().View(context.Background(), func(ctx context.Context, tx payment.Tx) error {
		txs, err := tx.ListTransactions(ctx, payment.CentralBankBook)
		n = len(txs)
		return err
	}); err != nil {
		t.Fatalf("counting the central bank's transactions: %v", err)
	}
	return n
}

// getSettlementMember is the CENTRAL BANK's own record of a member: the row a
// settlement agent with its own database would post from, read by BIC because
// that is the only identifier it has.
//
// Through the CENTRAL BANK's own store: no other institution's schema has a
// settlement_members table.
//
// It goes through the store rather than through a Network method because it
// predates one: payment.Network.GetSettlementMember exists now, added for GET
// /reserves/{bic}, and this stays a direct read so that a fixture asserting what
// the agent WROTE does not go through the method it is asserting about.
func (h *harness) getSettlementMember(t *testing.T, bic iso20022.BIC) payment.SettlementMember {
	t.Helper()
	var out payment.SettlementMember
	if err := h.cb().Store().View(context.Background(), func(ctx context.Context, tx payment.Tx) error {
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
//
// The creditor's participant and account are the real ones and only the ADDRESS
// varies, which is what makes an unresolvable address a message the debtor's
// bank can build and send. That is the honest shape: a payer quotes an IBAN,
// submission does not resolve it — the instruction carries what was quoted —
// and the bank at the other end is the party whose answer decides whether it is
// payable. See TestCreditTransferToAnUnknownAccountComesBackAsAC01.
func (h *harness) creditTransferRequestTo(t *testing.T, iban string) payment.InitiatePaymentRequest {
	t.Helper()
	return payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPACT,
		Debtor:      h.debtorRef(),
		Creditor:    h.creditorRef(iban),
		Amount:      harnessAmount,
		Description: "invoice 42",
		// Push: the creditor is the counterparty, and the NAME is the whole of what
		// this instruction says about it. No agent, because there is nowhere to put
		// one that would be read: the submitting bank derives it from the address
		// above, through its own copy of the routing directory.
		CreditorDetails: payment.PartyDetails{Name: h.creditorAcct.Name},
		// And the payer's own bank, which this fixture has to name and a customer's
		// instruction does not. Deployment.Submit picks the SUBMITTING bank out of
		// the two agents (submitterOf) before any bank's half runs, so a request that
		// left this empty would be refused with "holds no bank at" — the deployment
		// having nobody to hand it to. api's POST /payments fills the same field
		// from the port's bound identity and the seed names it outright; this is a
		// caller that names its own side, which every fixture here is.
		DebtorDetails: payment.PartyDetails{Agent: h.debtorBIC, Name: h.debtorAcct.Name},
	}
}

// reverseCreditTransfer is the same push the other way: the payee's customer
// paying the payer's, out of the payee's own bank.
//
// It is what a cut-off that nets to nothing needs — two banks paying each other
// the same amount inside one cycle — and it is the only instruction in this
// fixture that the CREDITOR's bank submits under a push scheme. The amount is
// the harness's one, because a position cancels exactly or it does not cancel at
// all.
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
//
// They are shared by the two flows and by the mandate, which is not tidiness: a
// mandate authorises debits from an ACCOUNT and SDD.ValidateMandate compares the
// refs party by party, so a fixture that built the mandate's parties and the
// collection's parties separately could drift into a mismatch that looked like a
// revoked mandate.
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
//
// The mirror of creditTransferRequest, and the difference is who hands it in. A
// credit transfer is the payer instructing their bank to push; a collection is
// the payee instructing THEIR bank to pull, which is why Deployment.Submit routes it
// to the creditor's actor and why nothing is posted when it is accepted.
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
		// the actor out of the submitting side's agent before any bank's half runs,
		// so a request leaving it empty has nobody to hand the collection to. See
		// creditTransferRequestTo, where the same field is here for the same
		// reason on the other side.
		CreditorDetails: payment.PartyDetails{Agent: h.creditorBIC, Name: h.creditorAcct.Name},
	}
}

// submitDirectDebit runs the payee's collection through their own bank, and
// returns what that bank answered. Like submitCreditTransfer it does NOT wait —
// and unlike it, what comes back has moved no money at all: the debtor's bank
// has not seen the collection yet, and until it does nobody has looked at the
// account being collected from.
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
//
// Both parties are the two customers' DOLLAR accounts, which is not a detail: a
// payment's legs must both be denominated in its scheme's asset, so quoting the
// euro account here would be refused by the payer's own bank before any message
// was built. Only newHarnessWithTwoAssets opens those accounts.
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

// dollarsTo is a dollar credit transfer to a payee at any bank, addressed by the
// account the register holds.
//
// The payee's account is a EURO one and that is the point rather than an
// oversight: the assets a member clears in are the roster's fact and no
// submitting bank can check them, so this is what a file addressed to a member
// outside its own scheme looks like on the wire. Only the payer's own leg is
// held to the scheme's asset, and it is a dollar account. See
// TestAMixedFileIsAnsweredPART.
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
//
// It exists for the FAN-OUT, which is the one claim a two-bank fixture cannot
// make: with two banks every file a submitting bank uploads has one destination,
// so "the clearing house sorts by creditor agent" and "the clearing house
// forwards the file" are indistinguishable. A third bank is the smallest network
// in which they are not.
//
// It comes back with the account as well as the bank, because the address a
// payment quotes is minted by the register and cannot be written down in
// advance.
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
// the account the register actually holds.
//
// creditTransferRequestTo varies the ADDRESS over one fixed account, which is
// what an unroutable payee needs; this varies both, which is what a payee at a
// third bank needs.
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
// arrival order, parsed.
//
// A bulk test asserts on how many files crossed and what is inside each, which
// is exactly what lastMessageOfTypeTo cannot say: the whole question is whether
// one file became two.
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
//
// "Submit, advance a day, assert" is what every flow here reads, and a day is
// synchronous: nothing is in flight when one returns, so no test waits for a
// duration and none ever needs to.

// workThrough runs every institution through everything queued for it, in the
// day's own order, WITHOUT reaching the clearing house's cut-off and without
// moving the clock.
//
// It is every phase of AdvanceDay that carries a file, and it leaves out the
// three that are the day's own acts: the directory refresh, the CYCLE cut-off,
// and the advance. That is what makes it the piece a test can stand inside. A
// payment is ACCEPTED when this returns and SETTLED when it is run again after a
// cut-off, and the two moments are a day apart in a deployment where one cycle
// closes per day.
//
// Leaving the cycle cut-off out is therefore what stops anything being RELEASED:
// the clearing house holds each receiving bank's share of a file until the cycle
// carrying it has settled, so a test that calls this once and asserts is looking
// at a network in which the payees\' banks have not been told anything yet, on
// purpose.
//
// The BANKS' cut-off is in, because it is where a file comes from: a hub that
// was never emptied uploads nothing and every flow test would measure a network
// carrying no payments. The two cut-offs share a word and are different acts —
// see Deployment.clear.
//
// Leaving the refresh out is load-bearing rather than tidy: a fixture asserting
// what a stale routing directory costs needs it not to run. Leaving the cycle
// cut-off out is what lets a test see a payment sitting in an open cycle at all.
//
// Members collect from the SETTLEMENT AGENT first, which is the ordering
// AdvanceDay's last phase exists for and the only thing that now guarantees a
// mirror leg is booked before the creditor legs draw on it.
func workThrough(d *Deployment) []Problem {
	ctx := context.Background()

	var ps []Problem
	for _, b := range d.subscribers() {
		_, problems := b.cutoff(ctx)
		ps = append(ps, problems...)
	}
	ps = append(ps, d.csm.work(ctx)...)
	ps = append(ps, d.cb.work(ctx)...)
	ps = append(ps, d.csm.collect(ctx)...)
	for _, b := range d.subscribers() {
		ps = append(ps, b.collect(ctx, d.cfg.CentralBankBIC, b.cb, ebics.C53)...)
		ps = append(ps, b.collect(ctx, d.cfg.CentralBankBIC, b.cb, ebics.BTD)...)
		ps = append(ps, b.collect(ctx, d.cfg.ClearingHouseBIC, b.csm, ebics.BTD)...)
	}
	return ps
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
//
// Through the CLEARING HOUSE and not through the network: closing a cycle is
// that institution's act and it ends in a pacs.009 uploaded to the settlement
// agent rather than in a return value. Driving h.net directly would close the
// cycle on a bare context — attributed to no institution, instructing nobody —
// and every settlement assertion in this package would measure an operator
// poking the store.
//
// EVERY open cycle, including the ones with nothing in them. A credit-transfer
// test leaves the direct-debit window untouched, so this closes an empty cycle
// on almost every run — which is exactly the path that must instruct nothing,
// and it is walked constantly rather than reasoned about. See
// ClearingHouse.instructSettlement.
//
// It returns nothing, and it settles nothing. A cut-off is the START of a
// conversation: the instruction is uploaded and the settlement agent has not
// looked at it, which is the state a test asserting on the INSTRUCTION wants.
// Call work afterwards to carry it.
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
// accepted, cleared at the cut-off and discharged by the central bank.
//
// It is what a RETURN needs, because a return is the one flow whose
// precondition is finality — the returning bank's own guard and
// payment.PostReturnLegTx both refuse anything that is not Settled. Built by
// driving the real flow rather than by writing a Settled payment into the store, so
// that what is returned is a payment this system really carried: the payer's
// money is in the payee's account and the reserves have moved, which is exactly
// what the return has to undo.
//
// It asserts the status itself rather than leaving that to its callers. A
// fixture that quietly handed back an unsettled payment would make every test
// built on it fail somewhere else, saying something else.
func (h *harness) settledPayment(t *testing.T) payment.Payment {
	t.Helper()
	return h.settle(t, h.submitCreditTransfer(t))
}

// settledCollection is the same thing in the other direction, and it exists
// because a return's direction is not the payment's. The bank that returns is
// the one that RECEIVED the instruction — the payee's bank on a push, the
// payer's on a pull — so a fixture with only credit transfers in it would leave
// half of that rule unmeasured.
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

// spendTheCredit is the payee sending the money straight back to the payer, and
// having that carried to finality too.
//
// It is the fixture for the two conditions a return can only meet after the
// money has MOVED AGAIN, and both of them are about a bank being unable to fund
// a leg it is asked for:
//
//   - the payee's own account is empty, so the clawback cannot be posted. On a
//     push that is the returning bank refusing before it sends;
//   - the payee's BANK's settlement account is empty, so the central bank cannot
//     reverse the reserves. That is the refusal that comes back RJCT.
//
// One helper for both because they are the same movement seen from two books: a
// customer paying money away takes it out of their bank's reserve and out of
// that bank's settlement account at the central bank, which is what a cut-off
// between the two banks does.
//
// It opens a cycle of its own, because the fixture's cut-off has already been
// reached by whatever settled the first payment and a payment with no open
// window is refused TM01 by the clearing house. Opened on h.net rather than
// through the clearing house: opening one is nobody's file, and closeCycle is what
// makes reaching the cut-off the clearing house's act.
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

// returnPayment sends a settled payment back, and fails the test if the bank
// that would send it refuses.
//
// Like submitCreditTransfer it does NOT wait: the returning bank's half builds
// a message and nothing else, and the money moves later, at the settlement
// agent. returnErr is for the tests that are asserting on the refusal.
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
// deposit register.
//
// It is what says a return actually gave the money back. The suspense helper
// answers a question about a bank's own clearing position; this one answers the
// question the payer would ask, which is the only one a return is about.
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
// idempotency key the domain gave it.
//
// By key and not by scanning descriptions, because the key is what identifies
// the leg — payment.PostReturnLegTx posts "<payment>:return-refund" in the
// payer's bank's book and "<payment>:return-claw" in the payee's — and a test
// that searched the text for what it is about to assert about the text would be
// asserting nothing.
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
// payment itself names.
//
// postingByKey's counterpart, for the reads whose subject is an id a payment
// carries rather than a key the domain composed. A retried return leg is posted
// under a key derived from the attempt it replaces, so its key is not something
// a test can spell — but the payment names the transaction, and whether THAT
// transaction still stands is the whole question. See payment.PostReturnLegTx.
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

// returnSentTo is the last pacs.004 an actor was handed, parsed.
//
// It is how a test reads what a return actually SAID, which is the one thing a
// pacs.004 exists to carry and the one thing no balance can show: two returns
// of the same payment for opposite reasons move exactly the same money.
func (h *harness) returnSentTo(t *testing.T, to iso20022.BIC) *iso20022.Pacs004 {
	t.Helper()
	env, err := iso20022.Unmarshal(h.lastMessageOfTypeTo(t, to, "pacs.004.001.09"))
	if err != nil {
		t.Fatalf("the last pacs.004 to %s does not parse: %v", to, err)
	}
	return env.Document.(*iso20022.Pacs004)
}

// payment reads the CLEARING HOUSE's copy of a payment, which is the only way to
// learn what became of it: Submit answers with the payment as its own bank left
// it, and everything after that happened at another actor.
//
// Whose copy is a question. Three institutions hold three rows for one payment
// and no institution can read another's, so "the payment" is not a thing a test
// can ask for. This one asks the clearing house because that is the institution
// whose copy runs the whole state machine — Accepted and Cleared are ITS facts,
// and a test about where a payment has got to in the network wants them.
//
// A test about what a BANK did needs bankPayment instead, and the shapes are
// what make the difference sharp: the clearing house's row has no leg columns at
// all, so a debtor leg read through here comes back empty rather than wrong.
func (h *harness) payment(t *testing.T, id payment.PaymentID) payment.Payment {
	t.Helper()
	p, err := h.net.GetPayment(context.Background(), id)
	if err != nil {
		t.Fatalf("GetPayment %s: %v", id, err)
	}
	return p
}

// bankPayment reads ONE BANK's own copy, which is where a leg is.
//
// A payment's legs are postings in a member's ledger and its copy is the only
// row that names them — payment.DebtorLegTx and CreditorLegTx are columns in the
// bank shape and in no other (store/sqlite/schema/bank/0001_init.sql). A test
// that reads a leg off h.payment is reading the clearing house's row, which has
// nowhere to hold one, and it fails saying the leg is missing rather than saying
// it asked the wrong institution.
//
// The bank also holds the status IT was told, which is not always the clearing
// house's: a bank that answered an instruction is never sent the ACCP, so its
// copy says Initiated while the network's says Accepted. Both are right. Which
// one a test wants is part of what it is asserting.
func (h *harness) bankPayment(t *testing.T, bic iso20022.BIC, id payment.PaymentID) payment.Payment {
	t.Helper()
	p, err := h.bank(bic).GetPayment(context.Background(), id)
	if err != nil {
		t.Fatalf("GetPayment %s at %s: %v", id, bic, err)
	}
	return p
}

// cycles is every clearing cycle this network holds, in the order it lists them.
// It is how a test reads what a cut-off left behind, which is the STATUS and not
// an id: a settled cycle is CycleSettled and a refused one is still CycleClosed.
// It cannot carry a SettlementID — that id is allocated inside the settlement
// agent's own unit of work in its own database, and the pacs.002 that comes back
// quotes the CYCLE, because the cycle is what the clearing house asked about.
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

// suspense is the balance of a bank's euro clearing suspense: money that has
// left a customer and has not yet settled between banks.
//
// It is what a rejection has to return to zero. A payment reversed at the
// clearing house but not at the payer's own bank leaves the payer short and the
// suspense holding money nobody will ever settle.
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
//
// A member cannot credit its own reserve account at the settlement agent; it
// asks, in a camt.050, and the answer arrives on a later download. Going through
// Deployment.Lodge would therefore leave the file queued until a day ran — and a
// day cuts every open cycle off, which is a fixture this package cannot build on.
//
// So the two halves are driven directly, which is what seed.builder.lodge does
// and for the same reason. The ROUND TRIP is not lost: it is what
// lodgement_test.go is about, and that suite drives the door and the day itself.
//
// The instruction the member's half renders is what the agent's half is handed,
// rather than one the fixture assembles: the two must agree about the account
// number and the reference.
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

// vaultCash is one bank's euro vault balance: the cash it is holding and has not
// placed on reserve.
//
// It exists for the deposit and lodgement measurements, which are the two acts
// that move it in opposite directions — cash in raises it, a lodgement lowers it
// — and both need a balance rather than a book set, because a book set cannot
// tell a posting from a read.
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
//
// It is NOT the central bank's record of the same account — that is
// ReserveBalance, read from the settlement agent's book — and the whole point of
// having both is that they can disagree. See payment.BankAccounts on the
// unreconciled interval.
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
//
// It is the only way to test what a receiver does with something it cannot
// parse. Everything else in this package goes through an upload or an enqueue
// that marshals — so an unparseable file could not exist, and FF01 would be a
// code with no path to it.
//
// Which door it uses follows from WHO the recipient is, and that is the topology
// rather than a convenience: a member bank is dialled by nobody, so bytes reach
// one by being put in its download queue at a host; a host is dialled by its
// subscribers, so bytes reach one by being uploaded.
//
// The order type is CST for a queued file, which is any label that is not C53 —
// a download of BTD takes everything else in the queue, so the label decides
// nothing here and the bytes are what the test is about.
func (h *harness) injectRaw(t *testing.T, from, to iso20022.BIC, raw []byte) {
	t.Helper()
	switch to {
	case h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC:
		client := ebics.NewClient(ebics.SubscriberID(from), h.urlOf(to))
		if _, err := client.Upload(context.Background(), ebics.CST, raw); err != nil {
			t.Fatalf("uploading raw bytes from %s to %s: %v", from, to, err)
		}
	default:
		if _, err := h.hostOf(t, from).Enqueue(ebics.SubscriberID(to), ebics.CST, raw); err != nil {
			t.Fatalf("queueing raw bytes at %s for %s: %v", from, to, err)
		}
	}
}

// upload puts one built message on the wire, from one institution to another,
// exactly as the sending institution would.
//
// It is what a test needs to make an institution answer a message no fixture
// flow produces: a pacs.004 the clearing house never asked for, a status about a
// payment nobody submitted. The bytes go up a real connection when the recipient
// is a HOST, and into a download queue when it is a member bank — because
// nothing is pushed at one.
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
		if _, err := h.hostOf(t, from).Enqueue(ebics.SubscriberID(to), orderType, raw); err != nil {
			t.Fatalf("queueing a %s at %s for %s: %v", orderType, from, to, err)
		}
	}
}

// hostOf is the EBICS side of one of the two institutions that has one, and
// fails on any other address: a member bank holds no host, because nothing is
// ever pushed at one.
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
// messagesSeen, in arrival order.
//
// It is how a test asserts on ONE conversation in a fixture that has already
// carried others: a return starts from a settled payment, so by the time it
// begins the wire is six messages deep and none of them is what the test is
// about. Copied under the lock, because listener goroutines are still appending.
func (h *harness) messagesFrom(mark int) []tappedMessage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]tappedMessage(nil), h.seen[mark:]...)
}

// lastMessageOfTypeTo is the raw bytes of the most recent message of one
// definition handed to one actor.
//
// Raw, and not parsed: what it is for is REDELIVERY — putting the very bytes an
// institution already handled back into its download queue, which is the only way to provoke
// what a queue does on its own in a real network. lastStatusTo parses because
// its callers are reading an answer; this one's caller is replaying a message.
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

// messagesSeen is how many messages actors have been handed.
//
// After a Drain that is also how many were SENT, because Drain returns only when
// nothing is in flight. Before one it is neither, which is why every test that
// counts drains first.
func (h *harness) messagesSeen() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.seen)
}

// assertLastStatusTo checks that the last message an actor received was a
// pacs.002 carrying a given reason code.
//
// The LAST one, because a bank in a finished flow has received several and only
// the most recent is the answer to what the test just did.
//
// The EMPTY code means "no reason at all", and it is a real assertion rather
// than a skipped one. payment.statusReasonOf omits StsRsnInf entirely unless the
// transaction was rejected, because StatusReasonChoice requires exactly one of a
// code and a proprietary text and an acceptance has neither — so "the reason
// element is absent" is the observable form of "this was not a rejection", and
// a settlement completing is the case that needs to say so. Matching an empty
// string against a present code would pass on nothing, which is why the two
// cases are separate here.
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
//
// It is a separate assertion from the reason code and not a second argument to
// one, because the two answer different questions and a test usually wants one
// of them. The status is what happened; the code is why, and only a rejection
// has one.
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
//
// pacs.009 and nothing else: it is the one message definition in this system
// whose parties are both banks, so counting the type IS counting settlement
// instructions.
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
