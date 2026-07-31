package iso20022

import (
	"encoding/xml"
	"errors"
	"testing"
	"time"
)

// testDoc is a stand-in message, registered only for the codec's own tests. It
// exists so the codec can be tested before any real message is written, and so
// the dispatch failures below have something well-formed to contrast with.
type testDoc struct {
	XMLName xml.Name `xml:"urn:example:test.001.001.01 Document"`
	Body    string   `xml:"Body"`
}

func (testDoc) MessageDefinitionIdentifier() string { return "test.001.001.01" }
func (testDoc) namespace() string                   { return "urn:example:test.001.001.01" }
func (d testDoc) validate() error {
	if d.Body == "" {
		return errors.New("test: empty body")
	}
	return nil
}

func init() {
	registerDocument("test.001.001.01", "urn:example:test.001.001.01",
		func() Document { return &testDoc{} })
}

func testEnvelope() Envelope {
	return Envelope{
		AppHdr: AppHdr{
			Fr:        agent("AURTSESSXXX"),
			To:        agent("CSMBFRPPXXX"),
			BizMsgIdr: "AURTSESSXXX-20260731-000001",
			MsgDefIdr: "test.001.001.01",
			CreDt:     ISODateTime{time.Date(2026, 7, 31, 9, 30, 0, 0, time.UTC)},
		},
		Document: &testDoc{Body: "hello"},
	}
}

const wantEnvelopeXML = `<?xml version="1.0" encoding="UTF-8"?>
<Envelope>
  <AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">
    <Fr>
      <FIId>
        <FinInstnId>
          <BICFI>AURTSESSXXX</BICFI>
        </FinInstnId>
      </FIId>
    </Fr>
    <To>
      <FIId>
        <FinInstnId>
          <BICFI>CSMBFRPPXXX</BICFI>
        </FinInstnId>
      </FIId>
    </To>
    <BizMsgIdr>AURTSESSXXX-20260731-000001</BizMsgIdr>
    <MsgDefIdr>test.001.001.01</MsgDefIdr>
    <CreDt>2026-07-31T09:30:00Z</CreDt>
  </AppHdr>
  <Document xmlns="urn:example:test.001.001.01">
    <Body>hello</Body>
  </Document>
</Envelope>`

func TestMarshalEmitsBothNamespaces(t *testing.T) {
	out, err := Marshal(testEnvelope())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if string(out) != wantEnvelopeXML {
		t.Fatalf("Marshal() =\n%s\n\nwant\n%s", out, wantEnvelopeXML)
	}
}

func TestUnmarshalDispatchesOnMsgDefIdr(t *testing.T) {
	env, err := Unmarshal([]byte(wantEnvelopeXML))
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	doc, ok := env.Document.(*testDoc)
	if !ok {
		t.Fatalf("Document is %T, want *testDoc", env.Document)
	}
	if doc.Body != "hello" {
		t.Fatalf("Body = %q, want hello", doc.Body)
	}
	if env.AppHdr.Fr.FIId.FinInstnId.BICFI != "AURTSESSXXX" {
		t.Fatalf("Fr BIC = %q, want AURTSESSXXX", env.AppHdr.Fr.FIId.FinInstnId.BICFI)
	}
}

