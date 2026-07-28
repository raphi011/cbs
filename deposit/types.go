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
//
// Asset duplicates the backing GL account's asset. That is a deliberate
// exception to deriving rather than duplicating: the GL account's asset is
// immutable, so the two cannot drift, and deriving it would make ListAccounts
// an N+1 lookup in store/mem and a join in store/pg — divergent complexity in
// both stores for a value that cannot change. store/storetest asserts they
// always agree.
//
// A customer holding several assets holds several accounts, each with its own
// IBAN, which is how most European retail banks work.
type Account struct {
	ID             AccountID
	GLAccount      ledger.AccountID
	Name           string
	Asset          ledger.AssetCode
	Status         AccountStatus
	OverdraftLimit ledger.Amount // positive amount the balance may go below zero by; 0 means none

	// Credit terms for the arranged overdraft. A zero Rate means the account
	// accrues no interest, the same convention a zero OverdraftLimit already
	// follows for the facility itself.
	//
	// These live here rather than in the lending package for two reasons. An
	// overdrawn account's drawn amount is the negative balance of its own GL
	// account viewed by sign — it has no independent existence and therefore no
	// loan account — so there is no facility record for them to belong to. And
	// real core banking puts an arranged overdraft in the current-account module
	// rather than in Loans, which is also what keeps deposit from depending on a
	// package that depends on deposit.
	Rate interest.Rate
	// UnarrangedRate applies to any balance drawn beyond OverdraftLimit. An
	// account can get there despite CheckWithdrawal: a direct GL posting does
	// not pass through this layer, and capitalizing interest on a fully-drawn
	// overdraft pushes it over by itself.
	UnarrangedRate interest.Rate
	DayCount       interest.DayCount
	// Accrued is interest earned and not yet charged, at sub-minor-unit
	// precision. The general ledger holds Accrued.Minor() in InterestGL; this
	// field holds the residue the ledger cannot represent.
	Accrued interest.Accrued
	// LastAccrualDate is the business date accrual has been run through. It
	// never moves backwards, which is what makes an end-of-day re-run a no-op
	// rather than a second charge.
	LastAccrualDate time.Time
	// InterestGL is this account's own accrued-interest-receivable account, an
	// Asset. It is created the first time a non-zero rate is set, so an account
	// with no overdraft facility does not carry an empty one.
	InterestGL ledger.AccountID

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
