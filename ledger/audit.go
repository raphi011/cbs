package ledger

import (
	"encoding/json"
	"time"
)

// BookID identifies one participant's book of accounts. Chart-of-accounts IDs
// are unique within a book, not globally, so BookID scopes every lookup.
type BookID string

// There is no sentinel BookID for "belongs to no single bank".

// Scope names the layer an audit event originated in.
type Scope string

const (
	ScopeLedger  Scope = "ledger"
	ScopeDeposit Scope = "deposit"
	ScopeProduct Scope = "product"
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
	EventSlotMapped          = "slot.mapped"
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
	// EventTransferPosted is a book transfer between two accounts in one bank's
	// register: one act, one event, keyed by the payer.
	EventTransferPosted    = "transfer.posted"
	EventIdentifierAdded   = "identifier.added"
	EventIdentifierRemoved = "identifier.removed"
	// The three ways an account's own terms row changes, kept apart because they
	// are three different decisions: what the bank will lend this customer, what
	// this customer was promised instead of the list price, and which product they
	// are on.
	EventOverdraftLimitSet        = "overdraft.limit_set"
	EventOverdraftPricingOverlaid = "overdraft.pricing_overlaid"
	EventAccountProductChanged    = "account.product_changed"
	EventOverdraftAccrued         = "overdraft.accrued"
	// EventOverdraftAccrualCorrected is a true-up of overdraft interest after a
	// backdated posting changed the balance a past day accrued on.
	EventOverdraftAccrualCorrected = "overdraft.accrual_corrected"
	EventOverdraftInterestCharged  = "overdraft.interest_charged"

	// ScopeProduct.
	EventProductCreated          = "product.created"
	EventProductRetired          = "product.retired"
	EventProductVersionDrafted   = "product.version_drafted"
	EventProductVersionPublished = "product.version_published"

	// ScopePayment.
	EventParticipantAdded = "participant.added"
	// EventBankCodeAllocated is the settlement agent's OTHER act, keyed by the
	// applicant's BIC and made in the same unit of work: a national registry
	// giving one institution the code its customers' addresses will carry.
	EventBankCodeAllocated = "bank_code.allocated"
	// EventSettlementAccountOpened is the settlement agent's act, keyed by the
	// member's BIC: the identifier between institutions, and the only one this
	// institution has.
	EventSettlementAccountOpened = "settlement_account.opened"
	// EventMemberAdmitted is the clearing house's act, keyed by BIC for the same
	// reason. Its payload is the RosterEntry: the address, the assets and the
	// admission the entry was written under.
	EventMemberAdmitted = "member.admitted"
	// EventMembershipRecorded is the bank's second act, keyed by the bank's own id
	// because this is the bank's own row. Its payload is the bank as it now
	// stands, with the settlement account numbers it has just been told.
	EventMembershipRecorded = "membership.recorded"
	// EventDirectoryRefreshed is a member bank taking delivery of a snapshot of
	// the scheme's routing directory, keyed by the SUBSCRIBER's own BIC — the act
	// is about the bank that pulled, not about any member in the file.
	EventDirectoryRefreshed = "directory.refreshed"
	EventMandateCreated     = "mandate.created"
	EventMandateRevoked     = "mandate.revoked"
	EventPaymentInitiated   = "payment.initiated"
	EventPaymentAccepted    = "payment.accepted"
	EventPaymentRejected    = "payment.rejected"
	EventPaymentCleared     = "payment.cleared"
	EventPaymentSettled     = "payment.settled"
	EventPaymentReturned    = "payment.returned"
	EventCycleOpened        = "cycle.opened"
	EventCycleClosed        = "cycle.closed"
	EventCycleSettled       = "cycle.settled"

	// EventReconciliationRun and EventReconciliationBreak are one bank checking
	// its own books against the statements it was sent, and what that found.
	EventReconciliationRun   = "reconciliation.run"
	EventReconciliationBreak = "reconciliation.break"

	// ScopeLending
	EventFacilityOpened    = "facility.opened"
	EventFacilityDisbursed = "facility.disbursed"
	EventFacilityDrawn     = "facility.drawn"
	// EventFacilityTermsSet records one row appended to a facility's
	// effective-dated terms timeline, carrying the effective date and the entry
	// date alike.
	EventFacilityTermsSet = "facility.terms_set"
	EventFacilityAccrued  = "facility.accrued"
	// EventFacilityAccrualCorrected is a true-up of facility interest after a
	// backdated posting changed the drawn balance a past day accrued on.
	EventFacilityAccrualCorrected = "facility.accrual_corrected"
	// EventFacilityInterestRefunded discharges what a correction left in a
	// facility's interest-refunds-payable account: the bank paying the borrower
	// back interest it took and never earned.
	EventFacilityInterestRefunded = "facility.interest_refunded"
	EventFacilityCharged          = "facility.interest_charged"
	EventFacilityRepaid           = "facility.repaid"
	EventFacilityArrears          = "facility.arrears_changed"
	EventFacilityClosed           = "facility.closed"
)

// AuditEvent is an immutable record of one mutation.
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
