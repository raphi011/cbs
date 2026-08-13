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
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	. "github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/payment/flow"
	"github.com/raphi011/cbs/product"
	"github.com/raphi011/cbs/store/storetest"
	"github.com/raphi011/cbs/store/testenv"
)

// fixedTime is the instant the test clock returns, matching the ledger package's.
var fixedTime = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

// testAsset is the asset these tests operate in: SEPA is a euro scheme, so a test
// bank that clears SEPA is a euro bank.
const testAsset ledger.AssetCode = "EUR"

var euroOnly = []ledger.AssetCode{testAsset}

// testBIC is a structurally valid ISO 9362 BIC, the default across these tests.
const testBIC iso20022.BIC = "BANKDEFFXXX"

// testAllocation is a bank code these tests hand to an act expecting one to have
// arrived on a message. It is NOT what the settlement agent would allocate; where
// the allocation is under test, the fixture goes through storetest.Admit.
var testAllocation = iban.Issuer{Country: storetest.FixtureCountry, BankCode: "99999999"}

// testBIC2 is a second, distinct BIC for fixtures where two banks' BICs must be
// tellable apart, so a test planting one where the other belongs catches a
// derivation that silently passes the wrong value through.
const testBIC2 iso20022.BIC = "BANKGB2LXXX"

// testBICs are distinct addresses for fixtures building more than two banks and
// settling between them. Three banks on one address would be one member with one
// reserve, and a settlement transaction's four entries would name one account.
var testBICs = []iso20022.BIC{testBIC, testBIC2, "BANKFRPPXXX", "BANKESMMXXX"}

// testSystem is what a fixture holds: the clearing house's view of the network,
// embedded, plus the factory the acts belonging to ONE institution go through.
// payment.Network has an identity, so one *Network cannot play every
// institution; bank() and cb() are visible at every call site that uses them.
type testSystem struct {
	*ClearingHouseNetwork
	nets *Networks
	// stores is the set the networks are minted over, for assertions that reach a
	// database directly rather than through an institution.
	stores Stores
}

// bank is one member's own view, over that member's own database. Keyed by BIC,
// because a bank's ParticipantID is its BIC (see AsBank).
func (s *testSystem) bank(bic iso20022.BIC) *BankNetwork {
	net, err := s.nets.Bank(context.Background(), ParticipantID(bic))
	if err != nil {
		panic("payment_test: opening " + string(bic) + "'s store: " + err.Error())
	}
	return net
}

// cb is the settlement agent's view, and the only one holding the central
// bank's book of accounts.
func (s *testSystem) cb() *CentralBankNetwork { return s.nets.CentralBank() }

// testCentralBankBIC is the address this fixture's settlement agent is reached
// at. It has no store row — a settlement agent is not a member of the scheme it
// settles — so it is configured rather than discovered.
const testCentralBankBIC iso20022.BIC = "CBANDEFFXXX"

// settleCycle instructs the settlement agent to discharge one cut-off, with the
// legs the clearing house would have put on the pacs.009.
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

// cbBook is the central bank's own book. The error cannot fire — s.cb() is the
// settlement agent's by construction — so the assertion is what says so.
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
	return &testSystem{ClearingHouseNetwork: nets.ClearingHouse(), nets: nets, stores: stores}
}

// accountsOf returns a participant's internal accounts in the test asset,
// failing the test if it does not operate in it.
func accountsOf(t *testing.T, p *Bank) BankAccounts {
	t.Helper()
	accts, err := p.AccountsFor(testAsset)
	assertNoError(t, err)
	return accts
}

// initiate is flow.Initiate, which owns the order: there is deliberately no
// method on any institution that plays two of them, and tests about the split
// call the halves directly.
func initiate(ctx context.Context, sys *testSystem, req InitiatePaymentRequest) (Payment, error) {
	return flow.Initiate(ctx, sys.nets, req)
}

// reject is flow.Reject, for initiate's reason.
func reject(ctx context.Context, sys *testSystem, id PaymentID, code iso20022.StatusReason, reason string) (Payment, error) {
	return flow.Reject(ctx, sys.nets, id, code, reason)
}

// setupTwoBanks creates two banks, opens a customer account at each (Alice at
// A, Bob at B) and funds Alice with 100000. Both accounts carry an IBAN because
// every scheme here is SEPA.
func setupTwoBanks(t *testing.T, sys *testSystem) (a, b *Bank, alice, bob deposit.AccountID) {
	t.Helper()
	ctx := context.Background()

	a, err := storetest.Admit(ctx, sys.nets, "Bank A", testBIC, euroOnly)
	assertNoError(t, err)
	b, err = storetest.Admit(ctx, sys.nets, "Bank B", testBIC2, euroOnly)
	assertNoError(t, err)

	aliceAcct := openCustomer(t, ctx, a, "Alice")
	bobAcct := openCustomer(t, ctx, b, "Bob")

	fundAccount(t, ctx, sys, a, aliceAcct, 100000)
	return a, b, aliceAcct.ID, bobAcct.ID
}

// addParticipant admits a euro-only bank at the given address through
// storetest.Admit, which runs the four acts in order with no transport between
// them. The address is an argument because a bank's address IS its DATABASE.
func addParticipant(t *testing.T, ctx context.Context, sys *testSystem, name string, bic iso20022.BIC) *Bank {
	t.Helper()
	p, err := storetest.Admit(ctx, sys.nets, name, bic, euroOnly)
	assertNoError(t, err)
	return p
}

// admitWithoutTheRoster builds the bank a deployment's flow does not produce:
// one the settlement agent has answered, so it can address its customers'
// accounts, and the clearing house has never admitted.
func admitWithoutTheRoster(t *testing.T, ctx context.Context, sys *testSystem, name string, bic iso20022.BIC) *Bank {
	t.Helper()
	applicant := sys.bank(bic)
	_, err := applicant.FoundBank(ctx, name, bic, storetest.FixtureCountry, euroOnly)
	assertNoError(t, err)
	ref := "unrostered-" + string(bic)
	member, issuer, err := sys.cb().OpenSettlementAccount(ctx, AdmissionRequest{
		Name: name, BIC: bic, Country: storetest.FixtureCountry, Asset: testAsset, Ref: ref,
	})
	assertNoError(t, err)
	bank, err := applicant.RecordMembership(ctx, AdmissionAcknowledgement{
		BIC: bic, Issuer: issuer, Accounts: member.Accounts, Ref: ref,
	})
	assertNoError(t, err)
	return bank
}

// auroraBIC and verdeBIC are the Aurora/Verde fixtures' two addresses, distinct
// because those fixtures' subject is one bank NOT seeing the other's register —
// a claim about two institutions, unmakeable on one address.
const (
	auroraBIC iso20022.BIC = "AURODEFFXXX"
	verdeBIC  iso20022.BIC = "VERDITMMXXX"
)

// openCustomer opens a customer deposit account at p. The bank mints its
// address; the caller does not choose one and cannot.
func openCustomer(t *testing.T, ctx context.Context, p *Bank, name string) deposit.Account {
	t.Helper()
	acct, err := p.Deposit.OpenAccount(ctx, name, testAsset, p.ProductID, 0)
	assertNoError(t, err)
	return acct
}

// addressOf is the IBAN a bank minted for one of its accounts.
func addressOf(t *testing.T, a deposit.Account) deposit.Identifier {
	t.Helper()
	for _, i := range a.Identifiers {
		if i.Scheme == deposit.IdentifierIBAN {
			return i
		}
	}
	t.Fatalf("account %s holds no IBAN", a.ID)
	return deposit.Identifier{}
}

// openCustomerWithoutIdentifier opens an account and WITHDRAWS the address the
// bank minted: the fixture for a scheme refusing to route to an account it cannot
// address. Withdrawing is the only route in, since every account is issued one.
func openCustomerWithoutIdentifier(t *testing.T, ctx context.Context, p *Bank, name string) deposit.Account {
	t.Helper()
	acct := openCustomer(t, ctx, p, name)
	assertNoError(t, p.Deposit.RemoveIdentifier(ctx, acct.ID, addressOf(t, acct)))
	got, err := p.Deposit.GetAccount(ctx, acct.ID)
	assertNoError(t, err)
	return got
}

// mintAt is an address in a bank's own range, minted outside its register.
func mintAt(t *testing.T, p *Bank, serial uint64) deposit.Identifier {
	t.Helper()
	a, err := iban.New(p.Issuer.Country, p.Issuer.BankCode, serial)
	assertNoError(t, err)
	return deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: string(a)}
}

// plantAddress writes an address onto an account past the register's write-time
// check. Nothing in the domain does this. Both collision fixtures need it, and
// each names the state it stands in for.
func plantAddress(t *testing.T, ctx context.Context, p *Bank, id deposit.AccountID, ident deposit.Identifier) {
	t.Helper()
	assertNoError(t, p.Deposit.Store().Update(ctx, func(ctx context.Context, tx deposit.Tx) error {
		a, err := tx.GetDepositAccount(ctx, p.Deposit.BookID(), id)
		if err != nil {
			return err
		}
		a.Identifiers = append(a.Identifiers, ident)
		return tx.PutDepositAccount(ctx, p.Deposit.BookID(), a)
	}))
}

// openCycle is runCycle's opening step, for tests needing only a cycle to
// initiate into.
func openCycle(t *testing.T, ctx context.Context, sys *testSystem, scheme SchemeID) {
	t.Helper()
	_, err := sys.OpenCycle(ctx, scheme)
	assertNoError(t, err)
}

// fundAccount deposits into a customer account AND places the cash on reserve,
// because its callers want a bank that can pay.
func fundAccount(t *testing.T, ctx context.Context, sys *testSystem, p *Bank, acct deposit.Account, amount ledger.Amount) {
	t.Helper()
	takeCashIn(t, ctx, sys, p, acct, amount)
	lodgeReserves(t, ctx, sys, p, assetOfAccount(t, ctx, sys, p, acct), amount)
}

// takeCashIn is the deposit half alone, through the BANK's own network, because a
// deposit is the bank's act on its own register.
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

