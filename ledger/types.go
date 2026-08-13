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

// Amount represents a monetary value in the smallest unit of the asset (e.g.,
// cents for USD, satoshis for BTC). This is the standard approach used by most
// payment systems and banks.
type Amount int64

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

// AssetCode is the natural key of an asset: "EUR", "USD", "BTC".
type AssetCode string

// AssetClass groups assets by what kind of thing they are. It carries no
// behaviour today; it is what lets a chart of accounts and a UI tell a
// currency from a token without pattern-matching on the code.
type AssetClass int

const (
	Fiat AssetClass = iota
	Crypto
)

func (c AssetClass) String() string {
	switch c {
	case Fiat:
		return "Fiat"
	case Crypto:
		return "Crypto"
	default:
		return "Unknown"
	}
}

// MaxAssetScale is the largest number of decimal places an asset may have.
const MaxAssetScale = 9

// AssetDef is a unit of value that accounts are denominated in.
type AssetDef struct {
	Code  AssetCode
	Name  string
	Scale uint8 // decimal places; 2 for EUR, 8 for BTC
	Class AssetClass
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
	Asset       AssetCode
	CreatedAt   time.Time

	// Control says this account pools subsidiaries: every entry against it must
	// name one, and every entry against an account without it must not.
	Control bool
}

// Position is what a balance is asked for: an account, and at most one
// subsidiary within it.
type Position struct {
	Account    AccountID
	Subsidiary string
}

// Total is the position of a whole account: every subsidiary under a control
// account, or a plain account's own balance.
func (a AccountID) Total() Position { return Position{Account: a} }

// For is the position of one subsidiary within a control account. The string is
// an id in the layer that knows what the subsidiary is; this package does not
// resolve it and does not require that it resolve.
func (a AccountID) For(subsidiary string) Position {
	return Position{Account: a, Subsidiary: subsidiary}
}

func (p Position) String() string {
	if p.Subsidiary == "" {
		return string(p.Account)
	}
	return string(p.Account) + "/" + p.Subsidiary
}

// Entry is one leg of a transaction: an amount in one direction against one
// account.
type Entry struct {
	ID        EntryID
	AccountID AccountID
	Amount    Amount
	Direction Direction

	// Subsidiary is what this leg belongs to within a control account: a deposit
	// account, a facility. It is empty on an account that pools nothing.
	Subsidiary string

	// ValueDate is when this leg takes economic effect. Zero on input means the
	// transaction's; PostTransaction resolves it, so a stored entry always carries
	// a concrete date and no reader has to fall back to the parent.
	ValueDate time.Time
}

// EntryFilter narrows a scan to what the entries index can serve. Nothing else
// belongs here: a predicate the index cannot answer is a full scan whichever
// language evaluates it, so the fold does the rest.
type EntryFilter struct {
	// From and To bound the value date to [From, To); a zero bound is no bound. An
	// entry with no value date is in neither, which is what makes a dated window
	// and an undated total two different questions.
	From time.Time
	To   time.Time
}

// DayMovement is an account's net movement on one value date, signed by the
// account's normal direction.
type DayMovement struct {
	Day    time.Time // UTC midnight
	Amount Amount
}

// Series is an account's value-dated balance history over a window: the balance
// carried into it, and the net movement on each day inside it that had any.
type Series struct {
	Opening   Amount
	Movements []DayMovement // ascending by Day
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
