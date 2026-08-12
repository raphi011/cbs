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

// DirectDebitGroupHeader describes the collection message as a whole.
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
type MandateRelatedInformation struct {
	MndtId    string  `xml:"MndtId"`
	DtOfSgntr ISODate `xml:"DtOfSgntr"`
}

// validate enforces both EPC-mandatory fields.
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
// issued under.
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
// place of birth, or one or more "other" identifiers.
type PersonIdentification struct {
	Othr GenericPersonIdentification `xml:"Othr"`
}

func (p PersonIdentification) validate() error { return p.Othr.validate() }

// CreditorSchemeIdentification is CdtrSchmeId: the Creditor Identifier the
// mandate was issued under — EPC AT-02, mandatory on every SEPA Core
// collection, though PartyIdentification135 leaves the whole element optional
// in the standard.
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
// the two EPC-mandatory elements of PmtTpInf that PaymentTypeInformation itself
// does not require (see that type's doc comment for why): LclInstrm must be
// present and given BY CODE — AT-20 names the scheme CORE, and only the Cd arm
// of the choice can carry a code, so a collection that supplied Prtry instead
// is exactly as non-conformant as one with no LclInstrm at all — and SeqTp must
// be present.
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