// lodgeReserves runs both halves of a lodgement, rendering the camt.050 and
// reading it back so the fixture exercises the real translation. See fundAccount.
func lodgeReserves(t *testing.T, ctx context.Context, sys *testSystem, p *Bank, asset ledger.AssetCode, amount ledger.Amount) {
	t.Helper()

	// Unique per lodgement: LodgeReservesTx keys its posting on the reference, so a
	// key derived from bank, asset and amount would collide the second time one bank
	// lodged the same amount. A deployment's message id supplies the unique one.
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

// TestALodgementQuotingNoAccountIsRefused closes the guard's own "does not
// apply" value: ReceiveLodgementTx refuses a camt.050 naming an account the
// agent does not hold, and one quoting NO account must not walk past that
// check.
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

	// And nothing was credited. The refusal is what stops it: a lodgement past the
	// guard would have posted into the agent's own row perfectly happily.
	after, err := sys.cb().ReserveBalance(ctx, a.BIC, testAsset)
	assertNoError(t, err)
	assertEqual(t, "the member's reserve after a lodgement naming no account", after, before)
}

// runCycle opens, closes and settles a cycle, returning the settled settlement.
func runCycle(t *testing.T, sys *testSystem, scheme SchemeID, submit func()) Settlement {
	t.Helper()
	ctx := context.Background()
	cyc, err := sys.OpenCycle(ctx, scheme)
	assertNoError(t, err)
	submit()
	closed, err := sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)

	st, err := flow.Settle(ctx, sys.nets, closed.ID, testCentralBankBIC)
	assertNoError(t, err)
	return st
}

// assetOf is the asset a scheme clears in, which is what a settlement leg is
// denominated in. See settleCycle.
func (s *testSystem) assetOf(id SchemeID) ledger.AssetCode {
	if scheme, ok := s.Scheme(id); ok {
		return scheme.Asset()
	}
	return ""
}

// returnWholePayment is flow.Return: every institution's half of an
// R-transaction, in order and each in its OWN unit of work.
func returnWholePayment(ctx context.Context, sys *testSystem, id PaymentID, reason string) (Payment, error) {
	return flow.Return(ctx, sys.nets, id, reason)
}

// bookTheAdvices is every member's half of a cut-off: each books the mirror leg
// its statement advises. The test's stand-in for one camt.053 per member.
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

// payTheCreditors is every institution's half of a cut-off once the reserves
// have moved, standing in for the clearing house's ACSC fan-out.
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
	bal, err := l.BookBalance(context.Background(), acct.Total())
	assertNoError(t, err)
	return bal
}

// suspenseBalance is where a payer's money sits between the debtor leg and
// settlement, so it says whether an actor touched the debtor bank's own book.
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

// TestASettlementIntoAClosedAccountGoesToUnclaimedBalances: a payee who empties
// and closes their account between their bank's acceptance and the cut-off must
// not be credited INTO the closed account.
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

	// Bob closes after his bank accepted the payment and the clearing house took it
	// into the cut-off. His balance is zero, which is what Close requires, and the
	// money on its way to him is in his bank's suspense.
	closeCreditorAccount(t, sys, pay)

	_, err = sys.CloseCycle(ctx, cyc.ID)
	assertNoError(t, err)

	// 1. The batch is not taken down by one closed account: the cut-off is the
	// settlement agent's netting transaction, and the closed account is not in it.
	_, _, err = sys.settleCycle(ctx, cyc.ID)
	assertNoError(t, err)
	_, err = sys.bank(b.BIC).SettleAtBank(ctx, pay.ID)
	assertNoError(t, err)

	// 2. The payment settled: the reserves moved and Bob's bank has been paid.
	// Which of that bank's accounts holds the money is between the bank and Bob.
	// On BOB'S BANK's copy, the one SettleAtBank moved.
	assertEqual(t, "status", mustGetPaymentAt(t, ctx, sys.bank(b.BIC), pay.ID).Status, Settled)

	// 3. And that account is the unclaimed-balances one, not Bob's.
	assertEqual(t, "bank B unclaimed balances", bookBalance(t, b.Ledger, accountsOf(t, b).Unclaimed), 30000)
	assertEqual(t, "bob's closed account", customerBalance(t, b, bob), 0)
}

