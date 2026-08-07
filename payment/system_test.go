package payment_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	. "github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/store/storetest"
	"github.com/raphi011/cbs/store/testenv"
)

// fixedTime is the instant returned by the test clock, matching the ledger
// package's own test clock.
var fixedTime = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

// testAsset is the asset these tests operate in. SEPA is a euro scheme, so a
// test bank that clears SEPA is a euro bank; euroOnly is the joining set that
// says so when the bank is founded.
const testAsset ledger.AssetCode = "EUR"

var euroOnly = []ledger.AssetCode{testAsset}

// testBIC is a structurally valid ISO 9362 BIC used as the default across
// these tests. There is no uniqueness constraint on it (see banks.bic's
// column comment), so a test bank sharing it with another is not automatically
// a fixture bug — except in the two situations where a bank IS its address.
//
// The first is an assertion that turns on telling two banks' BICs apart, which
// is what testBIC2 is for. The second is any fixture whose banks SETTLE: the
// central bank keys its own record of a member by BIC and by nothing else, so
// two banks sharing an address share one settlement account and one reserve
// balance between them. That is not a store rule that could be relaxed — it is
// what "the identifier between institutions is the BIC" means when the
// settlement agent keeps its own records. testBICs below is for those.
const testBIC iso20022.BIC = "BANKDEFFXXX"

// testBIC2 is a second, distinct BIC for fixtures where two banks' BICs must
// be tellable apart — setupTwoBanks uses it for Bank B so that a test planting
// one bank's BIC where the other's belongs (as
// TestSubmitDerivesTheCounterpartyAgentFromTheRoster does) can actually catch
// a derivation that silently passes the wrong value through.
const testBIC2 iso20022.BIC = "BANKGB2LXXX"

// testBICs are distinct addresses for the fixtures that build more than two
// banks and then settle between them.
//
// Those fixtures gave every bank testBIC, which was harmless while each bank
// carried its own note of its own settlement account. It stopped being harmless
// when SettleCycleTx and ReserveBalance began asking the central bank's own
// member row, which is keyed by address: three banks on one address became one
// member with one reserve, so an underfunded member could be covered by another
// bank's money and a settlement transaction's four entries all named one
// account.
var testBICs = []iso20022.BIC{testBIC, testBIC2, "BANKFRPPXXX", "BANKESMMXXX"}

// testSystem is what a fixture in this package holds: the clearing house's view
// of the network, embedded so that every network-scoped read reads as it always
// did, plus the factory the acts that belong to ONE institution go through.
//
// It exists because Task 18b gave payment.Network an identity. One *Network used
// to play every institution in these tests — a bank's submission, the receiving
// bank's answer, the clearing house's acceptance and the central bank's
// settlement, often inside one unit of work. It cannot now: a member's own act
// needs that member's network and the central bank's book is on the central
// bank's alone.
//
// The embedded network is the CLEARING HOUSE's, and that is a choice about which
// reads stay unchanged rather than a claim that the clearing house may make
// them. Each institution holds its own database since Task 18d, so a read that
// used to be "network-scoped" is now one of three institutions' — and the ones
// that stayed on this handle are the ones that were the clearing house's all
// along. bank() and cb() are the other two, and they are visible at every call
// site that uses them.
type testSystem struct {
	*Network
	nets *Networks
	// stores is the set the networks are minted over, for the assertions that
	// have to reach a database directly rather than through an institution.
	stores Stores
}

// bank is one member's own view: the network its acts are performed through,
// over that member's own database.
//
// Keyed by the BIC, because a bank's ParticipantID is its BIC since Task 18 (see
// AsBank) and every caller here now holds an address: the agents on a payment,
// the agent on a request, the agent on a settlement statement.
//
// It panics on a failure to open, which is not the usual shape for a helper in
// this file. Nothing here can recover from a bank's database refusing to open,
// every caller is one expression deep inside an assertion, and the alternative
// is a (t *testing.T) argument on a helper called several hundred times.
func (s *testSystem) bank(bic iso20022.BIC) *Network {
	net, err := s.nets.Bank(context.Background(), ParticipantID(bic))
	if err != nil {
		panic("payment_test: opening " + string(bic) + "'s store: " + err.Error())
	}
	return net
}

// cb is the settlement agent's view, and the only one holding the central
// bank's book of accounts.
func (s *testSystem) cb() *Network { return s.nets.CentralBank() }

// testCentralBankBIC is the address this fixture's settlement agent is reached
// at. It has no store row — a settlement agent is not a member of the scheme it
// settles — so, like the mesh's, it is configured rather than discovered.
//
// It exists because SettleCycle takes the LEGS since Task 18d and a leg names
// the agent at one end. See settleCycle.
const testCentralBankBIC iso20022.BIC = "CBANDEFFXXX"

// settleCycle instructs the settlement agent to discharge one cut-off, with the
// legs the clearing house would have put on the pacs.009.
//
// The two steps are one call here and two institutions in the mesh, which is
// what makes the helper worth having: the legs are rendered from the CLEARING
// HOUSE's closed cycle (payment.SettlementLegsOf) and handed to the CENTRAL
// BANK, because the agent holds no cycles table and settles what it was
// instructed rather than what it can look up.
func (s *testSystem) settleCycle(ctx context.Context, id CycleID) (Settlement, []SettlementStatement, error) {
	// The embedded Network is the clearing house's; see newTestSystem.
	c, err := s.GetCycle(ctx, id)
	if err != nil {
		return Settlement{}, nil, err
	}
	var asset ledger.AssetCode
	if scheme, ok := s.Scheme(c.Scheme); ok {
		asset = scheme.Asset()
	}
	return s.cb().SettleCycle(ctx, id, SettlementLegsOf(c, asset, testCentralBankBIC))
}

// cbBook is that book, for the assertions below that read the central bank's
// own accounts and transactions directly.
//
// It is two lines rather than one because Network.CentralBank returns an error
// now: on every network but the settlement agent's there is no such book, and a
// nil handle that panicked three frames down in ledger would be a worse answer
// than a refusal. Here the error cannot fire — s.cb() is the central bank's by
// construction — so the assertion is what says so.
func (s *testSystem) cbBook(t *testing.T) *ledger.Book {
	t.Helper()
	book, err := s.cb().CentralBank()
	assertNoError(t, err)
	return book
}

func testNetwork(t *testing.T) *testSystem {
	t.Helper()
	clock := func() time.Time { return fixedTime }
	stores := testenv.NewSet(t, clock)
	nets := NewNetworks(stores, clock)
	return &testSystem{Network: nets.ClearingHouse(), nets: nets, stores: stores}
}

// accountsOf returns a participant's internal accounts in the test asset,
// failing the test if it does not operate in it.
func accountsOf(t *testing.T, p *Bank) BankAccounts {
	t.Helper()
	accts, err := p.AccountsFor(testAsset)
	assertNoError(t, err)
	return accts
}

// initiate runs all four halves of an initiation — the submitting bank's, the
// receiving bank's, the clearing house's record and the clearing house's
// acceptance — and returns the payment Accepted in its scheme's open cycle.
//
// It is what InitiatePayment used to be, and it is a TEST helper rather than a
// method because there is deliberately no such method any more: a single call
// that validates both ends of a payment is precisely the thing sub-project 7b
// removed. Every test below that is not about the split uses this, so those
// tests keep asserting the end state the whole choreography produces; the
// tests that ARE about the split call the halves directly.
//
// # It was one Tx and it is now four, and it had to be
//
// One Tx for all three bought atomicity — a payment the far side refused left
// nothing behind — and that is not a thing the code under test can offer any
// more. A unit of work is ONE DATABASE's, each institution has its own, and a
// statement spanning two of them is what Task 18c removed. This helper held a
// transaction on the CLEARING HOUSE's store and ran two banks' halves inside it,
// which stopped working the moment those banks' rows lived elsewhere.
//
// What the loss costs a caller is nothing these tests were measuring: a failure
// in a later half leaves the earlier ones committed, and every test here
// assertNoErrors the whole call. It is seed.builder.initiate's shape, for
// seed.builder.initiate's reason, and the real thing is the mesh — three actors,
// three units of work, and a refusal after the debtor leg is posted is a
// reversal.
//
// The RECORD at the clearing house is the fourth act and is new: that
// institution has to be carrying the payment before it can take one into a
// cycle (RecordRelayedTx). In the mesh it is the moment the instruction is
// relayed on; here there is nothing to relay, so it stands alone.
func initiate(ctx context.Context, sys *testSystem, req InitiatePaymentRequest) (Payment, error) {
	// The SUBMITTING bank's own network, and it used to be sys — the clearing
	// house's. That worked while a payment's refs named their own banks and
	// checkPartyTx read the named one; the submitting side is resolved in this
	// network's own register now, so the institution has to be right. See
	// submitterOfReq.
	p, err := sys.bank(submitterOfReq(sys, req)).SubmitPayment(ctx, req)
	if err != nil {
		return Payment{}, err
	}
	relayed := relayedFrom(p)
	if err := sys.bank(receiverOf(sys, p)).AcceptInbound(ctx, p.ID, relayed); err != nil {
		return Payment{}, err
	}
	if _, err := sys.RecordRelayed(ctx, p.ID, relayed); err != nil {
		return Payment{}, err
	}
	return sys.AcceptAtCSM(ctx, p.ID)
}

// reject runs both halves of a rejection — the clearing house's transition and
// the debtor bank's reversal of its own leg — in one unit of work, and returns
// the Rejected payment.
//
// It is what RejectPayment used to be, and it is a TEST helper for the same
// reason initiate is: there is deliberately no method that plays both actors.
// The tests below that are not about the split use this, so they keep asserting
// the end state a whole rejection produces; the tests that ARE about the split
// call the halves directly and see the state between them.
func reject(ctx context.Context, sys *testSystem, id PaymentID, code iso20022.StatusReason, reason string) (Payment, error) {
	out, err := sys.RejectAtCSM(ctx, id, code, reason)
	if err != nil {
		return Payment{}, err
	}
	// Both banks record it on their own copy, and only the payer's gives any
	// money back — one act at each of them, because the decision and the reversal
	// cannot be separated once the row the guard reads is the acting bank's own.
	// See RejectAtBankTx. This helper used to run the clearing house's transition
	// and a ReverseDebtorLegTx in ONE transaction on the clearing house's store,
	// which was two institutions in one unit of work and a bank's book reached
	// through another institution's network.
	if _, err := sys.bank(out.DebtorDetails.Agent).RejectAtBank(ctx, id, code, reason); err != nil {
		return Payment{}, err
	}
	if other := out.CreditorDetails.Agent; other != out.DebtorDetails.Agent {
		if _, err := sys.bank(other).RejectAtBank(ctx, id, code, reason); err != nil {
			return Payment{}, err
		}
	}
	return out, nil
}

// setupTwoBanks creates two participant banks, opens a customer account at
// each (Alice at Bank A, Bob at Bank B), and funds Alice with 100000. The
// returned account IDs are deposit account IDs; their backing GL account IDs
// are resolved when checking book balances.
//
// Both accounts carry an IBAN — not because this fixture is about addressing,
// but because most of it is not: every scheme in play here is SEPA, so an
// account it cannot address is not a usable test fixture at all.
//
// Bank A and Bank B are given DISTINCT BICs (testBIC and testBIC2) rather than
// sharing testBIC the way single-bank fixtures do: this is the two-bank
// fixture that TestSubmitDerivesTheCounterpartyAgentFromTheRoster builds on,
// and a test that plants one bank's BIC where the other's belongs needs the
// two to actually differ or the plant is indistinguishable from the roster
// value it is meant to be wrong against.
func setupTwoBanks(t *testing.T, sys *testSystem) (a, b *Bank, alice, bob deposit.AccountID) {
	t.Helper()
	ctx := context.Background()

	a, err := storetest.Admit(ctx, sys.nets, "Bank A", testBIC, euroOnly)
	assertNoError(t, err)
	b, err = storetest.Admit(ctx, sys.nets, "Bank B", testBIC2, euroOnly)
	assertNoError(t, err)

	aliceAcct := openCustomer(t, ctx, a, "Alice", "SE89-BANKA-0001")
	bobAcct := openCustomer(t, ctx, b, "Bob", "SE89-BANKB-0001")

	fundAccount(t, ctx, sys, a, aliceAcct, 100000)
	return a, b, aliceAcct.ID, bobAcct.ID
}

// addParticipant admits a euro-only bank at the given address, failing the test
// on error. It is setupTwoBanks' admission, factored out for tests that want
// more than two banks or want to name them themselves.
//
// It goes through storetest.Admit, which runs the four acts in order with no mesh
// to carry the messages between them. The conversation itself is tested in
// mesh; what these tests need is a bank that is a Member.
//
// # The address is an argument now, and it had to become one
//
// It used to give every bank testBIC, and a caller admitting Aurora and Verde
// got two banks the fixture could tell apart by name. Since Task 18d a bank's
// address is its DATABASE — Networks.Bank routes on it, and a bank's
// ParticipantID is its BIC — so two banks on one address are one institution
// holding two rows: Verde opened its customer in Aurora's register, and every
// test asserting that one bank cannot see the other's account was asserting it
// about a bank that could. auroraBIC and verdeBIC are the pair those fixtures
// use; testBICs is for the ones building more than two.
func addParticipant(t *testing.T, ctx context.Context, sys *testSystem, name string, bic iso20022.BIC) *Bank {
	t.Helper()
	p, err := storetest.Admit(ctx, sys.nets, name, bic, euroOnly)
	assertNoError(t, err)
	return p
}

// auroraBIC and verdeBIC are the two addresses the Aurora/Verde fixtures below
// are built on. They are distinct because those fixtures' whole subject is one
// bank NOT seeing the other's register, which is a claim about two institutions
// and unmakeable on one address.
const (
	auroraBIC iso20022.BIC = "AURODEFFXXX"
	verdeBIC  iso20022.BIC = "VERDITMMXXX"
)

// openCustomer opens a customer deposit account at p, addressed by the given
// IBAN. It goes through p.Deposit.OpenAccount directly, rather than
// p.OpenCustomerAccount, because the identifier is a variadic argument that
// only the register method takes — mirroring how seed.go attaches an IBAN to
// every sample account.
func openCustomer(t *testing.T, ctx context.Context, p *Bank, name, iban string) deposit.Account {
	t.Helper()
	ident := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: iban}
	acct, err := p.Deposit.OpenAccount(ctx, p.CustomerSubledger, name, testAsset, p.ProductID, 0, ident)
	assertNoError(t, err)
	return acct
}

// openCustomerWithoutIdentifier opens a customer deposit account at p with no
// address at all — the fixture for proving a scheme refuses to route to an
// account it cannot address, rather than merely one whose quoted address is
// wrong.
func openCustomerWithoutIdentifier(t *testing.T, ctx context.Context, p *Bank, name string) deposit.Account {
	t.Helper()
	acct, err := p.OpenCustomerAccount(ctx, name, testAsset)
	assertNoError(t, err)
	return acct
}

// openCycle opens a clearing cycle for the given scheme, failing the test on
// error. It is runCycle's opening step, factored out for tests that only need
// a cycle open to initiate into — not the full open/close/settle round trip.
func openCycle(t *testing.T, ctx context.Context, sys *testSystem, scheme SchemeID) {
	t.Helper()
	_, err := sys.OpenCycle(ctx, scheme)
	assertNoError(t, err)
}

// fundAccount deposits amount into a customer account AND places the cash on
// reserve, failing the test on error.
//
// Two acts since Task 18a, and this helper runs both because what its callers
// want is a bank that can pay. A deposit reaches the bank's own vault and no
// institution but that bank; a bank settles out of central-bank money, and
// getting some is a lodgement.
//
// # It plays both institutions, and has to
//
// This package has no mesh, so there is nobody to send the camt.050 to and nobody
// to answer it. So the helper composes the two halves directly — the member's
// LodgeReservesTx and the settlement agent's ReceiveLodgementTx — exactly as
// runCycle composes settlement's three. What it must NOT do is call only the
// first: that posts the bank's reserve mirror and leaves the central bank's book
// untouched, so every fixture would carry a reserve that settlement, which reads
// the central bank's row, cannot see.
//
// The message is built and read on the way through rather than skipped, because
// the instruction the second half acts on is the one the first half rendered.
// Composing the two Go calls and passing the struct straight across would test a
// path no real lodgement takes and would miss anything ReadLodgement refuses.
//
// takeCashIn is the deposit alone, for the tests that want a bank holding cash it
// has not lodged.
func fundAccount(t *testing.T, ctx context.Context, sys *testSystem, p *Bank, acct deposit.Account, amount ledger.Amount) {
	t.Helper()
	takeCashIn(t, ctx, sys, p, acct, amount)
	lodgeReserves(t, ctx, sys, p, assetOfAccount(t, ctx, sys, p, acct), amount)
}

// takeCashIn is the deposit half alone: the customer's balance rises and the bank
// holds the cash.
//
// Through the BANK's own network, because a deposit is the bank's act on its own
// register. It went through sys.Network — the clearing house's — while there was
// one store, which was the same defect the seed's initiate carried: the act
// looked identical because both views reached the same database.
func takeCashIn(t *testing.T, ctx context.Context, sys *testSystem, p *Bank, acct deposit.Account, amount ledger.Amount) {
	t.Helper()
	assertNoError(t, sys.bank(p.BIC).Deposit(ctx, p.ID, acct.ID, amount, "opening deposit"))
}

// depositAccount re-reads a customer account from its id, for the fixtures that
// hold only the id and need the account fundAccount takes.
func depositAccount(t *testing.T, ctx context.Context, p *Bank, id deposit.AccountID) deposit.Account {
	t.Helper()
	got, err := p.Deposit.GetAccount(ctx, id)
	assertNoError(t, err)
	return got
}

// assetOfAccount is the asset a customer account is denominated in, which is the
// asset its deposit lands in and therefore the one to lodge.
func assetOfAccount(t *testing.T, ctx context.Context, sys *testSystem, p *Bank, acct deposit.Account) ledger.AssetCode {
	t.Helper()
	got, err := p.Deposit.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	return got.Asset
}

// lodgementSeq makes each fixture lodgement's reference unique. See lodgeReserves.
var lodgementSeq atomic.Int64

// lodgeReserves runs both halves of a lodgement in this package, where there is no
// mesh to carry the message between them.
//
// It renders the camt.050, reads it back and hands the instruction to the
// settlement agent's half — so the fixture exercises the real translation rather
// than passing a struct across a boundary no message crossed. See fundAccount.
func lodgeReserves(t *testing.T, ctx context.Context, sys *testSystem, p *Bank, asset ledger.AssetCode, amount ledger.Amount) {
	t.Helper()

	// The reference has to be unique per lodgement, and a description of the
	// lodgement is not: LodgeReservesTx keys its posting on it, so a fixture that
	// derived the key from the bank, the asset and the amount would fail the
	// second time one bank lodged the same amount twice — which is a collision in
	// this helper and not a defect in the domain. Mesh.nextMsgID is what supplies
	// a unique one in the running system; this counter is its stand-in.
	mc := MessageContext{
		From:  p.BIC,
		To:    "CBSEDEFFXXX",
		MsgID: fmt.Sprintf("%s-lodge-%s-%d", p.ID, asset, lodgementSeq.Add(1)),
		Now:   sys.Now(),
	}
	_, env, err := sys.bank(p.BIC).LodgeReserves(ctx, asset, amount, mc)
	assertNoError(t, err)

	doc, ok := env.Document.(*iso20022.Camt050)
	if !ok {
		t.Fatalf("LodgeReserves built %T, want *iso20022.Camt050", env.Document)
	}
	in, err := ReadLodgement(env.AppHdr, doc)
	assertNoError(t, err)
	_, err = sys.cb().ReceiveLodgement(ctx, in)
	assertNoError(t, err)
}

// TestALodgementQuotingNoAccountIsRefused is the guard's own "does not apply"
// value, closed.
//
// ReceiveLodgementTx compares the account a camt.050 names against the one the
// agent actually holds, and refuses a disagreement — a member and its settlement
// agent disagreeing about which account a lodgement credits is exactly what that
// comparison is for. It read `in.Account != "" && in.Account != held`, so an
// instruction quoting NO account walked past the check the doc calls a check.
// That is the shape this repository has met five times: a guard closing a hole
// and leaving its own empty value open.
//
// Nothing was ever misdirected by it. The account posted to is the agent's own
// row and never the quoted one, which is why settlementAccountTx is read first.
// What was lost is the ASSERTION, and the loss is only reachable the way this
// test reaches it: ReadLodgement refuses a camt.050 with no CdtrAcct/Id/Othr/Id,
// so no MESSAGE produces one. ReceiveLodgement is exported on settlementOps and
// callable with an instruction nobody parsed, which is the door this closes.
func TestALodgementQuotingNoAccountIsRefused(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, _, alice, _ := setupTwoBanks(t, sys)
	takeCashIn(t, ctx, sys, a, depositAccount(t, ctx, a, alice), 50000)

	// The reserve BEFORE, because setupTwoBanks lodges: what this asserts is
	// that the refused instruction moved nothing, not that the account is empty.
	before, err := sys.cb().ReserveBalance(ctx, a.BIC, testAsset)
	assertNoError(t, err)

	// Everything a real lodgement carries except the account it credits.
	_, err = sys.cb().ReceiveLodgement(ctx, LodgementInstruction{
		Ref:    "lodge-no-account",
		BIC:    a.BIC,
		Agent:  "CBSEDEFFXXX",
		Asset:  testAsset,
		Amount: 1000,
	})
	assertError(t, err, ErrSettlementAccountReplaced)

	// And nothing was credited. The refusal is what stops it, not the posting
	// failing afterwards: a lodgement that got past the guard would have posted
	// into the agent's own row perfectly happily, which is why the assertion
	// above is the whole of the defect.
	after, err := sys.cb().ReserveBalance(ctx, a.BIC, testAsset)
	assertNoError(t, err)
	assertEqual(t, "the member's reserve after a lodgement naming no account", after, before)
}

// runCycle opens, closes, and settles a cycle for the given scheme, returning
// the settled settlement.
//
// It plays every institution, and has to. Settlement is no longer one of them,
// and it is now the smallest of the three: the settlement agent posts its
// netting transaction and hands back one statement per member; each member books
// its own mirror leg from the statement it is sent; and each PAYEE's bank
// releases its own customer's money out of its own suspense. A test that only
// called SettleCycle would leave every bank's suspense holding the batch, every
// reserve mirror unmoved and every payee unpaid, which is not a settled cut-off
// at all. See bookTheAdvices and payTheCreditors, and seed's builder.settle,
// which is the same composite made for the same reason.
func runCycle(t *testing.T, sys *testSystem, scheme SchemeID, submit func()) Settlement {
	t.Helper()
	ctx := context.Background()
	cyc, err := sys.OpenCycle(ctx, scheme)
	assertNoError(t, err)
	submit()
	_, err = sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)
	st, statements, err := sys.settleCycle(ctx, cyc.ID)
	assertNoError(t, err)
	bookTheAdvices(t, sys, statements)
	payTheCreditors(t, sys, cyc.ID)
	return st
}

// returnWholePayment is every institution's half of an R-transaction, played in
// order and each in its OWN unit of work: the returning bank's customer leg, the
// settlement agent's reserve reversal, both members' reserve mirrors, and the
// other bank's customer leg.
//
// It is the test's stand-in for the four hops the mesh carries, and it replaces
// ReturnPaymentTx, which used to be one call that did all of this and which Task
// 16e deleted — no institution may post in another's book, and that function
// was three institutions in one unit of work. What these tests are about is
// where the money ends up, which is unchanged; each of them says so at the
// accounts rather than through the call, which is why moving the composition
// into the fixture leaves their assertions alone.
//
// Separate units of work, unlike seed's builder.returnPayment, and the
// difference is what each is for: the seed builds a fixed scenario and wants the
// whole return or none of it, while these tests are about the domain calls and
// one of them (TestReturnBeforeSettleIsRefused, in
// TestPaymentStateMachineRefusesIllegalTransitions) asserts that the FIRST half
// refuses on its own.
//
// The instruction is built from the payment row, as seed's is, because this
// package has no messages. See payment.ReadReturn for where the mesh gets the
// same values from instead.
func returnWholePayment(ctx context.Context, sys *testSystem, id PaymentID, reason string) (Payment, error) {
	p, err := sys.GetPayment(ctx, id)
	if err != nil {
		return Payment{}, err
	}
	scheme, ok := sys.Scheme(p.Scheme)
	if !ok || !scheme.AllowsReturn() {
		return Payment{}, ErrSchemeUnsupportedReturn
	}
	returner := ReturnerOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent)
	other := p.DebtorDetails.Agent
	if other == returner {
		other = p.CreditorDetails.Agent
	}
	if _, err := sys.bank(returner).PostReturnLeg(ctx, id, reason); err != nil {
		return Payment{}, err
	}
	statements, err := sys.cb().SettleReturn(ctx, ReturnInstruction{
		PaymentID:     p.ID,
		EndToEndID:    p.EndToEndID,
		DebtorAgent:   p.DebtorDetails.Agent,
		CreditorAgent: p.CreditorDetails.Agent,
		Amount:        p.Amount,
		Asset:         scheme.Asset(),
		Reason:        reason,
	})
	if err != nil {
		return Payment{}, err
	}
	for _, st := range statements {
		if _, err := sys.bank(st.Agent).PostSettlementAdvice(ctx, AdvisedMovement{
			Account:        st.Account,
			Asset:          st.Asset,
			Movement:       st.Movement,
			ClosingBalance: st.ClosingBalance,
			Reference:      st.Reference,
			ValueDate:      st.ValueDate,
		}); err != nil {
			return Payment{}, err
		}
	}
	// The OTHER bank's leg is the SECOND one and takes that bank's copy to
	// Returned; see PostReturnLegTx on position in the conversation.
	out, err := sys.bank(other).PostReturnLeg(ctx, id, reason)
	if err != nil {
		return Payment{}, err
	}
	// And the two institutions that learn it only by being told: the bank that
	// ASKED for the return, whose own leg was the first and therefore not the
	// one that closes the payment, and the clearing house, which carried the
	// pacs.004 and posts nothing. In the mesh both are sent the settlement
	// agent's ACSC; here, as in the seed, they are told directly.
	//
	// The fixture ran neither, which is why the returner's copy and the clearing
	// house's sat at Settled for ever and why this helper's own tests could only
	// see a return in one bank's log.
	if _, err := sys.bank(returner).CompleteReturn(ctx, id); err != nil {
		return Payment{}, err
	}
	if _, err := sys.CompleteReturn(ctx, id); err != nil {
		return Payment{}, err
	}
	return out, nil
}

// bookTheAdvices is every member's half of a cut-off: each books the mirror leg
// the statement it was sent advises.
//
// It is the test's stand-in for the messages the mesh carries — one camt.053 per
// member, and each member calling PostSettlementAdvice for itself. This package
// has no mesh and no actors, so it plays the members the way seed's builder does.
func bookTheAdvices(t *testing.T, sys *testSystem, statements []SettlementStatement) {
	t.Helper()
	ctx := context.Background()
	for _, st := range statements {
		_, err := sys.bank(st.Agent).PostSettlementAdvice(ctx, AdvisedMovement{
			Account:        st.Account,
			Asset:          st.Asset,
			Movement:       st.Movement,
			ClosingBalance: st.ClosingBalance,
			Reference:      st.Reference,
			ValueDate:      st.ValueDate,
		})
		assertNoError(t, err)
	}
}

// payTheCreditors is every institution's half of a cut-off, once the reserves
// have moved: the clearing house's own copies, and then each of the two banks'.
//
// It is the test's stand-in for the clearing house's ACSC fan-out — one pacs.002
// per payment per bank, and each bank calling SettleAtBank for itself. The cycle
// is re-read for its payment list because the settlement does not carry one: a
// settlement agent answers per MEMBER and cannot enumerate the batch, which is
// why the fan-out is the clearing house's.
//
// # THREE institutions act, and the name is now half the story
//
// It called SettleAtBank at the creditor's bank alone, because that was the only
// institution with anything left to do: the clearing house had already written
// Settled onto the row all three shared, and the payer's bank had no leg. Each
// holds its own copy now.
//
//   - The clearing house marks its own copies and its own cycle (SettleAtCSM).
//     Nothing else can, and a test fixture that skipped it left a cycle Closed
//     for ever.
//   - The PAYEE's bank pays its customer, which is the posting the old name is
//     about.
//   - The PAYER's bank posts nothing and still has to be told, because its copy
//     would otherwise say Initiated for ever — and a return is an edge from
//     Settled, so it would then refuse to return a payment it could not see had
//     settled. See csm.tellSettled in mesh, which is this loop with messages.
//
// Each leg is its OWN unit of work, and that is the substance rather than the
// shape of the loop: one payee's closed account, or one payment the ledger
// refuses, now fails alone instead of taking the cut-off down.
func payTheCreditors(t *testing.T, sys *testSystem, id CycleID) {
	t.Helper()
	ctx := context.Background()
	settled, err := sys.SettleAtCSM(ctx, id)
	assertNoError(t, err)
	for _, p := range settled {
		for _, agent := range []iso20022.BIC{p.CreditorDetails.Agent, p.DebtorDetails.Agent} {
			_, err := sys.bank(agent).SettleAtBank(ctx, p.ID)
			assertNoError(t, err)
		}
	}
}

// bookBalance returns the GL book balance of an arbitrary ledger account.
func bookBalance(t *testing.T, l *ledger.Book, acct ledger.AccountID) ledger.Amount {
	t.Helper()
	bal, err := l.BookBalance(context.Background(), acct)
	assertNoError(t, err)
	return bal
}

// suspenseBalance returns a bank's clearing-suspense balance in the test asset,
// looked up by participant id. It is where a payer's money sits between the
// debtor leg and settlement, so it is the balance that says whether an actor
// touched the debtor bank's own book.
func suspenseBalance(t *testing.T, n *testSystem, bic iso20022.BIC) ledger.Amount {
	t.Helper()
	// Through the named bank's own network: the row and the book are both its
	// own, and the clearing house has no table for either.
	return bookBalance(t, mustGetBank(t, context.Background(), n, ParticipantID(bic)).Ledger,
		accountsOf(t, mustGetBank(t, context.Background(), n, ParticipantID(bic))).Suspense)
}

// customerBalance returns the book balance of a customer deposit account at a
// participant, resolved through the participant's deposit layer.
func customerBalance(t *testing.T, p *Bank, acct deposit.AccountID) ledger.Amount {
	t.Helper()
	bal, err := p.Deposit.GetBalance(context.Background(), acct)
	assertNoError(t, err)
	return bal.Book
}

// assertReserveMirror checks that a bank's own reserve asset equals the
// central bank's view of that bank's reserve.
func assertReserveMirror(t *testing.T, sys *testSystem, p *Bank) {
	t.Helper()
	own := bookBalance(t, p.Ledger, accountsOf(t, p).Reserve)
	cb, err := sys.cb().ReserveBalance(context.Background(), p.BIC, testAsset)
	assertNoError(t, err)
	assertEqual(t, "reserve mirror for "+p.Name, own, cb)
}

// ---------------------------------------------------------------------------
// SEPA Credit Transfer
// ---------------------------------------------------------------------------

