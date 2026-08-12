package iso20022

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
			Fr:        NewAgent("AURTSESSXXX"),
			To:        NewAgent("CSMBFRPPXXX"),
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
				`<BizMsgIdr>x</BizMsgIdr><MsgDefIdr>camt.054.001.08</MsgDefIdr>` +
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
// losing it.
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

// TestUnmarshalPreservesPrefixedNamespaceBindings pins a message whose Document
// element uses a namespace prefix rather than a default xmlns.
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

// TestUnmarshalRejectsAnEnvelopeWithNoDocument pins the missing-<Document> case
// to ErrMissingElement, matching what Marshal already reports for a nil
// Document.
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
// registerDocument's self-consistency check: the registry key it is given must
// agree with what the constructed value itself reports, or the registry becomes
// a second, unreconciled source of truth.
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
// an otherwise well-formed header and document wrapped in something other than
// <Envelope> must fail, not decode successfully.
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
// requirement: a second top-level element after the first </Envelope> must fail
// rather than silently become the envelope that gets parsed.
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
// well-formed, individually valid envelopes concatenated back to back.
func TestUnmarshalRejectsASecondValidEnvelope(t *testing.T) {
	in := wantEnvelopeXML + wantEnvelopeXML
	if _, err := Unmarshal([]byte(in)); err == nil {
		t.Fatal("Unmarshal() = nil, want an error for a second, complete envelope concatenated after a valid one")
	}
}

// TestUnmarshalAcceptsTrailingWhitespaceAndComments pins the other half of the
// same fix: refusing a second envelope must not become refusing everything
// after the first one.
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

// envelopeAround wraps a valid header for test.001.001.01 and whatever body
// bytes the caller supplies in a single <Envelope>.
func envelopeAround(body string) string {
	return `<Envelope><AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">` +
		`<Fr><FIId><FinInstnId><BICFI>AURTSESSXXX</BICFI></FinInstnId></FIId></Fr>` +
		`<To><FIId><FinInstnId><BICFI>CSMBFRPPXXX</BICFI></FinInstnId></FIId></To>` +
		`<BizMsgIdr>AURTSESSXXX-20260731-000001</BizMsgIdr><MsgDefIdr>test.001.001.01</MsgDefIdr>` +
		`<CreDt>2026-07-31T09:30:00Z</CreDt></AppHdr>` + body + `</Envelope>`
}

const testDocumentXML = `<Document xmlns="urn:example:test.001.001.01"><Body>hello</Body></Document>`

// TestUnmarshalAcceptsAnUnknownElementAfterTheDocument pins that the
// single-envelope rule is about the ENVELOPE, not about the Document: an
// element the codec does not recognise, sitting between </Document> and
// </Envelope>, is skipped and the envelope still decodes. <Sgntr> is a real
// element of the ISO 20022 business message envelope, and it is defined to
// follow the document, so refusing it would refuse legitimate input.
func TestUnmarshalAcceptsAnUnknownElementAfterTheDocument(t *testing.T) {
	in := envelopeAround(testDocumentXML + `<Sgntr><X>signature</X></Sgntr>`)

	env, err := Unmarshal([]byte(in))
	if err != nil {
		t.Fatalf("Unmarshal() error = %v, want an element after <Document> but inside the envelope to be accepted", err)
	}
	doc, ok := env.Document.(*testDoc)
	if !ok {
		t.Fatalf("Document is %T, want *testDoc", env.Document)
	}
	if doc.Body != "hello" {
		t.Fatalf("Body = %q, want hello", doc.Body)
	}
}

// TestUnmarshalAcceptsTextBeforeTheEnvelopeCloses is
// TestUnmarshalAcceptsAnUnknownElementAfterTheDocument's counterpart for
// character data.
func TestUnmarshalAcceptsTextBeforeTheEnvelopeCloses(t *testing.T) {
	in := envelopeAround(testDocumentXML + `some trailing text inside the envelope`)

	env, err := Unmarshal([]byte(in))
	if err != nil {
		t.Fatalf("Unmarshal() error = %v, want text inside the envelope to be accepted", err)
	}
	if env.AppHdr.BizMsgIdr != "AURTSESSXXX-20260731-000001" {
		t.Fatalf("BizMsgIdr = %q, want the envelope to have decoded", env.AppHdr.BizMsgIdr)
	}
}

