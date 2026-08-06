// This file's tests live in package deposit_test, dot-importing deposit, for
// the same reason register_test.go does: newTestRegister builds a Register over
// a store from store/testenv, which reaches store/sqlite, which imports
// deposit, so an in-package test file using it would be an import cycle. See
// register_test.go's package comment.
package deposit_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	. "github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
)

func TestOpenAccountWithIdentifier(t *testing.T) {
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)
	iban := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}

	acct, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0, iban)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	if len(acct.Identifiers) != 1 || acct.Identifiers[0] != iban {
		t.Fatalf("identifiers = %#v, want [%#v]", acct.Identifiers, iban)
	}

	got, err := reg.ResolveIdentifier(ctx, iban)
	if err != nil {
		t.Fatalf("ResolveIdentifier: %v", err)
	}
	if got.ID != acct.ID {
		t.Fatalf("resolved %s, want %s", got.ID, acct.ID)
	}
}

func TestOpenAccountWithoutIdentifierIsNormal(t *testing.T) {
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)

	acct, err := reg.OpenAccount(ctx, sub, "Plumbing", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	if len(acct.Identifiers) != 0 {
		t.Fatalf("identifiers = %#v, want none", acct.Identifiers)
	}
}

func TestAddAndRemoveIdentifier(t *testing.T) {
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)
	acct, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	iban := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}

	if err := reg.AddIdentifier(ctx, acct.ID, iban); err != nil {
		t.Fatalf("AddIdentifier: %v", err)
	}
	if _, err := reg.ResolveIdentifier(ctx, iban); err != nil {
		t.Fatalf("ResolveIdentifier after add: %v", err)
	}

	if err := reg.RemoveIdentifier(ctx, acct.ID, iban); err != nil {
		t.Fatalf("RemoveIdentifier: %v", err)
	}
	if _, err := reg.ResolveIdentifier(ctx, iban); !errors.Is(err, ErrIdentifierNotFound) {
		t.Fatalf("ResolveIdentifier after remove = %v, want ErrIdentifierNotFound", err)
	}
}

// The same address twice in ONE OpenAccount call. checkIdentifierFreeTx only
// sees accounts already in the register, and the account being opened is not
// one of them, so nothing else catches this — and the API forwards the list
// from a request body verbatim. Left alone the list means two things at once:
// the store's identifier rows key on (scheme, value) and keep one, and the Go
// slice the caller sent holds two.
func TestOpenAccountRefusesTheSameIdentifierTwice(t *testing.T) {
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)
	iban := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}

	_, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0, iban, iban)
	if !errors.Is(err, ErrIdentifierTaken) {
		t.Fatalf("OpenAccount with a repeated identifier = %v, want ErrIdentifierTaken", err)
	}

	// And nothing was opened: the check runs before the GL account is created.
	if _, err := reg.ResolveIdentifier(ctx, iban); !errors.Is(err, ErrIdentifierNotFound) {
		t.Fatalf("ResolveIdentifier = %v, want ErrIdentifierNotFound", err)
	}
}

func TestAddIdentifierRefusesADuplicateAtTheSameBank(t *testing.T) {
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)
	iban := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}

	if _, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0, iban); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	other, err := reg.OpenAccount(ctx, sub, "Aaron", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	if err := reg.AddIdentifier(ctx, other.ID, iban); !errors.Is(err, ErrIdentifierTaken) {
		t.Fatalf("AddIdentifier = %v, want ErrIdentifierTaken", err)
	}
	// And the same rule at open, which is a different code path.
	if _, err := reg.OpenAccount(ctx, sub, "Annie", testAsset, prd, 0, iban); !errors.Is(err, ErrIdentifierTaken) {
		t.Fatalf("OpenAccount with a taken identifier = %v, want ErrIdentifierTaken", err)
	}
}

func TestAddIdentifierIsIdempotentForTheSameAccount(t *testing.T) {
	// Re-adding an identifier the account already holds is not a collision with
	// somebody else — it is a no-op. Refusing it would make a retried request
	// fail on its second delivery.
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)
	iban := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}
	acct, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0, iban)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	if err := reg.AddIdentifier(ctx, acct.ID, iban); err != nil {
		t.Fatalf("AddIdentifier (repeat) = %v, want nil", err)
	}
	got, err := reg.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if len(got.Identifiers) != 1 {
		t.Fatalf("identifiers = %#v, want exactly one", got.Identifiers)
	}
}

func TestIdentifierMutationsAreAudited(t *testing.T) {
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)
	acct, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	iban := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}
	if err := reg.AddIdentifier(ctx, acct.ID, iban); err != nil {
		t.Fatalf("AddIdentifier: %v", err)
	}
	if err := reg.RemoveIdentifier(ctx, acct.ID, iban); err != nil {
		t.Fatalf("RemoveIdentifier: %v", err)
	}

	// ledger.Tx.ListAudit takes a filter (ledger/store.go:124,
	// ledger/audit.go:152), so the account's own events come back directly.
	var types []string
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
			types = append(types, e.Type)
		}
		return nil
	}); err != nil {
		t.Fatalf("reading the audit trail: %v", err)
	}

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
	reg, _, sub, prd := newTestRegister(t)
	iban := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}
	if _, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0, iban); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	second, err := reg.OpenAccount(ctx, sub, "Aaron", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	// Write the duplicate straight through the store, past the register's check.
	if err := reg.Store().Update(ctx, func(ctx context.Context, tx Tx) error {
		a, err := tx.GetDepositAccount(ctx, reg.BookID(), second.ID)
		if err != nil {
			return err
		}
		a.Identifiers = append(a.Identifiers, iban)
		return tx.PutDepositAccount(ctx, reg.BookID(), a)
	}); err != nil {
		t.Fatalf("seeding the duplicate: %v", err)
	}

	if _, err := reg.ResolveIdentifier(ctx, iban); !errors.Is(err, ErrIdentifierAmbiguous) {
		t.Fatalf("ResolveIdentifier = %v, want ErrIdentifierAmbiguous", err)
	}
}

