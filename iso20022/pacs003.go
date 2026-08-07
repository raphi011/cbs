package iso20022

import (
	"encoding/xml"
	"fmt"
)

const pacs003Namespace = "urn:iso:std:iso:20022:tech:xsd:pacs.003.001.08"

func init() {
	registerDocument("pacs.003.001.08", pacs003Namespace, func() Document { return &Pacs003{} })
}

// Pacs003 is FIToFICustomerDirectDebit: a collection sent by the CREDITOR's
// bank, asking the debtor's bank to pay under a mandate the debtor signed.
//
// It is what payment.SDD names, and it is the mirror image of pacs.008 in one
// specific way worth noticing: the sender is the party being paid. A push
// scheme's message travels with the money; a pull scheme's message travels
// against it.
type Pacs003 struct {
	XMLName            xml.Name                  `xml:"urn:iso:std:iso:20022:tech:xsd:pacs.003.001.08 Document"`
	FIToFICstmrDrctDbt FIToFICustomerDirectDebit `xml:"FIToFICstmrDrctDbt"`
}

func (Pacs003) MessageDefinitionIdentifier() string { return "pacs.003.001.08" }
func (Pacs003) namespace() string                   { return pacs003Namespace }

func (d Pacs003) validate() error { return d.FIToFICstmrDrctDbt.validate() }

// FIToFICustomerDirectDebit is a group header and one or more collections.
type FIToFICustomerDirectDebit struct {
	GrpHdr       DirectDebitGroupHeader              `xml:"GrpHdr"`
	DrctDbtTxInf []DirectDebitTransactionInformation `xml:"DrctDbtTxInf"`
}

func (m FIToFICustomerDirectDebit) validate() error {
	if err := m.GrpHdr.validate(); err != nil {
		return err
	}
	if len(m.DrctDbtTxInf) == 0 {
		return fmt.Errorf("%w: DrctDbtTxInf", ErrMissingElement)
	}
	for i := range m.DrctDbtTxInf {
		if err := m.DrctDbtTxInf[i].validate(); err != nil {
			return fmt.Errorf("DrctDbtTxInf[%d]: %w", i, err)
		}
	}
	return nil
}

// DirectDebitGroupHeader describes the collection message as a whole. It has
// the same shape as a credit transfer's group header and is a separate type
// because the two are separate definitions in the standard and will not
// necessarily stay identical across versions.
type DirectDebitGroupHeader struct {
	MsgId             string                   `xml:"MsgId"`
	CreDtTm           ISODateTime              `xml:"CreDtTm"`
	NbOfTxs           string                   `xml:"NbOfTxs"`
	TtlIntrBkSttlmAmt *ActiveCurrencyAndAmount `xml:"TtlIntrBkSttlmAmt,omitempty"`
	IntrBkSttlmDt     ISODate                  `xml:"IntrBkSttlmDt"`
	SttlmInf          SettlementInstruction    `xml:"SttlmInf"`
}

func (h DirectDebitGroupHeader) validate() error {
	if h.MsgId == "" {
		return fmt.Errorf("%w: GrpHdr/MsgId", ErrMissingElement)
	}
	if h.NbOfTxs == "" {
		return fmt.Errorf("%w: GrpHdr/NbOfTxs", ErrMissingElement)
	}
	if h.TtlIntrBkSttlmAmt != nil {
		if err := h.TtlIntrBkSttlmAmt.Validate(); err != nil {
			return fmt.Errorf("TtlIntrBkSttlmAmt: %w", err)
		}
	}
	return h.SttlmInf.validate()
}

// MandateRelatedInformation is the authorisation the collection relies on.
//
// This is what makes a direct debit different from a credit transfer at the
// message level: the mandate travels WITH the collection, rather than being a
// record only the creditor's bank holds. A debtor's bank that has never seen
// the mandate can still check the identifier and the signature date against a
// dispute.
//
// Both fields are minOccurs="0" in MandateRelatedInformation14 — the ISO
// standard requires NEITHER — and both are mandatory in the EPC guidelines. This
// package keeps that distinction sharp on purpose (see CashAccount), and it
// matters here because a debtor's bank cannot check a dispute against a mandate
// identifier or signature date that never arrived. payment.Mandate has CreatedAt
// and no signature date.
type MandateRelatedInformation struct {
	MndtId    string  `xml:"MndtId"`
	DtOfSgntr ISODate `xml:"DtOfSgntr"`
}