// TestASettlementStoreFailureDoesNotRouteMoneyToUnclaimedBalances pins that
// only a CLOSED account diverts a credit, and that a store that could not
// answer diverts nothing.
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

	// The same store, decorated only from here on: the payment had to clear and the
	// reserves had to move before Bob's bank had a leg to post. The failure under
	// test is that bank's own.
	dropped := errors.New("connection reset by peer")
	broken := NewBankNetwork(failingUpdateStore{BankStore: sys.bank(b.BIC).Store(), accountErr: dropped},
		func() time.Time { return fixedTime }, b.ID)

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
// the other end of the diversion above, and it is a money bug.
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
	// Every institution is told, not just the payee's bank. A return is an edge from
	// Settled and each of the three keeps its own status, so without SettleAtCSM and
	// the payer's bank's own SettleAtBank the return below is refused by two copies.
	payTheCreditors(t, sys, cyc.ID)

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
	// And the liability the bank took on when it could not pay him is released: a
	// return of a diverted payment discharges that obligation to the only other
	// party with a claim on it.
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

	debtorPos, err := a.Deposit.Position(ctx, alice)
	assertNoError(t, err)

	// The two-way split below only names the legs correctly if there are
	// exactly two of them: with a third, "not the debtor's account" would
	// silently pick whichever came last.
	if len(posted.Entries) != 2 {
		t.Fatalf("debtor leg has %d entries, want 2 (customer and suspense)", len(posted.Entries))
	}
	var customer, suspense ledger.Entry
	for _, e := range posted.Entries {
		if e.AccountID == debtorPos.Account && e.Subsidiary == debtorPos.Subsidiary {
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
	// A return is not a rejection: it carries no StatusReason. pacs.004 draws its
	// reason from iso20022.ReturnReason instead, and PostReturnLegTx sets only the
	// free text. See the RejectCode doc on payment.Payment.
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

// TestSettleCycleIsAtomic tests the REFUSAL rather than a rollback.
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

	// No settlement was recorded and the cycle is still Closed, which leaves the
	// operation retriable once the member is funded.
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

	alice := openCustomer(t, ctx, a, "Alice")
	bob := openCustomer(t, ctx, b, "Bob")
	// Carol is opened here rather than through openCustomer because she needs an
	// overdraft: the fixture wants a customer who can afford a payment her bank
	// cannot settle.
	carol, err := c.Deposit.OpenAccount(ctx, "Carol", testAsset, c.ProductID, 100000)
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

// TestSettlementEntryOrderIsDeterministic pins the central bank's settlement
// entries to the order the participants were registered.
func TestSettlementEntryOrderIsDeterministic(t *testing.T) {
	ctx := context.Background()

	order := func() string {
		sys := testNetwork(t)
		var banks []*Bank
		var accounts []deposit.Account
		// One address each: these entries are settlement accounts, and four banks on
		// one BIC would be one member with one account, so the order compared would be
		// four copies of the same id.
		for i, name := range []string{"Bank A", "Bank B", "Bank C", "Bank D"} {
			p, err := storetest.Admit(ctx, sys.nets, name, testBICs[i], euroOnly)
			assertNoError(t, err)
			acct := openCustomer(t, ctx, p, "Customer at "+name)
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

	// Both settlement guards are one institution's own.
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
		// The cut-off belongs to the clearing house, so this refusal is its half's
		// and not either bank's: both accept the collection, and AcceptAtCSMTx finds
		// no window open for sepa.dd. A mandate is needed to get that far at all.
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

		// And unclaimed balances is a LIABILITY. Money that arrives for an account
		// which cannot receive it is still owed, so it is the same class as a
		// deposit; booking it as an asset would say the bank had earned it.
		assertEqual(t, "unclaimed account type", unclaimed.Type, ledger.Liability)

		// Returns Receivable is the CONTRAST, asserted beside its opposite because
		// the contrast is the teaching claim: two pooled accounts with no customer's
		// name on either, one money the bank OWES and one money the bank is OWED.
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

// submit is a payment SUBMITTED, through the bank the request says submits it.
func (s *testSystem) submit(ctx context.Context, req InitiatePaymentRequest) (Payment, error) {
	return s.bank(submitterOfReq(s, req)).SubmitPayment(ctx, req)
}

// allBanks is every bank in the system, read from each bank's own database.
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
func mustGetPaymentAt(t *testing.T, ctx context.Context, net interface {
	GetPayment(context.Context, PaymentID) (Payment, error)
}, id PaymentID) Payment {
	t.Helper()
	p, err := net.GetPayment(ctx, id)
	assertNoError(t, err)
	return p
}

// mustGetBank re-reads a bank from ITS OWN store, so an assertion is about what
// was committed. Its own, because a bank row is in the bank shape and no other:
// reading one through the clearing house's network is a missing table.
func mustGetBank(t *testing.T, ctx context.Context, sys *testSystem, id ParticipantID) *Bank {
	t.Helper()
	p, err := sys.bank(iso20022.BIC(id)).GetBank(ctx, id)
	assertNoError(t, err)
	return p
}

// assertCentralBankReserveAccountCount counts accounts in the central bank's
// own book carrying the given bank's name — the reserve accounts opened for
// that member, one per asset.
func assertCentralBankReserveAccountCount(t *testing.T, ctx context.Context, sys *testSystem, bank string, want int) {
	t.Helper()
	var got int
	// The CENTRAL BANK's store, because the book is.
	assertNoError(t, sys.cb().Store().View(ctx, func(ctx context.Context, tx CentralBankTx) error {
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
// made about the first act.
func TestFoundingABankTouchesNoOtherInstitution(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	aurora := sys.bank("AURODEFFXXX")
	b, err := aurora.FoundBank(ctx, "Aurora Bank", "AURODEFFXXX", storetest.FixtureCountry, euroOnly)
	assertNoError(t, err)

	if got := b.Assets[testAsset].Settlement; got != "" {
		t.Errorf("a founded bank names settlement account %q; it has not asked for one yet", got)
	}
	// And it can open NO customer account, which is the other half of the same
	// claim. A bank code is a national registry's to allocate and this bank has
	// applied to none, so it has no range to give an address out of.
	if _, err := b.OpenCustomerAccount(ctx, "Alice", testAsset); !errors.Is(err, deposit.ErrNoIssuer) {
		t.Errorf("a founded bank opened a customer account: %v, want deposit.ErrNoIssuer", err)
	}

	// Two institutions, two reads, and that is the point of the test rather than
	// an inconvenience: "no other institution was touched" is a claim about the
	// central bank's database AND about the clearing house's, and neither can be
	// asked about the other's table.
	assertNoError(t, sys.cb().Store().View(ctx, func(ctx context.Context, tx CentralBankTx) error {
		if _, err := tx.GetSettlementMember(ctx, "AURODEFFXXX"); !errors.Is(err, ErrSettlementMemberNotFound) {
			t.Errorf("the central bank has a member row for a bank that only founded itself: %v", err)
		}
		return nil
	}))
	assertNoError(t, sys.Store().View(ctx, func(ctx context.Context, tx CsmTx) error {
		if _, err := tx.GetRosterEntry(ctx, "AURODEFFXXX"); !errors.Is(err, ErrRosterEntryNotFound) {
			t.Errorf("the clearing house has a routing entry for a bank that has not applied: %v", err)
		}
		return nil
	}))
	assertCentralBankReserveAccountCount(t, ctx, sys, "Aurora Bank", 0)
}

// TestOpeningASettlementAccountTwiceOpensOne is what makes a retried admission
// safe: nothing delivers an admission, so the way in is the OPERATOR re-driving
// one that failed after the accounts were opened.
func TestOpeningASettlementAccountTwiceOpensOne(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	in := AdmissionRequest{
		Name: "Aurora Bank", BIC: "AURODEFFXXX", Country: testAllocation.Country, Asset: testAsset, Ref: "adm-1",
	}
	first, firstCode, err := sys.cb().OpenSettlementAccount(ctx, in)
	assertNoError(t, err)
	second, secondCode, err := sys.cb().OpenSettlementAccount(ctx, in)
	assertNoError(t, err)
	// The registry allocates once per bank per country, so a second request in
	// the same asset is answered with the code the first was given rather than
	// with a second range.
	if firstCode.BankCode == "" || firstCode != secondCode {
		t.Errorf("the second request was allocated %+v, want the first's %+v", secondCode, firstCode)
	}

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
	extended, extendedCode, err := sys.cb().OpenSettlementAccount(ctx, AdmissionRequest{
		Name: "Aurora Bank", BIC: "AURODEFFXXX", Country: testAllocation.Country, Asset: "USD", Ref: "adm-2",
	})
	assertNoError(t, err)
	// Nor does a second ASSET allocate a second range: one request asks for one
	// currency, and a bank issuing addresses under two codes would be two banks
	// to anybody routing.
	if extendedCode != firstCode {
		t.Errorf("the dollar request was allocated %+v, want the euro one's %+v", extendedCode, firstCode)
	}
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
// that owns routing. Enrolling a subscriber twice at an EBICS host is a no-op,
// which is about reachability; this is the statement about membership.
func TestAdmittingABICTwiceIsRefused(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	ack := AdmissionAcknowledgement{
		BIC:      "AURODEFFXXX",
		Issuer:   testAllocation,
		Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001"},
		Ref:      "adm-1",
	}
	_, err := sys.AdmitMember(ctx, ack)
	assertNoError(t, err)

	clash := ack
	clash.Accounts = map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.009"}
	clash.Ref = "adm-2"
	if _, err := sys.AdmitMember(ctx, clash); !errors.Is(err, ErrBICAlreadyAdmitted) {
		t.Fatalf("admitting a second bank on a taken BIC: %v, want ErrBICAlreadyAdmitted", err)
	}
	// And the roster still says what it said. A refusal that overwrote the entry and
	// then reported failure would leave routing pointing at the impostor — which is
	// what the ADMISSION REFERENCE is for, and the only field that could show it.
	assertNoError(t, sys.Store().View(ctx, func(ctx context.Context, tx CsmTx) error {
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

	// The same admission asking again is not a clash. This is the acknowledgement
	// for the bank's second currency, and it extends the entry it finds.
	second := ack
	second.Accounts = map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001", "USD": "200.100.002"}
	extended, err := sys.AdmitMember(ctx, second)
	assertNoError(t, err)
	if !slices.Equal(extended.Assets, []ledger.AssetCode{testAsset, "USD"}) {
		t.Errorf("the roster entry clears in %v, want [EUR USD]", extended.Assets)
	}
}

// TestABankRefusesAnAcknowledgementOfAnotherAdmission is the guard
// Bank.AdmissionRef exists for, made about the act: the act is separately
// callable, so what it refuses has to be true of the act and not only of
// today's one caller.
func TestABankRefusesAnAcknowledgementOfAnotherAdmission(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	own := sys.bank(testBIC)
	bank, err := own.FoundBank(ctx, "Aurora Bank", testBIC, storetest.FixtureCountry, euroOnly)
	assertNoError(t, err)
	if bank.AdmissionRef != "" {
		t.Fatalf("a founded bank cites admission %q; it has accepted none", bank.AdmissionRef)
	}

	ack := AdmissionAcknowledgement{
		BIC:      testBIC,
		Issuer:   testAllocation,
		Ref:      "adm-1",
		Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001"},
	}
	// A bank with nothing recorded takes the first that names it.
	_, err = sys.bank(bank.BIC).RecordMembership(ctx, ack)
	assertNoError(t, err)
	recorded := mustGetBank(t, ctx, sys, bank.ID)
	assertEqual(t, "the admission the bank recorded", recorded.AdmissionRef, "adm-1")
	assertEqual(t, "settlement reference", string(recorded.Assets[testAsset].Settlement), "200.100.001")

	// The same admission again, with a second asset: extended, not refused. This
	// is a two-currency bank's second acknowledgement, which one request per
	// currency makes ordinary.
	second := ack
	second.Accounts = map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001", "USD": "200.100.002"}
	_, err = sys.bank(bank.BIC).RecordMembership(ctx, second)
	assertNoError(t, err)

	// A DIFFERENT admission, naming this bank's own address and an account the
	// settlement agent never opened. Refused, and nothing moves.
	forged := ack
	forged.Ref = "adm-someone-else"
	forged.Accounts = map[ledger.AssetCode]ledger.AccountID{testAsset: "acc_bogus"}
	if _, err := sys.bank(bank.BIC).RecordMembership(ctx, forged); !errors.Is(err, ErrBankAlreadyAdmitted) {
		t.Fatalf("recording an acknowledgement of another admission: %v, want ErrBankAlreadyAdmitted", err)
	}
	after := mustGetBank(t, ctx, sys, bank.ID)
	assertEqual(t, "settlement reference after the refusal", string(after.Assets[testAsset].Settlement), "200.100.001")
	assertEqual(t, "the admission after the refusal", after.AdmissionRef, "adm-1")
}

// TestABankRefusesAnAcknowledgementThatWouldLeaveItWrong closes the two holes
// the guards above leave, both the shape this branch has met three times
// before: a guard closes a case and its own "does not apply" value stays
// reachable.
func TestABankRefusesAnAcknowledgementThatWouldLeaveItWrong(t *testing.T) {
	ctx := context.Background()

	// founded returns a network and a bank that operates in euro only and has
	// recorded nothing.
	founded := func(t *testing.T) (*testSystem, *Bank) {
		t.Helper()
		sys := testNetwork(t)
		bank, err := sys.bank(testBIC).FoundBank(ctx, "Aurora Bank", testBIC, storetest.FixtureCountry, euroOnly)
		assertNoError(t, err)
		return sys, bank
	}
	record := func(sys *testSystem, id ParticipantID, ack AdmissionAcknowledgement) error {
		_, err := sys.bank(iso20022.BIC(id)).RecordMembership(ctx, ack)
		return err
	}
	real := AdmissionAcknowledgement{
		BIC:      testBIC,
		Issuer:   testAllocation,
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
		// The whole point: the bank has still recorded nothing and its reference is
		// still free, so the acknowledgement it is actually waiting for is still
		// accepted.
		after := mustGetBank(t, ctx, sys, bank.ID)
		assertEqual(t, "the settlement account after the refusal",
			string(after.Assets[testAsset].Settlement), "")
		assertEqual(t, "the admission after the refusal", after.AdmissionRef, "")
		assertNoError(t, record(sys, bank.ID, real))
		assertEqual(t, "the admission it was waiting for",
			mustGetBank(t, ctx, sys, bank.ID).AdmissionRef, "adm-1")
	})

	t.Run("a second currency alongside one already recorded", func(t *testing.T) {
		sys := testNetwork(t)
		bank, err := sys.bank(testBIC).FoundBank(ctx, "Aurora Bank", testBIC, storetest.FixtureCountry, []ledger.AssetCode{testAsset, "USD"})
		assertNoError(t, err)
		assertNoError(t, record(sys, bank.ID, real))

		both := real
		both.Accounts = map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001", "USD": "200.100.002"}
		assertNoError(t, record(sys, bank.ID, both))
		after := mustGetBank(t, ctx, sys, bank.ID)
		assertEqual(t, "the euro reference", string(after.Assets[testAsset].Settlement), "200.100.001")
		assertEqual(t, "the dollar reference", string(after.Assets["USD"].Settlement), "200.100.002")
	})
}

// TestAnAcknowledgementQuotingNoAdmissionIsRefusedByBothActs guards the value
// BOTH admission guards are made of.
func TestAnAcknowledgementQuotingNoAdmissionIsRefusedByBothActs(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	bank, err := sys.bank(testBIC).FoundBank(ctx, "Aurora Bank", testBIC, storetest.FixtureCountry, euroOnly)
	assertNoError(t, err)

	noRef := AdmissionAcknowledgement{
		BIC:      testBIC,
		Issuer:   testAllocation,
		Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001"},
	}

	// The BANK. A membership recorded under no admission is a row that settles
	// through a real account and reads as "accepted nothing", which is the reset.
	if _, err := sys.bank(bank.BIC).RecordMembership(ctx, noRef); !errors.Is(err, ErrAdmissionNotIdentified) {
		t.Errorf("the bank recorded a membership under no admission: %v, want ErrAdmissionNotIdentified", err)
	}
	got := mustGetBank(t, ctx, sys, bank.ID)
	if got.Assets[testAsset].Settlement != "" && got.AdmissionRef == "" {
		t.Error("the bank settles through an account under no reference; its own guard reads that as having accepted nothing")
	}

	// The CLEARING HOUSE. Two institutions on one address, both quoting nothing,
	// compare equal — so the refusal that row exists for never fires.
	if _, err := sys.AdmitMember(ctx, noRef); !errors.Is(err, ErrAdmissionNotIdentified) {
		t.Errorf("the clearing house admitted a member under no admission: %v, want ErrAdmissionNotIdentified", err)
	}
	assertNoError(t, sys.Store().View(ctx, func(ctx context.Context, tx CsmTx) error {
		if _, err := tx.GetRosterEntry(ctx, testBIC); !errors.Is(err, ErrRosterEntryNotFound) {
			t.Errorf("an acknowledgement quoting no admission put %s in the roster: %v", testBIC, err)
		}
		return nil
	}))

	// And a real one still works, which is what says the guard refuses the
	// SENTINEL rather than the field.
	real := noRef
	real.Ref = "adm-1"
	_, err = sys.bank(bank.BIC).RecordMembership(ctx, real)
	assertNoError(t, err)
	assertEqual(t, "the admission the bank recorded", mustGetBank(t, ctx, sys, bank.ID).AdmissionRef, "adm-1")
}

// TestAnUnusableAcknowledgementIsRefusedByBothActs holds checkAcknowledgement
// to its claim: an acknowledgement neither act can act on is refused by BOTH,
// and the two refuse the same set.
func TestAnUnusableAcknowledgementIsRefusedByBothActs(t *testing.T) {
	ctx := context.Background()
	real := AdmissionAcknowledgement{
		BIC: testBIC, Ref: "adm-1",
		Issuer:   testAllocation,
		Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001"},
	}

	for _, tc := range []struct {
		what string
		in   AdmissionAcknowledgement
		want error
	}{
		{"no account owner", AdmissionAcknowledgement{
			Issuer: testAllocation, Ref: "adm-x",
			Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.009"}},
			iso20022.ErrBICFormat},
		{"a malformed account owner", AdmissionAcknowledgement{
			BIC: "nonsense", Issuer: testAllocation, Ref: "adm-x",
			Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.009"}},
			iso20022.ErrBICFormat},
		{"no allocation", AdmissionAcknowledgement{
			BIC: testBIC, Ref: "adm-x",
			Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.009"}},
			iban.ErrUnknownCountry},
		{"an allocation of the wrong width", AdmissionAcknowledgement{
			BIC: testBIC, Issuer: iban.Issuer{Country: testAllocation.Country, BankCode: "1"}, Ref: "adm-x",
			Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.009"}},
			iban.ErrBankCodeWidth},
		{"no account at all", AdmissionAcknowledgement{BIC: testBIC, Issuer: testAllocation, Ref: "adm-x"},
			ErrAdmittedAccountUnusable},
		{"an account naming no asset", AdmissionAcknowledgement{
			BIC: testBIC, Issuer: testAllocation, Ref: "adm-x",
			Accounts: map[ledger.AssetCode]ledger.AccountID{"": "200.100.009"}},
			ErrAdmittedAccountUnusable},
		{"an asset naming no account", AdmissionAcknowledgement{
			BIC: testBIC, Issuer: testAllocation, Ref: "adm-x",
			Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: ""}},
			ErrAdmittedAccountUnusable},
	} {
		t.Run(tc.what, func(t *testing.T) {
			// A network each, because a case that wrote something would otherwise
			// decide the next one's outcome.
			sys := testNetwork(t)
			bank, err := sys.bank(testBIC).FoundBank(ctx, "Aurora Bank", testBIC, storetest.FixtureCountry, euroOnly)
			assertNoError(t, err)

			if _, err := sys.AdmitMember(ctx, tc.in); !errors.Is(err, tc.want) {
				t.Errorf("the clearing house answered an acknowledgement with %s: %v, want %v", tc.what, err, tc.want)
			}
			// The BANK's own unit of work, on the BANK's own database. The two
			// refusals are two institutions', asked separately because they have to be.
			if _, err := sys.bank(bank.BIC).RecordMembership(ctx, tc.in); !errors.Is(err, tc.want) {
				t.Errorf("the bank answered an acknowledgement with %s: %v, want %v", tc.what, err, tc.want)
			}

			// Neither institution wrote anything, and — the load-bearing half —
			// the real acknowledgement still goes through. A refusal that left a
			// row behind would wedge the admission it was protecting.
			if got := mustGetBank(t, ctx, sys, bank.ID); got.Assets[testAsset].Settlement != "" {
				t.Errorf("the bank settles through %q after the refusal", got.Assets[testAsset].Settlement)
			}
			_, err = sys.AdmitMember(ctx, real)
			assertNoError(t, err)
			_, err = sys.bank(bank.BIC).RecordMembership(ctx, real)
			assertNoError(t, err)
			admitted := mustGetBank(t, ctx, sys, bank.ID)
			assertEqual(t, "its settlement reference after the true acknowledgement",
				string(admitted.Assets[testAsset].Settlement), "200.100.001")
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
	auroraNet, verdeNet := sys.bank("AURODEFFXXX"), sys.bank("VERDITMMXXX")
	aurora, err := auroraNet.FoundBank(ctx, "Aurora Bank", "AURODEFFXXX", storetest.FixtureCountry, euroOnly)
	assertNoError(t, err)
	verde, err := verdeNet.FoundBank(ctx, "Banca Verde", "VERDITMMXXX", storetest.FixtureCountry, euroOnly)
	assertNoError(t, err)

	// The acknowledgement is Aurora's; Verde tries to record it as its own.
	ack := AdmissionAcknowledgement{
		BIC:      aurora.BIC,
		Issuer:   testAllocation,
		Accounts: map[ledger.AssetCode]ledger.AccountID{testAsset: "200.100.001"},
		Ref:      "adm-1",
	}
	_, err = sys.bank(verde.BIC).RecordMembership(ctx, ack)
	if !errors.Is(err, ErrNotThisBanksAdmission) {
		t.Fatalf("recording another bank's admission: %v, want ErrNotThisBanksAdmission", err)
	}
	// Neither bank moved. Verde is not a member, and Aurora did not become one
	// because somebody else read its acknowledgement.
	for _, want := range []*Bank{aurora, verde} {
		got := mustGetBank(t, ctx, sys, want.ID)
		if got.Assets[testAsset].Settlement != "" {
			t.Errorf("%s now names settlement account %q", got.Name, got.Assets[testAsset].Settlement)
		}
	}

	// And the bank the acknowledgement IS addressed to records it.
	_, err = sys.bank(aurora.BIC).RecordMembership(ctx, ack)
	assertNoError(t, err)
	got := mustGetBank(t, ctx, sys, aurora.ID)
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

// TestSettleCycleFailsWhenParticipantLacksTheAsset: settlement must never fall
// back to a base currency when the agent holds no account for a member in the
// cycle's asset.
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

	assertNoError(t, sys.stores.CentralBank().Update(ctx, func(ctx context.Context, tx CentralBankTx) error {
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
// debit against a EUR credit, valid double-entry saying nothing about the
// creditor's account.
func TestPaymentRejectsCreditorAccountNotInSchemeAsset(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	alpha, err := storetest.Admit(ctx, sys.nets, "Alpha", testBIC, euroOnly)
	assertNoError(t, err)
	// Distinct addresses, because they are distinct institutions: this fixture's
	// whole subject is the check the PAYEE's bank makes on its own register.
	beta, err := storetest.Admit(ctx, sys.nets, "Beta", testBIC2, euroOnly)
	assertNoError(t, err)

	// The payer's leg has to be flawless for this test to be about the payee's.
	from := openCustomer(t, ctx, alpha, "Anna")
	fundAccount(t, ctx, sys, alpha, from, 100000)
	// Addressable despite the asset: a bank mints an account's IBAN when it opens
	// it, whatever the account is denominated in. An address says who holds the
	// account, and says nothing about what is in it.
	to, err := beta.OpenCustomerAccount(ctx, "Bruno", "BTC")
	assertNoError(t, err)

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
// different participant's book. This proves the debtor leg is checked too: a
// scheme that only validated the creditor would let this one through.
func TestPaymentRejectsDebtorAccountNotInSchemeAsset(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	alpha, err := storetest.Admit(ctx, sys.nets, "Alpha", testBIC, euroOnly)
	assertNoError(t, err)
	beta, err := storetest.Admit(ctx, sys.nets, "Beta", testBIC2, euroOnly)
	assertNoError(t, err)

	// Both accounts are addressable, so the only thing wrong is the payer's asset:
	// with the check removed the payment gets further rather than failing for a
	// second reason, which is what makes the counterfactual sharp.
	from, err := alpha.OpenCustomerAccount(ctx, "Anna", "BTC")
	assertNoError(t, err)
	to := openCustomer(t, ctx, beta, "Bruno")

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

// The asset check runs unconditionally in each bank's own half, before any
// scheme's Validate, so it applies to SDD structurally rather than by that
// scheme's choice.
func TestSDDPaymentRejectsAccountNotInSchemeAsset(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)

	alpha, err := storetest.Admit(ctx, sys.nets, "Alpha", testBIC, euroOnly)
	assertNoError(t, err)
	beta, err := storetest.Admit(ctx, sys.nets, "Beta", testBIC2, euroOnly)
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

// TestAMismatchedMandateIsRefusedAtItsFirstCollection measures the ruling that
// a mandate whose two accounts are in different assets is CREATED and refuses
// its first collection.
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

// TestAMandateBelongsToItsCreditorsBankAndToNoOther: a mandate is the
// CREDITOR's bank's row, and no other bank may record, read or revoke one.
func TestAMandateBelongsToItsCreditorsBankAndToNoOther(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)

	debtor := PartyRef{Account: alice}
	creditor := PartyRef{Account: bob}

	// A mandate's creditor is checked against the RECORDING bank's own register,
	// and a ref no register holds is refused.
	_, err := sys.bank(a.BIC).CreateMandate(ctx, a.BIC, debtor,
		PartyRef{Account: "dep_no_such_account"}, 0)
	assertError(t, err, ErrAccountNotInParticipant)
	// The clearing house cannot either: it is not a member bank, so this is not
	// its act at all. Recording one is a method on BankNetwork, so the crossing
	// left to measure is a bank's handle over the clearing house's core.
	asCSM := BankHandleOverClearingHouse(sys.bank(a.BIC), sys.ClearingHouseNetwork)
	_, err = asCSM.CreateMandate(ctx, a.BIC, debtor, creditor, 0)
	assertError(t, err, ErrNotThisInstitutionsAct)

	// The creditor's bank can, and the row carries its own account's asset and
	// the debtor's bank as an address.
	m, err := sys.bank(b.BIC).CreateMandate(ctx, a.BIC, debtor, creditor, 0)
	assertNoError(t, err)
	assertEqual(t, "the mandate's asset", string(m.Asset), string(testAsset))
	assertEqual(t, "the mandate's debtor agent", string(m.DebtorAgent), string(a.BIC))

	// Revoking is still refused to anything that is not a member bank, which is
	// the whole of what the row can now decide on its own.
	if err := asCSM.RevokeMandate(ctx, m.ID); !errors.Is(err, ErrNotThisBanksMandate) {
		t.Errorf("the clearing house revoked a mandate: %v", err)
	}

	// The creditor's bank sees its own, and the debtor's bank sees nothing —
	// which is the isolation claim the note above said to restore, made out of
	// the store rather than out of a comparison.
	mine, err := sys.bank(b.BIC).ListMandates(ctx)
	assertNoError(t, err)
	assertEqual(t, "the creditor bank's mandates", len(mine), 1)
	theirs, err := sys.bank(a.BIC).ListMandates(ctx)
	assertNoError(t, err)
	assertEqual(t, "the debtor bank's mandates", len(theirs), 0)
	// And it cannot read the one it is a party to by name either.
	if _, err := sys.bank(a.BIC).GetMandate(ctx, m.ID); !errors.Is(err, ErrMandateNotFound) {
		t.Errorf("the debtor's bank read the creditor's mandate: %v", err)
	}
}

// TestCrossAssetPaymentSurvivesInitiationAndFailsAtThePayeesBank is what the
// ledger does and does not catch about a euro-to-bitcoin payment.
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
	// exists to prevent. On the PAYEE'S BANK's copy, the row its own
	// PostCreditorLegTx reads.
	assertNoError(t, sys.bank(b.BIC).Store().Update(ctx, func(ctx context.Context, tx BankTx) error {
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
	settlements, err := sys.cb().ListSettlements(ctx)
	assertNoError(t, err)
	assertEqual(t, "settlements recorded", len(settlements), 1)
	// The clearing house's copy, which is where Cleared is a status at all: it
	// is that institution's record of having taken the payment into the cut-off.
	assertEqual(t, "payment status", mustGetPaymentAt(t, ctx, sys.ClearingHouseNetwork, pay.ID).Status, Cleared)
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
	line, err := bank.Lending.OpenRevolvingLine(ctx, "Bruno Line", testAsset, 250_000, 180_000, interest.ACT365, 20_000)
	assertNoError(t, err)
	brunoPos, err := bank.Deposit.Position(ctx, bruno.ID)
	assertNoError(t, err)
	_, err = bank.Lending.Draw(ctx, line.ID, brunoPos, 100_000, "Draw")
	assertNoError(t, err)

	// Spend the account into overdraft.
	merchant, err := bank.OpenCustomerAccount(ctx, "Merchant", testAsset)
	assertNoError(t, err)
	merchantPos, err := bank.Deposit.Position(ctx, merchant.ID)
	assertNoError(t, err)
	_, err = bank.Ledger.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "Card payment",
		Entries: []ledger.Entry{
			{AccountID: brunoPos.Account, Subsidiary: brunoPos.Subsidiary, Amount: 110_000, Direction: ledger.Debit},
			{AccountID: merchantPos.Account, Subsidiary: merchantPos.Subsidiary, Amount: 110_000, Direction: ledger.Credit},
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

// TestResolveIdentifierAnswersOnlyForTheAskingBanksOwnRegister is the
// narrowing, and it is one assertion followed by its complement — the second is
// the whole change and the first would pass on the sweep too.
func TestResolveIdentifierAnswersOnlyForTheAskingBanksOwnRegister(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)
	verde := addParticipant(t, ctx, net, "Banca Verde", verdeBIC)

	alice := openCustomer(t, ctx, aurora, "Alice")
	_ = openCustomer(t, ctx, verde, "Bruno")
	alicesIBAN := addressOf(t, alice)

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

	// And the bank that does not hold it has no answer — not the account, and not
	// "it is at Aurora" either. That second thing is what a directory SERVICE would
	// say; a bank saying it would be reading another bank's register.
	if _, err := net.bank(verde.BIC).ResolveIdentifier(ctx, alicesIBAN); !errors.Is(err, deposit.ErrIdentifierNotFound) {
		t.Fatalf("Verde resolving Aurora's customer = %v, want ErrIdentifierNotFound", err)
	}
}

func TestResolveIdentifierNotFound(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)

	// An address Aurora could have issued and has not. It has to be well-formed:
	// a value that failed its check digits would be refused for that, and the
	// lookup this test is about would never run.
	_, err := net.bank(aurora.BIC).ResolveIdentifier(ctx, mintAt(t, aurora, 999_999))
	if !errors.Is(err, deposit.ErrIdentifierNotFound) {
		t.Fatalf("ResolveIdentifier = %v, want ErrIdentifierNotFound", err)
	}
}

// TestACrossBankCollisionTakesADuplicateAllocation records a guarantee this
// system does not have, which is why it is a test rather than a deletion.
func TestACrossBankCollisionTakesADuplicateAllocation(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)
	verde := addParticipant(t, ctx, net, "Banca Verde", verdeBIC)

	alice := openCustomer(t, ctx, aurora, "Alice")
	bruno := openCustomer(t, ctx, verde, "Bruno")

	// Alice's own address, planted at Verde as well. Each bank holds it once, so
	// neither is ambiguous; what neither can tell is that the other holds it too.
	shared := addressOf(t, alice)
	plantAddress(t, ctx, verde, bruno.ID, shared)

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

// TestResolveIdentifierRefusesAWithinBankCollision is the half a register can
// police, and all it can police. Two accounts inside ONE bank holding one
// address is ambiguous, which is what makes the missing UNIQUE constraint safe.
func TestResolveIdentifierRefusesAWithinBankCollision(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)
	addParticipant(t, ctx, net, "Banca Verde", verdeBIC)

	alice := openCustomer(t, ctx, aurora, "Alice")
	aaron := openCustomer(t, ctx, aurora, "Aaron")

	shared := addressOf(t, alice)
	plantAddress(t, ctx, aurora, aaron.ID, shared)

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

	alice := openCustomer(t, ctx, aurora, "Alice")
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

// TestTheFarLegsAddressAndAccountCannotDisagree: the receiving bank resolves
// its own party FROM the address, so account and address cannot disagree on the
// far leg — the account is whichever one holds the address.
func TestTheFarLegsAddressAndAccountCannotDisagree(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)
	verde := addParticipant(t, ctx, net, "Banca Verde", verdeBIC)
	openCycle(t, ctx, net, SchemeSEPACT)

	alice := openCustomer(t, ctx, aurora, "Alice")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno")

	// The far leg is an ADDRESS and no account, which is what a pacs.008 carries.
	_, err := initiate(ctx, net, InitiatePaymentRequest{
		Scheme: SchemeSEPACT,
		Debtor: PartyRef{Account: alice.ID},
		Creditor: PartyRef{
			Identifier: mintAt(t, verde, 987654),
		},
		Amount:          10_00,
		CreditorDetails: PartyDetails{Name: bruno.Name},
		// The submitting bank's own agent, which this fixture uses to pick the
		// network that submits. Submission overwrites it from Aurora's own row.
		DebtorDetails: PartyDetails{Agent: aurora.BIC},
	})
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

	alice := openCustomer(t, ctx, aurora, "Alice")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno")

	_, err := initiate(ctx, net, InitiatePaymentRequest{
		Scheme: SchemeSEPACT,
		Debtor: PartyRef{
			Account: alice.ID,
			// Bruno's address, pointing at Alice's account.
			Identifier: addressOf(t, bruno),
		},
		Creditor:        PartyRef{Account: bruno.ID},
		Amount:          10_00,
		CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
		DebtorDetails:   PartyDetails{Agent: aurora.BIC}})
	if !errors.Is(err, ErrIdentifierMismatch) {
		t.Fatalf("initiation = %v, want ErrIdentifierMismatch", err)
	}
}

// TestInitiateRefusesAnAddressFromAnotherIdentifierScheme: an address the
// account really holds, in the wrong identifier scheme.
func TestInitiateRefusesAnAddressFromAnotherIdentifierScheme(t *testing.T) {
	const testPAN = deposit.IdentifierScheme("PAN")

	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)
	verde := addParticipant(t, ctx, net, "Banca Verde", verdeBIC)

	alice := openCustomer(t, ctx, aurora, "Alice")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno")

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
		mustGetPaymentAt(t, ctx, net.bank(aurora.BIC), pay.ID).Debtor.Identifier, addressOf(t, alice))
	assertEqual(t, "back-filled creditor address",
		mustGetPaymentAt(t, ctx, net.bank(verde.BIC), pay.ID).Creditor.Identifier, addressOf(t, bruno))
}

// TestInitiateBackFillsTheAddressOnBothLegs: a payment that quotes no address
// still records one, rather than the identifier staying empty for every caller
// who did not volunteer it.
func TestInitiateBackFillsTheAddressOnBothLegs(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	aurora := addParticipant(t, ctx, net, "Aurora Bank", auroraBIC)
	verde := addParticipant(t, ctx, net, "Banca Verde", verdeBIC)

	alice := openCustomer(t, ctx, aurora, "Alice")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno")

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

	alicesIBAN := addressOf(t, alice)
	brunosIBAN := addressOf(t, bruno)

	// Each bank's own leg, on that bank's own copy, read back out of storage
	// rather than taken from the value a call returned.
	atAurora := mustGetPaymentAt(t, ctx, net.bank(aurora.BIC), pay.ID)
	assertEqual(t, "the debtor address on the payer's bank's copy", atAurora.Debtor.Identifier, alicesIBAN)
	atVerde := mustGetPaymentAt(t, ctx, net.bank(verde.BIC), pay.ID)
	assertEqual(t, "the creditor address on the payee's bank's copy", atVerde.Creditor.Identifier, brunosIBAN)

	// The payer's back-fill travels: it was made before the instruction went out, so
	// it is on the message and on both downstream copies. The payee's does not — it
	// is made on arrival, and nothing carries it back upstream.
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

	alice := openCustomer(t, ctx, aurora, "Alice")
	fundAccount(t, ctx, net, aurora, alice, 100_00)
	bruno := openCustomer(t, ctx, verde, "Bruno")
	// A second IBAN on the debtor, which makes the debtor leg the one that has to
	// refuse. No cycle is open, which is harmless: the addressing checks run
	// before initiation looks for one.
	second := mintAt(t, aurora, 999_999)
	plantAddress(t, ctx, aurora, alice.ID, second)

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
				Identifier: second,
			},
			Creditor:        PartyRef{Account: bruno.ID},
			Amount:          10_00,
			CreditorDetails: PartyDetails{Agent: verde.BIC, Name: bruno.Name},
			DebtorDetails:   PartyDetails{Agent: aurora.BIC}})
		assertNoError(t, err)
		// The payer's bank's own copy, because the choice is that bank's own act. The
		// clearing house's row carries what the message said, built after this
		// back-fill, so both agree — but only one is where the fact is made.
		assertEqual(t, "chosen debtor address",
			mustGetPaymentAt(t, ctx, net.bank(aurora.BIC), pay.ID).Debtor.Identifier, second)
	})
}

// TestMandateSurvivesAReissuedDebtorIdentifier: reissuing an address must not
// kill the mandates on the account.
func TestMandateSurvivesAReissuedDebtorIdentifier(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)

	debtor := PartyRef{Account: alice}
	creditor := PartyRef{Account: biller}
	m, err := sys.bank(b.BIC).CreateMandate(ctx, a.BIC, debtor, creditor, 0)
	assertNoError(t, err)

	before, err := a.Deposit.GetAccount(ctx, alice)
	assertNoError(t, err)
	old := addressOf(t, before)
	reissued, err := a.Deposit.ReissueIdentifier(ctx, alice)
	assertNoError(t, err)
	if reissued == old {
		t.Fatalf("reissue returned the address it replaced: %v", reissued)
	}

	var pay Payment
	runCycle(t, sys, SchemeSEPADD, func() {
		pay, err = initiate(ctx, sys, InitiatePaymentRequest{
			Scheme: SchemeSEPADD, Amount: 25000, MandateID: m.ID,
			Debtor: debtor, Creditor: creditor, Description: "Electricity bill",
			DebtorDetails:   PartyDetails{Agent: a.BIC, Name: "Alice"},
			CreditorDetails: PartyDetails{Agent: b.BIC}})
		assertNoError(t, err)
	})

	// The collection went through, recording the NEW address: the mandate
	// authorises the account, and each payment records how it was reached.
	assertEqual(t, "reissued debtor address on the payment",
		mustGetPaymentAt(t, ctx, sys.bank(a.BIC), pay.ID).Debtor.Identifier, reissued)
	assertEqual(t, "alice", customerBalance(t, a, alice), 75000)
	assertEqual(t, "biller", customerBalance(t, b, biller), 25000)
}

// TestMandateStillRefusesADifferentParty is the other direction of SameParty's
// loosening: it stops comparing the quoted address, and what needs proof is
// that it did not loosen the part that matters.
func TestMandateStillRefusesADifferentParty(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, biller := setupTwoBanks(t, sys)

	debtor := PartyRef{Account: alice}
	creditor := PartyRef{Account: biller}
	m, err := sys.bank(b.BIC).CreateMandate(ctx, a.BIC, debtor, creditor, 0)
	assertNoError(t, err)

	// A second customer at Alice's own bank, funded, addressable, and party to
	// nothing. Same bank on purpose: an implementation comparing only
	// PartyRef.Participant would pass a cross-bank version of this test.
	carla := openCustomer(t, ctx, a, "Carla")
	fundAccount(t, ctx, sys, a, carla, 100000)
	// And a second biller at bank B, for the creditor half.
	other := openCustomer(t, ctx, b, "Other Biller")

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
		// Push: the creditor is the counterparty, so the request must name it — the
		// NAME and the BIC, neither derived. The DEBTOR's agent is named too, and is
		// not derived from the ref because a ref names no bank (see PartyRef).
		DebtorDetails:   PartyDetails{Agent: a.BIC, Name: "Alice"},
		CreditorDetails: PartyDetails{Agent: b.BIC, Name: "Bob"},
	}
}

// networkWithACollection is the direct-debit fixture: two addressed banks, a
// mandate the payee holds over the payer, and the collection request under it.
// fund is what the payer's account holds, so a caller can build a collectable
// one or not.
func networkWithACollection(t *testing.T, fund ledger.Amount) (*testSystem, InitiatePaymentRequest, MandateID) {
	t.Helper()
	ctx := context.Background()
	sys := testNetwork(t)
	a, err := storetest.Admit(ctx, sys.nets, "Bank A", testBIC, euroOnly)
	assertNoError(t, err)
	b, err := storetest.Admit(ctx, sys.nets, "Bank B", testBIC2, euroOnly)
	assertNoError(t, err)
	payer := openCustomer(t, ctx, a, "Alice")
	payee := openCustomer(t, ctx, b, "Biller")
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

// networkWithASubmittedPayment is a push payment submitted and RELAYED and
// nothing more: Initiated at the payer's bank and at the clearing house, its
// debtor leg posted, in no cycle, unanswered by the creditor's bank.
func networkWithASubmittedPayment(t *testing.T) (*testSystem, Payment) {
	t.Helper()
	ctx := context.Background()
	n, req := networkWithTwoBanks(t)
	p, err := n.submit(ctx, req)
	assertNoError(t, err)
	_, err = n.RecordRelayed(ctx, []InboundTransaction{{ID: p.ID, Request: relayedFrom(p)}})
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

// TestSubmitLeavesAPushPaymentInitiatedAndOutOfAnyCycle: Initiated is an
// observable state.
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
// the guard a submitting bank's door also makes.
func TestTheClearingHouseWillNotClearForANonMember(t *testing.T) {
	// build returns a network with one member and one bank the settlement agent has
	// answered and the clearing house has not admitted, the push request between them
	// in the caller's direction, and an open cycle. See admitWithoutTheRoster.
	build := func(t *testing.T, foundedPays bool) (*testSystem, InitiatePaymentRequest) {
		t.Helper()
		ctx := context.Background()
		sys := testNetwork(t)
		member, err := storetest.Admit(ctx, sys.nets, "Member Bank", testBIC, euroOnly)
		assertNoError(t, err)
		founded := admitWithoutTheRoster(t, ctx, sys, "Founded Bank", testBIC2)

		// The founded bank's customer is given an ARRANGED OVERDRAFT rather than a
		// deposit, because DepositTx refuses a bank the settlement agent holds no
		// account for — and an overdraft is how such a customer comes to have money.
		foundedAcct, err := founded.Deposit.OpenAccount(ctx, "Nora", testAsset,
			founded.ProductID, 100000)
		assertNoError(t, err)
		memberAcct := openCustomer(t, ctx, member, "Alice")
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
			// And the FOUNDED bank submits it, which the reversed refs alone do not say:
			// the debtor's agent selects the database the submission is made in.
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

			// The two banks' own halves run and are not what refuses: each checks its own
			// customer's account. Neither is asked about membership, and neither can be —
			// the roster is a third institution's row.
			p, err := sys.submit(ctx, req)
			assertNoError(t, err)
			assertNoError(t, sys.bank(receiverOf(sys, p)).AcceptInbound(ctx, p.ID, relayedFrom(p)))
			// The clearing house's own copy, which it must hold before it can take a
			// payment into a cycle, written from the instruction it relayed and asking
			// nothing about membership. The roster check is the ACCEPTANCE's, below.
			_, err = sys.RecordRelayed(ctx, []InboundTransaction{{ID: p.ID, Request: relayedFrom(p)}})
			assertNoError(t, err)

			_, err = sys.AcceptAtCSM(ctx, p.ID)
			if !errors.Is(err, ErrBankNotAdmitted) {
				t.Fatalf("AcceptAtCSM = %v, want ErrBankNotAdmitted", err)
			}
			if !strings.Contains(err.Error(), tc.role) {
				t.Errorf("the refusal says %q and does not name the %s", err, tc.role)
			}
			// RC01 on the wire, which is what makes the clearing house's rejection
			// an answer rather than a reported problem. reasonTable is what decides it.
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

// TestTheClearingHouseWillNotClearInAnAssetAMemberWasNotAdmittedIn is the
// second arm of the same guard, and what gives RosterEntry.Assets a reader —
// without one it would be the field-nothing-reads shape this repository
// refuses.
func TestTheClearingHouseWillNotClearInAnAssetAMemberWasNotAdmittedIn(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	sys.RegisterScheme(dollarPush{})
	bothAssets := []ledger.AssetCode{testAsset, "USD"}

	payer, err := storetest.Admit(ctx, sys.nets, "Member Bank", testBIC, bothAssets)
	assertNoError(t, err)

	// The half-admitted bank, built out of the acts rather than storetest.Admit:
	// founded in both assets, and the settlement agent asked for one. Nothing is
	// planted — this is provisioning's own sequence, stopped where a refusal stops it.
	half, err := sys.bank(testBIC2).FoundBank(ctx, "Half Bank", testBIC2, storetest.FixtureCountry, bothAssets)
	assertNoError(t, err)
	const ref = "half-admitted"
	member, issuer, err := sys.cb().OpenSettlementAccount(ctx, AdmissionRequest{
		Name: half.Name, BIC: half.BIC, Country: storetest.FixtureCountry, Asset: testAsset, Ref: ref,
	})
	assertNoError(t, err)
	ack := AdmissionAcknowledgement{BIC: half.BIC, Issuer: issuer, Accounts: member.Accounts, Ref: ref}
	_, err = sys.AdmitMember(ctx, ack)
	assertNoError(t, err)
	half, err = sys.bank(half.BIC).RecordMembership(ctx, ack)
	assertNoError(t, err)
	if half.Assets[testAsset].Settlement == "" {
		t.Fatal("the half-admitted bank holds no settlement account; this test needs it admitted in one asset")
	}
	entry, err := sys.GetRosterEntryByBIC(ctx, half.BIC)
	assertNoError(t, err)
	if !slices.Equal(entry.Assets, []ledger.AssetCode{testAsset}) {
		t.Fatalf("the roster entry clears in %v; this test needs it admitted in one asset only", entry.Assets)
	}

	// Opened through the register rather than through OpenCustomerAccount because
	// the asset is the scheme's and not the bank's default. Each comes out
	// addressed by its own bank, which is all this fixture needs of an address.
	payerAcct, err := payer.Deposit.OpenAccount(ctx, "Alice", "USD", payer.ProductID, 0)
	assertNoError(t, err)
	fundAccount(t, ctx, sys, payer, payerAcct, 100000)
	payeeAcct, err := half.Deposit.OpenAccount(ctx, "Nora", "USD", half.ProductID, 0)
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
	_, err = sys.RecordRelayed(ctx, []InboundTransaction{{ID: p.ID, Request: relayedFrom(p)}})
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
// the split is real: a submitting half that read the creditor's account would be
// one transaction reaching into two institutions' books.
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

	// And the check did not VANISH, it moved: the creditor's bank is what discovers
	// the account does not exist, which is the AC01 a receiving bank returns with. A
	// creditorSideTx tolerating a missing account passes the whole suite otherwise.
	if err := n.bank(receiverOf(n, p)).AcceptInbound(context.Background(), p.ID, relayedFrom(p)); !errors.Is(err, ErrAccountNotInParticipant) {
		t.Fatalf("AcceptInbound on an account the creditor's bank does not hold = %v, want ErrAccountNotInParticipant", err)
	}
}

// TestSubmitTakesTheCounterpartyNameFromTheRequest pins the direction rule: the
// submitting bank fills in its OWN side from its own register and is TOLD the
// counterparty's NAME.
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
	// n.submit routes on the agent the request claims for its own side, and the
	// point of this fixture is that that value is a lie — a fixture's problem, not
	// the domain's, since a real submission arrives AT a bank rather than naming
	// one.
	p, err := n.bank(submitter).SubmitPayment(ctx, req)
	if err != nil {
		t.Fatalf("SubmitPayment: %v", err)
	}
	if got := p.CreditorDetails.Name; got != "Whoever The Payer Typed" {
		t.Errorf("creditor name is %q, want the name the request carried", got)
	}
	// The debtor is this bank's own customer, so its name comes from its own register
	// and NOT from the request — a payer does not get to rename themselves. The
	// register value is "Alice", so this is exact rather than merely non-empty.
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
// what the message will need, rather than failing later when the message is
// built out of a bank's own register.
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

// TestSubmitRecordsAnAssertedAgentWhereThereIsNoAddressToDeriveFrom measures
// the door ErrCounterpartyAgentNotNamed keeps open.
func TestSubmitRecordsAnAssertedAgentWhereThereIsNoAddressToDeriveFrom(t *testing.T) {
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
			t.Errorf("creditor agent is %q, want the instruction's %q — an asserted agent is a fallback, not a correction", p.CreditorDetails.Agent, debtorBank.BIC)
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
			t.Errorf("debtor agent is %q, want the instruction's %q — an asserted agent is a fallback, not a correction", p.DebtorDetails.Agent, creditorBank.BIC)
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
// act does not make, which is why it is a test rather than a deletion. Nothing
// at submission looks the counterparty up at all.
func TestSubmitDoesNotCheckWhetherTheCounterpartysBankExists(t *testing.T) {
	n, req := networkWithTwoBanks(t)
	// A well-formed address nobody has founded. It has to be well formed: the FORMAT
	// is still refused here, because nothing can address a queue named by a string
	// that is not a BIC. What is not checked is whether anything answers to it.
	req.CreditorDetails.Agent = "NOSUCHBKXXX"

	if _, err := n.submit(context.Background(), req); err != nil {
		t.Errorf("SubmitPayment = %v, want it accepted — a submitting bank does not check the counterparty's registry", err)
	}
}

// TestSubmitRefusesItsOwnPartyAtNoSuchBank pins checkPartyTx's PARTICIPANT arm.
func TestSubmitRefusesItsOwnPartyAtNoSuchBank(t *testing.T) {
	n, req := networkWithTwoBanks(t)

	// Submitted at an address that has a DATABASE and no bank row in it — a
	// network for an institution never founded — which is the only shape "its own
	// party is at no such bank" can take.
	if _, err := n.bank("NOSUCHBKXXX").SubmitPayment(context.Background(), req); !errors.Is(err, ErrParticipantNotFound) {
		t.Errorf("got %v, want ErrParticipantNotFound", err)
	}
}

// TestAcceptInboundDoesNotRewriteEitherPartysDetails: debtorSideTx and
// creditorSideTx run from AcceptInboundTx as well as from SubmitPaymentTx, and
// there the bank executing them is the RECEIVING bank — the creditor's on a
// push, the debtor's on a pull.
func TestAcceptInboundDoesNotRewriteEitherPartysDetails(t *testing.T) {
	t.Run("push", func(t *testing.T) {
		ctx := context.Background()
		n, req := networkWithTwoBanks(t)
		creditorBank, err := n.bank(req.CreditorDetails.Agent).GetBank(ctx, ParticipantID(req.CreditorDetails.Agent))
		assertNoError(t, err)
		// Deliberately NOT "Bob" — the real name on the creditor's own register. If
		// AcceptInboundTx's creditorSideTx overwrote CreditorDetails from that
		// register, this would come back as "Bob".
		req.CreditorDetails = PartyDetails{Agent: creditorBank.BIC, Name: "Whoever The Payer Typed"}

		p, err := n.submit(ctx, req)
		assertNoError(t, err)
		if p.Creditor.Identifier != (deposit.Identifier{}) {
			t.Fatalf("the submitted payment already carries a creditor address (%+v), so AcceptInbound has nothing to change and this subtest can no longer fail; see the doc above", p.Creditor.Identifier)
		}
		assertNoError(t, n.bank(receiverOf(n, p)).AcceptInbound(ctx, p.ID, relayedFrom(p)))

		// The RECEIVING bank's copy, the row AcceptInbound wrote. The clearing house
		// has not been told about this payment at all — the fixture stops before
		// RecordRelayed.
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
		// Deliberately NOT "Alice" — the real name on the debtor's own register. If
		// AcceptInboundTx's debtorSideTx overwrote DebtorDetails from that register,
		// this would come back as "Alice".
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

// failingUpdateTx is a Tx whose PARTY lookups fail with an error of the
// caller's choosing; everything else is the real transaction, promoted by
// embedding.
type failingUpdateTx struct {
	BankTx
	participantErr error
	accountErr     error
}

func (t failingUpdateTx) GetBank(ctx context.Context, id ParticipantID) (Bank, error) {
	if t.participantErr != nil {
		return Bank{}, t.participantErr
	}
	return t.BankTx.GetBank(ctx, id)
}

func (t failingUpdateTx) GetDepositAccount(ctx context.Context, book ledger.BookID, id deposit.AccountID) (deposit.Account, error) {
	if t.accountErr != nil {
		return deposit.Account{}, t.accountErr
	}
	return t.BankTx.GetDepositAccount(ctx, book, id)
}

// failingUpdateStore wraps a real bank store and hands every UPDATE a
// failingUpdateTx. See message_test.go's failingStore for why a synthetic error
// at the seam is the only way to provoke a store failure on demand.
type failingUpdateStore struct {
	BankStore
	participantErr error
	accountErr     error
}

func (s failingUpdateStore) Update(ctx context.Context, fn func(context.Context, BankTx) error) error {
	return s.BankStore.Update(ctx, func(ctx context.Context, tx BankTx) error {
		return fn(ctx, failingUpdateTx{BankTx: tx, participantErr: s.participantErr, accountErr: s.accountErr})
	})
}

// TestAcceptInboundDoesNotBlameTheSenderForAStoreFailure is checkPartyTx's half
// of the property addressedPartyTx has pinned all along.
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

				// The same store, decorated only from here on: the payment had to be
				// submitted successfully for there to be one to answer.
				broken := NewBankNetwork(failingUpdateStore{
					BankStore:      n.bank(receiverOf(n, p)).Store(),
					participantErr: tc.participantErr,
					accountErr:     tc.accountErr,
				}, func() time.Time { return fixedTime }, ParticipantID(receiverOf(n, p)))

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

// TestAcceptInboundRefusesAPaymentThatIsNoLongerInitiated: a payment that has
// stopped being Initiated must not be revived by an answer in flight when it
// did.
func TestAcceptInboundRefusesAPaymentThatIsNoLongerInitiated(t *testing.T) {
	ctx := context.Background()
	n, p := networkWithASubmittedPayment(t)
	receiver := n.bank(receiverOf(n, p))

	// The receiving bank ANSWERS FIRST, and it has to. Its guard reads its OWN copy,
	// so a bank that has never seen the payment has no row to be wrong about and
	// writes a fresh one. What this test is about is the SECOND.
	assertNoError(t, receiver.AcceptInbound(ctx, p.ID, relayedFrom(p)))

	rejected, err := reject(ctx, n, p.ID, iso20022.StatusReasonDuplication, "cancelled by the payer")
	assertNoError(t, err)
	assertEqual(t, "status after rejection", rejected.Status, Rejected)

	// p is the copy submission returned: Initiated, with a debtor leg that has
	// since been reversed. Exactly what a bank's handler would still be
	// holding.
	if err := receiver.AcceptInbound(ctx, p.ID, relayedFrom(p)); !errors.Is(err, ErrInvalidStateTransition) {
		t.Fatalf("AcceptInbound on a rejected payment = %v, want ErrInvalidStateTransition", err)
	}

	assertEqual(t, "status after the late answer",
		mustGetPaymentAt(t, ctx, receiver, p.ID).Status, Rejected)
}

// TestAcceptInboundTakesOnAPayeeWhoseAccountHasClosed: a payee whose account is
// CLOSED is taken on rather than refused, and the money is diverted when the
// leg posts.
func TestAcceptInboundTakesOnAPayeeWhoseAccountHasClosed(t *testing.T) {
	ctx := context.Background()
	n, p := networkWithASubmittedPayment(t)
	closeCreditorAccount(t, n, p)
	receiver := n.bank(receiverOf(n, p))

	assertNoError(t, receiver.AcceptInbound(ctx, p.ID, relayedFrom(p)))
	assertEqual(t, "the receiving bank's own copy",
		mustGetPaymentAt(t, ctx, receiver, p.ID).Status, Initiated)
}

// TestMandateIsCheckedAtSubmissionAndFundsOnReceipt: the creditor's bank holds
// the mandate, so it validates it synchronously at submission; the debtor's
// bank validates funds asynchronously on receipt.
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

// TestRejectionIsTwoHalvesAndTheFirstHalfLeavesMoneyInSuspense: the CSM
// transitions the payment and drops it from its cycle, the debtor's bank
// reverses its own leg.
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
	// this reverses is on that copy and no other, because the posting was that
	// bank's.
	payer := n.bank(p.DebtorDetails.Agent)
	if err := payer.ReverseDebtorLeg(context.Background(), mustGetPaymentAt(t, context.Background(), payer, p.ID), "no such account"); err != nil {
		t.Fatalf("ReverseDebtorLeg: %v", err)
	}
	if got := suspenseBalance(t, n, p.DebtorDetails.Agent); got != before-p.Amount {
		t.Errorf("suspense = %d after the reversal, want %d", got, before-p.Amount)
	}
}

// TestRejectAtCSMDropsThePaymentFromItsCycle: the clearing house's own act, and the
// half of a rejection with consequences beyond the payment row — a rejected payment
// left in an open cycle would be closed and settled with it.
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

// TestAFailedRejectionAtABankLeavesThatBanksCopyUntouched: a failed reversal
// takes the PAYER'S BANK's whole half down and nothing else, because there is
// nothing else in that unit of work.
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

	// And the clearing house's decision stands, which is the half-happened outcome
	// named above: the pacs.002 is redelivered until the bank can act on it. Stated
	// so that a future unit of work quietly spanning both institutions fails this.
	assertEqual(t, "status at the clearing house",
		mustGetPaymentAt(t, ctx, n.ClearingHouseNetwork, p.ID).Status, Rejected)
}

// A collection the clearing house refuses before the payer's bank answered it took
// nothing from the payer. The pacs.002 still reaches that bank — it does not know
// whether it had answered — so the half has to be a clean no-op rather than error.
func TestReverseDebtorLegIsANoOpWhenNoLegWasPosted(t *testing.T) {
	ctx := context.Background()
	n, req := networkWithAMandate(t)
	p, err := n.submit(ctx, req)
	assertNoError(t, err)
	assertEqual(t, "debtor leg after submitting a collection", p.DebtorLegTx, "")
	// The clearing house's own copy, which it must be holding before it can
	// refuse one. See networkWithASubmittedPayment.
	_, err = n.RecordRelayed(ctx, []InboundTransaction{{ID: p.ID, Request: relayedFrom(p)}})
	assertNoError(t, err)

	_, err = n.RejectAtCSM(ctx, p.ID, iso20022.StatusReasonNoMandate, "no usable mandate")
	assertNoError(t, err)

	// Run at the PAYER'S BANK, on the payment as the submitting bank recorded it.
	// That bank has no row of its own and needs none: ReverseDebtorLegTx reads ID and
	// DebtorLegTx off the value it is handed and looks nothing up.
	payer := n.bank(p.DebtorDetails.Agent)
	bank := mustGetBank(t, ctx, n, ParticipantID(p.DebtorDetails.Agent))
	before := customerBalance(t, bank, p.Debtor.Account)

	if err := payer.ReverseDebtorLeg(ctx, p, "no usable mandate"); err != nil {
		t.Fatalf("ReverseDebtorLeg on a payment with no posted leg = %v, want a no-op", err)
	}
	assertEqual(t, "payer's balance", customerBalance(t, bank, p.Debtor.Account), before)
}

// Reversing the same leg twice would pay the payer twice, so the ledger refuses
// it rather than absorbing it: a redelivered pacs.002 fails loudly and reaches
// the day's report instead of quietly crediting the payer again.
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
		mustGetPaymentAt(t, ctx, n.ClearingHouseNetwork, p.ID).Status, Initiated)

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

// TestAcceptInboundIgnoresARedeliveredCollection: a queue redelivers, so the
// debtor's bank sees the same pacs.003 twice while the payment is still
// Initiated.
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
		t.Fatalf("redelivered collection = %v, want a no-op — a receiving bank would answer MS03 for a collection it accepted", err)
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

// TestSettlingAReturnReadsNoPayment: the settlement agent is handed an
// INSTRUCTION and moves reserves. It holds no payment rows, so a return it
// could only execute by looking one up is one it cannot execute.
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

	// Two statements, one per member, in the order the postings are made. Fixed
	// rather than incidental, for SettleCycleTx's reason: a caller sends these as
	// messages, and map iteration would put a different pair on the wire each run.
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

	// A redelivered instruction is refused rather than settled twice. The refusal
	// comes from the ledger's idempotency key — there is no row here recording that
	// this return was settled, the agent holding no payment rows.
	_, err = sys.cb().SettleReturn(ctx, in)
	assertError(t, err, ErrReturnAlreadySettled)
	assertEqual(t, "bank A reserve after the redelivery", reserveAt(t, sys, a), 100000)
	assertEqual(t, "bank B reserve after the redelivery", reserveAt(t, sys, b), 0)
}

// TestASettlementAgentRefusesAReturnItsCreditorBankCannotCover is the one
// decision the settlement agent makes on this path, and the same one
// SettleCycleTx makes at a cut-off: it will not take a member's reserve below
// zero to move somebody else's money.
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
// ReadReturn makes on the message, made again where the money is.
func TestASettlementAgentRefusesAReturnThatNamesNoPayment(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, _, _ := setupTwoBanks(t, sys)

	// Bank A is the CREDITOR's bank here and holds the reserves, so the funding check
	// cannot be what refuses. Written that way round deliberately: with the short bank
	// on that side the test passes against an agent with no id guard at all.
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

	// And nothing at all was written. Bob is not overdrawn, the clearing suspense is
	// flat, and no receivable was opened — the receivable is the PULL side's answer
	// and must not leak into this one.
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
// the same rule, and comes out the opposite way structurally.
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

	// THIS BANK's copy reaches Returned on this leg, which is position in the
	// conversation rather than a count of legs.
	assertEqual(t, "the payment's status at the clawing-back bank", got.Status, Returned)
	// And the payer's bank has not been told anything: its own copy is still
	// Settled, which is what "one leg of two" meant when there was one row.
	assertEqual(t, "the payment's status at the payer's bank",
		mustGetPaymentAt(t, ctx, sys.bank(a.BIC), pay.ID).Status, Settled)
	if got.ReturnClawbackTx == "" {
		t.Fatal("the clawback posted and left no transaction id on the payment; " +
			"the other leg has no way to know it is the second")
	}
	assertEqual(t, "the refund leg, which is the other bank's", got.ReturnRefundTx, "")
}

// TestAForcedClawbackOverdrawsAnOpenBillerRatherThanBookingAReceivable holds
// Returns Receivable to the one thing it is for.
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
func TestAPullRefundIsHonouredWhenOneBankIsBothParties(t *testing.T) {
	ctx := context.Background()
	sys := testNetwork(t)
	a, b, alice, bob := setupTwoBanks(t, sys)
	// A second customer of ALICE's bank. Both parties to the collection are then
	// bank A's, which is the whole fixture.
	biller := openCustomer(t, ctx, a, "Biller").ID
	pay := settledCollection(t, sys, a, alice, a, biller, 25000)

	// Spent OUT of the bank, to a customer of the other one, so the biller is empty
	// and bank A really is short: a transfer between two of its own accounts would
	// leave the money where the clawback could still reach it.
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

// returnTheWholeWay is returnWholePayment for a fixture that cannot carry on
// past a failure, and it fails the test rather than answering with an error.
func returnTheWholeWay(t *testing.T, sys *testSystem, p Payment, reason string) Payment {
	t.Helper()
	out, err := returnWholePayment(context.Background(), sys, p.ID, reason)
	assertNoError(t, err)
	return out
}

// TestARefundIntoAClosedPayersAccountGoesToUnclaimedBalances: a payer who
// empties and closes their account after a payment settles, whose payment is
// then returned, would otherwise end with the money in an account whose status
// is Closed — the withdrawal check answers "closed", the credit check answers
// "closed", and closing again is an invalid transition.
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
// on the guard above.
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

		broken := NewBankNetwork(failingUpdateStore{BankStore: sys.bank(a.BIC).Store(), accountErr: dropped},
			func() time.Time { return fixedTime }, a.ID)
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

		broken := NewBankNetwork(failingUpdateStore{BankStore: sys.bank(b.BIC).Store(), accountErr: dropped},
			func() time.Time { return fixedTime }, b.ID)
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

	// A bank that is neither side has no leg to unwind and does not get as far as
	// saying so: it holds no row for this payment, so its own store refuses before
	// ErrNotAPartyToThisReturn can be reached.
	c, err := storetest.Admit(ctx, sys.nets, "Bank C", "BANKFRPPXXX", euroOnly)
	assertNoError(t, err)
	assertError(t, sys.bank(c.BIC).ReverseReturnLeg(ctx, pay.ID, "AM04: not mine"), ErrPaymentNotFound)
}

// TestACompletedReturnCannotBeUnwound guards the unwind against the case that
// would cost real money.
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

// relayedFrom is the instruction the other two institutions would have been
// sent about a payment this fixture has just submitted.
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

// receiverOf is payment's rule over the clearing house's registry.
// AcceptInboundTx runs at the receiving bank, which resolves the counterparty in
// its OWN register and so has to be told which bank it is.
func receiverOf(n *testSystem, p Payment) iso20022.BIC {
	scheme, ok := n.Scheme(p.Scheme)
	if !ok {
		return p.CreditorDetails.Agent
	}
	return ReceiverOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent)
}

// submitterOfReq is the bank that SUBMITS a request. A request naming no
// submitting agent gets a BIC no fixture holds, so the failure names itself.
func submitterOfReq(n *testSystem, req InitiatePaymentRequest) iso20022.BIC {
	side := req.DebtorDetails.Agent
	if scheme, ok := n.Scheme(req.Scheme); ok {
		side = SubmitterOf(scheme, req.DebtorDetails.Agent, req.CreditorDetails.Agent)
	}
	if side == "" {
		return "THIS REQUEST NAMES NO SUBMITTING AGENT"
	}
	return side
}
