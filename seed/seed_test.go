package seed

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/raphi011/cbs/calendar"
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
type testDeployment struct {
	nets *payment.Networks
	now  func() time.Time
	// hub is what the banks have taken and not yet uploaded, in the order it was
	// taken.
	hub []taken
}

// taken is one instruction sitting in its bank's hub: who submitted it, and the
// instruction as that bank rewrote it.
type taken struct {
	by iso20022.BIC
	tx payment.InboundTransaction
	// scheme batches the file, which is what makes two schemes submitted by one
	// bank two uploads. See Bank.cutoff.
	scheme payment.SchemeID
}

func newTestDeployment(nets *payment.Networks, now func() time.Time) *testDeployment {
	return &testDeployment{nets: nets, now: now}
}

// AddBank writes nothing. A bank's rows are provision.Bank's and are already
// written by the time this is called; what a deployment adds on top is
// reachability, and everything below reaches a bank through its own network.
func (d *testDeployment) AddBank(context.Context, *payment.Bank) error { return nil }

func (d *testDeployment) CentralBankBIC() iso20022.BIC { return testCentralBankBIC }

// RefreshDirectory reads the roster at the clearing house and writes the copy
// at the subscriber, in that order and in two units of work — which is the
// whole of what the real one does, because a directory is a file delivered and
// not a message.
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

// Submit runs the submitting bank's half and holds the instruction, which is
// Deployment.Submit's shape and the whole of what it promises: nothing has left
// that bank when this returns.
func (d *testDeployment) Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	scheme, ok := d.nets.ClearingHouse().Scheme(req.Scheme)
	if !ok {
		return payment.Payment{}, fmt.Errorf("seed_test: no scheme %q, so no bank submits it", req.Scheme)
	}
	by := req.DebtorDetails.Agent
	if scheme.Direction() == payment.Pull {
		by = req.CreditorDetails.Agent
	}
	net, err := d.nets.Bank(ctx, payment.ParticipantID(by))
	if err != nil {
		return payment.Payment{}, err
	}
	p, err := net.SubmitPayment(ctx, req)
	if err != nil {
		return payment.Payment{}, err
	}
	relayed := req
	relayed.DebtorDetails, relayed.CreditorDetails = p.DebtorDetails, p.CreditorDetails
	d.hub = append(d.hub, taken{
		by:     by,
		tx:     payment.InboundTransaction{ID: p.ID, Request: relayed},
		scheme: p.Scheme,
	})
	return p, nil
}

