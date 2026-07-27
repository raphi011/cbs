package ledger_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/raphi011/cbs/ledger"
)

// The corpus below is shared with store/storetest, which asserts the other half
// of the same rule: every string ValidateText accepts must round-trip through
// BOTH stores unchanged. Keep the two in step — a string this test moves from
// "rejected" to "accepted" is a string store/pg then has to be able to hold.

func TestValidateTextAcceptsOrdinaryText(t *testing.T) {
	for _, s := range []string{
		"",
		"Aurora Bank",
		"Crédit Soleil",            // Latin-1 supplement
		"Ålesund Sparebank",        // beyond ASCII in the first rune
		"三菱UFJ銀行",                  // CJK
		"Банк «Восток»",            // Cyrillic plus guillemets
		"🏦 Emoji Bank",             // outside the BMP
		`He said "50% off"; 'ok'`,  // quoting that a naive escaper would mangle
		`C:\path\to\nowhere`,       // backslashes, which JSON and SQL both care about
		"$1 -- DROP TABLE ledgers", // placeholder- and comment-shaped, but plain text
		strings.Repeat("x", 4096),  // long, to catch a column that silently truncates
	} {
		if err := ledger.ValidateText("name", s); err != nil {
			t.Errorf("ValidateText(%q) = %v, want nil", s, err)
		}
	}
}

func TestValidateTextRejectsControlCharactersAndInvalidUTF8(t *testing.T) {
	// Every one of these is legal in a Go string and legal in JSON, and every
	// one of them is refused by Postgres — the first two as SQLSTATE 22021 in a
	// text column, the NUL again as 22P05 inside jsonb. store/mem would store
	// them happily, which is exactly the asymmetry the domain rule removes.
	for _, tc := range []struct{ label, s string }{
		{"NUL", "Ban\x00k"},
		{"invalid UTF-8", "Ban\xffk"},
		{"a lone surrogate half", "Ban\xed\xa0\x80k"},
		{"newline", "Bank\nof Nowhere"},
		{"tab", "Bank\tof Nowhere"},
		{"escape", "Bank\x1b[31m"},
		{"DEL", "Bank\x7f"},
		{"C1 control", "Bank\u0085"},
	} {
		err := ledger.ValidateText("name", tc.s)
		if !errors.Is(err, ledger.ErrInvalidText) {
			t.Errorf("ValidateText(%s = %q) = %v, want ErrInvalidText", tc.label, tc.s, err)
			continue
		}
		// The field name is in the message, because a 400 that does not say
		// which field was rejected is not actionable.
		if !strings.Contains(err.Error(), "name") {
			t.Errorf("ValidateText(%s) error %q does not name the field", tc.label, err)
		}
	}
}

func TestValidateTextMapChecksKeysAndValues(t *testing.T) {
	if err := ledger.ValidateTextMap("metadata", map[string]string{"ref": "INV-2025-77"}); err != nil {
		t.Errorf("ValidateTextMap on ordinary metadata = %v, want nil", err)
	}
	if err := ledger.ValidateTextMap("metadata", nil); err != nil {
		t.Errorf("ValidateTextMap(nil) = %v, want nil", err)
	}
	// A NUL in a jsonb value is SQLSTATE 22P05, a different code from the text
	// column's 22021 — so a fix that only looked at names would still diverge.
	if err := ledger.ValidateTextMap("metadata", map[string]string{"ref": "INV\x00"}); !errors.Is(err, ledger.ErrInvalidText) {
		t.Errorf("ValidateTextMap with a NUL value = %v, want ErrInvalidText", err)
	}
	if err := ledger.ValidateTextMap("metadata", map[string]string{"re\x00f": "INV"}); !errors.Is(err, ledger.ErrInvalidText) {
		t.Errorf("ValidateTextMap with a NUL key = %v, want ErrInvalidText", err)
	}
}

// A map has no order, so the error a bad map produces must not depend on which
// key Go's iteration happens to visit first: two keys are bad here, and the
// message has to name the same one on every run.
func TestValidateTextMapErrorIsDeterministic(t *testing.T) {
	m := map[string]string{"alpha": "a\x00", "beta": "b\x00", "gamma": "c\x00"}
	first := ledger.ValidateTextMap("metadata", m).Error()
	for range 50 {
		if got := ledger.ValidateTextMap("metadata", m).Error(); got != first {
			t.Fatalf("ValidateTextMap is not deterministic: got %q, then %q", first, got)
		}
	}
}
