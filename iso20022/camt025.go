package iso20022

import (
	"encoding/xml"
	"fmt"
)

const camt025Namespace = "urn:iso:std:iso:20022:tech:xsd:camt.025.001.05"

func init() {
	registerDocument("camt.025.001.05", camt025Namespace, func() Document { return &Camt025{} })
}

// Camt025 is Receipt: the institution that was asked to do something saying
// what it did with the request.
type Camt025 struct {
	XMLName xml.Name  `xml:"urn:iso:std:iso:20022:tech:xsd:camt.025.001.05 Document"`
	Rct     ReceiptV5 `xml:"Rct"`
}

func (Camt025) MessageDefinitionIdentifier() string { return "camt.025.001.05" }
func (Camt025) namespace() string                   { return camt025Namespace }

func (d Camt025) validate() error { return d.Rct.validate() }

// ReceiptV5 is the mandatory children of ReceiptV05: a header and one or more
// receipt details.
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

// ReceiptMessageHeader is MessageHeader9: this message's own identifier, when
// it was made, and optionally what kind of request is being answered.
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

// RequestHandling is RequestHandling1: what the servicer did, and optionally
// why.
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
