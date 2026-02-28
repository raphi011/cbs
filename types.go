package ledger

import "time"

// Amount represents a monetary value in the smallest unit of the currency
// (e.g., cents for USD, pence for GBP). This is the standard approach
// used by most payment systems and banks.
type Amount = int64

// AccountType classifies accounts in the chart of accounts.
type AccountType int

const (
	Asset     AccountType = iota // Debits increase, credits decrease
	Liability                    // Credits increase, debits decrease
	Equity                       // Credits increase, debits decrease
	Revenue                      // Credits increase, debits decrease
	Expense                      // Debits increase, credits decrease
)

func (t AccountType) String() string {
	switch t {
	case Asset:
		return "Asset"
	case Liability:
		return "Liability"
	case Equity:
		return "Equity"
	case Revenue:
		return "Revenue"
	case Expense:
		return "Expense"
	default:
		return "Unknown"
	}
}

// NormalBalance returns the direction that increases this account type.
func (t AccountType) NormalBalance() Direction {
	switch t {
	case Asset, Expense:
		return Debit
	default:
		return Credit
	}
}

// Direction indicates whether an entry is a debit or credit.
type Direction int

const (
	Debit  Direction = iota
	Credit Direction = iota
)

func (d Direction) String() string {
	if d == Debit {
		return "Debit"
	}
	return "Credit"
}

// Opposite returns the opposite direction.
func (d Direction) Opposite() Direction {
	if d == Debit {
		return Credit
	}
	return Debit
}

// Ledger is a top-level grouping for accounts (e.g., "General Ledger").
type Ledger struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// Subledger is a subdivision of a ledger (e.g., "Accounts Receivable").
type Subledger struct {
	ID        string
	LedgerID  string
	Name      string
	CreatedAt time.Time
}

// Account is a financial account within a subledger.
type Account struct {
	ID          string
	SubledgerID string
	Name        string
	Type        AccountType
	CreatedAt   time.Time
}

// Entry is a single leg of a transaction, representing a debit or credit
// to an account.
type Entry struct {
	ID        string
	AccountID string
	Amount    Amount
	Direction Direction
}

// TransactionStatus tracks the lifecycle of a transaction.
type TransactionStatus int

const (
	Posted   TransactionStatus = iota
	Reversed TransactionStatus = iota
)

func (s TransactionStatus) String() string {
	if s == Posted {
		return "Posted"
	}
	return "Reversed"
}

// Transaction is a multi-legged accounting entry. All entries within a
// transaction must balance (total debits = total credits).
type Transaction struct {
	ID             string
	IdempotencyKey string
	Entries        []Entry
	BookingDate    time.Time // When the transaction was recorded in the system
	ValueDate      time.Time // When the transaction takes economic effect
	Status         TransactionStatus
	Description    string
	Metadata       map[string]string
	CreatedAt      time.Time

	// ReversalOf is set when this transaction is a reversal of another.
	ReversalOf string
}

// HoldStatus tracks the lifecycle of a hold.
type HoldStatus int

const (
	HoldActive   HoldStatus = iota
	HoldReleased HoldStatus = iota
	HoldCaptured HoldStatus = iota
)

func (s HoldStatus) String() string {
	switch s {
	case HoldActive:
		return "Active"
	case HoldReleased:
		return "Released"
	case HoldCaptured:
		return "Captured"
	default:
		return "Unknown"
	}
}

// Hold represents a pending authorization that reduces the available
// balance of an account without affecting the book balance.
type Hold struct {
	ID          string
	AccountID   string
	Amount      Amount
	ExpiresAt   time.Time
	Description string
	Status      HoldStatus
	CreatedAt   time.Time
}

// Balance represents the balances of an account.
type Balance struct {
	Book      Amount // Sum of all posted entries (considering account normal)
	Holds     Amount // Sum of active holds
	Available Amount // Book minus holds
}

// BalanceSnapshot is a point-in-time record of an account's balance,
// taken at end-of-day for a given value date.
type BalanceSnapshot struct {
	AccountID string
	Date      time.Time // The business day this snapshot represents
	Balance   Balance
	TakenAt   time.Time // When the snapshot was actually taken
}

// AuditEventType categorizes audit log entries.
type AuditEventType string

const (
	EventAccountCreated      AuditEventType = "account.created"
	EventTransactionPosted   AuditEventType = "transaction.posted"
	EventTransactionReversed AuditEventType = "transaction.reversed"
	EventHoldCreated         AuditEventType = "hold.created"
	EventHoldReleased        AuditEventType = "hold.released"
	EventHoldCaptured        AuditEventType = "hold.captured"
	EventSnapshotTaken       AuditEventType = "snapshot.taken"
	EventLedgerCreated       AuditEventType = "ledger.created"
	EventSubledgerCreated    AuditEventType = "subledger.created"
)

// AuditEvent is an immutable record of a mutation in the system.
type AuditEvent struct {
	ID        string
	Timestamp time.Time
	Type      AuditEventType
	EntityID  string         // ID of the affected entity
	Payload   any            // Event-specific data
	Metadata  map[string]string
}
