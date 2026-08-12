package ledger

import (
	"fmt"
	"maps"
	"slices"
	"unicode"
	"unicode/utf8"
)

// Text validation.

// ValidateText reports whether a caller-supplied string may be stored or used
// as a lookup key. field names the offending field in the error, so the 400 the
// API renders says which one to fix.
func ValidateText(field, s string) error {
	if !utf8.ValidString(s) {
		return fmt.Errorf("%s: %w: not valid UTF-8", field, ErrInvalidText)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s: %w: contains U+%04X", field, ErrInvalidText, r)
		}
	}
	return nil
}

// ValidateTextMap applies ValidateText to a map's keys and its values.
func ValidateTextMap(field string, m map[string]string) error {
	for _, k := range slices.Sorted(maps.Keys(m)) {
		if err := ValidateText(field+" key", k); err != nil {
			return err
		}
		if err := ValidateText(field+"["+k+"]", m[k]); err != nil {
			return err
		}
	}
	return nil
}
