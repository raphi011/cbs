package iso20022

import (
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// bicLexicalSpace pulls the pattern facet out of a named simpleType.
func bicLexicalSpace(schema, typeName string) string {
	re := regexp.MustCompile(`(?s)<xs:simpleType name="` + typeName +
		`">.*?<xs:pattern value="([^"]+)"`)
	m := re.FindStringSubmatch(schema)
	if m == nil {
		return ""
	}
	return m[1]
}

// TestAnyBICAndBICFIShareOneLexicalSpace checks the claim
// OrganisationIdentification's doc makes about the standard: that
// AnyBICDec2014Identifier and BICFIDec2014Identifier constrain their values
// identically, so one Go BIC type may serve both elements.
func TestAnyBICAndBICFIShareOneLexicalSpace(t *testing.T) {
	schemas, err := filepath.Glob(filepath.Join("testdata", "xsd", "*.xsd"))
	if err != nil {
		t.Fatalf("globbing testdata/xsd: %v", err)
	}
	if len(schemas) == 0 {
		skipUnlessRequired(t, "no schemas in testdata/xsd; see testdata/README.md")
	}

	compared := 0
	for _, path := range schemas {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		anyBIC := bicLexicalSpace(string(body), "AnyBICDec2014Identifier")
		bicfi := bicLexicalSpace(string(body), "BICFIDec2014Identifier")
		// Neither, or only one: no evidence either way. See the note above on why
		// only-one is passed over rather than refused.
		if anyBIC == "" || bicfi == "" {
			continue
		}
		if anyBIC != bicfi {
			t.Errorf("%s: AnyBICDec2014Identifier pattern %q differs from BICFIDec2014Identifier's %q; "+
				"one Go BIC type can no longer serve both elements",
				filepath.Base(path), anyBIC, bicfi)
		}
		compared++
	}
	if compared == 0 {
		t.Fatal("no schema declared both BIC types, so this test compared nothing")
	}
}

func TestBICValidate(t *testing.T) {
	tests := []struct {
		name string
		bic  BIC
		ok   bool
	}{
		{"eight character", "AURTSESS", true},
		{"eleven character with branch", "AURTSESSXXX", true},
		{"numeric location code", "NORDNOK1XXX", true},
		{"numeric branch code", "VERDITMM001", true},
		{"seven characters", "AURTSES", false},
		{"nine characters", "AURTSESSX", false},
		{"ten characters", "AURTSESSXX", false},
		{"lowercase", "aurtsess", false},
		{"digit in institution code", "AUR1SESS", false},
		{"digit in country code", "AURTS1SS", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.bic.Validate()
			if tt.ok && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !tt.ok {
				if err == nil {
					t.Fatalf("Validate() = nil, want an error")
				}
				if !errors.Is(err, ErrBICFormat) {
					t.Fatalf("Validate() = %v, want it to wrap ErrBICFormat", err)
				}
			}
		})
	}
}

