// This file's tests live in package deposit_test, dot-importing deposit, for
// the same reason register_test.go does: newTestRegister builds a Register over
// a store from store/testenv, which reaches store/sqlite, which imports
// deposit, so an in-package test file using it would be an import cycle.
package deposit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/ledger"
)

// pan is a scheme somebody else issues, which is what makes it the right
// counterparty for every rule about identifiers-in-general.
const pan IdentifierScheme = "PAN"

func card(v string) Identifier { return Identifier{Scheme: pan, Value: v} }

// ibanOf returns the address the register minted for an account.
func ibanOf(t *testing.T, a Account) Identifier {
	t.Helper()
	for _, i := range a.Identifiers {
		if i.Scheme == IdentifierIBAN {
			return i
		}
	}
	t.Fatalf("account %s holds no IBAN: %#v", a.ID, a.Identifiers)
	return Identifier{}
}

// ---------------------------------------------------------------------------
// A bank issues its customers' addresses
// ---------------------------------------------------------------------------

func TestOpenAccountMintsAnAddress(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	addr := ibanOf(t, acct)

	// It is a real address, under this register's own allocation, and it is
	// STORED COMPACT — the canonical form, which is also the only one a pacs.008
	// carries.
	parsed, err := iban.Parse(addr.Value)
	if err != nil {
		t.Fatalf("the minted address does not verify: %v", err)
	}
	if string(parsed) != addr.Value {
		t.Errorf("stored %q, canonical form is %q", addr.Value, string(parsed))
	}
	code, err := parsed.BankCode()
	if err != nil {
		t.Fatalf("BankCode: %v", err)
	}
	if code != testIssuer.BankCode || parsed.Country() != testIssuer.Country {
		t.Errorf("minted under (%s, %s), want (%s, %s)",
			parsed.Country(), code, testIssuer.Country, testIssuer.BankCode)
	}

	got, err := reg.ResolveIdentifier(ctx, addr)
	if err != nil {
		t.Fatalf("ResolveIdentifier: %v", err)
	}
	if got.ID != acct.ID {
		t.Fatalf("resolved %s, want %s", got.ID, acct.ID)
	}
}

// Serials are dense and per bank, because the counter is the register's own and
// not the book's shared one. An account number is read out loud; gaps in it
// would be a record of unrelated work.
func TestMintedAddressesAreDenseAndDistinct(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)

	seen := map[string]bool{}
	for n := range 5 {
		acct, err := reg.OpenAccount(ctx, "Customer", testAsset, prd, 0)
		if err != nil {
			t.Fatalf("OpenAccount %d: %v", n, err)
		}
		v := ibanOf(t, acct).Value
		if seen[v] {
			t.Fatalf("address %s minted twice", v)
		}
		seen[v] = true

		want, err := iban.New(testIssuer.Country, testIssuer.BankCode, uint64(n+1))
		if err != nil {
			t.Fatalf("iban.New: %v", err)
		}
		if v != string(want) {
			t.Errorf("account %d got %s, want serial %d — %s", n, v, n+1, want)
		}
	}
}

// A caller does not get to say what an account's address is.
func TestOpenAccountRefusesACallerSuppliedIBAN(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)
	elsewhere := Identifier{Scheme: IdentifierIBAN, Value: "DE89370400440532013000"}

	_, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0, elsewhere)
	if !errors.Is(err, ErrIBANIsIssued) {
		t.Fatalf("OpenAccount with a supplied IBAN = %v, want ErrIBANIsIssued", err)
	}
	if _, err := reg.ResolveIdentifier(ctx, elsewhere); !errors.Is(err, ErrIdentifierNotFound) {
		t.Fatalf("ResolveIdentifier after the refusal = %v, want ErrIdentifierNotFound", err)
	}
}

