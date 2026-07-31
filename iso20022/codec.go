package iso20022

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
)

// Marshal renders an envelope as an XML document.
//
// It validates the whole tree first, so a message that would be rejected by a
// counterparty is a Go error here instead. That matters more than it looks:
// encoding/xml cannot express an xsd:choice, so without this the only thing
// that would notice a document with both an IBAN and an Othr is the receiving
// bank. It also checks the document against documentRegistry before emitting
// anything: a Document Unmarshal could not dispatch back to a concrete type is
// not a document this codec can round-trip, and Marshal refuses to produce one.
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
	want := env.Document.MessageDefinitionIdentifier()
	if _, ok := documentRegistry[want]; !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownMessageDefinition, want)
	}
	if got := env.AppHdr.MsgDefIdr; got != want {
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

// Unmarshal parses an XML document into an envelope, choosing the document type
// from the header's MsgDefIdr.
//
// It is the one function in this repository that consumes bytes it did not
// produce, so it is written to fail rather than to guess: a root element that
// is not Envelope, a second top-level element after the first one closes, an
// unknown message definition, a header that disagrees with the document's
// namespace, a missing Document, and a document missing a mandatory element
// are each a named error.
//
// It reads the input as a single stream of tokens from one *xml.Decoder,
// rather than unmarshalling into a struct and then re-parsing a captured
// fragment of the document. Two passes are still needed — the header names
// the type before the document's own bytes can be decoded into it — but the
// SAME decoder is used for both, so the Document element's own namespace
// declarations, default or prefixed, stay in scope for its whole subtree. A
// version that captured innerxml and re-wrapped it in a bare "<Document>" tag
// dropped every xmlns:* prefix binding along with it: a message written with
// a namespace prefix would decode with no error and an empty field, because
// encoding/xml does not treat an undefined prefix as a decode failure. See
// TestUnmarshalPreservesPrefixedNamespaceBindings.
//
// The token walk only ever dispatches on the element at depth 2 (AppHdr,
// Document); nothing checked what depth 1 actually was, so any wrapper
// element worked, including a bare <Document> as the root. It is checked
// explicitly here instead — deliberately with a plain error rather than one
// of this package's sentinels: a wrong root is a structurally different
// document, not a mandatory element that happens to be missing, and dressing
// it up as ErrMissingElement would name the wrong problem, the same mistake
// the missing-Document case made before it was fixed. The same check also
// catches a second top-level element appearing after the first root closes
// (rootClosed below): silently reading only the first, or only the last, of
// two concatenated envelopes is a parser differential two banks could
// disagree on, so it is refused rather than left to guesswork. Trailing
// non-element content — whitespace, a comment — after a valid envelope is NOT
// rejected: this function returns as soon as the Document decodes and
// validates, before the root's own closing tag is even read, so nothing after
// it is inspected either way.
func Unmarshal(data []byte) (Envelope, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))

	var hdr AppHdr
	haveHdr := false
	depth := 0
	rootClosed := false

	for {
		tok, err := dec.Token()
		if err != nil {
			if haveHdr && errors.Is(err, io.EOF) {
				return Envelope{}, fmt.Errorf("%w: Document", ErrMissingElement)
			}
			return Envelope{}, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 {
				if rootClosed {
					return Envelope{}, fmt.Errorf("iso20022: unexpected element %q after the root element closed", t.Name.Local)
				}
				if t.Name.Local != "Envelope" {
					return Envelope{}, fmt.Errorf("iso20022: root element is %q, want %q", t.Name.Local, "Envelope")
				}
				continue
			}
			if depth != 2 {
				continue
			}
			switch t.Name.Local {
			case "AppHdr":
				if err := dec.DecodeElement(&hdr, &t); err != nil {
					return Envelope{}, err
				}
				depth-- // DecodeElement consumed AppHdr's own EndElement too.
				if hdr.MsgDefIdr == "" {
					return Envelope{}, fmt.Errorf("%w: AppHdr/MsgDefIdr", ErrMissingElement)
				}
				haveHdr = true
			case "Document":
				if !haveHdr {
					return Envelope{}, fmt.Errorf("%w: AppHdr", ErrMissingElement)
				}
				dt, ok := documentRegistry[hdr.MsgDefIdr]
				if !ok {
					return Envelope{}, fmt.Errorf("%w: %q", ErrUnknownMessageDefinition, hdr.MsgDefIdr)
				}
				if t.Name.Space != dt.namespace {
					return Envelope{}, fmt.Errorf("%w: header says %q, document namespace is %q",
						ErrMessageDefinitionMismatch, hdr.MsgDefIdr, t.Name.Space)
				}
				doc := dt.make()
				if err := dec.DecodeElement(doc, &t); err != nil {
					return Envelope{}, err
				}
				if err := doc.validate(); err != nil {
					return Envelope{}, err
				}
				return Envelope{AppHdr: hdr, Document: doc}, nil
			default:
				// An element this codec does not recognize. Skip it rather than
				// walk into it, so the depth count stays correct for whatever
				// follows.
				if err := dec.Skip(); err != nil {
					return Envelope{}, err
				}
				depth--
			}
		case xml.EndElement:
			depth--
			if depth == 0 {
				rootClosed = true
			}
		}
	}
}
