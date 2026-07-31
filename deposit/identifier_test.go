package deposit

import (
	"errors"
	"testing"

	"github.com/raphi011/cbs/ledger"
)

func TestIdentifierValidate(t *testing.T) {
	cases := []struct {
		name    string
		ident   Identifier
		wantErr bool
	}{
		{"ok", Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}, false},
		{"empty scheme", Identifier{Value: "SE89-AURORA-1001"}, true},
		{"empty value", Identifier{Scheme: IdentifierIBAN}, true},
		{"control character in value", Identifier{Scheme: IdentifierIBAN, Value: "SE89\x00"}, true},
		{"control character in scheme", Identifier{Scheme: IdentifierScheme("IB\x00AN"), Value: "x"}, true},
		// Format is deliberately unvalidated: no check digit, no length rule.
		// The seed's readable pseudo-IBANs must stay legal.
		{"not a real IBAN but accepted", Identifier{Scheme: IdentifierIBAN, Value: "hello world"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.ident.Validate("debtor.identifier")
			if c.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want an error")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestIdentifierValidateReportsTheField(t *testing.T) {
	err := Identifier{Scheme: IdentifierIBAN, Value: "x\x00y"}.Validate("creditor.identifier")
	if err == nil {
		t.Fatal("Validate() = nil, want an error")
	}
	if !errors.Is(err, ledger.ErrInvalidText) {
		t.Fatalf("Validate() = %v, want it to wrap ledger.ErrInvalidText", err)
	}
}

func TestIdentifierEquality(t *testing.T) {
	// Identifier is a comparable struct on purpose: Register.RemoveIdentifier
	// and the uniqueness check both compare with ==, and a slice or a map in
	// this type would break that.
	a := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}
	b := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}
	if a != b {
		t.Fatal("identical identifiers compare unequal")
	}
}
