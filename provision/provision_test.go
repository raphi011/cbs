package provision_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/provision"
	"github.com/raphi011/cbs/store/testenv"
)

// The suite drives the provisioner over a whole deployment's databases, because
// that is the only thing it can be driven over: its subject is three
// institutions and there is no smaller set that has all three.
//
// What it does NOT re-test is what each act does. Those are payment's, asserted
// one act at a time in payment's own suite, and a second copy here would be two
// answers to one question. What is left is the COMPOSITION — the order, the
// per-asset loop, and the act that is deliberately not in it.

const (
	joinerBIC   iso20022.BIC     = "NORDSESSXXX"
	joinerName                   = "Nordhaven Bank"
	otherBIC    iso20022.BIC     = "AURODEFFXXX"
	fixtureAsts ledger.AssetCode = "EUR"
)

var testTime = time.Date(2025, 9, 15, 9, 0, 0, 0, time.UTC)

func newNetworks(t *testing.T) *payment.Networks {
	t.Helper()
	clock := func() time.Time { return testTime }
	return payment.NewNetworks(testenv.NewSet(t, clock), clock)
}

func provisionOne(t *testing.T, nets *payment.Networks, bic iso20022.BIC, name string, assets ...ledger.AssetCode) *payment.Bank {
	t.Helper()
	p, err := provision.Bank(context.Background(), nets, provision.BankSpec{
		Name: name, BIC: bic, Country: iban.DE, Assets: assets,
	})
	if err != nil {
		t.Fatalf("provisioning %s: %v", bic, err)
	}
	return p
}

// One bank is three rows, and each of the three is a different institution's.
//
// The assertion is made at each institution's own network rather than off the
// value the provisioner returned, because a return value proves only what one
// act said. What is worth pinning is that the OTHER two committed something a
// reader at that institution can find.
func TestProvisioningWritesOneRowAtEachOfThreeInstitutions(t *testing.T) {
	ctx := context.Background()
	nets := newNetworks(t)
	bank := provisionOne(t, nets, joinerBIC, joinerName)

	// The joining bank's own row, in its own database, holding the account
	// number the central bank allocated. An empty settlement reference is what
	// a half-provisioned bank has, and it is the thing this asserts is absent.
	own, err := nets.Bank(ctx, bank.ID)
	if err != nil {
		t.Fatalf("the joining bank's network: %v", err)
	}
	row, err := own.GetBank(ctx, bank.ID)
	if err != nil {
		t.Fatalf("the joining bank's own row: %v", err)
	}
	if got := row.Assets[fixtureAsts].Settlement; got == "" {
		t.Error("the bank holds no settlement account number; provisioning stopped before it recorded one")
	}

	// The settlement agent's register, keyed by address, because a settlement
	// agent has never heard of this system's bank ids.
	member, err := nets.CentralBank().GetSettlementMember(ctx, bank.BIC)
	if err != nil {
		t.Fatalf("the settlement agent holds no account for %s: %v", bank.BIC, err)
	}
	if member.Accounts[fixtureAsts] != row.Assets[fixtureAsts].Settlement {
		t.Errorf("the agent holds %q and the bank recorded %q; the acknowledgement carried the wrong number",
			member.Accounts[fixtureAsts], row.Assets[fixtureAsts].Settlement)
	}

	// And the roster, which is what makes the bank reachable at all.
	if _, err := nets.ClearingHouse().GetRosterEntryByBIC(ctx, bank.BIC); err != nil {
		t.Fatalf("the clearing house routes nothing to %s: %v", bank.BIC, err)
	}
}

// One settlement account is one asset's, so a bank in two assets applies twice.
//
// The agent answers about its own book and knows nothing about schemes, so it
// opens an account per asset asked for whether or not this scheme clears in it.
func TestABankInTwoAssetsGetsASettlementAccountInEach(t *testing.T) {
	ctx := context.Background()
	nets := newNetworks(t)
	bank := provisionOne(t, nets, joinerBIC, joinerName, "EUR", "USD")

	member, err := nets.CentralBank().GetSettlementMember(ctx, bank.BIC)
	if err != nil {
		t.Fatalf("GetSettlementMember: %v", err)
	}
	for _, asset := range []ledger.AssetCode{"EUR", "USD"} {
		if member.Accounts[asset] == "" {
			t.Errorf("the agent opened no %s account; one application per asset is what the loop is for", asset)
		}
		if bank.Assets[asset].Settlement == "" {
			t.Errorf("the bank recorded no %s settlement account", asset)
		}
	}
	if a, b := member.Accounts["EUR"], member.Accounts["USD"]; a == b {
		t.Errorf("both assets settle across %q; two accounts is the whole of what two applications buy", a)
	}
}

