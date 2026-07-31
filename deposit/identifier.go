package deposit

import (
	"fmt"

	"github.com/raphi011/cbs/ledger"
)

// IdentifierScheme names a way of addressing an account from outside the bank.
//
// It is deliberately not the same thing as a payment scheme. "SEPA Credit
// Transfer" is a product; "IBAN" is the kind of address that product carries,
// and both SEPA schemes carry the same one.
type IdentifierScheme string

// IdentifierIBAN is the only scheme this system issues today.
//
// Others exist in the world and drop in as constants: a UK sort code plus
// account number, a US routing number plus account number, a card PAN (which is
// an alias for a funding account and nothing more), and the proxy aliases —
// phone, email — that instant schemes resolve through a central service. Only
// the last kind cannot live here; see the comment on Register.AddIdentifier.
const IdentifierIBAN IdentifierScheme = "IBAN"

// Identifier is one external address for a deposit account: the thing a
// counterparty quotes when it wants to pay it. An account's own AccountID is
// never one of these — that is the bank's internal key and is not quoted.
//
// This mirrors ISO 20022's CashAccountIdentification, which is a choice between
// an IBAN and a generic (identification, scheme name, issuer) triple rather
// than an IBAN field. Modelling it as a pair is what makes a card PAN a new
// constant instead of a schema change.
//
// The struct is comparable: it holds two strings and nothing else, so == is the
// identity test, which is what RemoveIdentifier and the uniqueness check use.
type Identifier struct {
	Scheme IdentifierScheme
	Value  string
}

// Validate checks that both halves are present and free of control characters.
//
// It does NOT check the format of the value. There is no mod-97 check digit on
// an IBAN here and no length rule, because enforcing one would make the seed's
// readable SE89-AURORA-1001 illegal and replace it with opaque digits in every
// worked example in the repository. Format is a per-scheme concern this system
// deliberately does not implement; see the design spec.
func (i Identifier) Validate(field string) error {
	// ledger.ValidateText accepts the empty string — it only rejects invalid
	// UTF-8 and control characters, and "" is valid UTF-8 with no runes to
	// object to. So the presence check has to be explicit; ValidateText alone
	// would let a zero-value Identifier through.
	if i.Scheme == "" || i.Value == "" {
		return fmt.Errorf("%s: an identifier needs both a scheme and a value: %w", field, ledger.ErrInvalidText)
	}
	if err := ledger.ValidateText(field+".scheme", string(i.Scheme)); err != nil {
		return err
	}
	return ledger.ValidateText(field+".value", i.Value)
}