func TestUnmarshalRejects(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want error
	}{
		{
			name: "unknown message definition",
			in: `<Envelope><AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">` +
				`<Fr><FIId><FinInstnId><BICFI>AURTSESSXXX</BICFI></FinInstnId></FIId></Fr>` +
				`<To><FIId><FinInstnId><BICFI>CSMBFRPPXXX</BICFI></FinInstnId></FIId></To>` +
				`<BizMsgIdr>x</BizMsgIdr><MsgDefIdr>camt.053.001.08</MsgDefIdr>` +
				`<CreDt>2026-07-31T09:30:00Z</CreDt></AppHdr>` +
				`<Document xmlns="urn:example:test.001.001.01"><Body>hello</Body></Document></Envelope>`,
			want: ErrUnknownMessageDefinition,
		},
		{
			name: "header disagrees with document namespace",
			in: `<Envelope><AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">` +
				`<Fr><FIId><FinInstnId><BICFI>AURTSESSXXX</BICFI></FinInstnId></FIId></Fr>` +
				`<To><FIId><FinInstnId><BICFI>CSMBFRPPXXX</BICFI></FinInstnId></FIId></To>` +
				`<BizMsgIdr>x</BizMsgIdr><MsgDefIdr>test.001.001.01</MsgDefIdr>` +
				`<CreDt>2026-07-31T09:30:00Z</CreDt></AppHdr>` +
				`<Document xmlns="urn:example:other.001.001.01"><Body>hello</Body></Document></Envelope>`,
			want: ErrMessageDefinitionMismatch,
		},
		{
			name: "truncated",
			in:   `<Envelope><AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02"><Fr>`,
			want: nil, // any error, from the XML decoder
		},
		{
			name: "not XML at all",
			in:   `{"msg": "this is json"}`,
			want: nil,
		},
		{
			name: "empty",
			in:   ``,
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Unmarshal([]byte(tt.in))
			if err == nil {
				t.Fatalf("Unmarshal() = nil, want an error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("Unmarshal() = %v, want it to wrap %v", err, tt.want)
			}
		})
	}
}

func TestMarshalValidatesTheDocument(t *testing.T) {
	env := testEnvelope()
	env.Document = &testDoc{} // empty body fails testDoc.validate
	if _, err := Marshal(env); err == nil {
		t.Fatal("Marshal() = nil, want the document's validation error")
	}
}

func TestMarshalRejectsAHeaderThatDisagreesWithItsDocument(t *testing.T) {
	env := testEnvelope()
	env.AppHdr.MsgDefIdr = "pacs.008.001.08"
	if _, err := Marshal(env); !errors.Is(err, ErrMessageDefinitionMismatch) {
		t.Fatalf("Marshal() = %v, want it to wrap ErrMessageDefinitionMismatch", err)
	}
}

// prefixedTestDoc is registered only to prove that Unmarshal preserves an
// xmlns:* prefix binding declared on the Document start element, rather than
// losing it. Its Body field names the message's namespace explicitly — the
// way a real message written with a prefix would — so a decode that lost the
// prefix binding leaves Body unresolved and the field empty, rather than
// erroring. That silence is exactly what makes the bug dangerous: see
// TestUnmarshalPreservesPrefixedNamespaceBindings.
type prefixedTestDoc struct {
	XMLName xml.Name `xml:"urn:example:test.002.001.01 Document"`
	Body    string   `xml:"urn:example:test.002.001.01 Body"`
}

func (prefixedTestDoc) MessageDefinitionIdentifier() string { return "test.002.001.01" }
func (prefixedTestDoc) namespace() string                   { return "urn:example:test.002.001.01" }
func (d prefixedTestDoc) validate() error {
	if d.Body == "" {
		return errors.New("test: empty body")
	}
	return nil
}

func init() {
	registerDocument("test.002.001.01", "urn:example:test.002.001.01",
		func() Document { return &prefixedTestDoc{} })
}

// TestUnmarshalPreservesPrefixedNamespaceBindings pins a message whose
// Document element uses a namespace prefix rather than a default xmlns. A
// byte-rewrap that only copies the captured innerxml, without the start
// element's own attributes, drops the "xmlns:p" declaration; the inner
// <p:Body> then resolves to the undefined prefix "p" instead of the message's
// namespace, and encoding/xml silently leaves Body empty rather than
// erroring. Unmarshal must decode through a live *xml.Decoder so the prefix
// binding stays in scope for the whole subtree.
func TestUnmarshalPreservesPrefixedNamespaceBindings(t *testing.T) {
	in := `<Envelope><AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">` +
		`<Fr><FIId><FinInstnId><BICFI>AURTSESSXXX</BICFI></FinInstnId></FIId></Fr>` +
		`<To><FIId><FinInstnId><BICFI>CSMBFRPPXXX</BICFI></FinInstnId></FIId></To>` +
		`<BizMsgIdr>x</BizMsgIdr><MsgDefIdr>test.002.001.01</MsgDefIdr>` +
		`<CreDt>2026-07-31T09:30:00Z</CreDt></AppHdr>` +
		`<p:Document xmlns:p="urn:example:test.002.001.01"><p:Body>hello</p:Body></p:Document></Envelope>`

	env, err := Unmarshal([]byte(in))
	if err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	doc, ok := env.Document.(*prefixedTestDoc)
	if !ok {
		t.Fatalf("Document is %T, want *prefixedTestDoc", env.Document)
	}
	if doc.Body != "hello" {
		t.Fatalf("Body = %q, want %q — the p: prefix binding was lost during Unmarshal", doc.Body, "hello")
	}
}

