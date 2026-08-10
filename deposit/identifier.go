package deposit

import (
	"fmt"

	"github.com/raphi011/cbs/iban"
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

// MatchValue is the form two identifiers are compared BY, as against the form
// one is stored IN.
//
// What is STORED is compact: DE20999000010000000001, which is what the standard
// calls canonical and what a pacs.008 carries. What arrives from a person is
// whatever they read off a statement — grouped in fours, sometimes hyphenated,
// sometimes lower-cased — and it is the same address. Compaction is not
// reversible, so the only way to match the two is to compact BOTH sides, which
// is what this method exists to do.
//
// The rule is iban.Compact's, and this delegates rather than restates it. One
// copy survives elsewhere, in iso20022, because that package must import nothing
// from this repository; TestMatchValueAgreesWithTheWireCompaction pins the two
// together and is the reason there are two rather than three.
//
// Every other scheme matches EXACTLY. No other address kind this system carries
// has a display form — a card PAN is sixteen digits — and stripping punctuation
// out of arbitrary identifiers would silently merge two addresses that a scheme
// deliberately keeps apart.
//
// It changes nothing about what is stored or returned. A payment records the
// address it was quoted, a register keeps the value it was given, and only the
// COMPARISON is canonical.
func (i Identifier) MatchValue() string {
	if i.Scheme != IdentifierIBAN {
		return i.Value
	}
	return iban.Compact(i.Value)
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

// Validate checks that both halves are present, that the value is free of
// control characters, and — for an IBAN — that it is one.
//
// FORMAT IS PER SCHEME, and only one scheme here has a format to check. An IBAN
// carries its own structure and two kinds of check digit, so an Identifier
// claiming to be one and failing them is not an address at all; a card PAN is
// sixteen digits with an issuer this system has no table for, and nothing here
// can say more about it than that it is text.
//
// Checking it costs almost nothing on the paths that reach here, because none of
// them is where an IBAN normally arrives: this bank MINTS its customers'
// addresses (Register.OpenAccountTx), and AddIdentifier refuses the scheme
// outright. What this refuses is the address nobody can otherwise refuse — one
// reconstructed from a store row, a fixture or a future caller that has not been
// through the minter.
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
	if err := ledger.ValidateText(field+".value", i.Value); err != nil {
		return err
	}
	if i.Scheme == IdentifierIBAN {
		// Against the compact form, because that is what is stored and what the
		// checks are defined over. A grouped value reaching here is a caller
		// that has not normalised, and it is accepted: MatchValue already treats
		// the two spellings as one address, and refusing here would make them
		// disagree.
		if err := iban.IBAN(iban.Compact(i.Value)).Validate(); err != nil {
			return fmt.Errorf("%s.value: %w", field, err)
		}
	}
	return nil
}