// ---------------------------------------------------------------------------
// One address, two spellings
// ---------------------------------------------------------------------------
//
// An IBAN is stored here in its readable display form and transmitted compact,
// and the two are one address (Identifier.MatchValue). These tests pin that the
// rule is ONE rule: what routing treats as the same address, uniqueness,
// addition and withdrawal treat as the same address too. A layer that disagreed
// with the others would be discovered by a payment, not by a compiler.

var (
	displayIBAN = Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}
	compactIBAN = Identifier{Scheme: IdentifierIBAN, Value: "SE89AURORA1001"}
)

// A bank that has issued an address has issued it in every spelling. Letting a
// second account take the other one would mint an address that resolves to two
// accounts — which ResolveIdentifier then refuses for both of them, so the
// damage lands on the innocent account as well as the new one.
//
// This is the write-side half of the read-side change, and it arrived through
// the lookup rather than by editing this rule. It is pinned so that it is a
// decision rather than a side effect.
func TestAddIdentifierRefusesAnotherSpellingOfAnAddressTheBankHasIssued(t *testing.T) {
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)
	if _, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0, displayIBAN); err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	aaron, err := reg.OpenAccount(ctx, sub, "Aaron", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	if err := reg.AddIdentifier(ctx, aaron.ID, compactIBAN); !errors.Is(err, ErrIdentifierTaken) {
		t.Fatalf("AddIdentifier(compact form of another account's IBAN) = %v, want ErrIdentifierTaken", err)
	}
	// And the original account is untouched — the refusal is a refusal, not a
	// partial write.
	got, err := reg.ResolveIdentifier(ctx, compactIBAN)
	if err != nil {
		t.Fatalf("ResolveIdentifier after the refusal: %v", err)
	}
	if got.Name != "Alice" {
		t.Fatalf("the address now resolves to %s, want Alice", got.Name)
	}
}

// The same address in the other spelling is not new information, so adding it
// is the no-op that adding it twice already was.
//
// The consequence of getting this wrong is not a duplicate row: it is an
// account that holds what looks like two addresses, which makes every payment
// that quotes NEITHER of them ErrAmbiguousAddress — the account loses the
// ability to be paid without one being named, and nothing reports it, because
// both spellings resolve to it perfectly well.
func TestAddIdentifierIsANoOpForAnotherSpellingTheAccountAlreadyHolds(t *testing.T) {
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)
	acct, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0, displayIBAN)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	if err := reg.AddIdentifier(ctx, acct.ID, compactIBAN); err != nil {
		t.Fatalf("AddIdentifier(another spelling of an address the account holds) = %v, want a no-op", err)
	}
	after, err := reg.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if len(after.Identifiers) != 1 {
		t.Fatalf("identifiers = %#v, want the one address the account already had", after.Identifiers)
	}
	// The STORED spelling is the one kept. Rewriting it would edit what a
	// statement shows and what every earlier payment recorded, on the strength
	// of a call that told this bank nothing.
	if after.Identifiers[0] != displayIBAN {
		t.Errorf("identifier = %#v, want the stored display form %#v", after.Identifiers[0], displayIBAN)
	}
}

// The sibling check inside one OpenAccount call, which is the path a request
// body reaches directly: [X, X] is refused, and so is [X, X-written-differently],
// because they are the same list.
func TestOpenAccountRefusesTwoSpellingsOfOneAddressInOneCall(t *testing.T) {
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)

	_, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0, displayIBAN, compactIBAN)
	if !errors.Is(err, ErrIdentifierTaken) {
		t.Fatalf("OpenAccount with both spellings of one IBAN = %v, want ErrIdentifierTaken", err)
	}
	// Nothing was opened: the refusal happens before any write, so the address
	// is still free.
	if _, err := reg.ResolveIdentifier(ctx, displayIBAN); !errors.Is(err, ErrIdentifierNotFound) {
		t.Fatalf("ResolveIdentifier after the refusal = %v, want ErrIdentifierNotFound", err)
	}
}

// Withdrawal takes the address, whichever spelling names it.
//
// Removing an identifier that is not held is a no-op by design, so a literal
// comparison here fails SILENTLY: a bank quoting the compact form would believe
// it had withdrawn an address that is still live and still payable, with no
// error anywhere to say otherwise. That is the worst of the three sites to get
// wrong, which is why it is not left exact.
func TestRemoveIdentifierWithdrawsTheAddressInEitherSpelling(t *testing.T) {
	ctx := context.Background()
	reg, _, sub, prd := newTestRegister(t)
	acct, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0, displayIBAN)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}

	if err := reg.RemoveIdentifier(ctx, acct.ID, compactIBAN); err != nil {
		t.Fatalf("RemoveIdentifier(the compact spelling): %v", err)
	}
	after, err := reg.GetAccount(ctx, acct.ID)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if len(after.Identifiers) != 0 {
		t.Fatalf("identifiers = %#v, want the address withdrawn", after.Identifiers)
	}
	if _, err := reg.ResolveIdentifier(ctx, displayIBAN); !errors.Is(err, ErrIdentifierNotFound) {
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
	if !strings.Contains(payloads[0], displayIBAN.Value) {
		t.Errorf("identifier.removed payload = %s, want the stored form %q", payloads[0], displayIBAN.Value)
	}
}
