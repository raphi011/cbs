package iban

import (
	"errors"
	"testing"
)

// swap returns s with the characters at i and j exchanged.
func swap(s string, i, j int) string {
	b := []byte(s)
	b[i], b[j] = b[j], b[i]
	return string(b)
}

// sub returns s with the character at i replaced.
func sub(s string, i int, c byte) string {
	b := []byte(s)
	b[i] = c
	return string(b)
}

// reseal recomputes the international check digits over a BBAN somebody has
// tampered with, producing an address that passes mod-97 and is still wrong.
//
// This is not a hypothetical attack. It is what happens when an address is
// BUILT from a wrong domestic account number: the builder computes the two
// digits correctly over bad input, and the result is a well-formed IBAN for an
// account that does not exist. It is exactly the case a national check is still
// in the standard for.
func reseal(compact string) string {
	c := Country(compact[:2])
	return string(c) + checkDigits(c, compact[4:]) + compact[4:]
}

// TestTheNationalCheckCatchesWhatModNinetySevenCannot is the reason Italy and
// France still carry a check character of their own.
//
// A resealed address is internationally perfect: every bank in Europe running
// the only check it is required to run accepts it. The country that issued the
// address does not, because its own check is computed over a different span with
// different weights and the tampering does not survive both.
func TestTheNationalCheckCatchesWhatModNinetySevenCannot(t *testing.T) {
	for _, tc := range []struct {
		name string
		iban string
		pos  int // a character inside the account number
		to   byte
	}{
		{"an Italian account number", "IT60X0542811101000000123456", 20, '9'},
		{"a French account number", "FR1420041010050500013M02606", 18, '9'},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tampered := reseal(sub(tc.iban, tc.pos, tc.to))
			if tampered == tc.iban {
				t.Fatal("the mutation changed nothing; fix the test, not the code")
			}
			if mod97(tampered) != 1 {
				t.Fatalf("%s: mod-97 already refuses it, so this proves nothing", tampered)
			}
			if err := IBAN(tampered).Validate(); !errors.Is(err, ErrNationalCheck) {
				t.Errorf("Validate(%s) = %v, want ErrNationalCheck", tampered, err)
			}
		})
	}
}

// digitsOf lists the positions of an address that a person could plausibly
// mistype into another character of the same kind: everything after the country
// code.
func digitsOf(s string) []int {
	var pos []int
	for i := 2; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			pos = append(pos, i)
		}
	}
	return pos
}

// TestWhatModNinetySevenCatches is the measured claim behind the whole task, and
// it is measured rather than asserted because "a check digit catches typos" is
// worth nothing without a number beside it.
//
// Measured over the four published addresses, exhaustively:
//
//   - SINGLE-CHARACTER substitutions, every digit position, every one of the
//     nine other digits: 810 mutations, 810 caught. All of them.
//   - TRANSPOSITIONS of two different digits, every pair of positions at every
//     distance, not only adjacent ones: 787 mutations, 787 caught. All of them.
//
// The second is stronger than the property usually quoted for this check, and it
// is not luck: transposing digits at distance d changes the value by a multiple
// of 10^d − 1, and 10 has multiplicative order 96 modulo 97 — the reason 1/97
// has a 96-digit repeating decimal. No address is 96 characters long, so no
// transposition of two digits can ever be missed.
//
// What it DOES miss is in the test below.
func TestWhatModNinetySevenCatches(t *testing.T) {
	subs, subsCaught := 0, 0
	trans, transCaught := 0, 0

	for _, p := range published {
		pos := digitsOf(p.iban)
		for _, i := range pos {
			for c := byte('0'); c <= '9'; c++ {
				if c == p.iban[i] {
					continue
				}
				subs++
				if mod97(sub(p.iban, i, c)) != 1 {
					subsCaught++
				}
			}
		}
		for a := range pos {
			for b := a + 1; b < len(pos); b++ {
				i, j := pos[a], pos[b]
				if p.iban[i] == p.iban[j] {
					continue // not an error: the address is unchanged
				}
				trans++
				if mod97(swap(p.iban, i, j)) != 1 {
					transCaught++
				}
			}
		}
	}

	t.Logf("substitutions: %d/%d caught; transpositions: %d/%d caught",
		subsCaught, subs, transCaught, trans)

	if subs != 810 || subsCaught != subs {
		t.Errorf("substitutions: %d/%d caught, the doc says 810/810", subsCaught, subs)
	}
	if trans != 787 || transCaught != trans {
		t.Errorf("transpositions: %d/%d caught, the doc says 787/787", transCaught, trans)
	}
}

// TestWhatModNinetySevenMisses is the other half, and the honest half.
//
// A single wrong character is always caught and so is any transposition. TWO
// wrong characters are not: if the two errors happen to cancel modulo 97, the
// address verifies. Measured exhaustively over DE89370400440532013000 — every
// pair of digit positions, every pair of replacement digits — 15,390 mutations
// leave 141 undetected, which is 0.92%, near the 1.03% a uniform residue would
// give.
//
// That figure is the argument for the national checks above and for everything
// downstream of this package. A check digit says an address was probably typed
// correctly. It never says the address exists.
func TestWhatModNinetySevenMisses(t *testing.T) {
	const address = "DE89370400440532013000"
	pos := digitsOf(address)

	total, missed := 0, 0
	for a := range pos {
		for b := a + 1; b < len(pos); b++ {
			for c1 := byte('0'); c1 <= '9'; c1++ {
				if c1 == address[pos[a]] {
					continue
				}
				for c2 := byte('0'); c2 <= '9'; c2++ {
					if c2 == address[pos[b]] {
						continue
					}
					total++
					if mod97(sub(sub(address, pos[a], c1), pos[b], c2)) == 1 {
						missed++
					}
				}
			}
		}
	}

	t.Logf("two-character errors: %d of %d undetected (%.2f%%)",
		missed, total, 100*float64(missed)/float64(total))

	if total != 15390 || missed != 141 {
		t.Errorf("two-character errors: %d of %d undetected, the doc says 141 of 15390", missed, total)
	}
}

// TestTheTwoLetterMapsDisagree pins the fact that makes the French check
// independent rather than decorative: mod-97-10 and the clé RIB do not agree on
// what a letter is worth, and the disagreement is not a constant offset.
func TestTheTwoLetterMapsDisagree(t *testing.T) {
	// S is 28 to mod-97-10 and 2 to the clé; J is 19 and 1. If the maps differed
	// by a constant, an error in one would be an error in the other and the
	// second check would add nothing.
	if ribLetter('S') == ribLetter('J') {
		t.Fatal("the clé map collapses S and J, so this test needs different letters")
	}
	gapMod := int('S'-'A') - int('J'-'A')
	gapRIB := ribLetter('S') - ribLetter('J')
	if gapMod == gapRIB {
		t.Errorf("the two maps put S and J the same distance apart (%d); they are supposed to disagree", gapMod)
	}
	// And the collapse runs the other way: A, J are one to the clé and two
	// different values to mod-97-10.
	if ribLetter('A') != ribLetter('J') {
		t.Errorf("the clé map is supposed to collapse A and J, got %d and %d", ribLetter('A'), ribLetter('J'))
	}
}
