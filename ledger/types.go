package ledger

import "time"

// ID types for each entity. These are defined types (not aliases) so the
// compiler prevents accidentally passing e.g. a SubledgerID where an
// AccountID is expected.
type (
	LedgerID      string
	SubledgerID   string
	AccountID     string
	TransactionID string
	EntryID       string
)

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

// codeBlock returns the leading chart-of-accounts block for the type:
// 100 Asset, 200 Liability, 300 Equity, 400 Revenue, 500 Expense.
func (t AccountType) codeBlock() int {
	switch t {
	case Asset:
		return 100
	case Liability:
		return 200
	case Equity:
		return 300
	case Revenue:
		return 400
	case Expense:
		return 500
	default:
		return 0
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
	ID        LedgerID
	Name      string
	CreatedAt time.Time
}

// Subledger is a subdivision of a ledger (e.g., "Accounts Receivable").
type Subledger struct {
	ID        SubledgerID
	LedgerID  LedgerID
	Name      string
	CreatedAt time.Time
}

// Account is a financial account within a subledger.
type Account struct {
	ID          AccountID
	SubledgerID SubledgerID
	Name        string
	Type        AccountType
	CreatedAt   time.Time
}

// Entry is a single leg of a transaction, representing a debit or credit
// to an account.
type Entry struct {
	ID        EntryID
	AccountID AccountID
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
	ID             TransactionID
	IdempotencyKey string
	Entries        []Entry
	BookingDate    time.Time // When the transaction was recorded in the system
	ValueDate      time.Time // When the transaction takes economic effect
	Status         TransactionStatus
	Description    string
	Metadata       map[string]string
	CreatedAt      time.Time

	// ReversalOf is set when this transaction is a reversal of another.
	ReversalOf TransactionID
}
