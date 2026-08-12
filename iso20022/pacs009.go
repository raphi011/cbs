package iso20022

import (
	"encoding/xml"
	"fmt"
)

const pacs009Namespace = "urn:iso:std:iso:20022:tech:xsd:pacs.009.001.08"

func init() {
	registerDocument("pacs.009.001.08", pacs009Namespace, func() Document { return &Pacs009{} })
}

// Pacs009 is FinancialInstitutionCreditTransfer: a transfer where BOTH parties
// are financial institutions.
type Pacs009 struct {
	XMLName  xml.Name                                 `xml:"urn:iso:std:iso:20022:tech:xsd:pacs.009.001.08 Document"`
	FICdtTrf FIToFIFinancialInstitutionCreditTransfer `xml:"FICdtTrf"`
}

func (Pacs009) MessageDefinitionIdentifier() string { return "pacs.009.001.08" }
func (Pacs009) namespace() string                   { return pacs009Namespace }

func (d Pacs009) validate() error { return d.FICdtTrf.validate() }

// FIToFIFinancialInstitutionCreditTransfer is a group header and one or more
// transactions.
type FIToFIFinancialInstitutionCreditTransfer struct {
	GrpHdr      CreditTransferGroupHeader                       `xml:"GrpHdr"`
	CdtTrfTxInf []FinancialInstitutionCreditTransferTransaction `xml:"CdtTrfTxInf"`
}

func (m FIToFIFinancialInstitutionCreditTransfer) validate() error {
	if err := m.GrpHdr.validate(); err != nil {
		return err
	}
	if len(m.CdtTrfTxInf) == 0 {
		return fmt.Errorf("%w: CdtTrfTxInf", ErrMissingElement)
	}
	for i := range m.CdtTrfTxInf {
		if err := m.CdtTrfTxInf[i].validate(); err != nil {
			return fmt.Errorf("CdtTrfTxInf[%d]: %w", i, err)
		}
	}
	return nil
}

// FinancialInstitutionCreditTransferTransaction is one leg of a settlement.
type FinancialInstitutionCreditTransferTransaction struct {
	PmtId          PaymentIdentification         `xml:"PmtId"`
	IntrBkSttlmAmt ActiveCurrencyAndAmount       `xml:"IntrBkSttlmAmt"`
	IntrBkSttlmDt  ISODate                       `xml:"IntrBkSttlmDt"`
	Dbtr           BranchAndFinancialInstitution `xml:"Dbtr"`
	DbtrAcct       *CashAccount                  `xml:"DbtrAcct,omitempty"`
	Cdtr           BranchAndFinancialInstitution `xml:"Cdtr"`
	CdtrAcct       *CashAccount                  `xml:"CdtrAcct,omitempty"`
}

func (t FinancialInstitutionCreditTransferTransaction) validate() error {
	if err := t.PmtId.validate(); err != nil {
		return err
	}
	if err := t.IntrBkSttlmAmt.Validate(); err != nil {
		return fmt.Errorf("IntrBkSttlmAmt: %w", err)
	}
	if err := t.Dbtr.validate(); err != nil {
		return fmt.Errorf("Dbtr: %w", err)
	}
	if t.DbtrAcct != nil {
		if err := t.DbtrAcct.validate(); err != nil {
			return fmt.Errorf("DbtrAcct: %w", err)
		}
	}
	if err := t.Cdtr.validate(); err != nil {
		return fmt.Errorf("Cdtr: %w", err)
	}
	if t.CdtrAcct != nil {
		if err := t.CdtrAcct.validate(); err != nil {
			return fmt.Errorf("CdtrAcct: %w", err)
		}
	}
	return nil
}
