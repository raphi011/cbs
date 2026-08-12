package payment

import (
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// ID types for each entity in the payment domain. Like the ledger package,
// these are defined types (not aliases) so the compiler prevents mixing up,
// say, a MandateID and a PaymentID.
type (
	// ParticipantID names a bank, and the row it keys is called Bank. The mismatch
	// is deliberate, and it is written down here so that the next reader does not
	// take it for an oversight left behind by the split.
	ParticipantID string
	PaymentID     string
	MandateID     string
	CycleID       string
	SettlementID  string
	SchemeID      string
)

// SchemeDirection describes who initiates a payment under a scheme.
type SchemeDirection int

const (
	// Push payments are initiated by the debtor (the payer). The payer's
	// bank sends the funds. SEPA Credit Transfer is a push scheme.
	Push SchemeDirection = iota

	// Pull payments are initiated by the creditor (the payee), who collects
	// funds from the debtor under a previously signed mandate. SEPA Direct
	// Debit is a pull scheme.
	Pull
)

func (d SchemeDirection) String() string {
	switch d {
	case Push:
		return "Push"
	case Pull:
		return "Pull"
	default:
		return "Unknown"
	}
}

// SettlementModel describes how a scheme settles between banks.
type SettlementModel int

const (
	// Net settlement batches many payments together and settles only the
	// netted position of each participant at a cut-off. This is how most
	// "classic" retail schemes work, including SEPA CT and SEPA DD.
	Net SettlementModel = iota

	// Gross settlement settles each payment individually and immediately, with no
	// netting. Real-time gross settlement (RTGS) and instant payment schemes use
	// this model. Not implemented yet — the abstraction is ready for it.
	Gross
)

func (m SettlementModel) String() string {
	switch m {
	case Net:
		return "Net"
	case Gross:
		return "Gross"
	default:
		return "Unknown"
	}
}

// PaymentStatus tracks the lifecycle of a payment.
type PaymentStatus int

const (
	Initiated PaymentStatus = iota
	Accepted
	Cleared
	Settled
	Rejected
	Returned
)

func (s PaymentStatus) String() string {
	switch s {
	case Initiated:
		return "Initiated"
	case Accepted:
		return "Accepted"
	case Cleared:
		return "Cleared"
	case Settled:
		return "Settled"
	case Rejected:
		return "Rejected"
	case Returned:
		return "Returned"
	default:
		return "Unknown"
	}
}

// MandateStatus tracks whether a direct-debit mandate may still be used.
type MandateStatus int

const (
	MandateActive MandateStatus = iota
	MandateRevoked
)

func (s MandateStatus) String() string {
	switch s {
	case MandateActive:
		return "Active"
	case MandateRevoked:
		return "Revoked"
	default:
		return "Unknown"
	}
}

// CycleStatus tracks the lifecycle of a clearing cycle.
type CycleStatus int

const (
	// CycleOpen accepts new payments.
	CycleOpen CycleStatus = iota
	// CycleClosed has reached its cut-off: net positions are computed and
	// no further payments may join, but settlement has not happened yet.
	CycleClosed
	// CycleSettled has had its net positions settled at the central bank.
	CycleSettled
)

func (s CycleStatus) String() string {
	switch s {
	case CycleOpen:
		return "Open"
	case CycleClosed:
		return "Closed"
	case CycleSettled:
		return "Settled"
	default:
		return "Unknown"
	}
}

// PartyRef identifies one side of a payment: a customer deposit account, and
// the external address that was quoted to reach it.
type PartyRef struct {
	Account    deposit.AccountID // the customer deposit account at the agent's bank
	Identifier deposit.Identifier
}

// SameParty reports whether two refs name the same ACCOUNT, ignoring the
// address quoted to reach it.
func (r PartyRef) SameParty(o PartyRef) bool {
	return r.Account == o.Account
}

// PartyDetails is what a MESSAGE says about one side of a payment: which bank
// holds the account, and the name on it. It is the whole of what a counterparty
// bank is told, and the whole of what building an outbound message needs.
type PartyDetails struct {
	// Agent is the BIC of the bank holding this party's account.
	Agent iso20022.BIC
	// Name is the account holder's name.
	Name string
}

// Payment is a scheme-agnostic instruction to move funds from a debtor to a
// creditor. The concrete behaviour (push/pull, mandate, settlement timing)
// comes from its Scheme.
type Payment struct {
	ID         PaymentID
	Scheme     SchemeID
	Debtor     PartyRef // the payer
	Creditor   PartyRef // the payee
	Amount     ledger.Amount
	MandateID  MandateID // set for direct debits
	EndToEndID string    // client reference (the ISO 20022 "end-to-end id")

	// DebtorDetails and CreditorDetails are what a message says about each side.
	DebtorDetails   PartyDetails
	CreditorDetails PartyDetails

	Status PaymentStatus

	// RejectCode is the external-code-set reason a REJECTION (Status == Rejected,
	// set by RejectAtCSMTx) carries on the wire, and RejectReason is the free text
	// beside it.
	RejectCode   iso20022.StatusReason
	RejectReason string

	CycleID     CycleID
	BookingDate time.Time
	ValueDate   time.Time
	Description string
	Metadata    map[string]string
	CreatedAt   time.Time

	// References to the underlying ledger transactions, for tracing across
	// ledgers (there is no shared transaction id between banks).
	DebtorLegTx   ledger.TransactionID
	CreditorLegTx ledger.TransactionID

	// CreditorLegAccount is the account in the CREDITOR BANK's book that the
	// creditor leg actually credited, set by SettleAtBankTx and empty until that
	// leg is posted.
	CreditorLegAccount ledger.AccountID

	// ReturnClawbackTx and ReturnRefundTx are the two customer legs of a return,
	// each in its own bank's book: the clawback at the creditor's bank, the refund
	// at the debtor's bank. Empty until that bank posts its leg.
	ReturnClawbackTx ledger.TransactionID
	ReturnRefundTx   ledger.TransactionID
}

// Mandate is a debtor's standing authorization for a specific creditor to pull
// funds via direct debit.
type Mandate struct {
	ID MandateID

	// DebtorAgent is the BIC of the bank a collection under this mandate is sent
	// to, and it is the whole of what this row records about the other side.
	DebtorAgent iso20022.BIC

	Debtor   PartyRef
	Creditor PartyRef

	// Asset is what MaxAmount is denominated in, and it is the CREDITOR's
	// account's asset because that is the account this bank holds.
	Asset ledger.AssetCode

	MaxAmount ledger.Amount // 0 means unlimited
	Status    MandateStatus
	CreatedAt time.Time
}

// ClearingCycle collects accepted payments for one scheme and, at its
// cut-off, computes the net position of each participant.
type ClearingCycle struct {
	ID         CycleID
	Scheme     SchemeID
	Status     CycleStatus
	PaymentIDs []PaymentID

	// NetPositions is populated when the cycle is closed. A positive value means
	// the participant is a net receiver (its reserves increase at settlement); a
	// negative value means it is a net payer. The values always sum to zero.
	NetPositions map[iso20022.BIC]ledger.Amount

	OpenedAt time.Time
	ClosedAt time.Time

	// There is no SettlementID here.
}

// Settlement is the record of a closed cycle's net positions being moved
// across participants' reserve accounts at the central bank.
type Settlement struct {
	ID      SettlementID
	CycleID CycleID
	// Keyed by BIC, for ClearingCycle.NetPositions' reason and because this is
	// the institution that could least afford the other key: the settlement agent
	// holds no banks table to resolve one against.
	NetPositions map[iso20022.BIC]ledger.Amount
	// What was settled IN, taken off the instruction this agent acted on.
	Asset        ledger.AssetCode
	SettlementTx ledger.TransactionID // the transaction in the central-bank ledger
	ValueDate    time.Time
	SettledAt    time.Time
}

// AdviceStatus is how far a member bank has got with a settlement it was told
// about.
type AdviceStatus int

const (
	// AdviceAdvised is told and not yet booked — and NO COMMITTED ROW SAYS IT
	// today, which has to be known before it is read as a detector.
	AdviceAdvised AdviceStatus = iota
	// AdvicePosted is booked: the mirror leg is in this bank's own ledger.
	AdvicePosted
)

func (s AdviceStatus) String() string {
	switch s {
	case AdviceAdvised:
		return "Advised"
	case AdvicePosted:
		return "Posted"
	default:
		return "Unknown"
	}
}

// SettlementAdvice is a member bank's own record of a reserve movement it was
// told about — a cut-off's net settlement or a single return — and whether this
// bank has booked it yet: what its reserve moved by and what the central bank
// says it was left at.
type SettlementAdvice struct {
	Book      ledger.BookID
	Reference string
	Asset     ledger.AssetCode

	// Movement is SIGNED: positive means this bank's reserve went up.
	Movement       ledger.Amount
	ClosingBalance ledger.Amount

	Status   AdviceStatus
	MirrorTx ledger.TransactionID

	AdvisedAt time.Time
	PostedAt  time.Time
}

// SettlementStatement is one member's share of a settlement, as the CENTRAL
// BANK saw it at the moment it posted: the movement on that member's reserve
// account and the balance the account was left at.
type SettlementStatement struct {
	Agent        iso20022.BIC
	Account      ledger.AccountID
	Asset        ledger.AssetCode
	Reference    string
	StatementRef string

	Movement       ledger.Amount
	ClosingBalance ledger.Amount
	ValueDate      time.Time
}
