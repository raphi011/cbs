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
// immutable, so the two cannot drift, and deriving it would turn every
// ListAccounts into a join for a value that can never change.
// store/storetest asserts they always agree.
//
// A customer holding several assets holds several accounts, each with its own
// IBAN, which is how most European retail banks work.
type Account struct {
	ID        AccountID
	GLAccount ledger.AccountID
	Name      string
	Asset     ledger.AssetCode
	Status    AccountStatus

	// Identifiers are this account's external addresses — what a counterparty
	// quotes to pay it. Zero is normal: an account nobody pays from outside the
	// bank needs no address at all.
	//
	// They are part of the account aggregate rather than a sibling entity, and
	// that is load-bearing in the schema: PutDepositAccount writes both sides
	// itself, which is the one condition under which a real FOREIGN KEY is
	// allowed (see store/sqlite/schema/bank/0001_init.sql, on subledgers).
	Identifiers []Identifier

	// Accrued is interest earned and not yet charged, at sub-minor-unit
	// precision. The general ledger holds Accrued.Minor() in InterestGL; this
	// field holds the residue the ledger cannot represent.
	Accrued interest.Accrued
	// AccruedGross is the interest this account has produced over its WHOLE
	// LIFE, recomputed from its value-dated balance on every run. Accrued moves
	// by the change in it, which is what makes a backdated posting correct
	// itself: the days it takes effect over are re-derived with it in place,
	// gross moves, and the next run posts the difference.
	//
	// Unlike Accrued it is never decremented by capitalisation, and — unlike
	// before terms were effective-dated — it is never reset. There is no window
	// to start any more: the recompute opens at the account's opening terms row
	// and every day is re-derived at the terms that were actually in force on
	// it, so a repricing needs no fresh baseline. That is what widened the
	// retroactive-accrual window from the last repricing to account inception.
	//
	// Overflow is not a concern worth engineering around: this is int64
	// micro-minor-units, a EUR 10,000 overdraft at 10% produces on the order of
	// 1e11 a year, and int64 holds 9.2e18.
	AccruedGross interest.Accrued
	// LastAccrualDate is the business date accrual has been recomputed through.
	//
	// It enforces interest.Recompute's documented precondition: [from, to] must
	// cover at least one day, and a caller that recomputes an empty window over
	// a non-zero prior state is told to give the whole record back. That is the
	// only job it has left. It used to also carry the job of preventing a second
	// charge for a day already accrued; with a whole-life recompute the same
	// date produces the same gross and therefore a zero delta, so the same
	// invariant now has one fewer reason behind it.
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
	Book  ledger.Amount // GL book balance of the backing Liability account
	Holds ledger.Amount // sum of active, non-expired holds
	// Available is Book - Holds + the overdraft limit in force TODAY, resolved
	// from the account's effective-dated terms timeline rather than read off
	// the account row: what a customer could spend last March is as much a fact
	// about that March as what they were charged for it.
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

// AccountWithTerms is an account alongside the overdraft terms in force on a
// day — today, for every caller here.
//
// It exists because terms are resolved rather than cached on the row, and the
// API renders both together. Resolving one account at a time would make a
// listing N units of work; this pair is what lets Register do it in one.
//
// The alternative — keeping a copy of the current terms on the account — is the
// obvious shortcut and is the one this repo already argues against, in the
// schema, about a different field: a second copy of a fact creates the one
// thing a second copy always creates, the possibility that the two disagree.
type AccountWithTerms struct {
	Account Account
	// Terms is RESOLVED rather than the account's raw row: a floating account's
	// price lives in the catalogue, so the row alone would answer "nil" to the
	// question every caller of this type is asking.
	Terms EffectiveTerms
}
