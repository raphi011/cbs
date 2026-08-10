package iban

import "errors"

var (
	// ErrUnknownCountry is a country code this package has no structure for. It
	// is not a claim that the country has no IBAN — most do — only that nothing
	// here can say how long one is or where its bank code sits. See structures.
	ErrUnknownCountry = errors.New("iban: no structure for this country")

	// ErrLength is an address of the wrong length for its country. Separate from
	// ErrCharacter because it is the one malformation a caller can usually see:
	// a truncated paste is short, and a doubled one is long.
	ErrLength = errors.New("iban: wrong length for the country")

	// ErrCharacter is a character outside what its position admits — a letter in
	// a Bankleitzahl, a digit where Italy wants its CIN.
	ErrCharacter = errors.New("iban: character not permitted at this position")

	// ErrCheckDigits is a failed mod-97-10. It is what catches a typo, and it
	// catches every single-character substitution and every transposition of two
	// adjacent characters; see TestWhatModNinetySevenCatches for what it does
	// not.
	ErrCheckDigits = errors.New("iban: check digits do not verify")

	// ErrNationalCheck is a failed CIN or clé RIB: the international digits
	// verified and the country's own did not.
	//
	// A separate sentinel because it means something the other does not — the
	// address is the right shape and passes the check the whole of Europe runs,
	// and fails the one only its own country runs. A caller reporting this to a
	// human should say which country objected.
	ErrNationalCheck = errors.New("iban: the country's own check character does not verify")

	// ErrBankCodeWidth is a bank code that is not the width its country
	// allocates. It exists for the settlement agent, which validates a code it
	// is about to allocate rather than one it read out of an address.
	ErrBankCodeWidth = errors.New("iban: bank code is not the width this country allocates")

	// ErrSerialTooLarge is an account serial that will not fit the country's
	// account field. Germany's is ten digits, so a bank's ten-billionth account
	// has no address; the refusal is here rather than a silent truncation
	// because a truncated serial is a second account at the first one's address.
	ErrSerialTooLarge = errors.New("iban: account serial does not fit the country's account field")
)
