package iso20022

import "errors"

// Sentinel errors returned by this package. They follow the convention the
// ledger and payment packages use: callers match with errors.Is, and every
// returned error wraps one of these with the offending value.
var (
	// ErrUnknownMessageDefinition is returned when an AppHdr names a MsgDefIdr
	// this package does not implement. It is not a malformed message — it is a
	// well-formed message this system has no use for.
	ErrUnknownMessageDefinition = errors.New("iso20022: unknown message definition identifier")

	// ErrMessageDefinitionMismatch is returned when the AppHdr's MsgDefIdr
	// disagrees with the namespace of the enclosed Document. A receiver that
	// trusted the header here would parse a pacs.004 as a pacs.008.
	ErrMessageDefinitionMismatch = errors.New("iso20022: message definition identifier does not match the enclosed document")

	// ErrMissingElement is returned when an element the EPC guidelines make
	// mandatory is absent.
	ErrMissingElement = errors.New("iso20022: mandatory element missing")

	// ErrInvalidChoice is returned when neither member or both members of an
	// xsd:choice are set. encoding/xml cannot express a choice, so this is
	// checked rather than typed — see the package doc.
	ErrInvalidChoice = errors.New("iso20022: exactly one member of a choice must be set")

	// ErrBICFormat is returned by BIC.Validate for a value that is not a
	// structurally valid ISO 9362 business identifier code.
	ErrBICFormat = errors.New("iso20022: malformed BIC")

	// ErrIBANPattern is returned by IBAN.Validate for a value that does not
	// match the schema's IBAN2007Identifier pattern.
	//
	// The pattern, not the check digit: this package deliberately does not
	// verify ISO 7064 mod-97. See the IBAN type.
	ErrIBANPattern = errors.New("iso20022: IBAN does not match the ISO 20022 pattern")

	// ErrAmountScale is returned when a decimal amount carries more fraction
	// digits than the asset's scale permits. It is deliberately an error and
	// not a rounding: see ActiveCurrencyAndAmount.Minor.
	ErrAmountScale = errors.New("iso20022: amount has more decimal places than the asset's scale allows")

	// ErrAmountFormat is returned for an amount that is not a non-negative
	// decimal number.
	ErrAmountFormat = errors.New("iso20022: malformed amount")
)