// validate enforces both EPC-mandatory fields. DtOfSgntr counts as absent when
// it is the zero ISODate: a zero date marshals as 0001-01-01, which is a date
// no mandate was ever signed on, so treating it as "present but wrong" instead
// of "absent" would be the more misleading choice.
func (m MandateRelatedInformation) validate() error {
	if m.MndtId == "" {
		return fmt.Errorf("%w: MndtRltdInf/MndtId", ErrMissingElement)
	}
	if m.DtOfSgntr.IsZero() {
		return fmt.Errorf("%w: MndtRltdInf/DtOfSgntr", ErrMissingElement)
	}
	return nil
}

// PersonIdentificationScheme names the scheme an "other" person identifier was
// issued under. This package's only use of it is fixed by the EPC guidelines:
// CdtrSchmeId's Othr/Id is always a Creditor Identifier, and SchmeNm always
// says so with the literal proprietary value SEPA — not a member of the ISO
// external code list, which is why only Prtry is carried here and not a full
// Cd/Prtry choice.
type PersonIdentificationScheme struct {
	Prtry string `xml:"Prtry"`
}

func (s PersonIdentificationScheme) validate() error {
	if s.Prtry == "" {
		return fmt.Errorf("%w: SchmeNm/Prtry", ErrMissingElement)
	}
	return nil
}

// GenericPersonIdentification carries one non-BIC, non-account identifier for
// a party. In this package's only use, Id is the Creditor Identifier — EPC
// AT-02 — and SchmeNm says, per the guidelines, that it is one.
type GenericPersonIdentification struct {
	Id      string                     `xml:"Id"`
	SchmeNm PersonIdentificationScheme `xml:"SchmeNm"`
}

func (g GenericPersonIdentification) validate() error {
	if g.Id == "" {
		return fmt.Errorf("%w: Othr/Id", ErrMissingElement)
	}
	return g.SchmeNm.validate()
}

// PersonIdentification is the standard's PersonIdentification13: a date and
// place of birth, or one or more "other" identifiers. This package carries
// exactly one Othr — the Creditor Identifier — and nothing else: SEPA's use of
// this type is not about the creditor being a natural person, only about which
// arm of Party38Choice the standard puts a proprietary-scheme identifier in.
type PersonIdentification struct {
	Othr GenericPersonIdentification `xml:"Othr"`
}

func (p PersonIdentification) validate() error { return p.Othr.validate() }

// CreditorSchemeIdentification is CdtrSchmeId: the Creditor Identifier the
// mandate was issued under — EPC AT-02, mandatory on every SEPA Core
// collection, though PartyIdentification135 leaves the whole element optional
// in the standard.
//
// It is what lets the debtor's bank check a collection against a mandate
// scoped to a specific creditor without ever having seen that creditor's own
// records: the identifier is assigned by a national scheme (a country's
// central bank or an equivalent authority), not chosen by the creditor, so it
// cannot be forged the way a free-text creditor name could be. The standard's
// PartyIdentification135 also allows a name and postal address here; the EPC
// guidelines do not require them and neither is carried.
type CreditorSchemeIdentification struct {
	Id PartyChoice `xml:"Id"`
}

func (c CreditorSchemeIdentification) validate() error { return c.Id.validate() }

// DirectDebitTransaction wraps the mandate information and the creditor
// scheme identification. The standard also allows a pre-notification
// identifier and date here (PreNtfctnId, PreNtfctnDt); neither is used.
type DirectDebitTransaction struct {
	MndtRltdInf MandateRelatedInformation    `xml:"MndtRltdInf"`
	CdtrSchmeId CreditorSchemeIdentification `xml:"CdtrSchmeId"`
}

