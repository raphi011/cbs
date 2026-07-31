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
