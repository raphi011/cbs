package iso20022

import (
	"fmt"
	"regexp"
	"strings"
)

// bicPattern is ISO 9362: four alphabetic characters identifying the
// institution, two alphabetic identifying the country, two alphanumeric
// identifying the location, and an optional three alphanumeric identifying the
// branch.
var bicPattern = regexp.MustCompile(`^[A-Z]{6}[A-Z0-9]{2}([A-Z0-9]{3})?$`)

// BIC is a business identifier code: the address of a financial institution.
type BIC string

// Validate reports whether the code is structurally a BIC.
func (b BIC) Validate() error {
	if !bicPattern.MatchString(string(b)) {
		return fmt.Errorf("%w: %q", ErrBICFormat, string(b))
	}
	return nil
}

// ibanPattern is the schema's IBAN2007Identifier: two alphabetic characters for
// the country, two digits for the check digits, and up to thirty alphanumeric
// characters for the basic bank account number.
var ibanPattern = regexp.MustCompile(`^[A-Z]{2}[0-9]{2}[A-Za-z0-9]{1,30}$`)

// ibanSeparators are the characters an IBAN may be displayed with and is never
// stored or transmitted with.
var ibanSeparators = strings.NewReplacer(" ", "", "-", "")

// IBAN is an international bank account number.
type IBAN string

// Compact returns the IBAN with display separators removed.
func (i IBAN) Compact() IBAN { return IBAN(ibanSeparators.Replace(string(i))) }

// Validate reports whether the compact form matches the schema's pattern. It
// does not verify the check digit; see the type documentation.
func (i IBAN) Validate() error {
	if !ibanPattern.MatchString(string(i.Compact())) {
		return fmt.Errorf("%w: %q", ErrIBANPattern, string(i))
	}
	return nil
}

// OrganisationIdentification is OrganisationIdentification29: how a party that
// is an ORGANISATION rather than a person is identified.
type OrganisationIdentification struct {
	AnyBIC BIC                                 `xml:"AnyBIC"`
	Othr   []GenericOrganisationIdentification `xml:"Othr,omitempty"`
}

func (o OrganisationIdentification) validate() error {
	if o.AnyBIC == "" {
		return fmt.Errorf("%w: OrgId/AnyBIC", ErrMissingElement)
	}
	if err := o.AnyBIC.Validate(); err != nil {
		return err
	}
	for i := range o.Othr {
		if err := o.Othr[i].validate(); err != nil {
			return fmt.Errorf("OrgId/Othr[%d]: %w", i, err)
		}
	}
	return nil
}

// OrganisationIdentificationScheme names the scheme a generic organisation
// identifier was issued under. Only the proprietary arm is carried, and the
// reason is a code list rather than a convention.
type OrganisationIdentificationScheme struct {
	Prtry string `xml:"Prtry"`
}

func (s OrganisationIdentificationScheme) validate() error {
	if s.Prtry == "" {
		return fmt.Errorf("%w: SchmeNm/Prtry", ErrMissingElement)
	}
	return nil
}

// GenericOrganisationIdentification is GenericOrganisationIdentification1: an
// identifier issued to an organisation, the scheme it was issued under, and the
// institution that issued it.
type GenericOrganisationIdentification struct {
	Id      string                           `xml:"Id"`
	SchmeNm OrganisationIdentificationScheme `xml:"SchmeNm"`
	Issr    string                           `xml:"Issr"`
}

func (g GenericOrganisationIdentification) validate() error {
	if g.Id == "" {
		return fmt.Errorf("%w: Othr/Id", ErrMissingElement)
	}
	if err := g.SchmeNm.validate(); err != nil {
		return err
	}
	if g.Issr == "" {
		return fmt.Errorf("%w: Othr/Issr", ErrMissingElement)
	}
	return nil
}

