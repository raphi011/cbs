package iso20022

import (
	"encoding/xml"
	"fmt"
)

const camt050Namespace = "urn:iso:std:iso:20022:tech:xsd:camt.050.001.05"

func init() {
	registerDocument("camt.050.001.05", camt050Namespace, func() Document { return &Camt050{} })
}

// Camt050 is LiquidityCreditTransfer: an account holder telling the institution
// that keeps its account to move money INTO one of its accounts there.
type Camt050 struct {
	XMLName     xml.Name                  `xml:"urn:iso:std:iso:20022:tech:xsd:camt.050.001.05 Document"`
	LqdtyCdtTrf LiquidityCreditTransferV5 `xml:"LqdtyCdtTrf"`
}

func (Camt050) MessageDefinitionIdentifier() string { return "camt.050.001.05" }
func (Camt050) namespace() string                   { return camt050Namespace }

func (d Camt050) validate() error { return d.LqdtyCdtTrf.validate() }

// LiquidityCreditTransferV5 is the mandatory children of
// LiquidityCreditTransferV05: a header and one transfer.
type LiquidityCreditTransferV5 struct {
	MsgHdr      MessageHeader     `xml:"MsgHdr"`
	LqdtyCdtTrf LiquidityTransfer `xml:"LqdtyCdtTrf"`
}

func (m LiquidityCreditTransferV5) validate() error {
	if err := m.MsgHdr.validate(); err != nil {
		return err
	}
	return m.LqdtyCdtTrf.validate()
}

// MessageHeader is MessageHeader1: what identifies this message and when it was
// made.
type MessageHeader struct {
	MsgId   string      `xml:"MsgId"`
	CreDtTm ISODateTime `xml:"CreDtTm"`
}

func (h MessageHeader) validate() error {
	if h.MsgId == "" {
		return fmt.Errorf("%w: MsgHdr/MsgId", ErrMissingElement)
	}
	if h.CreDtTm.IsZero() {
		return fmt.Errorf("%w: MsgHdr/CreDtTm", ErrMissingElement)
	}
	return nil
}

// LiquidityTransfer is LiquidityCreditTransfer2: which accounts, whose, and how
// much.
type LiquidityTransfer struct {
	LqdtyTrfId LiquidityTransferIdentification `xml:"LqdtyTrfId"`
	Cdtr       BranchAndFinancialInstitution   `xml:"Cdtr"`
	CdtrAcct   CashAccount                     `xml:"CdtrAcct"`
	TrfdAmt    TransferredAmount               `xml:"TrfdAmt"`
	Dbtr       BranchAndFinancialInstitution   `xml:"Dbtr"`
}

func (t LiquidityTransfer) validate() error {
	if err := t.LqdtyTrfId.validate(); err != nil {
		return fmt.Errorf("LqdtyTrfId: %w", err)
	}
	if err := t.Cdtr.validate(); err != nil {
		return fmt.Errorf("Cdtr: %w", err)
	}
	if err := t.CdtrAcct.validate(); err != nil {
		return fmt.Errorf("CdtrAcct: %w", err)
	}
	if err := t.TrfdAmt.validate(); err != nil {
		return fmt.Errorf("TrfdAmt: %w", err)
	}
	if err := t.Dbtr.validate(); err != nil {
		return fmt.Errorf("Dbtr: %w", err)
	}
	return nil
}

// LiquidityTransferIdentification is PaymentIdentification8, of which one child
// is mandatory and carried: the sender's end-to-end reference.
type LiquidityTransferIdentification struct {
	EndToEndId string `xml:"EndToEndId"`
}

func (i LiquidityTransferIdentification) validate() error {
	if i.EndToEndId == "" {
		return fmt.Errorf("%w: EndToEndId", ErrMissingElement)
	}
	return nil
}

// TransferredAmount is Amount2Choice, and only the currency-bearing arm is
// carried.
type TransferredAmount struct {
	AmtWthCcy ActiveCurrencyAndAmount `xml:"AmtWthCcy"`
}

func (a TransferredAmount) validate() error { return a.AmtWthCcy.Validate() }
