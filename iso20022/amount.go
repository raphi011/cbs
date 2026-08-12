package iso20022

import (
	"fmt"
	"strconv"
	"strings"
)

// ActiveCurrencyAndAmount is the standard's money type: a decimal number with
// the currency as an XML ATTRIBUTE rather than a sibling element.
type ActiveCurrencyAndAmount struct {
	Ccy   string `xml:"Ccy,attr"`
	Value string `xml:",chardata"`
}

// NewAmount renders an integer number of minor units as a decimal string with
// scale fraction digits.
func NewAmount(minor int64, scale uint8, ccy string) (ActiveCurrencyAndAmount, error) {
	if minor < 0 {
		return ActiveCurrencyAndAmount{}, fmt.Errorf("%w: negative amount %d", ErrAmountFormat, minor)
	}
	digits := strconv.FormatInt(minor, 10)
	if scale == 0 {
		return ActiveCurrencyAndAmount{Ccy: ccy, Value: digits}, nil
	}
	for len(digits) <= int(scale) {
		digits = "0" + digits
	}
	split := len(digits) - int(scale)
	return ActiveCurrencyAndAmount{Ccy: ccy, Value: digits[:split] + "." + digits[split:]}, nil
}

// Minor converts the decimal back to an integer number of minor units.
func (a ActiveCurrencyAndAmount) Minor(scale uint8) (int64, error) {
	whole, frac, hasPoint := strings.Cut(a.Value, ".")
	if strings.Contains(frac, ".") {
		return 0, fmt.Errorf("%w: %q", ErrAmountFormat, a.Value)
	}
	if !isDigits(whole) || (hasPoint && !isDigits(frac)) {
		return 0, fmt.Errorf("%w: %q", ErrAmountFormat, a.Value)
	}
	if len(frac) > int(scale) {
		return 0, fmt.Errorf("%w: %q has %d decimal places, scale is %d",
			ErrAmountScale, a.Value, len(frac), scale)
	}
	for len(frac) < int(scale) {
		frac += "0"
	}
	minor, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q: %v", ErrAmountFormat, a.Value, err)
	}
	return minor, nil
}

// Validate reports whether the amount is well-formed enough to put on the wire:
// a currency code is present, and Value is shaped like a non-negative decimal.
func (a ActiveCurrencyAndAmount) Validate() error {
	if a.Ccy == "" {
		return fmt.Errorf("%w: Ccy", ErrMissingElement)
	}
	if _, err := a.Minor(5); err != nil {
		return err
	}
	return nil
}

// isDigits reports whether s is a non-empty run of ASCII digits. strconv would
// accept a leading sign and underscores; the schema's decimal does not.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
