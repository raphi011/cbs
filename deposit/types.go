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
	// order or fraud investigation). Withdrawals and new holds are blocked
	// until unfrozen; CREDITS still land, because the freeze modelled here is a
	// debit block — the garnishment case, where money owed to the customer
	// keeps arriving while they cannot take any out. A sanctions freeze blocks
	// credits too and this single status cannot express both; see
	// requireCreditable.
	Frozen
	// Closed is the terminal state: the account is permanently shut down and
	// accepts no further activity, in either direction. It is the only status
	// that refuses a credit, and it must: Close requires a zero balance, so a
	// credit landing afterwards would strand money no withdrawal can reach.
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
	//
	// It is an optional SURCHARGE, not a switch. Zero does not mean the excess
	// is free — it means the same Rate applies throughout, because a facility
	// on which exceeding the limit cost nothing would be cheaper outside the
	// limit than inside it. Only a zero Rate makes an overdraft interest-free,
	// and it makes the whole of it interest-free.
	UnarrangedRate interest.Rate
	DayCount       interest.DayCount
	// Accrued is interest earned and not yet charged, at sub-minor-unit
	// precision. The general ledger holds Accrued.Minor() in InterestGL; this
	// field holds the residue the ledger cannot represent.
	Accrued interest.Accrued
	// AccruedGross is the interest the current terms window has produced in
	// total, recomputed from the account's value-dated balance on every run.
	// Accrued moves by the change in it, which is what makes a backdated
	// posting correct itself: the day it lands on is recomputed, gross moves,
	// and the next run posts the difference.
	//
	// Unlike Accrued it is never decremented by capitalisation. It resets to
	// zero whenever terms are set, because that is where the window starts.
	AccruedGross interest.Accrued
	// TermsEffectiveFrom is the start of the recompute window: the date the
	// current terms took effect.
	//
	// The window is bounded there rather than at account opening because the
	// terms are stored as mutable columns. Recomputing across a repricing would
	// re-derive every earlier day at today's rate and post the difference,
	// rewriting accrual history every time an account is repriced. Prior
	// accrual is frozen instead, and widening this window to account inception
	// is what an effective-dated terms record would buy.
	//
	// Zero means the account has no priced overdraft and accrues nothing.
	TermsEffectiveFrom time.Time
	// LastAccrualDate is the business date accrual has been recomputed through.
	// It never moves backwards, which is what makes an end-of-day re-run a
	// no-op rather than a second charge — and it is why a backdated posting is
	// corrected by the next day's run rather than the same day's.
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

// Totals is a bank's customer-deposit position, split by the sign of each
// account's balance and keyed by asset.
//
// This split is the whole Asset-side classification of an overdraft, and it is
// derived rather than stored. A debit balance on a current account is a loan
// and advance to a customer, and a bank may not net it against the credit
// balances — but the drawn amount has no independent existence. It IS the
// negative balance, viewed by sign.
//
// In a real bank the same split falls out of subledger-to-GL summarization: the
// nightly feed buckets accounts by the sign of their balance into two control
// accounts. This system has no summarization step to hide it in, because it has
// no control accounts — see docs/deposit-accounts-vs-subledger.md. So the split
// is a query, exactly as "total customer deposits" already is, and no journal
// posts it.
//
// Keyed by asset because a total across assets is not a number. Euro and
// bitcoin do not add up.
type Totals struct {
	// Deposits is the sum of positive balances: what the bank owes customers.
	Deposits map[ledger.AssetCode]ledger.Amount
	// Overdrafts is the sum of the magnitudes of negative balances: what
	// customers owe the bank.
	Overdrafts map[ledger.AssetCode]ledger.Amount
}
