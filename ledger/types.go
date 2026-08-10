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

// Amount represents a monetary value in the smallest unit of the asset
// (e.g., cents for USD, satoshis for BTC). This is the standard approach
// used by most payment systems and banks.
//
// It is a defined type rather than an alias, so a bare int64 cannot be
// passed where an Amount is expected. That matters more than it looks:
// if a 128-bit amount ever becomes necessary, the compiler points at
// every site that has to change instead of silently accepting the old
// type.
//
// The width is a real constraint, not an incidental one. int64 tops out
// near 9.22e18, so an asset with 18 decimal places (wei) would hold only
// 9.2 units. AssetDef.Scale is capped at 9 for exactly this reason.
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
//
// Amount is an int64, which tops out near 9.22e18. At 9 decimal places that
// is still 9.2 billion whole units — more than enough for any fiat currency
// and for BTC, whose entire 21M supply is 2.1e15 satoshis at 8 places. At 18
// places (wei) an int64 would hold 9.2 units, so 18-decimal assets are held
// at reduced precision rather than being silently truncated at runtime.
const MaxAssetScale = 9

// AssetDef is a unit of value that accounts are denominated in.
//
// Every GL account is bound to exactly one asset, fixed at creation. That is
// how real core banking systems work — an account number and its currency
// are inseparable, which is why IBANs are per-currency — and it is what keeps
// a balance a scalar rather than a map.
//
// Named AssetDef rather than Asset because Asset is already taken: it is the
// AccountType constant for the asset side of the balance sheet. That constant
// keeps the name — it is the accounting term, and it appears throughout the
// README, the quiz and every chart of accounts.
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
//
// Asset is fixed at creation and never changes: an account number and its
// asset are inseparable, the same way an IBAN is per-currency. That is what
// keeps a balance a scalar and what lets Post check the double-entry
// invariant per asset.
type Account struct {
	ID          AccountID
	SubledgerID SubledgerID
	Name        string
	Type        AccountType
	Asset       AssetCode
	CreatedAt   time.Time

	// Control says this account pools obligors: every entry against it must
	// name one, and every entry against an account without it must not. Both
	// refusals are made in PostTransactionTx, which is where what each would
	// otherwise cost is written down.
	//
	// It is fixed at creation, like Type and Asset, and for a stronger reason
	// than either: flipping it would leave the entries already posted on the
	// wrong side of both refusals, and nothing could tell afterwards which
	// pool they had belonged to.
	Control bool
}

// Position is what a balance is asked for: an account, and at most one obligor
// within it.
//
// The two travel together because every read that grows the dimension needs
// both, and a caller carrying them apart would eventually check one account's
// balance against another obligor's. An empty Subsidiary means the WHOLE
// account — on a control account that is the control figure, the same sum with
// the obligor dropped from the WHERE clause, which is what makes
// Σ(detail) == control one statement read two ways rather than a nightly proof.
//
// There is no ambiguity between "the whole pool" and "the obligor named by the
// empty string", because the two posting refusals in PostTransactionTx leave no
// account holding both kinds of entry.
type Position struct {
	Account    AccountID
	Subsidiary string
}

// Total is the position of a whole account: every subsidiary under a control
// account, or a plain account's own balance.
func (a AccountID) Total() Position { return Position{Account: a} }

// For is the position of one obligor within a control account. The string is
// the obligor's id in the layer that knows what an obligor is; this package
// does not resolve it and does not require that it resolve.
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

	// Subsidiary is the obligor this leg belongs to within a control account: a
	// deposit account, a facility. It is empty on an account that pools nothing.
	//
	// It is an opaque string supplied by the layer above, with no foreign key
	// and no table of subsidiaries behind it — exactly the stance AccountID
	// takes, and for the same reason: this package does not know what a
	// customer is.
	Subsidiary string

	// ValueDate is when this leg takes economic effect. Zero on input means
	// the transaction's; PostTransaction resolves it, so a stored entry always
	// carries a concrete date and no reader has to fall back to the parent.
	//
	// It lives here rather than only on the Transaction because the two legs of
	// one event can legitimately value-date differently — an outbound transfer
	// debits the customer on the day it is debited, while the bank's clearing
	// position settles days later. Unlike an entry's asset, which is always its
	// account's and so would only create disagreement if stored twice, an
	// entry's value date is genuinely not derivable from its transaction.
	ValueDate time.Time
}

// DayMovement is an account's net movement on one value date, signed by the
// account's normal direction.
type DayMovement struct {
	Day    time.Time // UTC midnight
	Amount Amount
}

// Series is an account's value-dated balance history over a window: the balance
// carried into it, and the net movement on each day inside it that had any.
//
// It exists so that interest can be computed on each day's own closing balance
// without one query per day. Days with no movement are omitted, because the
// balance did not change on them — a consumer folds Opening forward and applies
// each movement as it reaches its day.
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
