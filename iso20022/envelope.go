package iso20022

import (
	"encoding/xml"
	"fmt"
)

// headerNamespace is the XML namespace of the business application header.
const headerNamespace = "urn:iso:std:iso:20022:tech:xsd:head.001.001.02"

// Document is one ISO 20022 message.
type Document interface {
	// MessageDefinitionIdentifier is the value the header's MsgDefIdr must
	// carry for this message, e.g. "pacs.008.001.08".
	MessageDefinitionIdentifier() string

	// namespace is the XML namespace of the message's root element.
	namespace() string

	// validate reports whether the message is missing an element this package
	// treats as mandatory, or breaks an xsd:choice constraint.
	validate() error
}

// documentRegistry maps a message definition identifier to its namespace and a
// constructor.
var documentRegistry = map[string]documentType{}

type documentType struct {
	namespace string
	make      func() Document
}

// registerDocument records a message type under a message definition identifier
// and a namespace.
func registerDocument(msgDefIdr, namespace string, make func() Document) {
	if _, dup := documentRegistry[msgDefIdr]; dup {
		panic("iso20022: duplicate message definition " + msgDefIdr)
	}
	probe := make()
	if got := probe.MessageDefinitionIdentifier(); got != msgDefIdr {
		panic(fmt.Sprintf("iso20022: %T registered under message definition identifier %q but MessageDefinitionIdentifier() returns %q",
			probe, msgDefIdr, got))
	}
	if got := probe.namespace(); got != namespace {
		panic(fmt.Sprintf("iso20022: %T registered under namespace %q but namespace() returns %q",
			probe, namespace, got))
	}
	documentRegistry[msgDefIdr] = documentType{namespace: namespace, make: make}
}

// FinancialInstitution wraps an institution identification for use in the
// header's Fr and To.
type FinancialInstitution struct {
	FinInstnId FinancialInstitutionIdentification `xml:"FinInstnId"`
}

// Party44Choice is the header's sender or receiver. The standard offers a
// choice between an organisation and a financial institution; every party in
// this system is a financial institution, so only that arm is carried.
type Party44Choice struct {
	FIId FinancialInstitution `xml:"FIId"`
}

// NewAgent builds a Party44Choice for a BIC. It is a constructor rather than a
// literal because the nesting — FIId, FinInstnId, BICFI — is four levels deep
// to say one thing.
func NewAgent(b BIC) Party44Choice {
	return Party44Choice{FIId: FinancialInstitution{
		FinInstnId: FinancialInstitutionIdentification{BICFI: b},
	}}
}

// AppHdr is the ISO 20022 business application header, head.001.001.02.
type AppHdr struct {
	XMLName   xml.Name      `xml:"urn:iso:std:iso:20022:tech:xsd:head.001.001.02 AppHdr"`
	Fr        Party44Choice `xml:"Fr"`
	To        Party44Choice `xml:"To"`
	BizMsgIdr string        `xml:"BizMsgIdr"`
	MsgDefIdr string        `xml:"MsgDefIdr"`
	CreDt     ISODateTime   `xml:"CreDt"`
}

// validate checks the five elements head.001.001.02 makes 1..1 and this package
// carries. It runs on both sides of the codec — Marshal will not emit a header
// it fails, and Unmarshal will not return one.
func (h AppHdr) validate() error {
	if err := h.Fr.FIId.FinInstnId.BICFI.Validate(); err != nil {
		return fmt.Errorf("AppHdr/Fr: %w", err)
	}
	if err := h.To.FIId.FinInstnId.BICFI.Validate(); err != nil {
		return fmt.Errorf("AppHdr/To: %w", err)
	}
	if h.BizMsgIdr == "" {
		return fmt.Errorf("%w: AppHdr/BizMsgIdr", ErrMissingElement)
	}
	if h.MsgDefIdr == "" {
		return fmt.Errorf("%w: AppHdr/MsgDefIdr", ErrMissingElement)
	}
	if h.CreDt.IsZero() {
		return fmt.Errorf("%w: AppHdr/CreDt", ErrMissingElement)
	}
	return nil
}

// Envelope is a header and a document travelling together.
type Envelope struct {
	XMLName  xml.Name `xml:"Envelope"`
	AppHdr   AppHdr
	Document Document
}
