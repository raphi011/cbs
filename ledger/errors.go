package ledger

import "errors"

// Sentinel errors returned by the Book. Callers can use errors.Is()
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

	// ErrUnbalancedTransaction is returned when a transaction does not
	// balance. It is the general fact; ErrUnbalancedAsset names which asset
	// it failed in, and every per-asset failure wraps both, so a caller may
	// match on whichever level it cares about.
	//
	// It is not returned on its own. The empty case has its own sentinel
	// (ErrEmptyTransaction, guarded earlier in PostTransactionTx), and every
	// other imbalance is an imbalance within some asset.
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

	// ErrInvalidAmount is returned when an entry amount is zero or
	// negative. All entry amounts must be positive; the direction
	// (debit/credit) determines the sign of the balance impact.
	ErrInvalidAmount = errors.New("amount must be positive")

	// ErrInsufficientBalance is returned when a transaction would cause
	// the book balance to go below zero for account types where that is
	// not permitted. Note: this is only enforced for Asset and Expense
	// accounts.
	ErrInsufficientBalance = errors.New("insufficient available balance")

	// ErrInvalidText is returned when a caller-supplied string is not
	// valid UTF-8 or contains a control character. See ValidateText for
	// which fields this covers and why the rule is a domain rule rather
	// than a per-store one.
	ErrInvalidText = errors.New("text must be valid UTF-8 without control characters")

	// ErrAssetNotFound is returned when an asset code is not registered in
	// this book. Assets are per book: a bank that does not deal in BTC has
	// no BTC in its chart of accounts.
	ErrAssetNotFound = errors.New("asset not found")

	// ErrDuplicateAsset is returned when registering an asset code that the
	// book already has.
	ErrDuplicateAsset = errors.New("asset already exists")

	// ErrInvalidScale is returned when an asset's scale exceeds
	// MaxAssetScale. Amount is an int64 and cannot represent 18 decimal
	// places usefully.
	ErrInvalidScale = errors.New("asset scale exceeds the maximum supported decimal places")

	// ErrUnbalancedAsset is returned when the debits and credits of one
	// asset within a transaction do not net to zero.
	//
	// This is the double-entry invariant restated for a multi-asset ledger.
	// A global check is not enough: a transaction debiting 100 EUR and
	// crediting 100 BTC balances by the old rule and still creates value out
	// of nothing. Balance has to hold per asset or it means nothing.
	//
	// It is returned wrapped with the offending asset code, so errors.Is
	// works and the message names the asset.
	ErrUnbalancedAsset = errors.New("transaction entries do not balance within an asset")
)
