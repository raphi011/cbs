package seed

import (
	"context"
	"testing"
	"time"

	"github.com/raphi011/cbs/calendar"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/payment/recon"
	"github.com/raphi011/cbs/provision"
	"github.com/raphi011/cbs/store/testenv"
)

// testCentralBankBIC is the settlement agent every bank here lodges cash with.
// It is real-shaped and is no bank's: an institution sharing an address with a
// seeded bank would be two institutions at one address.
const testCentralBankBIC iso20022.BIC = "CBSEDEFFXXX"

// testDeployment is the running system the base state is built into, composed
// directly instead of carried. It is small because the base state is: nothing
// here crosses a wire, and a scenario — which does — is driven over the real
// deployment in cmd/server rather than over this.
type testDeployment struct {
	nets *payment.Networks
	now  func() time.Time
}

// AddBank writes nothing. A bank's rows are provision.Bank's and are already
// written by the time this is called; what a deployment adds on top is
// reachability, and everything below reaches a bank through its own network.
func (d *testDeployment) AddBank(context.Context, *payment.Bank) error { return nil }

// Lodge places a member's cash on reserve IN PROCESS, for Subscribe's reason:
// there is no host here to POST a camt.050 to, and the rows both institutions
// end up holding are the same.
func (d *testDeployment) Lodge(ctx context.Context, bic iso20022.BIC, asset ledger.AssetCode,
	amount ledger.Amount) (payment.LodgementInstruction, error) {

	net, err := d.nets.Bank(ctx, payment.ParticipantID(bic))
	if err != nil {
		return payment.LodgementInstruction{}, err
	}
	in, _, err := net.LodgeReserves(ctx, asset, amount, payment.MessageContext{
		From: bic, To: testCentralBankBIC, Now: d.now(),
		MsgID: "seed-lodge-" + string(bic) + "-" + string(asset),
	})
	if err != nil {
		return payment.LodgementInstruction{}, err
	}
	_, err = d.nets.CentralBank().ReceiveLodgement(ctx, in)
	return in, err
}

// Subscribe delivers the roster to every member IN PROCESS. There is no host and
// no listener here, so what the real one publishes and collects over a wire this
// one hands over; the rows each member ends up holding are the same.
func (d *testDeployment) Subscribe(ctx context.Context) error {
	return provision.Subscribe(ctx, d.nets)
}

// testNets is what a fixture holds: the clearing house's view for the reads
// these tests make, plus the set of networks Populate was run over.
type testNets struct {
	*payment.ClearingHouseNetwork
	nets   *payment.Networks
	stores payment.Stores
}

func (n testNets) cb() *payment.CentralBankNetwork { return n.nets.CentralBank() }

// bank is one member bank's own view, over that bank's own database. It panics
// on a failure to open rather than taking a *testing.T: every bank a fixture
// here names is one Populate founded moments ago in this process.
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

func testNetworkAndClock(t *testing.T) (testNets, *calendar.Clock) {
	t.Helper()
	clock := calendar.NewClock(BaseDate)
	d := New(clock)
	stores := testenv.NewSet(t, clock.Now)
	nets := payment.NewNetworks(stores, clock.Now)
	if err := d.Populate(context.Background(), nets, &testDeployment{nets: nets, now: clock.Now}); err != nil {
		t.Fatalf("populate: %v", err)
	}
	return testNets{ClearingHouseNetwork: nets.ClearingHouse(), nets: nets, stores: stores}, clock
}

// bics is every bank the base state founds, in the order it founds them.
func bics(t *testing.T, net testNets) []payment.ParticipantID {
	t.Helper()
	ids, err := net.stores.Banks(context.Background())
	if err != nil {
		t.Fatalf("listing the deployment's banks: %v", err)
	}
	out := make([]payment.ParticipantID, 0, len(ids))
	for _, bic := range ids {
		out = append(out, payment.ParticipantID(bic))
	}
	return out
}

