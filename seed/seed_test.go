package seed

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/payment/recon"
	"github.com/raphi011/cbs/store/testenv"
)

// testCentralBankBIC is the settlement agent every scenario here lodges cash
// with. It is real-shaped and is no bank's: an institution sharing an address
// with a seeded bank would be two institutions at one address.
const testCentralBankBIC iso20022.BIC = "CBSEDEFFXXX"

// testDeployment is the running system a scenario is built into, composed
// directly instead of carried.
//
// Populate's five acts are the ones needing more than one institution (see
// Deployment). Three of them need nothing else at all here: an admission writes
// no row, so it is the network's own to make; nothing is ever in flight, so
// there is nothing to drain; and the settlement agent's address is
// configuration. The two that move rows are composed the way every other
// composite in this package is — one institution's half, then the other's, each
// through its own network — which is why Populate's own doc calls the
// deployment a thing that must be RUNNING rather than a transport.
//
// What it leaves out is the hop. The instruction and the receipt are domain
// values on both sides of it, so nothing here marshals or parses an envelope,
// and a lodgement whose camt.050 could not be read is therefore not a failure
// this fixture can reach. That is the transport's own to prove, and it does.
type testDeployment struct {
	nets *payment.Networks
	now  func() time.Time
	seq  atomic.Uint64
}

func newTestDeployment(nets *payment.Networks, now func() time.Time) *testDeployment {
	return &testDeployment{nets: nets, now: now}
}

// AddBank writes nothing. A bank's rows are provision.Bank's and are already
// written by the time this is called; what a deployment adds on top is
// reachability, and everything below reaches a bank through its own network.
func (d *testDeployment) AddBank(context.Context, *payment.Bank) error { return nil }

// Drain has nothing to wait for: every act here finishes before it returns.
func (d *testDeployment) Drain(context.Context) error { return nil }

func (d *testDeployment) CentralBankBIC() iso20022.BIC { return testCentralBankBIC }

// RefreshDirectory reads the roster at the clearing house and writes the copy at
// the subscriber, in that order and in two units of work — which is the whole of
// what the real one does, because a directory is a file delivered and not a
// message.
func (d *testDeployment) RefreshDirectory(ctx context.Context, bic iso20022.BIC) ([]payment.DirectoryEntry, error) {
	published, err := d.nets.ClearingHouse().ListRosterEntries(ctx)
	if err != nil {
		return nil, err
	}
	subscriber, err := d.nets.Bank(ctx, payment.ParticipantID(bic))
	if err != nil {
		return nil, err
	}
	return subscriber.RefreshDirectory(ctx, published)
}

// Lodge is the member's half and then the settlement agent's, each in its own
// unit of work, which is the property TestALodgementIsTwoBooksInTwoUnitsOfWork
// holds the real one to.
//
// The receipt is discarded because accepting one costs the member nothing: the
// mirror leg was posted by the first half, and the second half either credits
// the reserve or refuses — and a refusal comes back as an error here rather
// than as a receipt nobody reads.
func (d *testDeployment) Lodge(ctx context.Context, bic iso20022.BIC, asset ledger.AssetCode,
	amount ledger.Amount) (payment.LodgementInstruction, error) {

	member, err := d.nets.Bank(ctx, payment.ParticipantID(bic))
	if err != nil {
		return payment.LodgementInstruction{}, err
	}
	in, _, err := member.LodgeReserves(ctx, asset, amount, payment.MessageContext{
		From:  bic,
		To:    testCentralBankBIC,
		MsgID: fmt.Sprintf("%s-%d", bic, d.seq.Add(1)),
		Now:   d.now(),
	})
	if err != nil {
		return payment.LodgementInstruction{}, err
	}
	if _, err := d.nets.CentralBank().ReceiveLodgement(ctx, in); err != nil {
		return in, err
	}
	return in, nil
}

// testNetwork builds the sample scenario over the store testenv hands it.
//
// It is what makes the seed assertions (deterministic IDs, conserved reserves,
// status coverage) claims about the seed rather than about a store, and it is
// the whole of what a caller of this package assembles for itself: a store, a
// set of networks, a deployment, and Populate over the three.
//
// testNets is what a seed fixture holds: the clearing house's view for the reads
// these tests make, plus the factory Populate and the deployment both take. See
// payment.Networks.
type testNets struct {
	*payment.Network
	nets *payment.Networks
	// stores is the set the networks are minted over, for the one question no
	// institution can answer: which banks exist. See listParticipants.
	stores payment.Stores
}

// cb is the settlement agent's view, which is the only one that can be asked
// what a member's reserve balance is.
func (n testNets) cb() *payment.Network { return n.nets.CentralBank() }