func TestSCT_HappyPath(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	var pay Payment
	runCycle(t, sys, SchemeSEPACT, func() {
		var err error
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme:          SchemeSEPACT,
			Debtor:          PartyRef{Account: alice},
			Creditor:        PartyRef{Account: bob},
			Amount:          30000,
			Description:     "Invoice 42",
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
			DebtorDetails:   PartyDetails{Agent: a.BIC}})
		assertNoError(t, err)
		assertEqual(t, "status after initiation", pay.Status, Accepted)
		// Debtor leg is value-dated to settlement (T+1).
		assertEqual(t, "value date", pay.ValueDate, fixedTime.Add(24*time.Hour))
		// Alice paid immediately; Bob not yet credited.
		assertEqual(t, "alice after init", customerBalance(t, a, alice), 70000)
		assertEqual(t, "bob after init", customerBalance(t, b, bob), 0)
	})

	// After settlement the money has arrived and suspense is flat.
	assertEqual(t, "alice final", customerBalance(t, a, alice), 70000)
	assertEqual(t, "bob final", customerBalance(t, b, bob), 30000)
	assertEqual(t, "bank A suspense", bookBalance(t, a.Ledger, accountsOf(t, a).Suspense), 0)
	assertEqual(t, "bank B suspense", bookBalance(t, b.Ledger, accountsOf(t, b).Suspense), 0)
	assertEqual(t, "bank A reserve", bookBalance(t, a.Ledger, accountsOf(t, a).Reserve), 70000)
	assertEqual(t, "bank B reserve", bookBalance(t, b.Ledger, accountsOf(t, b).Reserve), 30000)
	assertReserveMirror(t, sys, a)
	assertReserveMirror(t, sys, b)

	got, err := sys.GetPayment(ctx, pay.ID)
	assertNoError(t, err)
	assertEqual(t, "status settled", got.Status, Settled)
}

// TestASettlementIntoAClosedAccountGoesToUnclaimedBalances is the fix for the
// ruling SettleCycleTx and the return both recorded and neither could make.
//
// A payee who empties and closes their account between their bank's acceptance
// and the cut-off used to be credited INTO the closed account: Close requires a
// zero balance, no withdrawal reaches a closed account, and Closed is terminal,
// so the money stranded for ever. The check that would have caught it was
// unaffordable while settlement was one unit of work over the batch — refusing
// took the whole cut-off down for one retail customer, with no route out.
//
// It is affordable now because one payment at one bank fails on its own, and the
// money has somewhere to go.
func TestASettlementIntoAClosedAccountGoesToUnclaimedBalances(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	pay, err := initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 30000,
		Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	assertNoError(t, err)

	// Bob closes after his bank has already accepted the payment and the
	// clearing house has already taken it into the cut-off. His balance is zero,
	// which is what Close requires, and the money on its way to him is in his
	// bank's suspense rather than in his account.
	closeCreditorAccount(t, sys, pay)

	_, err = sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)

	// 1. The batch is not taken down by one closed account: the cut-off is the
	//    settlement agent's netting transaction, and the closed account is not
	//    in it. Bob's bank meets it afterwards, on its own, when it comes to
	//    pay its own customer.
	_, _, err = sys.settleCycle(ctx, cyc.ID)
	assertNoError(t, err)
	_, err = sys.bank(b.BIC).SettleAtBank(ctx, pay.ID)
	assertNoError(t, err)

	// 2. The payment settled, because it did: the reserves moved and Bob's bank
	//    has been paid. Which of that bank's accounts holds the money afterwards
	//    is between the bank and Bob.
	got, err := sys.GetPayment(ctx, pay.ID)
	assertNoError(t, err)
	assertEqual(t, "status", got.Status, Settled)

	// 3. And that account is the unclaimed-balances one, not Bob's.
	assertEqual(t, "bank B unclaimed balances", bookBalance(t, b.Ledger, accountsOf(t, b).Unclaimed), 30000)
	assertEqual(t, "bob's closed account", customerBalance(t, b, bob), 0)
}

// TestASettlementStoreFailureDoesNotRouteMoneyToUnclaimedBalances is the guard
// on the guard above: it pins that only a CLOSED account diverts a credit, and
// that a store that could not answer diverts nothing.
//
// The hazard is specific and it is not visible from PostCreditorLegTx alone.
// glAccountTx collapses every error from its read into ErrAccountNotInParticipant
// — a dropped connection, a scan error, a cancelled context all arrive as that
// one sentinel — so a guard written as "return unless it is
// ErrAccountNotInParticipant" cannot fire, and a bank that failed to READ its
// own customer's account would credit its unclaimed balances, mark the payment
// Settled and commit. The money would be in the wrong account of the right bank,
// and the only record of why would be a description string.
//
// The payee's account was already resolved once, when that bank accepted the
// payment. A failure to resolve it now is not a statement about the account, so
// the leg fails — which is retriable, and is now retriable on its own, because
// the cut-off it used to take down is a different institution's unit of work —
// rather than routing money.
//
// It is the settlement-time twin of the defect Task 14 fixed in checkPartyTx,
// and TestAcceptInboundDoesNotBlameTheSenderForAStoreFailure is its sibling.
//
// # What this does NOT assert, and why
//
// Not the error's IDENTITY. Its sibling can insist on errors.Is(err, dropped),
// because checkPartyTx was fixed to stop collapsing; glAccountTx still does, and
// unwinding that is a change to a function creditorSideTx and debtorSideTx also
// call. So what comes back here is ErrAccountNotInParticipant over a store that
// never said any such thing. That is a real residue and it is named rather than
// papered over — but it is a residue in what the operator is TOLD, and this test
// is about where the money went, which is the part that cannot be retried.
func TestASettlementStoreFailureDoesNotRouteMoneyToUnclaimedBalances(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	pay, err := initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 30000,
		Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	assertNoError(t, err)
	_, err = sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)
	_, _, err = sys.settleCycle(ctx, cyc.ID)
	assertNoError(t, err)

	// The same store, decorated only from here on: the payment had to clear and
	// the reserves had to move before Bob's bank had a leg to post. The failure
	// under test is that bank's own, in its own unit of work, and the settlement
	// agent is not in it at all.
	dropped := errors.New("connection reset by peer")
	broken := NewNetwork(failingUpdateStore{Store: sys.Store(), accountErr: dropped},
		func() time.Time { return fixedTime }, AsBank(b.ID))

	if _, err := broken.SettleAtBank(ctx, pay.ID); err == nil {
		t.Error("a creditor leg over a store that could not read the payee's account succeeded; " +
			"a read that cannot answer is not permission to put the money somewhere else")
	}
	// The money is the part that cannot be retried, so it is the part asserted:
	// none of it reached the unclaimed-balances account.
	assertEqual(t, "bank B unclaimed balances after a failed read", bookBalance(t, b.Ledger, accountsOf(t, b).Unclaimed), 0)
	// And the payment is where it was, so this one leg can simply be posted
	// again. That is the unit that is now retriable: the cut-off is settled and
	// final, and one bank's failure to pay one customer does not unwind it.
	after, err := sys.GetPayment(ctx, pay.ID)
	assertNoError(t, err)
	assertEqual(t, "payment status after a failed creditor leg", after.Status, Cleared)
}

// TestReturningAPaymentThatSettledIntoUnclaimedBalancesReleasesTheLiability is
// the other end of the diversion above, and it was a money bug.
//
// The diversion made "the payee's bank was paid" and "the payee was paid" two
// different facts for the first time. The return did not know that, and
// there was nothing on the payment for it to know it FROM: it debited the payee's
// GL account, which for a diverted payment had never been credited. Measured
// before Payment.CreditorLegAccount existed:
//
//	AFTER SETTLE:  bankB unclaimed=30000  bob=0      bankB reserve=30000
//	AFTER RETURN:  bankB unclaimed=30000  bob=-30000 bankB reserve=0
//
// Three things wrong at once. Bob's CLOSED account held minus 30000 — an
// overdraft nobody granted, on an account that can neither be drawn on nor
// closed again. The unclaimed-balances liability was never released, so the bank
// still owed money it had just paid back. And the reserves went out anyway, which
// is the half that made the other two invisible: two liabilities that net to zero
// keep the book balanced, so no ledger guard fires. checkSufficientBalance does
// not refuse a Liability account going negative — that is what an overdrawn
// deposit IS — so nothing in the ledger was ever going to catch this.
//
// The fix is a fact recorded at settlement rather than re-derived at return:
// PostCreditorLegTx writes the account it actually credited onto the payment. It
// cannot be re-derived, and that is the substance. An account open at settlement
// and closed afterwards looks identical, TODAY, to one closed at settlement, so a
// return that re-checked the status would claw back from the wrong account in
// exactly the case that matters.
func TestReturningAPaymentThatSettledIntoUnclaimedBalancesReleasesTheLiability(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	pay, err := initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 30000,
		Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	assertNoError(t, err)
	closeCreditorAccount(t, sys, pay)

	_, err = sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)
	_, statements, err := sys.settleCycle(ctx, cyc.ID)
	assertNoError(t, err)
	bookTheAdvices(t, sys, statements)
	_, err = sys.bank(b.BIC).SettleAtBank(ctx, pay.ID)
	assertNoError(t, err)

	// Where the money is before the return, and it is not with Bob.
	assertEqual(t, "bank B unclaimed after settlement", bookBalance(t, b.Ledger, accountsOf(t, b).Unclaimed), 30000)
	assertEqual(t, "bank B reserve after settlement", bookBalance(t, b.Ledger, accountsOf(t, b).Reserve), 30000)
	assertEqual(t, "bob after settlement", customerBalance(t, b, bob), 0)

	returned, err := returnWholePayment(ctx, sys, pay.ID, "creditor account is closed")
	assertNoError(t, err)
	assertEqual(t, "status", returned.Status, Returned)

	// Bob's closed account is not touched by a return of money it never
	// received. This is the assertion the old code failed at minus 30000.
	assertEqual(t, "bob after the return", customerBalance(t, b, bob), 0)
	// And the liability the bank took on when it could not pay him is released,
	// because the money went back to the payer instead. A return of a diverted
	// payment is the bank discharging that obligation to the ONLY other party
	// with a claim on it.
	assertEqual(t, "bank B unclaimed after the return", bookBalance(t, b.Ledger, accountsOf(t, b).Unclaimed), 0)

	// The reserves unwind exactly as they do for an undiverted return: this is
	// the half that was already right, and it is asserted so that a fix which
	// released the liability by NOT paying the reserves back would not pass.
	assertEqual(t, "bank B reserve after the return", bookBalance(t, b.Ledger, accountsOf(t, b).Reserve), 0)
	assertEqual(t, "alice refunded", customerBalance(t, a, alice), 100000)
	assertReserveMirror(t, sys, a)
	assertReserveMirror(t, sys, b)
}

// PSD2 Art. 87(2): the payer's debit value date may be no earlier than the
// moment the amount leaves the account, so the customer's leg must not share
// the suspense leg's settlement-date value date.
func TestInitiateValueDatesTheCustomerLegToTheDebit(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	p, err := initiate(ctx, sys, InitiatePaymentRequest{
		Scheme:          SchemeSEPACT,
		Debtor:          PartyRef{Account: alice},
		Creditor:        PartyRef{Account: bob},
		Amount:          10000,
		Description:     "Rent",
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	assertNoError(t, err)

	// The leg is on the PAYER'S BANK's copy and on no other: it is that bank's
	// own posting, in that bank's own book, and the clearing house's row — which
	// is what initiate returns — has no leg columns at all. See mustGetPaymentAt.
	atA := mustGetPaymentAt(t, ctx, sys.bank(a.BIC), p.ID)
	posted, err := a.Ledger.GetTransaction(ctx, atA.DebtorLegTx)
	assertNoError(t, err)
	assertEqual(t, "transaction value date is the settlement date", posted.ValueDate, atA.ValueDate)

	debtorAcct, err := a.Deposit.GetAccount(ctx, alice)
	assertNoError(t, err)
	debtorGL := debtorAcct.GLAccount

	// The two-way split below only names the legs correctly if there are
	// exactly two of them: with a third, "not the debtor's account" would
	// silently pick whichever came last.
	if len(posted.Entries) != 2 {
		t.Fatalf("debtor leg has %d entries, want 2 (customer and suspense)", len(posted.Entries))
	}
	var customer, suspense ledger.Entry
	for _, e := range posted.Entries {
		if e.AccountID == debtorGL {
			customer = e
		} else {
			suspense = e
		}
	}

	if !customer.ValueDate.Equal(posted.BookingDate) {
		t.Errorf("customer leg value date = %v, want the booking date %v (PSD2 Art. 87(2))",
			customer.ValueDate, posted.BookingDate)
	}
	if !suspense.ValueDate.Equal(p.ValueDate) {
		t.Errorf("suspense leg value date = %v, want the settlement date %v", suspense.ValueDate, p.ValueDate)
	}
	if customer.ValueDate.Equal(suspense.ValueDate) {
		t.Fatal("the two legs must not share a value date; that is the whole point")
	}
}

func TestSCT_Netting(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	// Fund Bob so Bank B can also be a payer.
	fundAccount(t, ctx, sys, b, depositAccount(t, ctx, b, bob), 50000)

	st := runCycle(t, sys, SchemeSEPACT, func() {
		_, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 30000,
			Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
			DebtorDetails:   PartyDetails{Agent: a.BIC}})
		assertNoError(t, err)
		_, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 10000,
			Debtor: PartyRef{Account: bob}, Creditor: PartyRef{Account: alice},
			CreditorDetails: PartyDetails{Agent: a.BIC, Name: "Alice"},
			DebtorDetails:   PartyDetails{Agent: b.BIC}})
		assertNoError(t, err)
	})

	// Net: A owes 30000, receives 10000 => net -20000; B is the mirror +20000.
	assertEqual(t, "net A", st.NetPositions[a.BIC], -20000)
	assertEqual(t, "net B", st.NetPositions[b.BIC], 20000)

	// Gross customer movements still apply: Alice -30000 +10000, Bob -10000 +30000.
	assertEqual(t, "alice", customerBalance(t, a, alice), 80000)
	assertEqual(t, "bob", customerBalance(t, b, bob), 70000)
	// Reserves moved only by the net.
	assertEqual(t, "bank A reserve", bookBalance(t, a.Ledger, accountsOf(t, a).Reserve), 80000)
	assertEqual(t, "bank B reserve", bookBalance(t, b.Ledger, accountsOf(t, b).Reserve), 70000)
	assertReserveMirror(t, sys, a)
	assertReserveMirror(t, sys, b)
}

func TestSCT_InsufficientFunds(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 150000, // more than Alice has
		Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	assertError(t, err, deposit.ErrInsufficientAvailable)
}

// ---------------------------------------------------------------------------
// SEPA Direct Debit
// ---------------------------------------------------------------------------

func TestSDD_HappyPath(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)

	debtor := PartyRef{Account: alice}
	creditor := PartyRef{Account: biller}
	m, err := sys.bank(b.BIC).CreateMandate(ctx, a.BIC, debtor, creditor, 0)
	assertNoError(t, err)

	var pay Payment
	runCycle(t, sys, SchemeSEPADD, func() {
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
			Debtor: debtor, Creditor: creditor, Description: "Electricity bill",
			DebtorDetails:   PartyDetails{Agent: a.BIC, Name: "Alice"},
			CreditorDetails: PartyDetails{Agent: b.BIC}})
		assertNoError(t, err)
		assertEqual(t, "value date T+2", pay.ValueDate, fixedTime.Add(48*time.Hour))
	})

	assertEqual(t, "alice", customerBalance(t, a, alice), 75000)
	assertEqual(t, "biller", customerBalance(t, b, biller), 25000)
	assertReserveMirror(t, sys, a)
	assertReserveMirror(t, sys, b)
}

func TestSDD_MandateValidation(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)
	debtor := PartyRef{Account: alice}
	creditor := PartyRef{Account: biller}

	limited, err := sys.bank(b.BIC).CreateMandate(ctx, a.BIC, debtor, creditor, 5000)
	assertNoError(t, err)
	revoked, err := sys.bank(b.BIC).CreateMandate(ctx, a.BIC, debtor, creditor, 0)
	assertNoError(t, err)
	assertNoError(t, sys.bank(b.BIC).RevokeMandate(ctx, revoked.ID))

	cases := []struct {
		name      string
		mandateID MandateID
		amount    ledger.Amount
		want      error
	}{
		{"no mandate", "", 1000, ErrMandateRequired},
		{"unknown mandate", "mnd_999", 1000, ErrMandateNotFound},
		{"revoked mandate", revoked.ID, 1000, ErrMandateRevoked},
		{"exceeds max", limited.ID, 6000, ErrMandateExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sys.OpenCycle(ctx, SchemeSEPADD)
			assertNoError(t, err)
			_, err = initiate(ctx, sys, InitiatePaymentRequest{
				Scheme: SchemeSEPADD, Amount: tc.amount, MandateID: tc.mandateID,
				Debtor: debtor, Creditor: creditor,
				DebtorDetails:   PartyDetails{Agent: a.BIC, Name: "Alice"},
				CreditorDetails: PartyDetails{Agent: b.BIC}})
			assertError(t, err, tc.want)
			// Tidy up the open cycle for the next sub-test.
			cyc, err := sys.OpenCycleID(ctx, SchemeSEPADD)
			assertNoError(t, err)
			_, _ = sys.CloseCycle(ctx, cyc)
		})
	}
}

func TestSDD_Return(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)
	debtor := PartyRef{Account: alice}
	creditor := PartyRef{Account: biller}
	m, err := sys.bank(b.BIC).CreateMandate(ctx, a.BIC, debtor, creditor, 0)
	assertNoError(t, err)

	var pay Payment
	runCycle(t, sys, SchemeSEPADD, func() {
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
			Debtor:          debtor,
			Creditor:        creditor,
			DebtorDetails:   PartyDetails{Agent: a.BIC, Name: "Alice"},
			CreditorDetails: PartyDetails{Agent: b.BIC}})
		assertNoError(t, err)
	})
	assertEqual(t, "alice after collection", customerBalance(t, a, alice), 75000)

	returned, err := returnWholePayment(ctx, sys, pay.ID, "insufficient funds at debtor")
	assertNoError(t, err)
	assertEqual(t, "status", returned.Status, Returned)
	// A return is not a rejection: it carries no StatusReason. pacs.004 draws
	// its reason from iso20022.ReturnReason instead — a different external
	// code set — and PostReturnLegTx does not set RejectCode, only the free
	// text. See the RejectCode doc comment on payment.Payment.
	assertEqual(t, "return sets no reject code", string(returned.RejectCode), "")

	// Money fully unwound across all three ledgers.
	assertEqual(t, "alice refunded", customerBalance(t, a, alice), 100000)
	assertEqual(t, "biller clawed back", customerBalance(t, b, biller), 0)
	assertReserveMirror(t, sys, a)
	assertReserveMirror(t, sys, b)
}

// ---------------------------------------------------------------------------
// Settlement atomicity
// ---------------------------------------------------------------------------

// TestSettleCycleIsAtomic is now a test of the REFUSAL, not of a rollback, and
// the difference is worth being exact about because it used to be both.
//
// It was written when SettleCycleTx posted every member's mirror leg, so an
// underfunded member was discovered PARTWAY: the central bank's netting
// transaction had already posted and the ledger refused the mirror leg in the
// short member's own book, and the reserve balances below were the evidence that
// the committed central-bank posting had been rolled back with everything else.
//
// The mirror leg is the member's own act since Task 15b.2, so SettleCycleTx
// checks each net payer's reserve ITSELF, before anything is posted (see the
// net-payer loop in system.go). Against this fixture the refusal is therefore a
// clean no-op — there is no partway to fail at, and nothing was written to roll
// back. What this still asserts is real and worth keeping: the refusal writes
// nothing, which is what makes asking again safe once the member is funded
// (TestARefusedSettlementCanBeInstructedAgain in mesh is the other half).
//
// # What it no longer carries, and what nothing carries in its place
//
// The MID-FLIGHT rollback claim: a unit of work that had already WRITTEN, then
// failed, then left nothing behind. Until Task 15b.3 two tests carried it —
// TestASettlementStoreFailureDoesNotRouteMoneyToUnclaimedBalances and the
// cross-asset test now called TestCrossAssetPaymentSurvivesInitiationAndFailsAt-
// ThePayeesBank — because both failed inside the creditor-leg loop, after the
// netting transaction had posted.
//
// That loop is gone. Both now fail inside PostCreditorLeg, which is the payee's
// bank's OWN unit of work, and both fail before it writes anything: the store
// failure at glAccountTx and the asset mismatch at the posting itself, with only
// reads behind them. So both are clean no-ops too, and the claim is currently
// UNWITNESSED at this seam. That is a finding rather than a gap to paper over,
// and it is what splitting settlement per institution actually did: there is no
// longer a unit of work at a cut-off that writes in one institution's book and
// then discovers a problem in another's, because no unit of work reaches two
// institutions.
//
// It is not unreachable, only unexercised. PostCreditorLegTx posts the leg and
// THEN writes the payment row and the audit event, so a store that failed on
// either would roll a committed posting back. Nothing here provokes that, and a
// test that did would be a test of the store rather than of settlement.
//
// The claim still lives in this package, on the seam that still has a
// multi-write unit of work: TestAFailedReversalRollsBackTheWholeRejection, where
// the CSM's transition is written and the reversal that follows it fails.
func TestSettleCycleIsAtomic(t *testing.T) {
	ctx := context.Background()
	net, cycleID := newClosedCycleWithUnderfundedMember(t) // one member lacks reserves

	before := reserveBalances(t, ctx, net)

	_, _, err := net.settleCycle(ctx, cycleID)
	if err == nil {
		t.Fatal("SettleCycle succeeded, want failure on the underfunded member")
	}

	after := reserveBalances(t, ctx, net)
	for id, want := range before {
		assertEqual(t, "reserve balance for "+string(id), after[id], want)
	}
}

// TestSettleCycleRollsBackEveryLayer widens the test above from the central
// bank's reserve balances to every layer a cut-off writes: each participant's
// suspense and reserve, each bank's transaction count, the central bank's, the
// settlement record, the cycle's status and every payment's.
//
// Its NAME has outlived its mechanism, and the honest reading is in the note on
// the test above. It was written when the underfunded member was discovered
// partway — the netting transaction posted, the mirror leg in that member's own
// book refused — so "rolls back every layer" was a claim about a real rollback,
// and it was the assertion the reserve balances alone could not make: a
// two-transaction implementation that happened to post the participant legs
// first would slip past the test above and not past this one.
//
// Against this fixture there is now nothing to roll back. SettleCycleTx refuses
// the short member before it posts anything, so what the sweep below measures is
// that the refusal touched NO layer at all — which is a stronger statement than
// "it was undone", and a different one. It is kept, and kept wide, because the
// sweep is what would notice a future refusal that wrote something before
// deciding. Where the mid-flight rollback claim went is set out on the test
// above: at a cut-off it is now unwitnessed, because no unit of work here
// reaches two institutions any more.
func TestSettleCycleRollsBackEveryLayer(t *testing.T) {
	ctx := context.Background()
	net, cycleID := newClosedCycleWithUnderfundedMember(t)

	cycle, err := net.GetCycle(ctx, cycleID)
	assertNoError(t, err)

	participants := allBanks(t, ctx, net)

	// Snapshot every layer the settlement would touch.
	suspenseBefore := map[ParticipantID]ledger.Amount{}
	reserveBefore := map[ParticipantID]ledger.Amount{}
	txCountBefore := map[ParticipantID]int{}
	for _, p := range participants {
		suspenseBefore[p.ID] = bookBalance(t, p.Ledger, accountsOf(t, p).Suspense)
		reserveBefore[p.ID] = bookBalance(t, p.Ledger, accountsOf(t, p).Reserve)
		txs, err := p.Ledger.ListTransactions(ctx)
		assertNoError(t, err)
		txCountBefore[p.ID] = len(txs)
	}
	cbTxBefore, err := net.cbBook(t).ListTransactions(ctx)
	assertNoError(t, err)

	_, _, err = net.settleCycle(ctx, cycleID)
	if err == nil {
		t.Fatal("SettleCycle succeeded, want failure on the underfunded member")
	}

	// No participant's own book moved, and none of them gained a transaction.
	for _, p := range participants {
		assertEqual(t, "suspense at "+p.Name, bookBalance(t, p.Ledger, accountsOf(t, p).Suspense), suspenseBefore[p.ID])
		assertEqual(t, "reserve at "+p.Name, bookBalance(t, p.Ledger, accountsOf(t, p).Reserve), reserveBefore[p.ID])
		txs, err := p.Ledger.ListTransactions(ctx)
		assertNoError(t, err)
		assertEqual(t, "transaction count at "+p.Name, len(txs), txCountBefore[p.ID])
	}

	// The central bank did not gain the settlement transaction.
	cbTxAfter, err := net.cbBook(t).ListTransactions(ctx)
	assertNoError(t, err)
	assertEqual(t, "central bank transaction count", len(cbTxAfter), len(cbTxBefore))

	// No settlement was recorded, and the cycle is still Closed rather than
	// Settled — which is what leaves the operation retriable once the member is
	// funded. Retrying it is not this layer's act: in the mesh the clearing
	// house re-sends the pacs.009 (POST /cycles/{cid}/settle, mesh.csm.settle),
	// and SettleCycleTx's CycleClosed guard is what makes asking twice safe.
	settlements, err := net.cb().ListSettlements(ctx)
	assertNoError(t, err)
	assertEqual(t, "settlements recorded", len(settlements), 0)

	after, err := net.GetCycle(ctx, cycleID)
	assertNoError(t, err)
	assertEqual(t, "cycle status", after.Status, CycleClosed)

	// The payments are still Cleared: none of them was marked Settled.
	for _, pid := range cycle.PaymentIDs {
		p, err := net.GetPayment(ctx, pid)
		assertNoError(t, err)
		assertEqual(t, "status of "+string(pid), p.Status, Cleared)
	}
}

// newClosedCycleWithUnderfundedMember builds a closed cycle in which one net
// payer cannot cover its position at the central bank.
//
// The gap is opened with an overdraft: Carol's account at Bank C may go
// negative, so her payment passes the deposit layer's funds check, while Bank C
// holds no reserves at the central bank to settle with. That is the real shape
// of the failure a settlement window exists to contain: the instruction is
// valid, the member's liquidity is not.
//
// What REFUSES it moved with the mirror leg. It used to be the ledger, when that
// leg drew Bank C's own "Reserve at Central Bank" — a plain Asset account with
// no overdraft — below zero in Bank C's book. Since Task 15b.2 the leg is Bank
// C's own act and the refusal is SettleCycleTx's explicit net-payer check, which
// runs before anything is posted. Same fixture, same sentinel
// (ledger.ErrInsufficientBalance) and therefore the same AM04 on the wire; a
// different layer says it, and it now says it earlier.
func newClosedCycleWithUnderfundedMember(t *testing.T) (*testSystem, CycleID) {
	t.Helper()
	ctx := context.Background()
	sys := testNetwork(t)

	// Three addresses, because the central bank holds one reserve account per
	// ADDRESS: on one BIC these three would be one member, and the underfunded
	// one this fixture exists for would be covered by the other two's money.
	a, err := storetest.Admit(ctx, sys.nets, "Bank A", testBICs[0], euroOnly) // net receiver
	assertNoError(t, err)
	b, err := storetest.Admit(ctx, sys.nets, "Bank B", testBICs[1], euroOnly) // solvent net payer
	assertNoError(t, err)
	c, err := storetest.Admit(ctx, sys.nets, "Bank C", testBICs[2], euroOnly) // underfunded net payer
	assertNoError(t, err)

	alice := openCustomer(t, ctx, a, "Alice", "SE89-BANKA-0001")
	bob := openCustomer(t, ctx, b, "Bob", "SE89-BANKB-0001")
	carol, err := c.Deposit.OpenAccount(ctx, c.CustomerSubledger, "Carol", testAsset, c.ProductID, 100000,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-BANKC-0001"})
	assertNoError(t, err)

	fundAccount(t, ctx, sys, a, alice, 100000)
	fundAccount(t, ctx, sys, b, bob, 100000)
	fundAccount(t, ctx, sys, c, carol, 10000)

	cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)

	// Bank B pays 20000 it has the reserves for.
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 20000,
		Debtor: PartyRef{Account: bob.ID}, Creditor: PartyRef{Account: alice.ID},
		CreditorDetails: PartyDetails{Agent: a.BIC, Name: alice.Name},
		DebtorDetails:   PartyDetails{Agent: b.BIC}})
	assertNoError(t, err)
	// Bank C pays 60000 its customer can afford on overdraft and it cannot.
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 60000,
		Debtor: PartyRef{Account: carol.ID}, Creditor: PartyRef{Account: alice.ID},
		CreditorDetails: PartyDetails{Agent: a.BIC, Name: alice.Name},
		DebtorDetails:   PartyDetails{Agent: c.BIC}})
	assertNoError(t, err)

	_, err = sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)
	return sys, cyc.ID
}

// reserveBalances reads every participant's reserve as held at the central bank.
func reserveBalances(t *testing.T, ctx context.Context, sys *testSystem) map[ParticipantID]ledger.Amount {
	t.Helper()
	participants := allBanks(t, ctx, sys)

	out := make(map[ParticipantID]ledger.Amount, len(participants))
	for _, p := range participants {
		bal, err := sys.cb().ReserveBalance(ctx, p.BIC, testAsset)
		assertNoError(t, err)
		out[p.ID] = bal
	}
	return out
}

// SettleCycle writes the central bank's settlement entries in the order the
// participants were registered. Go randomises map iteration, so a settlement
// built by ranging over the net positions produced a different entry order on
// every run — and that order is persisted.
func TestSettlementEntryOrderIsDeterministic(t *testing.T) {
	ctx := context.Background()

	order := func() string {
		sys := testNetwork(t)
		var banks []*Bank
		var accounts []deposit.Account
		// One address each: the entries this test reads are settlement accounts,
		// and four banks on one BIC would be one member with one account, so the
		// order it compares would be four copies of the same id — deterministic
		// whatever the code does.
		for i, name := range []string{"Bank A", "Bank B", "Bank C", "Bank D"} {
			p, err := storetest.Admit(ctx, sys.nets, name, testBICs[i], euroOnly)
			assertNoError(t, err)
			acct := openCustomer(t, ctx, p, "Customer at "+name, fmt.Sprintf("SE89-BANK%d-0001", i))
			fundAccount(t, ctx, sys, p, acct, 100000)
			banks = append(banks, p)
			accounts = append(accounts, acct)
		}

		// Every bank ends up with a non-zero net position, so every one of them
		// contributes an entry to the settlement transaction.
		st := runCycle(t, sys, SchemeSEPACT, func() {
			for i := range banks {
				j := (i + 1) % len(banks)
				_, err := initiate(ctx, sys, InitiatePaymentRequest{
					Scheme: SchemeSEPACT, Amount: ledger.Amount(1000 * (i + 1)),
					Debtor:          PartyRef{Account: accounts[i].ID},
					Creditor:        PartyRef{Account: accounts[j].ID},
					CreditorDetails: PartyDetails{Agent: banks[j].BIC, Name: accounts[j].Name},
					DebtorDetails:   PartyDetails{Agent: banks[i].BIC}})
				assertNoError(t, err)
			}
		})

		tx, err := sys.cbBook(t).GetTransaction(ctx, st.SettlementTx)
		assertNoError(t, err)
		assertEqual(t, "settlement entry count", len(tx.Entries), 4)

		var s string
		for _, e := range tx.Entries {
			s += string(e.AccountID) + " "
		}
		return s
	}

	// Five runs, because a map with four keys iterated in a random order
	// repeats itself only about one time in twenty-four.
	want := order()
	for range 5 {
		assertEqual(t, "settlement entry order", order(), want)
	}
}