// CarryToClearing empties every hub into the clearing house and tells each
// submitting bank what became of its instructions.
func (d *testDeployment) CarryToClearing(ctx context.Context) error {
	files := d.uploaded()
	d.hub = nil

	csm := d.nets.ClearingHouse()
	for _, file := range files {
		txs := make([]payment.InboundTransaction, 0, len(file))
		for _, t := range file {
			txs = append(txs, t.tx)
		}
		// One unit of work for the whole file, and per transaction after it. A
		// file is one instruction to the bank that sent it and M to the network.
		if _, err := csm.RecordRelayed(ctx, txs); err != nil {
			return err
		}
		for _, t := range file {
			if _, err := csm.AcceptAtCSM(ctx, t.tx.ID); err != nil {
				return err
			}
		}
	}
	for _, file := range files {
		net, err := d.nets.Bank(ctx, payment.ParticipantID(file[0].by))
		if err != nil {
			return err
		}
		for _, t := range file {
			if _, err := net.AcceptAtBank(ctx, t.tx.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// uploaded sorts the hub into the files a cut-off would have built: one per
// bank and scheme, the banks ascending by address, each file's transactions in
// the order that bank took them.
func (d *testDeployment) uploaded() [][]taken {
	var order []struct {
		by     iso20022.BIC
		scheme payment.SchemeID
	}
	files := map[iso20022.BIC]map[payment.SchemeID][]taken{}
	for _, t := range d.hub {
		if files[t.by] == nil {
			files[t.by] = map[payment.SchemeID][]taken{}
		}
		if files[t.by][t.scheme] == nil {
			order = append(order, struct {
				by     iso20022.BIC
				scheme payment.SchemeID
			}{t.by, t.scheme})
		}
		files[t.by][t.scheme] = append(files[t.by][t.scheme], t)
	}
	slices.SortStableFunc(order, func(a, b struct {
		by     iso20022.BIC
		scheme payment.SchemeID
	}) int {
		return strings.Compare(string(a.by), string(b.by))
	})

	out := make([][]taken, 0, len(order))
	for _, k := range order {
		out = append(out, files[k.by][k.scheme])
	}
	return out
}

// testNets is what a seed fixture holds: the clearing house's view for the
// reads these tests make, plus the factory Populate and the deployment both
// take. testNetwork below builds the sample scenario over the store testenv
// hands it, which is what makes the seed assertions claims about the seed
// rather than about a store — and is the whole of what a caller of this package
// assembles: a store, a set of networks, a deployment, and Populate over the
// three.
type testNets struct {
	*payment.ClearingHouseNetwork
	nets *payment.Networks
	// stores is the set the networks are minted over, for the one question no
	// institution can answer: which banks exist. See listParticipants.
	stores payment.Stores
}

// cb is the settlement agent's view, which is the only one that can be asked
// what a member's reserve balance is.
func (n testNets) cb() *payment.CentralBankNetwork { return n.nets.CentralBank() }

// bank is one member bank's own view, over that bank's own database. It panics on a
// failure to open rather than taking a *testing.T: every bank a fixture here names is
// one Populate founded moments ago in this process.
func (n testNets) bank(pid payment.ParticipantID) *payment.BankNetwork {
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
func testNetworkAndClock(t *testing.T) (testNets, *calendar.Clock) {
	t.Helper()
	clock := calendar.NewClock(BaseDate)
	d := New(clock)
	stores := testenv.NewSet(t, clock.Now)
	nets := payment.NewNetworks(stores, clock.Now)
	if err := d.Populate(context.Background(), nets, newTestDeployment(nets, clock.Now)); err != nil {
		t.Fatalf("populate: %v", err)
	}
	return testNets{ClearingHouseNetwork: nets.ClearingHouse(), nets: nets, stores: stores}, clock
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
	// Two the scenario settled and two it leaves OPEN, one per scheme, holding
	// every payment it means to leave in flight.
	if got := len(cycles); got != 4 {
		t.Fatalf("cycles = %d, want 4", got)
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

// TestEveryPaymentInAnOpenCycleWasUploaded is the constraint a payment added to
// this scenario later would break without it.
func TestEveryPaymentInAnOpenCycleWasUploaded(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	cycles, err := net.ListCycles(ctx)
	if err != nil {
		t.Fatalf("list cycles: %v", err)
	}
	var open, inFlight int
	for _, c := range cycles {
		if c.Status != payment.CycleOpen {
			continue
		}
		open++
		for _, id := range c.PaymentIDs {
			inFlight++
			p, err := net.GetPayment(ctx, id)
			if err != nil {
				t.Fatalf("the clearing house's copy of %s: %v", id, err)
			}
			// The bank that INSTRUCTED it, which is the only one holding a copy
			// before finality: the receiving bank learns of a payment when its
			// output file is released, and nothing has settled here.
			submitter := p.DebtorDetails.Agent
			if p.Scheme == payment.SchemeSEPADD {
				submitter = p.CreditorDetails.Agent
			}
			own, err := net.bank(payment.ParticipantID(submitter)).GetPayment(ctx, id)
			if err != nil {
				t.Errorf("%s is in open cycle %s and %s, which submitted it, holds no copy: %v", id, c.ID, submitter, err)
				continue
			}
			if own.Status != payment.Accepted {
				t.Errorf("%s (%s) is in open cycle %s and %s holds it as %v; no cut-off answered it, so no file was uploaded and the clearing house holds no share to release when this cycle settles",
					id, p.EndToEndID, c.ID, submitter, own.Status)
			}
		}
	}
	if open != 2 {
		t.Errorf("open cycles = %d, want one per scheme; a day refuses the files it takes in before it opens tomorrow's window", open)
	}
	// A build that put nothing in flight would walk none of the above and pass.
	if inFlight == 0 {
		t.Error("no payment is in an open cycle; this scenario ends with six in flight and this test asserted nothing")
	}
}

func TestPaymentStatusCoverage(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)
	payments, err := net.ListPayments(ctx)
	if err != nil {
		t.Fatalf("list payments: %v", err)
	}
	if got := len(payments); got != 12 {
		t.Fatalf("total payments = %d, want 12", got)
	}
	byStatus := map[payment.PaymentStatus]int{}
	for _, p := range payments {
		byStatus[p.Status]++
	}
	// No Cleared, and its absence is the assertion.
	want := map[payment.PaymentStatus]int{
		payment.Settled:  4,
		payment.Returned: 1,
		payment.Accepted: 6,
		payment.Cleared:  0,
		payment.Rejected: 1,
	}
	for status, n := range want {
		if byStatus[status] != n {
			t.Errorf("status %v = %d, want %d", status, byStatus[status], n)
		}
	}
}

// TestTheSeededNetworkReconciles holds all six of this scenario's databases
// against each other at once, and asks a strictly larger question than
// TestReservesConserved.
func TestTheSeededNetworkReconciles(t *testing.T) {
	net := testNetwork(t)

	report := recon.Check(t, net.nets)

	if len(report.Unreconciled) == 0 {
		t.Fatal("no unreconciled position anywhere in the seed; this scenario ends with six payments in flight, " +
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
// the wide one, over the same six databases. payment/recon opens all of them at
// once, precisely because no institution may; payment.Network.Reconcile is one
// member bank over its own.
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
		t.Fatal("no bank reports a position of its own; this scenario ends with six payments in flight, " +
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

// TestTheRejectedTransferWasReversedInThePayersBank pins the second half of the
// seed's one rejection. build() composes both halves — the clearing house
// transitions the payment, the submitting bank reverses the leg it posted — and
// only the first is visible in the payment row every other seed assertion
// reads.
func TestTheRejectedTransferWasReversedInThePayersBank(t *testing.T) {
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
	// The leg is on the PAYER'S BANK's copy and no other. The clearing house's row has
	// no leg columns at all, so reading DebtorLegTx off it would report a fixture that
	// does not cover a reversal when the reversal is there all along.
	payerBIC := payment.ParticipantID(rejected.DebtorDetails.Agent)
	atPayer, err := net.bank(payerBIC).GetPayment(ctx, rejected.ID)
	if err != nil {
		t.Fatalf("the payer's bank's copy: %v", err)
	}
	if atPayer.DebtorLegTx == "" {
		t.Fatal("the rejected payment has no debtor leg; the fixture no longer covers a reversal")
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
		t.Errorf("the rejected payment's debtor leg is %v, want Reversed — the payer's money is still in suspense", leg.Status)
	}
}

// TestSeedRejectLeavesThePayersBankUntouchedWhenItsHalfFails pins the shape of
// the seed's composite, not just its result.
func TestSeedRejectLeavesThePayersBankUntouchedWhenItsHalfFails(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	// The subject is BUILT rather than scavenged out of the finished dataset.
	b := &builder{ctx: ctx, nets: net.nets}

	payments, err := net.ListPayments(ctx)
	if err != nil {
		t.Fatalf("list payments: %v", err)
	}
	var from payment.Payment
	for _, p := range payments {
		if p.Status == payment.Settled && p.Scheme == payment.SchemeSEPACT {
			from = p
			break
		}
	}
	if from.ID == "" {
		t.Fatal("no settled credit transfer in the seed data to take a pair of parties from")
	}
	made := b.initiate(payment.InitiatePaymentRequest{
		Scheme:          from.Scheme,
		Debtor:          from.Debtor,
		Creditor:        from.Creditor,
		Amount:          1_000,
		EndToEndID:      "SCT-REJECTED-HALFWAY",
		Description:     "A rejection whose reversal fails",
		DebtorDetails:   payment.PartyDetails{Agent: from.DebtorDetails.Agent, Name: from.DebtorDetails.Name},
		CreditorDetails: payment.PartyDetails{Agent: from.CreditorDetails.Agent, Name: from.CreditorDetails.Name},
	})
	if made.Status != payment.Accepted {
		t.Fatalf("the clearing house took the new instruction to %s, want Accepted; there is no open cycle to reject out of", made.Status)
	}

	payer := net.bank(payment.ParticipantID(made.DebtorDetails.Agent))
	target, err := payer.GetPayment(ctx, made.ID)
	if err != nil {
		t.Fatalf("the payer's bank's copy of %s: %v", made.ID, err)
	}
	if target.DebtorLegTx == "" {
		t.Fatal("the payer's bank posted no debtor leg, so a rejection has nothing to fail to reverse")
	}
	if err := payer.ReverseDebtorLeg(ctx, target, "reversed already"); err != nil {
		t.Fatalf("reverse the leg out from under the composite: %v", err)
	}

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
// 15%, 18%) and a final accrued interest blending both rates. 487 (EUR 4.87) is
// derived, not read off a run — 15% ACT/365 on EUR 200.00 up to the repricing's
// effective date, 18% from it.
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
func TestSeededCatalogueShowsAllThreePricingCases(t *testing.T) {
	ctx := context.Background()
	net, clock := testNetworkAndClock(t)

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
	inForce, err := verde.Catalogue.VersionInForce(ctx, verde.ProductID, ledger.DayStart(clock.Now()))
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

// A mutation made after the scenario is built is dated on the DEPLOYMENT's
// timeline, where the scenario left it, and not on the wall clock.
func TestAMutationAfterTheBuildIsDatedOnTheDeploymentsTimeline(t *testing.T) {
	ctx := context.Background()
	net, clock := testNetworkAndClock(t)
	first := listParticipants(t, ctx, net)[0]
	accts, err := first.Deposit.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("list deposit accounts: %v", err)
	}
	if len(accts) == 0 {
		t.Fatal("first participant has no accounts")
	}
	ref := payment.PartyRef{Account: accts[0].ID}

	m, err := net.bank(first.ID).CreateMandate(ctx, first.BIC, ref, ref, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !m.CreatedAt.Equal(clock.Now()) {
		t.Fatalf("mandate CreatedAt = %v, want the business date %v", m.CreatedAt, clock.Now())
	}
	if !m.CreatedAt.After(BaseDate) {
		t.Fatalf("mandate CreatedAt = %v, want a day the scenario advanced to past the anchor %v", m.CreatedAt, BaseDate)
	}
}

// listParticipants and listPayments keep the ctx/error plumbing out of the
// assertions above. listParticipants goes through the STORES rather than an
// institution, which is the shape of the question: the clearing house holds no
// banks table, and the roster names addresses and says nothing about a bank
// founded and never admitted.
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
	clock := calendar.NewClock(BaseDate)
	d := New(clock)
	stores := testenv.NewSet(t, clock.Now)
	nets := payment.NewNetworks(stores, clock.Now)
	net := testNets{ClearingHouseNetwork: nets.ClearingHouse(), nets: nets, stores: stores}
	dep := newTestDeployment(nets, clock.Now)

	if err := d.Populate(ctx, nets, dep); err != nil {
		t.Fatalf("first Populate: %v", err)
	}
	participants, payments := listParticipants(t, ctx, net), listPayments(t, ctx, net)
	built := clock.Now()

	if err := d.Populate(ctx, nets, dep); err != nil {
		t.Fatalf("second Populate: %v", err)
	}
	if got := len(listParticipants(t, ctx, net)); got != len(participants) {
		t.Fatalf("participants after reseeding = %d, want %d", got, len(participants))
	}
	if got := len(listPayments(t, ctx, net)); got != len(payments) {
		t.Fatalf("payments after reseeding = %d, want %d", got, len(payments))
	}
	if got := clock.Now(); !got.Equal(built) {
		t.Fatalf("the business date after a second Populate = %v, want %v — the skip moved the clock", got, built)
	}

	// The case the idempotent skip exists for: a second process opening a store
	// that outlived the first.
	resumed := BaseDate.AddDate(0, 0, 400)
	secondClock := calendar.NewClock(resumed)
	second := New(secondClock)
	secondNets := payment.NewNetworks(stores, secondClock.Now)
	secondNet := testNets{ClearingHouseNetwork: secondNets.ClearingHouse(), nets: secondNets, stores: stores}
	if err := second.Populate(ctx, secondNets, newTestDeployment(secondNets, secondClock.Now)); err != nil {
		t.Fatalf("Populate from a second process: %v", err)
	}
	if got := len(listParticipants(t, ctx, secondNet)); got != len(participants) {
		t.Fatalf("participants seen by the second process = %d, want %d", got, len(participants))
	}
	if got := secondClock.Now(); !got.Equal(resumed) {
		t.Fatalf("the business date after an idempotent skip = %v, want %v — the skip rewound the clock", got, resumed)
	}

	// And the observable consequence, not just the clock reading: a row written
	// after the skip carries the day that process resumed on.
	acct, err := listParticipants(t, ctx, secondNet)[0].OpenCustomerAccount(ctx, "Opened after the skip", "EUR")
	if err != nil {
		t.Fatalf("open account after the skip: %v", err)
	}
	if !acct.CreatedAt.Equal(resumed) {
		t.Fatalf("account opened after the skip is dated %v, want %v", acct.CreatedAt, resumed)
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
	clock := calendar.NewClock(BaseDate)
	d := New(clock)
	stores := testenv.NewSet(t, clock.Now)
	nets := payment.NewNetworks(stores, clock.Now)
	net := testNets{ClearingHouseNetwork: nets.ClearingHouse(), nets: nets, stores: stores}
	dep := newTestDeployment(nets, clock.Now)

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
	// admits the same addresses again.
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