func TestAddIdentifierRefusesAnIBAN(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)
	acct, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	second := Identifier{Scheme: IdentifierIBAN, Value: "DE89370400440532013000"}
	if err := reg.AddIdentifier(ctx, acct.ID, second); !errors.Is(err, ErrIBANIsIssued) {
		t.Fatalf("AddIdentifier(IBAN) = %v, want ErrIBANIsIssued", err)
	}
	// And the account still holds exactly the one it was issued.
	after, err := reg.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if len(after.Identifiers) != 1 {
		t.Fatalf("identifiers = %#v, want the one minted address", after.Identifiers)
	}
}

// A register with no allocation can open no accounts. That is a real state — a
// bank exists before any registry has heard of it — and the refusal is what
// stops one inventing a code for itself.
func TestOpenAccountRefusesARegisterWithNoBankCode(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegisterIssuedBy(t, iban.Issuer{})

	if _, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0); !errors.Is(err, ErrNoIssuer) {
		t.Fatalf("OpenAccount on a register with no issuer = %v, want ErrNoIssuer", err)
	}
}

// ---------------------------------------------------------------------------
// The schemes a caller does supply
// ---------------------------------------------------------------------------

func TestAddAndRemoveAnIssuedElsewhereIdentifier(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)
	acct, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	c := card("4000000000000001")

	if err := reg.AddIdentifier(ctx, acct.ID, c); err != nil {
		t.Fatalf("AddIdentifier: %v", err)
	}
	if _, err := reg.ResolveIdentifier(ctx, c); err != nil {
		t.Fatalf("ResolveIdentifier after add: %v", err)
	}

	if err := reg.RemoveIdentifier(ctx, acct.ID, c); err != nil {
		t.Fatalf("RemoveIdentifier: %v", err)
	}
	if _, err := reg.ResolveIdentifier(ctx, c); !errors.Is(err, ErrIdentifierNotFound) {
		t.Fatalf("ResolveIdentifier after remove = %v, want ErrIdentifierNotFound", err)
	}
	// The minted address is untouched by any of it.
	if _, err := reg.ResolveIdentifier(ctx, ibanOf(t, acct)); err != nil {
		t.Fatalf("the account's own address stopped resolving: %v", err)
	}
}

// The same address twice in ONE OpenAccount call. checkIdentifierFreeTx only
// sees accounts already in the register, and the account being opened is not
// one of them, so nothing else catches this — and the API forwards the list
// from a request body verbatim.
func TestOpenAccountRefusesTheSameIdentifierTwice(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)
	c := card("4000000000000001")

	_, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0, c, c)
	if !errors.Is(err, ErrIdentifierTaken) {
		t.Fatalf("OpenAccount with a repeated identifier = %v, want ErrIdentifierTaken", err)
	}
	if _, err := reg.ResolveIdentifier(ctx, c); !errors.Is(err, ErrIdentifierNotFound) {
		t.Fatalf("ResolveIdentifier = %v, want ErrIdentifierNotFound", err)
	}
}

func TestAddIdentifierRefusesADuplicateAtTheSameBank(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)
	c := card("4000000000000001")

	if _, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0, c); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	other, err := reg.OpenAccount(ctx, "Aaron", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	if err := reg.AddIdentifier(ctx, other.ID, c); !errors.Is(err, ErrIdentifierTaken) {
		t.Fatalf("AddIdentifier = %v, want ErrIdentifierTaken", err)
	}
	// And the same rule at open, which is a different code path.
	if _, err := reg.OpenAccount(ctx, "Annie", testAsset, prd, 0, c); !errors.Is(err, ErrIdentifierTaken) {
		t.Fatalf("OpenAccount with a taken identifier = %v, want ErrIdentifierTaken", err)
	}
}

func TestAddIdentifierIsIdempotentForTheSameAccount(t *testing.T) {
	// Re-adding an identifier the account already holds is not a collision with
	// somebody else — it is a no-op. Refusing it would make a retried request
	// fail on its second delivery.
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)
	c := card("4000000000000001")
	acct, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0, c)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	if err := reg.AddIdentifier(ctx, acct.ID, c); err != nil {
		t.Fatalf("AddIdentifier (repeat) = %v, want nil", err)
	}
	got, err := reg.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if len(got.Identifiers) != 2 { // the minted IBAN, and the card
		t.Fatalf("identifiers = %#v, want the address and one card", got.Identifiers)
	}
}

