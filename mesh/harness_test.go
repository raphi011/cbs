package mesh

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/testenv"
)

// The harness every flow test in this package is written against.
//
// One harness, not one per test. The flows differ in what they assert, not in
// what they need: two banks that can address each other, a clearing house
// between them, a store that records which book each actor reached, and a way
// to wait for a conversation to finish. A second fixture beside this one would
// be a second answer to "what does this system look like", and the two would
// drift.
//
// # What it is NOT
//
// It does not stand in for the API. Mesh.Submit is what a bank's customer-facing
// layer calls, and the harness calls it directly; Task 14 is what puts an HTTP
// handler in front of it.

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
const (
	debtorIBAN   = "DE89370400440532013000"
	creditorIBAN = "IT60X0542811101000000123456"

	// unknownIBAN is well-formed and belongs to no account in this network. It
	// is the address TestCreditTransferToAnUnknownAccountComesBackAsAC01 sends
	// to, and it has to be well-formed: an IBAN that failed the schema's own
	// pattern would be refused by the codec on the way out, so the message would
	// never leave the debtor's bank and the creditor's bank would never get the
	// chance to answer AC01.
	unknownIBAN = "DE00000000000000000000"
)

// tappedMessage is one message an actor received: who from, who to, and the
// bytes.
type tappedMessage struct {
	from iso20022.BIC
	to   iso20022.BIC
	raw  []byte
}

// meshHarness is a seeded two-bank network with a mesh running over it.
type meshHarness struct {
	rec  *recordingStore
	net  *payment.Network
	mesh *Mesh
	cfg  Config

	debtor       *payment.Participant
	creditor     *payment.Participant
	debtorAcct   deposit.Account
	creditorAcct deposit.Account

	debtorBIC    iso20022.BIC
	creditorBIC  iso20022.BIC
	debtorPID    payment.ParticipantID
	creditorPID  payment.ParticipantID
	debtorBook   ledger.BookID
	creditorBook ledger.BookID

	mu   sync.Mutex
	seen []tappedMessage
}

// newMeshHarness builds the network, opens a clearing cycle for SEPA credit
// transfer, and starts the mesh.
func newMeshHarness(t *testing.T) *meshHarness {
	t.Helper()
	return newHarness(t, true)
}

// newMeshHarnessWithNoOpenCycle is the same network with no cut-off window open.
//
// A separate constructor rather than a flag on the first, because "there is no
// open cycle" is not a variation on the fixture, it is the condition the test
// that uses it exists to provoke: the clearing house refuses a payment it has
// nowhere to clear, and answers TM01.
func newMeshHarnessWithNoOpenCycle(t *testing.T) *meshHarness {
	t.Helper()
	return newHarness(t, false)
}

func newHarness(t *testing.T, openCycle bool) *meshHarness {
	t.Helper()
	ctx := context.Background()
	clock := func() time.Time { return testTime }

	h := &meshHarness{cfg: testConfig}
	h.rec = newRecordingStore(testenv.New(t, clock).Payment())
	h.net = payment.NewNetwork(h.rec, clock)

	var err error
	if h.debtor, err = h.net.AddParticipant(ctx, "Aurora Bank", "AURODEFFXXX", euroOnly); err != nil {
		t.Fatalf("AddParticipant Aurora: %v", err)
	}
	if h.creditor, err = h.net.AddParticipant(ctx, "Banca Verde", "VERDITMMXXX", euroOnly); err != nil {
		t.Fatalf("AddParticipant Verde: %v", err)
	}
	h.debtorAcct = h.openCustomer(t, h.debtor, "Alice", debtorIBAN)
	h.creditorAcct = h.openCustomer(t, h.creditor, "Bruno", creditorIBAN)
	if err := h.net.Deposit(ctx, h.debtor.ID, h.debtorAcct.ID, harnessFunding, "Opening deposit"); err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	h.debtorBIC, h.creditorBIC = h.debtor.BIC, h.creditor.BIC
	h.debtorPID, h.creditorPID = h.debtor.ID, h.creditor.ID
	h.debtorBook, h.creditorBook = h.debtor.BookID, h.creditor.BookID

	if openCycle {
		if _, err := h.net.OpenCycle(ctx, payment.SchemeSEPACT); err != nil {
			t.Fatalf("OpenCycle: %v", err)
		}
	}

	if h.mesh, err = New(h.net, h.cfg, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatalf("New: %v", err)
	}
	// Set before Start, which is the only moment at which writing it is safe:
	// see Mesh.tap.
	h.mesh.tap = h.record
	if err := h.mesh.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Drain FIRST, then Stop. Stop closes every inbox in one step before it
	// joins anybody, so a chain still in flight when it runs is CUT — the payer
	// debited and the pacs.002 that would have said so never sent. Draining
	// leaves Stop nothing to do but join. Both return dead letters and both are
	// reported: a shutdown that swallowed a handler's failure would let a test
	// pass over the very thing it was asserting.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := h.mesh.Drain(ctx); err != nil {
			t.Errorf("draining at shutdown: %v", err)
		}
		if err := h.mesh.Stop(ctx); err != nil {
			t.Errorf("stopping: %v", err)
		}
	})
	return h
}

