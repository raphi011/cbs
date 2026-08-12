package iban

import (
	"fmt"
	"strings"
)

// separators are the characters an IBAN is DISPLAYED with and is never stored or
// transmitted with. A space is what the standard's own grouping uses; a hyphen
// is what people type anyway.
var separators = strings.NewReplacer(" ", "", "-", "")

// IBAN is a compact, canonical international bank account number.
type IBAN string

// Compact strips the display separators and upper-cases, WITHOUT validating.
func Compact(s string) string {
	return strings.ToUpper(separators.Replace(s))
}

// Parse normalises a caller-supplied address and validates it.
func Parse(s string) (IBAN, error) {
	i := IBAN(Compact(s))
	if err := i.Validate(); err != nil {
		return "", err
	}
	return i, nil
}

// Country is the two-letter code the address opens with, or "" if it is too
// short to have one. It does no validation; Validate is what refuses a country
// this package has no structure for.
func (i IBAN) Country() Country {
	if len(i) < 2 {
		return ""
	}
	return Country(i[:2])
}

// BankCode is the national institution identifier inside the address.
func (i IBAN) BankCode() (BankCode, error) {
	s, err := structureFor(i.Country())
	if err != nil {
		return "", err
	}
	if len(i) != s.length() {
		return "", fmt.Errorf("%w: %s wants %d characters, got %d", ErrLength, i.Country(), s.length(), len(i))
	}
	return BankCode(s.slice(string(i), s.bankCode)), nil
}

// Grouped is the display form: fours, separated by spaces.
func (i IBAN) Grouped() string {
	var b strings.Builder
	for n, c := range i {
		if n > 0 && n%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// Validate runs every check the address supports, cheapest first: the country's
// structure, then the international check digits, then the country's own check
// character where it has one.
func (i IBAN) Validate() error {
	s, err := structureFor(i.Country())
	if err != nil {
		return err
	}
	compact := string(i)
	if len(compact) != s.length() {
		return fmt.Errorf("%w: %s wants %d characters, got %d", ErrLength, i.Country(), s.length(), len(compact))
	}
	for n, c := range compact[2:4] {
		if c < '0' || c > '9' {
			return fmt.Errorf("%w: check digit %d is %q", ErrCharacter, n+1, c)
		}
	}
	for n, seg := range s.segments {
		for _, c := range s.slice(compact, n) {
			if !seg.class.admits(c) {
				return fmt.Errorf("%w: %q in %s", ErrCharacter, c, seg.name)
			}
		}
	}
	if mod97(compact) != 1 {
		return fmt.Errorf("%w: %s", ErrCheckDigits, compact)
	}
	if s.national >= 0 {
		want := s.compute(s.getter(compact))
		if got := s.slice(compact, s.national); got != want {
			return fmt.Errorf("%w: %s wants %q at %s, got %q",
				ErrNationalCheck, i.Country(), want, s.segments[s.national].name, got)
		}
	}
	return nil
}

// New mints an address: a country, a bank code that country's registry
// allocated, and an account serial the issuing bank allocated in its own book.
func New(c Country, bank BankCode, serial uint64) (IBAN, error) {
	s, err := structureFor(c)
	if err != nil {
		return "", err
	}
	bw := s.segments[s.bankCode].width
	if len(bank) != bw {
		return "", fmt.Errorf("%w: %s allocates %d characters, got %q", ErrBankCodeWidth, c, bw, string(bank))
	}
	for _, r := range bank {
		if !s.segments[s.bankCode].class.admits(r) {
			return "", fmt.Errorf("%w: %q in a %s bank code", ErrCharacter, r, c)
		}
	}
	account := fmt.Sprintf("%0*d", s.segments[s.account].width, serial)
	if len(account) > s.segments[s.account].width {
		return "", fmt.Errorf("%w: %s has %d characters for it, serial %d needs %d",
			ErrSerialTooLarge, c, s.segments[s.account].width, serial, len(account))
	}

	// The BBAN is assembled with the national check character left as filler,
	// because computing it needs the segments around it in place. The filler is
	// never seen: it is overwritten below before any check digit is computed.
	parts := make([]string, len(s.segments))
	for n, seg := range s.segments {
		switch n {
		case s.bankCode:
			parts[n] = string(bank)
		case s.account:
			parts[n] = account
		default:
			// A branch segment — Italy's CAB, France's guichet — and it is zeroes
			// because this system has no branches.
			parts[n] = strings.Repeat("0", seg.width)
		}
	}
	if s.national >= 0 {
		provisional := string(c) + "00" + strings.Join(parts, "")
		parts[s.national] = s.compute(s.getter(provisional))
	}
	bban := strings.Join(parts, "")

	i := IBAN(string(c) + checkDigits(c, bban) + bban)
	if err := i.Validate(); err != nil {
		// Unreachable, and it stays because the alternative is minting an address a
		// bank cannot resolve — the failure this repository has already had once,
		// when every seeded account was unreachable from a message it produced
		// itself.
		return "", fmt.Errorf("iban: minted an invalid address %q: %w", string(i), err)
	}
	return i, nil
}