// TestUnmarshalRejectsAnEnvelopeWithNoDocument pins the missing-<Document>
// case to ErrMissingElement, matching what Marshal already reports for a nil
// Document. Before this fix it reported ErrMessageDefinitionMismatch with an
// empty document namespace, which named the wrong problem.
func TestUnmarshalRejectsAnEnvelopeWithNoDocument(t *testing.T) {
	in := `<Envelope><AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">` +
		`<Fr><FIId><FinInstnId><BICFI>AURTSESSXXX</BICFI></FinInstnId></FIId></Fr>` +
		`<To><FIId><FinInstnId><BICFI>CSMBFRPPXXX</BICFI></FinInstnId></FIId></To>` +
		`<BizMsgIdr>x</BizMsgIdr><MsgDefIdr>test.001.001.01</MsgDefIdr>` +
		`<CreDt>2026-07-31T09:30:00Z</CreDt></AppHdr></Envelope>`

	_, err := Unmarshal([]byte(in))
	if !errors.Is(err, ErrMissingElement) {
		t.Fatalf("Unmarshal() = %v, want it to wrap ErrMissingElement", err)
	}
}

// unregisteredDoc satisfies Document but is deliberately never passed to
// registerDocument, so it proves Marshal will not emit a message the codec
// could not later dispatch back through Unmarshal.
type unregisteredDoc struct {
	XMLName xml.Name `xml:"urn:example:unregistered.001.001.01 Document"`
	Body    string   `xml:"Body"`
}

func (unregisteredDoc) MessageDefinitionIdentifier() string { return "unregistered.001.001.01" }
func (unregisteredDoc) namespace() string                   { return "urn:example:unregistered.001.001.01" }
func (d unregisteredDoc) validate() error                   { return nil }

// TestMarshalRejectsADocumentTheRegistryDoesNotKnow pins the doc comment's
// claim on Document — "a Document the codec cannot dispatch to is not a
// Document" — as something Marshal actually checks, not just Unmarshal.
func TestMarshalRejectsADocumentTheRegistryDoesNotKnow(t *testing.T) {
	env := testEnvelope()
	env.AppHdr.MsgDefIdr = "unregistered.001.001.01"
	env.Document = &unregisteredDoc{Body: "hello"}
	if _, err := Marshal(env); !errors.Is(err, ErrUnknownMessageDefinition) {
		t.Fatalf("Marshal() = %v, want it to wrap ErrUnknownMessageDefinition", err)
	}
}

// TestRegisterDocumentPanicsWhenMessageDefinitionIdentifierDisagrees pins
// registerDocument's self-consistency check: the registry key it is given
// must agree with what the constructed value itself reports, or the registry
// becomes a second, unreconciled source of truth.
func TestRegisterDocumentPanicsWhenMessageDefinitionIdentifierDisagrees(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("registerDocument did not panic for a msgDefIdr disagreeing with MessageDefinitionIdentifier()")
		}
	}()
	// testDoc reports "test.001.001.01", not this key.
	registerDocument("mismatched-msgdefidr.001.001.01", "urn:example:test.001.001.01",
		func() Document { return &testDoc{} })
}

// TestUnmarshalRejectsAWrongRootElement pins the root element's own identity:
// an otherwise well-formed header and document wrapped in something other
// than <Envelope> must fail, not decode successfully. The token walk that
// replaced rawEnvelope only ever inspects the element at depth 2 (AppHdr,
// Document) — it never looked at what depth 1 actually was, so any wrapper
// element worked by accident, including <Document> itself.
func TestUnmarshalRejectsAWrongRootElement(t *testing.T) {
	in := `<NotAnEnvelope><AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">` +
		`<Fr><FIId><FinInstnId><BICFI>AURTSESSXXX</BICFI></FinInstnId></FIId></Fr>` +
		`<To><FIId><FinInstnId><BICFI>CSMBFRPPXXX</BICFI></FinInstnId></FIId></To>` +
		`<BizMsgIdr>x</BizMsgIdr><MsgDefIdr>test.001.001.01</MsgDefIdr>` +
		`<CreDt>2026-07-31T09:30:00Z</CreDt></AppHdr>` +
		`<Document xmlns="urn:example:test.001.001.01"><Body>hello</Body></Document></NotAnEnvelope>`

	if _, err := Unmarshal([]byte(in)); err == nil {
		t.Fatal("Unmarshal() = nil, want an error for a root element that is not Envelope")
	}
}