// bank is one member bank's own view, over that bank's own database.
//
// It panics on a failure to open rather than taking a *testing.T, for the reason
// seed's own builder.bank does: every bank a fixture here names is one Populate
// founded moments ago in this process.
func (n testNets) bank(pid payment.ParticipantID) *payment.Network {
	net, err := n.nets.Bank(context.Background(), pid)
	if err != nil {
		panic("seed_test: opening " + string(pid) + "'s store: " + err.Error())
	}
	return net
}

func testNetwork(t *testing.T) testNets {
	t.Helper()
	net, _ := testNetworkAndClock(t)
	return net
}

// testNetworkAndClock is testNetwork for the tests that also need to ask what
// day it is, which after the catalogue is any test resolving a price.
func testNetworkAndClock(t *testing.T) (testNets, *Dataset) {
	t.Helper()
	d := New()
	stores := testenv.NewSet(t, d.Now)
	nets := payment.NewNetworks(stores, d.Now)
	if err := d.Populate(context.Background(), nets, newTestDeployment(nets, d.Now)); err != nil {
		t.Fatalf("populate: %v", err)
	}
	return testNets{Network: nets.ClearingHouse(), nets: nets, stores: stores}, d
}

func TestNetworkShape(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	if got := len(listParticipants(t, ctx, net)); got != 4 {
		t.Fatalf("participants = %d, want 4", got)
	}
	// Three mandates across the seed, and each is at its CREDITOR's bank: a
	// mandate is that bank's row and no listing spans them. Counting per bank is
	// what the move to the bank's surface makes this test mean.
	mandates := 0
	for _, p := range listParticipants(t, ctx, net) {
		mine, err := net.bank(p.ID).ListMandates(ctx)
		if err != nil {
			t.Fatalf("list %s's mandates: %v", p.ID, err)
		}
		mandates += len(mine)
	}
	if mandates != 3 {
		t.Fatalf("mandates = %d, want 3", mandates)
	}
	cycles, err := net.ListCycles(ctx)
	if err != nil {
		t.Fatalf("list cycles: %v", err)
	}
	if got := len(cycles); got != 5 {
		t.Fatalf("cycles = %d, want 5", got)
	}
	// The SETTLEMENT AGENT's rows: a settlement is what that institution did in
	// its own book, and the clearing house's shape has no table for one.
	settlements, err := net.cb().ListSettlements(ctx)
	if err != nil {
		t.Fatalf("list settlements: %v", err)
	}
	if got := len(settlements); got != 2 {
		t.Fatalf("settlements = %d, want 2", got)
	}

	// Deposit accounts: 5 + 3 + 2 + 2 = 12 across the four banks.
	total := 0
	for _, p := range listParticipants(t, ctx, net) {
		accts, err := p.Deposit.ListAccounts(ctx)
		if err != nil {
			t.Fatalf("list deposit accounts: %v", err)
		}
		total += len(accts)
	}
	if total != 12 {
		t.Fatalf("deposit accounts = %d, want 12", total)
	}
}

func TestPaymentStatusCoverage(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	payments, err := net.ListPayments(ctx)
	if err != nil {
		t.Fatalf("list payments: %v", err)
	}
	if got := len(payments); got != 11 {
		t.Fatalf("total payments = %d, want 11", got)
	}
	byStatus := map[payment.PaymentStatus]int{}
	for _, p := range payments {
		byStatus[p.Status]++
	}
	want := map[payment.PaymentStatus]int{
		payment.Settled:  4,
		payment.Returned: 1,
		payment.Cleared:  2,
		payment.Accepted: 3,
		payment.Rejected: 1,
	}
	for status, n := range want {
		if byStatus[status] != n {
			t.Errorf("status %v = %d, want %d", status, byStatus[status], n)
		}
	}
}

