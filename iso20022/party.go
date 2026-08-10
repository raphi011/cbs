package iso20022

import (
	"fmt"
	"regexp"
	"strings"
)

// bicPattern is ISO 9362: four alphabetic characters identifying the
// institution, two alphabetic identifying the country, two alphanumeric
// identifying the location, and an optional three alphanumeric identifying the
// branch. A BIC is therefore 8 or 11 characters and never 9 or 10 — a fact
// worth pinning, because a truncated 11-character code looks plausible.
var bicPattern = regexp.MustCompile(`^[A-Z]{6}[A-Z0-9]{2}([A-Z0-9]{3})?$`)

// BIC is a business identifier code: the address of a financial institution.
//
// In pacs.008 it appears as BICFI inside DbtrAgt and CdtrAgt, and it is
// MANDATORY there. A SEPA payment routes agent first and account second: the BIC
// says which bank, the IBAN says which account within it.
type BIC string

// Validate reports whether the code is structurally a BIC.
//
// It is a structural check only. Whether a well-formed BIC belongs to a bank
// that exists is a directory question, and SWIFT's directory is not something
// this repository models.
func (b BIC) Validate() error {
	if !bicPattern.MatchString(string(b)) {
		return fmt.Errorf("%w: %q", ErrBICFormat, string(b))
	}
	return nil
}

// ibanPattern is the schema's IBAN2007Identifier: two alphabetic characters for
// the country, two digits for the check digits, and up to thirty alphanumeric
// characters for the basic bank account number.
//
// Note what it does NOT constrain: the check digits are two DIGITS, and nothing
// says they are the correct ones. See IBAN.
var ibanPattern = regexp.MustCompile(`^[A-Z]{2}[0-9]{2}[A-Za-z0-9]{1,30}$`)

// ibanSeparators are the characters an IBAN may be displayed with and is never
// stored or transmitted with.
var ibanSeparators = strings.NewReplacer(" ", "", "-", "")

// IBAN is an international bank account number.
//
// # This type checks the pattern and not the check digit, and that is the
// schema's line rather than a shortcut
//
// A real IBAN's third and fourth characters are an ISO 7064 mod-97 checksum over
// the rest, and this package does not compute it. The reason is what this
// package IS: it models the messages, and the schema constrains an IBAN by
// PATTERN — IBAN2007Identifier — so a value satisfying the pattern produces a
// structurally valid document whatever its check digits say. Validate's failure
// is ErrIBANPattern rather than a checksum error, so the distinction survives in
// what a caller sees.
//
// The checksum is real and is checked, one package away: iban.IBAN.Validate
// computes mod-97 and the national check characters besides. This package cannot
// call it — it imports nothing from this repository — and does not need to,
// because every address it ever sees was minted by a register that already did.
//
// # Compact and display forms
//
// An IBAN is canonically stored and transmitted without separators and in upper
// case, and displayed in groups of four. Both this repository's registers and
// its messages carry the canonical form, so Compact is what normalises a value
// somebody wrote out for a human to read.
//
// Compaction is NOT reversible: the compact form cannot tell you where the
// spaces were. Code matching a received IBAN against a stored identifier must
// therefore compact BOTH sides and compare, rather than compacting one and
// hoping. The rule is stated on the side that owns the comparison —
// deposit.Identifier.MatchValue, which delegates it to iban.Compact — and the
// separator set is duplicated here because of the import rule above. A test on
// that side pins the two copies together.
type IBAN string

