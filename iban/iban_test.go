package iban

import (
	"errors"
	"math/big"
	"strings"
	"testing"
)

// published are the standard example addresses for the four countries this
// package issues in, taken from the registry's own documentation and in wide
// circulation as test data.
//
// They are what holds the CIN and the clé RIB honest. Both algorithms are
// lookup tables transcribed from a published description, and a transcription
// error in either produces a package that mints self-consistent addresses
// nobody else agrees with — which every round-trip test in this file would pass.
// These four are the only values here this package did not compute itself.
var published = []struct {
	iban    string
	country Country
	bank    BankCode
}{
	{"DE89370400440532013000", DE, "37040044"},
	{"IT60X0542811101000000123456", IT, "05428"},
	{"FR1420041010050500013M02606", FR, "20041"},
	{"SE4550000000058398257466", SE, "500"},
}

func TestThePublishedAddressesVerify(t *testing.T) {
	for _, p := range published {
		t.Run(p.iban, func(t *testing.T) {
			i, err := Parse(p.iban)
			if err != nil {
				t.Fatalf("Parse(%s) = %v", p.iban, err)
			}
			if i.Country() != p.country {
				t.Errorf("country = %q, want %q", i.Country(), p.country)
			}
			code, err := i.BankCode()
			if err != nil {
				t.Fatalf("BankCode() = %v", err)
			}
			if code != p.bank {
				t.Errorf("bank code = %q, want %q", code, p.bank)
			}
		})
	}
}

// mod97Independently is a second implementation, written the obvious way: build
// the whole number and divide.
//
// It exists because mod97's running accumulator is the kind of arithmetic that
// is wrong in a way tests written against the same idea do not notice — in
// particular the two-place advance for a letter. This one cannot make that
// mistake, because it never advances anything.
func mod97Independently(t *testing.T, compact string) int {
	t.Helper()
	var b strings.Builder
	for _, c := range compact[4:] + compact[:4] {
		switch {
		case c >= '0' && c <= '9':
			b.WriteRune(c)
		case c >= 'A' && c <= 'Z':
			b.WriteString(big.NewInt(int64(c-'A') + 10).String())
		default:
			t.Fatalf("mod97Independently: %q is not an IBAN character", c)
		}
	}
	n, ok := new(big.Int).SetString(b.String(), 10)
	if !ok {
		t.Fatalf("mod97Independently: %q is not a number", b.String())
	}
	return int(new(big.Int).Mod(n, big.NewInt(97)).Int64())
}

func TestTheAccumulatorAgreesWithTheWholeNumber(t *testing.T) {
	for _, p := range published {
		if got, want := mod97(p.iban), mod97Independently(t, p.iban); got != want {
			t.Errorf("mod97(%s) = %d, independently %d", p.iban, got, want)
		}
	}
	for _, c := range Countries() {
		for serial := uint64(1); serial < 40; serial++ {
			i := mint(t, c, serial)
			if got, want := mod97(string(i)), mod97Independently(t, string(i)); got != want {
				t.Errorf("mod97(%s) = %d, independently %d", i, got, want)
			}
		}
	}
}

// bankCodes is one allocated code per country, at that country's width. They
// are fictional; see the design spec on what this system cannot guarantee about
// that.
var bankCodes = map[Country]BankCode{DE: "99900001", IT: "99991", SE: "999", FR: "99991"}

func mint(t *testing.T, c Country, serial uint64) IBAN {
	t.Helper()
	i, err := New(c, bankCodes[c], serial)
	if err != nil {
		t.Fatalf("New(%s, %s, %d) = %v", c, bankCodes[c], serial, err)
	}
	return i
}

func TestEveryMintedAddressVerifies(t *testing.T) {
	for _, c := range Countries() {
		for serial := uint64(1); serial <= 1000; serial++ {
			i := mint(t, c, serial)
			if err := i.Validate(); err != nil {
				t.Fatalf("New(%s, _, %d) = %s, which does not verify: %v", c, serial, i, err)
			}
			code, err := i.BankCode()
			if err != nil {
				t.Fatalf("BankCode() = %v", err)
			}
			if code != bankCodes[c] {
				t.Errorf("%s: bank code reads back as %q, minted from %q", i, code, bankCodes[c])
			}
		}
	}
}

