package iban

import "fmt"

// Country is the ISO 3166 code an IBAN opens with.
type Country string

const (
	DE Country = "DE"
	IT Country = "IT"
	SE Country = "SE"
	FR Country = "FR"
)

// BankCode is the national institution identifier carried inside an IBAN —
// Germany's Bankleitzahl, Italy's ABI, Sweden's clearing number, France's code
// banque.
type BankCode string

// Issuer is one allocation from one country's registry: the authority under
// which a bank mints its customers' addresses.
type Issuer struct {
	Country  Country
	BankCode BankCode
}

// Allocated reports whether a registry has answered: both halves present. A
// country alone is an application, and there is nothing to mint under one.
func (i Issuer) Allocated() bool { return i.Country != "" && i.BankCode != "" }

// Validate checks that the country is one this package issues in and that the
// code is the width and character class that country allocates.
func (i Issuer) Validate() error {
	_, err := New(i.Country, i.BankCode, 1)
	return err
}

// class is what characters a segment admits. The names follow ISO 13616's own
// notation, where 8!n is eight digits and 12!c is twelve alphanumerics.
type class uint8

const (
	digits class = iota // n
	upper               // a
	alnum               // c
)

func (c class) admits(r rune) bool {
	switch c {
	case digits:
		return r >= '0' && r <= '9'
	case upper:
		return r >= 'A' && r <= 'Z'
	case alnum:
		return (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z')
	}
	return false
}

// segment is one named run of the BBAN — the part after the country code and
// the check digits.
type segment struct {
	name  string
	width int
	class class
}

// structure is one country's IBAN, and the fields below are the whole of what
// this package knows about a country.
type structure struct {
	segments []segment

	bankCode int
	account  int
	national int

	// compute returns the country's own check characters, given a getter for the
	// other segments by name.
	compute func(get func(string) string) string
}

// structures is the table. Adding a country is an entry here and nothing else.
var structures = map[Country]structure{
	// Bankleitzahl and Kontonummer, and no national check character: Germany
	// retired its own when the IBAN arrived.
	DE: {
		segments: []segment{
			{"blz", 8, digits},
			{"account", 10, digits},
		},
		bankCode: 0, account: 1, national: -1,
	},

	// The CIN comes FIRST and is computed over everything after it, which is
	// why the national segment is index 0 and the getter below names three
	// segments rather than taking a suffix.
	IT: {
		segments: []segment{
			{"cin", 1, upper},
			{"abi", 5, digits},
			{"cab", 5, digits},
			{"account", 12, alnum},
		},
		bankCode: 1, account: 3, national: 0,
		compute: func(get func(string) string) string {
			return cin(get("abi") + get("cab") + get("account"))
		},
	},

	// Sweden's three digits are the leading part of a clearing number, not the
	// whole of one: a four-digit clearing number identifies an office, and the
	// IBAN carries only enough of it to name the institution.
	SE: {
		segments: []segment{
			{"clearing", 3, digits},
			{"account", 17, digits},
		},
		bankCode: 0, account: 1, national: -1,
	},

	// The clé RIB comes LAST and is computed over the three segments before it,
	// with a letter-to-digit map that is not mod-97-10's. See cleRIB.
	FR: {
		segments: []segment{
			{"banque", 5, digits},
			{"guichet", 5, digits},
			{"compte", 11, alnum},
			{"cle", 2, digits},
		},
		bankCode: 0, account: 2, national: 3,
		compute: func(get func(string) string) string {
			return cleRIB(get("banque"), get("guichet"), get("compte"))
		},
	},
}

// length is the whole address: two for the country, two for the check digits,
// and the BBAN.
func (s structure) length() int {
	n := 4
	for _, seg := range s.segments {
		n += seg.width
	}
	return n
}

// offset is where a segment starts in the COMPACT address, counting the country
// code and check digits. Derived rather than stored; see segment.
func (s structure) offset(i int) int {
	n := 4
	for _, seg := range s.segments[:i] {
		n += seg.width
	}
	return n
}

// slice reads one segment out of a compact address. The caller has already
// checked the length.
func (s structure) slice(compact string, i int) string {
	off := s.offset(i)
	return compact[off : off+s.segments[i].width]
}

// getter returns a lookup by segment name over a compact address, for the
// national check algorithms.
func (s structure) getter(compact string) func(string) string {
	return func(name string) string {
		for i, seg := range s.segments {
			if seg.name == name {
				return s.slice(compact, i)
			}
		}
		panic(fmt.Sprintf("iban: no segment %q in this country's structure", name))
	}
}

// structureFor is the one place a country code becomes a structure.
func structureFor(c Country) (structure, error) {
	s, ok := structures[c]
	if !ok {
		return structure{}, fmt.Errorf("%w: %q", ErrUnknownCountry, string(c))
	}
	return s, nil
}

// BankCodeWidth is what the settlement agent allocates in: the width of a
// country's bank code, without an address to read one out of.
func BankCodeWidth(c Country) (int, error) {
	s, err := structureFor(c)
	if err != nil {
		return 0, err
	}
	return s.segments[s.bankCode].width, nil
}

// Countries is the set this package knows, sorted, for a caller enumerating
// what it may issue in.
func Countries() []Country {
	// Written out rather than ranged over the map, because map iteration is
	// random and a caller listing what it supports must not list it differently
	// each time.
	return []Country{DE, FR, IT, SE}
}
