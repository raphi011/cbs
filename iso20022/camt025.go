package iso20022

import (
	"encoding/xml"
	"fmt"
)

const camt025Namespace = "urn:iso:std:iso:20022:tech:xsd:camt.025.001.05"

func init() {
	registerDocument("camt.025.001.05", camt025Namespace, func() Document { return &Camt025{} })
}

// Camt025 is Receipt: the institution that was asked to do something saying what
// it did with the request.
//
// It is the answer to the Camt050 a member bank sends its central bank, and it
// closes the lodgement. The central bank has credited the member's reserve
// account, or it has not, and this is how the member is told which.
//
// # Why a receipt and not a pacs.002
//
// A status report is about a payment TRANSACTION, and a lodgement is not one. It
// moves no customer's money, belongs to no scheme and no clearing cycle, and has
// no OrgnlTxId for a pacs.002 to quote. The cash-management family carries its
// own acknowledgement and this is it.
//
// What that costs lands on one element. A pacs.002 rejection carries
// StatusReason, a real external code set this package uses as one. Here the
// outcome is StsCd, a
// Max4AlphaNumericText with no enumeration behind it in the schema, and the
// reason is Desc, free prose. See RequestHandling, which is where that costs
// something.
//
// # What it does NOT carry, and what follows from that
//
// No amount. Receipt3 has no element for one, and that is not an omission this
// package made — the message is an acknowledgement of a REQUEST, identified by
// the request's own message identifier, and the servicer assumes the requester
// still knows what it asked for.
//
// That single absence decides the shape of the whole conversation, so it is
// recorded here rather than only where it bites. A bank cannot post its own leg
// of a lodgement FROM this message, because the message does not say how much;
// so the bank posts before it sends, exactly as a submitting bank does
// (Mesh.Submit), and the alternative — remembering the outstanding request in the
// actor — is the shape csm.held already has and already pays for. See
// payment.LodgeReservesTx.
//
// # What the schema said, checked rather than recalled
//
// ReceiptV05 has MsgHdr (MessageHeader9, mandatory), RctDtls (Receipt3, ONE OR
// MORE) and SplmtryData. Receipt3 sequences OrgnlMsgId, then OrgnlPmtId, then
// ReqHdlg — and of those only OrgnlMsgId is mandatory.
//
// Deliberately omitted, and legal in the standard: SplmtryData, MsgHdr/ReqTp (a
// RequestType4Choice naming an external payment-control or enquiry type — this
// system answers one kind of request and the message definition already says
// which), Receipt3/OrgnlPmtId (a PaymentIdentification6Choice; the request's
// message identifier is what this system correlates on, and a lodgement has no
// payment identifier at all), OriginalMessageAndIssuer1/OrgtrNm, and
// RequestHandling1/Desc on the accepting arm. Each is absent rather than empty.
type Camt025 struct {
	XMLName xml.Name  `xml:"urn:iso:std:iso:20022:tech:xsd:camt.025.001.05 Document"`
	Rct     ReceiptV5 `xml:"Rct"`
}

func (Camt025) MessageDefinitionIdentifier() string { return "camt.025.001.05" }
func (Camt025) namespace() string                   { return camt025Namespace }

func (d Camt025) validate() error { return d.Rct.validate() }

// ReceiptV5 is the mandatory children of ReceiptV05: a header and one or more
// receipt details.
//
// RctDtls is a LIST because the schema makes it one: a servicer may acknowledge
// several requests in one document. This system answers one request per receipt
// — the lodgement it is answering is one movement, and the request that carried
// it was one message — so every receipt it builds has exactly one. validate
// refuses an EMPTY list, which is the schema's rule and not this package's.
type ReceiptV5 struct {
	MsgHdr  ReceiptMessageHeader `xml:"MsgHdr"`
	RctDtls []ReceiptDetails     `xml:"RctDtls"`
}

func (m ReceiptV5) validate() error {
	if err := m.MsgHdr.validate(); err != nil {
		return err
	}
	if len(m.RctDtls) == 0 {
		return fmt.Errorf("%w: RctDtls", ErrMissingElement)
	}
	for i := range m.RctDtls {
		if err := m.RctDtls[i].validate(); err != nil {
			return fmt.Errorf("RctDtls[%d]: %w", i, err)
		}
	}
	return nil
}

// ReceiptMessageHeader is MessageHeader9: this message's own identifier, when it
// was made, and optionally what kind of request is being answered.
//
// It is NOT MessageHeader, which camt.050 uses. MessageHeader1 and
// MessageHeader9 agree on MsgId and CreDtTm and differ in that this one admits a
// third child, so one Go type would be a type that could not grow — the same
// argument LiquidityTransferIdentification makes against sharing
// PaymentIdentification.
//
// CreDtTm is minOccurs="0" here as it is there, and required here for the same
// reason. ReqTp is omitted; see the note on Camt025.
type ReceiptMessageHeader struct {
	MsgId   string      `xml:"MsgId"`
	CreDtTm ISODateTime `xml:"CreDtTm"`
}