// TestUnmarshalRejectsASecondDocumentInTheSameEnvelope pins the one element the
// envelope-level rule must NOT let through twice.
func TestUnmarshalRejectsASecondDocumentInTheSameEnvelope(t *testing.T) {
	in := envelopeAround(testDocumentXML + testDocumentXML)

	if _, err := Unmarshal([]byte(in)); err == nil {
		t.Fatal("Unmarshal() = nil, want an error for a second <Document> in one envelope")
	}
}

// TestUnmarshalRejectsATrailingDirective pins the one trailing construct that
// carries content of its own. encoding/xml surfaces <!DOCTYPE ...> as an
// xml.Directive, and a DOCTYPE's internal subset can declare entities — so a
// DTD arriving after a complete business message is neither inert nor
// meaningful, and this function parses bytes it did not produce.
func TestUnmarshalRejectsATrailingDirective(t *testing.T) {
	for _, in := range []string{
		wantEnvelopeXML + "\n<!DOCTYPE x>",
		wantEnvelopeXML + "\n<!DOCTYPE x [<!ENTITY e 'boom'>]>",
	} {
		if _, err := Unmarshal([]byte(in)); err == nil {
			t.Fatalf("Unmarshal() = nil for %q, want an error for a directive after a complete envelope", in[len(wantEnvelopeXML):])
		}
	}
}

// TestUnmarshalAcceptsATrailingProcessingInstruction pins the processing
// instruction half of the doc comment's claim about what stays legal after the
// envelope closes.
func TestUnmarshalAcceptsATrailingProcessingInstruction(t *testing.T) {
	in := wantEnvelopeXML + "\n<?display mode=\"compact\"?>\n"

	env, err := Unmarshal([]byte(in))
	if err != nil {
		t.Fatalf("Unmarshal() error = %v, want a trailing processing instruction to be accepted", err)
	}
	if env.AppHdr.BizMsgIdr != "AURTSESSXXX-20260731-000001" {
		t.Fatalf("BizMsgIdr = %q, want the envelope decoded from wantEnvelopeXML", env.AppHdr.BizMsgIdr)
	}
}

// appHdrXML builds a header for test.001.001.01 carrying the given BizMsgIdr
// and MsgDefIdr, so the two duplicate-header tests below can make the two
// copies visibly disagree about which message definition the envelope holds.
func appHdrXML(bizMsgIdr, msgDefIdr string) string {
	return `<AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">` +
		`<Fr><FIId><FinInstnId><BICFI>AURTSESSXXX</BICFI></FinInstnId></FIId></Fr>` +
		`<To><FIId><FinInstnId><BICFI>CSMBFRPPXXX</BICFI></FinInstnId></FIId></To>` +
		`<BizMsgIdr>` + bizMsgIdr + `</BizMsgIdr><MsgDefIdr>` + msgDefIdr + `</MsgDefIdr>` +
		`<CreDt>2026-07-31T09:30:00Z</CreDt></AppHdr>`
}

