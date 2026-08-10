// The one check on an address that needs no server.
//
// An IBAN's check digits exist to catch a typo OFFLINE — before any request,
// any message, any bank — so running mod-97-10 here is not a duplicate of the
// backend's validation so much as the place that validation is actually FOR. A
// mistyped address is rejected where it was mistyped.
//
// What this deliberately does NOT do is the country structure: length per
// country, Italy's CIN letter, France's clé RIB. That table is the backend's,
// and copying it into the browser would put the rule in two places with no way
// to hold them together. So this is the international check and nothing else,
// and an address that passes here can still be refused on submission — which is
// the honest arrangement, because a check digit never says the address exists
// either.

// compactIban strips the separators a person types and folds case, which is
// what a register does before it compares two addresses.
export function compactIban(value: string): string {
  return value.replace(/[\s-]/g, "").toUpperCase();
}

// A country code, two check digits, then the country's own account structure.
// The upper bound is ISO 13616's: no country's IBAN exceeds 34 characters.
const IBAN_SHAPE = /^[A-Z]{2}[0-9]{2}[A-Z0-9]{1,30}$/;

// checkDigitsPass runs mod-97-10 (ISO 7064) over the compact form: move the
// first four characters to the end, map letters to digits (A=10 … Z=35), and
// take the whole thing modulo 97. A valid IBAN gives 1.
//
// Digit by digit rather than one big number, because the rearranged value runs
// past 30 digits and a JS number stops being exact at 2^53.
export function checkDigitsPass(value: string): boolean {
  const compact = compactIban(value);
  if (!IBAN_SHAPE.test(compact)) return false;

  let remainder = 0;
  for (const ch of compact.slice(4) + compact.slice(0, 4)) {
    const mapped = ch >= "A" ? String(ch.charCodeAt(0) - 55) : ch;
    for (const digit of mapped) {
      remainder = (remainder * 10 + Number(digit)) % 97;
    }
  }
  return remainder === 1;
}

// groupIban is the display form: fours, separated by spaces. What a statement
// prints, and what a person reads back to check they typed it right.
export function groupIban(value: string): string {
  return compactIban(value).replace(/(.{4})/g, "$1 ").trim();
}
