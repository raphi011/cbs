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
// MANDATORY there — which is why sub-project 7 reopened sub-project 5's
// decision to defer bank-level addressing. A SEPA payment routes agent first
// and account second: the BIC says which bank, the IBAN says which account
// within it.
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
// # This type does not verify the check digit, on purpose
//
// A real IBAN's third and fourth characters are an ISO 7064 mod-97 checksum
// over the rest, and this package does not compute it. That is inherited from
// sub-project 5, which refused mod-97 validation because it would have made the
// seed's readable SE89-AURORA-1001 illegal and replaced it with opaque digits
// in every screenshot, worked example and quiz answer in the repository.
//
// The refusal costs nothing here, which is the part worth knowing. The schema
// constrains an IBAN by PATTERN and not by checksum, so a readable identifier
// still produces a structurally valid document. Validate therefore checks the
// pattern, and its failure is ErrIBANPattern rather than a checksum error, so
// that the distinction survives in the error a caller sees.
//
// # Compact and display forms
//
// An IBAN is canonically stored and transmitted without separators, and
// displayed in groups of four. This repository's stored identifiers use hyphens
// for readability, so Compact is what turns a stored deposit.Identifier value
// into the form that goes on the wire.
//
// Compaction is NOT reversible: SE89AURORA1001 cannot tell you where the
// hyphens were. Code matching a received IBAN against a stored identifier must
// therefore compact BOTH sides and compare, rather than compacting one and
// hoping. That is sub-project 7b's problem, and this comment is where it is
// recorded.
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

// PartyIdentification names a non-financial party: a debtor or a creditor.
//
// The standard allows a postal address and a private or organisation
// identification here. Neither is carried: this system knows a customer's name
// and nothing else about them, and emitting empty optional elements would be
// invalid rather than merely uninformative.
type PartyIdentification struct {
	Nm string `xml:"Nm"`
}

func (p PartyIdentification) validate() error {
	if p.Nm == "" {
		return fmt.Errorf("%w: Nm", ErrMissingElement)
	}
	return nil
}

// GenericAccountIdentification is the non-IBAN arm of an account
// identification: an identifier plus, optionally, the scheme that issued it.
//
// Nothing in this system produces one today — SEPA is IBAN-only. It exists
// because it is the OTHER half of a choice, and a choice with one arm is not a
// choice. It is also where a card PAN would arrive.
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
// The EPC guidelines allow ONE unstructured line of at most 140 characters, so
// this is a string and not a slice. Structured remittance information exists in
// the standard and is not used in SEPA credit transfers.
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
// SHARED with pacs.008, whose SEPA Credit Transfer never carries either, and
// a credit transfer's golden document must keep marshalling exactly as it did
// before these two fields existed. It is pacs003.go's validate(), not this
// type's, that enforces their presence — the same way EPC-versus-ISO
// mandatoriness is decided per message elsewhere in this package.
type PaymentTypeInformation struct {
	SvcLvl    *ServiceLevelChoice    `xml:"SvcLvl,omitempty"`
	LclInstrm *LocalInstrumentChoice `xml:"LclInstrm,omitempty"`
	SeqTp     *SequenceType          `xml:"SeqTp,omitempty"`
}

// validate checks only the structural constraint this type owns: if LclInstrm
// is present, its choice is well-formed. It does NOT require LclInstrm or
// SeqTp to be present — see the type doc comment for why that decision
// belongs to the message, not here.
func (t PaymentTypeInformation) validate() error {
	if t.LclInstrm != nil {
		if err := t.LclInstrm.validate(); err != nil {
			return fmt.Errorf("PmtTpInf/LclInstrm: %w", err)
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

// SettlementInstruction says how interbank settlement happens. For SEPA the
// method is always CLRG — through a clearing system rather than across accounts
// the two agents hold with each other.
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
