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

	// ErrHoldNotActive is returned when releasing or capturing a hold that is not
	// Active.
	ErrHoldNotActive = errors.New("hold is not active")

	// ErrSnapshotNotFound is returned when no end-of-day snapshot exists for
	// the given account and date.
	ErrSnapshotNotFound = errors.New("snapshot not found")

	// ErrTermsNotFound is returned when no overdraft terms are in force on a day.
	// Every account gets an opening terms row at OpenAccount, so the only way to
	// miss is to ask about a day before the account existed.
	ErrTermsNotFound = errors.New("no overdraft terms in force on that day")

	// ErrProductRequired is returned for a terms row that names no product.
	ErrProductRequired = errors.New("overdraft terms must name a product")

	// ErrInvalidAmount is returned when a hold amount is zero or negative.
	ErrInvalidAmount = errors.New("amount must be positive")

	// ErrInvalidRate is returned when an interest rate is negative, or when an
	// unarranged rate is set on an account that has no arranged rate — an account
	// that accrues nothing inside its limit but something beyond it is not a
	// product, it is a mistake.
	ErrInvalidRate = errors.New("invalid interest rate")

	// ErrInsufficientAvailable is returned when a hold or withdrawal would
	// take the available balance below zero (accounting for the overdraft
	// limit).
	ErrInsufficientAvailable = errors.New("insufficient available balance")

	// ErrAccountFrozen is returned when an operation is attempted on a frozen
	// account.
	ErrAccountFrozen = errors.New("account is frozen")

	// ErrAccountDormant is returned when money is taken OUT of a dormant account —
	// a withdrawal or a new hold. Credits are permitted and are what brings such
	// an account back to life, so this is never returned for one.
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

	// ErrSameAccount is a transfer whose payer and payee are the one account.
	ErrSameAccount = errors.New("a transfer needs two different accounts")

	// ErrAssetMismatch is a transfer between accounts denominated in different
	// units.
	ErrAssetMismatch = errors.New("the two accounts are denominated in different assets")

	// ErrIdentifierTaken is returned when an account is given an identifier
	// another account at the SAME bank already holds.
	ErrIdentifierTaken = errors.New("identifier already in use at this bank")

	// ErrIdentifierNotFound is returned when no account holds the identifier.
	ErrIdentifierNotFound = errors.New("identifier not found")

	// ErrIdentifierAmbiguous is returned when more than one account holds it.
	ErrIdentifierAmbiguous = errors.New("identifier resolves to more than one account")

	// ErrIBANIsIssued is a caller supplying an IBAN — at OpenAccount, or at
	// AddIdentifier — rather than being given one.
	ErrIBANIsIssued = errors.New("an IBAN is issued by this bank, not supplied to it")

	// ErrNoIssuer is a Register with no bank code, asked to open an account.
	ErrNoIssuer = errors.New("this register has no bank code to issue addresses under")
)
