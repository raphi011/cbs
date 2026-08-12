package iso20022

import (
	"encoding/xml"
	"fmt"
)

const pacs002Namespace = "urn:iso:std:iso:20022:tech:xsd:pacs.002.001.10"

func init() {
	registerDocument("pacs.002.001.10", pacs002Namespace, func() Document { return &Pacs002{} })
}

// Pacs002 is FIToFIPaymentStatusReport: what happened to a message that was
// sent earlier.
type Pacs002 struct {
	XMLName         xml.Name                  `xml:"urn:iso:std:iso:20022:tech:xsd:pacs.002.001.10 Document"`
	FIToFIPmtStsRpt FIToFIPaymentStatusReport `xml:"FIToFIPmtStsRpt"`
}

func (Pacs002) MessageDefinitionIdentifier() string { return "pacs.002.001.10" }
func (Pacs002) namespace() string                   { return pacs002Namespace }

func (d Pacs002) validate() error { return d.FIToFIPmtStsRpt.validate() }

// FIToFIPaymentStatusReport is this message's own header, the status of the
// original group, and a status per transaction.
type FIToFIPaymentStatusReport struct {
	GrpHdr            StatusGroupHeader          `xml:"GrpHdr"`
	OrgnlGrpInfAndSts OriginalGroupHeader        `xml:"OrgnlGrpInfAndSts"`
	TxInfAndSts       []PaymentTransactionStatus `xml:"TxInfAndSts,omitempty"`
}

func (m FIToFIPaymentStatusReport) validate() error {
	if m.GrpHdr.MsgId == "" {
		return fmt.Errorf("%w: GrpHdr/MsgId", ErrMissingElement)
	}
	if err := m.OrgnlGrpInfAndSts.validate(); err != nil {
		return err
	}
	for i := range m.TxInfAndSts {
		if err := m.TxInfAndSts[i].validate(); err != nil {
			return fmt.Errorf("TxInfAndSts[%d]: %w", i, err)
		}
	}
	return nil
}

// StatusGroupHeader describes the status report itself. It is deliberately thin
// — a status report moves no money, so it has no settlement date and no total.
type StatusGroupHeader struct {
	MsgId   string      `xml:"MsgId"`
	CreDtTm ISODateTime `xml:"CreDtTm"`
}

// OriginalGroupHeader identifies the message being reported on, and gives its
// status as a whole.
type OriginalGroupHeader struct {
	OrgnlMsgId   string       `xml:"OrgnlMsgId"`
	OrgnlMsgNmId string       `xml:"OrgnlMsgNmId"`
	OrgnlCreDtTm *ISODateTime `xml:"OrgnlCreDtTm,omitempty"`
	GrpSts       GroupStatus  `xml:"GrpSts,omitempty"`
}

func (h OriginalGroupHeader) validate() error {
	if h.OrgnlMsgId == "" {
		return fmt.Errorf("%w: OrgnlGrpInfAndSts/OrgnlMsgId", ErrMissingElement)
	}
	if h.OrgnlMsgNmId == "" {
		return fmt.Errorf("%w: OrgnlGrpInfAndSts/OrgnlMsgNmId", ErrMissingElement)
	}
	return nil
}

// StatusReasonChoice is why, by code or by proprietary text — never both, never
// neither. Another xsd:choice; see the package doc.
type StatusReasonChoice struct {
	Cd    *StatusReason `xml:"Cd,omitempty"`
	Prtry *string       `xml:"Prtry,omitempty"`
}

func (c StatusReasonChoice) validate() error {
	switch {
	case c.Cd != nil && c.Prtry != nil:
		return fmt.Errorf("%w: StatusReasonChoice has both Cd and Prtry", ErrInvalidChoice)
	case c.Cd == nil && c.Prtry == nil:
		return fmt.Errorf("%w: StatusReasonChoice has neither Cd nor Prtry", ErrInvalidChoice)
	default:
		return nil
	}
}

// StatusReasonInformation is the reason a transaction was rejected.
type StatusReasonInformation struct {
	Orgtr    *PartyIdentification `xml:"Orgtr,omitempty"`
	Rsn      StatusReasonChoice   `xml:"Rsn"`
	AddtlInf string               `xml:"AddtlInf,omitempty"`
}

func (i StatusReasonInformation) validate() error {
	if i.Orgtr == nil {
		return fmt.Errorf("%w: StsRsnInf/Orgtr", ErrMissingElement)
	}
	if err := i.Orgtr.validate(); err != nil {
		return fmt.Errorf("StsRsnInf/Orgtr: %w", err)
	}
	return i.Rsn.validate()
}

// PaymentTransactionStatus is the status of one payment from the original
// message.
type PaymentTransactionStatus struct {
	StsId           string                   `xml:"StsId"`
	OrgnlEndToEndId string                   `xml:"OrgnlEndToEndId,omitempty"`
	OrgnlTxId       string                   `xml:"OrgnlTxId,omitempty"`
	TxSts           TransactionStatus        `xml:"TxSts"`
	StsRsnInf       *StatusReasonInformation `xml:"StsRsnInf,omitempty"`
}

func (t PaymentTransactionStatus) validate() error {
	if t.StsId == "" {
		return fmt.Errorf("%w: TxInfAndSts/StsId", ErrMissingElement)
	}
	if t.TxSts == "" {
		return fmt.Errorf("%w: TxInfAndSts/TxSts", ErrMissingElement)
	}
	if t.OrgnlEndToEndId == "" && t.OrgnlTxId == "" {
		return fmt.Errorf("%w: TxInfAndSts needs OrgnlEndToEndId or OrgnlTxId to refer back by",
			ErrMissingElement)
	}
	if t.StsRsnInf != nil {
		return t.StsRsnInf.validate()
	}
	return nil
}