// ---------------------------------------------------------------------------
// State machine, idempotency, and validation guards
// ---------------------------------------------------------------------------

func TestStateMachineGuards(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	mkPayment := func() (Payment, CycleID) {
		cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
		assertNoError(t, err)
		p, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 10000,
			Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
			DebtorDetails:   PartyDetails{Agent: a.BIC}})
		assertNoError(t, err)
		return p, cyc.ID
	}

	// # Both settlement guards changed hands at Task 18d, and neither is gone
	//
	// They were one refusal, ErrCycleNotClosed, made by SettleCycleTx off the
	// cycle's status. The settlement agent holds no cycles table now — it settles
	// what it was INSTRUCTED, and the instruction is a list of legs — so it can
	// make neither statement out of a row it cannot read.
	//
	// "Not closed" went to the CLEARING HOUSE, which is whose cut-off it is: it
	// will not build an instruction for a cycle that is still open, and
	// mesh/csm.settle is where that lives and where it is measured. What the
	// agent sees of an open cycle is an instruction with NO LEGS, because an
	// open cycle has no net positions to render — and refusing a batch it cannot
	// read as one is a statement it can make about the message in front of it.
	//
	// "Already settled" stayed with the agent and changed its evidence: from the
	// cycle's status to its OWN settlement register, which is its record of
	// having done the work. That one had to stay — a redelivered pacs.009 reaches
	// the agent directly and no clearing-house guard is between them.
	t.Run("settle before close", func(t *testing.T) {
		_, cyc := mkPayment()
		_, _, err := sys.settleCycle(ctx, cyc)
		assertError(t, err, ErrInvalidSettlement)
		_, _ = sys.CloseCycle(ctx, cyc)
		_, _, _ = sys.settleCycle(ctx, cyc)
	})

	t.Run("double settle", func(t *testing.T) {
		_, cyc := mkPayment()
		_, err := sys.CloseCycle(ctx, cyc)
		assertNoError(t, err)
		_, _, err = sys.settleCycle(ctx, cyc)
		assertNoError(t, err)
		_, _, err = sys.settleCycle(ctx, cyc)
		assertError(t, err, ErrCycleAlreadySettled)
	})

	t.Run("return before settle", func(t *testing.T) {
		p, cyc := mkPayment()
		_, err := returnWholePayment(ctx, sys, p.ID, "too early")
		assertError(t, err, ErrInvalidStateTransition)
		_, _ = sys.CloseCycle(ctx, cyc)
		_, _, _ = sys.settleCycle(ctx, cyc)
	})

	t.Run("reject after settle", func(t *testing.T) {
		p, cyc := mkPayment()
		_, err := sys.CloseCycle(ctx, cyc)
		assertNoError(t, err)
		_, _, err = sys.settleCycle(ctx, cyc)
		assertNoError(t, err)
		_, err = reject(ctx, sys, p.ID, iso20022.StatusReasonNotSpecifiedAgentGenerated, "too late")
		assertError(t, err, ErrInvalidStateTransition)
	})
}

func TestRejectionReversesTheDebtorLeg(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	p, err := initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 40000,
		Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	assertNoError(t, err)
	assertEqual(t, "alice debited", customerBalance(t, a, alice), 60000)

	rejected, err := reject(ctx, sys, p.ID, iso20022.StatusReasonNotSpecifiedAgentGenerated, "operator cancelled")
	assertNoError(t, err)
	assertEqual(t, "status", rejected.Status, Rejected)
	assertEqual(t, "alice restored", customerBalance(t, a, alice), 100000)
	assertEqual(t, "suspense flat", bookBalance(t, a.Ledger, accountsOf(t, a).Suspense), 0)
}

func TestDuplicateEndToEndID(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	req := InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 1000, EndToEndID: "e2e-1",
		Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}}
	_, err = initiate(ctx, sys, req)
	assertNoError(t, err)
	_, err = initiate(ctx, sys, req)
	assertError(t, err, ErrDuplicateEndToEndID)
}

func TestInitiatePayment_Validation(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)

	t.Run("unknown scheme", func(t *testing.T) {
		_, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: "nope", Amount: 1000,
			Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
			DebtorDetails:   PartyDetails{Agent: a.BIC},
			CreditorDetails: PartyDetails{Agent: b.BIC}})
		assertError(t, err, ErrSchemeNotFound)
	})
	t.Run("non-positive amount", func(t *testing.T) {
		_, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 0,
			Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
			DebtorDetails:   PartyDetails{Agent: a.BIC},
			CreditorDetails: PartyDetails{Agent: b.BIC}})
		assertError(t, err, ErrInvalidPaymentAmount)
	})
	t.Run("account not in participant", func(t *testing.T) {
		_, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 1000,
			Debtor: PartyRef{Account: "999.999.999"}, Creditor: PartyRef{Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
			DebtorDetails:   PartyDetails{Agent: a.BIC}})
		assertError(t, err, ErrAccountNotInParticipant)
	})
	t.Run("no open cycle", func(t *testing.T) {
		// The cut-off belongs to the clearing house, so this refusal comes
		// from its half and not from either bank's: both banks accept a
		// collection they are perfectly happy with, and AcceptAtCSMTx is what
		// finds no window open for sepa.dd. A mandate is needed to get that
		// far at all — without one the creditor's own bank refuses first.
		m, err := sys.bank(b.BIC).CreateMandate(ctx,
			a.BIC,
			PartyRef{Account: alice},
			PartyRef{Account: bob}, 0)
		assertNoError(t, err)

		_, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 1000, MandateID: m.ID, // no SDD cycle open
			Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
			DebtorDetails:   PartyDetails{Agent: a.BIC, Name: "Alice"},
			CreditorDetails: PartyDetails{Agent: b.BIC}})
		assertError(t, err, ErrCycleNotOpen)
	})
}

func TestOpenCycle_AlreadyOpen(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	_, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	_, err = sys.OpenCycle(ctx, SchemeSEPACT)
	assertError(t, err, ErrCycleAlreadyOpen)
}

// ---------------------------------------------------------------------------
// Assets
// ---------------------------------------------------------------------------

func TestParticipantHasAccountsPerAsset(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	p, err := storetest.Admit(ctx, sys.nets, "Alpha", testBIC, []ledger.AssetCode{"EUR", "USD"})
	assertNoError(t, err)

	for _, asset := range []ledger.AssetCode{"EUR", "USD"} {
		accts, err := p.AccountsFor(asset)
		assertNoError(t, err)
		for name, id := range map[string]ledger.AccountID{
			"suspense": accts.Suspense, "reserve": accts.Reserve,
			"unclaimed": accts.Unclaimed, "settlement": accts.Settlement,
			"returns receivable": accts.ReturnsReceivable,
		} {
			if id == "" {
				t.Errorf("%s account for %s is empty", name, asset)
			}
		}
		// Each of the four accounts must itself be denominated in that asset,
		// three of them in the bank's book and one in the central bank's.
		suspense, err := p.Ledger.GetAccount(ctx, accts.Suspense)
		assertNoError(t, err)
		assertEqual(t, "suspense asset", suspense.Asset, asset)
		reserve, err := p.Ledger.GetAccount(ctx, accts.Reserve)
		assertNoError(t, err)
		assertEqual(t, "reserve asset", reserve.Asset, asset)
		unclaimed, err := p.Ledger.GetAccount(ctx, accts.Unclaimed)
		assertNoError(t, err)
		assertEqual(t, "unclaimed asset", unclaimed.Asset, asset)
		settlement, err := sys.cbBook(t).GetAccount(ctx, accts.Settlement)
		assertNoError(t, err)
		assertEqual(t, "settlement asset", settlement.Asset, asset)

		// And unclaimed balances is a LIABILITY. Money that arrives for an
		// account which cannot receive it is still owed — to whoever eventually
		// claims it — so it is the same class as a customer's deposit. Booking it
		// as an asset of the bank's would say the bank had earned it, and would
		// put the credit leg of every diverted settlement on the wrong side.
		assertEqual(t, "unclaimed account type", unclaimed.Type, ledger.Liability)

		// Returns Receivable is the CONTRAST, and it is asserted beside its
		// opposite rather than only in TestABankJoinsWithAReturnsReceivableAccount,
		// because the contrast is the teaching claim: two pooled accounts with no
		// customer's name on either, one money the bank OWES and one money the
		// bank is OWED. Both balance either way round, and getting it backwards
		// would say the opposite in every report the account appears in. Asserted
		// per asset here, where the dedicated test covers euro only.
		receivable, err := p.Ledger.GetAccount(ctx, accts.ReturnsReceivable)
		assertNoError(t, err)
		assertEqual(t, "returns receivable account type", receivable.Type, ledger.Asset)
	}

	// And they survive the store, rather than only the value the last act
	// returned.
	reloaded, err := sys.bank(p.BIC).GetBank(ctx, p.ID)
	assertNoError(t, err)
	assertEqual(t, "assets after a reload", len(reloaded.Assets), 2)
}

// TestABankJoinsWithAReturnsReceivableAccount pins the account's CLASS as much
// as its existence. It is an Asset — a claim on a biller the bank paid out for
// — and not a liability like Unclaimed Balances, which is money the bank OWES.
// Getting that backwards would balance and mean the opposite.
func TestABankJoinsWithAReturnsReceivableAccount(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	p, err := storetest.Admit(ctx, sys.nets, "Alpha", testBIC, euroOnly)
	assertNoError(t, err)

	accts, err := p.AccountsFor(testAsset)
	assertNoError(t, err)
	if accts.ReturnsReceivable == "" {
		t.Fatal("returns receivable account is empty")
	}

	acct, err := p.Ledger.GetAccount(ctx, accts.ReturnsReceivable)
	assertNoError(t, err)
	assertEqual(t, "returns receivable account type", acct.Type, ledger.Asset)
	assertEqual(t, "returns receivable account name", acct.Name, "Returns Receivable (EUR)")
}

// ---------------------------------------------------------------------------
// Admission, one act at a time
//
// Every test above admits a bank through storetest.Admit, which calls all four in
// order, so all of them are already a check that the four compose to a working
// member. These are the tests for the parts of each act that a composition
// cannot exercise: what one institution's work leaves untouched, and what each
// act refuses.
// ---------------------------------------------------------------------------

// mustUpdate runs fn in one unit of work and fails the test if it does not
// commit. It is for the Tx forms of the acts below.
//
// Each act has a wrapper of its own on Network — FoundBank,
// OpenSettlementAccount, AdmitMember, RecordMembership — because a message
// handler holds no transaction, and there is deliberately no wrapper over all
// FOUR: what composes them is a conversation between three institutions, and a
// single unit of work spanning them is exactly what an admission across a store
// boundary cannot be.
func mustUpdate(t *testing.T, ctx context.Context, sys *testSystem, fn func(context.Context, Tx) error) {
	t.Helper()
	assertNoError(t, sys.Store().Update(ctx, fn))
}

// mustUpdateAt is mustUpdate at ONE NAMED BANK: its network, and its own
// database's unit of work.
//
// The two are not interchangeable and mustUpdate is the clearing house's. A
// bank's act run through the clearing house's network is refused outright
// (ErrNotThisInstitutionsAct) and a bank's row written into the clearing house's
// store is a table that shape does not have, so every *Tx act below whose
// subject is a bank has to come through here. See payment.Networks.
func mustUpdateAt(t *testing.T, ctx context.Context, net *Network, fn func(context.Context, Tx) error) {
	t.Helper()
	assertNoError(t, net.Store().Update(ctx, fn))
}

// submit is a payment SUBMITTED, through the bank the request says submits it.
//
// It exists because submission stopped being something a caller can do to "the
// network". SubmitPaymentTx resolves the submitting side's account in the
// acting network's OWN register and refuses any network that is not a member
// bank's, so a fixture calling it on the embedded clearing house gets
// ErrNotThisInstitutionsAct rather than a payment. Which bank submits is the
// scheme's direction; see submitterOfReq.
func (s *testSystem) submit(ctx context.Context, req InitiatePaymentRequest) (Payment, error) {
	return s.bank(submitterOfReq(s, req)).SubmitPayment(ctx, req)
}

// allBanks is every bank in the system, read from each bank's own database.
//
// It replaces ListBanks, which a clearing house cannot answer: it holds no banks
// table, and the roster it does hold names addresses rather than banks — and
// says nothing at all about a bank that has been founded and never admitted.
// Stores.Banks is the composition root's question, which is what a test
// assembling the whole system is asking. See auditReaders, which had the same
// problem and the same answer.
func allBanks(t *testing.T, ctx context.Context, sys *testSystem) []*Bank {
	t.Helper()
	bics, err := sys.stores.Banks(ctx)
	assertNoError(t, err)
	out := make([]*Bank, 0, len(bics))
	for _, bic := range bics {
		b, err := sys.bank(bic).GetBank(ctx, ParticipantID(bic))
		assertNoError(t, err)
		out = append(out, b)
	}
	return out
}

// mustGetPaymentAt is one named institution's copy of a payment.
//
// It is a helper rather than an inline GetPayment because a payment is THREE
// rows since Task 18d and they legitimately disagree: the submitting bank
// back-fills its own side, the receiving bank back-fills its own, and the
// clearing house holds the message's values and back-fills nothing, because it
// holds no register to back-fill from. Every assertion about a party ref has to
// say whose copy it is reading, and this is where it says so.
func mustGetPaymentAt(t *testing.T, ctx context.Context, net *Network, id PaymentID) Payment {
	t.Helper()
	p, err := net.GetPayment(ctx, id)
	assertNoError(t, err)
	return p
}

// mustGetBank re-reads a bank from ITS OWN store, so an assertion is about what
// was committed rather than about the value an act returned.
//
// Its own, because a bank row is in the bank shape and in no other: reading one
// through the clearing house's network is not a wrong answer but a missing
// table. The id is the address (Task 18), which is what makes the routing a
// conversion rather than a lookup.
func mustGetBank(t *testing.T, ctx context.Context, sys *testSystem, id ParticipantID) *Bank {
	t.Helper()
	p, err := sys.bank(iso20022.BIC(id)).GetBank(ctx, id)
	assertNoError(t, err)
	return p
}

// assertCentralBankReserveAccountCount counts the accounts in the central
// bank's own book whose name carries the given bank's name — the reserve
// accounts it has opened for that member, one per asset (see
// OpenSettlementAccountTx, which names them "Reserve: <bank> (<asset>)").
//
// It counts ACCOUNTS rather than comparing ids because a second opening leaves
// evidence in the book even when the caller is handed the first account's id
// back, and that is the failure this helper exists to catch.
func assertCentralBankReserveAccountCount(t *testing.T, ctx context.Context, sys *testSystem, bank string, want int) {
	t.Helper()
	var got int
	// The CENTRAL BANK's store, because the book is. Reading it through the
	// embedded clearing-house network is refused by name since Task 18d — that
	// store does not answer for another institution's book — so the routing here
	// is what makes the count a count rather than an error.
	assertNoError(t, sys.cb().Store().View(ctx, func(ctx context.Context, tx Tx) error {
		accounts, err := tx.ListAccounts(ctx, CentralBankBook)
		if err != nil {
			return err
		}
		for _, a := range accounts {
			if strings.Contains(a.Name, bank) {
				got++
			}
		}
		return nil
	}))
	if got != want {
		t.Errorf("the central bank's book holds %d accounts naming %s, want %d", got, bank, want)
	}
}

// TestFoundingABankTouchesNoOtherInstitution is the whole claim of this task,
// made about the first act. A founded bank has a book, a chart of accounts and a
// product; the central bank has opened it nothing and the roster has never heard
// of it.
func TestFoundingABankTouchesNoOtherInstitution(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	var b *Bank
	aurora := sys.bank("AURODEFFXXX")
	mustUpdateAt(t, ctx, aurora, func(ctx context.Context, tx Tx) (err error) {
		b, err = aurora.FoundBankTx(ctx, tx, "Aurora Bank", "AURODEFFXXX", euroOnly)
		return err
	})

	if b.Status != BankFounded {
		t.Errorf("a founded bank has status %q, want %q", b.Status, BankFounded)
	}
	if got := b.Assets[testAsset].Settlement; got != "" {
		t.Errorf("a founded bank names settlement account %q; it has not asked for one yet", got)
	}
	// It is a working bank all the same: it can open a customer account, which
	// takes a subledger and a product.
	if _, err := b.OpenCustomerAccount(ctx, "Alice", testAsset); err != nil {
		t.Errorf("a founded bank cannot open a customer account: %v", err)
	}

	// Two institutions, two reads, and that is the point of the test rather than
	// an inconvenience: "no other institution was touched" is a claim about the
	// central bank's database AND about the clearing house's, and since Task 18d
	// neither can be asked about the other's table. One View over one store was
	// the same sentence only while there was one store.
	assertNoError(t, sys.cb().Store().View(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.GetSettlementMember(ctx, "AURODEFFXXX"); !errors.Is(err, ErrSettlementMemberNotFound) {
			t.Errorf("the central bank has a member row for a bank that only founded itself: %v", err)
		}
		return nil
	}))
	assertNoError(t, sys.Store().View(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.GetRosterEntry(ctx, "AURODEFFXXX"); !errors.Is(err, ErrRosterEntryNotFound) {
			t.Errorf("the clearing house has a routing entry for a bank that has not applied: %v", err)
		}
		return nil
	}))
	assertCentralBankReserveAccountCount(t, ctx, sys, "Aurora Bank", 0)
}

// TestOpeningASettlementAccountTwiceOpensOne is what makes a retried admission
// safe. The mesh delivers exactly once, so this is not reachable through the
// transport — it is reachable through the OPERATOR, who re-drives an admission
// that failed after the accounts were opened.
//
// The second half is the other side of the same idempotency. One acmt.007 names
// one currency, so a bank clearing two schemes asks twice, and the second ask
// must EXTEND the member rather than be swallowed as a repeat.
func TestOpeningASettlementAccountTwiceOpensOne(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	in := AdmissionRequest{Name: "Aurora Bank", BIC: "AURODEFFXXX", Asset: testAsset, Ref: "adm-1"}
	var first, second SettlementMember
	mustUpdateAt(t, ctx, sys.cb(), func(ctx context.Context, tx Tx) (err error) {
		first, err = sys.cb().OpenSettlementAccountTx(ctx, tx, in)
		return err
	})
	mustUpdateAt(t, ctx, sys.cb(), func(ctx context.Context, tx Tx) (err error) {
		second, err = sys.cb().OpenSettlementAccountTx(ctx, tx, in)
		return err
	})

	if first.Accounts[testAsset] == "" {
		t.Fatal("the first request opened no account at all")
	}
	if first.Accounts[testAsset] != second.Accounts[testAsset] {
		t.Errorf("a second request answered with a different account: %q then %q",
			first.Accounts[testAsset], second.Accounts[testAsset])
	}
	// And the central bank's book holds ONE, which the ids above would not catch
	// if the second call created an account and then returned the first's id.
	assertCentralBankReserveAccountCount(t, ctx, sys, "Aurora Bank", 1)

	// A different asset is a different account, and it does not disturb the one
	// already open.
	var extended SettlementMember
	mustUpdateAt(t, ctx, sys.cb(), func(ctx context.Context, tx Tx) (err error) {
		extended, err = sys.cb().OpenSettlementAccountTx(ctx, tx,
			AdmissionRequest{Name: "Aurora Bank", BIC: "AURODEFFXXX", Asset: "USD", Ref: "adm-2"})
		return err
	})
	if extended.Accounts[testAsset] != first.Accounts[testAsset] {
		t.Errorf("opening a dollar account moved the euro one: %q, want %q",
			extended.Accounts[testAsset], first.Accounts[testAsset])
	}
	if extended.Accounts["USD"] == "" {
		t.Error("a request for a second asset opened no account for it")
	}
	assertCentralBankReserveAccountCount(t, ctx, sys, "Aurora Bank", 2)
}

// TestAdmittingABICTwiceIsRefused is the address clash, decided by the entity
// that owns routing. The mesh's actor map refuses a taken address too; that one
// is a statement about connectivity, and this is the statement about membership.
//
// The impostor's acknowledgement carries a DIFFERENT admission reference, and
// that is what makes it an impostor rather than a second message of the same
// admission: a bank asking for its second currency and an operator re-driving an
// interrupted admission both arrive on a BIC already in the roster, and refusing
// those would refuse the only two cases this refusal must let through.
func TestAdmittingABICTwiceIsRefused(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	ack := AdmissionAcknowledgement{
		BIC:      "AURODEFFXXX",
		Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001"},
		Ref:      "adm-1",
	}
	mustUpdate(t, ctx, sys, func(ctx context.Context, tx Tx) error {
		_, err := sys.AdmitMemberTx(ctx, tx, ack)
		return err
	})

	clash := ack
	clash.Accounts = map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.009"}
	clash.Ref = "adm-2"
	err := sys.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		_, err := sys.AdmitMemberTx(ctx, tx, clash)
		return err
	})
	if !errors.Is(err, ErrBICAlreadyAdmitted) {
		t.Fatalf("admitting a second bank on a taken BIC: %v, want ErrBICAlreadyAdmitted", err)
	}
	// And the roster still says what it said. A refusal that overwrote the entry
	// and then reported failure would leave routing pointing at the impostor —
	// which is what the ADMISSION REFERENCE on it is for, and the only field that
	// could show it: the row records an address and the admission that put it
	// there, and the impostor quoted the same address.
	assertNoError(t, sys.Store().View(ctx, func(ctx context.Context, tx Tx) error {
		e, err := tx.GetRosterEntry(ctx, "AURODEFFXXX")
		if err != nil {
			return err
		}
		if e.AdmissionRef != "adm-1" {
			t.Errorf("the roster entry now cites admission %q; the refusal overwrote it", e.AdmissionRef)
		}
		if !slices.Equal(e.Assets, []ledger.AssetCode{testAsset}) {
			t.Errorf("the roster entry clears in %v; the refusal took the impostor's assets", e.Assets)
		}
		return nil
	}))

	// The same admission asking again is not a clash. This is the acmt.010 for
	// the bank's second currency, and it extends the entry it finds.
	second := ack
	second.Accounts = map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001", "USD": "200.100.002"}
	var extended RosterEntry
	mustUpdate(t, ctx, sys, func(ctx context.Context, tx Tx) (err error) {
		extended, err = sys.AdmitMemberTx(ctx, tx, second)
		return err
	})
	if !slices.Equal(extended.Assets, []ledger.AssetCode{testAsset, "USD"}) {
		t.Errorf("the roster entry clears in %v, want [EUR USD]", extended.Assets)
	}
}

// TestABankRefusesAnAcknowledgementOfAnotherAdmission is the guard
// Bank.AdmissionRef exists for, made about the act rather than about the mesh.
//
// mesh's TestAMemberRefusesAnAcknowledgementOfAnotherAdmission drives the same
// refusal through a message and is where the measurement that provoked it is
// written down. This one is the domain's half: the act is separately callable —
// that is the whole point of splitting admission into four — so what it refuses
// has to be true of the act and not only of the one caller that exists today.
//
// The two arms are the point. A bank that has recorded NOTHING accepts the first
// acknowledgement that names it, whatever reference it quotes, which is what
// lets an operator re-drive an interrupted admission under a new process id. A
// bank that HAS recorded one accepts no other, which is what stops a second
// admission moving a member's settlement reference.
func TestABankRefusesAnAcknowledgementOfAnotherAdmission(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	var bank *Bank
	own := sys.bank(testBIC)
	mustUpdateAt(t, ctx, own, func(ctx context.Context, tx Tx) (err error) {
		bank, err = own.FoundBankTx(ctx, tx, "Aurora Bank", testBIC, euroOnly)
		return err
	})
	if bank.AdmissionRef != "" {
		t.Fatalf("a founded bank cites admission %q; it has accepted none", bank.AdmissionRef)
	}

	ack := AdmissionAcknowledgement{
		BIC:      testBIC,
		Ref:      "adm-1",
		Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001"},
	}
	// A bank with nothing recorded takes the first that names it.
	mustUpdateAt(t, ctx, sys.bank(bank.BIC), func(ctx context.Context, tx Tx) error {
		_, err := sys.bank(bank.BIC).RecordMembershipTx(ctx, tx, ack)
		return err
	})
	recorded := mustGetBank(t, ctx, sys, bank.ID)
	assertEqual(t, "the admission the bank recorded", recorded.AdmissionRef, "adm-1")
	assertEqual(t, "settlement reference", string(recorded.Assets[testAsset].Settlement), "200.100.001")

	// The same admission again, with a second asset: extended, not refused. This
	// is a two-currency bank's second acknowledgement, which one acmt.007 per
	// currency makes ordinary.
	second := ack
	second.Accounts = map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001", "USD": "200.100.002"}
	mustUpdateAt(t, ctx, sys.bank(bank.BIC), func(ctx context.Context, tx Tx) error {
		_, err := sys.bank(bank.BIC).RecordMembershipTx(ctx, tx, second)
		return err
	})

	// A DIFFERENT admission, naming this bank's own address and an account the
	// settlement agent never opened. Refused, and nothing moves.
	forged := ack
	forged.Ref = "adm-someone-else"
	forged.Accounts = map[ledger.AssetCode]ledger.AccountID{testAsset: "acc_bogus"}
	err := sys.bank(bank.BIC).Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		_, err := sys.bank(bank.BIC).RecordMembershipTx(ctx, tx, forged)
		return err
	})
	if !errors.Is(err, ErrBankAlreadyAdmitted) {
		t.Fatalf("recording an acknowledgement of another admission: %v, want ErrBankAlreadyAdmitted", err)
	}
	after := mustGetBank(t, ctx, sys, bank.ID)
	assertEqual(t, "settlement reference after the refusal", string(after.Assets[testAsset].Settlement), "200.100.001")
	assertEqual(t, "the admission after the refusal", after.AdmissionRef, "adm-1")
}

// TestABankRefusesAnAcknowledgementThatWouldLeaveItWrong is the two holes the
// guards above left, and they are the same shape as each other and as the three
// this branch found before them: a guard closes a case and its own "does not
// apply" value stays reachable.
//
// The guards above are made from the MESSAGE — whose BIC, which admission — and
// they are complete against ReadAdmissionAcknowledgement, refusal for refusal
// (see checkAcknowledgement's table). Neither of these is a message this system
// will not read. Both are STATES this act would write:
//
//   - an account MOVED, under the admission's own reference. ErrBankAlreadyAdmitted
//     compares the reference and says nothing when they match, and the loop wrote
//     whatever arrived. Measured on a healthy member: DepositTx then answered a
//     bare "account not found", ReserveBalance went on reporting the healthy
//     reserve off the settlement agent's untouched row, and re-driving the
//     admission was refused as "already a member".
//   - NOTHING RECORDED. The loop skips an asset the bank does not operate in,
//     which is right — the servicer answers about its own book — and an
//     acknowledgement in which EVERY asset is one of those skipped every account
//     and still made the bank a Member and still burned its AdmissionRef, so its
//     true acknowledgement was refused for ever afterwards. checkAcknowledgement
//     refuses an EMPTY account list for exactly that consequence; this is the
//     same consequence reached with a non-empty one.
//
// The extension cases are asserted beside them, because the fix is a comparison
// and not a prohibition: a redelivery and a second currency both quote accounts
// the bank already holds, since an acmt.010 lists every account the servicer
// holds for the address.
func TestABankRefusesAnAcknowledgementThatWouldLeaveItWrong(t *testing.T) {
	ctx := context.Background()

	// founded returns a network and a bank that operates in euro only and has
	// recorded nothing.
	founded := func(t *testing.T) (*testSystem, *Bank) {
		t.Helper()
		sys := testNetwork(t)
		var bank *Bank
		own := sys.bank(testBIC)
		mustUpdateAt(t, ctx, own, func(ctx context.Context, tx Tx) (err error) {
			bank, err = own.FoundBankTx(ctx, tx, "Aurora Bank", testBIC, euroOnly)
			return err
		})
		return sys, bank
	}
	record := func(sys *testSystem, id ParticipantID, ack AdmissionAcknowledgement) error {
		return sys.bank(iso20022.BIC(id)).Store().Update(ctx, func(ctx context.Context, tx Tx) error {
			_, err := sys.bank(iso20022.BIC(id)).RecordMembershipTx(ctx, tx, ack)
			return err
		})
	}
	real := AdmissionAcknowledgement{
		BIC:      testBIC,
		Ref:      "adm-1",
		Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001"},
	}

	t.Run("an account moved under the admission's own reference", func(t *testing.T) {
		sys, bank := founded(t)
		assertNoError(t, record(sys, bank.ID, real))

		moved := real
		moved.Accounts = map[ledger.AssetCode]ledger.AccountID{testAsset: "999.999.999"}
		err := record(sys, bank.ID, moved)
		if !errors.Is(err, ErrSettlementAccountReplaced) {
			t.Fatalf("recording a moved account: %v, want ErrSettlementAccountReplaced", err)
		}
		after := mustGetBank(t, ctx, sys, bank.ID)
		assertEqual(t, "settlement reference after the refusal",
			string(after.Assets[testAsset].Settlement), "200.100.001")

		// The same message again is not a move. This is the redelivery every
		// queue in this system can produce, and the comparison is what keeps it
		// an extension rather than a refusal.
		assertNoError(t, record(sys, bank.ID, real))
	})

	t.Run("no account this bank can file", func(t *testing.T) {
		sys, bank := founded(t)
		dollarsOnly := real
		dollarsOnly.Accounts = map[ledger.AssetCode]ledger.AccountID{"USD": "200.100.002"}
		err := record(sys, bank.ID, dollarsOnly)
		if !errors.Is(err, ErrAdmittedAccountUnusable) {
			t.Fatalf("recording an acknowledgement in an asset this bank has none of: %v, want ErrAdmittedAccountUnusable", err)
		}
		// The whole point: the bank is still Founded and its reference is still
		// free, so the acknowledgement it is actually waiting for is still
		// accepted.
		after := mustGetBank(t, ctx, sys, bank.ID)
		if after.Status != BankFounded {
			t.Errorf("the refused acknowledgement left the bank %v, want %v", after.Status, BankFounded)
		}
		assertEqual(t, "the admission after the refusal", after.AdmissionRef, "")
		assertNoError(t, record(sys, bank.ID, real))
		assertEqual(t, "the admission it was waiting for",
			mustGetBank(t, ctx, sys, bank.ID).AdmissionRef, "adm-1")
	})

	t.Run("a second currency alongside one already recorded", func(t *testing.T) {
		sys := testNetwork(t)
		var bank *Bank
		own := sys.bank(testBIC)
		mustUpdateAt(t, ctx, own, func(ctx context.Context, tx Tx) (err error) {
			bank, err = own.FoundBankTx(ctx, tx, "Aurora Bank", testBIC, []ledger.AssetCode{testAsset, "USD"})
			return err
		})
		assertNoError(t, record(sys, bank.ID, real))

		both := real
		both.Accounts = map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001", "USD": "200.100.002"}
		assertNoError(t, record(sys, bank.ID, both))
		after := mustGetBank(t, ctx, sys, bank.ID)
		assertEqual(t, "the euro reference", string(after.Assets[testAsset].Settlement), "200.100.001")
		assertEqual(t, "the dollar reference", string(after.Assets["USD"].Settlement), "200.100.002")
	})
}

