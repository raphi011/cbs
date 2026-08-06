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
// It was written for a divergence, and it outlived the divergence.
//
// While there were two stores, store/mem was a map of Go strings and held any
// byte sequence at all; Postgres would not — a NUL is SQLSTATE 22021 in a text
// column and 22P05 inside jsonb, and so is anything that is not valid UTF-8 —
// so `POST /participants` with `{"name":"Ban\u0000k"}`, legal JSON, created a
// participant on one store and returned a 500 carrying a raw SQLSTATE on the
// other. The fix went here rather than into either store.
//
// store/sqlite holds every one of those bytes happily, so there is nothing left
// to diverge, and the rule stays. The divergence was the occasion for it and
// never the reason: a store is a per-table key/value layer that holds what it is
// handed, what the system will ACCEPT is a domain question, and a rule that can
// only be stated by naming a database is not a domain rule. It is the same
// position the schema takes about the absent UNIQUE (book_id, name) and about
// parent references — decided once, above the store, where it does not depend on
// what is underneath. The section below is the wider half, and it was the wider
// half even when Postgres was the reason anyone looked.
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
// # Why control characters and not only the two bytes Postgres refused
//
// Rejecting exactly NUL and invalid UTF-8 would have closed the parity gap and
// nothing else, and it would have left a rule no one can state without naming a
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
// Keys are checked too, because a metadata map is stored as a JSON document and
// a key is an object NAME: it reaches the same column a value does, and there is
// no reason for one side of a pair to be held to a weaker rule than the other.
// The keys are visited in
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