func TestIdentifierMutationsAreAudited(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)
	acct, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	c := card("4000000000000001")
	if err := reg.AddIdentifier(ctx, acct.ID, c); err != nil {
		t.Fatalf("AddIdentifier: %v", err)
	}
	if err := reg.RemoveIdentifier(ctx, acct.ID, c); err != nil {
		t.Fatalf("RemoveIdentifier: %v", err)
	}

	types := auditTypesFor(t, reg, acct.ID)
	wantBoth := map[string]bool{ledger.EventIdentifierAdded: false, ledger.EventIdentifierRemoved: false}
	for _, got := range types {
		if _, ok := wantBoth[got]; ok {
			wantBoth[got] = true
		}
	}
	for want, seen := range wantBoth {
		if !seen {
			t.Fatalf("audit types = %v, want it to include %s", types, want)
		}
	}
}

func TestResolveIdentifierIsAmbiguousWhenTwoAccountsHoldIt(t *testing.T) {
	// Reachable only by going around the register — which is exactly what a
	// concurrent add does, since uniqueness has no constraint behind it. The
	// answer must be a refusal, not the first hit.
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)
	first, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	addr := ibanOf(t, first)
	second, err := reg.OpenAccount(ctx, "Aaron", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	// Write the duplicate straight through the store, past the register's check.
	if err := reg.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		a, err := tx.GetDepositAccount(ctx, reg.BookID(), second.ID)
		if err != nil {
			return err
		}
		a.Identifiers = append(a.Identifiers, addr)
		return tx.PutDepositAccount(ctx, reg.BookID(), a)
	}); err != nil {
		t.Fatalf("seeding the duplicate: %v", err)
	}

	if _, err := reg.ResolveIdentifier(ctx, addr); !errors.Is(err, ErrIdentifierAmbiguous) {
		t.Fatalf("ResolveIdentifier = %v, want ErrIdentifierAmbiguous", err)
	}
}

// ---------------------------------------------------------------------------
// One address, two spellings
// ---------------------------------------------------------------------------

func TestResolveFindsAnAddressInTheSpellingAPersonTypes(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)
	acct, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	stored := ibanOf(t, acct)
	grouped := Identifier{Scheme: IdentifierIBAN, Value: iban.IBAN(stored.Value).Grouped()}
	if grouped.Value == stored.Value {
		t.Fatal("the grouped form is the stored form; this test is testing nothing")
	}

	got, err := reg.ResolveIdentifier(ctx, grouped)
	if err != nil {
		t.Fatalf("ResolveIdentifier(%q) = %v", grouped.Value, err)
	}
	if got.ID != acct.ID {
		t.Fatalf("resolved %s, want %s", got.ID, acct.ID)
	}
}

