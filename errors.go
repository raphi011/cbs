package ledger

import "errors"

// Sentinel errors returned by the Service. Callers can use errors.Is()
// to check for specific failure conditions.
//
// These errors cover the main categories of failures:
//   - Not found: the referenced entity does not exist
//   - Validation: the request is malformed or violates business rules
//   - Conflict: the operation conflicts with existing state
//   - Invalid state: the entity is not in a valid state for the operation
var (
	// ErrLedgerNotFound is returned when a ledger ID does not match
	// any existing ledger in the system.
	ErrLedgerNotFound = errors.New("ledger not found")

	// ErrSubledgerNotFound is returned when a subledger ID does not
	// match any existing subledger in the system.
	ErrSubledgerNotFound = errors.New("subledger not found")

	// ErrAccountNotFound is returned when an account ID does not match
	// any existing account in the system.
	ErrAccountNotFound = errors.New("account not found")

	// ErrTransactionNotFound is returned when a transaction ID does not
	// match any existing transaction in the system.
	ErrTransactionNotFound = errors.New("transaction not found")

	// ErrHoldNotFound is returned when a hold ID does not match any
	// existing hold in the system.
	ErrHoldNotFound = errors.New("hold not found")

	// ErrUnbalancedTransaction is returned when the total debits do not
	// equal the total credits in a transaction. In double-entry
	// bookkeeping, every transaction must balance: the sum of debit
	// amounts must equal the sum of credit amounts.
	ErrUnbalancedTransaction = errors.New("transaction entries do not balance: total debits must equal total credits")

	// ErrEmptyTransaction is returned when a transaction is submitted
	// with no entries. A valid transaction requires at least two entries
	// (one debit and one credit).
	ErrEmptyTransaction = errors.New("transaction must have at least one entry")

	// ErrDuplicateIdempotencyKey is returned when a transaction is
	// submitted with an idempotency key that has already been used.
	// The original transaction ID is typically available in the error
	// context. This mechanism prevents accidental double-posting of
	// the same logical operation.
	ErrDuplicateIdempotencyKey = errors.New("idempotency key already used")

	// ErrTransactionAlreadyReversed is returned when attempting to
	// reverse a transaction that has already been reversed. A transaction
	// can only be reversed once.
	ErrTransactionAlreadyReversed = errors.New("transaction already reversed")

	// ErrHoldNotActive is returned when attempting to release or capture
	// a hold that is no longer in the Active state. Holds can only be
	// released or captured while they are active.
	ErrHoldNotActive = errors.New("hold is not active")

	// ErrInvalidAmount is returned when an entry amount is zero or
	// negative. All entry amounts must be positive; the direction
	// (debit/credit) determines the sign of the balance impact.
	ErrInvalidAmount = errors.New("amount must be positive")

	// ErrInsufficientBalance is returned when a hold or transaction
	// would cause the available balance to go below zero for account
	// types where that is not permitted. Note: this is only enforced
	// for Asset and Expense accounts.
	ErrInsufficientBalance = errors.New("insufficient available balance")
)