// Provisioning fills NOBODY's routing directory, and this is the guard on that.
//
// A directory is a copy each member pulls for itself. A deployment that
// provisions a bank and stops has a scheme where the roster names the new bank
// and no incumbent can address it — which is the real behaviour of a published
// routing table, and the thing that stops being observable the moment
// provisioning refreshes anybody. See provision.Subscribe.
func TestProvisioningFillsNobodysDirectory(t *testing.T) {
	ctx := context.Background()
	nets := newNetworks(t)
	incumbent := provisionOne(t, nets, otherBIC, "Aurora Bank")
	if err := provision.Subscribe(ctx, nets); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	joiner := provisionOne(t, nets, joinerBIC, joinerName)

	// The roster knows. The incumbent's own copy does not.
	if _, err := nets.ClearingHouse().GetRosterEntryByBIC(ctx, joiner.BIC); err != nil {
		t.Fatalf("the joining bank is not in the roster: %v", err)
	}
	aurora, err := nets.Bank(ctx, incumbent.ID)
	if err != nil {
		t.Fatalf("the incumbent's network: %v", err)
	}
	if _, err := aurora.ResolveBankCode(ctx, joiner.Issuer); !errors.Is(err, payment.ErrBankCodeUnknown) {
		t.Fatalf("the incumbent resolves the new bank's allocation to %v, want ErrBankCodeUnknown — "+
			"provisioning published a directory it has no business publishing", err)
	}

	// And one refresh, which is the subscribers' own act, is what changes it.
	if err := provision.Subscribe(ctx, nets); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if _, err := aurora.ResolveBankCode(ctx, joiner.Issuer); err != nil {
		t.Errorf("after a refresh the incumbent still cannot route to %s: %v", joiner.BIC, err)
	}
}

// Two banks listed on one address is refused, and no domain act refuses it.
//
// A bank's id is its BIC, so the second spec is a second FOUNDING of the bank
// already there: same book, same row, same roster entry — and a new name on all
// of them. That is the silent failure this guards, and the assertion that
// matters is the one after the refusal, that the incumbent is untouched.
func TestTwoBanksOnOneAddressAreRefused(t *testing.T) {
	ctx := context.Background()
	nets := newNetworks(t)
	incumbent := provisionOne(t, nets, joinerBIC, joinerName)

	_, err := provision.Bank(ctx, nets, provision.BankSpec{
		Name: "Impostor Bank", BIC: joinerBIC, Country: iban.DE,
	})
	if !errors.Is(err, provision.ErrAddressTaken) {
		t.Fatalf("a second bank on one address = %v, want ErrAddressTaken", err)
	}
	own, err := nets.Bank(ctx, incumbent.ID)
	if err != nil {
		t.Fatalf("the incumbent's network: %v", err)
	}
	row, err := own.GetBank(ctx, incumbent.ID)
	if err != nil {
		t.Fatalf("the incumbent's own row: %v", err)
	}
	if row.Name != joinerName {
		t.Errorf("the incumbent is now %q; a refused spec renamed the bank that holds the address", row.Name)
	}
}

// Running the same deployment twice writes nothing new.
//
// A provisioner is a thing a deployment re-runs — after a crash between the
// acts, or simply on the next boot — so the same list twice must leave the same
// system rather than a second copy or a refusal. The reference is derived from
// the address for exactly this: the second pass quotes what the clearing house
// already holds, and is read as the admission it already has.
func TestProvisioningTheSameDeploymentTwiceIsANoOp(t *testing.T) {
	ctx := context.Background()
	nets := newNetworks(t)
	first := provisionOne(t, nets, joinerBIC, joinerName)

	second, err := provision.Bank(ctx, nets, provision.BankSpec{
		Name: joinerName, BIC: joinerBIC, Country: iban.DE,
	})
	if err != nil {
		t.Fatalf("provisioning the same bank twice: %v", err)
	}
	if second.ID != first.ID || second.BookID != first.BookID {
		t.Errorf("the second pass produced %s/%s, want the first pass's %s/%s",
			second.ID, second.BookID, first.ID, first.BookID)
	}
	entries, err := nets.ClearingHouse().ListRosterEntries(ctx)
	if err != nil {
		t.Fatalf("ListRosterEntries: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("the roster holds %d entries after two passes, want 1", len(entries))
	}
	banks, err := nets.Stores().Banks(ctx)
	if err != nil {
		t.Fatalf("Banks: %v", err)
	}
	if len(banks) != 1 {
		t.Errorf("two passes left %d bank databases, want 1", len(banks))
	}
}
