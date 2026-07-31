package iso20022

import (
	"encoding/json"
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

type dateHolder struct {
	XMLName xml.Name    `xml:"Holder"`
	Date    ISODate     `xml:"IntrBkSttlmDt"`
	Stamp   ISODateTime `xml:"CreDtTm"`
}

func TestDatesMarshal(t *testing.T) {
	h := dateHolder{
		Date:  ISODate{time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
		Stamp: ISODateTime{time.Date(2026, 7, 31, 9, 30, 0, 0, time.UTC)},
	}
	out, err := xml.Marshal(h)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `<Holder><IntrBkSttlmDt>2026-08-01</IntrBkSttlmDt><CreDtTm>2026-07-31T09:30:00Z</CreDtTm></Holder>`
	if string(out) != want {
		t.Fatalf("Marshal() =\n%s\nwant\n%s", out, want)
	}
}

func TestDatesUnmarshal(t *testing.T) {
	in := `<Holder><IntrBkSttlmDt>2026-08-01</IntrBkSttlmDt><CreDtTm>2026-07-31T09:30:00Z</CreDtTm></Holder>`
	var h dateHolder
	if err := xml.Unmarshal([]byte(in), &h); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := h.Date.Format("2006-01-02"); got != "2026-08-01" {
		t.Fatalf("Date = %s, want 2026-08-01", got)
	}
	if !h.Stamp.Equal(time.Date(2026, 7, 31, 9, 30, 0, 0, time.UTC)) {
		t.Fatalf("Stamp = %s, want 2026-07-31T09:30:00Z", h.Stamp)
	}
}

// TestDateTimeUsesSecondPrecision pins that a Go timestamp carrying nanoseconds
// does not leak them onto the wire. The standard permits fractional seconds;
// emitting a nanosecond-precision stamp would make every golden file depend on
// the clock that produced it.
func TestDateTimeUsesSecondPrecision(t *testing.T) {
	h := dateHolder{
		Date:  ISODate{time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
		Stamp: ISODateTime{time.Date(2026, 7, 31, 9, 30, 0, 123456789, time.UTC)},
	}
	out, err := xml.Marshal(h)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if want := `<CreDtTm>2026-07-31T09:30:00Z</CreDtTm>`; !strings.Contains(string(out), want) {
		t.Fatalf("Marshal() =\n%s\nwant it to contain %s", out, want)
	}
}

func TestDatesRejectMalformed(t *testing.T) {
	for _, in := range []string{
		`<Holder><IntrBkSttlmDt>01-08-2026</IntrBkSttlmDt><CreDtTm>2026-07-31T09:30:00Z</CreDtTm></Holder>`,
		`<Holder><IntrBkSttlmDt>2026-08-01</IntrBkSttlmDt><CreDtTm>not a time</CreDtTm></Holder>`,
	} {
		var h dateHolder
		if err := xml.Unmarshal([]byte(in), &h); err == nil {
			t.Fatalf("Unmarshal(%q) = nil, want an error", in)
		}
	}
}

func TestDatesMarshalJSON(t *testing.T) {
	h := dateHolder{
		Date:  ISODate{time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
		Stamp: ISODateTime{time.Date(2026, 7, 31, 9, 30, 0, 0, time.UTC)},
	}
	out, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if want := "2026-08-01"; !strings.Contains(string(out), want) {
		t.Fatalf("Marshal() =\n%s\nwant it to contain %s", out, want)
	}
	if want := "2026-07-31T09:30:00Z"; !strings.Contains(string(out), want) {
		t.Fatalf("Marshal() =\n%s\nwant it to contain %s", out, want)
	}

	// Verify nanoseconds do not leak onto the wire.
	h2 := dateHolder{
		Date:  ISODate{time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},
		Stamp: ISODateTime{time.Date(2026, 7, 31, 9, 30, 0, 123456789, time.UTC)},
	}
	out2, err := json.Marshal(h2)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if want := "2026-07-31T09:30:00Z"; !strings.Contains(string(out2), want) {
		t.Fatalf("Marshal() =\n%s\nwant it to contain %s", out2, want)
	}
}

func TestDatesUnmarshalJSON(t *testing.T) {
	in := `{"Date":"2026-08-01","Stamp":"2026-07-31T09:30:00Z"}`
	var h dateHolder
	if err := json.Unmarshal([]byte(in), &h); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := h.Date.Format("2006-01-02"); got != "2026-08-01" {
		t.Fatalf("Date = %s, want 2026-08-01", got)
	}
	if !h.Stamp.Equal(time.Date(2026, 7, 31, 9, 30, 0, 0, time.UTC)) {
		t.Fatalf("Stamp = %s, want 2026-07-31T09:30:00Z", h.Stamp)
	}
}

func TestDatesRejectMalformedJSON(t *testing.T) {
	for _, in := range []string{
		`{"Date":"01-08-2026","Stamp":"2026-07-31T09:30:00Z"}`,
		`{"Date":"2026-08-01","Stamp":"not a time"}`,
	} {
		var h dateHolder
		if err := json.Unmarshal([]byte(in), &h); err == nil {
			t.Fatalf("Unmarshal(%q) = nil, want an error", in)
		}
	}
}
