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
//
// This is the message that makes clearing ASYNCHRONOUS, and it has no
// counterpart in the payment package's current model. A bank sends an
// instruction and does not learn its fate from the sending; it learns it later,
// from a separate document that refers back by identifier. Everything about
// sub-project 7b — the 202 Accepted, the status query, the mesh — follows from
// this message existing.
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
//
// GrpSts is separate from the per-transaction statuses because a message is a
// BULK: PART means some transactions were accepted and some rejected, and
// without it a partly-rejected file could only be described as one or the
// other. It is optional in the standard — a report may say nothing about the
// group and everything about the transactions — and omitempty suffices to omit
// it, because it is a string type rather than a composite.
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
//
// AddtlInf is free text alongside the code, and it is where this system says
// the thing the code set cannot: which mandate ceiling was exceeded, what the
// available balance actually was. The code is for machines and the text is for
// the person reading the exception queue.
type StatusReasonInformation struct {
	Rsn      StatusReasonChoice `xml:"Rsn"`
	AddtlInf string             `xml:"AddtlInf,omitempty"`
}

// PaymentTransactionStatus is the status of one payment from the original
// message.
//
// It refers back by the ORIGINAL identifiers, never by a new one: the sender
// has no other way to find the payment this is about. OrgnlEndToEndId is the
// customer's reference and survives the whole journey; OrgnlTxId is the
// bank-assigned one.
type PaymentTransactionStatus struct {
	OrgnlEndToEndId string                   `xml:"OrgnlEndToEndId,omitempty"`
	OrgnlTxId       string                   `xml:"OrgnlTxId,omitempty"`
	TxSts           TransactionStatus        `xml:"TxSts"`
	StsRsnInf       *StatusReasonInformation `xml:"StsRsnInf,omitempty"`
}

func (t PaymentTransactionStatus) validate() error {
	if t.TxSts == "" {
		return fmt.Errorf("%w: TxInfAndSts/TxSts", ErrMissingElement)
	}
	if t.OrgnlEndToEndId == "" && t.OrgnlTxId == "" {
		return fmt.Errorf("%w: TxInfAndSts needs OrgnlEndToEndId or OrgnlTxId to refer back by",
			ErrMissingElement)
	}
	if t.StsRsnInf != nil {
		return t.StsRsnInf.Rsn.validate()
	}
	return nil
}
