package iso20022

import (
	"encoding/xml"
	"time"
)

// isoDateLayout is the standard's ISODate: a calendar date with no time and no
// zone.
const isoDateLayout = "2006-01-02"

// isoDateTimeLayout is the standard's ISODateTime at second precision.
//
// The standard permits fractional seconds. This package does not emit them,
// because a nanosecond-precision timestamp would make every golden document
// depend on the clock that produced it, and no message in this system means
// anything at sub-second resolution.
const isoDateTimeLayout = "2006-01-02T15:04:05Z07:00"

// ISODate is a calendar date, such as an interbank settlement date.
//
// It wraps time.Time rather than aliasing it so that XML marshalling can use
// the standard's layout. encoding/xml renders a bare time.Time as RFC 3339,
// which is a timestamp and not a date.
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
