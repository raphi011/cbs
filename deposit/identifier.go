package deposit

import (
	"fmt"
	"strings"

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
// The struct is comparable: it holds two strings and nothing else. What ==
// tests is whether two identifiers are written the same way, which is NOT the
// same question as whether they name the same address — see Matches, which is
// what routing, uniqueness, addition and withdrawal all use. == survives for
// the questions it does answer: whether an identifier is the zero value, and
// whether a stored value is literally unchanged.
type Identifier struct {
	Scheme IdentifierScheme
	Value  string
}

// ibanSeparators are the characters an IBAN is DISPLAYED with and is never
// stored or transmitted with.
//
// The set is the same one iso20022.IBAN.Compact strips, and it is written out
// twice rather than shared because that package must import nothing from this
// repository and this one must not acquire a message codec to compare two
// strings. Two copies of a rule is a drift risk, so it is pinned:
// TestMatchValueAgreesWithTheWireCompaction fails if either side learns a
// separator the other has not.
var ibanSeparators = strings.NewReplacer(" ", "", "-", "")

// MatchValue is the form two identifiers are compared BY, as against the form
// one is stored IN.
//
// For an IBAN those differ, and the difference is a fact about IBANs rather than
// about this repository: the canonical stored and transmitted form has no
// separators, and the DISPLAY form is grouped for readability. This repository
// writes the display form — seed.go's SE89-AURORA-1001 is what a statement, a
// worked example and a screenshot all show — while an IBAN arriving in a
// pacs.008 is compact, because that is the only form the schema's pattern
// admits. They are one address.
//
// Compaction is not reversible: SE89AURORA1001 cannot say where the hyphens
// were. So the only way to match a received address against a stored one is to
// compact BOTH sides, which is what this method exists to do — see
// iso20022.IBAN.Compact, whose doc records the same rule from the message side.
// Before it existed, this system emitted an address it could then not resolve,
// and every account the seed creates was unreachable from the wire.
//
// Every other scheme matches EXACTLY. No other address kind this system carries
// has a display form — a card PAN is sixteen digits — and stripping punctuation
// out of arbitrary identifiers would silently merge two addresses that a scheme
// deliberately keeps apart.
//
// It changes nothing about what is stored or returned. A payment records the
// address it was quoted, a register keeps the value it was given, and only the
// COMPARISON is canonical. That is why it is a method here rather than a
// normalisation at write time: normalising on the way in would replace the
// readable form the rest of the repository teaches with opaque digits, which is
// the trade sub-project 5 already refused when it declined mod-97 validation.
func (i Identifier) MatchValue() string {
	if i.Scheme != IdentifierIBAN {
		return i.Value
	}
	return ibanSeparators.Replace(i.Value)
}

// Matches reports whether two identifiers name the same address: the same
// scheme, and the same value under that scheme's comparison rule.
//
// A store must use this for ListDepositAccountsByIdentifier rather than ==, and
// it is the only thing it may use — a store comparing raw values leaves an
// account opened as SE89-AURORA-1001 unreachable from a message carrying
// SE89AURORA1001, which is the same address. A store expressing this in SQL is
// re-implementing this function, so it is pinned by
// storetest/ListDepositAccountsByIdentifierMatchesAnIBANThroughItsSeparators.
func (i Identifier) Matches(o Identifier) bool {
	return i.Scheme == o.Scheme && i.MatchValue() == o.MatchValue()
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
