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
func reseal(compact string) string {
	c := Country(compact[:2])
	return string(c) + checkDigits(c, compact[4:]) + compact[4:]
}

// TestTheNationalCheckCatchesWhatModNinetySevenCannot is the reason Italy and
// France still carry a check character of their own.
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

// TestWhatModNinetySevenCatches is the measured claim behind the whole task,
// and it is measured rather than asserted because "a check digit catches typos"
// is worth nothing without a number beside it.
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