// TestTheSeededNetworkReconciles holds all six of this scenario's databases
// against each other at once.
//
// It is the seed's counterpart to TestReservesConserved and it asks a
// strictly larger question. That one sums the reserves the SETTLEMENT AGENT
// holds and checks the total against what was funded, which is one
// institution's book agreeing with itself; this one holds every member's own
// Reserve at Central Bank against the agent's liability to it, every member's
// clearing suspense against what is still owed to that member, the clearing
// house's cycles against the agent's settlements, and each of the three
// institutions' copy of a payment against the other two. Not one of those is a
// question any actor in this system may ask — see payment/recon.
//
// The seed is the right subject for it because it is the widest deployment this
// repository builds: four banks, eleven payments in five statuses, five cycles
// in three, two settlements and a return. Every earlier assertion in this file
// is about one of those in isolation.
//
// The unreconciled positions are asserted rather than ignored, and that is the
// point of the second half. This scenario deliberately ends with payments in
// flight — three Accepted and two Cleared — so a bank's suspense NOT returning
// to zero is the correct outcome and a harness that demanded zero would be
// measuring the wrong thing. What must be true is that every non-zero suspense
// has something outstanding against it, which is what Check enforces, and that
// it is the banks holding the in-flight payments that have one.
func TestTheSeededNetworkReconciles(t *testing.T) {
	net := testNetwork(t)

	report := recon.Check(t, net.nets)

	if len(report.Unreconciled) == 0 {
		t.Fatal("no unreconciled position anywhere in the seed; this scenario ends with five payments in flight, " +
			"so a suspense that has returned to zero everywhere means the harness is not reading the suspense accounts")
	}
	for _, u := range report.Unreconciled {
		if len(u.InFlight) == 0 && len(u.Unbooked) == 0 {
			t.Errorf("%s (%s) holds %d in clearing suspense with nothing outstanding; Check should have reported that as a break",
				u.Bank, u.Asset, u.Suspense)
		}
		t.Logf("unreconciled: %s (%s) suspense %d, %d in flight, unbooked %v",
			u.Bank, u.Asset, u.Suspense, len(u.InFlight), u.Unbooked)
	}
}

// TestABanksOwnRunAgreesWithTheHarness calibrates the narrow instrument against
// the wide one, over the same six databases the test above reads.
//
// There are two instruments now and they answer the same question from different
// numbers of databases. payment/recon opens all of them at once, precisely
// because no institution may; payment.Network.Reconcile is one member bank over
// its own, which is an act that bank performs. This holds the second against the
// first.
//
// # The agreement can only ever hold in one direction
//
// A bank sees a subset. So what is asserted is that NO BREAK A BANK REPORTS IS
// ABSENT FROM THE HARNESS'S REPORT — never the converse, which is false by
// construction: the harness catches a member's advice row against the AGENT's
// register, and a bank holding no such register cannot. Over this scenario the
// harness reports no break at all, so the containment says every one of the four
// banks reconciles from inside, across eleven payments in five statuses, two
// settlements and a return. A false positive in the narrow instrument fails here.
//
// # The positions agree in BOTH directions, and that is the stronger half
//
// A clearing suspense that has not returned to zero is the one finding both
// instruments read off the same account — the harness out of the bank's book, the
// bank out of its own — so the two must name the same banks. This scenario ends
// with five payments in flight deliberately, which is what makes that assertion
// have something to bite on.
func TestABanksOwnRunAgreesWithTheHarness(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	// Check fails the test on any break of its own, which is what makes the
	// containment below a statement about the narrow instrument rather than about
	// this scenario.
	harness := recon.Check(t, net.nets)

	type position struct {
		bank  iso20022.BIC
		asset ledger.AssetCode
	}
	byBank := map[position]bool{}
	for _, p := range listParticipants(t, ctx, net) {
		for asset := range p.Assets {
			rec, err := net.bank(p.ID).Reconcile(ctx, asset)
			if err != nil {
				t.Fatalf("%s reconciling its own books in %s: %v", p.BIC, asset, err)
			}
			for _, b := range rec.Breaks {
				t.Errorf("%s (%s) reports a break the harness over all six databases does not: %s",
					p.BIC, asset, b)
			}
			if len(rec.Positions) > 0 {
				byBank[position{p.BIC, asset}] = true
			}
		}
	}

	if len(byBank) == 0 {
		t.Fatal("no bank reports a position of its own; this scenario ends with five payments in flight, " +
			"so a run that finds nothing in transit anywhere means it is not reading the suspense accounts")
	}
	for _, u := range harness.Unreconciled {
		if !byBank[position{u.Bank, u.Asset}] {
			t.Errorf("the harness reports %s (%s) holding %d in clearing suspense and that bank's own run reports nothing in transit",
				u.Bank, u.Asset, u.Suspense)
		}
		delete(byBank, position{u.Bank, u.Asset})
	}
	for p := range byBank {
		t.Errorf("%s (%s) reports money in transit and the harness reads its suspense as zero",
			p.bank, p.asset)
	}
}

