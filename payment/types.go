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
// specific participant bank, and the external address that was quoted to reach
// it.
//
// The identifier is STORED rather than derived, because identifiers are
// mutable: an account that later has its IBAN withdrawn must not retroactively
// change what a settled payment says it was sent to. What a payment records is
// the address actually used.
type PartyRef struct {
	Participant ParticipantID
	Account     deposit.AccountID // the customer deposit account within that bank
	Identifier  deposit.Identifier
}

// SameParty reports whether two refs name the same account at the same bank,
// ignoring the address quoted to reach it.
//
// Identity is the (participant, account) pair and NOTHING else, because the
// identifier is a record of how a party was reached on one occasion, not part
// of who that party is. Anything comparing two refs to decide whether they mean
// the same counterparty — a mandate against the payment claiming it — must use
// this rather than ==.
//
// The reason is that identifiers are mutable by design: reissuing a card is a
// RemoveIdentifier plus an AddIdentifier against an account whose balance and
// history do not move (deposit/register.go). Whole-struct equality would turn
// that ordinary operation into a silent, permanent kill of every mandate on the
// account — quoting the new address fails the mandate comparison, quoting the
// old one is refused because the account no longer holds it, and quoting
// nothing back-fills the new one and fails the comparison again. There is no
// UpdateMandate, so there would be no way back.
// TestMandateSurvivesAReissuedDebtorIdentifier pins it.
func (r PartyRef) SameParty(o PartyRef) bool {
	return r.Participant == o.Participant && r.Account == o.Account
}

// PartyDetails is what a MESSAGE says about one side of a payment: which bank
// holds the account, and the name on it. It is the whole of what a counterparty
// bank is told, and the whole of what building an outbound message needs.
//
// It is SEPARATE from PartyRef because the two answer different questions. A
// PartyRef names an account this system can resolve and act on; PartyDetails is
// a statement made in a message, which the receiving bank does not verify and
// has no business verifying. That is a restraint, not an inability — on its own
// side it has the account in hand (creditorSideTx/debtorSideTx) and could
// compare the name it finds there — and overwriting what the other bank
// asserted would desynchronise the stored payment from the message already on
// the wire.
//
// # Why it is stored on the payment rather than resolved
//
// It used to be resolved: payment.partyTx read the account out of the party's
// own bank's deposit register to get the name on it. That is a read of ANOTHER
// BANK'S BOOK on the happy path of every submission, measured by the recorder in
// mesh/books_test.go and recorded there at length. A real payer's bank knows the
// payee's name because the payer typed it in, not because building the
// instruction went and found it, so the name travels on the instruction. That
// is a claim about the payment and not about the whole system: GET /directory
// still resolves an address across the network and does read the resolved
// account's name off its own bank's register. What no longer happens is that
// answer reaching a payment — what is stored is what was typed.
//
// Storing it is therefore not a cache. There is nothing to fall back to.
type PartyDetails struct {
	// Agent is the BIC of the bank holding this party's account.
	//
	// On a SUBMISSION it is never taken from the instruction, on either side.
	// Both come from the roster — the participant row for the party the payment
	// already names — because this element ROUTES: it goes out as
	// CdtrAgt/DbtrAgt and the clearing house relays on it without a store read
	// of its own. A payer allowed to assert it is a payer allowed to choose
	// which bank receives their payment, which was measured doing exactly that
	// before it was closed; see SubmitPaymentTx and
	// mesh/books_test.go's TestAWrongCounterpartyAgentDoesNotMisroute. It is
	// also what a real SEPA originating bank does: IBAN-only since 2016, the
	// payer gives an address and a name and the bank derives the rest.
	//
	// On a RECEIVED message it is what the message said, read off the wire by
	// CreditTransferRequest/DirectDebitRequest, which is a different question
	// with a different answer — there the agent is the sender's assertion and
	// this system records rather than verifies it.
	Agent iso20022.BIC
	// Name is the account holder's name. For the SUBMITTING bank's own side
	// this is taken from its own deposit register, not from whatever the
	// request supplied — a bank is the authority on its own customer's name,
	// exactly as the Dbtr element on a real pacs.008 is what the originating
	// bank holds on file, not a claim it takes on faith. For the COUNTERPARTY's
	// side there is no register to be the authority: the instruction asserts
	// it, because that is the only place it can come from. The asymmetry is
	// the point — see SubmitPaymentTx.
	//
	// It is the ONLY thing about the counterparty a payer asserts. The name is
	// carried because no bank can look it up without reading another bank's
	// register; the agent is derived because routing is not the payer's to
	// decide.
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
	// The submitting bank fills its OWN side from its own register and is TOLD
	// the counterparty's; see SubmitPaymentTx.
	DebtorDetails   PartyDetails
	CreditorDetails PartyDetails

	Status PaymentStatus

	// RejectCode is the external-code-set reason a REJECTION (Status ==
	// Rejected, set by RejectAtCSMTx) carries on the wire, and RejectReason
	// is the free text beside it.
	//
	// Both, not either, for a rejection: the code is what makes it
	// machine-actionable — it is the entire point of ISO 20022's external
	// code sets — and the text is what says the part no code can. A rejection
	// produced inside this system without a code would be one the mesh could
	// not put in a pacs.002.
	//
	// A RETURN (Status == Returned, set by ReturnPaymentTx) is not a
	// rejection and does not set RejectCode: pacs.004 draws its reason from a
	// different external code set, iso20022.ReturnReason, not this one, and
	// giving a return a StatusReason here would misrepresent which set it
	// actually carries on the wire. RejectReason is reused as the return's
	// free text because the two share the same shape, not the same meaning.
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