// Four banks, each in the roster, and the clearing house's window open for
// every scheme it runs.
func TestTheBaseStateIsFourAdmittedBanksAndAnOpenWindow(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	if got := len(bics(t, net)); got != 4 {
		t.Fatalf("the deployment holds %d bank databases, want 4", got)
	}
	entries, err := net.ListRosterEntries(ctx)
	if err != nil {
		t.Fatalf("ListRosterEntries: %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("the roster names %d members, want 4 — a bank not on it can be sent nothing", len(entries))
	}

	cycles, err := net.ListCycles(ctx)
	if err != nil {
		t.Fatalf("ListCycles: %v", err)
	}
	schemes := net.ListSchemes()
	if len(cycles) != len(schemes) {
		t.Fatalf("cycles = %d, want one open per scheme (%d)", len(cycles), len(schemes))
	}
	for _, c := range cycles {
		if c.Status != payment.CycleOpen {
			t.Errorf("cycle %s for %s is %v, want open — a file uploaded into a closed window has nowhere to go",
				c.ID, c.Scheme, c.Status)
		}
	}
}

// Every member holds the routing table, which is what makes the others
// addressable at all.
func TestEveryMemberCanRouteToEveryOther(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	entries, err := net.ListRosterEntries(ctx)
	if err != nil {
		t.Fatalf("ListRosterEntries: %v", err)
	}
	for _, from := range bics(t, net) {
		for _, to := range entries {
			if payment.ParticipantID(to.BIC) == from {
				continue
			}
			if _, err := net.bank(from).ResolveBankCode(ctx, to.Issuer); err != nil {
				t.Errorf("%s cannot route to %s: %v — the base state left a member unsubscribed", from, to.BIC, err)
			}
		}
	}
}

// A bank funded before it has a single depositor, which is what the capital
// injection exists for: cash it owns, most of it placed on reserve.
func TestEveryBankIsPrefundedAndOnReserve(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	for _, pid := range bics(t, net) {
		bank, err := net.bank(pid).GetBank(ctx, pid)
		if err != nil {
			t.Fatalf("%s's own row: %v", pid, err)
		}
		accts, err := bank.AccountsFor(seedAsset)
		if err != nil {
			t.Fatalf("%s's EUR accounts: %v", pid, err)
		}

		equity, err := bank.Ledger.BookBalance(ctx, ledger.Position{Account: accts.ShareCapital})
		if err != nil {
			t.Fatalf("%s's share capital: %v", pid, err)
		}
		if equity != Capital {
			t.Errorf("%s's owners put in %d, want %d", pid, equity, Capital)
		}
		// What is left in the drawer, and it is deliberately not zero: reserves and
		// cash are not the same money. The depositor's own money is in it too, and
		// none of that was lodged — a customer's cash is theirs and is owed back.
		cash, err := bank.Ledger.BookBalance(ctx, ledger.Position{Account: accts.VaultCash})
		if err != nil {
			t.Fatalf("%s's vault cash: %v", pid, err)
		}
		if want := Capital - Lodged + Opening; cash != want {
			t.Errorf("%s holds %d in the vault, want %d", pid, cash, want)
		}

		// And the settlement agent's own record of the same money, which is the
		// only balance a member cannot read for itself.
		reserve, err := net.cb().ReserveBalance(ctx, bank.BIC, seedAsset)
		if err != nil {
			t.Fatalf("%s's reserve at the agent: %v", pid, err)
		}
		if reserve != Lodged {
			t.Errorf("%s's reserve stands at %d, want %d — it cannot settle out of vault cash", pid, reserve, Lodged)
		}
	}
}

// Every bank is priced, because an account is opened FROM a product and a
// scenario that had to price one first would be pricing rather than paying.
func TestEveryBankIsPriced(t *testing.T) {
	ctx := context.Background()
	net, clock := testNetworkAndClock(t)

	for _, pid := range bics(t, net) {
		bank, err := net.bank(pid).GetBank(ctx, pid)
		if err != nil {
			t.Fatalf("%s's own row: %v", pid, err)
		}
		products, err := bank.Catalogue.ListProducts(ctx)
		if err != nil {
			t.Fatalf("%s's catalogue: %v", pid, err)
		}
		if len(products) != 2 {
			t.Fatalf("%s lists %d products, want Basic and %s", pid, len(products), PremiumProduct)
		}
		// The Basic product has a version IN FORCE on the base date, which is what
		// an account opened by a scenario floats to.
		if _, err := bank.Catalogue.VersionInForce(ctx, bank.ProductID, clock.Now()); err != nil {
			t.Errorf("%s's default product prices nothing on the base date: %v", pid, err)
		}
	}
}

// The base state is a starting position and not a history: one depositor per
// bank with money in the account, and nothing that has happened to it — nothing
// paid, nothing lent, nothing signed.
func TestTheBaseStateHoldsOneDepositorPerBankAndNoHistory(t *testing.T) {
	ctx := context.Background()
	net := testNetwork(t)

	if payments, err := net.ListPayments(ctx); err != nil {
		t.Fatalf("ListPayments: %v", err)
	} else if len(payments) != 0 {
		t.Errorf("the clearing house holds %d payments, want none", len(payments))
	}
	if settlements, err := net.cb().ListSettlements(ctx); err != nil {
		t.Fatalf("ListSettlements: %v", err)
	} else if len(settlements) != 0 {
		t.Errorf("the settlement agent has discharged %d cut-offs, want none", len(settlements))
	}

	for _, pid := range bics(t, net) {
		bank, err := net.bank(pid).GetBank(ctx, pid)
		if err != nil {
			t.Fatalf("%s's own row: %v", pid, err)
		}
		accts, err := bank.Deposit.ListAccounts(ctx)
		if err != nil {
			t.Fatalf("%s's deposit accounts: %v", pid, err)
		}
		if len(accts) != 1 {
			t.Fatalf("%s holds %d deposit accounts, want its one depositor", pid, len(accts))
		}
		balance, err := bank.Deposit.GetBalance(ctx, accts[0].ID)
		if err != nil {
			t.Fatalf("%s's depositor's balance: %v", pid, err)
		}
		if balance.Book != Opening {
			t.Errorf("%s's depositor holds %d, want %d — an account nobody paid into cannot pay anyone",
				pid, balance.Book, Opening)
		}
		if facilities, err := bank.Lending.ListFacilities(ctx); err != nil {
			t.Fatalf("%s's facilities: %v", pid, err)
		} else if len(facilities) != 0 {
			t.Errorf("%s holds %d credit facilities, want none", pid, len(facilities))
		}
		if mandates, err := net.bank(pid).ListMandates(ctx); err != nil {
			t.Fatalf("%s's mandates: %v", pid, err)
		} else if len(mandates) != 0 {
			t.Errorf("%s holds %d mandates, want none", pid, len(mandates))
		}
	}
}

// The whole deployment's books agree, which is the instrument no institution
// may run for itself.
func TestTheBaseStateReconciles(t *testing.T) {
	net := testNetwork(t)

	recon.Check(t, net.nets)
}

// Populate is called at boot and again by every reset, so building over what is
// already there must write nothing.
func TestPopulatingTwiceBuildsNothingNew(t *testing.T) {
	ctx := context.Background()
	clock := calendar.NewClock(BaseDate)
	d := New(clock)
	stores := testenv.NewSet(t, clock.Now)
	nets := payment.NewNetworks(stores, clock.Now)
	dep := &testDeployment{nets: nets, now: clock.Now}

	if err := d.Populate(ctx, nets, dep); err != nil {
		t.Fatalf("the first pass: %v", err)
	}
	if err := d.Populate(ctx, nets, dep); err != nil {
		t.Fatalf("the second pass: %v", err)
	}

	net := testNets{ClearingHouseNetwork: nets.ClearingHouse(), nets: nets, stores: stores}
	if got := len(bics(t, net)); got != 4 {
		t.Errorf("two passes left %d bank databases, want 4", got)
	}
	cycles, err := net.ListCycles(ctx)
	if err != nil {
		t.Fatalf("ListCycles: %v", err)
	}
	if want := len(net.ListSchemes()); len(cycles) != want {
		t.Errorf("two passes left %d cycles, want %d", len(cycles), want)
	}
}

// A deployment is required, and saying so is better than founding banks no
// institution can address.
func TestPopulateRefusesWithoutADeployment(t *testing.T) {
	clock := calendar.NewClock(BaseDate)
	nets := payment.NewNetworks(testenv.NewSet(t, clock.Now), clock.Now)
	if err := New(clock).Populate(context.Background(), nets, nil); err == nil {
		t.Error("Populate with no deployment returned nil; a founded bank nobody admitted can neither send nor be sent to")
	}
}
