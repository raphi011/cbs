package ledger

import "errors"

// Sentinel errors returned by the Book. Callers can use errors.Is() to check
// for specific failure conditions.
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

	// ErrUnbalancedTransaction is returned when a transaction does not balance.
	ErrUnbalancedTransaction = errors.New("transaction entries do not balance: debits must equal credits within each asset")

	// ErrEmptyTransaction is returned when a transaction is submitted
	// with no entries. A valid transaction requires at least two entries
	// (one debit and one credit).
	ErrEmptyTransaction = errors.New("transaction must have at least one entry")

	// ErrDuplicateIdempotencyKey is returned when a transaction is submitted with
	// an idempotency key that has already been used. The original transaction ID
	// is typically available in the error context.
	ErrDuplicateIdempotencyKey = errors.New("idempotency key already used")

	// ErrTransactionAlreadyReversed is returned when attempting to
	// reverse a transaction that has already been reversed. A transaction
	// can only be reversed once.
	ErrTransactionAlreadyReversed = errors.New("transaction already reversed")

	// ErrInvalidAmount is returned when an entry amount is zero or
	// negative. All entry amounts must be positive; the direction
	// (debit/credit) determines the sign of the balance impact.
	ErrInvalidAmount = errors.New("amount must be positive")

	// ErrInsufficientBalance is returned when a transaction would cause the book
	// balance to go below zero for account types where that is not permitted.
	// Note: this is only enforced for Asset and Expense accounts.
	ErrInsufficientBalance = errors.New("insufficient available balance")

	// ErrInvalidText is returned when a caller-supplied string is not valid UTF-8
	// or contains a control character. See ValidateText for which fields this
	// covers and why the rule is a domain rule rather than a per-store one.
	ErrInvalidText = errors.New("text must be valid UTF-8 without control characters")

	// ErrAssetNotFound is returned when an asset code is not one the system knows.
	ErrAssetNotFound = errors.New("asset not found")

	// ErrSubsidiaryRequired is returned when an entry against a control account
	// names no subsidiary.
	ErrSubsidiaryRequired = errors.New("an entry against a control account must name a subsidiary")

	// ErrSubsidiaryNotAllowed is returned when an entry against a plain account
	// names a subsidiary.
	ErrSubsidiaryNotAllowed = errors.New("only a control account takes an entry with a subsidiary")

	// ErrSlotNotMapped is returned when no row says which account a flow posts to.
	ErrSlotNotMapped = errors.New("no account is mapped to this slot")

	// ErrSlotAccountMismatch is returned when the account a slot is being pointed
	// at is not the kind of account the slot requires: wrong asset, wrong type, or
	// plain where the flow posts subsidiaries.
	ErrSlotAccountMismatch = errors.New("account does not satisfy the slot")

	// ErrSlotNotProductScoped is returned when a product-specific row is written
	// for a slot that holds a balance.
	ErrSlotNotProductScoped = errors.New("this slot takes no product-specific mapping")

	// ErrUnbalancedAsset is returned when the debits and credits of one asset
	// within a transaction do not net to zero.
	ErrUnbalancedAsset = errors.New("transaction entries do not balance within an asset")
)