// Withdrawal takes the address, whichever spelling names it.
func TestRemoveIdentifierWithdrawsTheAddressInEitherSpelling(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)
	acct, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	stored := ibanOf(t, acct)
	grouped := Identifier{Scheme: IdentifierIBAN, Value: iban.IBAN(stored.Value).Grouped()}

	if err := reg.RemoveIdentifier(ctx, acct.ID, grouped); err != nil {
		t.Fatalf("RemoveIdentifier(the grouped spelling): %v", err)
	}
	after, err := reg.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if len(after.Identifiers) != 0 {
		t.Fatalf("identifiers = %#v, want the address withdrawn", after.Identifiers)
	}
	if _, err := reg.ResolveIdentifier(ctx, stored); !errors.Is(err, ErrIdentifierNotFound) {
		t.Fatalf("ResolveIdentifier after withdrawal = %v, want ErrIdentifierNotFound", err)
	}

	// The audit trail records the identifier as STORED, not as quoted: what
	// happened is that the account's own address was withdrawn.
	var payloads []string
	if err := reg.Store().View(ctx, func(ctx context.Context, tx Tx) error {
		events, err := tx.ListAudit(ctx, ledger.AuditFilter{
			BookID:   reg.BookID(),
			Scope:    ledger.ScopeDeposit,
			EntityID: string(acct.ID),
		})
		if err != nil {
			return err
		}
		for _, e := range events {
			if e.Type == ledger.EventIdentifierRemoved {
				payloads = append(payloads, string(e.Payload))
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("reading the audit trail: %v", err)
	}
	if len(payloads) != 1 {
		t.Fatalf("got %d identifier.removed events, want 1", len(payloads))
	}
	if !strings.Contains(payloads[0], stored.Value) {
		t.Errorf("identifier.removed payload = %s, want the stored form %q", payloads[0], stored.Value)
	}
}

// ---------------------------------------------------------------------------
// Reissue
// ---------------------------------------------------------------------------

// Reissue is one act because it stopped being expressible as two: the add half
// of remove-plus-add is a refusal now, and an account between the two calls has
// no address and cannot be paid.
func TestReissueMintsAndWithdrawsTogether(t *testing.T) {
	ctx := context.Background()
	reg, _, _, prd := newTestRegister(t)
	c := card("4000000000000001")
	acct, err := reg.OpenAccount(ctx, "Alice", testAsset, prd, 0, c)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	old := ibanOf(t, acct)

	fresh, err := reg.ReissueIdentifier(ctx, acct.ID)
	if err != nil {
		t.Fatalf("ReissueIdentifier: %v", err)
	}
	if fresh.Value == old.Value {
		t.Fatal("reissue returned the address it was replacing")
	}
	if err := iban.IBAN(fresh.Value).Validate(); err != nil {
		t.Fatalf("the reissued address does not verify: %v", err)
	}

	// The old one is gone and the new one resolves.
	if _, err := reg.ResolveIdentifier(ctx, old); !errors.Is(err, ErrIdentifierNotFound) {
		t.Errorf("the withdrawn address still resolves: %v", err)
	}
	got, err := reg.ResolveIdentifier(ctx, fresh)
	if err != nil {
		t.Fatalf("ResolveIdentifier(new) = %v", err)
	}
	if got.ID != acct.ID {
		t.Errorf("resolved %s, want %s", got.ID, acct.ID)
	}

	// Everything else the account holds survives. A reissue is about one
	// address, not about the account.
	after, err := reg.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	var kept bool
	for _, i := range after.Identifiers {
		if i.Matches(c) {
			kept = true
		}
	}
	if !kept {
		t.Errorf("identifiers = %#v, want the card kept", after.Identifiers)
	}

	// Two events, because two things happened: a log that collapsed them could
	// not say when the old address stopped working.
	types := auditTypesFor(t, reg, acct.ID)
	var added, removed int
	for _, ty := range types {
		switch ty {
		case ledger.EventIdentifierAdded:
			added++
		case ledger.EventIdentifierRemoved:
			removed++
		}
	}
	if added != 1 || removed != 1 {
		t.Errorf("audit = %v, want one add and one remove from the reissue", types)
	}
}

func auditTypesFor(t *testing.T, reg *Register, id AccountID) []string {
	t.Helper()
	var types []string
	if err := reg.Store().View(context.Background(), func(ctx context.Context, tx Tx) error {
		events, err := tx.ListAudit(ctx, ledger.AuditFilter{
			BookID:   reg.BookID(),
			Scope:    ledger.ScopeDeposit,
			EntityID: string(id),
		})
		if err != nil {
			return err
		}
		for _, e := range events {
			types = append(types, e.Type)
		}
		return nil
	}); err != nil {
		t.Fatalf("reading the audit trail: %v", err)
	}
	return types
}