// TestRejectedCollectionWasReversedInThePayersBank pins the second half of the
// seed's one rejection. build() composes both halves itself — the clearing
// house transitions the payment, the payer's bank reverses the leg it posted —
// and only the first is visible in the payment row every other seed assertion
// reads. Without this, the reversal could vanish from the seed and the sample
// data would show a Rejected collection whose payer never got their money back.
func TestRejectedCollectionWasReversedInThePayersBank(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	payments, err := net.ListPayments(ctx)
	if err != nil {
		t.Fatalf("list payments: %v", err)
	}
	var rejected payment.Payment
	for _, p := range payments {
		if p.Status == payment.Rejected {
			rejected = p
		}
	}
	if rejected.ID == "" {
		t.Fatal("no rejected payment in the seed data")
	}
	// The leg is on the PAYER'S BANK's copy and on no other. The clearing house's
	// row — which is what ListPayments above returns — has no leg columns at all,
	// so reading DebtorLegTx off it would report a fixture that does not cover a
	// reversal when the reversal is there all along.
	payerBIC := payment.ParticipantID(rejected.DebtorDetails.Agent)
	atPayer, err := net.bank(payerBIC).GetPayment(ctx, rejected.ID)
	if err != nil {
		t.Fatalf("the payer's bank's copy: %v", err)
	}
	if atPayer.DebtorLegTx == "" {
		t.Fatal("the rejected collection has no debtor leg; the fixture no longer covers a reversal")
	}

	bank, err := net.bank(payerBIC).GetBank(ctx, payerBIC)
	if err != nil {
		t.Fatalf("get participant: %v", err)
	}
	leg, err := bank.Ledger.GetTransaction(ctx, atPayer.DebtorLegTx)
	if err != nil {
		t.Fatalf("get the debtor leg: %v", err)
	}
	if leg.Status != ledger.Reversed {
		t.Errorf("the rejected collection's debtor leg is %v, want Reversed — the payer's money is still in suspense", leg.Status)
	}
}

// TestSeedRejectLeavesThePayersBankUntouchedWhenItsHalfFails pins the shape of
// the seed's composite, not just its result.
//
// # It was TestSeedRejectIsOneUnitOfWork, and that unit of work is gone
//
// The two halves cannot run on ONE transaction: that would span the clearing
// house and a bank, and a unit of work is ONE DATABASE's. b.reject is three of
// them — the decision, and each bank recording it on its own copy — so a
// reversal that fails leaves the clearing house's transition standing, which is
// a dataset this seed can build.
//
// So the half-happened state RejectAtCSMTx names is reachable here, as it always
// was in the mesh, and the guarantee that replaces it is the one a single
// institution can still make: RejectAtBankTx transitions THIS bank's copy and
// reverses THIS bank's leg together, so a bank that cannot give the money back
// does not record the rejection either. That is the inconsistency that would
// cost real money; the clearing house's decision standing while a bank has not
// acted is a message waiting to be redelivered.
//
// The forced failure is a leg that has already been reversed, which is what a
// retried rejection produces. b.reject reports it the way the whole builder
// does, by panicking with a seedErr, so the call goes through recoverBuild.
func TestSeedRejectLeavesThePayersBankUntouchedWhenItsHalfFails(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	// Any Accepted payment: it has a posted debtor leg and the CSM's half
	// takes it, exactly as the one the seed itself rejects. Accepted is the
	// CLEARING HOUSE's status, and the leg is the payer's bank's fact, so the
	// two come off two copies.
	payments, err := net.ListPayments(ctx)
	if err != nil {
		t.Fatalf("list payments: %v", err)
	}
	var target payment.Payment
	var payer *payment.Network
	for _, p := range payments {
		if p.Status != payment.Accepted {
			continue
		}
		at := net.bank(payment.ParticipantID(p.DebtorDetails.Agent))
		mine, err := at.GetPayment(ctx, p.ID)
		if err != nil {
			t.Fatalf("the payer's bank's copy of %s: %v", p.ID, err)
		}
		if mine.DebtorLegTx != "" {
			target, payer = mine, at
		}
	}
	if target.ID == "" {
		t.Fatal("no accepted payment with a posted leg in the seed data")
	}
	if err := payer.ReverseDebtorLeg(ctx, target, "reversed already"); err != nil {
		t.Fatalf("reverse the leg out from under the composite: %v", err)
	}

	b := &builder{ctx: ctx, nets: net.nets}
	err = rejectErr(b, target.ID, iso20022.StatusReasonDuplication, "duplicate instruction")
	if !errors.Is(err, ledger.ErrTransactionAlreadyReversed) {
		t.Fatalf("reject = %v, want ErrTransactionAlreadyReversed", err)
	}

	// The payer's bank recorded nothing.
	after, err := payer.GetPayment(ctx, target.ID)
	if err != nil {
		t.Fatalf("get payment: %v", err)
	}
	if after.Status == payment.Rejected {
		t.Error("the payer's bank recorded a rejection whose reversal it could not post")
	}
	if after.RejectReason != "" {
		t.Errorf("reject reason at the payer's bank = %q, want empty", after.RejectReason)
	}

	// And the clearing house's decision stands, which is the half-happened
	// outcome named above. It is stated so that a future unit of work quietly
	// spanning both institutions would fail this test.
	atCSM, err := net.GetPayment(ctx, target.ID)
	if err != nil {
		t.Fatalf("the clearing house's copy: %v", err)
	}
	if atCSM.Status != payment.Rejected {
		t.Errorf("status at the clearing house = %v, want Rejected", atCSM.Status)
	}
}