func (h ReceiptMessageHeader) validate() error {
	if h.MsgId == "" {
		return fmt.Errorf("%w: MsgHdr/MsgId", ErrMissingElement)
	}
	if h.CreDtTm.IsZero() {
		return fmt.Errorf("%w: MsgHdr/CreDtTm", ErrMissingElement)
	}
	return nil
}

// ReceiptDetails is Receipt3: which request this is about, and how it was
// handled.
//
// The field order is the schema's sequence order and must not be changed.
//
// ReqHdlg is minOccurs="0" in the schema and this package requires at least one.
// That narrowing is this system's, and it is the whole value of the message: a
// receipt naming a request and saying nothing about what became of it tells the
// requester only that the document arrived, which the transport already
// guarantees here. See payment.ReadLodgementReceipt, which refuses one.
type ReceiptDetails struct {
	OrgnlMsgId OriginalMessageAndIssuer `xml:"OrgnlMsgId"`
	ReqHdlg    []RequestHandling        `xml:"ReqHdlg"`
}

func (d ReceiptDetails) validate() error {
	if err := d.OrgnlMsgId.validate(); err != nil {
		return fmt.Errorf("OrgnlMsgId: %w", err)
	}
	if len(d.ReqHdlg) == 0 {
		return fmt.Errorf("%w: ReqHdlg", ErrMissingElement)
	}
	for i := range d.ReqHdlg {
		if err := d.ReqHdlg[i].validate(); err != nil {
			return fmt.Errorf("ReqHdlg[%d]: %w", i, err)
		}
	}
	return nil
}

// OriginalMessageAndIssuer is OriginalMessageAndIssuer1: the request being
// acknowledged, named by its own message identifier.
//
// This is the conversation's only correlator, and it is a back-reference rather
// than an identifier above the pair. A lodgement is one request and one answer,
// so naming the request is enough; a family whose answer could arrive without
// having been asked for would need the other shape.
//
// MsgNmId is the message definition identifier of the request — "camt.050.001.05"
// — and it is optional in the schema. This package carries it, because a receipt
// that says which KIND of request it answers can be dispatched by a reader that
// holds several outstanding; the alternative is a bare identifier whose meaning
// depends on a table only the sender has.
//
// OrgtrNm is omitted: the header's Fr already names the institution that sent
// this, and a name is not how anything here is identified.
type OriginalMessageAndIssuer struct {
	MsgId   string `xml:"MsgId"`
	MsgNmId string `xml:"MsgNmId,omitempty"`
}

func (o OriginalMessageAndIssuer) validate() error {
	if o.MsgId == "" {
		return fmt.Errorf("%w: MsgId", ErrMissingElement)
	}
	return nil
}

// RequestHandling is RequestHandling1: what the servicer did, and optionally why.
//
// # StsCd is a code nothing available here can check
//
// Its type is Max4AlphaNumericText — a pattern, one to four alphanumerics, with
// no xs:enumeration anywhere in camt.025.001.05.xsd. So xmllint accepts any four
// characters, and a wrong code would look exactly like a right one for ever.
// That is the same position BankTransactionCode's Domn arm is in, and this file
// takes the same view of it: the values below are the ones this system issues,
// they are recorded here as such, and a repository that obtains the external
// status code list should revisit them rather than assume they were verified.
//
// What is NOT guessed is the vocabulary. Rather than invent two codes, this
// system reuses the two TransactionStatus values it already sends on a pacs.002
// and already documents — ACSC for done, RJCT for refused. One vocabulary across
// two families is one thing for a reader to learn, and if the codes turn out to
// be wrong for this element they are wrong in a way that is easy to find and
// easy to change in one place.
//
// # Desc carries the reason, as prose
//
// Max140Text, optional, and absent on the accepting arm. A refusal fills it, and
// the loss is that the reason travels as text rather than as a code a
// counterparty can branch on. It is why payment's reasonTable gives the
// lodgement's sentinels the empty code.
//
// Note the width. The refusals that reach it are Go error strings quoting a BIC,
// an asset and two account ids, which can exceed 140 characters between them.
// payment.LodgementReceiptMessage is where that is dealt with rather than
// discovered.
type RequestHandling struct {
	StsCd string `xml:"StsCd"`
	Desc  string `xml:"Desc,omitempty"`
}

func (h RequestHandling) validate() error {
	if h.StsCd == "" {
		return fmt.Errorf("%w: StsCd", ErrMissingElement)
	}
	return nil
}