// Compact returns the IBAN with display separators removed.
//
// It does NOT fold case, and the difference from iban.Compact — which does — is
// deliberate on both sides. The schema's pattern requires an upper-case country
// code, so this package must be able to REFUSE a lower-case one; folding here
// would make Validate accept a document that is not schema-valid. Folding what a
// PERSON typed is a register's job, on the way in, and that is where it happens.
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
//
// It reaches this package's messages by two different routes, and the type is
// the same because the schema type is. In pacs.002 it is the OrgId arm of
// Party38Choice under StsRsnInf/Orgtr, where the EPC guidelines restrict the
// originator of a status to a BIC or a name and nothing else. In the acmt family
// it is an element in its own right — Org/OrgId on the request, OrgId on the
// acknowledgement and the rejection — and it is how the applicant bank is named.
//
// AnyBIC and the generic identifiers are carried; the LEI is not, because
// nothing in this system holds one. Othr is empty on every message but the
// acmt.010, where it is how a national bank code reaches the two institutions
// that route by it — see GenericOrganisationIdentification.
//
// AnyBIC's schema type is AnyBICDec2014Identifier, whose pattern is character
// for character the same as BICFIDec2014Identifier's.
// TestAnyBICAndBICFIShareOneLexicalSpace is what checks that, over every XSD in
// testdata/xsd. It skips on a machine with no schemas and fails there under
// ISO20022_REQUIRE_SCHEMAS.
//
// The BIC type is therefore the right one here and not merely a convenient one.
// What differs is the ROLE: BICFI addresses an
// agent that is party to the payment, AnyBIC identifies any organisation at
// all, which is why the standard keeps two element names for one lexical space.
//
// # validate requires the BIC, and no schema does
//
// AnyBIC is minOccurs="0" wherever OrganisationIdentification29 appears, so this
// is a narrowing and the callers are what justify it. pacs.002's originator must
// be identified, or the status names nobody. The acmt family's applicant must be
// identified by BIC specifically: it is what the settlement agent keys its member
// record by, what the clearing house keys its routing entry by, and what the
// acknowledgement is addressed back on. See AccountOwner.
//
// Othr is NOT required here, and cannot be: the same type is pacs.002's Orgtr,
// which carries a BIC and nothing else. Which message wants a generic identifier
// is the message's own rule, the way LclInstrm's presence is — see
// PaymentTypeInformation. AccountRequestAcknowledgement is the one that has it.
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
//
// The choice is OrganisationIdentificationSchemeName1Choice, whose Cd is an
// ExternalOrganisationIdentification1Code — the list of ways to identify an
// ORGANISATION: a company registration number, a tax reference, a DUNS number. A
// national bank code is not on it. The codes that name those registries — DEBLZ
// for Germany's Bankleitzahl, ITNCC for Italy's national clearing code, SESBA
// for Sweden's — are on ExternalClearingSystemIdentification1Code, which is a
// different list that this element does not reach. Putting one in Cd would be a
// value the schema's own enumeration has never heard of.
//
// So every scheme this system names here is proprietary, and the asymmetry the
// real lists carry survives in the VALUE rather than in the arm: three of the
// four countries name a registry the clearing-system list knows and France names
// one it does not, because French domestic routing is IBAN-only and no equivalent
// entry exists. The arm where a code WOULD be right is ClrSysMmbId on a
// pacs.008's agents, and no message this package builds populates it.
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
//
// The three together are what makes it readable by somebody who was not party to
// the allocation, and that is why all three are required here where the schema
// requires only the first. An Id alone is digits: "99900001" is a Bankleitzahl,
// an ABI and a clearing number depending on a scheme nobody stated, and a bank
// code is unique only within its country. Issr names the institution whose
// register the allocation is in, which is what a reader would have to ask to
// check it.
//
// SchmeNm and Issr are both minOccurs="0", so requiring them is this package's
// narrowing and not the standard's.
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
//
// Both of this package's users of it take a different arm, which is the point
// of the type carrying both. pacs.003's CdtrSchmeId uses PrvtId, because that
// is where the standard puts a Creditor Identifier regardless of whether the
// creditor is itself a company. pacs.002's StsRsnInf/Orgtr uses OrgId, because
// the party issuing a status is a PSP or a clearing house and is identified by
// its BIC. Neither arm is a subset of the other and neither is the "normal"
// one: which arm applies is decided by the ELEMENT, not by the party.
//
// Party38Choice is byte-identical in pacs.002.001.10, pacs.003.001.08 and
// pacs.008.001.08, so widening this type from one arm to two carries none of
// the version-skew risk that SeqTp did — see PaymentTypeInformation.
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
//
// The standard also allows a postal address, a country of residence and contact
// details here. None is carried: this system knows a customer's name and
// nothing else about them, and emitting empty optional elements would be
// invalid rather than merely uninformative.
//
// Nm and Id are BOTH optional at this type's level, and that is deliberate,
// because the element being modelled decides which one applies. A pacs.008 or
// pacs.003 Dbtr/Cdtr must carry Nm — EPC AT-P001 and AT-E001 — and carries no
// Id, because this system holds no organisation or private identifier for a
// customer. A pacs.002 StsRsnInf/Orgtr must carry Id/OrgId/AnyBIC, or Nm if
// (and only if) the clearing house issuing the status has no BIC. Requiring Nm
// here would make the second case unrepresentable; the messages enforce their
// own requirement, the same way PaymentTypeInformation leaves LclInstrm to
// pacs003.go. What this type does enforce is the floor the standard implies
// without stating: a party element that identifies its party in NO way is a
// party nobody can act on.
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
// models pacs.002's Orgtr, which is identified by BIC instead. It lives here,
// rather than being written out twice, because both messages want the identical
// rule; WHICH messages want it is the part that belongs to the messages.
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
//
// It existed for a long time because it is the OTHER half of a choice, and a
// choice with one arm is not a choice — nothing in this system produced one, and
// its doc said so. camt.053 is what changed that: a member bank's reserve
// account at the central bank has no IBAN, because it is not a payment address
// and no customer ever quotes it. It is identified by the servicer's own account
// identifier, which is exactly what this arm is for. It is still where a card
// PAN would arrive.
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
//
// This is an xsd:choice, which encoding/xml cannot express. Both arms are
// pointers and validate enforces exactly-one. It is also the shape sub-project
// 5 cited when it decided a deposit account carries a SET of identifiers rather
// than an iban column: the standard already treats addressing as plural and
// scheme-dependent.
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
//
// The standard permits a type, a currency and a name alongside the identifier.
// The EPC guidelines forbid all three on a SEPA credit transfer, so none is
// carried.
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
//
// The guidelines allow EITHER arm: "Either 'Structured' or 'Unstructured' may
// be present" (SCT Inter-PSP IG idx 2.137, SDD Core IG idx 2.156), and the 2025
// SCT IG adds an Extended Remittance Information option under
// LclInstrm/Cd = "PERI". So "SEPA remittance is one unstructured line" is a
// description of this PACKAGE and not of the scheme.
//
// What is true of the scheme is the narrower part: the unstructured arm is
// limited to one occurrence of Max140Text, which is why Ustrd is a string and
// not a slice. This package models that arm and no other — the structured arm
// is a deliberate omission, recorded with the rest under "Out of scope,
// deliberately" in
// docs/superpowers/specs/2026-07-31-iso20022-messages-design.md — and it does
// not re-check the 140-character bound, for the same reason it checks
// ChrgBr for presence rather than for value: this is a codec, and the schema is
// where a length is enforced. See
// TestRemittanceInformationCarriesTheUnstructuredArmOnly.
type RemittanceInformation struct {
	Ustrd string `xml:"Ustrd,omitempty"`
}

