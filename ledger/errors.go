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
	ErrUnbalancedTransaction = errors.New("transaction entries do not balance: debits must equal credits within each asset")

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

	// ErrAssetNotFound is returned when an asset code is not one the system
	// knows. The known assets are a package-level list in code (see
	// LookupAsset), so this is a bad request rather than missing state:
	// nothing a caller can do at runtime will make "DOGE" resolve.
	ErrAssetNotFound = errors.New("asset not found")

	// ErrSubsidiaryRequired is returned when an entry against a control
	// account names no subsidiary. The amount would sit in the pool belonging
	// to nobody, and no later read could say whose it was — the control
	// figure would be right and every detail under it wrong.
	ErrSubsidiaryRequired = errors.New("an entry against a control account must name a subsidiary")

	// ErrSubsidiaryNotAllowed is returned when an entry against a plain
	// account names a subsidiary. Nothing aggregates a non-control account by
	// obligor, so the dimension would be written and never read, and the
	// caller's belief that it had recorded whose money this is would be false.
	ErrSubsidiaryNotAllowed = errors.New("only a control account takes an entry with a subsidiary")

	// ErrSlotNotMapped is returned when no row says which account a flow posts
	// to. It is a chart of accounts that has not been configured for this
	// asset, seen from a posting path — a refusal rather than a fallback,
	// because the only fallback available would be to guess a line and the
	// money would land somewhere nobody chose.
	ErrSlotNotMapped = errors.New("no account is mapped to this slot")

	// ErrSlotAccountMismatch is returned when the account a slot is being
	// pointed at is not the kind of account the slot requires: wrong asset,
	// wrong type, or plain where the flow posts obligors. Refused at the WRITE,
	// because the alternative is a posting that fails weeks later at a moment
	// nobody connects to the configuration change that caused it.
	ErrSlotAccountMismatch = errors.New("account does not satisfy the slot")

	// ErrSlotNotProductScoped is returned when a product-specific row is
	// written for a slot that holds a balance. See Slot.ByProduct: the money
	// already posted would stay in the old line while every later posting went
	// to the new one.
	ErrSlotNotProductScoped = errors.New("this slot takes no product-specific mapping")

	// ErrUnbalancedAsset is returned when the debits and credits of one
	// asset within a transaction do not net to zero.
	//
	// This is the double-entry invariant restated for a multi-asset ledger.
	// A global check is not enough: amounts are integers in their asset's
	// minor units, so a global sum is satisfied whenever the integers match.
	// 10_000_000_000 debited from a EUR account (€100M) against
	// 10_000_000_000 credited to a BTC one (100 BTC) balances by the old rule
	// and creates most of a hundred million euro out of nothing. Balance has
	// to hold per asset or it means nothing.
	//
	// It is returned wrapped with the offending asset code, so errors.Is
	// works and the message names the asset.
	ErrUnbalancedAsset = errors.New("transaction entries do not balance within an asset")
)
