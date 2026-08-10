package iban

// mod97 is ISO 7064's MOD 97-10 over an IBAN, and it is the check the whole of
// Europe runs.
//
// The address is REARRANGED first — the country code and check digits move to
// the end — and then read as one enormous decimal number, with each letter
// standing for two digits (A=10 … Z=35). A valid address leaves a remainder of
// 1. Nothing here builds that number: it is up to 34 characters and would need
// arbitrary precision, so the remainder is carried along instead, which is the
// same arithmetic done in a machine word.
//
// A LETTER ADVANCES THE ACCUMULATOR BY TWO PLACES, not one. That is the only
// subtle line in the function and the only way to get it silently wrong: a
// letter treated as one digit still produces a remainder, still verifies some
// addresses, and rejects perfectly good ones in every country whose BBAN admits
// letters. Italy's and France's do.
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
//
// It is 98 minus the remainder of the same address with "00" where the check
// digits go, which is the standard construction and is why a valid address
// leaves 1 rather than 0.
//
// ORDERING IS LOAD-BEARING: a country's own check character is part of the BBAN
// and therefore part of what these digits are computed over. Filling the CIN
// after the check digits produces an address that fails mod-97 in Italy every
// time. See New, which is the only caller.
func checkDigits(c Country, bban string) string {
	r := mod97(string(c) + "00" + bban)
	d := 98 - r
	return string(rune('0'+d/10)) + string(rune('0'+d%10))
}
