package deposit

import (
	"errors"
	"testing"

	"github.com/raphi011/cbs/iso20022"
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
	// Identifier is a comparable struct on purpose: a slice or a map in this
	// type would make even the zero-value test impossible. What == answers is
	// "written the same way", which is not "the same address" — Matches is
	// that question, and it is the one every caller deciding where money goes
	// asks.
	a := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}
	b := Identifier{Scheme: IdentifierIBAN, Value: "SE89-AURORA-1001"}
	if a != b {
		t.Fatal("identical identifiers compare unequal")
	}
	if (Identifier{}) != (Identifier{}) {
		t.Fatal("the zero identifier does not compare equal to itself")
	}
}

func TestMatchValueStripsIBANSeparatorsAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ident Identifier
		want  string
	}{
		{"hyphenated display form", Identifier{IdentifierIBAN, "SE89-AURORA-1001"}, "SE89AURORA1001"},
		{"grouped with spaces", Identifier{IdentifierIBAN, "DE89 3704 0044 0532 0130 00"}, "DE89370400440532013000"},
		{"already compact", Identifier{IdentifierIBAN, "DE89370400440532013000"}, "DE89370400440532013000"},
		// Only the IBAN scheme has a display form. Stripping punctuation out of
		// every identifier would merge a card PAN written with separators into
		// one written without, and those are addresses a scheme keeps apart.
		{"another scheme keeps its value", Identifier{IdentifierScheme("PAN"), "4111-1111"}, "4111-1111"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ident.MatchValue(); got != tc.want {
				t.Errorf("MatchValue() = %q, want %q", got, tc.want)
			}
		})
	}
}

// Matches is the comparison both stores must use, and it is not ==: two
// identifiers that differ only in an IBAN's display separators name one
// address, and two that differ in scheme never do.
func TestIdentifierMatches(t *testing.T) {
	stored := Identifier{IdentifierIBAN, "SE89-AURORA-1001"}
	compact := Identifier{IdentifierIBAN, "SE89AURORA1001"}
	if !stored.Matches(compact) || !compact.Matches(stored) {
		t.Error("an IBAN does not match itself across display forms; a payment message could not resolve it")
	}
	if stored == compact {
		t.Error("the two forms are the same string; this test is not testing what it claims")
	}
	if stored.Matches(Identifier{IdentifierIBAN, "SE89-AURORA-1002"}) {
		t.Error("two different account numbers matched")
	}
	if stored.Matches(Identifier{IdentifierScheme("PAN"), "SE89AURORA1001"}) {
		t.Error("identifiers in different schemes matched")
	}
}

// The separator set is written twice — here and in iso20022.IBAN.Compact — for
// a reason each side states: that package must import nothing from this
// repository, and this one must not acquire a message codec to compare two
// strings. Two copies of a rule drift, so this is the test that stops them.
//
// It imports iso20022 from a TEST file, which costs the package nothing: a test
// import is not part of the dependency graph, and the direction that is
// forbidden — iso20022 importing this repository — is unaffected and is itself
// pinned by TestPackageImportsNothingFromThisRepository.
//
// The cases include every separator either side knows and a value carrying
// both, so a set that gained a character on one side alone fails here rather
// than in a payment that cannot be resolved.
func TestMatchValueAgreesWithTheWireCompaction(t *testing.T) {
	for _, value := range []string{
		"SE89-AURORA-1001",
		"DE89 3704 0044 0532 0130 00",
		"IT60 X054-2811 1010-0000 0123 456",
		"DE89370400440532013000",
		"",
		"----",
		"    ",
	} {
		got := Identifier{Scheme: IdentifierIBAN, Value: value}.MatchValue()
		want := string(iso20022.IBAN(value).Compact())
		if got != want {
			t.Errorf("MatchValue(%q) = %q, iso20022.IBAN.Compact = %q; the two separator sets have drifted apart",
				value, got, want)
		}
	}
}
