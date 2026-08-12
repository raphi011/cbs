package deposit

import (
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// ID types for each entity in the deposit domain. Like the ledger package,
// these are defined types (not aliases) so the compiler prevents mixing up,
// say, a HoldID and an AccountID.
type (
	AccountID string
	HoldID    string
)

// AccountStatus tracks the lifecycle state of a deposit account.
type AccountStatus int

const (
	// Active is the normal operating state: the account is open and fully
	// functional.
	Active AccountStatus = iota
	// Dormant indicates no customer-initiated activity for an extended
	// period. A credit (or explicit reactivation) returns it to Active.
	Dormant
	// Frozen indicates the account is temporarily restricted (e.g. a court order
	// or fraud investigation).
	Frozen
	// Closed is the terminal state: the account is permanently shut down and
	// accepts no further activity, in either direction.
	Closed
)

func (s AccountStatus) String() string {
	switch s {
	case Active:
		return "Active"
	case Dormant:
		return "Dormant"
	case Frozen:
		return "Frozen"
	case Closed:
		return "Closed"
	default:
		return "Unknown"
	}
}

// Account is a customer demand-deposit account.
type Account struct {
	ID     AccountID
	Name   string
	Asset  ledger.AssetCode
	Status AccountStatus

	// Identifiers are this account's external addresses — what a counterparty
	// quotes to pay it. Zero is normal: an account nobody pays from outside the
	// bank needs no address at all.
	Identifiers []Identifier

	// Accrued is interest earned and not yet charged, at sub-minor-unit precision.
	Accrued interest.Accrued
	// AccruedGross is the interest this account has produced over its WHOLE LIFE,
	// recomputed from its value-dated balance on every run.
	AccruedGross interest.Accrued
	// LastAccrualDate is the business date accrual has been recomputed through.
	LastAccrualDate time.Time

	CreatedAt time.Time
}

// HoldStatus tracks the lifecycle of a hold.
type HoldStatus int

const (
	HoldActive HoldStatus = iota
	HoldReleased
	HoldCaptured
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

// Hold represents a pending authorization that reduces an account's available
// balance without affecting its book balance.
type Hold struct {
	ID          HoldID
	AccountID   AccountID
	Amount      ledger.Amount
	ExpiresAt   time.Time
	Description string
	Status      HoldStatus
	CreatedAt   time.Time
}

// Balance represents the balances of a deposit account.
type Balance struct {
	Book  ledger.Amount // book balance of this account's position
	Holds ledger.Amount // sum of active, non-expired holds
	// Available is Book - Holds + the overdraft limit in force TODAY, resolved
	// from the account's effective-dated terms timeline rather than read off the
	// account row: what a customer could spend last March is as much a fact about
	// that March as what they were charged for it.
	Available ledger.Amount
}

// Snapshot is a point-in-time record of a deposit account's balance, taken at
// end-of-day for a given business date.
type Snapshot struct {
	AccountID AccountID
	Date      time.Time // the business day this snapshot represents
	Balance   Balance
	TakenAt   time.Time // when the snapshot was actually taken
}

// Totals is a bank's customer-deposit position, split by the sign of each
// account's balance and keyed by asset.
type Totals struct {
	// Deposits is the sum of positive balances: what the bank owes customers.
	Deposits map[ledger.AssetCode]ledger.Amount
	// Overdrafts is the sum of the magnitudes of negative balances: what
	// customers owe the bank.
	Overdrafts map[ledger.AssetCode]ledger.Amount
}

// AccountWithTerms is an account alongside the overdraft terms in force on a
// day — today, for every caller here.
type AccountWithTerms struct {
	Account Account
	// Terms is RESOLVED rather than the account's raw row: a floating account's
	// price lives in the catalogue, so the row alone would answer "nil" to the
	// question every caller of this type is asking.
	Terms EffectiveTerms
}