// PartyChoice is the standard's Party38Choice: an organisation identification
// or a private one — never both, never neither. Another xsd:choice; see the
// package doc.
type PartyChoice struct {
	OrgId  *OrganisationIdentification `xml:"OrgId,omitempty"`
	PrvtId *PersonIdentification       `xml:"PrvtId,omitempty"`
}

func (p PartyChoice) validate() error {
	switch {
	case p.OrgId != nil && p.PrvtId != nil:
		return fmt.Errorf("%w: Party38Choice has both OrgId and PrvtId", ErrInvalidChoice)
	case p.OrgId != nil:
		return p.OrgId.validate()
	case p.PrvtId != nil:
		return p.PrvtId.validate()
	default:
		return fmt.Errorf("%w: Party38Choice has neither OrgId nor PrvtId", ErrInvalidChoice)
	}
}

// PartyIdentification is the standard's PartyIdentification135: a party named,
// identified, or both.
type PartyIdentification struct {
	Nm string       `xml:"Nm,omitempty"`
	Id *PartyChoice `xml:"Id,omitempty"`
}

func (p PartyIdentification) validate() error {
	if p.Nm == "" && p.Id == nil {
		return fmt.Errorf("%w: Nm or Id", ErrMissingElement)
	}
	if p.Id != nil {
		if err := p.Id.validate(); err != nil {
			return fmt.Errorf("Id: %w", err)
		}
	}
	return nil
}

// validateNamedParty is the rule PartyIdentification itself cannot state: a
// CUSTOMER party — the debtor or the creditor of a pacs.008 or a pacs.003 —
// must carry a name (EPC AT-P001, AT-E001), whereas the same Go type also
// models pacs.002's Orgtr, which is identified by BIC instead.
func validateNamedParty(element string, p PartyIdentification) error {
	if err := p.validate(); err != nil {
		return fmt.Errorf("%s: %w", element, err)
	}
	if p.Nm == "" {
		return fmt.Errorf("%w: %s/Nm", ErrMissingElement, element)
	}
	return nil
}

// GenericAccountIdentification is the non-IBAN arm of an account
// identification: an identifier plus, optionally, the scheme that issued it.
type GenericAccountIdentification struct {
	Id string `xml:"Id"`
}

func (g GenericAccountIdentification) validate() error {
	if g.Id == "" {
		return fmt.Errorf("%w: Othr/Id", ErrMissingElement)
	}
	return nil
}

// AccountIdentification4Choice is how an account is addressed: by IBAN, or by a
// generic identifier — never both, and never neither.
type AccountIdentification4Choice struct {
	IBAN *IBAN                         `xml:"IBAN,omitempty"`
	Othr *GenericAccountIdentification `xml:"Othr,omitempty"`
}

func (c AccountIdentification4Choice) validate() error {
	switch {
	case c.IBAN != nil && c.Othr != nil:
		return fmt.Errorf("%w: AccountIdentification4Choice has both IBAN and Othr", ErrInvalidChoice)
	case c.IBAN != nil:
		return c.IBAN.Validate()
	case c.Othr != nil:
		return c.Othr.validate()
	default:
		return fmt.Errorf("%w: AccountIdentification4Choice has neither IBAN nor Othr", ErrInvalidChoice)
	}
}

// CashAccount is an account referenced by a payment.
type CashAccount struct {
	Id AccountIdentification4Choice `xml:"Id"`
}

func (a CashAccount) validate() error { return a.Id.validate() }

// FinancialInstitutionIdentification identifies a bank. In SEPA that is a BIC
// and nothing else.
type FinancialInstitutionIdentification struct {
	BICFI BIC `xml:"BICFI"`
}

// BranchAndFinancialInstitution is an agent: the debtor's bank or the
// creditor's bank. It is MANDATORY on both sides of a pacs.008, which is why
// this package ships a BIC type at all.
type BranchAndFinancialInstitution struct {
	FinInstnId FinancialInstitutionIdentification `xml:"FinInstnId"`
}