// TestAnAcknowledgementQuotingNoAdmissionIsRefusedByBothActs is the guard on the
// value that BOTH admission guards are made of, and the one whose absence
// reopened the hole Bank.AdmissionRef was added to close.
//
// # "" is a sentinel on this side of the wire, not a value
//
// An empty Bank.AdmissionRef means "this bank has accepted nothing yet", which
// is what lets a founded bank take the first acknowledgement that names it and
// an operator re-drive under a fresh process id. An empty
// RosterEntry.AdmissionRef compares equal to any other empty one. So an
// acknowledgement carrying no reference does not merely fail to identify itself:
// it defeats both guards, from opposite ends.
//
// Measured on the acts, with the four lines this test exists for removed:
//
//	PROBE1 after an ack with no ref: err=<nil> status="Member" ref="" settlement="200.100.001"
//	PROBE2 forged ack err=<nil> -> settlement="acc_bogus" ref="someone-elses-admission"
//	PROBE3 second institution quoting an empty ref on the same BIC: err=<nil>
//
// The first line is the reset — a Member indistinguishable from a bank that has
// accepted nothing — and the second is the overwrite it reopens, which is
// exactly what TestABankRefusesAnAcknowledgementOfAnotherAdmission stops when
// the reference is real. The third is the same hole seen by the clearing house:
// two institutions on one BIC, both quoting nothing, comparing equal.
//
// # Why the reader is not enough
//
// ReadAdmissionAcknowledgement refuses an empty Refs/PrcId/Id on the way in from
// the wire, so none of this is reachable through a message. The acts are
// separately callable — which is this file's own premise for testing them at all
// — so a reader's guard that is the only line is not defence in depth. Same
// reachability profile as the currency hole beside it, and the same fix.
func TestAnAcknowledgementQuotingNoAdmissionIsRefusedByBothActs(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	var bank *Bank
	own := sys.bank(testBIC)
	mustUpdateAt(t, ctx, own, func(ctx context.Context, tx Tx) (err error) {
		bank, err = own.FoundBankTx(ctx, tx, "Aurora Bank", testBIC, euroOnly)
		return err
	})

	noRef := AdmissionAcknowledgement{
		BIC:      testBIC,
		Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001"},
	}

	// The BANK. A membership recorded under no admission is a Member whose row
	// reads as "accepted nothing", which is the reset.
	err := sys.bank(bank.BIC).Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		_, err := sys.bank(bank.BIC).RecordMembershipTx(ctx, tx, noRef)
		return err
	})
	if !errors.Is(err, ErrAdmissionNotIdentified) {
		t.Errorf("the bank recorded a membership under no admission: %v, want ErrAdmissionNotIdentified", err)
	}
	got := mustGetBank(t, ctx, sys, bank.ID)
	if got.Status == BankMember && got.AdmissionRef == "" {
		t.Error("the bank is a Member with no reference; its own guard reads that as having accepted nothing")
	}

	// The CLEARING HOUSE. Two institutions on one address, both quoting nothing,
	// compare equal — so the refusal that row exists for never fires.
	err = sys.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		_, err := sys.AdmitMemberTx(ctx, tx, noRef)
		return err
	})
	if !errors.Is(err, ErrAdmissionNotIdentified) {
		t.Errorf("the clearing house admitted a member under no admission: %v, want ErrAdmissionNotIdentified", err)
	}
	assertNoError(t, sys.Store().View(ctx, func(ctx context.Context, tx Tx) error {
		if _, err := tx.GetRosterEntry(ctx, testBIC); !errors.Is(err, ErrRosterEntryNotFound) {
			t.Errorf("an acknowledgement quoting no admission put %s in the roster: %v", testBIC, err)
		}
		return nil
	}))

	// And a real one still works, which is what says the guard refuses the
	// SENTINEL rather than the field.
	real := noRef
	real.Ref = "adm-1"
	mustUpdateAt(t, ctx, sys.bank(bank.BIC), func(ctx context.Context, tx Tx) error {
		_, err := sys.bank(bank.BIC).RecordMembershipTx(ctx, tx, real)
		return err
	})
	assertEqual(t, "the admission the bank recorded", mustGetBank(t, ctx, sys, bank.ID).AdmissionRef, "adm-1")
}

// TestAnUnusableAcknowledgementIsRefusedByBothActs holds the right-hand column
// of checkAcknowledgement's correspondence: everything
// ReadAdmissionAcknowledgement will not read off an acmt.010, neither act will
// act on.
//
// It is a table rather than a case per shape because the property is the
// CORRESPONDENCE and not any one refusal. The owner rows and the
// empty-account-list row were added after the fact, each of them a hole found
// by probing rather than by reading, and each time the missing row was the one
// nobody had set the two lists side by side to notice. The owner is two rows
// because the reader's column is two — OrgId/AnyBIC absent, OrgId/AnyBIC
// malformed — and both were measured writing a roster entry keyed by a BIC
// nothing can address: keying a row by a value is not checking it. A refusal
// added to the reader with no row here is what this is meant to make visible.
//
// The admission reference is a refusal of the same kind, and it is held next
// door rather than here: TestAnAcknowledgementQuotingNoAdmissionIsRefusedByBothActs
// is its test. Every row of this table quotes a reference, so that no case
// tests that guard by accident and none of them is really about it.
//
// # The wedge is asserted, and it is the reason these are refusals rather than
// no-ops
//
// An acknowledgement naming no account is the shape that looks harmless in
// isolation: the row it writes has nothing in it, which reads like doing
// nothing. Measured, it is not. The bank becomes a Member that settles through
// no account and the roster entry clears in no scheme, and then the TRUE
// acknowledgement is refused for ever, by the admission-reference guard each of
// those rows now carries:
//
//	PROBE6  bank, ack with NO accounts:   err=<nil> status="Member" ref="impostor-adm" settlement=""
//	PROBE6b the REAL ack then arrives:    err=…recorded its membership under "impostor-adm"…
//	PROBE7  roster, ack with NO accounts: err=<nil> entry={BIC:… Assets:[] AdmissionRef:impostor-adm}
//	PROBE7b the REAL ack then arrives:    err=…is admitted under "impostor-adm"…
//
// So each case ends by driving the real acknowledgement through, which is what
// says the refusal left both institutions able to finish the admission.
//
// # Every row names the error it expects, and not every one of them is ours
//
// "Some error came back" is not the property: a refusal arriving for an
// unrelated reason — a bank in a state the act will not run from, a store that
// failed — would satisfy it and hide the guard going missing. The test this one
// replaced named ErrAdmittedAccountUnusable, and the expectation is per row here
// rather than shared because the owner rows do not end in a sentinel of this
// package at all. checkAcknowledgement asks iso20022.BIC.Validate and wraps what
// it says, so the error to name there is iso20022.ErrBICFormat — another
// package's sentinel, arriving under this one's wrapping, and errors.Is is what
// lets the test ask through it.
func TestAnUnusableAcknowledgementIsRefusedByBothActs(t *testing.T) {
	ctx := context.Background()
	real := AdmissionAcknowledgement{
		BIC: testBIC, Ref: "adm-1",
		Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001"},
	}

	for _, tc := range []struct {
		what string
		in   AdmissionAcknowledgement
		want error
	}{
		{"no account owner", AdmissionAcknowledgement{
			Ref: "adm-x", Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.009"}},
			iso20022.ErrBICFormat},
		{"a malformed account owner", AdmissionAcknowledgement{
			BIC: "nonsense", Ref: "adm-x", Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.009"}},
			iso20022.ErrBICFormat},
		{"no account at all", AdmissionAcknowledgement{BIC: testBIC, Ref: "adm-x"},
			ErrAdmittedAccountUnusable},
		{"an account naming no asset", AdmissionAcknowledgement{
			BIC: testBIC, Ref: "adm-x", Accounts: map[ledger.AssetCode]ledger.AccountID{"": "200.100.009"}},
			ErrAdmittedAccountUnusable},
		{"an asset naming no account", AdmissionAcknowledgement{
			BIC: testBIC, Ref: "adm-x", Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: ""}},
			ErrAdmittedAccountUnusable},
	} {
		t.Run(tc.what, func(t *testing.T) {
			// A network each, because a case that wrote something would otherwise
			// decide the next one's outcome.
			sys := testNetwork(t)
			var bank *Bank
			own := sys.bank(testBIC)
			mustUpdateAt(t, ctx, own, func(ctx context.Context, tx Tx) (err error) {
				bank, err = own.FoundBankTx(ctx, tx, "Aurora Bank", testBIC, euroOnly)
				return err
			})

			if err := sys.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
				_, err := sys.AdmitMemberTx(ctx, tx, tc.in)
				return err
			}); !errors.Is(err, tc.want) {
				t.Errorf("the clearing house answered an acknowledgement with %s: %v, want %v", tc.what, err, tc.want)
			}
			// The BANK's own unit of work, on the BANK's own database. It shared
			// the clearing house's above, which is what the split takes away: the
			// two refusals are two institutions', asked separately because they
			// have to be.
			if err := sys.bank(bank.BIC).Store().Update(ctx, func(ctx context.Context, tx Tx) error {
				_, err := sys.bank(bank.BIC).RecordMembershipTx(ctx, tx, tc.in)
				return err
			}); !errors.Is(err, tc.want) {
				t.Errorf("the bank answered an acknowledgement with %s: %v, want %v", tc.what, err, tc.want)
			}

			// Neither institution wrote anything, and — the load-bearing half —
			// the real acknowledgement still goes through. A refusal that left a
			// row behind would wedge the admission it was protecting.
			if got := mustGetBank(t, ctx, sys, bank.ID); got.Status != BankFounded {
				t.Errorf("the bank is %q after the refusal, want %q", got.Status, BankFounded)
			}
			mustUpdate(t, ctx, sys, func(ctx context.Context, tx Tx) error {
				_, err := sys.AdmitMemberTx(ctx, tx, real)
				return err
			})
			mustUpdateAt(t, ctx, sys.bank(bank.BIC), func(ctx context.Context, tx Tx) error {
				_, err := sys.bank(bank.BIC).RecordMembershipTx(ctx, tx, real)
				return err
			})
			admitted := mustGetBank(t, ctx, sys, bank.ID)
			assertEqual(t, "the bank after the true acknowledgement", string(admitted.Status), "Member")
			assertEqual(t, "its settlement reference", string(admitted.Assets[testAsset].Settlement), "200.100.001")
		})
	}
}

// TestABankCannotRecordAnotherBanksMembership is ErrStatementNotForThisBank's
// shape, one flow over. The actor passes its own id and the domain refuses the
// one that is not its business, because nothing in the signature can.
func TestABankCannotRecordAnotherBanksMembership(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	// One unit of work each, at each bank's own network over its own database.
	// They shared one, which is the thing that cannot be done any more: two banks
	// founding themselves is two institutions, and a Tx belongs to one.
	var aurora, verde *Bank
	auroraNet, verdeNet := sys.bank("AURODEFFXXX"), sys.bank("VERDITMMXXX")
	mustUpdateAt(t, ctx, auroraNet, func(ctx context.Context, tx Tx) (err error) {
		aurora, err = auroraNet.FoundBankTx(ctx, tx, "Aurora Bank", "AURODEFFXXX", euroOnly)
		return err
	})
	mustUpdateAt(t, ctx, verdeNet, func(ctx context.Context, tx Tx) (err error) {
		verde, err = verdeNet.FoundBankTx(ctx, tx, "Banca Verde", "VERDITMMXXX", euroOnly)
		return err
	})

	// The acknowledgement is Aurora's; Verde tries to record it as its own.
	ack := AdmissionAcknowledgement{
		BIC:      aurora.BIC,
		Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001"},
		Ref:      "adm-1",
	}
	err := sys.bank(verde.BIC).Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		_, err := sys.bank(verde.BIC).RecordMembershipTx(ctx, tx, ack)
		return err
	})
	if !errors.Is(err, ErrNotThisBanksAdmission) {
		t.Fatalf("recording another bank's admission: %v, want ErrNotThisBanksAdmission", err)
	}
	// Neither bank moved. Verde is not a member, and Aurora did not become one
	// because somebody else read its acknowledgement.
	for _, want := range []*Bank{aurora, verde} {
		got := mustGetBank(t, ctx, sys, want.ID)
		if got.Status != BankFounded {
			t.Errorf("%s is %q after a refused recording, want %q", got.Name, got.Status, BankFounded)
		}
		if got.Assets[testAsset].Settlement != "" {
			t.Errorf("%s now names settlement account %q", got.Name, got.Assets[testAsset].Settlement)
		}
	}

	// And the bank the acknowledgement IS addressed to records it.
	mustUpdateAt(t, ctx, sys.bank(aurora.BIC), func(ctx context.Context, tx Tx) error {
		_, err := sys.bank(aurora.BIC).RecordMembershipTx(ctx, tx, ack)
		return err
	})
	got := mustGetBank(t, ctx, sys, aurora.ID)
	assertEqual(t, "Aurora's status once it has recorded its admission", got.Status, BankMember)
	assertEqual(t, "the settlement account Aurora recorded", got.Assets[testAsset].Settlement, "200.100.001")
}

func TestAccountsForUnknownAssetFails(t *testing.T) {
	sys := testNetwork(t)

	p, err := storetest.Admit(context.Background(), sys.nets, "Alpha", testBIC, nil) // defaults to EUR
	assertNoError(t, err)
	assertEqual(t, "assets a bank joins with by default", len(p.Assets), 1)

	_, err = p.AccountsFor("BTC")
	assertError(t, err, ErrParticipantAssetNotFound)
}

// Settlement must never fall back to a base currency when the settlement agent
// holds no account for a member in the cycle's asset. Taking the euro account
// away from a member that is about to settle simulates the state a future
// non-euro scheme would produce naturally.
//
// The asset is taken off the CENTRAL BANK's own member row and it used to be
// taken off the bank's. Both were ErrParticipantAssetNotFound and only one of
// them is a question this institution can ask: settlementLegsTx reads the agent's
// own register now, because the agent's database holds no bank rows to read. See
// its doc for what the bank's own AccountsFor check was and why losing it costs
// nothing the settlement agent is entitled to decide.
func TestSettleCycleFailsWhenParticipantLacksTheAsset(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 30000,
		Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	assertNoError(t, err)
	_, err = sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)

	assertNoError(t, sys.stores.CentralBank().Update(ctx, func(ctx context.Context, tx Tx) error {
		member, err := tx.GetSettlementMember(ctx, b.BIC)
		if err != nil {
			return err
		}
		delete(member.Accounts, testAsset)
		return tx.PutSettlementMember(ctx, member)
	}))

	_, _, err = sys.settleCycle(ctx, cyc.ID)
	assertError(t, err, ErrParticipantAssetNotFound)

	// And nothing was posted: the batch fails whole, exactly as it does for a
	// member that cannot cover its position. The payer's money is still in its
	// bank's suspense, where the debtor leg left it.
	settlements, err := sys.cb().ListSettlements(ctx)
	assertNoError(t, err)
	assertEqual(t, "settlements recorded", len(settlements), 0)

	after, err := sys.GetCycle(ctx, cyc.ID)
	assertNoError(t, err)
	assertEqual(t, "cycle status", after.Status, CycleClosed)
	assertEqual(t, "bank A suspense", bookBalance(t, a.Ledger, accountsOf(t, a).Suspense), 30000)
	assertEqual(t, "bob was not credited", customerBalance(t, b, bob), 0)
}

// The ledger cannot catch this at initiation: the debtor leg alone is a EUR
// debit against a EUR credit, valid double-entry that says nothing about the
// creditor's account. It surfaces only when the payee's bank comes to post the
// creditor leg, by which time the cut-off has settled and the payer has been
// debited for hours — and then as a failure of that ONE payment, see
// TestCrossAssetPaymentSurvivesInitiationAndFailsAtThePayeesBank. The scheme
// check is what makes the refusal immediate and attributable.
func TestPaymentRejectsCreditorAccountNotInSchemeAsset(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	alpha, err := storetest.Admit(ctx, sys.nets, "Alpha", testBIC, euroOnly)
	assertNoError(t, err)
	beta, err := storetest.Admit(ctx, sys.nets, "Beta", testBIC, euroOnly)
	assertNoError(t, err)

	// The payer's leg has to be flawless for this test to be about the payee's.
	// Since the split, the debtor's own bank runs its whole half — account,
	// asset, address, funds — before the creditor's bank sees the payment at
	// all, so a payer with no IBAN would fail with ErrUnaddressableAccount and
	// the asset check under test would never run.
	from := openCustomer(t, ctx, alpha, "Anna", "SE89-ALPHA-0001")
	fundAccount(t, ctx, sys, alpha, from, 100000)
	to, err := beta.OpenCustomerAccount(ctx, "Bruno", "BTC")
	assertNoError(t, err)
	assertNoError(t, beta.Deposit.AddIdentifier(ctx, to.ID,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "IT60-BETA-0001"}))

	_, err = sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)

	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme:          SchemeSEPACT,
		Amount:          1000,
		Debtor:          PartyRef{Account: from.ID},
		Creditor:        PartyRef{Account: to.ID},
		CreditorDetails: PartyDetails{Agent: beta.BIC, Name: to.Name},
		DebtorDetails:   PartyDetails{Agent: alpha.BIC}})
	assertError(t, err, ErrAssetMismatch)
}

// The creditor leg above proves the check reaches an account that may live in a
// different participant's book — nothing forbids a payment whose debtor and
// creditor are in the same one. This proves the debtor leg is checked too: a
// scheme that only validated the creditor would let this one through.
func TestPaymentRejectsDebtorAccountNotInSchemeAsset(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	alpha, err := storetest.Admit(ctx, sys.nets, "Alpha", testBIC, euroOnly)
	assertNoError(t, err)
	beta, err := storetest.Admit(ctx, sys.nets, "Beta", testBIC, euroOnly)
	assertNoError(t, err)

	// Both accounts are addressable, so the only thing wrong with this payment
	// is the payer's asset: with the asset check removed the payment gets
	// further rather than failing for a second reason, which is what makes the
	// counterfactual on this test sharp.
	from, err := alpha.OpenCustomerAccount(ctx, "Anna", "BTC")
	assertNoError(t, err)
	assertNoError(t, alpha.Deposit.AddIdentifier(ctx, from.ID,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-ALPHA-0001"}))
	to := openCustomer(t, ctx, beta, "Bruno", "IT60-BETA-0001")

	_, err = sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)

	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme:          SchemeSEPACT,
		Amount:          1000,
		Debtor:          PartyRef{Account: from.ID},
		Creditor:        PartyRef{Account: to.ID},
		CreditorDetails: PartyDetails{Agent: beta.BIC, Name: to.Name},
		DebtorDetails:   PartyDetails{Agent: alpha.BIC}})
	assertError(t, err, ErrAssetMismatch)
}

// SEPA Direct Debit used to inherit the asset check only because SDD.Validate
// happened to call validateFunds. It now runs unconditionally in each bank's
// own half, before any scheme's Validate is reached, so it applies to SDD
// structurally rather than by that scheme's choice. A valid mandate keeps SDD's
// own mandate guards out of the way, so this isolates the asset check.
//
// Both ends are in BTC, so either half would catch it; a pull is submitted by
// the creditor's bank, so the half that does is the creditor's.
func TestSDDPaymentRejectsAccountNotInSchemeAsset(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	alpha, err := storetest.Admit(ctx, sys.nets, "Alpha", testBIC, euroOnly)
	assertNoError(t, err)
	beta, err := storetest.Admit(ctx, sys.nets, "Beta", testBIC, euroOnly)
	assertNoError(t, err)

	// Both ends in BTC: a mandate's two accounts have to agree (see
	// CreateMandateTx), so the mismatch under test is between the mandate's
	// asset and the SEPA scheme's, not between the two accounts.
	debtorAcct, err := alpha.OpenCustomerAccount(ctx, "Anna", "BTC")
	assertNoError(t, err)
	creditorAcct, err := beta.OpenCustomerAccount(ctx, "Bruno", "BTC")
	assertNoError(t, err)

	debtor := PartyRef{Account: debtorAcct.ID}
	creditor := PartyRef{Account: creditorAcct.ID}
	m, err := sys.bank(beta.BIC).CreateMandate(ctx, alpha.BIC, debtor, creditor, 0)
	assertNoError(t, err)

	_, err = sys.OpenCycle(ctx, SchemeSEPADD)
	assertNoError(t, err)

	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme:          SchemeSEPADD,
		Amount:          1000,
		MandateID:       m.ID,
		Debtor:          debtor,
		Creditor:        creditor,
		DebtorDetails:   PartyDetails{Agent: alpha.BIC, Name: debtorAcct.Name},
		CreditorDetails: PartyDetails{Agent: beta.BIC}})
	assertError(t, err, ErrAssetMismatch)
}

// A mandate whose two accounts are in different assets is CREATED and refuses
// its first collection, and this test is where that ruling is measured.
//
// It used to be TestCreateMandateRejectsMismatchedAssets, and its doc said the
// two accounts "have to agree before the mandate exists — rather than every
// payment it could authorize failing later". The reasoning was sound and the
// read it rested on is gone: the debtor's account is at another bank, so the
// creditor's bank cannot compare the two. See CreateMandateTx.
//
// The claim that replaces it is the second half of the old doc, which was
// already true and already stated: every payment such a mandate could authorise
// fails, because each leg is checked against the SCHEME's asset by its own bank
// and these two cannot both match it. So what is asserted here is that the
// refusal still happens, at a named moment, with the same sentinel — not that
// the mandate is somehow harmless.
//
// The moment moved from creation to first use, which is worse for an operator
// and is the price of the boundary. That is recorded rather than hidden.
func TestAMismatchedMandateIsRefusedAtItsFirstCollection(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	alpha, err := storetest.Admit(ctx, sys.nets, "Alpha", testBIC, euroOnly)
	assertNoError(t, err)
	beta, err := storetest.Admit(ctx, sys.nets, "Beta", testBIC2, euroOnly)
	assertNoError(t, err)

	debtorAcct, err := alpha.OpenCustomerAccount(ctx, "Anna", testAsset)
	assertNoError(t, err)
	creditorAcct, err := beta.OpenCustomerAccount(ctx, "Bruno", "BTC")
	assertNoError(t, err)

	// Created, and it records the CREDITOR's asset, because that is the account
	// the bank recording it holds.
	m, err := sys.bank(beta.BIC).CreateMandate(ctx,
		alpha.BIC,
		PartyRef{Account: debtorAcct.ID},
		PartyRef{Account: creditorAcct.ID}, 50000)
	assertNoError(t, err)
	assertEqual(t, "the mandate's asset", string(m.Asset), "BTC")

	mandates, err := sys.bank(beta.BIC).ListMandates(ctx)
	assertNoError(t, err)
	assertEqual(t, "mandates recorded", len(mandates), 1)

	// And the collection it authorises is refused, by the bank whose account
	// does not match the scheme's asset.
	openCycle(t, ctx, sys, SchemeSEPADD)
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme:          SchemeSEPADD,
		Amount:          1000,
		MandateID:       m.ID,
		Debtor:          PartyRef{Account: debtorAcct.ID},
		Creditor:        PartyRef{Account: creditorAcct.ID},
		DebtorDetails:   PartyDetails{Agent: alpha.BIC, Name: debtorAcct.Name},
		CreditorDetails: PartyDetails{Agent: beta.BIC}})
	assertError(t, err, ErrAssetMismatch)
}

// A mandate is the CREDITOR's bank's row, and no other bank may record, read or
// revoke one.
//
// It is the storage agreeing with a rule the code already stated:
// SDD.ValidateMandate opens "the half the CREDITOR's bank runs, because in SEPA
// the creditor holds the mandate". Until this change CreateMandateTx checked
// BOTH parties' accounts — one unit of work reading two banks' deposit registers
// — and GET /mandates was on the clearing house's port, where rendering a
// mandate meant loading the DEBTOR's bank and listing its register for an asset.
//
// The DEBTOR is not refused here and is not checked either: it is another bank's
// customer, recorded from what the creditor said, exactly as a payment's
// counterparty is since Task 14. Its BANK is recorded, as an address — see
// Mandate.DebtorAgent.
//
// # What this test lost at Task 18, and what has to give it back
//
// It used to assert two more things: that the debtor's bank READING the mandate
// was refused with ErrNotThisBanksMandate, and that its listing held none. Both
// were decided by comparing the mandate's creditor participant against the
// asking bank, and a mandate carries no creditor bank any more — it cannot,
// because every mandate in a creditor bank's database is that bank's, so the
// column would hold one value for ever (see the mandates statement in the bank
// schema).
//
// What is supposed to make those two true is the STORE: another bank's mandate
// is not a row this database holds, so the read is a not-found and the listing is
// empty by construction. The wiring that gives each entity a database has not
// landed, so both are asserted here as the transitional truth rather than as the
// intended one — a shared store answers about rows it should not see. See
// Network.ListMandates, which names the same gap, and restore these two
// assertions as isolation claims when the split lands.
func TestAMandateBelongsToItsCreditorsBankAndToNoOther(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	debtor := PartyRef{Account: alice}
	creditor := PartyRef{Account: bob}

	// The DEBTOR's bank cannot record it — not because it is told whose mandate
	// this is, but because the creditor account named is not one of its own, and
	// a mandate's creditor is checked against the recording bank's own register.
	// That is the same refusal from the only table that can still answer it.
	_, err := sys.bank(a.BIC).CreateMandate(ctx, a.BIC, debtor, creditor, 0)
	assertError(t, err, ErrAccountNotInParticipant)
	// The clearing house cannot either, and that refusal is unchanged: it is not
	// a member bank, so this is not its act at all.
	_, err = sys.CreateMandate(ctx, a.BIC, debtor, creditor, 0)
	assertError(t, err, ErrNotThisInstitutionsAct)

	// The creditor's bank can, and the row carries its own account's asset and
	// the debtor's bank as an address.
	m, err := sys.bank(b.BIC).CreateMandate(ctx, a.BIC, debtor, creditor, 0)
	assertNoError(t, err)
	assertEqual(t, "the mandate's asset", string(m.Asset), string(testAsset))
	assertEqual(t, "the mandate's debtor agent", string(m.DebtorAgent), string(a.BIC))

	// Revoking is still refused to anything that is not a member bank, which is
	// the whole of what the row can now decide on its own.
	if err := sys.RevokeMandate(ctx, m.ID); !errors.Is(err, ErrNotThisBanksMandate) {
		t.Errorf("the clearing house revoked a mandate: %v", err)
	}

	// The creditor's bank sees its own. The debtor's bank sees it too, today,
	// and that is the transitional gap named above rather than a rule.
	mine, err := sys.bank(b.BIC).ListMandates(ctx)
	assertNoError(t, err)
	assertEqual(t, "the creditor bank's mandates", len(mine), 1)
	theirs, err := sys.bank(a.BIC).ListMandates(ctx)
	assertNoError(t, err)
	assertEqual(t, "the debtor bank's mandates, until each bank has a store", len(theirs), 1)
}

// What the ledger does and does not catch about a euro-to-bitcoin payment.
//
// The claim this test exists to keep honest is a narrow one, and it was stated
// too broadly once already. The ledger DOES catch a cross-asset payment — at the
// creditor leg. PostCreditorLegTx resolves the creditor's suspense account with
// creditor.AccountsFor(scheme.Asset()), so the leg comes out as a EUR suspense
// debit against a BTC credit, and validateBalance refuses it with
// ErrUnbalancedAsset.
//
// What the ledger cannot catch is the payment at INITIATION. The debtor leg on
// its own is impeccable double-entry within one asset, and nothing in that
// posting says a second posting elsewhere is its other half.
//
// # What it costs has changed, and this is where that shows
//
// It used to take down the whole clearing cycle, because the creditor leg was
// posted inside the settlement agent's unit of work: one bad payment and no
// member settled. Task 15b.3 made the leg the payee's bank's own act, in its own
// unit of work, so the cut-off now settles and this ONE payment stays Cleared
// while the ledger refuses it. That is a strictly better failure — it is the
// same argument that made the closed-account check affordable — and what
// ErrAssetMismatch still buys is finding out at initiation, where the error can
// name the payment rather than an imbalance.
//
// Constructing the state needs the store directly, because ErrAssetMismatch
// now refuses such a payment at initiation — which is the point.
func TestCrossAssetPaymentSurvivesInitiationAndFailsAtThePayeesBank(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	// A BTC account in a euro-only bank: allowed, because an account's asset
	// is validated against the known assets, not against what its bank
	// clears in.
	bobBTC, err := b.OpenCustomerAccount(ctx, "Bob BTC", "BTC")
	assertNoError(t, err)

	cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)
	pay, err := initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 30000,
		Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	assertNoError(t, err)

	// The debtor leg is already posted and already balanced — in EUR, on its
	// own, with nothing wrong with it.
	assertEqual(t, "bank A suspense after initiation", bookBalance(t, a.Ledger, accountsOf(t, a).Suspense), 30000)

	// Point the creditor end at the bitcoin account, the state ErrAssetMismatch
	// exists to prevent.
	assertNoError(t, sys.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		stored, err := tx.GetPayment(ctx, pay.ID)
		if err != nil {
			return err
		}
		stored.Creditor.Account = bobBTC.ID
		return tx.PutPayment(ctx, stored)
	}))

	_, err = sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)

	// The cut-off itself is untouched by this: the settlement agent nets reserves
	// and never reads a payee's account.
	_, statements, err := sys.settleCycle(ctx, cyc.ID)
	assertNoError(t, err)
	bookTheAdvices(t, sys, statements)

	// The refusal arrives at the payee's bank, when it comes to pay.
	_, err = sys.bank(b.BIC).SettleAtBank(ctx, pay.ID)
	assertError(t, err, ledger.ErrUnbalancedAsset)

	// The one payment fails, not the batch: the cycle settled, and this payment
	// is still Cleared with nobody credited in either asset.
	settlements, err := sys.ListSettlements(ctx)
	assertNoError(t, err)
	assertEqual(t, "settlements recorded", len(settlements), 1)
	after, err := sys.GetPayment(ctx, pay.ID)
	assertNoError(t, err)
	assertEqual(t, "payment status", after.Status, Cleared)
	assertEqual(t, "bob's euro account was not credited", customerBalance(t, b, bob), 0)
	assertEqual(t, "bob's bitcoin account was not credited", customerBalance(t, b, bobBTC.ID), 0)
}

// SEPA is a euro scheme, not a scheme that happens to be tested with EUR
// accounts. This pins that fact directly on the scheme types.
func TestSEPASchemesAreEuroSchemes(t *testing.T) {
	for _, sc := range []Scheme{SCT{}, SDD{}} {
		if sc.Asset() != "EUR" {
			t.Errorf("%s asset = %q, want EUR", sc.ID(), sc.Asset())
		}
	}
}