// TestTheWorkedExampleInTheSpec pins the one address the design document writes
// out in full and claims to have verified by hand. If this fails, the document
// is wrong and the document is what the next reader will believe.
func TestTheWorkedExampleInTheSpec(t *testing.T) {
	const want = "DE20999000010000000001"
	got, err := New(DE, "99900001", 1)
	if err != nil {
		t.Fatalf("New = %v", err)
	}
	if string(got) != want {
		t.Errorf("New(DE, 99900001, 1) = %s, spec says %s", got, want)
	}
	if grouped := got.Grouped(); grouped != "DE20 9990 0001 0000 0000 01" {
		t.Errorf("Grouped() = %q", grouped)
	}
}

func TestParseAcceptsWhatAPersonTypes(t *testing.T) {
	const canonical = "DE89370400440532013000"
	for _, typed := range []string{
		"DE89 3704 0044 0532 0130 00",
		"DE89-3704-0044-0532-0130-00",
		"de89370400440532013000",
		"  DE89 370400440532013000",
	} {
		got, err := Parse(typed)
		if err != nil {
			t.Fatalf("Parse(%q) = %v", typed, err)
		}
		if string(got) != canonical {
			t.Errorf("Parse(%q) = %s, want %s", typed, got, canonical)
		}
	}
}

func TestValidateRefusesTheThingsItShould(t *testing.T) {
	for _, tc := range []struct {
		name string
		iban string
		want error
	}{
		{"unknown country", "XX8937040044053201300", ErrUnknownCountry},
		{"too short", "DE8937040044", ErrLength},
		{"too long", "DE893704004405320130000", ErrLength},
		{"letter in a Bankleitzahl", "DE893704004A0532013000", ErrCharacter},
		{"a wrong check digit", "DE88370400440532013000", ErrCheckDigits},
		{"a digit where Italy wants its CIN", "IT6010542811101000000123456", ErrCharacter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := IBAN(tc.iban).Validate(); !errors.Is(err, tc.want) {
				t.Errorf("Validate(%s) = %v, want %v", tc.iban, err, tc.want)
			}
		})
	}
}

func TestNewRefusesABankCodeOfTheWrongWidth(t *testing.T) {
	if _, err := New(DE, "999", 1); !errors.Is(err, ErrBankCodeWidth) {
		t.Errorf("New(DE, 999, 1) = %v, want ErrBankCodeWidth", err)
	}
	if _, err := New(SE, "99900001", 1); !errors.Is(err, ErrBankCodeWidth) {
		t.Errorf("New(SE, 99900001, 1) = %v, want ErrBankCodeWidth", err)
	}
}

// TestASerialIsRefusedRatherThanTruncated is the one that matters for money: a
// truncated serial is a second account at the first one's address.
func TestASerialIsRefusedRatherThanTruncated(t *testing.T) {
	// Germany's account field is ten digits, so this is the first serial with
	// nowhere to go.
	if _, err := New(DE, "99900001", 10_000_000_000); !errors.Is(err, ErrSerialTooLarge) {
		t.Errorf("New = %v, want ErrSerialTooLarge", err)
	}
	if _, err := New(DE, "99900001", 9_999_999_999); err != nil {
		t.Errorf("the last serial that fits was refused: %v", err)
	}
}

// TestEveryCountryNamesADistinctRegister is what makes a bank code readable by
// an institution that was not party to its allocation. The code alone is digits
// — 99991 is an ABI and a code banque — so the register's name travels beside it
// and has to come back to one country.
func TestEveryCountryNamesADistinctRegister(t *testing.T) {
	seen := map[string]Country{}
	for _, c := range Countries() {
		scheme, err := Scheme(c)
		if err != nil {
			t.Fatalf("Scheme(%s) = %v", c, err)
		}
		if scheme == "" {
			t.Fatalf("%s names no register", c)
		}
		if other, dup := seen[scheme]; dup {
			t.Fatalf("%s and %s both allocate under %q", other, c, scheme)
		}
		seen[scheme] = c

		back, err := CountryForScheme(scheme)
		if err != nil {
			t.Fatalf("CountryForScheme(%s) = %v", scheme, err)
		}
		if back != c {
			t.Errorf("CountryForScheme(%s) = %s, want %s", scheme, back, c)
		}
	}
	if _, err := CountryForScheme("GBDSC"); !errors.Is(err, ErrUnknownCountry) {
		t.Errorf("CountryForScheme of a register nobody here uses = %v, want ErrUnknownCountry", err)
	}
}
