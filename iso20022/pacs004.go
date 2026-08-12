package iso20022

import (
	"encoding/xml"
	"fmt"
)

const pacs004Namespace = "urn:iso:std:iso:20022:tech:xsd:pacs.004.001.09"

func init() {
	registerDocument("pacs.004.001.09", pacs004Namespace, func() Document { return &Pacs004{} })
}

// Pacs004 is PaymentReturn: money going back after it has already settled.
type Pacs004 struct {
	XMLName xml.Name      `xml:"urn:iso:std:iso:20022:tech:xsd:pacs.004.001.09 Document"`
	PmtRtr  PaymentReturn `xml:"PmtRtr"`
}

func (Pacs004) MessageDefinitionIdentifier() string { return "pacs.004.001.09" }
func (Pacs004) namespace() string                   { return pacs004Namespace }

func (d Pacs004) validate() error { return d.PmtRtr.validate() }

// PaymentReturn is a group header and one or more returns.
type PaymentReturn struct {
	GrpHdr ReturnGroupHeader   `xml:"GrpHdr"`
	TxInf  []ReturnTransaction `xml:"TxInf"`
}

func (m PaymentReturn) validate() error {
	if err := m.GrpHdr.validate(); err != nil {
		return err
	}
	if len(m.TxInf) == 0 {
		return fmt.Errorf("%w: TxInf", ErrMissingElement)
	}
	for i := range m.TxInf {
		if err := m.TxInf[i].validate(); err != nil {
			return fmt.Errorf("TxInf[%d]: %w", i, err)
		}
	}
	return nil
}

// ReturnGroupHeader describes the return message. Its total is a RETURNED
// amount, not a settlement amount, which is the standard being careful about a
// distinction that matters when returns are partial.
type ReturnGroupHeader struct {
	MsgId                 string                   `xml:"MsgId"`
	CreDtTm               ISODateTime              `xml:"CreDtTm"`
	NbOfTxs               string                   `xml:"NbOfTxs"`
	TtlRtrdIntrBkSttlmAmt *ActiveCurrencyAndAmount `xml:"TtlRtrdIntrBkSttlmAmt,omitempty"`
	IntrBkSttlmDt         ISODate                  `xml:"IntrBkSttlmDt"`
	SttlmInf              SettlementInstruction    `xml:"SttlmInf"`
}

func (h ReturnGroupHeader) validate() error {
	if h.MsgId == "" {
		return fmt.Errorf("%w: GrpHdr/MsgId", ErrMissingElement)
	}
	if h.NbOfTxs == "" {
		return fmt.Errorf("%w: GrpHdr/NbOfTxs", ErrMissingElement)
	}
	if h.TtlRtrdIntrBkSttlmAmt != nil {
		if err := h.TtlRtrdIntrBkSttlmAmt.Validate(); err != nil {
			return fmt.Errorf("TtlRtrdIntrBkSttlmAmt: %w", err)
		}
	}
	return h.SttlmInf.validate()
}

// OriginalGroupInformation identifies the message the returned payment came in.
type OriginalGroupInformation struct {
	OrgnlMsgId   string `xml:"OrgnlMsgId"`
	OrgnlMsgNmId string `xml:"OrgnlMsgNmId"`
}

// ReturnReasonChoice is why the payment is coming back, by code or by
// proprietary text. Another xsd:choice.
type ReturnReasonChoice struct {
	Cd    *ReturnReason `xml:"Cd,omitempty"`
	Prtry *string       `xml:"Prtry,omitempty"`
}

func (c ReturnReasonChoice) validate() error {
	switch {
	case c.Cd != nil && c.Prtry != nil:
		return fmt.Errorf("%w: ReturnReasonChoice has both Cd and Prtry", ErrInvalidChoice)
	case c.Cd == nil && c.Prtry == nil:
		return fmt.Errorf("%w: ReturnReasonChoice has neither Cd nor Prtry", ErrInvalidChoice)
	default:
		return nil
	}
}

