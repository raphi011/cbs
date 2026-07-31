package iso20022

import (
	"fmt"
	"regexp"
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
