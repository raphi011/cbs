package iban

// The two national check characters, which exist because national schemes
// predate ISO 13616 and were never retired when it arrived.

// cinOdd is the value of a character in an ODD position (1-indexed) of the span
// the CIN covers. It is a lookup table and not a formula; there is no pattern
// in it to derive.
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
func ribNumber(s string) int64 {
	var n int64
	for _, c := range s {
		n = n*10 + int64(ribLetter(c))
	}
	return n
}

// cleRIB is France's clé: two digits, 97 minus the weighted sum of the three
// segments before it, mod 97.
func cleRIB(banque, guichet, compte string) string {
	sum := 89*ribNumber(banque) + 15*ribNumber(guichet) + 3*ribNumber(compte)
	k := 97 - sum%97
	return string(rune('0'+k/10)) + string(rune('0'+k%10))
}
