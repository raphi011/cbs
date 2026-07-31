// This file's tests live in package deposit_test, dot-importing deposit, for
// the same reason register_test.go does: newTestRegister builds a Register
// over store/mem, and store/mem imports deposit, so an in-package test file
// using it would be an import cycle. See register_test.go's package comment.
package deposit_test

import (
	"context"
	"errors"
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
	first, err := reg.OpenAccount(ctx, sub, "Alice", testAsset, prd, 0, iban)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	second, err := reg.OpenAccount(ctx, sub, "Aaron", testAsset, prd, 0)
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	_ = first

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
