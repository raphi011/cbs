package payment

import (
	"time"

	"github.com/raphi011/ledger"
	"github.com/raphi011/ledger/deposit"
)

// ID types for each entity in the payment domain. Like the ledger package,
// these are defined types (not aliases) so the compiler prevents mixing up,
// say, a MandateID and a PaymentID.
type (
	ParticipantID string
	PaymentID     string
	MandateID     string
	CycleID       string
	SettlementID  string
	SchemeID      string
)

// SchemeDirection describes who initiates a payment under a scheme.
//
// This is NOT the same as ledger.Direction (debit/credit). It only governs
// initiation and mandate semantics — the money itself always flows from the
// debtor (payer) to the creditor (payee), regardless of direction.
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

	// Gross settlement settles each payment individually and immediately,
	// with no netting. Real-time gross settlement (RTGS) and instant
	// payment schemes use this model. Not implemented yet — the abstraction
	// is ready for it.
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
//
// The normal happy path is:
//
//	Initiated -> Accepted -> Cleared -> Settled
//
// with two branches: a payment can be Rejected before it clears, and a
// settled payment can be Returned (a SEPA R-transaction).
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

// PartyRef identifies one side of a payment: a customer deposit account at a
// specific participant bank.
type PartyRef struct {
	Participant ParticipantID
	Account     deposit.AccountID // the customer deposit account within that bank
	IBAN        string            // free-form label; no validation in this model
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

	Status       PaymentStatus
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
}

// Mandate is a debtor's standing authorization for a specific creditor to
// pull funds via direct debit.
type Mandate struct {
	ID        MandateID
	Debtor    PartyRef
	Creditor  PartyRef
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

	// NetPositions is populated when the cycle is closed. A positive value
	// means the participant is a net receiver (its reserves increase at
	// settlement); a negative value means it is a net payer. The values
	// always sum to zero.
	NetPositions map[ParticipantID]ledger.Amount

	OpenedAt     time.Time
	ClosedAt     time.Time
	SettlementID SettlementID
}

// Settlement is the record of a closed cycle's net positions being moved
// across participants' reserve accounts at the central bank.
type Settlement struct {
	ID           SettlementID
	CycleID      CycleID
	NetPositions map[ParticipantID]ledger.Amount
	SettlementTx ledger.TransactionID // the transaction in the central-bank ledger
	ValueDate    time.Time
	SettledAt    time.Time
}
