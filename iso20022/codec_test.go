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

// envelopeAround wraps a valid header for test.001.001.01 and whatever body
// bytes the caller supplies in a single <Envelope>. The tests below differ
// only in what sits between the header and the root's closing tag, so the
// header is written once here rather than copied into each of them.
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
// follow the document, so refusing it would refuse legitimate input. The same
// element placed BEFORE <Document> was always skipped, so a rule that refused
// it here would also be positional and asymmetric for no stated reason.
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
// character data. Text between </Document> and </Envelope> is still inside the
// envelope, so the post-envelope text guard must not reach it.
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

// TestUnmarshalRejectsASecondDocumentInTheSameEnvelope pins the one element
// the envelope-level rule must NOT let through twice. Widening the guard so
// that <Sgntr> can follow <Document> would, on its own, also let a second
// <Document> follow the first and silently overwrite it — the same parser
// differential as two concatenated envelopes, one level down.
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
// envelope closes. Without it, mutating the code to refuse trailing
// xml.ProcInst leaves the whole suite green and the claim unchecked.
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
//
// AppHdr is the element that DRIVES dispatch, so two headers is the parser
// differential the <Document> guard exists to prevent, applied to the field
// that decides the document's type. Which copy won depended on where it sat:
// a second header BEFORE <Document> overwrote hdr before dispatch, so the
// SECOND won; a second header AFTER <Document> was decoded into hdr and then
// thrown away, because result had already snapshotted the first — so the
// FIRST won. Same bytes, two answers, chosen by position.
//
// The two copies here differ ONLY in BizMsgIdr, and both name the same,
// registered message definition. That is deliberate. An earlier draft made
// the second header name camt.053.001.08, and the before-position subtest
// passed against the unfixed code — not because the duplicate was refused,
// but because the overwritten header then failed the registry lookup. A
// duplicate that changes nothing the other checks look at is the only kind
// that isolates this guard.
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
// duplicate guards do NOT make. An element this codec does not dispatch on is
// skipped wherever it sits — before <Document> as well as after it, which is
// what makes the envelope-level rule symmetric rather than positional — and it
// may repeat, because only the two elements the codec actually reads into
// (AppHdr, Document) can be duplicated into a parser differential. Without
// this, "skipped either way" and "may repeat freely" are prose no test checks.
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
//
// XML 1.0 forbids character data in the prolog, so a conforming parser rejects
// the leading case outright — meaning this codec used to return a value for
// bytes another parser would refuse to read at all. encoding/xml is lenient
// there and will not raise it, so Unmarshal does.
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
//
// XML 1.0 §4.3.3 and Appendix F make a leading UTF-8 byte order mark part of
// the entity's ENCODING AUTODETECTION rather than its content, so a conforming
// parser accepts it — and producers on the .NET and Windows stacks emit one by
// default, which very much includes real bank and CSM middleware. encoding/xml
// surfaces it as the first xml.CharData token, carrying the three bytes
// EF BB BF, and bytes.TrimSpace does not trim them: U+FEFF is not
// unicode.IsSpace. So the text-outside-the-root guard, once widened to fire at
// depth 0 in EITHER direction, refused every BOM'd document — the exact
// inversion of the asymmetry it was widened to close, since here a conforming
// parser accepts what this one refused. Nothing in this repository noticed
// because Marshal never emits a BOM.
//
// Anywhere other than first, U+FEFF is ZERO WIDTH NO-BREAK SPACE: ordinary
// character data, and still refused outside the root. Both halves are pinned,
// because a fix that trimmed the mark unconditionally would reopen the very
// hole the widening closed.
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
//
// Inside a subtree handed to dec.DecodeElement, encoding/xml is last-wins for a
// repeated scalar child: a second <BizMsgIdr> silently replaces the first
// rather than being skipped or refused. The envelope's own children are
// guarded against exactly this (a second <AppHdr> or <Document> is an error);
// one level down, they are not. A parser that took the first value would
// disagree with this one about the same bytes.
//
// It is left as-is because every actor in this repository decodes through this
// function and so agrees with itself, and because the package doc says
// interoperability with a foreign parser is not what this envelope is for. If
// that ever stops being true, this test is where the decision is recorded, and
// it will fail the moment the behaviour changes.
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
// asymmetry FuzzUnmarshal found: an AppHdr carrying nothing but a MsgDefIdr
// used to satisfy Unmarshal and then fail Marshal on the very next call, so
// this package accepted bytes it could not reproduce.
//
// The document below is deliberately VALID — the point is that the header
// alone decides it, and that the error is the same one Marshal would have
// raised, only raised at the boundary where the bytes actually arrived.
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
//
// head.001.001.02 declares CreDt with minOccurs defaulted to 1, and xmllint
// refuses a header without it ("Missing child element(s). Expected is one of
// ( BizSvc, MktPrctc, CreDt )"). AppHdr.validate() used to check four things
// and not this one, so a header with no <CreDt> was accepted, decoded to the
// zero time, and re-marshalled as <CreDt>0001-01-01T00:00:00Z</CreDt>: a
// schema-VALID document carrying a business fact — the moment the header was
// created — that nobody sent. The round trip holds, which is exactly why
// FuzzUnmarshal cannot find it; silent fabrication is not a crash.
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
//
// It does not add coverage, and the comment here used to claim it did — "so
// that it holds in a normal `go test` run and not only under the fuzzer, which
// CI does not invoke". That is wrong twice over: a plain `go test` runs
// FuzzUnmarshal over its seed corpus, and those seeds are f.Add-ed from these
// same four golden files. assertGoldenRoundTrip has been asserting it since
// before this branch as well. What this test buys is that the property has a
// name a reader can grep for and a failure message that names it, rather than
// arriving as a fuzz seed failing. That is a smaller claim, and it is the true
// one.
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
//
// It asserts the error CLASS per case rather than merely non-nilness. An
// earlier version listed these same inputs and checked only err != nil, which
// locked in — without ever observing it — that the empty string and
// <Envelope></Envelope> came back as a bare io.EOF: neither
// ErrMissingElement: AppHdr nor ErrMissingElement: Document, and the one error
// a transport layer is idiomatically required to read as "the peer closed
// cleanly, read more" rather than "these bytes are bad". Sub-project 7b puts
// this function behind a channel or an HTTP handler, where a framing bug that
// truncates a body would then have surfaced as a clean disconnect.
//
// So io.EOF may never escape Unmarshal, for ANY input — that is asserted for
// every case, including the ones whose sentinel is deliberately left open
// because the input is not a structurally recognisable envelope at all.
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
		// The root-name check fires on the first token, so this one never
		// reaches any nesting logic and is a duplicate of the `<a>` case as far
		// as the code is concerned. It is kept because it is cheap, and the
		// case below is the one that actually walks 5 000 levels — inside an
		// <Envelope>, where dec.Skip has to do the walking.
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
