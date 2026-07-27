package ledger

import (
	"fmt"
	"maps"
	"slices"
	"unicode"
	"unicode/utf8"
)

// Text validation.
//
// # Why this lives in the domain
//
// store/mem is a map of Go strings and will hold any byte sequence at all.
// Postgres will not: a NUL is SQLSTATE 22021 in a text column and SQLSTATE
// 22P05 inside jsonb, and so is any byte sequence that is not valid UTF-8. So
// `POST /participants` with `{"name":"Ban\u0000k"}` — legal JSON — created a
// participant on one store and returned a 500 carrying a raw SQLSTATE on the
// other.
//
// That breaks the one rule store/storetest exists to enforce: store/pg must
// never accept or refuse a write that store/mem handles differently. The fix
// belongs here rather than in either store, for the same reason there is no
// UNIQUE (book_id, name) in the schema: a store is a per-table key/value layer,
// and the moment one of them can express a rule the other cannot, the two
// disagree. What the system will accept is a domain question, so the domain
// answers it once, for both.
//
// # Where the boundary sits
//
// Every caller-supplied string that reaches a store — as a value it stores or
// as a key it looks a row up by — must be valid UTF-8 and free of control
// characters. Concretely that is:
//
//   - names: ledgers, subledgers, GL accounts, deposit accounts, participants;
//   - free text: transaction and hold descriptions, reject and return reasons;
//   - client references: idempotency keys, end-to-end ids, IBANs;
//   - metadata, both keys and values;
//   - identifiers a request supplies for lookup: account ids in a posting's
//     entries, the deposit account in a funding request, a payment's party
//     refs.
//
// System-generated identifiers (`bank_1`, `200.100.001`, `dep_3`) contain no
// control characters by construction, so an identifier that does cannot name
// anything; refusing it is the same answer as looking it up, minus the
// backend-specific failure. Identifiers arriving in a URL are screened at the
// API edge instead — see the api package's middleware — because they never pass
// through a domain constructor.
//
// # Why control characters and not only the two bytes Postgres refuses
//
// Rejecting exactly NUL and invalid UTF-8 would close the parity gap and
// nothing else, and it would leave a rule no one can state without naming a
// database. Every field above is a single-line label rendered in tables, log
// lines, CSV exports and JSON audit payloads; a tab, a newline or an ANSI
// escape in a bank's name has no legitimate use and several illegitimate ones.
// One rule — valid UTF-8, no control characters — covers both, is checkable
// without reference to a backend, and is the same rule on every field.
//
// Text is not otherwise constrained: length is unbounded, every printable
// Unicode character is allowed, and an empty string is valid (the domain's
// required-ness rules, where it has any, are separate from this).

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
//
// Keys are checked too, because they are stored as jsonb object names and a NUL
// in one is refused exactly as a NUL in a value is. The keys are visited in
// sorted order so that a map with several bad entries always reports the same
// one: Go's map iteration is randomised, and an error message that changes from
// run to run is one no test can pin.
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
