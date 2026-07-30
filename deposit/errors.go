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

	// ErrTermsNotFound is returned when no overdraft terms are in force on a
	// day. Every account gets an opening terms row at OpenAccount, so the only
	// way to miss is to ask about a day before the account existed.
	//
	// It is deliberately absent from api.errorStatus's 404 list and so reaches
	// the client as a 500. That is the choice, not an oversight: an account with
	// no opening row is internally inconsistent state rather than a missing
	// resource, and a 404 would read as "no such account" — worth saying because
	// ListAccountsWithTerms resolves terms for every account in the book, so one
	// account missing its opening row fails the whole listing rather than a row
	// of it.
	ErrTermsNotFound = errors.New("no overdraft terms in force on that day")

	// ErrInvalidAmount is returned when a hold amount is zero or negative.
	ErrInvalidAmount = errors.New("amount must be positive")

	// ErrInvalidRate is returned when an interest rate is negative, or when an
	// unarranged rate is set on an account that has no arranged rate — an
	// account that accrues nothing inside its limit but something beyond it is
	// not a product, it is a mistake.
	ErrInvalidRate = errors.New("invalid interest rate")

	// ErrInsufficientAvailable is returned when a hold or withdrawal would
	// take the available balance below zero (accounting for the overdraft
	// limit).
	ErrInsufficientAvailable = errors.New("insufficient available balance")

	// ErrAccountFrozen is returned when an operation is attempted on a frozen
	// account.
	ErrAccountFrozen = errors.New("account is frozen")

	// ErrAccountDormant is returned when money is taken OUT of a dormant
	// account — a withdrawal or a new hold. Credits are permitted and are what
	// brings such an account back to life, so this is never returned for one.
	//
	// It exists because a blocked debit on a dormant account used to fall
	// through requireActive's default branch and report
	// ErrInvalidStatusTransition — an error about changing a status, raised by
	// an operation that was not changing one. Dormancy is an ordinary state with
	// an ordinary rule, and the error a caller sees should say so.
	ErrAccountDormant = errors.New("account is dormant")

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