// TestUnmarshalRejectsContentAfterTheRootCloses pins the single-root
// requirement: a second top-level element after the first </Envelope> must
// fail rather than silently become the envelope that gets parsed. Without
// this, an attacker (or a buggy peer) could smuggle two envelopes into one
// message and rely on this codec picking whichever one a naive parser lands
// on last, which is exactly the kind of parser differential that matters
// between two banks.
func TestUnmarshalRejectsContentAfterTheRootCloses(t *testing.T) {
	second := `<Envelope><AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">` +
		`<Fr><FIId><FinInstnId><BICFI>AURTSESSXXX</BICFI></FinInstnId></FIId></Fr>` +
		`<To><FIId><FinInstnId><BICFI>CSMBFRPPXXX</BICFI></FinInstnId></FIId></To>` +
		`<BizMsgIdr>x</BizMsgIdr><MsgDefIdr>test.001.001.01</MsgDefIdr>` +
		`<CreDt>2026-07-31T09:30:00Z</CreDt></AppHdr>` +
		`<Document xmlns="urn:example:test.001.001.01"><Body>hello</Body></Document></Envelope>`
	in := `<Envelope></Envelope>` + second

	if _, err := Unmarshal([]byte(in)); err == nil {
		t.Fatal("Unmarshal() = nil, want an error for a second top-level element after the root closed")
	}
}

// TestUnmarshalRejectsASecondValidEnvelope pins the case
// TestUnmarshalRejectsContentAfterTheRootCloses does NOT cover: two complete,
// well-formed, individually valid envelopes concatenated back to back. That
// first attempt at fixing "content after the root" only ever set its guard
// inside the xml.EndElement branch, which Unmarshal never reaches on the
// success path — it returns as soon as Document decodes and validates,
// before consuming the root's own closing tag. So a first envelope that
// fully succeeds was never checked for what came after it, which is exactly
// the parser differential that matters: one reader takes the first envelope,
// another could be misled into taking the last.
func TestUnmarshalRejectsASecondValidEnvelope(t *testing.T) {
	in := wantEnvelopeXML + wantEnvelopeXML
	if _, err := Unmarshal([]byte(in)); err == nil {
		t.Fatal("Unmarshal() = nil, want an error for a second, complete envelope concatenated after a valid one")
	}
}

// TestUnmarshalAcceptsTrailingWhitespaceAndComments pins the other half of
// the same fix: refusing a second envelope must not become refusing
// everything after the first one. Whitespace and a comment carry no content
// of their own and are legal trailing bytes.
func TestUnmarshalAcceptsTrailingWhitespaceAndComments(t *testing.T) {
	in := wantEnvelopeXML + "\n  \n<!-- a trailing comment -->\n  "
	env, err := Unmarshal([]byte(in))
	if err != nil {
		t.Fatalf("Unmarshal() error = %v, want trailing whitespace and a comment to be accepted", err)
	}
	if env.AppHdr.BizMsgIdr != "AURTSESSXXX-20260731-000001" {
		t.Fatalf("BizMsgIdr = %q, want the envelope decoded from wantEnvelopeXML", env.AppHdr.BizMsgIdr)
	}
}

// TestRegisterDocumentPanicsWhenNamespaceDisagrees is
// TestRegisterDocumentPanicsWhenMessageDefinitionIdentifierDisagrees's
// counterpart for the namespace argument.
func TestRegisterDocumentPanicsWhenNamespaceDisagrees(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("registerDocument did not panic for a namespace disagreeing with namespace()")
		}
	}()
	// testDoc reports "urn:example:test.001.001.01", not this namespace.
	registerDocument("mismatched-namespace.001.001.01", "urn:example:wrong",
		func() Document { return &testDoc{} })
}