// ---------------------------------------------------------------------------
// Bank.RunEndOfDay: driving deposit and lending together
// ---------------------------------------------------------------------------

func TestParticipantRunEndOfDay_DrivesBothLayers(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	bank, err := storetest.Admit(ctx, net.nets, "Aurora Bank", testBIC, euroOnly)
	assertNoError(t, err)

	// An overdrawn current account with a priced overdraft.
	bruno, err := bank.OpenCustomerAccount(ctx, "Bruno Bianchi", testAsset)
	assertNoError(t, err)
	_, err = bank.Deposit.SetOverdraftLimit(ctx, bruno.ID, 50_000, time.Time{})
	assertNoError(t, err)
	_, err = bank.Deposit.SetOverdraftPricingOverlay(ctx, bruno.ID,
		&product.OverdraftPricing{Rate: 150_000, DayCount: interest.ACT365}, time.Time{})
	assertNoError(t, err)
	fundAccount(t, ctx, net, bank, bruno, 5_000)

	// And a drawn revolving line.
	line, err := bank.Lending.OpenRevolvingLine(ctx, bank.CustomerSubledger, "Bruno Line", testAsset, 250_000, 180_000, interest.ACT365, 20_000)
	assertNoError(t, err)
	brunoGL, err := bank.Deposit.GetAccount(ctx, bruno.ID)
	assertNoError(t, err)
	_, err = bank.Lending.Draw(ctx, line.ID, brunoGL.GLAccount, 100_000, "Draw")
	assertNoError(t, err)

	// Spend the account into overdraft.
	merchant, err := bank.OpenCustomerAccount(ctx, "Merchant", testAsset)
	assertNoError(t, err)
	merchantGL, err := bank.Deposit.GetAccount(ctx, merchant.ID)
	assertNoError(t, err)
	_, err = bank.Ledger.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "Card payment",
		Entries: []ledger.Entry{
			{AccountID: brunoGL.GLAccount, Amount: 110_000, Direction: ledger.Debit},
			{AccountID: merchantGL.GLAccount, Amount: 110_000, Direction: ledger.Credit},
		},
	})
	assertNoError(t, err)

	// One call runs both batches.
	start := fixedTime
	assertNoError(t, bank.RunEndOfDay(ctx, start))
	assertNoError(t, bank.RunEndOfDay(ctx, start.AddDate(0, 0, 1)))

	account, err := bank.Deposit.GetAccount(ctx, bruno.ID)
	assertNoError(t, err)
	if account.Accrued <= 0 {
		t.Errorf("the overdraft did not accrue: %d", account.Accrued)
	}

	facility, err := bank.Lending.GetFacility(ctx, line.ID)
	assertNoError(t, err)
	if facility.Accrued <= 0 {
		t.Errorf("the line did not accrue: %d", facility.Accrued)
	}
}

// A bank with neither loans nor priced overdrafts must still run — over a real
// portfolio that is most banks on most days.
func TestParticipantRunEndOfDay_QuietBank(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	bank, err := storetest.Admit(ctx, net.nets, "Quiet Bank", testBIC, euroOnly)
	assertNoError(t, err)
	assertNoError(t, bank.RunEndOfDay(ctx, fixedTime))
}

// ---------------------------------------------------------------------------
// Enum String() methods
// ---------------------------------------------------------------------------

func TestStringers(t *testing.T) {
	assertEqual(t, "push", Push.String(), "Push")
	assertEqual(t, "pull", Pull.String(), "Pull")
	assertEqual(t, "dir unknown", SchemeDirection(99).String(), "Unknown")
	assertEqual(t, "net", Net.String(), "Net")
	assertEqual(t, "gross", Gross.String(), "Gross")
	assertEqual(t, "model unknown", SettlementModel(99).String(), "Unknown")
	assertEqual(t, "settled", Settled.String(), "Settled")
	assertEqual(t, "returned", Returned.String(), "Returned")
	assertEqual(t, "status unknown", PaymentStatus(99).String(), "Unknown")
	assertEqual(t, "mandate active", MandateActive.String(), "Active")
	assertEqual(t, "mandate unknown", MandateStatus(99).String(), "Unknown")
	assertEqual(t, "cycle closed", CycleClosed.String(), "Closed")
	assertEqual(t, "cycle unknown", CycleStatus(99).String(), "Unknown")
}

// ---------------------------------------------------------------------------
// Test assertion helpers (mirroring the ledger package's style)
// ---------------------------------------------------------------------------

func assertNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertError(t *testing.T, err, target error) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %v, got nil", target)
	}
	if !errors.Is(err, target) {
		t.Fatalf("expected error %v, got %v", target, err)
	}
}

func assertEqual[T comparable](t *testing.T, label string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %v, want %v", label, got, want)
	}
}

// ---------------------------------------------------------------------------
// ResolveIdentifier — one bank's own register
// ---------------------------------------------------------------------------
//
// This block used to be headed "the network's directory" and its first test was
// TestResolveIdentifierAcrossBanks, which is the name of the crossing rather
// than of a feature. Task 18a narrowed the lookup to the asking bank's own
// register; the four tests below are the same four questions asked of the shape
// that replaced it, and one of them now records a guarantee that is GONE.

// TestResolveIdentifierAnswersOnlyForTheAskingBanksOwnRegister is the
// narrowing, and it is one assertion followed by its complement — the second is
// the whole change and the first would pass on the sweep too.
func TestResolveIdentifierAnswersOnlyForTheAskingBanksOwnRegister(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)
	verde := addParticipant(t, ctx, net, "Banca Verde", verdeBIC)

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	_ = openCustomer(t, ctx, verde, "Bruno", "IT60-VERDE-2001")
	alicesIBAN := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"}

	// The bank that holds the account answers about it.
	ref, err := net.bank(aurora.BIC).ResolveIdentifier(ctx, alicesIBAN)
	if err != nil {
		t.Fatalf("Aurora resolving its own customer: %v", err)
	}
	// The ref names the account and not the bank: a resolution answers about THIS
	// bank's own register, so the only bank it could name is the one that asked.
	// See PartyRef, and ResolveIdentifierTx.
	if ref.Account != alice.ID {
		t.Fatalf("resolved %s, want %s", ref.Account, alice.ID)
	}

	// And the bank that does not hold it has no answer — not the account, and
	// not "it is at Aurora" either. That second thing is what a directory
	// SERVICE would say and what the sweep used to say; a bank saying it would
	// be reading another bank's register, which is the crossing.
	if _, err := net.bank(verde.BIC).ResolveIdentifier(ctx, alicesIBAN); !errors.Is(err, deposit.ErrIdentifierNotFound) {
		t.Fatalf("Verde resolving Aurora's customer = %v, want ErrIdentifierNotFound", err)
	}
}

func TestResolveIdentifierNotFound(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)

	_, err := net.bank(aurora.BIC).ResolveIdentifier(ctx, deposit.Identifier{
		Scheme: deposit.IdentifierIBAN, Value: "NOBODY-0001",
	})
	if !errors.Is(err, deposit.ErrIdentifierNotFound) {
		t.Fatalf("ResolveIdentifier = %v, want ErrIdentifierNotFound", err)
	}
}

// TestACrossBankCollisionIsNoLongerObservable records a guarantee this system
// HAD and no longer has, which is why it is a test rather than a deletion.
//
// It used to be TestResolveIdentifierRefusesACrossBankCollision, and its
// argument was sound: uniqueness is enforced per bank, that is the widest scope
// a register can see, so two banks issuing one IBAN is representable and the
// sweep refused to choose between them. The sweep was the only thing that could
// SEE both, and it is gone — so each bank now answers confidently about its own
// account and neither can know the other exists.
//
// That is not a regression introduced by the narrowing; it is the narrowing's
// price, stated. In life it is the IBAN's issuer registry — the national
// numbering authority, and the bank code inside the IBAN itself — that stops two
// banks issuing one address, not a directory noticing afterwards. This system
// has no such authority, so nothing here refuses it. The within-bank half below
// is what survives, and it is the half a register can actually police.
func TestACrossBankCollisionIsNoLongerObservable(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)
	verde := addParticipant(t, ctx, net, "Banca Verde", verdeBIC)

	shared := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SHARED-0001"}
	alice := openCustomer(t, ctx, aurora, "Alice", "SHARED-0001")
	bruno := openCustomer(t, ctx, verde, "Bruno", "SHARED-0001")

	for _, c := range []struct {
		bank *Bank
		want deposit.AccountID
	}{{aurora, alice.ID}, {verde, bruno.ID}} {
		ref, err := net.bank(c.bank.BIC).ResolveIdentifier(ctx, shared)
		if err != nil {
			t.Fatalf("%s resolving the shared address: %v", c.bank.Name, err)
		}
		if ref.Account != c.want {
			t.Errorf("%s resolved %s, want its own %s", c.bank.Name, ref.Account, c.want)
		}
	}
}

// The other half of the same claim, and the half that survives the narrowing.
//
// Two accounts inside ONE bank holding one address is ambiguous, because that
// is the scope a register can see — and it is what makes the missing UNIQUE
// constraint safe. The cross-bank half above used to be covered by the same
// `hits` accumulator and is not covered by anything now.
//
// The duplicate is written straight through the store, past the register's
// write-time check, because that is the only way it arises: a race between two
// AddIdentifier calls that both read before either wrote.
func TestResolveIdentifierRefusesAWithinBankCollision(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)
	addParticipant(t, ctx, net, "Banca Verde", verdeBIC)

	shared := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SHARED-0001"}
	openCustomer(t, ctx, aurora, "Alice", "SHARED-0001")
	aaron := openCustomer(t, ctx, aurora, "Aaron", "SE89-AURORA-0002")

	assertNoError(t, aurora.Deposit.Store().Update(ctx, func(ctx context.Context, tx deposit.Tx) error {
		a, err := tx.GetDepositAccount(ctx, aurora.Deposit.BookID(), aaron.ID)
		if err != nil {
			return err
		}
		a.Identifiers = append(a.Identifiers, shared)
		return tx.PutDepositAccount(ctx, aurora.Deposit.BookID(), a)
	}))

	if _, err := net.bank(aurora.BIC).ResolveIdentifier(ctx, shared); !errors.Is(err, deposit.ErrIdentifierAmbiguous) {
		t.Fatalf("ResolveIdentifier = %v, want ErrIdentifierAmbiguous", err)
	}
}

// ---------------------------------------------------------------------------
// AddressedBy — the scheme declares what addresses it, initiation enforces it
// ---------------------------------------------------------------------------

func TestInitiateRefusesAnAccountWithNoIdentifierInTheSchemesScheme(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)
	verde := addParticipant(t, ctx, net, "Banca Verde", verdeBIC)
	openCycle(t, ctx, net, SchemeSEPACT)

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	// Bruno has no IBAN at all: an SCT cannot address him.
	bruno := openCustomerWithoutIdentifier(t, ctx, verde, "Bruno")

	_, err := initiate(ctx, net, InitiatePaymentRequest{
		Scheme:          SchemeSEPACT,
		Debtor:          PartyRef{Account: alice.ID},
		Creditor:        PartyRef{Account: bruno.ID},
		Amount:          10_00,
		CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
		DebtorDetails:   PartyDetails{Agent: aurora.BIC}})
	if !errors.Is(err, ErrUnaddressableAccount) {
		t.Fatalf("initiation = %v, want ErrUnaddressableAccount", err)
	}
}

// TestTheFarLegsAddressAndAccountCannotDisagree is
// TestInitiateRefusesAQuotedIdentifierTheAccountDoesNotHold's successor, and
// the change of subject is the finding.
//
// It used to quote one bank's address against another bank's account and expect
// ErrIdentifierMismatch: addressFor compared the quoted address against the
// identifiers on the account the REF named, and the two disagreed. That
// comparison ran at the receiving bank, on a ref the SUBMITTING bank wrote.
//
// Since Task 18a the receiving bank resolves its own party FROM the address, so
// the account and the address cannot disagree on the far leg — the account is
// whichever one holds the address. What is left is the case underneath: an
// address this bank does not hold, which is ErrAccountNotInParticipant and AC01
// on the wire. That is the answer a real receiving bank gives, and the mismatch
// it replaces was only ever reachable because the ref was taken on trust.
//
// Task 18d moved WHERE that is true without changing that it is. resolveOwnPartyTx
// is deleted: there is no foreign row for the receiving bank to correct, because
// each institution writes its own from the message, and the message carries an
// address and no account id. So "cannot disagree" is now a property of what a
// pacs.008 contains rather than of a correction AcceptInboundTx makes — which is
// why the request below quotes an address and leaves the far leg's account
// empty. A fixture that filled it in would be giving the receiving bank a fact
// no message could deliver.
//
// ErrIdentifierMismatch is not dead. It still fires on the SUBMITTING bank's OWN
// leg, where the ref is that bank's own and the quoted address is the payer's
// claim about their own account —
// TestInitiateRefusesAQuotedIdentifierOnTheDebtorLeg is the pin, and it is now
// the only one.
func TestTheFarLegsAddressAndAccountCannotDisagree(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)
	verde := addParticipant(t, ctx, net, "Banca Verde", verdeBIC)
	openCycle(t, ctx, net, SchemeSEPACT)

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno", "IT60-VERDE-2001")

	// The far leg is an ADDRESS and no account, which is what a pacs.008 carries
	// and therefore all the receiving bank is given. Naming Bruno's account here
	// would be the fixture handing Verde an answer no message could have brought
	// it — see relayedFrom — and it would be refused a leg earlier, by the
	// address-against-account comparison rather than by the resolution.
	//
	// The address is Alice's, which Verde does not hold.
	_, err := initiate(ctx, net, InitiatePaymentRequest{
		Scheme: SchemeSEPACT,
		Debtor: PartyRef{Account: alice.ID},
		Creditor: PartyRef{
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"},
		},
		Amount:          10_00,
		CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
		DebtorDetails:   PartyDetails{Agent: aurora.BIC}})
	if !errors.Is(err, ErrAccountNotInParticipant) {
		t.Fatalf("initiation = %v, want ErrAccountNotInParticipant", err)
	}
}

func TestInitiateRefusesAQuotedIdentifierOnTheDebtorLeg(t *testing.T) {
	// The creditor-leg case above and this one are separate tests on purpose:
	// addressFor is called once per leg, and a check wired to only one of them
	// passes every creditor-leg test in the file.
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)
	verde := addParticipant(t, ctx, net, "Banca Verde", verdeBIC)
	openCycle(t, ctx, net, SchemeSEPACT)

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno", "IT60-VERDE-2001")

	_, err := initiate(ctx, net, InitiatePaymentRequest{
		Scheme: SchemeSEPACT,
		Debtor: PartyRef{
			Account: alice.ID,
			// Bruno's address, pointing at Alice's account.
			Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "IT60-VERDE-2001"},
		},
		Creditor:        PartyRef{Account: bruno.ID},
		Amount:          10_00,
		CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
		DebtorDetails:   PartyDetails{Agent: aurora.BIC}})
	if !errors.Is(err, ErrIdentifierMismatch) {
		t.Fatalf("initiation = %v, want ErrIdentifierMismatch", err)
	}
}

// An address the account really holds, but in the wrong identifier scheme.
//
// testPAN is declared here rather than shipped as a deposit constant, because
// the card scheme it would address does not exist yet — which is the whole
// reason this test matters. Nothing in the shipped system can reach this case
// while IBAN is the only scheme, so the check would have gone on being wrong
// until the day a PAN arrived, and on that day it would have been wrong
// silently: an account holding both would have had a SEPA payment accepted, and
// stored, quoting its card number. Scheme.AddressedBy() means the address is
// bound to the scheme, not merely to the account.
func TestInitiateRefusesAnAddressFromAnotherIdentifierScheme(t *testing.T) {
	const testPAN = deposit.IdentifierScheme("PAN")

	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)
	verde := addParticipant(t, ctx, net, "Banca Verde", verdeBIC)

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno", "IT60-VERDE-2001")

	// Both customers keep their IBAN and gain a card, exactly as the design
	// says a plural identifier set is for.
	alicePAN := deposit.Identifier{Scheme: testPAN, Value: "4000-0000-0000-0001"}
	brunoPAN := deposit.Identifier{Scheme: testPAN, Value: "4000-0000-0000-0002"}
	assertNoError(t, aurora.Deposit.AddIdentifier(ctx, alice.ID, alicePAN))
	assertNoError(t, verde.Deposit.AddIdentifier(ctx, bruno.ID, brunoPAN))

	// Creditor leg.
	_, err := initiate(ctx, net, InitiatePaymentRequest{
		Scheme:          SchemeSEPACT,
		Debtor:          PartyRef{Account: alice.ID},
		Creditor:        PartyRef{Account: bruno.ID, Identifier: brunoPAN},
		Amount:          10_00,
		CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
		DebtorDetails:   PartyDetails{Agent: aurora.BIC}})
	if !errors.Is(err, ErrIdentifierMismatch) {
		t.Fatalf("creditor quoting a PAN for an SCT = %v, want ErrIdentifierMismatch", err)
	}

	// Debtor leg.
	_, err = initiate(ctx, net, InitiatePaymentRequest{
		Scheme:          SchemeSEPACT,
		Debtor:          PartyRef{Account: alice.ID, Identifier: alicePAN},
		Creditor:        PartyRef{Account: bruno.ID},
		Amount:          10_00,
		CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
		DebtorDetails:   PartyDetails{Agent: aurora.BIC}})
	if !errors.Is(err, ErrIdentifierMismatch) {
		t.Fatalf("debtor quoting a PAN for an SCT = %v, want ErrIdentifierMismatch", err)
	}

	// And the PAN does not disturb the back-fill: one IBAN each is still one
	// candidate each, so the ordinary payment goes through and records IBANs.
	var pay Payment
	runCycle(t, net, SchemeSEPACT, func() {
		pay, err = initiate(ctx, net, InitiatePaymentRequest{
			Scheme:          SchemeSEPACT,
			Debtor:          PartyRef{Account: alice.ID},
			Creditor:        PartyRef{Account: bruno.ID},
			Amount:          10_00,
			CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
			DebtorDetails:   PartyDetails{Agent: aurora.BIC}})
		assertNoError(t, err)
	})
	// Each on the copy that makes it; see TestInitiateBackFillsTheAddressOnBothLegs.
	assertEqual(t, "back-filled debtor address",
		mustGetPaymentAt(t, ctx, net.bank(aurora.BIC), pay.ID).Debtor.Identifier,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"})
	assertEqual(t, "back-filled creditor address",
		mustGetPaymentAt(t, ctx, net.bank(verde.BIC), pay.ID).Creditor.Identifier,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "IT60-VERDE-2001"})
}

// A payment that quotes no address still records one. Before this, the
// identifier was optional on the way in and simply stayed empty on the way to
// storage, so the documented property — "a payment records the address it was
// sent to" — held only for callers who volunteered it, which the API's own
// tests never did.
//
// # "Both legs" is now two institutions, and that is the finding
//
// It used to be one row, so one assertion pair read both back-fills. Since Task
// 18d a bank fills in ITS OWN side and no other: SubmitPaymentTx runs
// debtorSideTx on a push and writes DebtorDetails from its own register,
// AcceptInboundTx runs creditorSideTx at the OTHER bank, and neither can reach
// the other's account to address it. So the debtor's IBAN is on Aurora's copy,
// the creditor's is on Verde's, and the clearing house's copy — which is what
// the initiate helper returns — carries what the message said and back-fills
// nothing, because a clearing house holds no register.
//
// The property survives intact. Every address that can be filled in is filled
// in by the one institution that could know it. What is gone is the single row
// that held both at once, and asserting against it was asserting against a
// value no institution in the running system can produce.
func TestInitiateBackFillsTheAddressOnBothLegs(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)
	verde := addParticipant(t, ctx, net, "Banca Verde", verdeBIC)

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno", "IT60-VERDE-2001")

	var pay Payment
	runCycle(t, net, SchemeSEPACT, func() {
		var err error
		pay, err = initiate(ctx, net, InitiatePaymentRequest{
			Scheme:          SchemeSEPACT,
			Debtor:          PartyRef{Account: alice.ID},
			Creditor:        PartyRef{Account: bruno.ID},
			Amount:          10_00,
			CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
			DebtorDetails:   PartyDetails{Agent: aurora.BIC}})
		assertNoError(t, err)
	})

	alicesIBAN := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1001"}
	brunosIBAN := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "IT60-VERDE-2001"}

	// Each bank's own leg, on that bank's own copy, read back out of storage
	// rather than taken from the value a call returned.
	atAurora := mustGetPaymentAt(t, ctx, net.bank(aurora.BIC), pay.ID)
	assertEqual(t, "the debtor address on the payer's bank's copy", atAurora.Debtor.Identifier, alicesIBAN)
	atVerde := mustGetPaymentAt(t, ctx, net.bank(verde.BIC), pay.ID)
	assertEqual(t, "the creditor address on the payee's bank's copy", atVerde.Creditor.Identifier, brunosIBAN)

	// The payer's back-fill travels: it was made before the instruction went out,
	// so it is on the message and therefore on both downstream copies. The
	// payee's does not — it is made on arrival, by the last institution the
	// message reaches, and nothing carries it back upstream.
	assertEqual(t, "the debtor address on the payee's bank's copy", atVerde.Debtor.Identifier, alicesIBAN)
	assertEqual(t, "the debtor address on the clearing house's copy", pay.Debtor.Identifier, alicesIBAN)
	assertEqual(t, "the creditor address on the payer's bank's copy", atAurora.Creditor.Identifier, deposit.Identifier{})
	assertEqual(t, "the creditor address on the clearing house's copy", pay.Creditor.Identifier, deposit.Identifier{})
}

// Back-filling stops where choosing would start. Two IBANs on one account and
// nothing quoted is the same shape as an ambiguous resolution, and gets the
// same answer.
func TestInitiateRefusesToChooseBetweenTwoAddresses(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)
	verde := addParticipant(t, ctx, net, "Banca Verde", verdeBIC)

	alice := openCustomer(t, ctx, aurora, "Alice", "SE89-AURORA-1001")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno", "IT60-VERDE-2001")
	// A second IBAN on the debtor: legal, and it makes the debtor leg the one
	// that has to refuse. No cycle is open yet, which is harmless — the
	// addressing checks run before initiation looks for one.
	assertNoError(t, aurora.Deposit.AddIdentifier(ctx, alice.ID,
		deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1002"}))

	_, err := initiate(ctx, net, InitiatePaymentRequest{
		Scheme:          SchemeSEPACT,
		Debtor:          PartyRef{Account: alice.ID},
		Creditor:        PartyRef{Account: bruno.ID},
		Amount:          10_00,
		CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
		DebtorDetails:   PartyDetails{Agent: aurora.BIC}})
	if !errors.Is(err, ErrAmbiguousAddress) {
		t.Fatalf("initiation = %v, want ErrAmbiguousAddress", err)
	}

	// Naming one of them is how the caller gets past it — the refusal is a
	// question, not a dead end.
	runCycle(t, net, SchemeSEPACT, func() {
		pay, err := initiate(ctx, net, InitiatePaymentRequest{
			Scheme: SchemeSEPACT,
			Debtor: PartyRef{
				Account:    alice.ID,
				Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1002"},
			},
			Creditor:        PartyRef{Account: bruno.ID},
			Amount:          10_00,
			CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
			DebtorDetails:   PartyDetails{Agent: aurora.BIC}})
		assertNoError(t, err)
		// The payer's bank's own copy, because the choice is that bank's own
		// act: the clearing house's row carries what the message said, and the
		// message was built after this back-fill, so both agree here — but only
		// one of them is where the fact is made. See
		// TestInitiateBackFillsTheAddressOnBothLegs.
		assertEqual(t, "chosen debtor address",
			mustGetPaymentAt(t, ctx, net.bank(aurora.BIC), pay.ID).Debtor.Identifier,
			deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-AURORA-1002"})
	})
}

// Reissuing an address must not kill the mandates on the account.
//
// A remove plus an add is the documented way to reissue a card, and it moves
// neither balance nor history. Before PartyRef.SameParty it moved something
// else: the mandate compared whole PartyRefs, so after the reissue there was NO
// address the payment could quote that worked — the new one differed from the
// mandate's, the old one no longer belonged to the account, and quoting nothing
// back-filled the new one. With no UpdateMandate, the mandate was dead for good.
func TestMandateSurvivesAReissuedDebtorIdentifier(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)

	debtor := PartyRef{Account: alice}
	creditor := PartyRef{Account: biller}
	m, err := sys.bank(b.BIC).CreateMandate(ctx, a.BIC, debtor, creditor, 0)
	assertNoError(t, err)

	old := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-BANKA-0001"}
	reissued := deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-BANKA-0002"}
	assertNoError(t, a.Deposit.RemoveIdentifier(ctx, alice, old))
	assertNoError(t, a.Deposit.AddIdentifier(ctx, alice, reissued))

	var pay Payment
	runCycle(t, sys, SchemeSEPADD, func() {
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
			Debtor: debtor, Creditor: creditor, Description: "Electricity bill",
			DebtorDetails:   PartyDetails{Agent: a.BIC, Name: "Alice"},
			CreditorDetails: PartyDetails{Agent: b.BIC}})
		assertNoError(t, err)
	})

	// The collection went through, and it records the NEW address — the mandate
	// authorises the account, and each payment records how it was reached.
	assertEqual(t, "reissued debtor address on the payment", pay.Debtor.Identifier, reissued)
	assertEqual(t, "alice", customerBalance(t, a, alice), 75000)
	assertEqual(t, "biller", customerBalance(t, b, biller), 25000)
}

// The other direction of the same change, and the one that has to keep holding.
//
// SameParty deliberately LOOSENS the mandate comparison — it stops comparing
// the quoted address — so what a reader wants proof of is that it did not
// loosen the part that matters: a mandate still authorises exactly one debtor
// account paying exactly one creditor account, and nothing else. Substituting
// either end is ErrMandateMismatch, which before this wave had no test at all.
func TestMandateStillRefusesADifferentParty(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)

	debtor := PartyRef{Account: alice}
	creditor := PartyRef{Account: biller}
	m, err := sys.bank(b.BIC).CreateMandate(ctx, a.BIC, debtor, creditor, 0)
	assertNoError(t, err)

	// A second customer at Alice's own bank, funded, addressable, and party to
	// nothing. Same bank on purpose: the participant matching is the easy half,
	// and an implementation that compared only PartyRef.Participant would pass a
	// cross-bank version of this test.
	carla := openCustomer(t, ctx, a, "Carla", "SE89-BANKA-0009")
	fundAccount(t, ctx, sys, a, carla, 100000)
	// And a second biller at bank B, for the creditor half.
	other := openCustomer(t, ctx, b, "Other Biller", "SE89-BANKB-0009")

	openCycle(t, ctx, sys, SchemeSEPADD)

	// Someone else's account, drawn on under Alice's mandate.
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
		Debtor: PartyRef{Account: carla.ID}, Creditor: creditor,
		DebtorDetails:   PartyDetails{Agent: a.BIC, Name: "Carla"},
		CreditorDetails: PartyDetails{Agent: b.BIC}})
	if !errors.Is(err, ErrMandateMismatch) {
		t.Fatalf("substituted debtor = %v, want ErrMandateMismatch", err)
	}

	// Alice's account, collected by a creditor she never authorised.
	_, err = initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
		Debtor: debtor, Creditor: PartyRef{Account: other.ID},
		DebtorDetails:   PartyDetails{Agent: a.BIC, Name: "Alice"},
		CreditorDetails: PartyDetails{Agent: b.BIC}})
	if !errors.Is(err, ErrMandateMismatch) {
		t.Fatalf("substituted creditor = %v, want ErrMandateMismatch", err)
	}

	// Nothing moved on either attempt: a refused instruction rolls back whole.
	assertEqual(t, "alice", customerBalance(t, a, alice), 100000)
	assertEqual(t, "carla", customerBalance(t, a, carla.ID), 100000)
	assertEqual(t, "biller", customerBalance(t, b, biller), 0)
}

func TestSchemesDeclareTheirIdentifierScheme(t *testing.T) {
	if got := (SCT{}).AddressedBy(); got != deposit.IdentifierIBAN {
		t.Fatalf("SCT.AddressedBy() = %q, want %q", got, deposit.IdentifierIBAN)
	}
	if got := (SDD{}).AddressedBy(); got != deposit.IdentifierIBAN {
		t.Fatalf("SDD.AddressedBy() = %q, want %q", got, deposit.IdentifierIBAN)
	}
}

// ---------------------------------------------------------------------------
// The split: a submitting bank's half and a receiving bank's half
// ---------------------------------------------------------------------------

// networkWithTwoBanks is the split's credit-transfer fixture: two addressed
// banks, a funded payer at the first, a payee at the second, and the request
// between them.
//
// No cycle is opened, deliberately. Submission no longer looks for one — which
// cycle a payment joins is the clearing house's business, and every test below
// that submits without opening one is evidence of that.
func networkWithTwoBanks(t *testing.T) (*testSystem, InitiatePaymentRequest) {
	t.Helper()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	return sys, InitiatePaymentRequest{
		Scheme:      SchemeSEPACT,
		Amount:      25000,
		Debtor:      PartyRef{Account: alice},
		Creditor:    PartyRef{Account: bob},
		Description: "Invoice 42",
		// Push: the creditor is the counterparty, so the request must name it —
		// the NAME and the BIC. Neither is derived: Task 18a took the derivation
		// out, because the row it read is the counterparty's own.
		//
		// The DEBTOR's agent is named too, and it is not derived from the ref any
		// more because a ref names no bank (see PartyRef). SubmitPaymentTx
		// discards and refills it from the submitting bank's own row, so what it
		// is for here is the same thing the seed uses it for: saying which bank
		// submits.
		DebtorDetails:   PartyDetails{Agent: a.BIC, Name: "Alice"},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
	}
}

// networkWithACollection is the direct-debit fixture: two addressed banks, a
// mandate the payee holds over the payer, and the collection request under it.
// fund is what the payer's account holds, so a caller can build both a
// collectable and an uncollectable one.
//
// Bank A and Bank B get distinct BICs (testBIC / testBIC2), not addParticipant's
// shared testBIC — same reason as setupTwoBanks: this is the pull direction's
// two-bank fixture, and TestSubmitDerivesTheCounterpartyAgentFromTheRoster's
// pull subtest plants one bank's BIC where the other's belongs.
func networkWithACollection(t *testing.T, fund ledger.Amount) (*testSystem, InitiatePaymentRequest, MandateID) {
	t.Helper()
	ctx := context.Background()
	sys := testNetwork(t)
	a, err := storetest.Admit(ctx, sys.nets, "Bank A", testBIC, euroOnly)
	assertNoError(t, err)
	b, err := storetest.Admit(ctx, sys.nets, "Bank B", testBIC2, euroOnly)
	assertNoError(t, err)
	payer := openCustomer(t, ctx, a, "Alice", "SE89-BANKA-0001")
	payee := openCustomer(t, ctx, b, "Biller", "SE89-BANKB-0001")
	if fund > 0 {
		fundAccount(t, ctx, sys, a, payer, fund)
	}
	debtor := PartyRef{Account: payer.ID}
	creditor := PartyRef{Account: payee.ID}
	// The mandate is the CREDITOR bank's row, so it is created through that
	// bank's network and names the debtor's bank as an address. See
	// Mandate.DebtorAgent.
	m, err := sys.bank(b.BIC).CreateMandate(ctx, a.BIC, debtor, creditor, 0)
	assertNoError(t, err)
	return sys, InitiatePaymentRequest{
		Scheme:      SchemeSEPADD,
		Amount:      25000,
		MandateID:   m.ID,
		Debtor:      debtor,
		Creditor:    creditor,
		Description: "Electricity bill",
		// Pull: the debtor is the counterparty, so the request must name it. See
		// networkWithTwoBanks on why both agents are the caller's to supply.
		DebtorDetails:   PartyDetails{Agent: a.BIC, Name: payer.Name},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: payee.Name},
	}, m.ID
}

