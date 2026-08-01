package iso20022

import (
	"fmt"
	"strconv"
	"strings"
)

// ActiveCurrencyAndAmount is the standard's money type: a decimal number with
// the currency as an XML ATTRIBUTE rather than a sibling element.
//
//	<IntrBkSttlmAmt Ccy="EUR">3000.00</IntrBkSttlmAmt>
//
// Value is held as a string, not a float. The standard permits eighteen total
// digits, which no float64 can carry exactly, and the whole point of this
// repository's integer minor units is that money is never approximated.
//
// The type permits up to five fraction digits for ANY currency. Only the
// currency's own exponent narrows that, and the schema does not know it — which
// is exactly the hazard Minor exists to close.
type ActiveCurrencyAndAmount struct {
	Ccy   string `xml:"Ccy,attr"`
	Value string `xml:",chardata"`
}

// NewAmount renders an integer number of minor units as a decimal string with
// scale fraction digits.
//
// scale is a parameter rather than a lookup because this package may not import
// ledger; callers pass ledger.AssetDef.Scale — 2 for EUR, 8 for BTC.
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
//
// It FAILS on a value carrying more fraction digits than scale allows, rather
// than rounding. A message that says 0.005 in a two-decimal currency is not a
// half-cent to be resolved one way or the other; it is a statement this system
// cannot represent, and quietly choosing an answer would introduce the one kind
// of error the integer ledger exists to prevent. A missing fraction, by
// contrast, is unambiguous and is padded: 1234.5 at scale 2 is 123450.
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

// Validate reports whether the amount is well-formed enough to put on the
// wire: a currency code is present, and Value is shaped like a non-negative
// decimal.
//
// It is exported, as BIC.Validate and IBAN.Validate are and for the same
// reason: this is a value type with a lexical space, and a caller building one
// wants to know whether it is legal BEFORE the message it is going into is
// assembled. payment's translator asks exactly that — an asset this repository
// can hold may have no representation here at all, because the ceiling below is
// on the ELEMENT and not on the currency, and a translator that discovered it
// inside Marshal would report an element rather than the payment.
//
// It checks FORMAT, not scale. Scale is a property of the asset an amount is
// denominated in — 2 for EUR, 8 for BTC — and this type does not know it and
// must not guess it; a value with more fraction digits than one particular
// currency allows is not malformed in the abstract, only in that currency, and
// that check belongs to a caller that knows the asset, via Minor. What IS
// universal, and so IS checked here, is the schema's own bound on this type:
// at most five fraction digits, for any currency — see the type doc comment.
// This reuses Minor(5) rather than a second parser: Minor already rejects
// exactly the malformed shapes (non-digits, a second '.', and so on) that
// would make a value unrepresentable, and five is that ceiling, not a guess.
//
// # The reuse carries a bound this method does not otherwise state
//
// Minor(5) zero-pads the fraction to five places and parses the result as an
// int64, so a value whose padded digits exceed MaxInt64 is refused as
// ErrAmountFormat — "value out of range" — even though it is a perfectly legal
// decimal. For a two-decimal currency the padding multiplies by a thousand, so
// the effective ceiling is MaxInt64/1000: 92,233,720,368,547.75 passes and
// 92,233,720,368,547.76 does not, at SIXTEEN significant digits.
//
// The standard's own ceiling is different and larger: ActiveCurrencyAndAmount
// has totalDigits="18", so 999999999999999.99 is legal and this method refuses
// it. The gap is an artifact of the reuse, not a rule from anywhere, and it is
// stated here because a caller reading "checks FORMAT, not scale" would
// otherwise have no way to know that magnitude is checked at all — payment's
// amountOf depends on this method for exactly that refusal and documents the
// same bound from the other side.
//
// The bound is not uniformly conservative, and it is worth being exact about
// which way it errs, because it goes BOTH ways depending on the scale:
//
//   - Below five fraction digits the padding multiplies, so the ceiling is
//     tighter than the standard's and legal values are refused. Two decimals
//     divides the int64 range by a thousand, as above.
//   - At exactly five, the padding is a no-op and the check degenerates to
//     "fits in an int64". 92233720368547.75807 is nineteen significant digits,
//     one past totalDigits="18", and this method ACCEPTS it. See
//     TestValidateAdmitsNineteenDigitsAtScaleFive.
//   - Above five, the fraction-digit check refuses first, so the question does
//     not arise.
//
// So an invalid amount can escape, at one scale, and this method must not be
// read as a totalDigits check. Closing the gap properly means factoring Minor's
// lexing into a helper this method can call without the int64 conversion, then
// checking fractionDigits and totalDigits directly. That is a change to this
// package's validation semantics rather than a bug fix, so it is recorded rather
// than smuggled in beside a translator.
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