func (t DirectDebitTransaction) validate() error {
	if err := t.MndtRltdInf.validate(); err != nil {
		return err
	}
	if err := t.CdtrSchmeId.validate(); err != nil {
		return fmt.Errorf("CdtrSchmeId: %w", err)
	}
	return nil
}

// DirectDebitTransactionInformation is one collection.
//
// The field order is the schema's, and it differs from a credit transfer's in
// the way the message's direction implies: the creditor and its agent come
// first, because the creditor's bank is the sender.
type DirectDebitTransactionInformation struct {
	PmtId          PaymentIdentification         `xml:"PmtId"`
	PmtTpInf       *PaymentTypeInformation       `xml:"PmtTpInf,omitempty"`
	IntrBkSttlmAmt ActiveCurrencyAndAmount       `xml:"IntrBkSttlmAmt"`
	ChrgBr         ChargeBearer                  `xml:"ChrgBr"`
	DrctDbtTx      DirectDebitTransaction        `xml:"DrctDbtTx"`
	Cdtr           PartyIdentification           `xml:"Cdtr"`
	CdtrAcct       CashAccount                   `xml:"CdtrAcct"`
	CdtrAgt        BranchAndFinancialInstitution `xml:"CdtrAgt"`
	Dbtr           PartyIdentification           `xml:"Dbtr"`
	DbtrAcct       CashAccount                   `xml:"DbtrAcct"`
	DbtrAgt        BranchAndFinancialInstitution `xml:"DbtrAgt"`
	RmtInf         *RemittanceInformation        `xml:"RmtInf,omitempty"`
}

// validate enforces, alongside the checks pacs.008's equivalent method makes,
// the two EPC-mandatory elements of PmtTpInf that PaymentTypeInformation
// itself does not require (see that type's doc comment for why): LclInstrm
// must be present and given BY CODE — AT-20 names the scheme CORE, and only
// the Cd arm of the choice can carry a code, so a collection that supplied
// Prtry instead is exactly as non-conformant as one with no LclInstrm at all —
// and SeqTp must be present.
func (t DirectDebitTransactionInformation) validate() error {
	if err := t.PmtId.validate(); err != nil {
		return err
	}
	if t.PmtTpInf == nil {
		return fmt.Errorf("%w: PmtTpInf", ErrMissingElement)
	}
	if err := t.PmtTpInf.validate(); err != nil {
		return fmt.Errorf("PmtTpInf: %w", err)
	}
	if t.PmtTpInf.LclInstrm == nil {
		return fmt.Errorf("%w: PmtTpInf/LclInstrm", ErrMissingElement)
	}
	if t.PmtTpInf.LclInstrm.Cd == nil {
		return fmt.Errorf("%w: PmtTpInf/LclInstrm/Cd", ErrMissingElement)
	}
	if t.PmtTpInf.SeqTp == nil {
		return fmt.Errorf("%w: PmtTpInf/SeqTp", ErrMissingElement)
	}
	if err := t.IntrBkSttlmAmt.Validate(); err != nil {
		return fmt.Errorf("IntrBkSttlmAmt: %w", err)
	}
	if t.ChrgBr == "" {
		return fmt.Errorf("%w: ChrgBr", ErrMissingElement)
	}
	if err := t.DrctDbtTx.validate(); err != nil {
		return fmt.Errorf("DrctDbtTx: %w", err)
	}
	if err := validateNamedParty("Cdtr", t.Cdtr); err != nil {
		return err
	}
	if err := t.CdtrAcct.validate(); err != nil {
		return fmt.Errorf("CdtrAcct: %w", err)
	}
	if err := t.CdtrAgt.validate(); err != nil {
		return fmt.Errorf("CdtrAgt: %w", err)
	}
	if err := validateNamedParty("Dbtr", t.Dbtr); err != nil {
		return err
	}
	if err := t.DbtrAcct.validate(); err != nil {
		return fmt.Errorf("DbtrAcct: %w", err)
	}
	if err := t.DbtrAgt.validate(); err != nil {
		return fmt.Errorf("DbtrAgt: %w", err)
	}
	return nil
}