// rejectErr runs b.reject and returns the error it would otherwise panic with,
// the way Populate does.
func rejectErr(b *builder, id payment.PaymentID, code iso20022.StatusReason, reason string) (err error) {
	defer func() {
		if e := recoverBuild(recover()); e != nil {
			err = e
		}
	}()
	b.reject(id, code, reason)
	return nil
}

func TestAccountStatusCoverage(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	seen := map[deposit.AccountStatus]bool{}
	for _, p := range listParticipants(t, ctx, net) {
		accts, err := p.Deposit.ListAccounts(ctx)
		if err != nil {
			t.Fatalf("list deposit accounts: %v", err)
		}
		for _, a := range accts {
			seen[a.Status] = true
		}
	}
	for _, st := range []deposit.AccountStatus{deposit.Active, deposit.Dormant, deposit.Frozen, deposit.Closed} {
		if !seen[st] {
			t.Errorf("missing account status %v in seed data", st)
		}
	}
}

func TestReservesConserved(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	var sum int64
	for _, p := range listParticipants(t, ctx, net) {
		bal, err := net.cb().ReserveBalance(ctx, p.BIC, "EUR")
		if err != nil {
			t.Fatal(err)
		}
		if bal < 0 {
			t.Errorf("participant %s reserve negative: %d", p.ID, bal)
		}
		sum += int64(bal)
	}
	// Total reserves equal total funded; settlements and returns only move
	// reserves between participants.
	const wantFunded = 1_120_000
	if sum != wantFunded {
		t.Fatalf("sum of reserves = %d, want %d", sum, wantFunded)
	}
}

// TestBrunoOverdraftRepricing pins the one figure seed.go's comments argue for
// at length: Bruno's overdraft ends the build with three terms rows (opening,
// 15%, 18%) and a final accrued interest blending both rates. 487 (EUR 4.87)
// is derived, not read off a run: 15% ACT/365 on EUR 200.00 for the days up to
// the repricing's effective date, 18% from it, per the arithmetic walked
// through in the comment above lendingShowcase's SetOverdraftTerms call for
// the repricing. Without this, a change to Bella's 30-day span or the
// repricing's twenty-day offset would rot that comment silently.
func TestBrunoOverdraftRepricing(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	var verde *payment.Bank
	for _, p := range listParticipants(t, ctx, net) {
		if p.Name == "Banca Verde" {
			verde = p
		}
	}
	if verde == nil {
		t.Fatal("Banca Verde not found")
	}

	accts, err := verde.Deposit.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("list deposit accounts: %v", err)
	}
	var bruno *deposit.Account
	for i := range accts {
		if accts[i].Name == "Bruno Bianchi" {
			bruno = &accts[i]
		}
	}
	if bruno == nil {
		t.Fatal("Bruno Bianchi not found")
	}

	if got := bruno.Accrued.Minor(); got != 487 {
		t.Errorf("Bruno's accrued interest = %d, want 487 (EUR 4.87)", got)
	}

	rows, err := verde.Deposit.OverdraftTermsHistory(ctx, bruno.ID)
	if err != nil {
		t.Fatalf("overdraft terms history: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("terms rows = %d, want 3 (opening, 15%%, 18%%)", len(rows))
	}
	last := rows[2]
	wantEffective := ledger.DayStart(last.CreatedAt.AddDate(0, 0, -20))
	if !last.EffectiveFrom.Equal(wantEffective) {
		t.Errorf("last row EffectiveFrom = %v, want %v (twenty days before its CreatedAt %v)",
			last.EffectiveFrom, wantEffective, last.CreatedAt)
	}
}

