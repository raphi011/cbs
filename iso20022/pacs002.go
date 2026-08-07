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
// This is the message that makes clearing ASYNCHRONOUS. A bank sends an
// instruction and does not learn its fate from the sending; it learns it later,
// from a separate document that refers back by identifier. The 202 Accepted, the
// status query and the mesh all follow from this message existing.
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
// Orgtr is WHO decided, and it is separate from the message's own sender for a
// reason that only shows up in a chain: a status report travelling back from
// the creditor's bank through the clearing house is sent BY the clearing house
// and originated by the bank, and a receiver that read the header's Fr as the
// decider would blame the wrong institution for every rejection that passed
// through an intermediary. ISO leaves it minOccurs="0"; the EPC guidelines make
// it mandatory (AT-R002) and restrict it to an AnyBIC identifying the PSP or
// CSM, or — only where that CSM has no BIC at all — a name. This package's
// clearing house has a BIC, so the sample takes the AnyBIC arm; "has no BIC" is
// a fact about the world rather than about the document, so validate() enforces
// the presence of Orgtr and leaves the choice between the two arms to the
// caller.
//
// AddtlInf is free text alongside the code, and it is where this system says
// the thing the code set cannot: which mandate ceiling was exceeded, what the
// available balance actually was. The code is for machines and the text is for
// the person reading the exception queue.
//
// The field order is StatusReasonInformation12's sequence: Orgtr, Rsn,
// AddtlInf.
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
//
// It refers back by the ORIGINAL identifiers, never by a new one: the sender
// has no other way to find the payment this is about. OrgnlEndToEndId is the
// customer's reference and survives the whole journey; OrgnlTxId is the
// bank-assigned one.
//
// # Three identifiers, and which is which
//
// StsId is the odd one out, and the one to keep straight. The two Orgnl* fields
// point BACKWARDS: they are the original payment's references, copied out of a
// message this one is reporting on, and neither belongs to this status report.
// StsId points at ITSELF — it is the reference the party issuing the status
// assigns to THIS status, so that a later message can name the status rather
// than the payment. That is what makes a query answerable: "what came of
// pay-0001" is answered by OrgnlTxId, but "which of the three statuses you sent
// me about pay-0001 are we discussing" is answerable only by StsId. ISO leaves
// it minOccurs="0"; the EPC guidelines make it mandatory (AT-R003), and it is
// first in PaymentTransaction110's sequence, ahead of every Orgnl* element.
//
// # OrgnlTxRef is deliberately absent
//
// PaymentTransaction110 also carries OrgnlTxRef, an EPC-MANDATORY echo of the
// original instruction, and this package does not implement it. That is a
// choice, not an oversight, and it is the one respect in which this message is
// an EPC subset. What it teaches is real — OrgnlTxRef is how a bank identifies
// a rejected payment without having kept any state about the original, because
// the reject carries the payment back with it. But it is a roughly twenty-field
// structure whose own subfields are mostly optional, duplicating most of
// CreditTransferTransaction to say nothing new; in a package where every field
// is present because it means something, a large and mostly-empty echo is a
// poor ratio of insight to noise. The omission recorded honestly is worth more
// than the fields.
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
