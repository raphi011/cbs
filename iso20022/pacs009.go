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
//
// That is the whole difference from a pacs.008, and it is why this is the
// message that instructs settlement. A pacs.008 moves a customer's money and
// names two customers; a pacs.009 moves a bank's own money and names two banks.
// Here the clearing house sends one to the central bank carrying a closed
// cycle's net positions, and the central bank answers with a pacs.002.
//
// A real ancillary system reaches a settlement agent through an interface of
// its own — TARGET2's ASI, for instance — of which a pacs.009 is one part
// rather than the whole. This repository models the instruction and not the
// interface, because the instruction is the part with a standard shape.
//
// Deliberately omitted, and legal in the standard: SttlmTmIndctn and
// SttlmTmReq (this system settles when told, not within a window),
// InstgAgt/InstdAgt (the header's Fr and To already say who is talking),
// IntrmyAgt1-3 (there is one hop), Purp, RmtInf and UndrlygCstmrCdtTrf (a net
// position has no single underlying payment — that is what netting means).
type Pacs009 struct {
	XMLName  xml.Name                                 `xml:"urn:iso:std:iso:20022:tech:xsd:pacs.009.001.08 Document"`
	FICdtTrf FIToFIFinancialInstitutionCreditTransfer `xml:"FICdtTrf"`
}

func (Pacs009) MessageDefinitionIdentifier() string { return "pacs.009.001.08" }
func (Pacs009) namespace() string                   { return pacs009Namespace }

func (d Pacs009) validate() error { return d.FICdtTrf.validate() }

// FIToFIFinancialInstitutionCreditTransfer is a group header and one or more
// transactions.
//
// The group header type is shared with pacs.008 rather than duplicated: the
// schema's GroupHeader93 here and GroupHeader93 there carry the same elements
// in the same order, and the EPC constrains both the same way. A second
// identical struct would be two places to fix one mistake.
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
//
// The field order is the schema's sequence order and must not be changed.
//
// Dbtr and Cdtr are BranchAndFinancialInstitution and not PartyIdentification.
// That is the type-level statement of what this message is for, and it is why
// the compiler will not let a customer be put in one.
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
	if err := t.IntrBkSttlmAmt.validate(); err != nil {
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