// The seeded data holds the catalogue's three pricing cases side by side, so a
// reader can see them in the web app without writing a test: an account
// floating with its product, one whose negotiated overlay outranks it, and one
// migrated onto another product.
//
// The floating account's rate is asserted to EQUAL the product's version in
// force today rather than a hardcoded number. That is the claim worth pinning —
// the account tracks the product, with no per-account write — and it stays true
// if the story's length or the reprice's offset ever moves. The reprice itself
// is pinned separately, on the timeline, where a hardcoded figure means
// something.
func TestSeededCatalogueShowsAllThreePricingCases(t *testing.T) {
	ctx := context.Background()
	net, data := testNetworkAndClock(t)

	var verde *payment.Bank
	for _, p := range listParticipants(t, ctx, net) {
		if p.Name == "Banca Verde" {
			verde = p
		}
	}
	if verde == nil {
		t.Fatal("Banca Verde not found")
	}

	accts, err := verde.Deposit.ListAccountsWithTerms(ctx)
	if err != nil {
		t.Fatalf("list deposit accounts: %v", err)
	}
	byName := map[string]deposit.AccountWithTerms{}
	for _, a := range accts {
		byName[a.Account.Name] = a
	}

	// The day-30 reprice is really on Basic's timeline, published and at 14.9%.
	versions, err := verde.Catalogue.Versions(ctx, verde.ProductID)
	if err != nil {
		t.Fatalf("product versions: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("Basic has %d versions, want 3 (interest-free at onboarding, 12%%, 14.9%%)", len(versions))
	}
	repriced := versions[2]
	if !repriced.Published() {
		t.Error("the day-30 reprice was never published")
	}
	if got := int64(repriced.Overdraft.Rate); got != 149_000 {
		t.Errorf("the reprice = %d, want 149000 (14.9%%)", got)
	}
	inForce, err := verde.Catalogue.VersionInForce(ctx, verde.ProductID, ledger.DayStart(data.Now()))
	if err != nil {
		t.Fatalf("version in force: %v", err)
	}

	// Floating: Bianca was never negotiated with and never migrated, so she is
	// priced by whatever Basic costs today.
	bianca, ok := byName["Bianca Belli"]
	if !ok {
		t.Fatal("Bianca Belli not found")
	}
	if bianca.Terms.Negotiated {
		t.Error("Bianca has an overlay; she is meant to float")
	}
	if got, want := string(bianca.Terms.ProductID), string(verde.ProductID); got != want {
		t.Errorf("Bianca's product = %v, want %v", got, want)
	}
	if got, want := int64(bianca.Terms.Pricing.Rate), int64(inForce.Overdraft.Rate); got != want {
		t.Errorf("Bianca floats with her product = %v, want %v", got, want)
	}

	// Negotiated: Bruno's own rate outranks the product, and the day-30 reprice
	// did not move him. That is the distinction the overlay exists for.
	bruno, ok := byName["Bruno Bianchi"]
	if !ok {
		t.Fatal("Bruno Bianchi not found")
	}
	if !bruno.Terms.Negotiated {
		t.Fatal("Bruno's rate is not negotiated")
	}
	if got, want := int64(bruno.Terms.Pricing.Rate), int64(180_000); got != want {
		t.Errorf("Bruno's negotiated rate = %v, want %v", got, want)
	}
	if bruno.Terms.Pricing.Rate == inForce.Overdraft.Rate {
		t.Error("Bruno's negotiated rate matches the product's; the reprice moved him")
	}

	// Migrated: Bella is on Premium, and her earlier days still price at Basic
	// — a migration is a forward-dated row, not a rewrite.
	bella, ok := byName["Bella Bruno"]
	if !ok {
		t.Fatal("Bella Bruno not found")
	}
	if got, want := int64(bella.Terms.Pricing.Rate), int64(70_000); got != want {
		t.Errorf("Bella's rate = %v, want %v", got, want)
	}
	if bella.Terms.ProductID == verde.ProductID {
		t.Error("Bella was not migrated off Basic")
	}
	rows, err := verde.Deposit.OverdraftTermsHistory(ctx, bella.Account.ID)
	if err != nil {
		t.Fatalf("Bella's terms history: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("Bella has %d terms rows, want 2 (opening, migration)", len(rows))
	}
	if got, want := string(rows[0].ProductID), string(verde.ProductID); got != want {
		t.Errorf("Bella opened on Basic = %v, want %v", got, want)
	}
	if rows[0].ProductID == rows[1].ProductID {
		t.Error("the migration row names the same product as the opening one")
	}
}

func TestDeterministicIDs(t *testing.T) {
	ctx := context.Background()
	a := testNetwork(t)
	b := testNetwork(t)

	pa, pb := listParticipants(t, ctx, a), listParticipants(t, ctx, b)
	if len(pa) != len(pb) {
		t.Fatalf("participant counts differ: %d vs %d", len(pa), len(pb))
	}
	for i := range pa {
		if pa[i].ID != pb[i].ID || pa[i].Name != pb[i].Name {
			t.Fatalf("participant %d differs: %v/%v vs %v/%v", i, pa[i].ID, pa[i].Name, pb[i].ID, pb[i].Name)
		}
	}

	xa, xb := listPayments(t, ctx, a), listPayments(t, ctx, b)
	if len(xa) != len(xb) {
		t.Fatalf("payment counts differ: %d vs %d", len(xa), len(xb))
	}
	for i := range xa {
		if xa[i].ID != xb[i].ID || xa[i].Status != xb[i].Status || xa[i].Amount != xb[i].Amount {
			t.Fatalf("payment %d differs across builds", i)
		}
	}
}

func TestClockWentLive(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	first := listParticipants(t, ctx, net)[0]
	accts, err := first.Deposit.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("list deposit accounts: %v", err)
	}
	if len(accts) == 0 {
		t.Fatal("first participant has no accounts")
	}
	ref := payment.PartyRef{Account: accts[0].ID}

	// A mutation after build must be timestamped in real time, not at baseDate.
	m, err := net.bank(first.ID).CreateMandate(ctx, first.BIC, ref, ref, 0)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(m.CreatedAt) > time.Minute {
		t.Fatalf("mandate CreatedAt = %v, expected ~now (clock did not go live)", m.CreatedAt)
	}
}

// listParticipants and listPayments keep the ctx/error plumbing out of the
// assertions above.
//
// listParticipants goes through the STORES rather than through an institution,
// and that is the shape of the question rather than a workaround. The clearing
// house holds no banks table, and the roster it does hold names addresses and
// says nothing about a bank that was founded and never admitted. "Which banks
// exist" is the composition root's question and no institution has it. See
// payment's allBanks and auditReaders.
func listParticipants(t *testing.T, ctx context.Context, net testNets) []*payment.Bank {
	t.Helper()
	bics, err := net.stores.Banks(ctx)
	if err != nil {
		t.Fatalf("list participants: %v", err)
	}
	out := make([]*payment.Bank, 0, len(bics))
	for _, bic := range bics {
		b, err := net.bank(payment.ParticipantID(bic)).GetBank(ctx, payment.ParticipantID(bic))
		if err != nil {
			t.Fatalf("reading %s's own row: %v", bic, err)
		}
		out = append(out, b)
	}
	return out
}

func listPayments(t *testing.T, ctx context.Context, net testNets) []payment.Payment {
	t.Helper()
	payments, err := net.ListPayments(ctx)
	if err != nil {
		t.Fatalf("list payments: %v", err)
	}
	return payments
}

// Populate must be safe to call again. The server calls it on every boot, and
// against a store that outlives the process an unconditional seed would stack a
// second copy of the scenario on top of the first.
func TestPopulateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	d := New()
	stores := testenv.NewSet(t, d.Now)
	nets := payment.NewNetworks(stores, d.Now)
	net := testNets{Network: nets.ClearingHouse(), nets: nets, stores: stores}
	dep := newTestDeployment(nets, d.Now)

	if err := d.Populate(ctx, nets, dep); err != nil {
		t.Fatalf("first Populate: %v", err)
	}
	participants, payments := listParticipants(t, ctx, net), listPayments(t, ctx, net)

	if err := d.Populate(ctx, nets, dep); err != nil {
		t.Fatalf("second Populate: %v", err)
	}
	if got := len(listParticipants(t, ctx, net)); got != len(participants) {
		t.Fatalf("participants after reseeding = %d, want %d", got, len(participants))
	}
	if got := len(listPayments(t, ctx, net)); got != len(payments) {
		t.Fatalf("payments after reseeding = %d, want %d", got, len(payments))
	}
	assertClockIsLive(t, d, "after a second Populate on the same Dataset")

	// The case the idempotent skip exists for: a second process opening a store
	// that outlived the first. Its Dataset is brand new, so its clock starts
	// frozen at baseDate and Populate builds nothing — and if the skip returned
	// without releasing the clock, everything this process went on to write
	// would be timestamped 2025-09-15.
	second := New()
	secondNets := payment.NewNetworks(stores, second.Now)
	secondNet := testNets{Network: secondNets.ClearingHouse(), nets: secondNets, stores: stores}
	if err := second.Populate(ctx, secondNets, newTestDeployment(secondNets, second.Now)); err != nil {
		t.Fatalf("Populate from a second process: %v", err)
	}
	if got := len(listParticipants(t, ctx, secondNet)); got != len(participants) {
		t.Fatalf("participants seen by the second process = %d, want %d", got, len(participants))
	}
	assertClockIsLive(t, second, "after an idempotent skip in a second process")

	// And the observable consequence, not just the clock reading: a row written
	// after the skip must carry a live timestamp.
	acct, err := listParticipants(t, ctx, secondNet)[0].OpenCustomerAccount(ctx, "Opened after the skip", "EUR")
	if err != nil {
		t.Fatalf("open account after the skip: %v", err)
	}
	if age := time.Since(acct.CreatedAt); age > time.Minute {
		t.Fatalf("account opened after the skip is dated %v (%v ago), expected ~now", acct.CreatedAt, age)
	}
}

