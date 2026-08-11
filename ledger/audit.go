package ledger

import (
	"encoding/json"
	"time"
)

// BookID identifies one participant's book of accounts. Chart-of-accounts IDs
// are unique within a book, not globally, so BookID scopes every lookup.
type BookID string

// There is no sentinel BookID for "belongs to no single bank".
//
// Every row the payment layer writes has exactly one owner, so each institution
// keeps its own audit stream under its own book — a bank's is its own id, and
// see payment.ClearingHouseBook and payment.CentralBankBook for the other two.

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
	// register: one act, one event, keyed by the payer. It is a DEPOSIT event and
	// there is no payment-scope counterpart, because nothing about it clears —
	// see deposit.Register.TransferTx.
	EventTransferPosted    = "transfer.posted"
	EventIdentifierAdded   = "identifier.added"
	EventIdentifierRemoved = "identifier.removed"
	// The three ways an account's own terms row changes, kept apart because they
	// are three different decisions: what the bank will lend this customer, what
	// this customer was promised instead of the list price, and which product
	// they are on. One event type for all three would make a rate change and a
	// limit change indistinguishable in the log.
	EventOverdraftLimitSet        = "overdraft.limit_set"
	EventOverdraftPricingOverlaid = "overdraft.pricing_overlaid"
	EventAccountProductChanged    = "account.product_changed"
	EventOverdraftAccrued         = "overdraft.accrued"
	// EventOverdraftAccrualCorrected is a true-up of overdraft interest after a
	// backdated posting changed the balance a past day accrued on. Distinct
	// from EventOverdraftAccrued so a correction is visible as one in the log
	// rather than hiding inside the ordinary daily stream.
	EventOverdraftAccrualCorrected = "overdraft.accrual_corrected"
	EventOverdraftInterestCharged  = "overdraft.interest_charged"

	// ScopeProduct. A published price is exactly the kind of fact an auditor
	// asks who entered and when, so every catalogue write is logged — including
	// the draft, because the gap between drafting and publishing is where a
	// four-eyes control would sit if this system had one.
	EventProductCreated          = "product.created"
	EventProductRetired          = "product.retired"
	EventProductVersionDrafted   = "product.version_drafted"
	EventProductVersionPublished = "product.version_published"

	// ScopePayment. Each of these is recorded under the book of the institution
	// that performed it, in that institution's own store: a mandate event in the
	// creditor bank's log, a clearing event in the clearing house's, a settlement
	// event in the central bank's. See payment/audit.go.
	//
	// The first four are an admission, one event per act, because an admission
	// is four units of work at three institutions and the log is this system's
	// only immutable record of what each of them did. One event for the whole
	// thing would have to be written by somebody, and there is nobody: the bank
	// founds itself, the settlement agent opens an account in its own book, the
	// clearing house writes a routing entry, and the bank records what it was
	// told — three institutions, four commits, and a message between each pair.
	// See payment's FoundBankTx, OpenSettlementAccountTx, AdmitMemberTx and
	// RecordMembershipTx.
	//
	// EventParticipantAdded is the FOUNDING and nothing after it. Its payload is
	// a Founded bank whose settlement account numbers are empty, because at the
	// moment it is written no settlement agent has opened one — which is what
	// makes the other three worth having rather than derivable.
	EventParticipantAdded = "participant.added"
	// EventBankCodeAllocated is the settlement agent's OTHER act, keyed by the
	// applicant's BIC and made in the same unit of work: a national registry
	// giving one institution the code its customers' addresses will carry. Its
	// payload is the allocation — a country and a code — and it is written once
	// per bank per country, so the second currency of one admission appends
	// nothing.
	//
	// It is a separate event from the one below because it is a separate register
	// and, in the world, a separate institution: a bank's reserve account is at
	// its central bank and its Bankleitzahl comes from the Bundesbank's file. One
	// event covering both would say an account was opened where a code was
	// issued.
	EventBankCodeAllocated = "bank_code.allocated"
	// EventSettlementAccountOpened is the settlement agent's act, keyed by the
	// member's BIC: the identifier between institutions, and the only one this
	// institution has. Its payload is the SettlementMember row, which carries
	// every account the agent holds for that BIC and not only the one this act
	// opened — one request asks for one currency, so a bank in two schemes is
	// admitted twice and the second event supersedes the first.
	EventSettlementAccountOpened = "settlement_account.opened"
	// EventMemberAdmitted is the clearing house's act, keyed by BIC for the same
	// reason. Its payload is the RosterEntry: the address, the assets and the
	// admission the entry was written under.
	EventMemberAdmitted = "member.admitted"
	// EventMembershipRecorded is the bank's second act, keyed by the bank's own
	// id because this is the bank's own row. Its payload is the bank as it now
	// stands — a Member, with the settlement account numbers it has just been
	// told. It is the pair to EventParticipantAdded and the reason that one can
	// stay silent about everything the founding did not know.
	EventMembershipRecorded = "membership.recorded"
	// EventDirectoryRefreshed is a member bank taking delivery of a snapshot of
	// the scheme's routing directory, keyed by the SUBSCRIBER's own BIC — the act
	// is about the bank that pulled, not about any member in the file. Its payload
	// is the whole snapshot, which is what makes the log answer the question a
	// stale directory raises: not "is this bank behind" but "what did it believe
	// when it refused that payment".
	//
	// It is the only event in this block written by an institution about
	// institutions it does not act for, and it records no decision about any of
	// them: a directory says where to send a message and never whether to.
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
	//
	// They are the whole durable record of a run: there is no findings table,
	// because a finding is a pure function of the books at a moment and a stored
	// one is a cache that can disagree with them — which is the defect class the
	// instrument exists to detect, reintroduced by the instrument. What that
	// costs is a history of how long a break stood; what is left is that a run
	// happened, when, and what it said.
	//
	// The break event is keyed by the ACCOUNT the disagreement is on, because
	// that is what its reader goes and looks at. The run event is keyed by the
	// asset, because a bank reconciles each asset separately and a euro reserve
	// says nothing about a dollar one.
	EventReconciliationRun   = "reconciliation.run"
	EventReconciliationBreak = "reconciliation.break"

	// ScopeLending
	EventFacilityOpened    = "facility.opened"
	EventFacilityDisbursed = "facility.disbursed"
	EventFacilityDrawn     = "facility.drawn"
	// EventFacilityTermsSet records one row appended to a facility's
	// effective-dated terms timeline, carrying the effective date and the entry
	// date alike. It is the deposit layer's EventOverdraftTermsSet for a credit
	// facility, and it is the only control on a BACKDATED repricing: that moves
	// interest already charged to a borrower, and this event is what makes "who
	// repriced this facility backwards, and when" answerable.
	EventFacilityTermsSet = "facility.terms_set"
	EventFacilityAccrued  = "facility.accrued"
	// EventFacilityAccrualCorrected is a true-up of facility interest after a
	// backdated posting changed the drawn balance a past day accrued on.
	// Distinct from EventFacilityAccrued so a correction is visible as one in
	// the log rather than hiding inside the ordinary daily stream, and it is
	// the deposit layer's EventOverdraftAccrualCorrected for a credit facility.
	EventFacilityAccrualCorrected = "facility.accrual_corrected"
	// EventFacilityInterestRefunded discharges what a correction left in a
	// facility's interest-refunds-payable account: the bank paying the borrower
	// back interest it took and never earned.
	//
	// Distinct from EventFacilityRepaid because the money runs the other way. A
	// repayment settles what the borrower owes the bank; this settles what the
	// bank owes the borrower, and a log that called both "repaid" would net two
	// opposite movements into one figure.
	EventFacilityInterestRefunded = "facility.interest_refunded"
	EventFacilityCharged          = "facility.interest_charged"
	EventFacilityRepaid           = "facility.repaid"
	EventFacilityArrears          = "facility.arrears_changed"
	EventFacilityClosed           = "facility.closed"
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