func (a BranchAndFinancialInstitution) validate() error {
	if a.FinInstnId.BICFI == "" {
		return fmt.Errorf("%w: FinInstnId/BICFI", ErrMissingElement)
	}
	return a.FinInstnId.BICFI.Validate()
}

// RemittanceInformation is what the payment is for, as far as the two customers
// are concerned. The banks do not read it.
type RemittanceInformation struct {
	Ustrd string `xml:"Ustrd,omitempty"`
}

// PaymentIdentification carries the references that let a payment be traced.
type PaymentIdentification struct {
	InstrId    string `xml:"InstrId,omitempty"`
	EndToEndId string `xml:"EndToEndId"`
	TxId       string `xml:"TxId,omitempty"`
}

func (p PaymentIdentification) validate() error {
	if p.EndToEndId == "" {
		return fmt.Errorf("%w: PmtId/EndToEndId", ErrMissingElement)
	}
	return nil
}

// ServiceLevelChoice names the rulebook. In SEPA it is always the code SEPA;
// the proprietary arm of the choice is not carried.
type ServiceLevelChoice struct {
	Cd ServiceLevel `xml:"Cd"`
}

// LocalInstrumentChoice names the local instrument a payment or collection
// travels under: by a member of the standard's external code list, or by a
// proprietary identifier — never both, never neither.
type LocalInstrumentChoice struct {
	Cd    *LocalInstrument `xml:"Cd,omitempty"`
	Prtry *string          `xml:"Prtry,omitempty"`
}

func (c LocalInstrumentChoice) validate() error {
	switch {
	case c.Cd != nil && c.Prtry != nil:
		return fmt.Errorf("%w: LclInstrm has both Cd and Prtry", ErrInvalidChoice)
	case c.Cd == nil && c.Prtry == nil:
		return fmt.Errorf("%w: LclInstrm has neither Cd nor Prtry", ErrInvalidChoice)
	default:
		return nil
	}
}

// PaymentTypeInformation carries the service level, local instrument, sequence
// type and category purpose. Only the first three are used here; category
// purpose is not carried.
type PaymentTypeInformation struct {
	SvcLvl    *ServiceLevelChoice    `xml:"SvcLvl,omitempty"`
	LclInstrm *LocalInstrumentChoice `xml:"LclInstrm,omitempty"`
	SeqTp     *SequenceType          `xml:"SeqTp,omitempty"`
}

// validate enforces SvcLvl/Cd — EPC-mandatory for every message this package
// emits, though ISO leaves SvcLvl optional in both PaymentTypeInformation27 and
// PaymentTypeInformation28 — and, if LclInstrm is present, that its choice is
// well-formed.
func (t PaymentTypeInformation) validate() error {
	if t.SvcLvl == nil {
		return fmt.Errorf("%w: SvcLvl", ErrMissingElement)
	}
	if t.SvcLvl.Cd == "" {
		return fmt.Errorf("%w: SvcLvl/Cd", ErrMissingElement)
	}
	if t.LclInstrm != nil {
		if err := t.LclInstrm.validate(); err != nil {
			return fmt.Errorf("LclInstrm: %w", err)
		}
	}
	return nil
}

// ClearingSystemIdentification names the clearing system a payment settles
// through, by code or by proprietary identifier.
type ClearingSystemIdentification struct {
	Prtry string `xml:"Prtry,omitempty"`
}

// SettlementInstruction says how interbank settlement happens.
type SettlementInstruction struct {
	SttlmMtd SettlementMethod              `xml:"SttlmMtd"`
	ClrSys   *ClearingSystemIdentification `xml:"ClrSys,omitempty"`
}

func (s SettlementInstruction) validate() error {
	if s.SttlmMtd == "" {
		return fmt.Errorf("%w: SttlmInf/SttlmMtd", ErrMissingElement)
	}
	return nil
}
