package iso20022

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
)

// utf8BOM is the UTF-8 encoding of U+FEFF, the byte order mark.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Marshal renders an envelope as an XML document.
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
func Unmarshal(data []byte) (Envelope, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))

	var hdr AppHdr
	haveHdr := false
	depth := 0
	rootClosed := false
	// tokens counts what the decoder has handed over. Only one rule needs it, and
	// it needs only the value 1: a byte order mark is a property of the entity's
	// very start, so it can be recognised in the first token and nowhere else.
	tokens := 0
	var result *Envelope

	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if result != nil {
					return *result, nil
				}
				if haveHdr {
					return Envelope{}, fmt.Errorf("%w: Document", ErrMissingElement)
				}
				if rootClosed {
					// An envelope that opened and closed without a header. The document cannot
					// be named, so AppHdr is the element to report — and it is a missing
					// mandatory element, exactly as Document is.
					return Envelope{}, fmt.Errorf("%w: AppHdr", ErrMissingElement)
				}
				return Envelope{}, errors.New("iso20022: input contains no root element")
			}
			return Envelope{}, notEOF(err)
		}
		tokens++

		switch t := tok.(type) {
		case xml.StartElement:
			if result != nil && depth == 0 {
				return Envelope{}, fmt.Errorf("iso20022: unexpected element %q after a valid envelope closed", t.Name.Local)
			}
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
				if haveHdr {
					return Envelope{}, errors.New("iso20022: a second AppHdr in one envelope")
				}
				if err := dec.DecodeElement(&hdr, &t); err != nil {
					return Envelope{}, notEOF(err)
				}
				depth-- // DecodeElement consumed AppHdr's own EndElement too.
				if err := hdr.validate(); err != nil {
					return Envelope{}, err
				}
				haveHdr = true
			case "Document":
				if result != nil {
					return Envelope{}, errors.New("iso20022: a second Document in one envelope")
				}
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
					return Envelope{}, notEOF(err)
				}
				if err := doc.validate(); err != nil {
					return Envelope{}, err
				}
				depth-- // DecodeElement consumed Document's own EndElement too.
				result = &Envelope{AppHdr: hdr, Document: doc}
			default:
				// An element this codec does not recognize. Skip it rather than
				// walk into it, so the depth count stays correct for whatever
				// follows.
				if err := dec.Skip(); err != nil {
					return Envelope{}, notEOF(err)
				}
				depth--
			}
		case xml.EndElement:
			depth--
			if depth == 0 {
				rootClosed = true
			}
		case xml.CharData:
			// Depth 0 is OUTSIDE the root element, in either direction: the prolog
			// before <Envelope> and whatever follows </Envelope> are the same position
			// as far as the XML grammar is concerned, and XML 1.0 forbids character data
			// in both.
			text := []byte(t)
			if tokens == 1 {
				text = bytes.TrimPrefix(text, utf8BOM)
			}
			if depth == 0 && len(bytes.TrimSpace(text)) != 0 {
				return Envelope{}, errors.New("iso20022: unexpected character data outside the envelope")
			}
		case xml.Directive:
			// encoding/xml surfaces <!DOCTYPE ...> here, internal subset and all.
			if result != nil && depth == 0 {
				return Envelope{}, errors.New("iso20022: unexpected directive after a valid envelope closed")
			}
		}
	}
}

// notEOF keeps io.EOF from escaping Unmarshal.
func notEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("iso20022: input ended mid-document: %v", err)
	}
	return err
}