// assertClockIsLive checks that a Dataset's clock has been released to real
// time rather than left frozen at baseDate.
func assertClockIsLive(t *testing.T, d *Dataset, when string) {
	t.Helper()
	if age := time.Since(d.Now()); age > time.Minute {
		t.Fatalf("clock %s reads %v (%v ago), expected ~now — the seed clock never went live", when, d.Now(), age)
	}
}

// Populate recovers the builder's own must/check panic and nothing else. A nil
// dereference in payment, deposit, ledger or the store is a bug: flattening it
// into a seed error would return it as a 500 with the stack thrown away.
func TestRecoverBuildOnlyCatchesSeedErrors(t *testing.T) {
	if err := recoverBuild(nil); err != nil {
		t.Fatalf("recoverBuild(nil) = %v, want nil", err)
	}

	boom := errors.New("boom")
	err := recoverBuild(seedErr{boom})
	if err == nil {
		t.Fatal("recoverBuild did not convert a seedErr into an error")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("recoverBuild(seedErr) = %v, want it to wrap %v", err, boom)
	}
	if !strings.HasPrefix(err.Error(), "seed: ") {
		t.Fatalf("recoverBuild(seedErr) = %q, want it prefixed with \"seed: \"", err.Error())
	}

	// Anything else keeps going, with its original value.
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("recoverBuild swallowed a panic that was not the builder's")
			}
			if r != "a runtime bug" {
				t.Fatalf("re-panicked with %v, want the original value", r)
			}
		}()
		_ = recoverBuild("a runtime bug")
	}()
}

