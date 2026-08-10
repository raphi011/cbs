package iban

import "fmt"

// Country is the ISO 3166 code an IBAN opens with. It is the ACCOUNT's country,
// which is not necessarily the account-holding institution's: a bank passported
// into another market issues addresses in the country the account lives in, and
// its BIC still says where the bank is. Nothing here should be read as saying
// the two agree.
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
//
// It is NOT a BIC and no arithmetic turns it into one. It is also only unique
// within its country, which is why every table keyed by one is keyed by
// (country, code) and never by the code alone.
type BankCode string

// Issuer is one allocation from one country's registry: the authority under
// which a bank mints its customers' addresses.
//
// The two halves are inseparable and that is why they are a type. A bank code is
// only unique within its country, so a code without one names nothing, and every
// table keyed by an allocation is keyed by the pair.
//
// The halves nevertheless arrive at different moments, and that is the state
// worth knowing about. A bank chooses the market it will operate in when it is
// licensed; the code is a registry's to give and arrives when the registry gives
// it. So a country with no code is a bank that has applied and not been
// answered — a real state, and one in which it can hold a book and address no
// account at all — and the layers that mint refuse it rather than defaulting,
// because every default would be some other bank's code.
type Issuer struct {
	Country  Country
	BankCode BankCode
}

// Allocated reports whether a registry has answered: both halves present. A
// country alone is an application, and there is nothing to mint under one.
func (i Issuer) Allocated() bool { return i.Country != "" && i.BankCode != "" }

// Validate checks that the country is one this package issues in and that the
// code is the width and character class that country allocates.
//
// It does NOT check that the code was ever really allocated. Nothing in this
// package could: an allocation is a row in a registry somebody else keeps, and
// the whole reason a routing directory has to be published is that a well-formed
// code is not a issued one.
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
//
// The BBAN is modelled as a list rather than as offsets because the offsets are
// then DERIVED, and a country whose bank code moves cannot be half-updated. It
// also keeps the table readable against the ISO notation it is transcribed from.
type segment struct {
	name  string
	width int
	class class
}

// structure is one country's IBAN, and the fields below are the whole of what
// this package knows about a country.
//
// bankCode, account and national are indices into segments. national is -1 for
// a country that carries no check character of its own, and compute is nil in
// exactly the same cases.
type structure struct {
	segments []segment

	// scheme names the register a bank code in this country comes out of. It
	// travels beside the code wherever one leaves an institution, because a code
	// on its own is digits: 99991 is an ABI and a code banque and there is no
	// third thing that says which.
	//
	// Three of the four are the code the ISO external clearing-system list uses
	// for that register, and the fourth is not, because France has no entry on
	// that list — French domestic routing is IBAN-only, which is the same fact
	// this whole directory exists because of, seen from the standard's side. That
	// asymmetry is why the values are declared here rather than being assumed
	// derivable, and why every one of them travels as a PROPRIETARY scheme name;
	// see iso20022.OrganisationIdentificationScheme for the element that carries
	// them and the code list it can and cannot reach.
	scheme string

	bankCode int
	account  int
	national int

	// compute returns the country's own check characters, given a getter for
	// the other segments by name. It is handed a getter rather than the raw
	// BBAN so that a national algorithm names what it reads — cin over abi, cab
	// and account — instead of slicing at offsets stated twice.
	compute func(get func(string) string) string
}

// structures is the table. Adding a country is an entry here and nothing else.
//
// Two things are deliberately absent. There is no CHECK on which countries a
// bank may be admitted in — that is the settlement agent's question, not this
// package's — and there is no branch-level routing: Italy's CAB and France's
// guichet are carried faithfully and are not part of any key, because a BIC
// identifies an institution and the directory answers at that granularity.
var structures = map[Country]structure{
	// Bankleitzahl and Kontonummer, and no national check character: Germany
	// retired its own when the IBAN arrived.
	DE: {
		segments: []segment{
			{"blz", 8, digits},
			{"account", 10, digits},
		},
		scheme:   "DEBLZ",
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
		scheme:   "ITNCC",
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
		scheme:   "SESBA",
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
		scheme:   "FRCIB",
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

// Scheme is the name of the register a country's bank codes come out of, for a
// caller putting one on a message. See structure.scheme.
func Scheme(c Country) (string, error) {
	s, err := structureFor(c)
	if err != nil {
		return "", err
	}
	return s.scheme, nil
}

// CountryForScheme is Scheme backwards: which country's register a scheme name
// belongs to.
//
// It is the reader's half, and it is what makes a bank code arriving on a
// message keyable. A code is unique only within a country, so a reader given
// digits and a scheme has an allocation and a reader given digits alone has
// nothing — and the scheme is what the message carries, because the country is
// not on it: an acmt.010 identifies its owner by BIC and no more, and a BIC's
// country is where the INSTITUTION is rather than where it issues.
//
// The search is linear over four entries and the map is not inverted, because an
// inverted map is a second table to keep true.
func CountryForScheme(scheme string) (Country, error) {
	for _, c := range Countries() {
		if structures[c].scheme == scheme {
			return c, nil
		}
	}
	return "", fmt.Errorf("%w: no country here allocates under %q", ErrUnknownCountry, scheme)
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
