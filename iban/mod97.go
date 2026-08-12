package iban

// mod97 is ISO 7064's MOD 97-10 over an IBAN, and it is the check the whole of
// Europe runs.
func mod97(compact string) int {
	r := 0
	rearranged := compact[4:] + compact[:4]
	for _, c := range rearranged {
		switch {
		case c >= '0' && c <= '9':
			r = (r*10 + int(c-'0')) % 97
		case c >= 'A' && c <= 'Z':
			r = (r*100 + int(c-'A') + 10) % 97
		default:
			// Unreachable through this package's own callers, which validate the
			// character classes first. Returning a value that cannot be 1 keeps
			// a future caller that forgets from being told an address verifies.
			return 0
		}
	}
	return r
}

// checkDigits computes the two the address should carry, given a country and a
// BBAN whose own national check character is already in place.
func checkDigits(c Country, bban string) string {
	r := mod97(string(c) + "00" + bban)
	d := 98 - r
	return string(rune('0'+d/10)) + string(rune('0'+d%10))
}