func networkWithAMandate(t *testing.T) (*testSystem, InitiatePaymentRequest) {
	t.Helper()
	sys, req, _ := networkWithACollection(t, 100000)
	return sys, req
}

func networkWithARevokedMandate(t *testing.T) (*testSystem, InitiatePaymentRequest) {
	t.Helper()
	sys, req, id := networkWithACollection(t, 100000)
	// The creditor's bank revokes: a mandate is its row. On a pull the creditor
	// is the SUBMITTING bank, so this is req's submitter.
	assertNoError(t, sys.bank(req.CreditorDetails.Agent).RevokeMandate(context.Background(), id))
	return sys, req
}

// networkWithAnUnfundedDebtor is the collection fixture whose payer cannot
// cover it. The creditor's bank cannot see that, which is the point.
func networkWithAnUnfundedDebtor(t *testing.T) (*testSystem, InitiatePaymentRequest) {
	t.Helper()
	sys, req, _ := networkWithACollection(t, 0)
	return sys, req
}

// networkWithASubmittedPayment is a push payment that has been submitted and
// RELAYED and nothing more: Initiated at the payer's bank and at the clearing
// house, its debtor leg posted, in no cycle, and not yet answered by the
// creditor's bank.
//
// The relay is not decoration and it is not the receiving bank's answer. The
// clearing house records its own copy BEFORE it routes an instruction on (see
// csm.relayRecorded, where the row is also the duplicate check), so a payment
// the payee's bank has been sent is one this institution is already carrying.
// Every test below that rejects this payment goes through RejectAtCSM, which is
// that institution's act on that institution's row — and without the relay it
// answered "payment not found" about a payment the fixture had just made.
func networkWithASubmittedPayment(t *testing.T) (*testSystem, Payment) {
	t.Helper()
	ctx := context.Background()
	n, req := networkWithTwoBanks(t)
	p, err := n.submit(ctx, req)
	assertNoError(t, err)
	_, err = n.RecordRelayed(ctx, p.ID, relayedFrom(p))
	assertNoError(t, err)
	return n, p
}

// closeCreditorAccount closes the payee's account at the payee's own bank.
func closeCreditorAccount(t *testing.T, n *testSystem, p Payment) {
	t.Helper()
	ctx := context.Background()
	// The payee's own bank, read out of its own database. A customer account is
	// that bank's and nobody else's, and so is the row that names its book.
	assertNoError(t, mustGetBank(t, ctx, n, ParticipantID(p.CreditorDetails.Agent)).
		Deposit.Close(ctx, p.Creditor.Account))
}

// Initiated becomes an observable state for the first time. Today
// InitiatePaymentTx transitions straight to Accepted inside the submitting
// transaction, so no caller has ever seen it; the mesh needs the payment to
// exist, with its debtor leg posted, while the creditor's bank has not yet
// answered.
func TestSubmitLeavesAPushPaymentInitiatedAndOutOfAnyCycle(t *testing.T) {
	n, req := networkWithTwoBanks(t)

	p, err := n.submit(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitPayment: %v", err)
	}
	if p.Status != Initiated {
		t.Errorf("status = %v, want Initiated", p.Status)
	}
	if p.CycleID != "" {
		t.Errorf("cycle = %q, want empty — adding to a cycle is the CSM's act", p.CycleID)
	}
	// The debtor leg IS posted: the payer's money has left, which is what makes
	// a later rejection a reversal rather than a cancellation.
	if p.DebtorLegTx == "" {
		t.Error("no debtor leg posted; a push payment's money leaves at submission")
	}
}

// TestTheClearingHouseWillNotClearForANonMember is the clearing house's half of
// the guard mesh.TestAFoundedBankCanNeitherPayNorBePaid holds at the door.
//
// It is here and not only there because these acts are separately callable —
// seed/seed.go composes them directly and every fixture above does too — so a
// refusal that lived only at Mesh.Submit would be a refusal the split removes.
// The same argument checkAcknowledgement makes about the admission acts one flow
// over.
//
// Both arms of the loop are exercised, because the two are reached by different
// payments and a guard that read only the submitter would miss whichever
// direction the scheme puts the submitter on. And the payer's arm is what shows
// why this refusal is not enough on its own: the debtor leg is already posted
// when it fires, and getting that money back needs a pacs.002 the clearing house
// addresses through the roster — which is the row the non-member has none of.
// That is Mesh.Submit's guard's whole reason for existing.
func TestTheClearingHouseWillNotClearForANonMember(t *testing.T) {
	// build returns a network with one member and one founded-but-unadmitted
	// bank, the push request between them in the caller's direction, and an open
	// cycle for it to be refused out of.
	build := func(t *testing.T, foundedPays bool) (*testSystem, InitiatePaymentRequest) {
		t.Helper()
		ctx := context.Background()
		sys := testNetwork(t)
		member, err := storetest.Admit(ctx, sys.nets, "Member Bank", testBIC, euroOnly)
		assertNoError(t, err)
		founded, err := sys.bank(testBIC2).FoundBank(ctx, "Founded Bank", testBIC2, euroOnly)
		assertNoError(t, err)

		// The founded bank's customer is given an ARRANGED OVERDRAFT rather than
		// a deposit, because DepositTx refuses a founded bank — and an overdraft
		// is exactly how such a customer came to have spendable money in the
		// measurement this test descends from.
		foundedAcct, err := founded.Deposit.OpenAccount(ctx, founded.CustomerSubledger, "Nora", testAsset,
			founded.ProductID, 100000, deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-FOUND-0001"})
		assertNoError(t, err)
		memberAcct := openCustomer(t, ctx, member, "Alice", "SE89-MEMBR-0001")
		fundAccount(t, ctx, sys, member, memberAcct, 100000)
		openCycle(t, ctx, sys, SchemeSEPACT)

		req := InitiatePaymentRequest{
			Scheme:          SchemeSEPACT,
			Amount:          25000,
			Debtor:          PartyRef{Account: memberAcct.ID},
			Creditor:        PartyRef{Account: foundedAcct.ID},
			Description:     "Invoice 42",
			CreditorDetails: PartyDetails{Agent: founded.BIC, Name: "Nora"},
			DebtorDetails:   PartyDetails{Agent: member.BIC}}
		if foundedPays {
			req.Debtor = PartyRef{Account: foundedAcct.ID}
			req.Creditor = PartyRef{Account: memberAcct.ID}
			req.CreditorDetails = PartyDetails{Agent: member.BIC, Name: "Alice"}
			// And the FOUNDED bank submits it, which the reversed refs alone no
			// longer say: since Task 18d the debtor's agent selects the database
			// the submission is made in, so leaving it at the member's had that
			// bank submitting a payment drawn on an account it does not hold.
			req.DebtorDetails = PartyDetails{Agent: founded.BIC}
		}
		return sys, req
	}

	for _, tc := range []struct {
		name        string
		foundedPays bool
		role        string
	}{
		{"the payee's bank is not a member", false, "payee's bank"},
		{"the payer's bank is not a member", true, "payer's bank"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			sys, req := build(t, tc.foundedPays)

			// The two banks' own halves run and are not what refuses: the
			// submitting bank checks its own customer's account and the
			// receiving bank checks its own. Neither is asked about membership,
			// and neither can be — the roster is a third institution's row.
			p, err := sys.submit(ctx, req)
			assertNoError(t, err)
			assertNoError(t, sys.bank(receiverOf(sys, p)).AcceptInbound(ctx, p.ID, relayedFrom(p)))
			// The clearing house's own copy, which it has to be holding before
			// it can take a payment into a cycle — and which it writes from the
			// instruction it relayed, asking nothing about membership. The
			// roster check is the ACCEPTANCE's, below. See RecordRelayedTx.
			_, err = sys.RecordRelayed(ctx, p.ID, relayedFrom(p))
			assertNoError(t, err)

			_, err = sys.AcceptAtCSM(ctx, p.ID)
			if !errors.Is(err, ErrBankNotAdmitted) {
				t.Fatalf("AcceptAtCSM = %v, want ErrBankNotAdmitted", err)
			}
			if !strings.Contains(err.Error(), tc.role) {
				t.Errorf("the refusal says %q and does not name the %s", err, tc.role)
			}
			// RC01 on the wire, which is what makes csm.clear's rejection an
			// answer rather than a dead letter. reasonTable is what decides it.
			if got := ReasonFor(err); got != iso20022.StatusReasonBankIdentifierIncorrect {
				t.Errorf("ReasonFor = %q, want RC01", got)
			}

			// Refused before the transition, so the payment is where the two
			// banks left it and no cycle has it.
			after, err := sys.GetPayment(ctx, p.ID)
			assertNoError(t, err)
			if after.Status != Initiated {
				t.Errorf("the refused payment is %v, want Initiated", after.Status)
			}
			if after.CycleID != "" {
				t.Errorf("the refused payment is in cycle %q", after.CycleID)
			}
			cycles, err := sys.ListCycles(ctx)
			assertNoError(t, err)
			for _, c := range cycles {
				if len(c.PaymentIDs) != 0 {
					t.Errorf("cycle %s (%s) holds %d payments, want 0", c.ID, c.Scheme, len(c.PaymentIDs))
				}
			}
		})
	}
}

// TestTheClearingHouseWillNotClearInAnAssetAMemberWasNotAdmittedIn is the second
// arm of the same guard, and it is what gives RosterEntry.Assets a reader.
//
// The row says "the assets this member clears in" and until this existed nothing
// in production read it: every caller took the BIC off the entry and touched
// nothing else, which is the field-nothing-reads shape this sub-project has
// refused three times before — it is what deleted RosterEntry.Name in the same
// row.
//
// The state it refuses is the PARTLY-ADMITTED one, and it is reachable rather
// than hypothetical. One acmt.007 asks for one currency, so a two-asset
// admission is two conversations that commit separately; if the settlement agent
// answers for one and refuses the other, the bank is a Member with internal
// accounts in both assets and a settlement account in one. Its customers can
// hold accounts in the asset it was not admitted in, both banks' own halves pass
// — each checks the account against the SCHEME's asset, and both accounts are in
// it — and the payment would go into a cycle whose pacs.009 the clearing house
// cannot build, which is the same stranding the non-member case produces.
func TestTheClearingHouseWillNotClearInAnAssetAMemberWasNotAdmittedIn(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	sys.RegisterScheme(dollarPush{})
	bothAssets := []ledger.AssetCode{testAsset, "USD"}

	payer, err := storetest.Admit(ctx, sys.nets, "Member Bank", testBIC, bothAssets)
	assertNoError(t, err)

	// The half-admitted bank, built out of the acts rather than out of
	// storetest.Admit: founded in both assets, and the settlement agent asked for
	// one. Nothing is planted — this is the sequence the mesh runs, stopped where
	// a refused acmt.007 stops it.
	half, err := sys.bank(testBIC2).FoundBank(ctx, "Half Bank", testBIC2, bothAssets)
	assertNoError(t, err)
	const ref = "half-admitted"
	member, err := sys.cb().OpenSettlementAccount(ctx, AdmissionRequest{
		Name: half.Name, BIC: half.BIC, Asset: testAsset, Ref: ref,
	})
	assertNoError(t, err)
	ack := AdmissionAcknowledgement{BIC: half.BIC, Accounts: member.Accounts, Ref: ref}
	_, err = sys.AdmitMember(ctx, ack)
	assertNoError(t, err)
	half, err = sys.bank(half.BIC).RecordMembership(ctx, ack)
	assertNoError(t, err)
	if half.Status != BankMember {
		t.Fatalf("the half-admitted bank is %v; this test needs a Member", half.Status)
	}
	entry, err := sys.GetRosterEntryByBIC(ctx, half.BIC)
	assertNoError(t, err)
	if !slices.Equal(entry.Assets, []ledger.AssetCode{testAsset}) {
		t.Fatalf("the roster entry clears in %v; this test needs it admitted in one asset only", entry.Assets)
	}

	payerAcct, err := payer.Deposit.OpenAccount(ctx, payer.CustomerSubledger, "Alice", "USD",
		payer.ProductID, 0, deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-MEMBR-9001"})
	assertNoError(t, err)
	fundAccount(t, ctx, sys, payer, payerAcct, 100000)
	payeeAcct, err := half.Deposit.OpenAccount(ctx, half.CustomerSubledger, "Nora", "USD",
		half.ProductID, 0, deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: "SE89-HALFB-9001"})
	assertNoError(t, err)
	openCycle(t, ctx, sys, dollarPush{}.ID())

	p, err := sys.submit(ctx, InitiatePaymentRequest{
		Scheme:          dollarPush{}.ID(),
		Amount:          25000,
		Debtor:          PartyRef{Account: payerAcct.ID},
		Creditor:        PartyRef{Account: payeeAcct.ID},
		Description:     "a dollar this member does not clear",
		CreditorDetails: PartyDetails{Agent: half.BIC, Name: "Nora"},
		DebtorDetails:   PartyDetails{Agent: payer.BIC}})
	assertNoError(t, err)
	assertNoError(t, sys.bank(receiverOf(sys, p)).AcceptInbound(ctx, p.ID, relayedFrom(p)))
	// The clearing house's own copy first; see the same step in
	// TestTheClearingHouseWillNotClearForANonMember.
	_, err = sys.RecordRelayed(ctx, p.ID, relayedFrom(p))
	assertNoError(t, err)

	_, err = sys.AcceptAtCSM(ctx, p.ID)
	if !errors.Is(err, ErrBankNotAdmitted) {
		t.Fatalf("AcceptAtCSM = %v, want ErrBankNotAdmitted", err)
	}
	if !strings.Contains(err.Error(), "clears in [EUR], not USD") {
		t.Errorf("the refusal does not say which assets the member clears in: %v", err)
	}
	// The payer's bank IS admitted in both, so the refusal names the payee's and
	// not merely the first side read.
	if !strings.Contains(err.Error(), "payee's bank") {
		t.Errorf("the refusal names the wrong side: %v", err)
	}
}

// A pull posts nothing at submission. The creditor's bank has no access to the
// debtor's account and the money has not moved; the debtor's bank posts the
// leg when it accepts the collection.
func TestSubmitPostsNothingForAPullPayment(t *testing.T) {
	n, req := networkWithAMandate(t)
	p, err := n.submit(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitPayment: %v", err)
	}
	if p.DebtorLegTx != "" {
		t.Error("a direct debit posted a debtor leg at submission; the debtor's bank has not seen it yet")
	}
	if p.Status != Initiated {
		t.Errorf("status = %v, want Initiated", p.Status)
	}
}

// The submitting half must NOT check the far side. This is the assertion that
// makes the whole sub-project real: before it, InitiatePaymentTx read both
// banks' books in one transaction.
func TestSubmitDoesNotCheckTheCreditorAccount(t *testing.T) {
	n, req := networkWithTwoBanks(t)
	// A creditor account that does not exist. Before the split this was a
	// synchronous ErrAccountNotInParticipant; now it is the creditor bank's to
	// discover and answer with AC01.
	req.Creditor.Account = "no-such-account"

	p, err := n.submit(context.Background(), req)
	if err != nil {
		t.Fatalf("SubmitPayment refused a payment whose far side it cannot see: %v", err)
	}

	// And the check did not VANISH, it moved: the creditor's bank is the one
	// that discovers the account does not exist, which is the AC01 the mesh
	// answers with. Without this half the inventory's "nothing was dropped"
	// would be a claim no test could contradict — a creditorSideTx that
	// tolerated a missing account passed the whole suite before this line
	// existed.
	if err := n.bank(receiverOf(n, p)).AcceptInbound(context.Background(), p.ID, relayedFrom(p)); !errors.Is(err, ErrAccountNotInParticipant) {
		t.Fatalf("AcceptInbound on an account the creditor's bank does not hold = %v, want ErrAccountNotInParticipant", err)
	}
}

// TestSubmitTakesTheCounterpartyNameFromTheRequest pins the direction rule: the
// submitting bank fills in its OWN side from its own register and is TOLD both
// of the counterparty's — the name and, since Task 18a, the agent
// (TestSubmitRecordsTheCounterpartyAgentTheInstructionAsserts). It never reads
// the counterparty's register for either, and that half has not moved.
func TestSubmitTakesTheCounterpartyNameFromTheRequest(t *testing.T) {
	ctx := context.Background()
	n, req := networkWithTwoBanks(t)
	// The agent is whatever the fixture's creditor bank is; this test is about
	// the NAME, and the agent only has to be present and well formed.
	creditorBank, err := n.bank(req.CreditorDetails.Agent).GetBank(ctx, ParticipantID(req.CreditorDetails.Agent))
	assertNoError(t, err)
	req.CreditorDetails = PartyDetails{Agent: creditorBank.BIC, Name: "Whoever The Payer Typed"}
	// A WRONG name AND a wrong agent on the bank's own side. A merge that copied
	// req.DebtorDetails onto the payment unchanged would pass this test's name
	// check; only an overwrite from the register catches it.
	submitter := req.DebtorDetails.Agent
	req.DebtorDetails = PartyDetails{Agent: "WRONGDEFFXXX", Name: "Not Alice At All"}

	// Submitted through the bank that really submits, named before the plant.
	// n.submit cannot be used here for the first time: it routes on the agent
	// the request claims for its own side, and the whole point of this fixture
	// is that that value is a lie. Which is a fixture's problem and not the
	// domain's — a real submission arrives AT a bank, it does not name one.
	p, err := n.bank(submitter).SubmitPayment(ctx, req)
	if err != nil {
		t.Fatalf("SubmitPayment: %v", err)
	}
	if got := p.CreditorDetails.Name; got != "Whoever The Payer Typed" {
		t.Errorf("creditor name is %q, want the name the request carried", got)
	}
	// The debtor is this bank's own customer, so its name comes from its own
	// register and NOT from the request — a payer does not get to rename
	// themselves on an instruction. setupTwoBanks names the debtor's customer
	// "Alice", so this is not merely non-empty but exactly the register value.
	if p.DebtorDetails.Name != "Alice" {
		t.Errorf("debtor name is %q, want the submitting bank's own register value %q, not what the request carried", p.DebtorDetails.Name, "Alice")
	}
	debtorBank, err := n.bank(submitter).GetBank(ctx, ParticipantID(submitter))
	assertNoError(t, err)
	if p.DebtorDetails.Agent != debtorBank.BIC {
		t.Errorf("debtor agent is %q, want the submitting bank's own BIC %q", p.DebtorDetails.Agent, debtorBank.BIC)
	}
}

// TestSubmitRefusesAnUnnamedCounterparty pins that the instruction must carry
// what the message will need. Before this, a request that named no counterparty
// was accepted and the failure surfaced later, when the message was built, out of
// a bank's own register — which is exactly the read that was removed.
//
// It has had a subtest for the AGENT, then not, and now again, and the movement
// is the point rather than churn: the guard exists exactly while the field is
// the caller's to supply. Task 14 derived the agent from the counterparty's own
// bank row, so an instruction supplying none was ordinary and the disjunct could
// not fire for its own reason; Task 18a took the derivation away, because that
// row is the counterparty's and a bank holds only its own. Both are required
// again, and they are two sentinels because the remedies differ — a payer who
// left the name out types one, and a payer who left the BIC out has to go and
// find it.
func TestSubmitRefusesAnUnnamedCounterparty(t *testing.T) {
	for _, tc := range []struct {
		name    string
		details PartyDetails
		want    error
	}{
		{"no name", PartyDetails{}, ErrCounterpartyNotNamed},
		{"no name, and an agent supplied", PartyDetails{Agent: testBIC}, ErrCounterpartyNotNamed},
		{"a name and no agent", PartyDetails{Name: "Bob"}, ErrCounterpartyAgentNotNamed},
		{"a name and an agent that is not a BIC", PartyDetails{Name: "Bob", Agent: "not-a-bic"}, ErrCounterpartyAgentNotNamed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, req := networkWithTwoBanks(t)
			req.CreditorDetails = tc.details
			if _, err := n.submit(context.Background(), req); !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// TestSubmitRecordsTheCounterpartyAgentTheInstructionAsserts is the domain half
// of mesh/books_test.go's TestAWrongCounterpartyAgentIsRefusedByTheBankItNames,
// and it is Task 14's derivation test REVERSED.
//
// # What it asserted, and what took the derivation away
//
// It was TestSubmitDerivesTheCounterpartyAgentFromTheRoster: the instruction
// named a BIC that was not the counterparty's — the submitting bank's own, the
// worst case, because a message routed on it comes straight back to its sender
// — and SubmitPaymentTx discarded it and read the counterparty's own Bank row
// instead. (The name said "the roster" and the code read the bank row; the two
// differ for a founded-and-not-admitted bank, and the row was the right one.)
//
// That read is the counterparty's row, in the counterparty's own store from
// Task 18c, and there is no second source: the roster is keyed by the BIC being
// asked for and is the clearing house's, and this network has no IBAN-to-BIC
// directory service. So the payer asserts it, as SEPA's payers did before 2016
// and as a cross-border payer still does.
//
// # What is asserted here, and what is asserted a layer up
//
// This test's claim is narrow and complete: what the payer typed is what is
// STORED, on both sides, and the bank's own side is still overwritten from its
// own register. What happens to a payment carrying a WRONG agent is not a
// question this layer can answer — it is answered by the bank the message
// reaches, and mesh's test is where that lives.
//
// Both directions, because which side is the counterparty follows the scheme's
// direction and nothing else.
func TestSubmitRecordsTheCounterpartyAgentTheInstructionAsserts(t *testing.T) {
	t.Run("push: the creditor is the counterparty", func(t *testing.T) {
		ctx := context.Background()
		n, req := networkWithTwoBanks(t)
		debtorBank, err := n.bank(req.DebtorDetails.Agent).GetBank(ctx, ParticipantID(req.DebtorDetails.Agent))
		assertNoError(t, err)
		// The payer names the WRONG bank — their own. Nothing here corrects it.
		req.CreditorDetails = PartyDetails{Agent: debtorBank.BIC, Name: "Whoever The Payer Typed"}

		p, err := n.submit(ctx, req)
		assertNoError(t, err)
		if p.CreditorDetails.Agent != debtorBank.BIC {
			t.Errorf("creditor agent is %q, want the instruction's %q — an asserted agent is recorded, not replaced", p.CreditorDetails.Agent, debtorBank.BIC)
		}
		if p.CreditorDetails.Name != "Whoever The Payer Typed" {
			t.Errorf("creditor name is %q, want the name the instruction carried", p.CreditorDetails.Name)
		}
		// And the submitting bank's OWN side is still its register's, not the
		// request's. That asymmetry is the whole of what a bank is the authority
		// on, and it did not move when the counterparty's agent did.
		if p.DebtorDetails.Agent != debtorBank.BIC {
			t.Errorf("debtor agent is %q, want the submitting bank's own %q", p.DebtorDetails.Agent, debtorBank.BIC)
		}
	})

	t.Run("pull: the debtor is the counterparty", func(t *testing.T) {
		ctx := context.Background()
		n, req, _ := networkWithACollection(t, 100000)
		creditorBank, err := n.bank(req.CreditorDetails.Agent).GetBank(ctx, ParticipantID(req.CreditorDetails.Agent))
		assertNoError(t, err)
		// The collector names ITSELF as the payer's bank.
		req.DebtorDetails = PartyDetails{Agent: creditorBank.BIC, Name: "Whoever The Biller Typed"}

		p, err := n.submit(ctx, req)
		assertNoError(t, err)
		if p.DebtorDetails.Agent != creditorBank.BIC {
			t.Errorf("debtor agent is %q, want the instruction's %q — an asserted agent is recorded, not replaced", p.DebtorDetails.Agent, creditorBank.BIC)
		}
		if p.DebtorDetails.Name != "Whoever The Biller Typed" {
			t.Errorf("debtor name is %q, want the name the instruction carried", p.DebtorDetails.Name)
		}
		if p.CreditorDetails.Agent != creditorBank.BIC {
			t.Errorf("creditor agent is %q, want the submitting bank's own %q", p.CreditorDetails.Agent, creditorBank.BIC)
		}
	})
}

// TestSubmitDoesNotCheckWhetherTheCounterpartysBankExists records a check this
// act HAD and no longer makes, which is why it is a test rather than a deletion.
//
// It was TestSubmitRefusesACounterpartyAtNoSuchBank, and it was the other side
// of deriving the agent: there had to be a row to derive it FROM, so a
// counterparty at a participant nobody has founded was ErrParticipantNotFound at
// submission. The derivation is gone and the read with it, so nothing at
// submission looks the counterparty up at all.
//
// That is the split doing what it is for rather than a hole. The submitting bank
// has no business knowing which institutions exist — it is not the registry, and
// under Task 18c it holds no row but its own. What refuses an unreachable
// counterparty is the layer that actually knows: the mesh answers RC01 for a BIC
// no actor answers to (mesh.ErrUnknownBIC), and the clearing house refuses a
// payment whose banks it does not both route for (ErrBankNotAdmitted). Both of
// those are refusals by an institution that holds the relevant record, which the
// submitting bank never did.
//
// The payment is ACCEPTED here and the payer's leg posts, which is the cost and
// is the same cost every other misaddressed instruction has: the rejection comes
// back as a message and reverses it.
func TestSubmitDoesNotCheckWhetherTheCounterpartysBankExists(t *testing.T) {
	n, req := networkWithTwoBanks(t)
	// A well-formed address nobody has founded. It has to be well formed: the
	// FORMAT is still refused here, and by design — the mesh cannot route to a
	// string that is not a BIC, so that refusal belongs where the payer can
	// still fix it. See SubmitPaymentTx. What is not checked is whether anything
	// answers to it.
	req.CreditorDetails.Agent = "NOSUCHBKXXX"

	if _, err := n.submit(context.Background(), req); err != nil {
		t.Errorf("SubmitPayment = %v, want it accepted — a submitting bank does not check the counterparty's registry", err)
	}
}

// TestSubmitRefusesItsOwnPartyAtNoSuchBank pins checkPartyTx's PARTICIPANT arm,
// which had no test at all: TestSubmitDoesNotCheckTheCreditorAccount covers the
// account half of the same function, and nothing covered the half above it.
//
// It goes through the SUBMITTING bank's own side deliberately. The counterparty
// arm is TestSubmitRefusesACounterpartyAtNoSuchBank's and reaches a different
// function (the roster read in SubmitPaymentTx); this one reaches checkPartyTx,
// which is also what AcceptInboundTx runs on the receiving side.
func TestSubmitRefusesItsOwnPartyAtNoSuchBank(t *testing.T) {
	n, req := networkWithTwoBanks(t)

	// The instruction is submitted at an address that has a DATABASE and no bank
	// row in it — a network for an institution that was never founded — which is
	// the only shape "its own party is at no such bank" can take since Task 18d.
	// It used to be a field on the request: the submitting bank's own agent was
	// read back out and looked up, so planting a name there reached the lookup.
	// SubmitPaymentTx writes its own identity over that field now and never
	// reads it, so the party this act could fail to find is the ACTING one.
	if _, err := n.bank("NOSUCHBKXXX").SubmitPayment(context.Background(), req); !errors.Is(err, ErrParticipantNotFound) {
		t.Errorf("got %v, want ErrParticipantNotFound", err)
	}
}

// TestAcceptInboundDoesNotRewriteEitherPartysDetails pins a fix, not merely a
// property: debtorSideTx and creditorSideTx run from AcceptInboundTx as well as
// from SubmitPaymentTx, and in AcceptInboundTx the bank executing them is the
// RECEIVING bank for that direction — the creditor's on a push, the debtor's on
// a pull — not the submitting one. Filling PartyDetails from the register
// there would silently overwrite what the submitting bank already stored (and
// already sent, in the message SubmitAndInstruct built in the same unit of
// work) with the receiving bank's own record of the same account, which need
// not agree with what the payer typed. Checked on both directions, because the
// two arms fail differently if this regresses: a pull always posts its debtor
// leg in AcceptInboundTx, so its overwrite would be unconditional, while a
// push's is gated behind AcceptInboundTx's dirty check and only shows up when
// something else (an address back-fill) also changed.
//
// # The push subtest's precondition is asserted, not assumed
//
// That dirty check is `if p.Debtor == before.Debtor && p.Creditor ==
// before.Creditor && p.DebtorLegTx == before.DebtorLegTx { return nil }`
// (AcceptInboundTx). On a push nothing here changes DebtorLegTx, so the ONLY
// thing that can make this half write at all is the creditor address being
// back-filled — and that happens only because networkWithTwoBanks quotes no
// Creditor.Identifier. Give that fixture an identifier and this subtest keeps
// passing while pinning nothing: AcceptInboundTx would return before the write,
// so an overwrite of CreditorDetails would never be persisted for the assertion
// to catch.
//
// So the write is asserted. It is not a second subject — it is this subject's
// precondition, and a precondition that lives in another function's fixture is
// exactly the kind that rots silently.
func TestAcceptInboundDoesNotRewriteEitherPartysDetails(t *testing.T) {
	t.Run("push", func(t *testing.T) {
		ctx := context.Background()
		n, req := networkWithTwoBanks(t)
		creditorBank, err := n.bank(req.CreditorDetails.Agent).GetBank(ctx, ParticipantID(req.CreditorDetails.Agent))
		assertNoError(t, err)
		// Deliberately NOT "Bob" — the real name on the creditor's own
		// register (setupTwoBanks). If AcceptInboundTx's creditorSideTx (the
		// creditor's bank, here the RECEIVING bank) overwrote CreditorDetails
		// from its register, this would come back as "Bob".
		req.CreditorDetails = PartyDetails{Agent: creditorBank.BIC, Name: "Whoever The Payer Typed"}

		p, err := n.submit(ctx, req)
		assertNoError(t, err)
		if p.Creditor.Identifier != (deposit.Identifier{}) {
			t.Fatalf("the submitted payment already carries a creditor address (%+v), so AcceptInbound has nothing to change and this subtest can no longer fail; see the doc above", p.Creditor.Identifier)
		}
		assertNoError(t, n.bank(receiverOf(n, p)).AcceptInbound(ctx, p.ID, relayedFrom(p)))

		// The RECEIVING bank's copy, because that is the row AcceptInbound
		// wrote. The clearing house has not been told about this payment at all
		// — the fixture stops before RecordRelayed — and reading it there was a
		// "payment not found" that said nothing about the subject.
		after := mustGetPaymentAt(t, ctx, n.bank(receiverOf(n, p)), p.ID)
		if after.Creditor == p.Creditor {
			t.Fatalf("AcceptInbound wrote nothing back: the creditor is still %+v, so nothing this subtest asserts about CreditorDetails had to survive a PutPayment", after.Creditor)
		}
		if after.CreditorDetails != p.CreditorDetails {
			t.Errorf("creditor details after AcceptInbound = %+v, want unchanged from submission %+v",
				after.CreditorDetails, p.CreditorDetails)
		}
		if after.DebtorDetails != p.DebtorDetails {
			t.Errorf("debtor details after AcceptInbound = %+v, want unchanged from submission %+v",
				after.DebtorDetails, p.DebtorDetails)
		}
	})

	t.Run("pull", func(t *testing.T) {
		ctx := context.Background()
		n, req, _ := networkWithACollection(t, 100000)
		debtorBank, err := n.bank(req.DebtorDetails.Agent).GetBank(ctx, ParticipantID(req.DebtorDetails.Agent))
		assertNoError(t, err)
		// Deliberately NOT "Alice" — the real name on the debtor's own
		// register (networkWithACollection). If AcceptInboundTx's
		// debtorSideTx (the debtor's bank, here the RECEIVING bank) overwrote
		// DebtorDetails from its register, this would come back as "Alice".
		req.DebtorDetails = PartyDetails{Agent: debtorBank.BIC, Name: "Whoever The Payee Typed"}

		p, err := n.submit(ctx, req)
		assertNoError(t, err)
		assertNoError(t, n.bank(receiverOf(n, p)).AcceptInbound(ctx, p.ID, relayedFrom(p)))

		// The receiving bank's copy; see the push subtest.
		after := mustGetPaymentAt(t, ctx, n.bank(receiverOf(n, p)), p.ID)
		if after.DebtorDetails != p.DebtorDetails {
			t.Errorf("debtor details after AcceptInbound = %+v, want unchanged from submission %+v",
				after.DebtorDetails, p.DebtorDetails)
		}
		if after.CreditorDetails != p.CreditorDetails {
			t.Errorf("creditor details after AcceptInbound = %+v, want unchanged from submission %+v",
				after.CreditorDetails, p.CreditorDetails)
		}
	})
}

// failingUpdateTx is a Tx whose PARTY lookups fail with an error of the caller's
// choosing. Everything else is the real transaction, promoted by embedding.
//
// It is the Update-side counterpart of message_test.go's failingTx, and it has
// to be a second type rather than a bigger first one: failingStore decorates
// View alone, and checkPartyTx — the function these tests are about — runs
// inside an Update, on the receiving bank's money path. failingTx's own doc
// records that its GetDepositAccount override was deleted for exactly this
// reason, that nothing reached it through a View. Nothing has changed about
// that; what is added here is the other seam.
//
// The two failures are separate fields rather than one, because checkPartyTx
// makes two reads and the first one failing hides the second. A single error
// would leave the deposit-account arm untested while looking as though it were
// covered.
type failingUpdateTx struct {
	Tx
	participantErr error
	accountErr     error
}

func (t failingUpdateTx) GetBank(ctx context.Context, id ParticipantID) (Bank, error) {
	if t.participantErr != nil {
		return Bank{}, t.participantErr
	}
	return t.Tx.GetBank(ctx, id)
}

func (t failingUpdateTx) GetDepositAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) (deposit.Account, error) {
	if t.accountErr != nil {
		return deposit.Account{}, t.accountErr
	}
	return t.Tx.GetDepositAccount(ctx, book, id)
}

// failingUpdateStore wraps a real Store and hands every UPDATE a failingUpdateTx.
// See message_test.go's failingStore for why a synthetic error at the seam is
// the only way to provoke a store failure on demand.
type failingUpdateStore struct {
	Store
	participantErr error
	accountErr     error
}

func (s failingUpdateStore) Update(ctx context.Context, fn func(context.Context, Tx) error) error {
	return s.Store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return fn(ctx, failingUpdateTx{Tx: tx, participantErr: s.participantErr, accountErr: s.accountErr})
	})
}