// PaymentIdentification carries the references that let a payment be traced.
//
// EndToEndId is the one that matters and the one the standard makes mandatory:
// it is assigned by the originating customer and travels unchanged to the
// beneficiary, which is what "end to end" means. InstrId and TxId are
// bank-assigned and change hands.
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
//
// Unlike ServiceLevelChoice, BOTH arms are modelled with pointers and an
// exactly-one validate(), the way AccountIdentification4Choice is. SEPA Core
// direct debit always uses Cd=CORE — see LocalInstrumentCore — but a caller
// outside this package's own messages could plausibly need Prtry, and this is
// a genuine xsd:choice in the schema, not a case (like SvcLvl) where the
// second arm is simply never reachable in this system.
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

// PaymentTypeInformation carries the service level, local instrument,
// sequence type and category purpose. Only the first three are used here;
// category purpose is not carried.
//
// LclInstrm and SeqTp are pointers and optional at this type's level even
// though pacs.003 treats both as mandatory (EPC AT-20, AT-21): this struct is
// SHARED with pacs.008, and a credit transfer's golden document must keep
// marshalling exactly as it did before these two fields existed.
//
// The two are NOT symmetric, and must not be read as if they were. LclInstrm
// IS an element of pacs.008's own PaymentTypeInformation28 — EPC's SEPA
// Credit Transfer simply never populates it, which is a rulebook convention
// this package chooses to follow, not a schema constraint. SeqTp has NO
// element in PaymentTypeInformation28 at all: a pacs.008 that set it would
// marshal successfully and be rejected by the real XSD. pacs008.go's own
// validate() therefore refuses a non-nil SeqTp outright — see there — rather
// than merely declining to require it. It is pacs003.go's validate(), not
// this shared type's, that enforces LclInstrm's presence for a collection —
// the same way EPC-versus-ISO mandatoriness is decided per message elsewhere
// in this package.
type PaymentTypeInformation struct {
	SvcLvl    *ServiceLevelChoice    `xml:"SvcLvl,omitempty"`
	LclInstrm *LocalInstrumentChoice `xml:"LclInstrm,omitempty"`
	SeqTp     *SequenceType          `xml:"SeqTp,omitempty"`
}

// validate enforces SvcLvl/Cd — EPC-mandatory for every message this package
// emits, though ISO leaves SvcLvl optional in both PaymentTypeInformation27
// and PaymentTypeInformation28 — and, if LclInstrm is present, that its
// choice is well-formed. It does NOT require LclInstrm to be present: that
// decision differs by message (see the type doc comment) and belongs to the
// message's own validate(). SeqTp is not touched here at all, for the same
// reason and then some — see the type doc comment for why pacs.008 must
// reject it rather than simply not require it.
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
//
// This repository's clearing house is not in the ISO external clearing-system
// list, so the proprietary arm is what it uses.
type ClearingSystemIdentification struct {
	Prtry string `xml:"Prtry,omitempty"`
}

// SettlementInstruction says how interbank settlement happens.
//
// The scheme is less narrow here than one code suggests. The SCT Inter-PSP IG
// allows three methods — "Only 'CLRG', 'INGA' and 'INDA' are allowed" (idx 1.9
// for pacs.008, idx 1.11 for pacs.004) — and the SDD Core IG imposes no SEPA
// restriction at all (idx 1.10 is a plain SettlementMethod2Code). "SEPA means
// CLRG" is this repository's clearing house, not the rule book.
//
// CLRG — settling through a clearing system rather than across accounts the two
// agents hold with each other — is therefore the only member codes.go declares,
// because it is the only one this system produces. validate() checks SttlmMtd
// for PRESENCE and not for value, so a counterparty's INGA or INDA is decoded
// and re-emitted rather than refused. Both halves are pinned by
// TestSettlementInstructionChecksPresenceNotValue, because a comment saying
// "always CLRG" next to code that accepts anything is how this one went wrong.
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
