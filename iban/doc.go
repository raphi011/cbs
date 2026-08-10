// Package iban is ISO 13616: the international bank account number, its check
// digits, and the country structures that say where a bank code sits inside one.
//
// It imports nothing from this repository, and that is load-bearing rather than
// tidy. Two institutions need it and they are on opposite sides of the system's
// hardest boundary: a member bank MINTS addresses for its own customers, and the
// settlement agent ALLOCATES the bank codes those addresses are built from and
// must validate one against the country it is issued in. A central bank
// importing a member bank's deposit register to check a string would be a
// layering inversion this repository has nowhere else.
//
// # What an IBAN is, and what it is not
//
// It is an ADDRESS with a checksum. The checksum — mod-97-10, ISO 7064 — is what
// lets a bank reject a mistyped address offline, before it asks anybody
// anything, and that is the whole of what it buys. It says nothing about whether
// the account exists, nothing about whether the bank exists, and nothing about
// whether either can be reached: an IBAN whose check digits are perfect and
// whose bank code was never issued is well-formed and unpayable.
//
// The BANK CODE inside it is not a BIC and cannot be turned into one by
// arithmetic. It is a national identifier — Germany's Bankleitzahl, Italy's ABI,
// Sweden's clearing number, France's code banque — and mapping it to the BIC a
// message routes on takes a directory somebody publishes. That is why this
// package answers Country and BankCode and stops there.
//
// # Compact and grouped
//
// The canonical form has no separators and is what is stored and transmitted.
// Grouping in fours is a DISPLAY convention and exists only because humans read
// the thing off a statement and type it back in. Parse accepts either; every
// value this package returns is compact. Grouped is the only function that goes
// the other way, and nothing but a user interface should call it.
//
// # Two check digits, and they disagree on what a letter is worth
//
// Three of the four countries here carry the international check digits and
// nothing else. Two carry a second, national one that predates ISO 13616 and was
// never retired: Italy's CIN and France's clé RIB. They are computed
// differently, over different spans, and — the detail worth knowing — France's
// letter-to-digit map is NOT mod-97-10's A=10…Z=35. That is what makes the two
// checks independent rather than one check run twice, and it is why a clé RIB
// catches errors the international digits let through.
//
// # Four countries, not eighty
//
// The full ISO 13616 registry is licensed reference data covering some eighty
// countries. What is here is the four this system issues in, and adding a fifth
// is a table entry. Validating an address in a country nobody banks in is not a
// capability this repository is short of.
package iban
