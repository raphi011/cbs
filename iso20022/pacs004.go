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
//
// This is the R-transaction, and it is a distinct MESSAGE rather than a status
// precisely because settlement was final. A pacs.002 says an instruction was
// refused; a pacs.004 says a completed payment is being reversed by sending an
// equal and opposite one. payment.PostReturnLegTx and payment.SettleReturnTx
// implement the two halves of the operation — the customer leg each bank owns,
// and the reserve reversal between them.
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
//
// Its code type is ReturnReason and not StatusReason, because they are separate
// external code sets in the standard even where their members coincide. One
// type for both would let a rejection reason be used as a return reason with
// nothing to notice.
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
//
// Orgtr is the same element pacs.002's StatusReasonInformation carries, for the
// same reason and with one difference worth noticing. The reason: this message
// is addressed TO the clearing house, so the header says who is passing the
// return along and only Orgtr says who DECIDED to send the money back. The
// difference: pacs.002's EPC rule limits the originator to a PSP or CSM,
// because only a bank rejects an instruction, whereas a return can be
// originated by a non-financial institution too — the EPC wording here is
// "'AnyBIC' for an Agent or 'Name' for a non-financial institution". That is
// why PartyIdentification's two arms are both genuinely reachable rather than
// one being a formality; see that type.
//
// ISO leaves both Orgtr and Rsn minOccurs="0" and leaves this whole element
// optional; EPC makes all three mandatory (AT-R002, AT-R004) and allows exactly
// one occurrence.
//
// The field order is PaymentReturnReason6's sequence: Orgtr, Rsn, AddtlInf.
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
//
// OrgnlIntrBkSttlmAmt and RtrdIntrBkSttlmAmt are separate fields because a
// return may be PARTIAL. This system's returns are always whole — the domain's
// return acts take no amount, and payment.ReturnMessage renders the payment's
// own — so the two are always equal here. The field stays because
// dropping it would make the message unable to express something the standard
// is specifically shaped to express, and a reader comparing this against the
// real schema would find a hole with no explanation.
//
// The two amounts also have DIFFERENT schema types — OrgnlIntrBkSttlmAmt is an
// ActiveOrHistoricCurrencyAndAmount and RtrdIntrBkSttlmAmt an
// ActiveCurrencyAndAmount — and this package maps both to one Go type. That is
// safe rather than sloppy: the two complexTypes' facets are identical (five
// fraction digits, eighteen total digits, non-negative) and differ only in
// which currency code list the Ccy attribute draws from, a list this package
// does not model either way. The distinction the standard is drawing is about
// whether a currency may be one that no longer exists, which is meaningful for
// an amount quoted from a past instruction and meaningless for SEPA's
// EUR-only rulebook.
//
// # OrgnlTxRef was absent, and is reversed here — and only here
//
// PaymentTransaction112 also carries OrgnlTxRef, EPC-mandatory here exactly as
// it is on pacs.002, and this package used not to implement it, on the ruling
// recorded in full on PaymentTransactionStatus: a roughly twenty-field echo of
// the original instruction, mostly optional, duplicating CreditTransferTransaction
// to say nothing new. That ruling still holds on pacs.002 and is NOT reversed
// there — a rejection answers a payment the same message flow already carries
// a record of, so nothing there needs OrgnlTxRef to find its way home.
//
// A pacs.004 answers a payment that has already SETTLED, and sub-project 8 is
// what makes that difference load-bearing: once settlement is split one
// database per institution, the settlement agent that must reverse a return's
// two reserve legs holds no payment row to read DbtrAgt/CdtrAgt from. It has
// only what arrived on the wire. So this package implements the narrow slice
// of OrgnlTxRef those two agents are — OriginalTransactionReference below,
// not the twenty-field structure the standard defines — and leaves the rest
// of the omission exactly as PaymentTransactionStatus describes it.
//
// The field order is a subsequence of PaymentTransaction112's, which is what
// the schema requires: RtrId, OrgnlGrpInf, OrgnlEndToEndId, OrgnlTxId,
// OrgnlIntrBkSttlmAmt, RtrdIntrBkSttlmAmt, ChrgBr, RtrRsnInf, OrgnlTxRef.
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

// OriginalTransactionReference is this package's slice of PaymentTransaction112's
// OrgnlTxRef: the two agents, and nothing else of the twenty-odd fields the
// standard allows there. See the ruling above ReturnTransaction for why only
// these two, and why now.
//
// It stays OPTIONAL where the EPC makes it mandatory — validate below enforces
// only that, when present, it names both agents or neither, because a
// reference naming one side is one a settlement agent cannot act on, and a
// half-filled element is worse than an absent one for looking usable when it
// is not.
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
// original payment back by, both amounts, the EPC-mandatory return reason,
// and — when OrgnlTxRef is present at all — both its agents.
//
// ChrgBr is NOT required, unlike on a pacs.008 or a pacs.003. The EPC
// guidelines leave it 0..1 for a return and restrict it to SLEV when present,
// because the charge bearer here applies to the return leg rather than to the
// original instruction — an asymmetry with the other two messages that is a
// property of the rule book, not an oversight.
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
