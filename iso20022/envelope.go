package iso20022

import (
	"encoding/xml"
	"fmt"
)

// headerNamespace is the XML namespace of the business application header.
const headerNamespace = "urn:iso:std:iso:20022:tech:xsd:head.001.001.02"

// Document is one ISO 20022 message.
//
// Its methods are unexported by design, so that only this package can implement
// it. A message defined elsewhere would not have a namespace registered, and a
// Document the codec cannot dispatch to is not a Document.
type Document interface {
	// MessageDefinitionIdentifier is the value the header's MsgDefIdr must
	// carry for this message, e.g. "pacs.008.001.08".
	MessageDefinitionIdentifier() string

	// namespace is the XML namespace of the message's root element.
	namespace() string

	// validate reports whether the message satisfies the EPC guidelines'
	// mandatory elements and its xsd:choice constraints.
	validate() error
}

// documentRegistry maps a message definition identifier to its namespace and a
// constructor. Each message file registers itself in an init function, which is
// what lets Unmarshal turn a MsgDefIdr into a concrete type without a switch
// that must be kept in step with the file list.
var documentRegistry = map[string]documentType{}

type documentType struct {
	namespace string
	make      func() Document
}

func registerDocument(msgDefIdr, namespace string, make func() Document) {
	if _, dup := documentRegistry[msgDefIdr]; dup {
		panic("iso20022: duplicate message definition " + msgDefIdr)
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

// agent builds a Party44Choice for a BIC. It is a constructor rather than a
// literal because the nesting — FIId, FinInstnId, BICFI — is four levels deep
// to say one thing.
func agent(b BIC) Party44Choice {
	return Party44Choice{FIId: FinancialInstitution{
		FinInstnId: FinancialInstitutionIdentification{BICFI: b},
	}}
}

// AppHdr is the ISO 20022 business application header, head.001.001.02.
//
// It is the routing layer: who sent the message, who it is for, its own
// identifier, and — the field the codec turns on — which message definition the
// enclosed document follows. A receiver dispatches on MsgDefIdr, which is
// exactly what Unmarshal does.
//
// The standard has more fields than these: a business service, a market
// practice identifier, a priority, a duplicate flag, a signature. None is used
// here, and each is absent rather than empty.
type AppHdr struct {
	XMLName   xml.Name      `xml:"urn:iso:std:iso:20022:tech:xsd:head.001.001.02 AppHdr"`
	Fr        Party44Choice `xml:"Fr"`
	To        Party44Choice `xml:"To"`
	BizMsgIdr string        `xml:"BizMsgIdr"`
	MsgDefIdr string        `xml:"MsgDefIdr"`
	CreDt     ISODateTime   `xml:"CreDt"`
}

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
	return nil
}

// Envelope is a header and a document travelling together.
//
// The two inner elements are standard. The Envelope element around them is NOT:
// how a header and a document are packaged is a property of the network
// carrying them, and every clearing house does it differently. This is this
// repository's stand-in. See the package doc.
// Note the ABSENCE of struct tags on AppHdr and Document. Both are named by
// their own XMLName field's tag, which is what carries the namespace; a tag
// here would name them again without one, and encoding/xml would then have two
// disagreeing sources for the same element. Document is an interface, so its
// element name comes from whichever concrete message it holds — which is the
// mechanism that lets one Envelope type carry all four messages.
type Envelope struct {
	XMLName  xml.Name `xml:"Envelope"`
	AppHdr   AppHdr
	Document Document
}
