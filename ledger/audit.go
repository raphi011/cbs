package ledger

import (
	"encoding/json"
	"time"
)

// BookID identifies one participant's book of accounts. Chart-of-accounts IDs
// are unique within a book, not globally, so BookID scopes every lookup.
type BookID string

// NetworkBook is the sentinel BookID for network-scoped entities (participants,
// payments, mandates, cycles, settlements), which belong to no single bank.
const NetworkBook BookID = "network"

// Scope names the layer an audit event originated in.
type Scope string

const (
	ScopeLedger  Scope = "ledger"
	ScopeDeposit Scope = "deposit"
	ScopePayment Scope = "payment"
	ScopeLending Scope = "lending"
)

// Audit event types. Grouped by scope; the values are the wire format and
// must not change once released.
const (
	// ScopeLedger
	EventLedgerCreated       = "ledger.created"
	EventSubledgerCreated    = "subledger.created"
	EventAccountCreated      = "account.created"
	EventTransactionPosted   = "transaction.posted"
	EventTransactionReversed = "transaction.reversed"

	// ScopeDeposit
	EventAccountOpened      = "account.opened"
	EventAccountFrozen      = "account.frozen"
	EventAccountUnfrozen    = "account.unfrozen"
	EventAccountClosed      = "account.closed"
	EventAccountDormant     = "account.dormant"
	EventAccountReactivated = "account.reactivated"
	EventHoldCreated        = "hold.created"
	EventHoldReleased       = "hold.released"
	EventHoldCaptured       = "hold.captured"
	EventSnapshotTaken      = "snapshot.taken"
	EventOverdraftTermsSet  = "overdraft.terms_set"
	EventOverdraftAccrued   = "overdraft.accrued"
	// EventOverdraftAccrualCorrected is a true-up of overdraft interest after a
	// backdated posting changed the balance a past day accrued on. Distinct
	// from EventOverdraftAccrued so a correction is visible as one in the log
	// rather than hiding inside the ordinary daily stream.
	EventOverdraftAccrualCorrected = "overdraft.accrual_corrected"
	EventOverdraftInterestCharged  = "overdraft.interest_charged"

	// ScopePayment. These are network-scoped: they describe entities that
	// belong to no single bank, so they are recorded under NetworkBook.
	EventParticipantAdded = "participant.added"
	EventMandateCreated   = "mandate.created"
	EventMandateRevoked   = "mandate.revoked"
	EventPaymentInitiated = "payment.initiated"
	EventPaymentAccepted  = "payment.accepted"
	EventPaymentRejected  = "payment.rejected"
	EventPaymentCleared   = "payment.cleared"
	EventPaymentSettled   = "payment.settled"
	EventPaymentReturned  = "payment.returned"
	EventCycleOpened      = "cycle.opened"
	EventCycleClosed      = "cycle.closed"
	EventCycleSettled     = "cycle.settled"

	// ScopeLending
	EventFacilityOpened    = "facility.opened"
	EventFacilityDisbursed = "facility.disbursed"
	EventFacilityDrawn     = "facility.drawn"
	EventFacilityAccrued   = "facility.accrued"
	EventFacilityCharged   = "facility.interest_charged"
	EventFacilityRepaid    = "facility.repaid"
	EventFacilityArrears   = "facility.arrears_changed"
	EventFacilityClosed    = "facility.closed"
)

// AuditEvent is an immutable record of one mutation.
//
// Payload is marshalled at append time rather than held as a live reference.
// The original implementation stored the entity pointer itself, so mutating an
// account later retroactively rewrote the audit record of its own creation — an
// immutable log with mutable entries.
type AuditEvent struct {
	// Seq is a monotonic total order assigned by the store. It, not OccurredAt,
	// is the ordering and pagination key: the clock is injectable and both the
	// tests and seed/clock.go pin it, so timestamps tie.
	Seq        int64
	ID         string
	BookID     BookID
	Scope      Scope
	Type       string
	EntityID   string
	Payload    json.RawMessage
	Metadata   map[string]string
	Actor      string // empty until authentication exists
	OccurredAt time.Time
}

// AuditFilter narrows an audit log listing. Zero values mean "no filter".
//
// A listing is always returned in ascending Seq order, whatever the filter.
// Before and Limit together form a backwards pager: Before is the exclusive
// upper cursor and Limit keeps the Limit events with the HIGHEST Seq below it,
// still handed back oldest-first. Paging therefore walks backwards through the
// log by passing the lowest Seq of the previous page as the next Before, while
// each page still reads in chronological order. Every Store implementation must
// behave this way; storetest pins it.
type AuditFilter struct {
	BookID   BookID
	Scope    Scope
	Type     string
	EntityID string
	// Before pages backwards: return only events with Seq < Before.
	Before int64
	// Limit caps the result size. Zero means the caller's default.
	Limit int
}
