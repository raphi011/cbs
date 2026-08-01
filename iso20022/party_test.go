package iso20022

import (
	"encoding/xml"
	"errors"
	"testing"
)

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
		{"seed value with hyphens", "SE89-AURORA-1001", "SE89AURORA1001"},
		{"seed value for Banca Verde", "IT60-VERDE-2002", "IT60VERDE2002"},
		{"displayed in groups of four", "SE89 AURO RA10 01", "SE89AURORA1001"},
		{"already compact", "NO93NORD3001", "NO93NORD3001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.Compact(); got != tt.want {
				t.Fatalf("Compact() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestIBANValidateAcceptsTheSeedsReadableIdentifiers is the claim sub-project 5
// made and this package must not quietly break: the seed's IBANs carry no valid
// mod-97 check digit, and they must still produce schema-valid documents.
func TestIBANValidateAcceptsTheSeedsReadableIdentifiers(t *testing.T) {
	for _, v := range []IBAN{"SE89-AURORA-1001", "IT60-VERDE-2002", "NO93-NORD-3001"} {
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
		{"lowercase country code", "se89AURORA1001"},
		{"no check digits", "SEXXAURORA1001"},
		{"country code only", "SE89"},
		{"too long", "SE89" + "A123456789012345678901234567890X"},
		{"punctuation that is not a separator", "SE89/AURORA/1001"},
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
	iban := IBAN("SE89AURORA1001")
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

func TestPartyIdentificationRequiresAName(t *testing.T) {
	if err := (PartyIdentification{}).validate(); !errors.Is(err, ErrMissingElement) {
		t.Fatalf("validate() = %v, want it to wrap ErrMissingElement", err)
	}
	if err := (PartyIdentification{Nm: "Alice Andersson"}).validate(); err != nil {
		t.Fatalf("validate() = %v, want nil", err)
	}
}

// TestPartyIdentificationAcceptsAnIdentifierInsteadOfAName pins the widening
// that pacs.002's Orgtr required: a party may be identified without being
// named, because the party issuing a status is a PSP known by its BIC. The
// name requirement did not disappear where it applies — it moved to
// validateNamedParty, which the two customer-carrying messages call; see
// TestNamedPartyStillRequiresAName below.
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
