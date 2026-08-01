package iso20022

import (
	"encoding/xml"
	"fmt"
)

const pacs008Namespace = "urn:iso:std:iso:20022:tech:xsd:pacs.008.001.08"

func init() {
	registerDocument("pacs.008.001.08", pacs008Namespace, func() Document { return &Pacs008{} })
}

// Pacs008 is FIToFICustomerCreditTransfer: the interbank message that moves a
// credit transfer from the debtor's bank to the creditor's.
//
// It is what payment.SCT names. Note what it is NOT: the customer's instruction
// to their own bank is a pain.001, a different message on a different layer,
// which this package does not implement.
type Pacs008 struct {
	XMLName           xml.Name                     `xml:"urn:iso:std:iso:20022:tech:xsd:pacs.008.001.08 Document"`
	FIToFICstmrCdtTrf FIToFICustomerCreditTransfer `xml:"FIToFICstmrCdtTrf"`
}

func (Pacs008) MessageDefinitionIdentifier() string { return "pacs.008.001.08" }
func (Pacs008) namespace() string                   { return pacs008Namespace }

func (d Pacs008) validate() error { return d.FIToFICstmrCdtTrf.validate() }

// FIToFICustomerCreditTransfer is a group header and one or more transactions.
type FIToFICustomerCreditTransfer struct {
	GrpHdr      CreditTransferGroupHeader   `xml:"GrpHdr"`
	CdtTrfTxInf []CreditTransferTransaction `xml:"CdtTrfTxInf"`
}

func (m FIToFICustomerCreditTransfer) validate() error {
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

// CreditTransferGroupHeader describes the message as a whole.
//
// NbOfTxs and TtlIntrBkSttlmAmt are a string and an amount rather than
// derivations of the slice below, because they are what the SENDER asserted.
// A receiver that recomputed them instead of checking them would never notice a
// truncated file.
type CreditTransferGroupHeader struct {
	MsgId             string                   `xml:"MsgId"`
	CreDtTm           ISODateTime              `xml:"CreDtTm"`
	NbOfTxs           string                   `xml:"NbOfTxs"`
	TtlIntrBkSttlmAmt *ActiveCurrencyAndAmount `xml:"TtlIntrBkSttlmAmt,omitempty"`
	IntrBkSttlmDt     ISODate                  `xml:"IntrBkSttlmDt"`
	SttlmInf          SettlementInstruction    `xml:"SttlmInf"`
}

func (h CreditTransferGroupHeader) validate() error {
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

// CreditTransferTransaction is one credit transfer.
//
// The field order is the schema's sequence order and must not be changed: the
// debtor comes before the debtor's agent, and the creditor's agent comes before
// the creditor. That looks inconsistent until you read it as the payment's own
// path — party, its bank, the other bank, the other party.
type CreditTransferTransaction struct {
	PmtId          PaymentIdentification         `xml:"PmtId"`
	PmtTpInf       *PaymentTypeInformation       `xml:"PmtTpInf,omitempty"`
	IntrBkSttlmAmt ActiveCurrencyAndAmount       `xml:"IntrBkSttlmAmt"`
	ChrgBr         ChargeBearer                  `xml:"ChrgBr"`
	Dbtr           PartyIdentification           `xml:"Dbtr"`
	DbtrAcct       CashAccount                   `xml:"DbtrAcct"`
	DbtrAgt        BranchAndFinancialInstitution `xml:"DbtrAgt"`
	CdtrAgt        BranchAndFinancialInstitution `xml:"CdtrAgt"`
	Cdtr           PartyIdentification           `xml:"Cdtr"`
	CdtrAcct       CashAccount                   `xml:"CdtrAcct"`
	RmtInf         *RemittanceInformation        `xml:"RmtInf,omitempty"`
}

func (t CreditTransferTransaction) validate() error {
	if err := t.PmtId.validate(); err != nil {
		return err
	}
	if t.PmtTpInf != nil {
		if err := t.PmtTpInf.validate(); err != nil {
			return fmt.Errorf("PmtTpInf: %w", err)
		}
	}
	if err := t.IntrBkSttlmAmt.validate(); err != nil {
		return fmt.Errorf("IntrBkSttlmAmt: %w", err)
	}
	if t.ChrgBr == "" {
		return fmt.Errorf("%w: ChrgBr", ErrMissingElement)
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
	if err := t.CdtrAgt.validate(); err != nil {
		return fmt.Errorf("CdtrAgt: %w", err)
	}
	if err := t.Cdtr.validate(); err != nil {
		return fmt.Errorf("Cdtr: %w", err)
	}
	if err := t.CdtrAcct.validate(); err != nil {
		return fmt.Errorf("CdtrAcct: %w", err)
	}
	return nil
}
