package product

import "errors"

// Sentinel errors returned by the Catalogue and by the value types. Callers use
// errors.Is, the same convention every other layer here follows.
var (
	// ErrProductNotFound is returned when a product ID matches no product in
	// the book.
	ErrProductNotFound = errors.New("product not found")

	// ErrVersionNotFound is returned when no PUBLISHED version is in force on
	// a day. A draft is not a candidate, so a product whose only version is a
	// draft has none in force on any day.
	ErrVersionNotFound = errors.New("no published product version in force on that day")

	// ErrProductRetired is returned when a retired product is used to open or
	// migrate an account.
	ErrProductRetired = errors.New("product is retired")

	// ErrVersionPublished is returned when a published version is written to.
	ErrVersionPublished = errors.New("product version is published and cannot be changed")

	// ErrRetroactivePublish is returned when a version would be published
	// effective before today.
	ErrRetroactivePublish = errors.New("a product version cannot be published effective in the past")

	// ErrHashMismatch is returned when a version's stored hash does not match
	// its content — a row edited behind the system's back, since no code path
	// can produce one. Resolution fails rather than pricing a day from it.
	ErrHashMismatch = errors.New("product version hash does not match its content")

	// ErrKindMismatch is returned for an unknown Kind, and when a product of
	// one kind is used where another is required — opening a current account
	// from a term-loan product, say.
	ErrKindMismatch = errors.New("wrong product kind for this operation")

	// ErrInvalidRate is returned for a negative rate, and for an unarranged
	// rate with no arranged one. It mirrors deposit.ErrInvalidRate, because the
	// two are the same rule about the same three numbers.
	ErrInvalidRate = errors.New("invalid product pricing")

	// ErrNameRequired is returned for a product with no name.
	ErrNameRequired = errors.New("product name is required")
)