// openCustomer opens a euro deposit account addressable by an IBAN.
func (h *meshHarness) openCustomer(t *testing.T, p *payment.Participant, name, iban string) deposit.Account {
	t.Helper()
	acct, err := p.Deposit.OpenAccount(context.Background(), p.CustomerSubledger, name, "EUR", p.ProductID, 0,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: iban})
	if err != nil {
		t.Fatalf("OpenAccount %s: %v", name, err)
	}
	return acct
}

// record is the tap: every message an actor is handed lands here.
func (h *meshHarness) record(to, from iso20022.BIC, raw []byte) {
	h.mu.Lock()
	h.seen = append(h.seen, tappedMessage{from: from, to: to, raw: raw})
	h.mu.Unlock()
}

// creditTransferRequest is the instruction the harness's payer gives its bank:
// the whole of the happy path, as a value a test can alter one field of.
func (h *meshHarness) creditTransferRequest(t *testing.T) payment.InitiatePaymentRequest {
	t.Helper()
	return h.creditTransferRequestTo(t, creditorIBAN)
}

// creditTransferRequestTo is the same instruction addressed somewhere else.
//
// The creditor's participant and account are the real ones and only the ADDRESS
// varies, which is what makes an unresolvable address a message the debtor's
// bank can build and send. That is the honest shape: a payer quotes an IBAN,
// their bank has no way to check whose it is, and the bank at the other end is
// the first party that can say. See TestCreditTransferToAnUnknownAccountComesBackAsAC01.
func (h *meshHarness) creditTransferRequestTo(t *testing.T, iban string) payment.InitiatePaymentRequest {
	t.Helper()
	return payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPACT,
		Debtor:      payment.PartyRef{Participant: h.debtorPID, Account: h.debtorAcct.ID, Identifier: h.debtorAcct.Identifiers[0]},
		Creditor:    payment.PartyRef{Participant: h.creditorPID, Account: h.creditorAcct.ID, Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: iban}},
		Amount:      harnessAmount,
		Description: "invoice 42",
	}
}

// submitCreditTransfer runs the payer's instruction through their own bank, and
// returns what that bank answered. It does NOT wait: the creditor's bank has not
// seen the payment yet, which is the whole point of the mesh.
func (h *meshHarness) submitCreditTransfer(t *testing.T) payment.Payment {
	t.Helper()
	return h.submitCreditTransferTo(t, creditorIBAN)
}

func (h *meshHarness) submitCreditTransferTo(t *testing.T, iban string) payment.Payment {
	t.Helper()
	p, err := h.mesh.Submit(context.Background(), h.creditTransferRequestTo(t, iban))
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	return p
}

