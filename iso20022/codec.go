package iso20022

import (
	"encoding/xml"
	"fmt"
)

// Marshal renders an envelope as an XML document.
//
// It validates the whole tree first, so a message that would be rejected by a
// counterparty is a Go error here instead. That matters more than it looks:
// encoding/xml cannot express an xsd:choice, so without this the only thing
// that would notice a document with both an IBAN and an Othr is the receiving
// bank.
//
// Output is indented. These documents are read by people in this repository —
// they end up in golden files and, in sub-project 7c, on a screen.
func Marshal(env Envelope) ([]byte, error) {
	if env.Document == nil {
		return nil, fmt.Errorf("%w: Document", ErrMissingElement)
	}
	if err := env.AppHdr.validate(); err != nil {
		return nil, err
	}
	if got, want := env.AppHdr.MsgDefIdr, env.Document.MessageDefinitionIdentifier(); got != want {
		return nil, fmt.Errorf("%w: header says %q, document is %q",
			ErrMessageDefinitionMismatch, got, want)
	}
	if err := env.Document.validate(); err != nil {
		return nil, err
	}
	body, err := xml.MarshalIndent(env, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), body...), nil
}

// rawEnvelope is the first of Unmarshal's two passes: it reads the header, and
// captures the document as unparsed bytes plus its element name.
//
// Two passes are needed because the header says which type the document is, and
// encoding/xml has no way to choose a destination type mid-parse. This is what
// a real receiver does too: read the header, route on MsgDefIdr, then parse.
type rawEnvelope struct {
	XMLName  xml.Name `xml:"Envelope"`
	AppHdr   AppHdr
	Document struct {
		// XMLName is captured, not declared: it is how the document's actual
		// namespace is recovered so it can be checked against the header.
		XMLName xml.Name
		Inner   []byte `xml:",innerxml"`
	} `xml:"Document"`
}

// Unmarshal parses an XML document into an envelope, choosing the document type
// from the header's MsgDefIdr.
//
// It is the one function in this repository that consumes bytes it did not
// produce, so it is written to fail rather than to guess: an unknown message
// definition, a header that disagrees with the document's namespace, and a
// document missing a mandatory element are each a named error.
func Unmarshal(data []byte) (Envelope, error) {
	var raw rawEnvelope
	if err := xml.Unmarshal(data, &raw); err != nil {
		return Envelope{}, err
	}
	if raw.AppHdr.MsgDefIdr == "" {
		return Envelope{}, fmt.Errorf("%w: AppHdr/MsgDefIdr", ErrMissingElement)
	}
	dt, ok := documentRegistry[raw.AppHdr.MsgDefIdr]
	if !ok {
		return Envelope{}, fmt.Errorf("%w: %q", ErrUnknownMessageDefinition, raw.AppHdr.MsgDefIdr)
	}
	if raw.Document.XMLName.Space != dt.namespace {
		return Envelope{}, fmt.Errorf("%w: header says %q, document namespace is %q",
			ErrMessageDefinitionMismatch, raw.AppHdr.MsgDefIdr, raw.Document.XMLName.Space)
	}
	doc := dt.make()
	// The captured inner XML is re-wrapped in a Document element carrying the
	// namespace already checked above. The namespace must be restated here:
	// each message's XMLName tag names its own namespace (see e.g. testDoc),
	// and encoding/xml's root-element match is exact — a wrapper with no
	// namespace does not match a field that declares one. The children below
	// it are unaffected either way, since their own tags carry no namespace of
	// their own and match regardless of what the parent declares.
	fragment := append(append([]byte(`<Document xmlns="`+dt.namespace+`">`), raw.Document.Inner...), []byte("</Document>")...)
	if err := xml.Unmarshal(fragment, doc); err != nil {
		return Envelope{}, err
	}
	if err := doc.validate(); err != nil {
		return Envelope{}, err
	}
	return Envelope{AppHdr: raw.AppHdr, Document: doc}, nil
}