// must and check are what recoverBuild recognises, so the panic value they
// raise is part of the contract rather than an implementation detail.
func TestMustAndCheckPanicWithSeedErr(t *testing.T) {
	boom := errors.New("boom")

	cases := map[string]func(){
		"check": func() { check(boom) },
		"must":  func() { must("", boom) },
	}
	for name, fn := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("%s did not panic", name)
				}
				se, ok := r.(seedErr)
				if !ok {
					t.Fatalf("%s panicked with %T, want seedErr", name, r)
				}
				if !errors.Is(se, boom) {
					t.Fatalf("%s panicked with %v, want it to wrap %v", name, se, boom)
				}
			}()
			fn()
		})
	}
}

// A reset must restore the dataset, not a version of it shifted forward in
// time. Populate rewinds its clock, so the second build reproduces the first
// exactly — IDs, statuses, amounts and booking dates.
func TestPopulateAfterResetRebuildsTheSameDataset(t *testing.T) {
	ctx := context.Background()
	d := New()
	stores := testenv.NewSet(t, d.Now)
	nets := payment.NewNetworks(stores, d.Now)
	net := testNets{Network: nets.ClearingHouse(), nets: nets, stores: stores}
	dep := newTestDeployment(nets, d.Now)

	if err := d.Populate(ctx, nets, dep); err != nil {
		t.Fatalf("Populate: %v", err)
	}
	before := listPayments(t, ctx, net)

	if err := stores.Reset(ctx); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if got := len(listParticipants(t, ctx, net)); got != 0 {
		t.Fatalf("participants after reset = %d, want 0", got)
	}
	// Nothing is reconciled between the truncate and the reseed, and the reseed
	// admits the same addresses again. That works here because an admission
	// writes no row this fixture holds; a deployment that had given each bank a
	// place would have to give the old ones up first, which is the deployment's
	// own act and is proved where it lives.
	if err := d.Populate(ctx, nets, dep); err != nil {
		t.Fatalf("Populate after reset: %v", err)
	}
	after := listPayments(t, ctx, net)

	if len(after) != len(before) {
		t.Fatalf("payments after reset = %d, want %d", len(after), len(before))
	}
	for i := range before {
		if after[i].ID != before[i].ID || after[i].Status != before[i].Status ||
			after[i].Amount != before[i].Amount || !after[i].BookingDate.Equal(before[i].BookingDate) {
			t.Fatalf("payment %d differs after a reset: %+v vs %+v", i, after[i], before[i])
		}
	}
}

func TestSeededAccountsCarryTheirIBAN(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	for _, p := range listParticipants(t, ctx, net) {
		accounts, err := p.Deposit.ListAccounts(ctx)
		if err != nil {
			t.Fatalf("ListAccounts: %v", err)
		}
		for _, a := range accounts {
			if len(a.Identifiers) != 1 || a.Identifiers[0].Scheme != deposit.IdentifierIBAN {
				t.Fatalf("%s/%s identifiers = %#v, want exactly one IBAN",
					p.Name, a.Name, a.Identifiers)
			}
			// Resolving it must come back to the same account: this is the
			// property the customer send form depends on.
			got, err := p.Deposit.ResolveIdentifier(ctx, a.Identifiers[0])
			if err != nil {
				t.Fatalf("ResolveIdentifier(%s): %v", a.Identifiers[0].Value, err)
			}
			if got.ID != a.ID {
				t.Fatalf("resolved %s, want %s", got.ID, a.ID)
			}
		}
	}
}
