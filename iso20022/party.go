package iso20022

import (
	"fmt"
	"regexp"
	"strings"
)

// bicPattern is ISO 9362: four alphabetic characters identifying the
// institution, two alphabetic identifying the country, two alphanumeric
// identifying the location, and an optional three alphanumeric identifying the
// branch. A BIC is therefore 8 or 11 characters and never 9 or 10 — a fact
// worth pinning, because a truncated 11-character code looks plausible.
var bicPattern = regexp.MustCompile(`^[A-Z]{6}[A-Z0-9]{2}([A-Z0-9]{3})?$`)

// BIC is a business identifier code: the address of a financial institution.
//
// In pacs.008 it appears as BICFI inside DbtrAgt and CdtrAgt, and it is
// MANDATORY there — which is why sub-project 7 reopened sub-project 5's
// decision to defer bank-level addressing. A SEPA payment routes agent first
// and account second: the BIC says which bank, the IBAN says which account
// within it.
type BIC string

// Validate reports whether the code is structurally a BIC.
//
// It is a structural check only. Whether a well-formed BIC belongs to a bank
// that exists is a directory question, and SWIFT's directory is not something
// this repository models.
func (b BIC) Validate() error {
	if !bicPattern.MatchString(string(b)) {
		return fmt.Errorf("%w: %q", ErrBICFormat, string(b))
	}
	return nil
}

// ibanPattern is the schema's IBAN2007Identifier: two alphabetic characters for
// the country, two digits for the check digits, and up to thirty alphanumeric
// characters for the basic bank account number.
//
// Note what it does NOT constrain: the check digits are two DIGITS, and nothing
// says they are the correct ones. See IBAN.
var ibanPattern = regexp.MustCompile(`^[A-Z]{2}[0-9]{2}[A-Za-z0-9]{1,30}$`)

// ibanSeparators are the characters an IBAN may be displayed with and is never
// stored or transmitted with.
var ibanSeparators = strings.NewReplacer(" ", "", "-", "")

// IBAN is an international bank account number.
//
// # This type does not verify the check digit, on purpose
//
// A real IBAN's third and fourth characters are an ISO 7064 mod-97 checksum
// over the rest, and this package does not compute it. That is inherited from
// sub-project 5, which refused mod-97 validation because it would have made the
// seed's readable SE89-AURORA-1001 illegal and replaced it with opaque digits
// in every screenshot, worked example and quiz answer in the repository.
//
// The refusal costs nothing here, which is the part worth knowing. The schema
// constrains an IBAN by PATTERN and not by checksum, so a readable identifier
// still produces a structurally valid document. Validate therefore checks the
// pattern, and its failure is ErrIBANPattern rather than a checksum error, so
// that the distinction survives in the error a caller sees.
//
// # Compact and display forms
//
// An IBAN is canonically stored and transmitted without separators, and
// displayed in groups of four. This repository's stored identifiers use hyphens
// for readability, so Compact is what turns a stored deposit.Identifier value
// into the form that goes on the wire.
//
// Compaction is NOT reversible: SE89AURORA1001 cannot tell you where the
// hyphens were. Code matching a received IBAN against a stored identifier must
// therefore compact BOTH sides and compare, rather than compacting one and
// hoping. That is sub-project 7b's problem, and this comment is where it is
// recorded.
type IBAN string

// Compact returns the IBAN with display separators removed.
func (i IBAN) Compact() IBAN { return IBAN(ibanSeparators.Replace(string(i))) }

// Validate reports whether the compact form matches the schema's pattern. It
// does not verify the check digit; see the type documentation.
func (i IBAN) Validate() error {
	if !ibanPattern.MatchString(string(i.Compact())) {
		return fmt.Errorf("%w: %q", ErrIBANPattern, string(i))
	}
	return nil
}
