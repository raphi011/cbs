package deposit

import (
	"time"

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
	// Frozen indicates the account is temporarily restricted (e.g. a court
	// order or fraud investigation). New holds are blocked until unfrozen.
	Frozen
	// Closed is the terminal state: the account is permanently shut down and
	// accepts no further activity.
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

// Account is a customer demand-deposit account. It wraps a backing Liability
// account in the general ledger (GLAccount): the GL book balance of that
// account is the customer's money.
type Account struct {
	ID             AccountID
	GLAccount      ledger.AccountID
	Name           string
	Status         AccountStatus
	OverdraftLimit ledger.Amount // positive amount the balance may go below zero by; 0 means none
	CreatedAt      time.Time
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
//
// The customer's spendable funds are the GL book balance of the backing
// Liability account: a positive Book balance means the bank owes the customer
// that much.
type Balance struct {
	Book      ledger.Amount // GL book balance of the backing Liability account
	Holds     ledger.Amount // sum of active, non-expired holds
	Available ledger.Amount // Book - Holds + OverdraftLimit
}

// Snapshot is a point-in-time record of a deposit account's balance, taken at
// end-of-day for a given business date.
type Snapshot struct {
	AccountID AccountID
	Date      time.Time // the business day this snapshot represents
	Balance   Balance
	TakenAt   time.Time // when the snapshot was actually taken
}
