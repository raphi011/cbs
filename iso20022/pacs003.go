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
		if err := h.TtlIntrBkSttlmAmt.validate(); err != nil {
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
// DtOfSgntr is mandatory in the standard. payment.Mandate has CreatedAt and no
// signature date, so sub-project 7b must either add one or map CreatedAt and
// document the elision.
type MandateRelatedInformation struct {
	MndtId    string  `xml:"MndtId"`
	DtOfSgntr ISODate `xml:"DtOfSgntr"`
}

func (m MandateRelatedInformation) validate() error {
	if m.MndtId == "" {
		return fmt.Errorf("%w: MndtRltdInf/MndtId", ErrMissingElement)
	}
	return nil
}

// DirectDebitTransaction wraps the mandate information. The standard also
// allows the creditor scheme identification and a pre-notification reference
// here; neither is used.
type DirectDebitTransaction struct {
	MndtRltdInf MandateRelatedInformation `xml:"MndtRltdInf"`
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

func (t DirectDebitTransactionInformation) validate() error {
	if err := t.PmtId.validate(); err != nil {
		return err
	}
	if err := t.IntrBkSttlmAmt.validate(); err != nil {
		return fmt.Errorf("IntrBkSttlmAmt: %w", err)
	}
	if t.ChrgBr == "" {
		return fmt.Errorf("%w: ChrgBr", ErrMissingElement)
	}
	if err := t.DrctDbtTx.MndtRltdInf.validate(); err != nil {
		return err
	}
	if err := t.Cdtr.validate(); err != nil {
		return fmt.Errorf("Cdtr: %w", err)
	}
	if err := t.CdtrAcct.validate(); err != nil {
		return fmt.Errorf("CdtrAcct: %w", err)
	}
	if err := t.CdtrAgt.validate(); err != nil {
		return fmt.Errorf("CdtrAgt: %w", err)
	}
	if err := t.Dbtr.validate(); err != nil {
		return fmt.Errorf("Dbtr: %w", err)
	}
	if err := t.DbtrAcct.validate(); err != nil {
		return fmt.Errorf("DbtrAcct: %w", err)
	}
	if err := t.DbtrAgt.validate(); err != nil {
		return fmt.Errorf("DbtrAgt: %w", err)
	}
	return nil
}