// TestUnmarshalRejectsASecondAppHdrInTheSameEnvelope pins the header against
// the same duplication rule as <Document>, in BOTH positions, because the two
// positions failed differently and only one of them was ever refused.
func TestUnmarshalRejectsASecondAppHdrInTheSameEnvelope(t *testing.T) {
	first := appHdrXML("FIRST", "test.001.001.01")
	second := appHdrXML("SECOND", "test.001.001.01")

	tests := []struct {
		name string
		in   string
	}{
		{
			name: "before the document",
			in:   `<Envelope>` + first + second + testDocumentXML + `</Envelope>`,
		},
		{
			name: "after the document",
			in:   `<Envelope>` + first + testDocumentXML + second + `</Envelope>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := Unmarshal([]byte(tt.in))
			if err == nil {
				t.Fatalf("Unmarshal() = nil (BizMsgIdr = %q, MsgDefIdr = %q), want an error for a second <AppHdr> in one envelope",
					env.AppHdr.BizMsgIdr, env.AppHdr.MsgDefIdr)
			}
		})
	}
}

// TestUnmarshalAcceptsUndispatchedElementsAnywhere pins the two claims the
// duplicate guards do NOT make.
func TestUnmarshalAcceptsUndispatchedElementsAnywhere(t *testing.T) {
	hdr := appHdrXML("AURTSESSXXX-20260731-000001", "test.001.001.01")

	tests := []struct {
		name string
		in   string
	}{
		{
			name: "before the document",
			in:   `<Envelope>` + hdr + `<Sgntr><X>s</X></Sgntr>` + testDocumentXML + `</Envelope>`,
		},
		{
			name: "repeated on both sides of the document",
			in: `<Envelope>` + hdr + `<Sgntr><X>1</X></Sgntr>` + testDocumentXML +
				`<Sgntr><X>2</X></Sgntr></Envelope>`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := Unmarshal([]byte(tt.in))
			if err != nil {
				t.Fatalf("Unmarshal() error = %v, want an undispatched element to be skipped", err)
			}
			doc, ok := env.Document.(*testDoc)
			if !ok {
				t.Fatalf("Document is %T, want *testDoc", env.Document)
			}
			if doc.Body != "hello" {
				t.Fatalf("Body = %q, want hello", doc.Body)
			}
		})
	}
}

// TestUnmarshalRejectsTextOutsideTheEnvelope closes the last asymmetry in the
// trailing-content rules: text after </Envelope> was refused while text before
// <Envelope> was accepted, so the same junk was fatal in one position and
// invisible in the other.
func TestUnmarshalRejectsTextOutsideTheEnvelope(t *testing.T) {
	body := envelopeAround(testDocumentXML)

	for _, tc := range []struct {
		name string
		in   string
	}{
		{"before the root", "junk" + body},
		{"after the root", body + "junk"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Unmarshal([]byte(tc.in)); err == nil {
				t.Fatal("Unmarshal() = nil, want an error")
			}
		})
	}

	// Whitespace outside the root is not content and stays legal in both
	// positions — the guard trims before it judges, so widening it must not
	// have made a pretty-printed document illegal.
	if _, err := Unmarshal([]byte("\n  " + body + "\n")); err != nil {
		t.Fatalf("Unmarshal() error = %v; whitespace around the root is not content", err)
	}
}

// TestUnmarshalAcceptsALeadingByteOrderMark pins the one position in a document
// where U+FEFF is not character data.
func TestUnmarshalAcceptsALeadingByteOrderMark(t *testing.T) {
	bom := string(utf8BOM)

	t.Run("before the XML declaration", func(t *testing.T) {
		env, err := Unmarshal([]byte(bom + wantEnvelopeXML))
		if err != nil {
			t.Fatalf("Unmarshal() error = %v, want a leading BOM to be accepted", err)
		}
		if got := env.AppHdr.BizMsgIdr; got != "AURTSESSXXX-20260731-000001" {
			t.Fatalf("BizMsgIdr = %q, want the same value the un-marked document yields", got)
		}
	})

	// The golden files rather than only the synthetic envelope: these are the
	// bytes a counterparty would send, and a BOM'd copy of one is the shape of
	// the message this guard was refusing.
	for _, file := range []string{"pacs008.xml", "pacs003.xml", "pacs002.xml", "pacs004.xml"} {
		t.Run(file, func(t *testing.T) {
			golden, err := os.ReadFile(filepath.Join("testdata", file))
			if err != nil {
				t.Fatalf("reading %s: %v", file, err)
			}
			plain, err := Unmarshal(golden)
			if err != nil {
				t.Fatalf("Unmarshal(%s) error = %v", file, err)
			}
			marked, err := Unmarshal(append([]byte(bom), golden...))
			if err != nil {
				t.Fatalf("Unmarshal(BOM + %s) error = %v, want the BOM to be accepted", file, err)
			}
			if marked.AppHdr != plain.AppHdr {
				t.Fatalf("BOM changed the header: %+v, want %+v", marked.AppHdr, plain.AppHdr)
			}
			if fmt.Sprintf("%T", marked.Document) != fmt.Sprintf("%T", plain.Document) {
				t.Fatalf("BOM changed the document type: %T, want %T", marked.Document, plain.Document)
			}
		})
	}

	for _, tc := range []struct {
		name string
		in   string
	}{
		{"after the root", wantEnvelopeXML + bom},
		{"before the root but not first", "\n" + bom + wantEnvelopeXML},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Unmarshal([]byte(tc.in)); err == nil {
				t.Fatal("Unmarshal() = nil; U+FEFF anywhere but the first byte is character data")
			}
		})
	}
}

// TestUnmarshalTakesTheLastOfARepeatedScalar pins a known residual rather than
// a desired behaviour, so that the doc comment claiming it stays honest.
func TestUnmarshalTakesTheLastOfARepeatedScalar(t *testing.T) {
	in := `<Envelope><AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">` +
		`<Fr><FIId><FinInstnId><BICFI>AURTSESSXXX</BICFI></FinInstnId></FIId></Fr>` +
		`<To><FIId><FinInstnId><BICFI>CSMBFRPPXXX</BICFI></FinInstnId></FIId></To>` +
		`<BizMsgIdr>FIRST</BizMsgIdr><BizMsgIdr>SECOND</BizMsgIdr>` +
		`<MsgDefIdr>test.001.001.01</MsgDefIdr>` +
		`<CreDt>2026-07-31T09:30:00Z</CreDt></AppHdr>` + testDocumentXML + `</Envelope>`

	env, err := Unmarshal([]byte(in))
	if err != nil {
		t.Fatalf("Unmarshal() error = %v; a repeated scalar inside AppHdr is accepted, not refused", err)
	}
	if got := env.AppHdr.BizMsgIdr; got != "SECOND" {
		t.Fatalf("BizMsgIdr = %q, want %q — encoding/xml is last-wins here and the "+
			"codec doc comment says so", got, "SECOND")
	}
}

// TestUnmarshalRejectsAHeaderItCouldNotReMarshal closes the decode/encode
// asymmetry FuzzUnmarshal found: an AppHdr carrying nothing but a MsgDefIdr can
// satisfy Unmarshal and then fail Marshal on the very next call, so the package
// would accept bytes it could not reproduce.
func TestUnmarshalRejectsAHeaderItCouldNotReMarshal(t *testing.T) {
	in := `<Envelope><AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">` +
		`<MsgDefIdr>test.001.001.01</MsgDefIdr></AppHdr>` + testDocumentXML + `</Envelope>`

	if _, err := Unmarshal([]byte(in)); !errors.Is(err, ErrBICFormat) {
		t.Fatalf("Unmarshal() = %v, want it to wrap ErrBICFormat", err)
	}

	// The same header with agents but no business message identifier: a
	// different member of AppHdr.validate()'s five checks, so that this test
	// pins the delegation rather than one branch of it.
	noBizMsgIdr := `<Envelope><AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">` +
		`<Fr><FIId><FinInstnId><BICFI>AURTSESSXXX</BICFI></FinInstnId></FIId></Fr>` +
		`<To><FIId><FinInstnId><BICFI>CSMBFRPPXXX</BICFI></FinInstnId></FIId></To>` +
		`<MsgDefIdr>test.001.001.01</MsgDefIdr>` +
		`<CreDt>2026-07-31T09:30:00Z</CreDt></AppHdr>` + testDocumentXML + `</Envelope>`

	if _, err := Unmarshal([]byte(noBizMsgIdr)); !errors.Is(err, ErrMissingElement) {
		t.Fatalf("Unmarshal() = %v, want it to wrap ErrMissingElement", err)
	}
}

// TestUnmarshalRejectsAHeaderWithNoCreationDate is the CreDt case of the same
// asymmetry, and it is worse than the others because it does not fail on the
// way back out — it INVENTS.
func TestUnmarshalRejectsAHeaderWithNoCreationDate(t *testing.T) {
	noCreDt := `<Envelope><AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">` +
		`<Fr><FIId><FinInstnId><BICFI>AURTSESSXXX</BICFI></FinInstnId></FIId></Fr>` +
		`<To><FIId><FinInstnId><BICFI>CSMBFRPPXXX</BICFI></FinInstnId></FIId></To>` +
		`<BizMsgIdr>AURTSESSXXX-20260731-000001</BizMsgIdr>` +
		`<MsgDefIdr>test.001.001.01</MsgDefIdr></AppHdr>` + testDocumentXML + `</Envelope>`

	env, err := Unmarshal([]byte(noCreDt))
	if !errors.Is(err, ErrMissingElement) {
		t.Fatalf("Unmarshal() = %v (CreDt = %v), want it to wrap ErrMissingElement",
			err, env.AppHdr.CreDt)
	}

	// The other end of the same gap: Marshal must not manufacture a creation
	// timestamp for a header this repository built without one either.
	fabricated := testEnvelope()
	fabricated.AppHdr.CreDt = ISODateTime{}
	out, err := Marshal(fabricated)
	if !errors.Is(err, ErrMissingElement) {
		t.Fatalf("Marshal() = %v, want it to wrap ErrMissingElement; it emitted\n%s", err, out)
	}
}

// TestUnmarshalledEnvelopesAlwaysReMarshal is FuzzUnmarshal's property stated
// as an ordinary test, under a name that says what the property IS.
func TestUnmarshalledEnvelopesAlwaysReMarshal(t *testing.T) {
	for _, file := range []string{"pacs008.xml", "pacs003.xml", "pacs002.xml", "pacs004.xml"} {
		t.Run(file, func(t *testing.T) {
			golden, err := os.ReadFile(filepath.Join("testdata", file))
			if err != nil {
				t.Fatalf("reading %s: %v", file, err)
			}
			env, err := Unmarshal(golden)
			if err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if _, err := Marshal(env); err != nil {
				t.Fatalf("a document that unmarshalled failed to marshal: %v", err)
			}
		})
	}
}

// TestUnmarshalSurvivesHostileInput is the safety net for the one function here
// that reads bytes it did not write. Every case must return an error and none
// may panic.
func TestUnmarshalSurvivesHostileInput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// want is the sentinel the caller may match on, or nil when the input
		// is malformed XML rather than a recognisable envelope missing
		// something — those cases still have to be a non-EOF error.
		want error
	}{
		{name: "empty", in: ``},
		{name: "one angle bracket", in: `<`},
		{name: "unclosed root", in: `<Envelope>`},
		{name: "empty envelope", in: `<Envelope></Envelope>`, want: ErrMissingElement},
		{name: "AppHdr in no namespace", in: `<Envelope><AppHdr/></Envelope>`},
		{
			name: "header with only a MsgDefIdr",
			in: `<Envelope><AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">` +
				`<MsgDefIdr>pacs.008.001.08</MsgDefIdr></AppHdr></Envelope>`,
			want: ErrBICFormat,
		},
		{
			name: "document with no header",
			in:   `<Envelope><Document xmlns="urn:iso:std:iso:20022:tech:xsd:pacs.008.001.08"/></Envelope>`,
			want: ErrMissingElement,
		},
		{
			name: "valid header, empty document",
			in: `<Envelope><AppHdr xmlns="urn:iso:std:iso:20022:tech:xsd:head.001.001.02">` +
				`<Fr><FIId><FinInstnId><BICFI>AURTSESSXXX</BICFI></FinInstnId></FIId></Fr>` +
				`<To><FIId><FinInstnId><BICFI>CSMBFRPPXXX</BICFI></FinInstnId></FIId></To>` +
				`<BizMsgIdr>x</BizMsgIdr><MsgDefIdr>pacs.008.001.08</MsgDefIdr>` +
				`<CreDt>2026-07-31T09:30:00Z</CreDt></AppHdr>` +
				`<Document xmlns="urn:iso:std:iso:20022:tech:xsd:pacs.008.001.08">` +
				`<FIToFICstmrCdtTrf></FIToFICstmrCdtTrf></Document></Envelope>`,
			want: ErrMissingElement,
		},
		// The root-name check fires on the first token, so this one never reaches any
		// nesting logic and is a duplicate of the `<a>` case as far as the code is
		// concerned.
		{name: "deep nesting at the root", in: strings.Repeat(`<a>`, 5000)},
		{name: "deep nesting inside the envelope", in: `<Envelope>` + strings.Repeat(`<a>`, 5000)},
		{name: "control characters", in: "\x00\x01\x02"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panicked: %v", r)
				}
			}()
			_, err := Unmarshal([]byte(tc.in))
			if err == nil {
				t.Fatal("Unmarshal() = nil, want an error")
			}
			if errors.Is(err, io.EOF) {
				t.Fatalf("Unmarshal() = %v, which is io.EOF; a transport reads that as a clean close", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("Unmarshal() = %v, want it to wrap %v", err, tc.want)
			}
		})
	}
}
