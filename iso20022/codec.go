package iso20022

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
)

// utf8BOM is the UTF-8 encoding of U+FEFF, the byte order mark.
//
// Written as bytes rather than as the character, because Go's own compiler
// rejects a literal BOM anywhere but the first position of a source file.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

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
// Output is indented. These documents are read by people in this repository:
// they end up in golden files and on a screen.
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
// produce, so it fails rather than guesses: a root element that is not
// Envelope, a second top-level element (complete or not) anywhere after the
// first one, a repeated AppHdr or Document inside one envelope, a DTD after a
// complete envelope, an unknown message definition, a header that disagrees
// with the document's namespace, a missing Document, a header that is not
// itself valid, and a document missing a mandatory element are each an error.
// The sections below are the authority on the exact set; this sentence is a
// summary and does not promise to be exhaustive.
//
// Five checks return a plain error rather than one of this package's sentinels:
// the root's name, there being only one top-level element, what may follow a
// closed envelope, a repeated AppHdr or Document, and input that never contained
// a root element at all. Each says the input is a structurally different
// document, not a mandatory element that happens to be missing, so naming them
// ErrMissingElement would misname the problem. A missing Document is deliberately
// NOT among them — that one really is an absent mandatory element. Nor is a
// missing AppHdr: an envelope that opened and closed carrying neither element
// would otherwise come back as a bare io.EOF. See the notEOF section below.
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
// # Decoding the document does not end the function
//
// dec.DecodeElement(doc, &t) consumes through Document's own end tag and
// stops there, leaving the rest of the input — at minimum the root's own
// </Envelope>, possibly a great deal more — unread. Unmarshal keeps draining
// tokens after that, all the way to EOF, instead of returning as soon as it
// has a usable result. Returning early would accept a second, complete
// <Envelope>...</Envelope> concatenated after the first: silently reading
// only the one this function happened to reach, while a different parser
// reads the other, which is exactly the kind of disagreement two banks must
// not have about the same bytes. This makes Unmarshal's cost proportional to
// the WHOLE input rather than just the prefix containing the document — the
// right tradeoff for the one function that validates bytes this package did
// not produce, not an oversight.
//
// The rule this enforces is about the ENVELOPE, not about the Document. It
// takes effect only once a valid document has been read AND the root element
// has closed — depth back to zero — not the moment </Document> is decoded.
// The distinction is not academic: <Sgntr> is a real element of the ISO 20022
// business message envelope and it follows the document, so a guard that fired
// on the document's close would refuse legitimate input, and would refuse it
// only in that position, since the same element before <Document> is skipped
// by the default branch below. See
// TestUnmarshalAcceptsAnUnknownElementAfterTheDocument,
// TestUnmarshalAcceptsTextBeforeTheEnvelopeCloses and
// TestUnmarshalAcceptsUndispatchedElementsAnywhere, which pins the
// before-position that makes the rule symmetric.
//
// Once the root has closed on a valid envelope, what remains is scrutinised:
// a further xml.StartElement is refused, whether it is another <Envelope> or
// anything else; and an xml.Directive is refused, because a DOCTYPE's internal
// subset can declare entities and so is the one trailing construct that carries
// content of its own — a DTD arriving after a complete business message has no
// legitimate meaning. What stays legal is what carries nothing: whitespace,
// comments and processing instructions. See
// TestUnmarshalRejectsASecondValidEnvelope,
// TestUnmarshalRejectsATrailingDirective,
// TestUnmarshalAcceptsTrailingWhitespaceAndComments and
// TestUnmarshalAcceptsATrailingProcessingInstruction.
//
// Non-blank character data is refused OUTSIDE the root in either direction,
// which is a wider rule than the three above and deliberately not conditioned
// on having a result yet. Its one exception is a leading UTF-8 byte order
// mark, which XML 1.0 §4.3.3 makes part of the entity's encoding
// autodetection rather than character data, so a conforming parser accepts it
// and this one must too. See the xml.CharData case below,
// TestUnmarshalRejectsTextOutsideTheEnvelope and
// TestUnmarshalAcceptsALeadingByteOrderMark.
//
// # io.EOF never escapes
//
// The guarantee is about the RESULT, not about a code path:
// errors.Is(err, io.EOF) is false for every error this function returns.
//
// It is reached two different ways, because the errors come from two places.
// The errors this package constructs — the sentinels and the structural ones —
// were never io.EOF to begin with and need nothing done to them. The errors
// that come out of the decoder do: a clean end of input is answered by the
// branch below, which reports what was actually missing instead of handing
// io.EOF back, and every other decoder error — from DecodeElement, from Skip,
// from a mid-stream token — goes through notEOF.
//
// io.EOF is the one error a transport layer is idiomatically required to read as
// "the peer closed cleanly, read more" rather than "this content is bad", and
// this function sits behind a network boundary — where a framing bug that
// truncated a body would otherwise arrive as a clean disconnect. See
// TestUnmarshalSurvivesHostileInput and FuzzUnmarshal.
//
// The envelope-level guard says nothing about repetition INSIDE one envelope,
// so both elements this codec dispatches on are guarded where they are
// dispatched instead. Neither <AppHdr> nor <Document> may appear twice: the
// duplicate is the same parser differential as two concatenated envelopes,
// one level down. It bit hardest on the header, which is what drives dispatch
// — and which copy won depended on position, since a second header before
// <Document> overwrote the first before dispatch while a second after it was
// decoded and then discarded, result having already snapshotted the first.
// See TestUnmarshalRejectsASecondDocumentInTheSameEnvelope and
// TestUnmarshalRejectsASecondAppHdrInTheSameEnvelope, which pins both
// positions.
//
// The envelope's OWN children that this codec does not dispatch on may repeat
// freely; they are skipped either way. See
// TestUnmarshalAcceptsUndispatchedElementsAnywhere.
//
// That is scoped to the envelope's children on purpose, because it is not true
// one level down. Inside a subtree handed to dec.DecodeElement — an AppHdr, or
// any message — encoding/xml is LAST-WINS for a repeated scalar child, so a
// second <BizMsgIdr> or a second <MsgDefIdr> is neither skipped nor refused: it
// silently replaces the first. Two banks reading the same bytes with different
// parsers can therefore disagree about which value was sent, and this codec
// takes the last. It is a known residual rather than a defect being fixed here:
// every actor in this repository decodes through this same function, so they
// agree with each other, and the package doc states plainly that
// interoperability with a foreign parser is not what this envelope is for.
//
// # The header is validated on the way in, not only on the way out
//
// hdr.validate() runs here rather than only in Marshal, and that symmetry is
// load-bearing rather than tidy. Unmarshal already refuses a document missing
// a mandatory element, so a header that Marshal would refuse had to be
// refused too: without it, Unmarshal accepted an envelope whose Fr or To
// carried no well-formed BIC, and the value it returned could then never be
// re-marshalled — err == nil on the way in and an error on the way back out,
// for bytes this package had itself just accepted. FuzzUnmarshal asserts
// exactly that round trip and found this within seconds of first being run;
// see TestUnmarshalRejectsAHeaderItCouldNotReMarshal.
//
// It subsumes the older MsgDefIdr != "" check, which was the same
// ErrMissingElement on the same element and is now one of the five things
// AppHdr.validate() covers.
//
// A second, SEPARATE guard (rootClosed below) covers the case where the
// first top-level element closes WITHOUT ever producing a valid result — an
// empty or incomplete first <Envelope> followed by more content. That case
// never reaches the checks above, since there is no result yet, so it needs
// its own check that a new element cannot legally follow a closed root at
// depth 1. See TestUnmarshalRejectsContentAfterTheRootCloses.
func Unmarshal(data []byte) (Envelope, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))

	var hdr AppHdr
	haveHdr := false
	depth := 0
	rootClosed := false
	// tokens counts what the decoder has handed over. Only one rule needs it,
	// and it needs only the value 1: a byte order mark is a property of the
	// entity's very start, so it can be recognised in the first token and
	// nowhere else. See the xml.CharData case.
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
					// An envelope that opened and closed without a header. The
					// document cannot be named, so AppHdr is the element to
					// report — and it is a missing mandatory element, exactly
					// as Document is.
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
			// before <Envelope> and whatever follows </Envelope> are the same
			// position as far as the XML grammar is concerned, and XML 1.0 forbids
			// character data in both. A guard that fired only once a result existed
			// would be a trailing-content rule and would leave the prolog open.
			// encoding/xml is lenient in the prolog and will not raise it for us.
			//
			// A leading UTF-8 byte order mark is the one thing at depth 0 that
			// LOOKS like character data and is not. XML 1.0 §4.3.3 and
			// Appendix F make it part of the entity's encoding autodetection,
			// so a conforming parser accepts it — and .NET and Windows
			// producers, which is to say a great deal of real bank and CSM
			// middleware, emit one by default. encoding/xml hands it over as
			// the first token, an xml.CharData of the three bytes EF BB BF, and
			// bytes.TrimSpace does not remove them because U+FEFF is not
			// unicode.IsSpace. Refusing it would be this asymmetry inverted:
			// this codec rejecting what a conforming parser accepts, which for
			// a codec whose whole job is reading a counterparty's message is a
			// payment that does not happen.
			//
			// Only in the FIRST token, though. The mark is a property of the
			// entity's start; anywhere else U+FEFF is ZERO WIDTH NO-BREAK
			// SPACE, ordinary character data, and stays refused — trimming it
			// unconditionally would reopen the hole this guard was widened to
			// close.
			text := []byte(t)
			if tokens == 1 {
				text = bytes.TrimPrefix(text, utf8BOM)
			}
			if depth == 0 && len(bytes.TrimSpace(text)) != 0 {
				return Envelope{}, errors.New("iso20022: unexpected character data outside the envelope")
			}
		case xml.Directive:
			// encoding/xml surfaces <!DOCTYPE ...> here, internal subset and
			// all. Unlike a comment or a processing instruction it can declare
			// entities, so it is not inert, and nothing legitimate declares a
			// DTD after the message it would have applied to.
			if result != nil && depth == 0 {
				return Envelope{}, errors.New("iso20022: unexpected directive after a valid envelope closed")
			}
		}
	}
}

// notEOF keeps io.EOF from escaping Unmarshal.
//
// A caller of Unmarshal is a caller at a boundary: bytes arrived from
// somewhere else and something has to decide what they were. io.EOF is the one
// error that already has a meaning at such a boundary — "the peer closed
// cleanly, read more" — and it is not the meaning any answer here has. A
// truncated body from a framing bug would otherwise report itself as a clean
// disconnect, which is the failure mode that hides rather than the one that
// stops.
//
// The wrapping is deliberately %v and not %w: the point is that
// errors.Is(err, io.EOF) is FALSE for everything this function returns. The
// original text is kept because it is the only thing that says where the input
// ran out.
func notEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("iso20022: input ended mid-document: %v", err)
	}
	return err
}