func TestIBANCompact(t *testing.T) {
	tests := []struct {
		name string
		in   IBAN
		want IBAN
	}{
		{"grouped as a statement prints it", "DE20 9990 0001 0000 0000 01", "DE20999000010000000001"},
		{"hyphenated on a form", "DE20-9990-0001-0000-0000-01", "DE20999000010000000001"},
		{"already compact", "IT50R9999100000000000000001", "IT50R9999100000000000000001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.Compact(); got != tt.want {
				t.Fatalf("Compact() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestIBANValidateAcceptsAValueWhoseCheckDigitsAreWrong is the claim this
// package must not quietly break, and the reason ErrIBANPattern is not a
// checksum error.
func TestIBANValidateAcceptsAValueWhoseCheckDigitsAreWrong(t *testing.T) {
	// Real addresses with their check digits overwritten, which is the shape a
	// mistyped one arrives in.
	for _, v := range []IBAN{"DE00999000010000000001", "IT00R9999100000000000000001"} {
		if err := v.Validate(); err != nil {
			t.Fatalf("Validate(%q) = %v, want nil", v, err)
		}
	}
}

func TestIBANValidateRejects(t *testing.T) {
	tests := []struct {
		name string
		in   IBAN
	}{
		{"lowercase country code", "de20999000010000000001"},
		{"no check digits", "DEXX999000010000000001"},
		{"country code only", "DE20"},
		{"too long", "DE20" + "A123456789012345678901234567890X"},
		{"punctuation that is not a separator", "DE20/9990/0001/0000/0000/01"},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate()
			if err == nil {
				t.Fatalf("Validate(%q) = nil, want an error", tt.in)
			}
			if !errors.Is(err, ErrIBANPattern) {
				t.Fatalf("Validate(%q) = %v, want it to wrap ErrIBANPattern", tt.in, err)
			}
		})
	}
}

func TestAccountIdentificationChoice(t *testing.T) {
	iban := IBAN("SE0888100000000000000001")
	other := GenericAccountIdentification{Id: "internal-1"}

	t.Run("IBAN only is valid", func(t *testing.T) {
		c := AccountIdentification4Choice{IBAN: &iban}
		if err := c.validate(); err != nil {
			t.Fatalf("validate() = %v, want nil", err)
		}
	})
	t.Run("Othr only is valid", func(t *testing.T) {
		c := AccountIdentification4Choice{Othr: &other}
		if err := c.validate(); err != nil {
			t.Fatalf("validate() = %v, want nil", err)
		}
	})
	t.Run("both is invalid", func(t *testing.T) {
		c := AccountIdentification4Choice{IBAN: &iban, Othr: &other}
		if err := c.validate(); !errors.Is(err, ErrInvalidChoice) {
			t.Fatalf("validate() = %v, want it to wrap ErrInvalidChoice", err)
		}
	})
	t.Run("neither is invalid", func(t *testing.T) {
		c := AccountIdentification4Choice{}
		if err := c.validate(); !errors.Is(err, ErrInvalidChoice) {
			t.Fatalf("validate() = %v, want it to wrap ErrInvalidChoice", err)
		}
	})
	t.Run("a malformed IBAN is rejected", func(t *testing.T) {
		bad := IBAN("nope")
		c := AccountIdentification4Choice{IBAN: &bad}
		if err := c.validate(); !errors.Is(err, ErrIBANPattern) {
			t.Fatalf("validate() = %v, want it to wrap ErrIBANPattern", err)
		}
	})
}

func TestBranchAndFinancialInstitutionValidate(t *testing.T) {
	t.Run("a well-formed BIC is valid", func(t *testing.T) {
		a := BranchAndFinancialInstitution{FinInstnId: FinancialInstitutionIdentification{BICFI: "AURTSESSXXX"}}
		if err := a.validate(); err != nil {
			t.Fatalf("validate() = %v, want nil", err)
		}
	})
	t.Run("a missing BIC is a missing element", func(t *testing.T) {
		a := BranchAndFinancialInstitution{}
		if err := a.validate(); !errors.Is(err, ErrMissingElement) {
			t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
		}
	})
	t.Run("a malformed BIC is a format error", func(t *testing.T) {
		a := BranchAndFinancialInstitution{FinInstnId: FinancialInstitutionIdentification{BICFI: "AURTSESSX"}}
		if err := a.validate(); !errors.Is(err, ErrBICFormat) {
			t.Fatalf("validate() = %v, want it to wrap ErrBICFormat", err)
		}
	})
}

// TestPartyIdentificationRequiresSomeIdentification pins the floor: a party
// element that identifies its party in NO way is refused.
func TestPartyIdentificationRequiresSomeIdentification(t *testing.T) {
	if err := (PartyIdentification{}).validate(); !errors.Is(err, ErrMissingElement) {
		t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
	}
	if err := (PartyIdentification{Nm: "Alice Andersson"}).validate(); err != nil {
		t.Fatalf("validate() = %v, want nil", err)
	}
}

// TestPartyIdentificationAcceptsAnIdentifierInsteadOfAName pins the widening
// that pacs.002's Orgtr required: a party may be identified without being
// named, because the party issuing a status is a PSP known by its BIC.
func TestPartyIdentificationAcceptsAnIdentifierInsteadOfAName(t *testing.T) {
	byBIC := PartyIdentification{Id: &PartyChoice{
		OrgId: &OrganisationIdentification{AnyBIC: "CSMBFRPPXXX"},
	}}
	if err := byBIC.validate(); err != nil {
		t.Fatalf("validate() = %v, want nil", err)
	}

	t.Run("both arms of Party38Choice is an invalid choice", func(t *testing.T) {
		p := PartyIdentification{Id: &PartyChoice{
			OrgId:  &OrganisationIdentification{AnyBIC: "CSMBFRPPXXX"},
			PrvtId: &PersonIdentification{},
		}}
		if err := p.validate(); !errors.Is(err, ErrInvalidChoice) {
			t.Fatalf("validate() = %v, want it to wrap ErrInvalidChoice", err)
		}
	})
	t.Run("neither arm of Party38Choice is an invalid choice", func(t *testing.T) {
		p := PartyIdentification{Id: &PartyChoice{}}
		if err := p.validate(); !errors.Is(err, ErrInvalidChoice) {
			t.Fatalf("validate() = %v, want it to wrap ErrInvalidChoice", err)
		}
	})
	t.Run("a malformed AnyBIC is a format error", func(t *testing.T) {
		p := PartyIdentification{Id: &PartyChoice{
			OrgId: &OrganisationIdentification{AnyBIC: "CSMBFRPPX"},
		}}
		if err := p.validate(); !errors.Is(err, ErrBICFormat) {
			t.Fatalf("validate() = %v, want it to wrap ErrBICFormat", err)
		}
	})
}

// TestNamedPartyStillRequiresAName is the other half of the widening: a debtor
// or a creditor identified only by an organisation identifier satisfies
// PartyIdentification and must still be refused, because EPC AT-P001 and
// AT-E001 make the name mandatory for a CUSTOMER party.
func TestNamedPartyStillRequiresAName(t *testing.T) {
	byBIC := PartyIdentification{Id: &PartyChoice{
		OrgId: &OrganisationIdentification{AnyBIC: "AURTSESSXXX"},
	}}
	if err := byBIC.validate(); err != nil {
		t.Fatalf("PartyIdentification.validate() = %v, want nil for this fixture", err)
	}
	if err := validateNamedParty("Dbtr", byBIC); !errors.Is(err, ErrMissingElement) {
		t.Fatalf("validateNamedParty() = %v, want it to wrap ErrMissingElement", err)
	}
	if err := validateNamedParty("Dbtr", PartyIdentification{Nm: "Alice Andersson"}); err != nil {
		t.Fatalf("validateNamedParty() = %v, want nil", err)
	}
}

// TestSettlementInstructionChecksPresenceNotValue pins the sentence
// SettlementInstruction's doc comment now makes about the CODE, as opposed to
// the ones it makes about the guidelines.
func TestSettlementInstructionChecksPresenceNotValue(t *testing.T) {
	if err := (SettlementInstruction{}).validate(); !errors.Is(err, ErrMissingElement) {
		t.Fatalf("validate() with no SttlmMtd = %v, want it to wrap ErrMissingElement", err)
	}
	// CLRG is what this system emits. INGA and INDA are the other two the SCT
	// guidelines allow, so a counterparty may send either; neither is refused.
	for _, mtd := range []SettlementMethod{SettlementMethodClearing, "INGA", "INDA"} {
		if err := (SettlementInstruction{SttlmMtd: mtd}).validate(); err != nil {
			t.Fatalf("validate() with SttlmMtd = %q returned %v, want nil — this codec checks presence, not value",
				mtd, err)
		}
	}
}

// TestRemittanceInformationCarriesTheUnstructuredArmOnly pins the two claims
// RemittanceInformation's doc comment makes about this package: it models the
// unstructured arm and nothing else, and it does not re-check the schema's
// Max140Text bound.
func TestRemittanceInformationCarriesTheUnstructuredArmOnly(t *testing.T) {
	if n := reflect.TypeOf(RemittanceInformation{}).NumField(); n != 1 {
		t.Fatalf("RemittanceInformation has %d fields, want 1; the doc comment says only the unstructured arm is modelled", n)
	}

	// Marshal rather than validate: RemittanceInformation has no validate() of
	// its own, so the only way to observe "the bound is not re-checked" is to
	// put an over-long line through the whole encode path and see it come out.
	env := assertGoldenRoundTrip(t, "pacs008.xml")
	long := strings.Repeat("x", 200)
	env.Document.(*Pacs008).FIToFICstmrCdtTrf.CdtTrfTxInf[0].RmtInf = &RemittanceInformation{Ustrd: long}

	out, err := Marshal(env)
	if err != nil {
		t.Fatalf("Marshal() error = %v; the Max140Text bound is the schema's and is not enforced here", err)
	}
	if !strings.Contains(string(out), long) {
		t.Fatalf("Marshal() dropped or truncated the remittance line:\n%s", out)
	}
}

// TestOptionalCompositesAreOmitted pins the encoding/xml hazard the package doc
// describes: an optional composite element that is not a pointer would emit an
// empty element, which the schema rejects.
func TestOptionalCompositesAreOmitted(t *testing.T) {
	type holder struct {
		XMLName xml.Name               `xml:"Holder"`
		RmtInf  *RemittanceInformation `xml:"RmtInf,omitempty"`
	}
	out, err := xml.Marshal(holder{})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(out) != `<Holder></Holder>` {
		t.Fatalf("Marshal() = %s, want <Holder></Holder>", out)
	}
}
