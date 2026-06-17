package deposit

import "errors"

// Sentinel errors returned by the Register. Callers can use errors.Is() to
// check for specific failure conditions, the same convention the ledger
// package uses.
var (
	// ErrAccountNotFound is returned when a deposit account ID does not
	// match any account in the register.
	ErrAccountNotFound = errors.New("deposit account not found")

	// ErrHoldNotFound is returned when a hold ID does not match any hold in
	// the register.
	ErrHoldNotFound = errors.New("hold not found")

	// ErrHoldNotActive is returned when attempting to release or capture a
	// hold that is no longer in the Active state.
	ErrHoldNotActive = errors.New("hold is not active")

	// ErrSnapshotNotFound is returned when no end-of-day snapshot exists for
	// the given account and date.
	ErrSnapshotNotFound = errors.New("snapshot not found")

	// ErrInvalidAmount is returned when a hold amount is zero or negative.
	ErrInvalidAmount = errors.New("amount must be positive")

	// ErrInsufficientAvailable is returned when a hold or withdrawal would
	// take the available balance below zero (accounting for the overdraft
	// limit).
	ErrInsufficientAvailable = errors.New("insufficient available balance")

	// ErrAccountFrozen is returned when an operation is attempted on a frozen
	// account.
	ErrAccountFrozen = errors.New("account is frozen")

	// ErrAccountClosed is returned when an operation is attempted on a closed
	// account.
	ErrAccountClosed = errors.New("account is closed")

	// ErrAccountNotEmpty is returned when closing an account whose book
	// balance is not zero.
	ErrAccountNotEmpty = errors.New("account balance must be zero to close")

	// ErrInvalidStatusTransition is returned when an account status change is
	// not permitted from the account's current state.
	ErrInvalidStatusTransition = errors.New("invalid account status transition")
)
