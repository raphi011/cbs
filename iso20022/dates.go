package iso20022

import (
	"encoding/json"
	"encoding/xml"
	"time"
)

// isoDateLayout is the standard's ISODate: a calendar date with no time and no
// zone.
const isoDateLayout = "2006-01-02"

// isoDateTimeLayout is the standard's ISODateTime at second precision.
const isoDateTimeLayout = "2006-01-02T15:04:05Z07:00"

// ISODate is a calendar date, such as an interbank settlement date.
type ISODate struct{ time.Time }

// MarshalXML writes the date as YYYY-MM-DD.
func (d ISODate) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return e.EncodeElement(d.Format(isoDateLayout), start)
}

// UnmarshalXML parses YYYY-MM-DD, and fails on anything else.
func (d *ISODate) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	var s string
	if err := dec.DecodeElement(&s, &start); err != nil {
		return err
	}
	t, err := time.Parse(isoDateLayout, s)
	if err != nil {
		return err
	}
	d.Time = t
	return nil
}

// MarshalJSON writes the date as a JSON string in YYYY-MM-DD format.
func (d ISODate) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Format(isoDateLayout))
}

// UnmarshalJSON parses YYYY-MM-DD from a JSON string, and fails on anything
// else.
func (d *ISODate) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	t, err := time.Parse(isoDateLayout, s)
	if err != nil {
		return err
	}
	d.Time = t
	return nil
}

// ISODateTime is a timestamp, such as a message creation time.
type ISODateTime struct{ time.Time }

// MarshalXML writes the timestamp at second precision, with its zone.
func (d ISODateTime) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return e.EncodeElement(d.Truncate(time.Second).Format(isoDateTimeLayout), start)
}

// UnmarshalXML parses an ISO 20022 ISODateTime, accepting the fractional
// seconds the standard permits even though this package never writes them.
func (d *ISODateTime) UnmarshalXML(dec *xml.Decoder, start xml.StartElement) error {
	var s string
	if err := dec.DecodeElement(&s, &start); err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return err
	}
	d.Time = t
	return nil
}

// MarshalJSON writes the timestamp as a JSON string at second precision, with
// its zone.
func (d ISODateTime) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Truncate(time.Second).Format(isoDateTimeLayout))
}

// UnmarshalJSON parses an ISO 20022 ISODateTime from a JSON string, accepting
// the fractional seconds the standard permits even though this package never
// writes them.
func (d *ISODateTime) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return err
	}
	d.Time = t
	return nil
}