// TestAcceptInboundDoesNotBlameTheSenderForAStoreFailure is checkPartyTx's half
// of the property addressedPartyTx has pinned all along, and it is the half that
// was missing while the hazard sat in the code.
//
// checkPartyTx used to collapse every error from either of its two reads into a
// domain sentinel — `if err != nil { return ErrAccountNotInParticipant }`, and
// the same shape one line up for the participant. That is on the MONEY path:
// AcceptInboundTx runs it through creditorSideTx/debtorSideTx, mesh/bank.go's
// answer hands whatever comes back to ReasonFor, and AC01 "incorrect account
// number" goes out in a pacs.002. So a dropped connection at the RECEIVING bank
// told the SENDING bank its customer's IBAN was wrong — and on a push the
// payer's debit is then reversed, so a fault that a retry would have cleared
// became a permanent rejection carrying a false reason.
//
// Both reads and both directions, which is four subtests for two lines of fix,
// and the count is the point rather than thoroughness for its own sake: the
// participant arm and the account arm are separate collapses that were both
// there, and creditorSideTx and debtorSideTx are separate call sites that reach
// the same function on opposite sides of the money.
//
// What is asserted is not merely "an error came back". The sentinel that must
// NOT come back is named, because an error that is technically non-nil and still
// maps to AC01 is the whole bug; and ReasonFor is called directly, because the
// wire code is the thing that reaches the counterparty.
func TestAcceptInboundDoesNotBlameTheSenderForAStoreFailure(t *testing.T) {
	dropped := errors.New("connection reset by peer")

	for _, tc := range []struct {
		name           string
		participantErr error
		accountErr     error
	}{
		{"the participant read fails", dropped, nil},
		{"the deposit-account read fails", nil, dropped},
	} {
		for _, dir := range []struct {
			name  string
			setup func(t *testing.T) (*testSystem, InitiatePaymentRequest)
		}{
			{"push", func(t *testing.T) (*testSystem, InitiatePaymentRequest) {
				n, req := networkWithTwoBanks(t)
				return n, req
			}},
			{"pull", func(t *testing.T) (*testSystem, InitiatePaymentRequest) {
				n, req, _ := networkWithACollection(t, 100000)
				req.DebtorDetails = PartyDetails{Agent: testBIC, Name: "Alice"}
				return n, req
			}},
		} {
			t.Run(tc.name+", "+dir.name, func(t *testing.T) {
				ctx := context.Background()
				n, req := dir.setup(t)
				p, err := n.submit(ctx, req)
				assertNoError(t, err)

				// The same store, decorated only from here on: the payment had
				// to be submitted successfully for there to be one to answer.
				broken := NewNetwork(failingUpdateStore{
					Store:          n.Store(),
					participantErr: tc.participantErr,
					accountErr:     tc.accountErr,
				}, func() time.Time { return fixedTime }, AsBank(ParticipantID(receiverOf(n, p))))

				err = broken.AcceptInbound(ctx, p.ID, relayedFrom(p))
				if !errors.Is(err, dropped) {
					t.Fatalf("AcceptInbound over a broken store = %v, want the store's own error", err)
				}
				if errors.Is(err, ErrAccountNotInParticipant) {
					t.Error("a store failure surfaced as ErrAccountNotInParticipant, which reaches the sender as AC01 \"incorrect account number\" — the number is not the problem")
				}
				if errors.Is(err, ErrParticipantNotFound) {
					t.Error("a store failure surfaced as ErrParticipantNotFound, which reaches the sender as RC01 \"bank identifier incorrect\" — the bank is not the problem")
				}
				if got := ReasonFor(err); got != iso20022.StatusReasonNotSpecifiedAgentGenerated {
					t.Errorf("ReasonFor(a store failure) = %q, want MS03: the only true thing to say is that this agent could not carry the instruction out", got)
				}
			})
		}
	}
}

// A payment that is no longer Initiated must not be revived by an answer that
// was in flight when it stopped being one.
//
// This is why AcceptInboundTx takes an ID and loads: given the payment by
// value it wrote the caller's copy back, and this sequence — which Task 9 makes
// routine and which RejectAtCSMTx already permits, since it accepts an
// Initiated payment — returned a rejected payment to Initiated with its
// DebtorLegTx still naming the transaction the rejection had reversed. The
// clearing house would then have accepted and settled it, paying the creditor
// out of a suspense position that no longer existed.
func TestAcceptInboundRefusesAPaymentThatIsNoLongerInitiated(t *testing.T) {
	ctx := context.Background()
	n, p := networkWithASubmittedPayment(t)

	rejected, err := reject(ctx, n, p.ID, iso20022.StatusReasonDuplication, "cancelled by the payer")
	assertNoError(t, err)
	assertEqual(t, "status after rejection", rejected.Status, Rejected)

	// p is the copy submission returned: Initiated, with a debtor leg that has
	// since been reversed. Exactly what a bank's handler would still be
	// holding.
	if err := n.bank(receiverOf(n, p)).AcceptInbound(ctx, p.ID, relayedFrom(p)); !errors.Is(err, ErrInvalidStateTransition) {
		t.Fatalf("AcceptInbound on a rejected payment = %v, want ErrInvalidStateTransition", err)
	}

	stored, err := n.GetPayment(ctx, p.ID)
	assertNoError(t, err)
	assertEqual(t, "status after the late answer", stored.Status, Rejected)
}

func TestAcceptInboundRefusesAClosedCreditorAccount(t *testing.T) {
	n, p := networkWithASubmittedPayment(t)
	closeCreditorAccount(t, n, p)

	err := n.bank(receiverOf(n, p)).AcceptInbound(context.Background(), p.ID, relayedFrom(p))
	if !errors.Is(err, deposit.ErrAccountClosed) {
		t.Fatalf("AcceptInbound = %v, want the closed-account error", err)
	}
}

// The creditor's bank holds the mandate, so it validates it — synchronously,
// at submission. The debtor's bank validates funds, asynchronously, on
// receipt. That is who holds what in SEPA, and it is the seam that survives
// the eventual store split.
//
// The funds half here is also the pull mirror of
// TestSubmitDoesNotCheckTheCreditorAccount, as far as one can be written. The
// account half of that mirror is not constructible: pointing the request's
// debtor at an account that does not exist fails the mandate's SameParty
// comparison with ErrMandateMismatch — a comparison of two stored refs, not a
// look in the debtor's register — before the absence of any debtor-book read
// could be observed. The property holds by construction: creditorSideTx never
// calls checkPartyTx for the debtor.
func TestMandateIsCheckedAtSubmissionAndFundsOnReceipt(t *testing.T) {
	n, req := networkWithARevokedMandate(t)
	if _, err := n.submit(context.Background(), req); !errors.Is(err, ErrMandateRevoked) {
		t.Fatalf("SubmitPayment with a revoked mandate = %v, want ErrMandateRevoked", err)
	}

	n2, req2 := networkWithAnUnfundedDebtor(t)
	p, err := n2.submit(context.Background(), req2)
	if err != nil {
		t.Fatalf("SubmitPayment refused for lack of funds it cannot see: %v", err)
	}
	if err := n2.bank(receiverOf(n2, p)).AcceptInbound(context.Background(), p.ID, relayedFrom(p)); !errors.Is(err, deposit.ErrInsufficientAvailable) {
		t.Fatalf("AcceptInbound = %v, want insufficient funds", err)
	}
}

// Rejection is two units of work in two actors: the CSM transitions the
// payment and drops it from its cycle, the debtor's bank reverses its own leg.
//
// This is the FIRST operation in this repository that can half-happen, and the
// test says so rather than letting it be discovered: after RejectAtCSMTx alone,
// the payment is Rejected and the customer's money is still in suspense.
func TestRejectionIsTwoHalvesAndTheFirstHalfLeavesMoneyInSuspense(t *testing.T) {
	n, p := networkWithASubmittedPayment(t)
	before := suspenseBalance(t, n, p.DebtorDetails.Agent)

	rejected, err := n.RejectAtCSM(context.Background(), p.ID, "AC01", "no such account")
	if err != nil {
		t.Fatalf("RejectAtCSM: %v", err)
	}
	if rejected.Status != Rejected || rejected.RejectCode != "AC01" {
		t.Fatalf("got %v/%q, want Rejected/AC01", rejected.Status, rejected.RejectCode)
	}
	if got := suspenseBalance(t, n, p.DebtorDetails.Agent); got != before {
		t.Error("the CSM's half moved money; only the debtor's bank may touch its own book")
	}

	// The PAYER'S BANK's own copy is what is handed to its own half: the leg id
	// this reverses is on that copy and on no other, because the posting was
	// that bank's. `rejected` is the clearing house's row, which has no leg
	// columns at all, and passing it made the reversal a silent no-op — the
	// same "nothing to give back" answer a collection with no posted leg gets.
	payer := n.bank(p.DebtorDetails.Agent)
	if err := payer.ReverseDebtorLeg(context.Background(), mustGetPaymentAt(t, context.Background(), payer, p.ID), "no such account"); err != nil {
		t.Fatalf("ReverseDebtorLeg: %v", err)
	}
	if got := suspenseBalance(t, n, p.DebtorDetails.Agent); got != before-p.Amount {
		t.Errorf("suspense = %d after the reversal, want %d", got, before-p.Amount)
	}
}

// Dropping the payment from its cycle is the clearing house's own act, and it
// is the half of a rejection with consequences beyond the payment row: a
// rejected payment left in an open cycle would be closed and settled with it,
// paying a creditor for an instruction the network refused.
func TestRejectAtCSMDropsThePaymentFromItsCycle(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	cyc, err := sys.OpenCycle(ctx, SchemeSEPACT)
	assertNoError(t, err)

	p, err := initiate(ctx, sys, InitiatePaymentRequest{
		Scheme: SchemeSEPACT, Amount: 5000,
		Debtor: PartyRef{Account: alice}, Creditor: PartyRef{Account: bob},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
		DebtorDetails:   PartyDetails{Agent: a.BIC}})
	assertNoError(t, err)
	before, err := sys.GetCycle(ctx, cyc.ID)
	assertNoError(t, err)
	assertEqual(t, "payments in the cycle after acceptance", len(before.PaymentIDs), 1)

	_, err = sys.RejectAtCSM(ctx, p.ID, iso20022.StatusReasonDuplication, "duplicate instruction")
	assertNoError(t, err)

	after, err := sys.GetCycle(ctx, cyc.ID)
	assertNoError(t, err)
	if slices.Contains(after.PaymentIDs, p.ID) {
		t.Errorf("the rejected payment is still in cycle %s; closing it would settle a refused instruction", cyc.ID)
	}
}

// A failed reversal takes the PAYER'S BANK's whole half down with it, and takes
// nothing else down, because there is nothing else in that unit of work.
//
// # It used to claim more, and the claim was retired rather than the test
//
// It was called TestAFailedReversalRollsBackTheWholeRejection and its subject
// was the composition sites that stand in for the mesh: they ran the clearing
// house's transition and the payer's bank's reversal in ONE unit of work, so a
// reversal that failed took the transition with it and no caller could read a
// Rejected payment whose money was still in suspense.
//
// That unit of work spanned two institutions and Task 18c is exactly its
// removal. A transaction is ONE DATABASE's; the clearing house decides on its
// own row, in its own store, and each bank records the decision on its own. So
// the half-happened outcome the old test kept off the synchronous routes is now
// on all of them, and it is what the mesh always had: the clearing house's
// decision stands, the payer's bank has not acted, and the retry is a message
// redelivered rather than a rollback.
//
// What is left to measure is stronger than nothing and is the part that stayed
// atomic: RejectAtBankTx transitions THIS bank's copy and reverses THIS bank's
// leg in one unit of work, so a bank that cannot give the money back does not
// record the rejection either. A bank whose row said Rejected while the payer's
// money sat in its own suspense would be the one inconsistency no message can
// repair.
func TestAFailedRejectionAtABankLeavesThatBanksCopyUntouched(t *testing.T) {
	ctx := context.Background()
	n, p := networkWithASubmittedPayment(t)
	payer := n.bank(p.DebtorDetails.Agent)

	// The payer's bank has already given the money back, so its half of the
	// rejection will refuse. Anything that fails inside it would do; this is the
	// one a retried pacs.002 actually produces.
	assertNoError(t, payer.ReverseDebtorLeg(ctx, mustGetPaymentAt(t, ctx, payer, p.ID), "reversed already"))

	_, err := reject(ctx, n, p.ID, iso20022.StatusReasonDuplication, "cancelled by the payer")
	if !errors.Is(err, ledger.ErrTransactionAlreadyReversed) {
		t.Fatalf("reject = %v, want ErrTransactionAlreadyReversed", err)
	}

	// The payer's bank recorded nothing: not the status, not the reason.
	atPayer := mustGetPaymentAt(t, ctx, payer, p.ID)
	assertEqual(t, "status at the payer's bank", atPayer.Status, Initiated)
	assertEqual(t, "reject reason at the payer's bank", atPayer.RejectReason, "")

	// And the clearing house's decision stands, which is the half-happened
	// outcome named above. It is not a defect to be fixed here — the pacs.002
	// is redelivered until the bank can act on it — and it is stated so that a
	// future unit of work quietly spanning both institutions would fail this.
	assertEqual(t, "status at the clearing house",
		mustGetPaymentAt(t, ctx, n.Network, p.ID).Status, Rejected)
}

// A collection the clearing house refuses before the payer's bank has answered
// it took nothing from the payer, so the debtor bank's half has nothing to give
// back. The pacs.002 still reaches that bank — it does not know whether it had
// answered — so the half has to be a clean no-op rather than an error.
func TestReverseDebtorLegIsANoOpWhenNoLegWasPosted(t *testing.T) {
	ctx := context.Background()
	n, req := networkWithAMandate(t)
	p, err := n.submit(ctx, req)
	assertNoError(t, err)
	assertEqual(t, "debtor leg after submitting a collection", p.DebtorLegTx, "")
	// The clearing house's own copy, which it must be holding before it can
	// refuse one. See networkWithASubmittedPayment.
	_, err = n.RecordRelayed(ctx, p.ID, relayedFrom(p))
	assertNoError(t, err)

	_, err = n.RejectAtCSM(ctx, p.ID, iso20022.StatusReasonNoMandate, "no usable mandate")
	assertNoError(t, err)

	// Run at the PAYER'S BANK, on the payment as the submitting bank recorded
	// it. That bank has no row of its own — the clearing house refused the
	// collection before relaying it, so this bank was never told — and it does
	// not need one: ReverseDebtorLegTx reads ID and DebtorLegTx off the value it
	// is handed and looks nothing up, which is exactly what makes it safe to run
	// on a payment that never reached this institution. See its doc on taking
	// the payment by value.
	payer := n.bank(p.DebtorDetails.Agent)
	bank := mustGetBank(t, ctx, n, ParticipantID(p.DebtorDetails.Agent))
	before := customerBalance(t, bank, p.Debtor.Account)

	if err := payer.ReverseDebtorLeg(ctx, p, "no usable mandate"); err != nil {
		t.Fatalf("ReverseDebtorLeg on a payment with no posted leg = %v, want a no-op", err)
	}
	assertEqual(t, "payer's balance", customerBalance(t, bank, p.Debtor.Account), before)
}

// Reversing the same leg twice would pay the payer twice, so the ledger refuses
// it rather than absorbing it: a redelivered pacs.002 fails loudly and becomes
// a dead letter instead of quietly crediting the payer again.
func TestReverseDebtorLegRefusesToRunTwice(t *testing.T) {
	ctx := context.Background()
	n, p := networkWithASubmittedPayment(t)

	_, err := n.RejectAtCSM(ctx, p.ID, iso20022.StatusReasonIncorrectAccountNumber, "no such account")
	assertNoError(t, err)
	// The payer's bank's own copy, which is where the leg is. See
	// TestRejectionIsTwoHalvesAndTheFirstHalfLeavesMoneyInSuspense.
	payer := n.bank(p.DebtorDetails.Agent)
	atPayer := mustGetPaymentAt(t, ctx, payer, p.ID)
	assertNoError(t, payer.ReverseDebtorLeg(ctx, atPayer, "no such account"))

	err = payer.ReverseDebtorLeg(ctx, atPayer, "no such account")
	if !errors.Is(err, ledger.ErrTransactionAlreadyReversed) {
		t.Fatalf("second ReverseDebtorLeg = %v, want ErrTransactionAlreadyReversed", err)
	}
	assertEqual(t, "suspense after the refused second reversal",
		suspenseBalance(t, n, p.DebtorDetails.Agent), 0)
}

// The reason text is stored by one half and only described by the other, and
// neither may put an unprintable byte where it will be persisted.
func TestRejectionRefusesAnUnsafeReasonInBothHalves(t *testing.T) {
	ctx := context.Background()
	n, p := networkWithASubmittedPayment(t)
	const unsafe = "cancelled\x00by the payer"

	// The CSM's half validates it itself: RejectReason is written onto the
	// payment and copied into the audit event.
	if _, err := n.RejectAtCSM(ctx, p.ID, iso20022.StatusReasonDuplication, unsafe); !errors.Is(err, ledger.ErrInvalidText) {
		t.Fatalf("RejectAtCSM with an unprintable reason = %v, want ErrInvalidText", err)
	}
	assertEqual(t, "status after the refused rejection",
		mustGetPaymentAt(t, ctx, n.Network, p.ID).Status, Initiated)

	// The debtor bank's half does not repeat the check, because the ledger
	// makes it: the reason travels in the reversal's description.
	_, err := n.RejectAtCSM(ctx, p.ID, iso20022.StatusReasonDuplication, "cancelled by the payer")
	assertNoError(t, err)
	payer := n.bank(p.DebtorDetails.Agent)
	if err := payer.ReverseDebtorLeg(ctx, mustGetPaymentAt(t, ctx, payer, p.ID), unsafe); !errors.Is(err, ledger.ErrInvalidText) {
		t.Fatalf("ReverseDebtorLeg with an unprintable reason = %v, want ErrInvalidText", err)
	}
	assertEqual(t, "suspense after the refused reversal",
		suspenseBalance(t, n, p.DebtorDetails.Agent), p.Amount)
}

// A queue redelivers, so the debtor's bank sees the same pacs.003 twice while
// the payment is still Initiated. The second delivery must be a no-op.
//
// Before DebtorLegTx was used as the witness, it reached postDebtorLegTx again
// and came back with the ledger's ErrDuplicateIdempotencyKey — no double debit,
// because the key is the payment's own id, but an error with no entry in
// reasonTable, so ReasonFor turns it into MS03 and the bank rejects on the wire
// a collection it has in fact accepted.
func TestAcceptInboundIgnoresARedeliveredCollection(t *testing.T) {
	ctx := context.Background()
	n, req := networkWithAMandate(t)
	p, err := n.submit(ctx, req)
	assertNoError(t, err)
	assertNoError(t, n.bank(receiverOf(n, p)).AcceptInbound(ctx, p.ID, relayedFrom(p)))

	// The RECEIVING bank's copy: on a pull that is the payer's bank, and its
	// leg is the row this test is about.
	answered := mustGetPaymentAt(t, ctx, n.bank(receiverOf(n, p)), p.ID)
	bank := mustGetBank(t, ctx, n, ParticipantID(p.DebtorDetails.Agent))
	balance := customerBalance(t, bank, p.Debtor.Account)

	if err := n.bank(receiverOf(n, p)).AcceptInbound(ctx, p.ID, relayedFrom(p)); err != nil {
		t.Fatalf("redelivered collection = %v, want a no-op — the mesh would answer MS03 for a collection this bank accepted", err)
	}
	assertEqual(t, "payer's balance after the redelivery", customerBalance(t, bank, p.Debtor.Account), balance)

	again := mustGetPaymentAt(t, ctx, n.bank(receiverOf(n, p)), p.ID)
	assertEqual(t, "debtor leg after the redelivery", again.DebtorLegTx, answered.DebtorLegTx)
	assertEqual(t, "suspense after the redelivery",
		suspenseBalance(t, n, p.DebtorDetails.Agent), p.Amount)
}

// ---------------------------------------------------------------------------
// The return, as three acts
// ---------------------------------------------------------------------------

// TestSettlingAReturnReadsNoPayment is the crossing this task closes.
//
// The settlement agent is handed an INSTRUCTION and moves reserves. If it can
// still do that by reading a payment row, nothing has been split: under
// sub-project 8 the central bank holds no payment rows at all, so a return it
// can only execute by looking one up is a return it cannot execute.
//
// The instruction below is built by hand, out of the fields a pacs.004's
// OrgnlTxRef actually carries — the two agents' BICs, the amount and its
// currency — and not out of the settled payment. What the payment is used for
// here is the assertion, not the input: it must come out of this untouched.
func TestSettlingAReturnReadsNoPayment(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	var pay Payment
	runCycle(t, sys, SchemeSEPACT, func() {
		var err error
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 30000,
			Debtor:          PartyRef{Account: alice},
			Creditor:        PartyRef{Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
			DebtorDetails:   PartyDetails{Agent: a.BIC}})
		assertNoError(t, err)
	})
	assertEqual(t, "bank A reserve at the central bank after the cut-off", reserveAt(t, sys, a), 70000)
	assertEqual(t, "bank B reserve at the central bank after the cut-off", reserveAt(t, sys, b), 30000)

	in := ReturnInstruction{
		PaymentID:     pay.ID,
		EndToEndID:    pay.EndToEndID,
		DebtorAgent:   a.BIC,
		CreditorAgent: b.BIC,
		Amount:        30000,
		Asset:         testAsset,
		Reason:        "AC04: account closed",
	}
	statements, err := sys.cb().SettleReturn(ctx, in)
	assertNoError(t, err)

	// The reserves are where they were before the cut-off moved them.
	assertEqual(t, "bank A reserve after the return settled", reserveAt(t, sys, a), 100000)
	assertEqual(t, "bank B reserve after the return settled", reserveAt(t, sys, b), 0)

	// Two statements, one per member, in the order the postings are made: the
	// creditor's bank pays the reserves back and the debtor's bank receives
	// them. Fixed rather than incidental, for SettleCycleTx's reason — a
	// caller sends these as messages, and a set whose order came out of map
	// iteration would put a different pair on the wire each run.
	if len(statements) != 2 {
		t.Fatalf("a return produced %d statements, want 2 — one per member", len(statements))
	}
	assertEqual(t, "the first statement's member", statements[0].Agent, b.BIC)
	assertEqual(t, "the creditor bank's movement", statements[0].Movement, -30000)
	assertEqual(t, "the creditor bank's closing balance", statements[0].ClosingBalance, 0)
	assertEqual(t, "the creditor bank's agent", statements[0].Agent, b.BIC)
	assertEqual(t, "the second statement's member", statements[1].Agent, a.BIC)
	assertEqual(t, "the debtor bank's movement", statements[1].Movement, 30000)
	assertEqual(t, "the debtor bank's closing balance", statements[1].ClosingBalance, 100000)
	for _, st := range statements {
		assertEqual(t, "the entry reference", st.Reference, string(pay.ID))
		assertEqual(t, "the statement reference", st.StatementRef, string(pay.ID)+":rtr")
		assertEqual(t, "the asset", st.Asset, testAsset)
	}

	// And the payment row is exactly as the cut-off left it: the settlement
	// agent moved reserves and wrote nothing about the payment, because it has
	// no payment to write about.
	after, err := sys.GetPayment(ctx, pay.ID)
	assertNoError(t, err)
	assertEqual(t, "the payment's status after its reserves were reversed", after.Status, Settled)
	assertEqual(t, "the clawback leg the settlement agent must not have posted", after.ReturnClawbackTx, "")
	assertEqual(t, "the refund leg the settlement agent must not have posted", after.ReturnRefundTx, "")

	// A redelivered instruction is refused rather than settled twice. The
	// refusal comes from the ledger's idempotency key — there is no row here
	// recording that this return was settled, because the settlement agent
	// holds no payment rows to record it on.
	_, err = sys.cb().SettleReturn(ctx, in)
	assertError(t, err, ErrReturnAlreadySettled)
	assertEqual(t, "bank A reserve after the redelivery", reserveAt(t, sys, a), 100000)
	assertEqual(t, "bank B reserve after the redelivery", reserveAt(t, sys, b), 0)
}

// TestASettlementAgentRefusesAReturnItsCreditorBankCannotCover is the one
// decision the settlement agent makes on this path, and it is the same one
// SettleCycleTx makes at a cut-off: it will not take a member's reserve below
// zero to move somebody else's money.
//
// It has to be checked here explicitly, because nothing else will. A member's
// settlement account in the central bank's book is a ledger.Liability — the
// central bank owes the member its reserve — and Book.checkSufficientBalance
// guards only Asset and Expense accounts.
func TestASettlementAgentRefusesAReturnItsCreditorBankCannotCover(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, _, _, _ := setupTwoBanks(t, sys)

	// A third member, holding no reserves at all: it has taken part in no
	// cut-off. A bank that had paid its reserves out since the payment settled
	// is the same position arrived at the long way round.
	c, err := storetest.Admit(ctx, sys.nets, "Bank C", "BANKFRPPXXX", euroOnly)
	assertNoError(t, err)

	_, err = sys.cb().SettleReturn(ctx, ReturnInstruction{
		PaymentID:     "pay_absent",
		DebtorAgent:   a.BIC,
		CreditorAgent: c.BIC,
		Amount:        1000,
		Asset:         testAsset,
		Reason:        "AC04: account closed",
	})
	assertError(t, err, ledger.ErrInsufficientBalance)

	// Nothing moved, on either side of the transaction it refused to post.
	assertEqual(t, "bank A reserve after the refusal", reserveAt(t, sys, a), 100000)
	assertEqual(t, "bank C reserve after the refusal", reserveAt(t, sys, c), 0)
}

// TestASettlementAgentRefusesAReturnThatNamesNoPayment is the same guard
// ReadReturn makes on the message, made a second time where the money is.
//
// The key on the reserve reversal is "<payment>:return-settle", and it is the
// ONLY record this actor keeps that it settled a return — it holds no payment
// rows. So an empty payment id is not a cosmetic defect in an instruction: it
// moves reserves between two real banks under ":return-settle", and every later
// nameless return then comes back ErrReturnAlreadySettled having settled
// nothing. The first one costs money and the rest are silently wrong.
//
// mesh's settlement agent cannot reach this, because payment.ReadReturn refuses
// the message before an instruction exists. That is exactly why the check is
// ALSO here: ReadReturn is a reader, and a ReturnInstruction built any other way
// — a future caller, a test, another transport — would reopen the hole. It is
// ReverseReturnLegTx's own argument, which this package already made once: the
// domain is where a check on the money belongs, and not in the handler that
// happens to be the only caller today.
//
// There is no sentinel, which is deliberate: this is a judgement about the
// sender's INSTRUCTION rather than a statement about this system's state, so
// ReasonFor's fallback to MS03 is the right answer on the wire and not a hazard
// — the discrimination reasonTable's empty codes exist to make. It is the shape
// cycleOf already uses for a settlement instruction that names no cycle. What
// the test therefore asserts is that SOMETHING refused and that it was not the
// funding check.
func TestASettlementAgentRefusesAReturnThatNamesNoPayment(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, _, _ := setupTwoBanks(t, sys)

	// Bank A is the CREDITOR's bank here and holds the reserves, so the funding
	// check this actor makes cannot be what refuses the instruction. Written that
	// way round deliberately: with the short bank on that side the test passes
	// against a settlement agent that has no id guard at all, which is the shape
	// of a test that proves nothing.
	_, err := sys.cb().SettleReturn(ctx, ReturnInstruction{
		PaymentID:     "",
		DebtorAgent:   b.BIC,
		CreditorAgent: a.BIC,
		Amount:        1000,
		Asset:         testAsset,
		Reason:        "AC04: account closed",
	})
	if err == nil {
		t.Fatal("settled a return naming no payment; the reserve reversal would be keyed by nothing")
	}
	if errors.Is(err, ledger.ErrInsufficientBalance) {
		t.Fatalf("refused for want of reserves (%v); this fixture funds the creditor's bank so that the id is what refuses", err)
	}
	// Nothing moved, which is the half that matters: an instruction refused
	// after the posting would already have cost the creditor's bank the money.
	assertEqual(t, "bank A reserve after the refusal", reserveAt(t, sys, a), 100000)
	assertEqual(t, "bank B reserve after the refusal", reserveAt(t, sys, b), 0)
	// And no transaction stands under the key an empty id would have produced.
	_, err = sys.cbBook(t).GetTransactionByIdempotencyKey(ctx, ":return-settle")
	assertError(t, err, ledger.ErrTransactionNotFound)
}