// ReturnReasonInformation is the reason, plus free text for what the code
// cannot say.
type ReturnReasonInformation struct {
	Orgtr    *PartyIdentification `xml:"Orgtr,omitempty"`
	Rsn      ReturnReasonChoice   `xml:"Rsn"`
	AddtlInf string               `xml:"AddtlInf,omitempty"`
}

func (i ReturnReasonInformation) validate() error {
	if i.Orgtr == nil {
		return fmt.Errorf("%w: RtrRsnInf/Orgtr", ErrMissingElement)
	}
	if err := i.Orgtr.validate(); err != nil {
		return fmt.Errorf("RtrRsnInf/Orgtr: %w", err)
	}
	return i.Rsn.validate()
}

// ReturnTransaction is one payment being returned.
type ReturnTransaction struct {
	RtrId               string                        `xml:"RtrId"`
	OrgnlGrpInf         *OriginalGroupInformation     `xml:"OrgnlGrpInf,omitempty"`
	OrgnlEndToEndId     string                        `xml:"OrgnlEndToEndId,omitempty"`
	OrgnlTxId           string                        `xml:"OrgnlTxId,omitempty"`
	OrgnlIntrBkSttlmAmt ActiveCurrencyAndAmount       `xml:"OrgnlIntrBkSttlmAmt"`
	RtrdIntrBkSttlmAmt  ActiveCurrencyAndAmount       `xml:"RtrdIntrBkSttlmAmt"`
	ChrgBr              ChargeBearer                  `xml:"ChrgBr,omitempty"`
	RtrRsnInf           *ReturnReasonInformation      `xml:"RtrRsnInf,omitempty"`
	OrgnlTxRef          *OriginalTransactionReference `xml:"OrgnlTxRef,omitempty"`
}

// OriginalTransactionReference is this package's slice of
// PaymentTransaction112's OrgnlTxRef: the two agents, and nothing else of the
// twenty-odd fields the standard allows there.
type OriginalTransactionReference struct {
	DbtrAgt *BranchAndFinancialInstitution `xml:"DbtrAgt,omitempty"`
	CdtrAgt *BranchAndFinancialInstitution `xml:"CdtrAgt,omitempty"`
}

func (r OriginalTransactionReference) validate() error {
	switch {
	case r.DbtrAgt == nil && r.CdtrAgt == nil:
		return nil
	case r.DbtrAgt == nil:
		return fmt.Errorf("%w: OrgnlTxRef/DbtrAgt without CdtrAgt", ErrMissingElement)
	case r.CdtrAgt == nil:
		return fmt.Errorf("%w: OrgnlTxRef/CdtrAgt without DbtrAgt", ErrMissingElement)
	}
	if err := r.DbtrAgt.validate(); err != nil {
		return fmt.Errorf("OrgnlTxRef/DbtrAgt: %w", err)
	}
	if err := r.CdtrAgt.validate(); err != nil {
		return fmt.Errorf("OrgnlTxRef/CdtrAgt: %w", err)
	}
	return nil
}

// validate enforces the return's own identifier, something to refer the
// original payment back by, both amounts, the EPC-mandatory return reason, and
// — when OrgnlTxRef is present at all — both its agents.
func (t ReturnTransaction) validate() error {
	if t.RtrId == "" {
		return fmt.Errorf("%w: TxInf/RtrId", ErrMissingElement)
	}
	if t.OrgnlEndToEndId == "" && t.OrgnlTxId == "" {
		return fmt.Errorf("%w: TxInf needs OrgnlEndToEndId or OrgnlTxId to refer back by",
			ErrMissingElement)
	}
	if err := t.OrgnlIntrBkSttlmAmt.Validate(); err != nil {
		return fmt.Errorf("OrgnlIntrBkSttlmAmt: %w", err)
	}
	if err := t.RtrdIntrBkSttlmAmt.Validate(); err != nil {
		return fmt.Errorf("RtrdIntrBkSttlmAmt: %w", err)
	}
	if t.RtrRsnInf == nil {
		return fmt.Errorf("%w: TxInf/RtrRsnInf", ErrMissingElement)
	}
	if err := t.RtrRsnInf.validate(); err != nil {
		return err
	}
	if t.OrgnlTxRef != nil {
		return t.OrgnlTxRef.validate()
	}
	return nil
}