// drain waits for every message in flight to be handled, and fails the test on
// a dead letter.
//
// No sleep, no Eventually, no retry: Drain blocks until nothing is in flight,
// which is what lets every test here read submit, drain, assert. A test that
// needed a duration would be reporting a bug in the flow, not in itself.
func (h *meshHarness) drain(t *testing.T) {
	t.Helper()
	if err := h.mesh.Drain(drainCtx(t)); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

// drainErr is drain for the tests that are ASSERTING on a dead letter: a message
// no actor could answer is the mesh's only way of saying so, and a test about
// that path has to be able to read it.
func (h *meshHarness) drainErr(t *testing.T) error {
	t.Helper()
	return h.mesh.Drain(drainCtx(t))
}

// closeCycle reaches the SEPA credit transfer cut-off.
//
// It drives the network directly rather than through an actor, because at Task
// 10 no actor closes a cycle: the clearing house's handlers move payments INTO
// one, and the cut-off is still an operator's act. Task 12 is where closing a
// cycle emits a pacs.009 and settlement becomes a conversation.
func (h *meshHarness) closeCycle(t *testing.T) payment.ClearingCycle {
	t.Helper()
	ctx := context.Background()
	cycles, err := h.net.ListCycles(ctx)
	if err != nil {
		t.Fatalf("ListCycles: %v", err)
	}
	for _, c := range cycles {
		if c.Scheme == payment.SchemeSEPACT && c.Status == payment.CycleOpen {
			closed, err := h.net.CloseCycle(ctx, c.ID)
			if err != nil {
				t.Fatalf("CloseCycle: %v", err)
			}
			return closed
		}
	}
	t.Fatal("no open SEPA credit transfer cycle to close")
	return payment.ClearingCycle{}
}

// payment reads a payment back out of the store, which is the only way to learn
// what became of it: Submit answers with the payment as its own bank left it,
// and everything after that happened at another actor.
func (h *meshHarness) payment(t *testing.T, id payment.PaymentID) payment.Payment {
	t.Helper()
	p, err := h.net.GetPayment(context.Background(), id)
	if err != nil {
		t.Fatalf("GetPayment %s: %v", id, err)
	}
	return p
}

// suspense is the balance of a bank's euro clearing suspense: money that has
// left a customer and has not yet settled between banks.
//
// It is what a rejection has to return to zero. A payment reversed at the
// clearing house but not at the payer's own bank leaves the payer short and the
// suspense holding money nobody will ever settle.
func (h *meshHarness) suspense(t *testing.T, id payment.ParticipantID) ledger.Amount {
	t.Helper()
	ctx := context.Background()
	p, err := h.net.GetParticipant(ctx, id)
	if err != nil {
		t.Fatalf("GetParticipant %s: %v", id, err)
	}
	accts, err := p.AccountsFor("EUR")
	if err != nil {
		t.Fatalf("AccountsFor EUR: %v", err)
	}
	bal, err := p.Ledger.BookBalance(ctx, accts.Suspense)
	if err != nil {
		t.Fatalf("BookBalance: %v", err)
	}
	return bal
}

// booksTouchedBy is every ledger book one actor's units of work reached, sorted.
func (h *meshHarness) booksTouchedBy(who iso20022.BIC) []ledger.BookID {
	return h.rec.touchedBy(who)
}

// allBooks is every book this network has: one per bank, the central bank's, and
// the network's own.
//
// It exists so that "only its own book" can be stated as a claim about the ones
// an actor did NOT reach, rather than only about the ones it did — and so that
// an expectation which really is "every bank's book" (the directory sweep; see
// TestWhichBooksEachBankActuallyReaches) is derived from the network rather than
// typed out.
func (h *meshHarness) allBooks() []ledger.BookID {
	out := []ledger.BookID{h.debtorBook, h.creditorBook, payment.CentralBankBook, ledger.NetworkBook}
	slices.Sort(out)
	return out
}

// bankBooks is allBooks without the two that belong to no bank: the central
// bank's, and the label network-scoped rows carry.
//
// Derived from allBooks rather than listed again, so that a third bank in the
// fixture reaches both at once.
func (h *meshHarness) bankBooks() []ledger.BookID {
	return slices.DeleteFunc(h.allBooks(), func(b ledger.BookID) bool {
		return b == payment.CentralBankBook || b == ledger.NetworkBook
	})
}

// injectRaw puts bytes into an actor's inbox without building a message.
//
// It is the only way to test what a receiver does with something it cannot
// parse. Everything else in this package goes through send, which marshals — so
// an unparseable message could not exist, and FF01 would be a code with no path
// to it.
func (h *meshHarness) injectRaw(t *testing.T, from, to iso20022.BIC, raw []byte) {
	t.Helper()
	if err := h.mesh.sendRaw(from, to, raw); err != nil {
		t.Fatalf("sendRaw to %s: %v", to, err)
	}
}

// messagesSeen is how many messages actors have been handed.
//
// After a Drain that is also how many were SENT, because Drain returns only when
// nothing is in flight. Before one it is neither, which is why every test that
// counts drains first.
func (h *meshHarness) messagesSeen() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.seen)
}

// assertLastStatusTo checks that the last message an actor received was a
// pacs.002 carrying a given reason code.
//
// The LAST one, because a bank in a finished flow has received several and only
// the most recent is the answer to what the test just did.
func (h *meshHarness) assertLastStatusTo(t *testing.T, to iso20022.BIC, want iso20022.StatusReason) {
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
	for _, s := range doc.FIToFIPmtStsRpt.TxInfAndSts {
		if s.StsRsnInf != nil && s.StsRsnInf.Rsn.Cd != nil && *s.StsRsnInf.Rsn.Cd == want {
			return
		}
	}
	t.Errorf("the last pacs.002 to %s carries no %s; its statuses are %+v", to, want, doc.FIToFIPmtStsRpt.TxInfAndSts)
}
