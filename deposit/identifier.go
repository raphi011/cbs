package deposit

import (
	"fmt"

	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/ledger"
)

// IdentifierScheme names a way of addressing an account from outside the bank.
type IdentifierScheme string

// IdentifierIBAN is the only scheme this system issues today.
const IdentifierIBAN IdentifierScheme = "IBAN"

// Identifier is one external address for a deposit account: the thing a
// counterparty quotes when it wants to pay it. An account's own AccountID is
// never one of these — that is the bank's internal key and is not quoted.
type Identifier struct {
	Scheme IdentifierScheme
	Value  string
}

// MatchValue is the form two identifiers are compared BY, as against the form
// one is stored IN.
func (i Identifier) MatchValue() string {
	if i.Scheme != IdentifierIBAN {
		return i.Value
	}
	return iban.Compact(i.Value)
}

// Matches reports whether two identifiers name the same address: the same
// scheme, and the same value under that scheme's comparison rule.
func (i Identifier) Matches(o Identifier) bool {
	return i.Scheme == o.Scheme && i.MatchValue() == o.MatchValue()
}

// Validate checks that both halves are present, that the value is free of
// control characters, and — for an IBAN — that it is one.
func (i Identifier) Validate(field string) error {
	// ledger.ValidateText accepts the empty string — it only rejects invalid UTF-8
	// and control characters, and "" is valid UTF-8 with no runes to object to.
	if i.Scheme == "" || i.Value == "" {
		return fmt.Errorf("%s: an identifier needs both a scheme and a value: %w", field, ledger.ErrInvalidText)
	}
	if err := ledger.ValidateText(field+".scheme", string(i.Scheme)); err != nil {
		return err
	}
	if err := ledger.ValidateText(field+".value", i.Value); err != nil {
		return err
	}
	if i.Scheme == IdentifierIBAN {
		// Against the compact form, because that is what is stored and what the
		// checks are defined over.
		if err := iban.IBAN(iban.Compact(i.Value)).Validate(); err != nil {
			return fmt.Errorf("%s.value: %w", field, err)
		}
	}
	return nil
}
