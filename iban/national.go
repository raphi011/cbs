package iban

// The two national check characters, which exist because national schemes
// predate ISO 13616 and were never retired when it arrived.
//
// Both are computed over the BBAN and are therefore INSIDE what mod-97-10 then
// covers, so an address carries two checks that are genuinely independent:
// different spans, different weights, and — France's case — a different idea of
// what a letter is worth. That independence is the point. A second check that
// agreed with the first about everything would catch nothing new.

// cinOdd is the value of a character in an ODD position (1-indexed) of the span
// the CIN covers. It is a lookup table and not a formula; there is no pattern in
// it to derive.
//
// The digits repeat the values of A–J, which is why '0' is 1 and '1' is 0 rather
// than the other way round.
var cinOdd = map[rune]int{
	'0': 1, '1': 0, '2': 5, '3': 7, '4': 9, '5': 13, '6': 15, '7': 17, '8': 19, '9': 21,
	'A': 1, 'B': 0, 'C': 5, 'D': 7, 'E': 9, 'F': 13, 'G': 15, 'H': 17, 'I': 19, 'J': 21,
	'K': 2, 'L': 4, 'M': 18, 'N': 20, 'O': 11, 'P': 3, 'Q': 6, 'R': 8, 'S': 12, 'T': 14,
	'U': 16, 'V': 10, 'W': 22, 'X': 25, 'Y': 24, 'Z': 23,
}

// cinEven is the natural value: 0–9 for the digits, 0–25 for the letters. Even
// positions are the unweighted half of the sum.
func cinEven(c rune) int {
	if c >= '0' && c <= '9' {
		return int(c - '0')
	}
	return int(c - 'A')
}

// cin is Italy's Codice di Controllo Interno: one letter, computed over the
// twenty-two characters that follow it — ABI, CAB and the account number.
//
// Odd positions are weighted through a table, even positions are not, the sum is
// taken mod 26, and the result is a letter. Position is 1-INDEXED, which is the
// convention the published algorithm is stated in and the thing to check first
// if a vector disagrees.
func cin(span string) string {
	sum := 0
	for i, c := range span {
		if i%2 == 0 { // 1-indexed odd
			sum += cinOdd[c]
		} else {
			sum += cinEven(c)
		}
	}
	return string(rune('A' + sum%26))
}

// ribLetter is France's letter-to-digit map, and it is NOT mod-97-10's
// A=10…Z=35.
//
// It is A–I → 1–9, then J–R → 1–9 again, then S–Z → 2–9. The repetition is the
// whole reason this check catches things the international one does not: two
// letters that mod-97-10 tells apart, this map collapses, and two it collapses,
// mod-97-10 tells apart. Neither is a superset of the other.
func ribLetter(c rune) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'I':
		return int(c-'A') + 1
	case c >= 'J' && c <= 'R':
		return int(c-'J') + 1
	case c >= 'S' && c <= 'Z':
		return int(c-'S') + 2
	}
	return 0
}

// ribNumber reads a French BBAN segment as a number under the map above.
//
// The compte is eleven characters, so the largest value is under 10^11 and the
// weighted sum below is under 10^12: an int64 holds it with a factor of a
// million to spare, and no modular reduction is needed on the way.
func ribNumber(s string) int64 {
	var n int64
	for _, c := range s {
		n = n*10 + int64(ribLetter(c))
	}
	return n
}

// cleRIB is France's clé: two digits, 97 minus the weighted sum of the three
// segments before it, mod 97.
//
// The weights (89, 15, 3) are the published ones. A result of 97 is legal and is
// why the range is 1–97 rather than 0–96.
func cleRIB(banque, guichet, compte string) string {
	sum := 89*ribNumber(banque) + 15*ribNumber(guichet) + 3*ribNumber(compte)
	k := 97 - sum%97
	return string(rune('0'+k/10)) + string(rune('0'+k%10))
}