// reserveAt is a bank's reserve balance as the CENTRAL BANK holds it, which is
// the only side of the mirror a settlement agent's act moves.
func reserveAt(t *testing.T, sys *testSystem, p *Bank) ledger.Amount {
	t.Helper()
	bal, err := sys.cb().ReserveBalance(context.Background(), p.BIC, testAsset)
	assertNoError(t, err)
	return bal
}

// spendTheCredit empties a payee's account the way a payee actually empties
// one: an outgoing credit transfer, cleared and settled through its own
// cut-off.
//
// It is a whole extra cycle for what a test wants in one line, and there is no
// shorter honest way — the deposit register exposes no withdrawal, and a
// direct posting against the customer's GL account would leave this bank's
// reserve mirror claiming money that is no longer in any account. What the
// tests below turn on is precisely that the money is GONE while the account
// itself is untouched, so a fixture that faked the balance would be testing
// its own arithmetic.
func spendTheCredit(t *testing.T, sys *testSystem, from *Bank, fromAcct deposit.AccountID,
	to *Bank, toAcct deposit.AccountID, amount ledger.Amount,
) {
	t.Helper()
	ctx := context.Background()
	runCycle(t, sys, SchemeSEPACT, func() {
		_, err := initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: amount,
			Debtor:          PartyRef{Account: fromAcct},
			Creditor:        PartyRef{Account: toAcct},
			CreditorDetails: PartyDetails{Agent: to.BIC, Name: "the payee"},
			DebtorDetails:   PartyDetails{Agent: from.BIC}})
		assertNoError(t, err)
	})
}

// TestAPayeeWhoSpentTheMoneyStopsTheReturnBeforeItIsSent is the push half of
// the return's one rule, and the reason the returning bank POSTS its leg before
// it sends rather than checking and then sending.
//
// On a credit transfer the returning bank is the payee's bank, which is also
// the creditor's bank, so the leg it holds is the clawback — and no bank
// force-takes money from a customer who has already spent it. A beneficiary
// bank that cannot fund the clawback refuses there and then, to its own caller,
// and no pacs.004 is ever composed. That is the shape MD01 already has here:
// refused at the bank that holds the account, as an error and not as a message.
//
// A check that is not a posting could be outrun by the customer between the
// check and the answer. Posting is what makes the refusal bind, which is why
// the assertion below is that NOTHING moved rather than merely that an error
// came back.
func TestAPayeeWhoSpentTheMoneyStopsTheReturnBeforeItIsSent(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	var pay Payment
	runCycle(t, sys, SchemeSEPACT, func() {
		var err error
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 30000,
			Debtor:          PartyRef{Account: alice},
			Creditor:        PartyRef{Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
			DebtorDetails:   PartyDetails{Agent: a.BIC}})
		assertNoError(t, err)
	})
	assertEqual(t, "bob after the credit transfer arrived", customerBalance(t, b, bob), 30000)

	spendTheCredit(t, sys, b, bob, a, alice, 30000)
	assertEqual(t, "bob after spending it", customerBalance(t, b, bob), 0)

	// Bob's bank is the returner here, so this is the call that would have
	// produced the pacs.004.
	_, err := sys.bank(b.BIC).PostReturnLeg(ctx, pay.ID, "AC04: account closed")
	assertError(t, err, deposit.ErrInsufficientAvailable)

	// And nothing at all was written. Bob is not overdrawn, the clearing
	// suspense that would have held the clawback is flat, and no receivable was
	// opened — the receivable is the PULL side's answer and must not leak into
	// this one.
	assertEqual(t, "bob after the refused return", customerBalance(t, b, bob), 0)
	assertEqual(t, "bank B suspense after the refused return",
		bookBalance(t, b.Ledger, accountsOf(t, b).Suspense), 0)
	assertEqual(t, "bank B returns receivable after the refused return",
		bookBalance(t, b.Ledger, accountsOf(t, b).ReturnsReceivable), 0)

	after, err := sys.GetPayment(ctx, pay.ID)
	assertNoError(t, err)
	assertEqual(t, "the payment's status after the refused return", after.Status, Settled)
	assertEqual(t, "the clawback leg after the refused return", after.ReturnClawbackTx, "")
}

// TestAPullRefundIsHonouredEvenWhenTheBillerCannotFundIt is the other half of
// the same rule, and it comes out the opposite way for a reason that is
// structural rather than stipulated.
//
// On a direct debit the returning bank is the PAYER's bank, so the leg the
// creditor's bank holds is the clawback and it hears about the return only
// after the reserves have already moved. The payer's eight-week refund right is
// unconditional, so that bank honours the refund whether or not the biller can
// pay and carries the shortfall itself. That is why creditor banks vet their
// creditors, and why Returns Receivable is an Asset: the bank is owed this, by
// somebody it has identified perfectly well.
//
// The biller here has BOTH spent the money and closed the account, and both
// halves are load-bearing. Spending it alone is the overdraft case below — the
// bank simply lets the account go negative, which is what an overdraft is.
// Closing it is what leaves the bank with nowhere to put the debit: a credit or
// debit into a closed account strands, since Close requires a zero balance,
// Closed is terminal and no later posting can reach it.
func TestAPullRefundIsHonouredEvenWhenTheBillerCannotFundIt(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)
	pay := settledCollection(t, sys, a, alice, b, biller, 25000)

	spendTheCredit(t, sys, b, biller, a, alice, 25000)
	assertNoError(t, b.Deposit.Close(ctx, biller))

	got, err := sys.bank(b.BIC).PostReturnLeg(ctx, pay.ID, "MD06: refund requested by the payer")
	assertNoError(t, err)

	// The refund is funded out of a claim on the biller, and the money is in
	// this bank's clearing suspense on its way back to the payer's bank.
	assertEqual(t, "bank B returns receivable", bookBalance(t, b.Ledger, accountsOf(t, b).ReturnsReceivable), 25000)
	assertEqual(t, "bank B suspense after the forced clawback",
		bookBalance(t, b.Ledger, accountsOf(t, b).Suspense), 25000)
	// The closed account is not touched, which is the whole point of the
	// receivable: a debit posted into it would strand exactly as a credit does.
	assertEqual(t, "the biller's closed account", customerBalance(t, b, biller), 0)

	// One leg of two, so the payment has not reached Returned: the payer's bank
	// has not been paid yet.
	assertEqual(t, "the payment's status after the first leg", got.Status, Settled)
	if got.ReturnClawbackTx == "" {
		t.Fatal("the clawback posted and left no transaction id on the payment; " +
			"the other leg has no way to know it is the second")
	}
	assertEqual(t, "the refund leg, which is the other bank's", got.ReturnRefundTx, "")
}

// TestAForcedClawbackOverdrawsAnOpenBillerRatherThanBookingAReceivable is the
// case next door, and it is here to hold Returns Receivable to the one thing it
// is for.
//
// A biller who has spent the money but still HAS an account is simply overdrawn
// by it. The ledger will not stop that — a deposit is a ledger.Liability and
// checkSufficientBalance guards only Asset and Expense accounts — and it should
// not: an overdrawn biller is a debt the bank collects from a customer it is
// still in a relationship with, which is what an overdraft already is. Booking
// a receivable here instead would take the debt off the account it belongs to
// and leave the customer's own balance saying the money was never taken back.
func TestAForcedClawbackOverdrawsAnOpenBillerRatherThanBookingAReceivable(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)
	pay := settledCollection(t, sys, a, alice, b, biller, 25000)

	spendTheCredit(t, sys, b, biller, a, alice, 25000)

	_, err := sys.bank(b.BIC).PostReturnLeg(ctx, pay.ID, "MD06: refund requested by the payer")
	assertNoError(t, err)

	assertEqual(t, "the biller's overdrawn account", customerBalance(t, b, biller), -25000)
	assertEqual(t, "bank B returns receivable", bookBalance(t, b.Ledger, accountsOf(t, b).ReturnsReceivable), 0)
	assertEqual(t, "bank B suspense after the forced clawback",
		bookBalance(t, b.Ledger, accountsOf(t, b).Suspense), 25000)
}

// TestAPullRefundIsHonouredWhenOneBankIsBothParties holds the return's one rule
// where the two banks collapse into one.
//
// # Why this is tested here and not through the mesh
//
// A payment with the same institution at both ends never reaches a clearing
// house — Mesh.Submit refuses one, because nothing leaves the bank and there is
// no interbank obligation to clear. So this arrangement is not buildable through
// the transport, and the guard that would be measured there is a different
// guard. This one is the DOMAIN's, and the domain is where it has to be right:
// PostReturnLegTx decides refusability from the payment it is handed, and a rule
// that holds only because a caller elsewhere never builds the counter-example is
// a rule nobody is keeping. It is the argument ReadReturn's id guard and
// SettleReturnTx's own copy of it already make.
//
// # What it measures
//
// Refusability is a property of the LEG. The clawback may be refused when the
// scheme is a push AND this bank is the returner; the refund never may. Asked as
// "is this bank the returner" alone — which is what it used to ask — one bank
// that is both parties answers yes to BOTH legs, so the clawback becomes
// refusable on a PULL and the returning bank turns down its own customer's
// unconditional eight-week refund. The bank would be telling the payer it cannot
// have its money back because the biller, also its customer, has spent it.
//
// The biller here has spent the money, so the clawback is exactly the posting a
// refusal would stop. It goes through, the biller goes overdrawn, and the payer
// is repaid out of the same bank's clearing suspense.
func TestAPullRefundIsHonouredWhenOneBankIsBothParties(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	// A second customer of ALICE's bank. Both parties to the collection are then
	// bank A's, which is the whole fixture.
	biller := openCustomer(t, ctx, a, "Biller", "SE89-BANKA-0002").ID
	pay := settledCollection(t, sys, a, alice, a, biller, 25000)

	// Spent OUT of the bank, to a customer of the other one, so the biller is
	// empty and bank A really is short: a transfer between two of its own
	// accounts would have left the money where the clawback could still reach
	// it.
	spendTheCredit(t, sys, a, biller, b, bob, 25000)
	assertEqual(t, "the biller after spending it", customerBalance(t, a, biller), 0)

	// The clawback, which on a pull is forced. Bank A is the returner AND the
	// creditor's bank here, so this is the call the old rule refused.
	got, err := sys.bank(a.BIC).PostReturnLeg(ctx, pay.ID, "MD06: refund requested by the payer")
	assertNoError(t, err)
	assertEqual(t, "the biller after the forced clawback", customerBalance(t, a, biller), -25000)

	// And then the refund, which is the same bank's other leg. Two calls, not
	// one, because a bank posts one leg at a time; the second is what carries the
	// payment to Returned.
	got, err = sys.bank(a.BIC).PostReturnLeg(ctx, pay.ID, "MD06: refund requested by the payer")
	assertNoError(t, err)
	assertEqual(t, "the payment after both legs", got.Status, Returned)
	assertEqual(t, "the payer after the refund", customerBalance(t, a, alice), 100000)
	// Flat, because both legs of an on-us return pass through one suspense and
	// cancel. Nothing was ever owed to another bank.
	assertEqual(t, "bank A suspense after both legs",
		bookBalance(t, a.Ledger, accountsOf(t, a).Suspense), 0)
}

// settledCollection runs one direct debit all the way to Settled: a mandate,
// a cut-off, and the biller's own bank paying the biller out of its suspense.
func settledCollection(t *testing.T, sys *testSystem, debtorBank *Bank, debtorAcct deposit.AccountID,
	creditorBank *Bank, creditorAcct deposit.AccountID, amount ledger.Amount,
) Payment {
	t.Helper()
	ctx := context.Background()
	debtor := PartyRef{Account: debtorAcct}
	creditor := PartyRef{Account: creditorAcct}
	m, err := sys.bank(creditorBank.BIC).CreateMandate(ctx, debtorBank.BIC, debtor, creditor, 0)
	assertNoError(t, err)

	var pay Payment
	runCycle(t, sys, SchemeSEPADD, func() {
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: amount, MandateID: m.ID,
			Debtor:          debtor,
			Creditor:        creditor,
			DebtorDetails:   PartyDetails{Agent: debtorBank.BIC, Name: "the payer"},
			CreditorDetails: PartyDetails{Agent: creditorBank.BIC}})
		assertNoError(t, err)
	})
	assertEqual(t, "the biller after the collection", customerBalance(t, creditorBank, creditorAcct), amount)
	return pay
}

// returnTheWholeWay plays every institution in a return, in the order the
// messages travel: the returning bank posts its own leg and sends, the
// settlement agent reverses the reserves and states each member's account, both
// members book their reserve mirrors, and the other bank posts the leg it was
// sent. It returns the payment as that last leg left it.
//
// It is this package's stand-in for the mesh, exactly as bookTheAdvices and
// payTheCreditors are for a cut-off, and for the same reason: there are no
// actors here, so a test that wants the end state has to play them. Which bank
// goes first is ReturnerOf's answer and not the caller's — that is the rule the
// whole flow turns on.
func returnTheWholeWay(t *testing.T, sys *testSystem, p Payment, reason string) Payment {
	t.Helper()
	ctx := context.Background()
	scheme, ok := sys.Scheme(p.Scheme)
	if !ok {
		t.Fatalf("payment %s is under unregistered scheme %s", p.ID, p.Scheme)
	}
	returner := ReturnerOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent)
	other := p.DebtorDetails.Agent
	if other == returner {
		other = p.CreditorDetails.Agent
	}

	_, err := sys.bank(returner).PostReturnLeg(ctx, p.ID, reason)
	assertNoError(t, err)

	debtorBank := mustGetBank(t, ctx, sys, ParticipantID(p.DebtorDetails.Agent))
	creditorBank := mustGetBank(t, ctx, sys, ParticipantID(p.CreditorDetails.Agent))
	statements, err := sys.cb().SettleReturn(ctx, ReturnInstruction{
		PaymentID:     p.ID,
		EndToEndID:    p.EndToEndID,
		DebtorAgent:   debtorBank.BIC,
		CreditorAgent: creditorBank.BIC,
		Amount:        p.Amount,
		Asset:         scheme.Asset(),
		Reason:        reason,
	})
	assertNoError(t, err)
	bookTheAdvices(t, sys, statements)

	out, err := sys.bank(other).PostReturnLeg(ctx, p.ID, reason)
	assertNoError(t, err)
	return out
}

// TestARefundIntoAClosedPayersAccountGoesToUnclaimedBalances closes a gap that
// was a recorded RULING for two tasks rather than a defect nobody had noticed.
//
// The old ReturnPaymentTx's doc measured it: a payer who empties and closes their
// account after a payment settles, whose payment is then returned, ended with
// the money in an account whose status is Closed — the withdrawal check answers
// "closed", the credit check answers "closed", and closing again is an invalid
// transition. It could not be fixed by REFUSING, which was the only option the
// note could weigh: refusing the return leaves the disputed money with the
// payee permanently, so one stranding is traded for another.
//
// What was missing was not an account — the payer's bank has had unclaimed
// balances since Task 15 — but a unit of work small enough to divert in. While
// a return was three postings in three institutions committed together, the
// payer's bank had no act of its own to make the decision in. It has one now,
// and this is the same fix PostCreditorLegTx made on the creditor side.
//
// The payment still reaches Returned, because it did: the reserves came back
// and the payer's BANK has been repaid. Whether the CUSTOMER has been repaid is
// between the bank and its customer, which is what an unclaimed balance says.
func TestARefundIntoAClosedPayersAccountGoesToUnclaimedBalances(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	var pay Payment
	runCycle(t, sys, SchemeSEPACT, func() {
		var err error
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 30000,
			Debtor:          PartyRef{Account: alice},
			Creditor:        PartyRef{Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
			DebtorDetails:   PartyDetails{Agent: a.BIC}})
		assertNoError(t, err)
	})

	// Alice moves the rest of her money out and closes the account — after the
	// cut-off, so nothing about the settled payment can be re-derived from her
	// account's status.
	spendTheCredit(t, sys, a, alice, b, bob, 70000)
	assertNoError(t, a.Deposit.Close(ctx, alice))

	returned := returnTheWholeWay(t, sys, pay, "AC04: account closed")
	assertEqual(t, "the payment's status", returned.Status, Returned)

	// The refund is at the payer's bank, and it is in the one account there
	// that can hold money for somebody the bank cannot pay.
	assertEqual(t, "bank A unclaimed balances", bookBalance(t, a.Ledger, accountsOf(t, a).Unclaimed), 30000)
	assertEqual(t, "alice's closed account", customerBalance(t, a, alice), 0)
	// Bob was clawed back, so this is a return that completed rather than one
	// that stopped at the diversion.
	assertEqual(t, "bob after the clawback", customerBalance(t, b, bob), 70000)

	// Both suspenses are flat again, which is the reconciliation on this path:
	// each bank has booked both of its halves — its reserve mirror from the
	// statement and its customer leg from the return.
	assertEqual(t, "bank A suspense", bookBalance(t, a.Ledger, accountsOf(t, a).Suspense), 0)
	assertEqual(t, "bank B suspense", bookBalance(t, b.Ledger, accountsOf(t, b).Suspense), 0)
	assertReserveMirror(t, sys, a)
	assertReserveMirror(t, sys, b)
}

// TestAReturnStoreFailureDoesNotRouteTheRefundToUnclaimedBalances is the guard
// on the guard above, and it is the same hazard
// TestASettlementStoreFailureDoesNotRouteMoneyToUnclaimedBalances pins one leg
// over.
//
// Only a CLOSED account may send a refund somewhere other than the payer. A
// guard written as "divert unless the read succeeded" would put a customer's
// money in their bank's unclaimed balances because a database connection
// dropped, mark the payment Returned and commit — and the only record of why
// would be a description string. The account was resolved once already, when
// this bank posted the payer's own debit; a read that cannot answer now is a
// fault in the reading and not news about the account, so the leg fails and can
// simply be posted again.
//
// The clawback subtest is the same discrimination on the other side, where the
// choice is between a customer's account and a receivable rather than between a
// customer's account and unclaimed balances. It is a pull, because that is the
// direction whose clawback is forced: on a push a store failure and a refusal
// come out the same way — nothing is posted — so there is nothing to tell
// apart.
func TestAReturnStoreFailureDoesNotRouteTheRefundToUnclaimedBalances(t *testing.T) {
	dropped := errors.New("connection reset by peer")

	t.Run("the refund", func(t *testing.T) {
		ctx := context.Background()
		sys := testNetwork(t)
		a, b, alice, bob := setupTwoBanks(t, sys)

		var pay Payment
		runCycle(t, sys, SchemeSEPACT, func() {
			var err error
			pay, err = initiate(ctx, sys, InitiatePaymentRequest{
				Scheme: SchemeSEPACT, Amount: 30000,
				Debtor:          PartyRef{Account: alice},
				Creditor:        PartyRef{Account: bob},
				CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
				DebtorDetails:   PartyDetails{Agent: a.BIC}})
			assertNoError(t, err)
		})
		// The clawback first, over the sound store: this is the payer's bank's
		// own unit of work, and the failure under test is its alone.
		_, err := sys.bank(b.BIC).PostReturnLeg(ctx, pay.ID, "AC04: account closed")
		assertNoError(t, err)

		broken := NewNetwork(failingUpdateStore{Store: sys.Store(), accountErr: dropped},
			func() time.Time { return fixedTime }, AsBank(a.ID))
		if _, err := broken.PostReturnLeg(ctx, pay.ID, "AC04: account closed"); err == nil {
			t.Error("a refund over a store that could not read the payer's account succeeded; " +
				"a read that cannot answer is not permission to put the money somewhere else")
		}

		assertEqual(t, "bank A unclaimed balances after a failed read",
			bookBalance(t, a.Ledger, accountsOf(t, a).Unclaimed), 0)
		assertEqual(t, "alice after a failed read", customerBalance(t, a, alice), 70000)
		after, err := sys.GetPayment(ctx, pay.ID)
		assertNoError(t, err)
		assertEqual(t, "the payment's status after a failed refund", after.Status, Settled)
		assertEqual(t, "the refund leg after a failed refund", after.ReturnRefundTx, "")
	})

	t.Run("the forced clawback", func(t *testing.T) {
		ctx := context.Background()
		sys := testNetwork(t)
		a, b, alice, biller := setupTwoBanks(t, sys)
		pay := settledCollection(t, sys, a, alice, b, biller, 25000)

		broken := NewNetwork(failingUpdateStore{Store: sys.Store(), accountErr: dropped},
			func() time.Time { return fixedTime }, AsBank(b.ID))
		if _, err := broken.PostReturnLeg(ctx, pay.ID, "MD06: refund requested by the payer"); err == nil {
			t.Error("a forced clawback over a store that could not read the biller's account succeeded; " +
				"forcing is a decision about a customer's account and cannot be made without reading it")
		}

		assertEqual(t, "bank B returns receivable after a failed read",
			bookBalance(t, b.Ledger, accountsOf(t, b).ReturnsReceivable), 0)
		assertEqual(t, "the biller after a failed read", customerBalance(t, b, biller), 25000)
		assertEqual(t, "bank B suspense after a failed read",
			bookBalance(t, b.Ledger, accountsOf(t, b).Suspense), 0)
	})
}

// TestARejectedReturnUnwindsTheReturningBanksOwnLeg is the other end of the
// ordering that makes a refusal bind.
//
// The returning bank posts before it sends, so when the settlement agent
// answers RJCT that posting is already standing against a return that will not
// happen. Nothing else in the flow can undo it: the reserves never moved, and
// the other bank never heard about the return at all.
//
// It is a ledger reversal rather than a hand-written counter-posting, so the
// original stays in the book marked Reversed and the two are linked — a
// customer's statement shows a debit and its reversal, which is what happened,
// rather than two unexplained entries.
func TestARejectedReturnUnwindsTheReturningBanksOwnLeg(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	var pay Payment
	runCycle(t, sys, SchemeSEPACT, func() {
		var err error
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 30000,
			Debtor:          PartyRef{Account: alice},
			Creditor:        PartyRef{Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
			DebtorDetails:   PartyDetails{Agent: a.BIC}})
		assertNoError(t, err)
	})

	posted, err := sys.bank(b.BIC).PostReturnLeg(ctx, pay.ID, "AC04: account closed")
	assertNoError(t, err)
	assertEqual(t, "bob after the clawback", customerBalance(t, b, bob), 0)
	assertEqual(t, "bank B suspense holding the clawback",
		bookBalance(t, b.Ledger, accountsOf(t, b).Suspense), 30000)

	assertNoError(t, sys.bank(b.BIC).ReverseReturnLeg(ctx, pay.ID, "AM04: the settlement agent could not cover it"))
	assertEqual(t, "bob after the unwind", customerBalance(t, b, bob), 30000)
	assertEqual(t, "bank B suspense after the unwind",
		bookBalance(t, b.Ledger, accountsOf(t, b).Suspense), 0)

	// The original is in the book and marked, rather than deleted.
	original, err := b.Ledger.GetTransaction(ctx, posted.ReturnClawbackTx)
	assertNoError(t, err)
	assertEqual(t, "the clawback's status after the unwind", original.Status, ledger.Reversed)

	// Unwinding twice does not pay Bob twice.
	assertError(t, sys.bank(b.BIC).ReverseReturnLeg(ctx, pay.ID, "AM04: told again"), ledger.ErrTransactionAlreadyReversed)
	assertEqual(t, "bob after the second unwind", customerBalance(t, b, bob), 30000)

	// A bank that is neither side of the payment has no leg to unwind.
	c, err := storetest.Admit(ctx, sys.nets, "Bank C", "BANKFRPPXXX", euroOnly)
	assertNoError(t, err)
	assertError(t, sys.bank(c.BIC).ReverseReturnLeg(ctx, pay.ID, "AM04: not mine"), ErrNotAPartyToThisReturn)
}

// TestACompletedReturnCannotBeUnwound is the guard on the unwind, and the case
// it refuses is the one that would cost real money.
//
// ReverseReturnLegTx exists for an RJCT: the returning bank posted before it
// sent, the network refused, and that posting has to come back. Which is a
// statement about a return that STOPPED. A return that finished has two customer
// legs standing in two banks' books, and undoing either one of them alone leaves
// the other — reverse the clawback on a completed push and the payee is made
// whole while the payer keeps the refund, with the amount out of the returning
// bank's own suspense and the payment row still saying Returned.
//
// Nothing calls it on a completed return, and the caller it does have is exactly
// why the check belongs here: mesh's bank.receiveReturnStatus acts on a MESSAGE
// rather than on a status it checked, so a late or duplicated RJCT is the shape
// that would otherwise arrive. Next to the money, and not in the handler.
//
// ErrInvalidStateTransition rather than a new sentinel: it is the same statement
// this package makes everywhere else about an operation a payment's status does
// not permit, and payment's reasonTable already classifies it as a defect in
// this system rather than a judgement to answer a counterparty with.
func TestACompletedReturnCannotBeUnwound(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	var pay Payment
	runCycle(t, sys, SchemeSEPACT, func() {
		var err error
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPACT, Amount: 30000,
			Debtor:          PartyRef{Account: alice},
			Creditor:        PartyRef{Account: bob},
			CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
			DebtorDetails:   PartyDetails{Agent: a.BIC}})
		assertNoError(t, err)
	})
	returned := returnTheWholeWay(t, sys, pay, "AC04: account closed")
	assertEqual(t, "the payment's status", returned.Status, Returned)
	assertEqual(t, "alice refunded", customerBalance(t, a, alice), 100000)
	assertEqual(t, "bob clawed back", customerBalance(t, b, bob), 0)

	// Both banks are refused, because either one alone would break the pair.
	for _, by := range []iso20022.BIC{b.BIC, a.BIC} {
		assertError(t, sys.bank(by).ReverseReturnLeg(ctx, pay.ID, "AM04: told too late"), ErrInvalidStateTransition)
	}

	// And no half of it happened: the money is where the completed return left
	// it, and neither bank is carrying the amount in its suspense.
	assertEqual(t, "alice after the refused unwinds", customerBalance(t, a, alice), 100000)
	assertEqual(t, "bob after the refused unwinds", customerBalance(t, b, bob), 0)
	assertEqual(t, "bank A suspense after the refused unwinds",
		bookBalance(t, a.Ledger, accountsOf(t, a).Suspense), 0)
	assertEqual(t, "bank B suspense after the refused unwinds",
		bookBalance(t, b.Ledger, accountsOf(t, b).Suspense), 0)
}

// receiverOf is the bank that ANSWERS a payment: the payee's on a push, the
// payer's on a pull.
//
// It exists because AcceptInboundTx takes the acting participant since Task
// 18a — the receiving bank resolves the counterparty in its OWN register, so it
// has to be told which bank it is. The direction decides which side that is, and
// nothing else does; a helper that guessed "the creditor" would work for every
// credit transfer and post the wrong bank's debit on every collection.
// receiverOf is the bank that ANSWERS a payment: the payee's on a push, the
// payer's on a pull. It reads the agents, because a PartyRef stopped naming a
// bank at Task 18 — see PartyRef — and it returns a BIC, which is what
// testSystem.bank now takes.
// relayedFrom is the instruction the other two institutions would have been sent
// about a payment this fixture has just submitted.
//
// It exists because AcceptInboundTx and RecordRelayedTx take a REQUEST since
// Task 18d: each institution writes its own row from the message, so an id alone
// says nothing to an institution that has never seen the payment. In the mesh
// this is CreditTransferRequest or DirectDebitRequest reading a real pacs.008;
// here it is the payment turned back into what the message would have said,
// which is the same set of values by a shorter route.
//
// The account ids go through unchanged, and that is the fixture being a fixture.
// A real receiving bank resolves its own side from the ADDRESS and never learns
// the other's internal key; this process holds every register, and the mesh's
// own suites are where that distinction is measured.
func relayedFrom(p Payment) InitiatePaymentRequest {
	return InitiatePaymentRequest{
		Scheme:          p.Scheme,
		Debtor:          p.Debtor,
		Creditor:        p.Creditor,
		Amount:          p.Amount,
		MandateID:       p.MandateID,
		EndToEndID:      p.EndToEndID,
		Description:     p.Description,
		Metadata:        p.Metadata,
		DebtorDetails:   p.DebtorDetails,
		CreditorDetails: p.CreditorDetails,
	}
}

func receiverOf(n *testSystem, p Payment) iso20022.BIC {
	scheme, ok := n.Scheme(p.Scheme)
	if ok && scheme.Direction() == Pull {
		return p.DebtorDetails.Agent
	}
	return p.CreditorDetails.Agent
}

// submitterOfReq is the bank that SUBMITS one, which is the other side.
//
// It exists because the fixtures below build a request and then have to pick the
// network to submit it through, and submission became a bank's own act: since
// Task 18 checkPartyTx resolves the submitting side's account in THIS network's
// own register, so a request submitted through the clearing house's network —
// which is what the initiate helper used to do — resolves nothing.
//
// # A request that names nobody is a fixture bug, and it used to be silent
//
// This fell back to testBIC, on the argument that a caller only has to name the
// COUNTERPARTY's agent because SubmitPaymentTx fills its own side in from the
// network it runs on. That argument is about the DOMAIN and it still holds. What
// it does not cover is the fixture's own problem: which of several databases to
// open. testBIC was a serviceable answer while every fixture bank had it and
// wrong the moment two banks had two addresses — a pull naming no collector was
// submitted at the PAYER's bank, which holds no mandate, and four SDD fixtures
// failed "mandate not found" about a mandate that exists.
//
// So it fails instead, by name. It cannot take a *testing.T — every caller is
// one expression inside a request literal — and a panic here would take the
// whole test binary down with it, hiding every test declared after it. Returning
// the sentinel means the store refuses to open a database for it, which is one
// test's error with this string in it.
func submitterOfReq(n *testSystem, req InitiatePaymentRequest) iso20022.BIC {
	scheme, ok := n.Scheme(req.Scheme)
	side := req.DebtorDetails.Agent
	if ok && scheme.Direction() == Pull {
		side = req.CreditorDetails.Agent
	}
	if side == "" {
		return "THIS REQUEST NAMES NO SUBMITTING AGENT"
	}
	return side
}
