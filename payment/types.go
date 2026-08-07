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
	// ParticipantID names a bank, and the row it keys is called Bank. The
	// mismatch is deliberate, and it is written down here so that the next
	// reader does not take it for an oversight left behind by the split.
	//
	// It is on both of a payment's PartyRefs, on every bank path in the API, in
	// the shared store suite and in the web types. Renaming it is a
	// mechanical diff across the whole repository for no behavioural change, and
	// it would bury the content of the task that split the row.
	//
	// It is also the one identifier of the three rows that is not a BIC, and
	// that is what it is for: the network numbers its members, and a bank's own
	// id is its own. Neither the settlement agent's row nor the clearing house's
	// carries it — see SettlementMember and RosterEntry, which are keyed by BIC
	// because a BIC is all either institution is ever told.
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

// PartyRef identifies one side of a payment: a customer deposit account, and the
// external address that was quoted to reach it.
//
// # It does not name the bank, and it used to
//
// The field was Participant, a ParticipantID, and it sat beside a
// PartyDetails.Agent naming the same bank by BIC. Task 18 made a bank's id its
// BIC (see AsBank), so the two could no longer differ, and two values that cannot
// differ are not two facts. The agent is the survivor because it is what ROUTES:
// it goes out as DbtrAgt/CdtrAgt and the clearing house relays on it, which is
// something an id nobody puts on the wire never did.
//
// So one side of a payment is now read as a PAIR — this ref says which account,
// and the PartyDetails beside it says at which bank. Payment.Debtor and
// Payment.DebtorDetails are that pair, and neither half answers "who is this" on
// its own. See SameParty, which stops being a whole identity comparison for
// exactly that reason, and store/sqlite/schema/bank/0001_init.sql's payments,
// where the two dropped columns are argued at length.
//
// One consequence is worth naming rather than discovering: a WRONG counterparty
// agent and a wrong counterparty are no longer distinguishable, because there is
// one value to be wrong. Nothing is weakened — the instruction still reaches the
// bank it names, which resolves the address in its own register and answers AC01
// — but a test constructing a DISAGREEMENT between the two has nothing to build.
//
// The identifier is STORED rather than derived, because identifiers are
// mutable: an account that later has its IBAN withdrawn must not retroactively
// change what a settled payment says it was sent to. What a payment records is
// the address actually used.
type PartyRef struct {
	Account    deposit.AccountID // the customer deposit account at the agent's bank
	Identifier deposit.Identifier
}

// SameParty reports whether two refs name the same ACCOUNT, ignoring the address
// quoted to reach it.
//
// It is HALF of an identity comparison and not the whole of one. An account id is
// unique within the bank that issued it and nowhere else, so a caller comparing
// two records that could name accounts at different banks must compare the agent
// BIC beside each ref as well. SDD.ValidateMandate is the one that has to —
// against Mandate.DebtorAgent — and it is the only caller for which the question
// arises, because both of its refs are the creditor bank's own rows.
//
// It was the whole comparison while a ref carried its own Participant, and it is
// named as half rather than quietly narrowed because the missing half is now a
// thing a caller can forget. See PartyRef.
//
// What it deliberately still ignores is the identifier, because the identifier is
// a record of how a party was reached on one occasion, not part of who that party
// is. Anything comparing two refs to decide whether they mean the same
// counterparty — a mandate against the payment claiming it — must use this rather
// than ==.
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
	return r.Account == o.Account
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
// instruction went and found it, so the name travels on the instruction.
//
// This used to add that it was a claim about the payment and not about the whole
// system, because GET /directory "still resolves an address across the network"
// and reads the resolved account's name. That exception is gone as of Task 18a:
// the lookup answers out of ONE bank's own register, so nothing in this system
// resolves an address across banks and there is no cross-bank name to reach a
// payment even in principle. The claim is now about the whole system.
//
// Storing it is therefore not a cache. There is nothing to fall back to.
type PartyDetails struct {
	// Agent is the BIC of the bank holding this party's account.
	//
	// On a SUBMISSION it is never taken from the instruction, on either side.
	// Both come from the BANK ROW of the party the payment already names — not
	// from the clearing house's roster, which is keyed by the very BIC being
	// derived — because this element ROUTES: it goes out as
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
	// The submitting bank fills its OWN side from its own register; for the
	// counterparty it is TOLD the name and derives the agent from that
	// counterparty's own bank row — see PartyDetails.Agent, PartyDetails.Name,
	// and SubmitPaymentTx.
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
	// A RETURN (Status == Returned, set by PostReturnLegTx when the SECOND of
	// the two banks posts its leg) is not a rejection and does not set
	// RejectCode: pacs.004 draws its reason from a
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

	// CreditorLegAccount is the account in the CREDITOR BANK's book that the
	// creditor leg actually credited, set by SettleAtBankTx and empty until
	// that leg is posted.
	//
	// Usually it is the payee's own GL account. It is the bank's
	// unclaimed-balances account when the payee's account would not take the
	// credit — see SettleAtBankTx, which diverts rather than stranding.
	//
	// # Why it is STORED and not derived
	//
	// Because the fact it records is about a MOMENT, and the account it names
	// cannot be worked out from any later reading of the world. PostReturnLegTx
	// has to claw the money back from wherever it landed, and the only other way
	// to find out is to re-ask whether the payee's account is creditable — which
	// answers a question about NOW, not about the cut-off. A payee who was open
	// at settlement and closed afterwards is indistinguishable from one who was
	// closed at settlement, so a return that re-derived would debit the wrong
	// account in precisely the case that matters, and the ledger would not
	// notice: an overdrawn deposit is a Liability going negative, which
	// ledger.checkSufficientBalance does not refuse.
	//
	// It is an AccountID rather than a "was diverted" flag deliberately. The
	// flag would be a record of one special case; this is a record of what
	// happened, and it stays true if a later scheme grows a third destination.
	CreditorLegAccount ledger.AccountID

	// ReturnClawbackTx and ReturnRefundTx are the two customer legs of a
	// return, each in its own bank's book: the clawback at the creditor's bank,
	// the refund at the debtor's bank. Empty until that bank posts its leg.
	//
	// Neither names the account it moved, and neither can be read as though it
	// did. The clawback comes out of CreditorLegAccount in the ordinary case
	// and out of that bank's ReturnsReceivable when the account CreditorLegAccount
	// names has closed since — the bank funds the refund itself and books what
	// it is owed. The refund goes into the payer's account, or into that bank's
	// unclaimed balances when the payer's account has closed since. Both
	// substitutions are decided at the moment of posting, by clawbackTx and
	// refundTx, and the entries of the transactions these ids name are where
	// they are recorded. There is no CreditorLegAccount equivalent for the
	// return, and the reason there does not need to be is that nothing in this
	// system claws a clawback back.
	//
	// They are how PostReturnLegTx knows it is the SECOND leg, and therefore
	// the one that takes the payment to Returned: the leg that finds the other
	// side's id already written is the one arriving last. Which of the two goes
	// first flips with the scheme's direction — the returning bank posts before
	// it sends — so neither field can be the marker on its own.
	//
	// # What this cannot survive
	//
	// It works because one payment is one row that both banks can see. Under
	// sub-project 8's store split it is two rows in two stores, and neither
	// bank can read the other's, so the counterparty's transaction id stops
	// being available to read at all. What replaces it is Task 18's to decide —
	// the material a bank will have is the return message it received and the
	// status its own row is already at. This note exists so that task inherits
	// the problem rather than discovering it.
	ReturnClawbackTx ledger.TransactionID
	ReturnRefundTx   ledger.TransactionID
}

// Mandate is a debtor's standing authorization for a specific creditor to
// pull funds via direct debit.
//
// It is the CREDITOR's bank's row. In SEPA the creditor holds the mandate —
// SDD.ValidateMandate has said so since the pull flow landed, and it is the
// creditor's bank that checks one at submission — and CreateMandateTx is what
// makes the storage agree with the rule.
type Mandate struct {
	ID MandateID

	// DebtorAgent is the BIC of the bank a collection under this mandate is sent
	// to, and it is the whole of what this row records about the other side.
	//
	// It is here rather than on Debtor because a PartyRef stopped naming a bank —
	// see PartyRef — and because there is nothing to pair it with on the creditor
	// side. A mandate is the CREDITOR's bank's row, so the creditor's agent is
	// always this institution; a field holding the same value in every mandate it
	// ever writes records nothing, and CreateMandateTx's ErrNotThisBanksMandate is
	// the guard such a field would have looked like it was supporting.
	//
	// Recorded rather than resolved, for the reason the whole row is: that bank's
	// register is in that bank's database.
	DebtorAgent iso20022.BIC

	Debtor   PartyRef
	Creditor PartyRef

	// Asset is what MaxAmount is denominated in, and it is the CREDITOR's
	// account's asset because that is the account this bank holds.
	//
	// It is on the row rather than derived, and the derivation it replaces is
	// why. A mandate carries no scheme, so api resolved this by loading the
	// DEBTOR's bank and listing its deposit accounts — on the clearing house's
	// port, which is one institution reading another's register over HTTP for a
	// field. The `csm` shape has no deposit register at all, so that read had no
	// answer coming; and even at the creditor's own bank, deriving it later
	// would report today's asset for an authorisation granted under a different
	// one. What a mandate is denominated in is a fact about the moment it was
	// granted, exactly as a payment's stored addresses are.
	//
	// It is not checked against the DEBTOR's account, and cannot be: that
	// account is at another bank. See CreateMandateTx for where the mismatch is
	// refused instead.
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

	// NetPositions is populated when the cycle is closed. A positive value
	// means the participant is a net receiver (its reserves increase at
	// settlement); a negative value means it is a net payer. The values
	// always sum to zero.
	//
	// Keyed by BIC, and it was keyed by ParticipantID. The difference was
	// invisible while one database held both a roster and a banks table to
	// convert between them, and it is not invisible now: these figures are
	// computed by the clearing house and sent to the settlement agent, whose
	// settlement_members are keyed by BIC and by nothing else. A position naming a
	// participant id would have been a set of figures about banks the recipient
	// could not identify — which is what mesh.csm.settlementLegs' id-to-BIC lookup
	// was for, and it is gone. See cycles in store/sqlite/schema/csm/0001_init.sql.
	NetPositions map[iso20022.BIC]ledger.Amount

	OpenedAt time.Time
	ClosedAt time.Time

	// There is no SettlementID here, and there was until Task 18d.
	//
	// The settlement's id is the SETTLEMENT AGENT's own row number, allocated
	// inside that agent's own unit of work in its own database. What comes back
	// to the clearing house is a pacs.002 quoting the CYCLE — because the cycle
	// is what the clearing house asked about — so no message in this system ever
	// carries the other number and the field could only ever have been empty.
	//
	// What the clearing house knows is that it was told, and Status carries that:
	// CycleSettled. The link in the other direction is real and kept where it can
	// be — Settlement.CycleID, which is the agent's own row naming what it
	// settled, answerable through Tx.GetSettlementByCycle.
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
	//
	// It is stored rather than derived, and the derivation it replaces was a
	// crossing: a settlement's asset used to be read settlement -> its cycle ->
	// the cycle's scheme, and the cycle is the CLEARING HOUSE's row. The
	// settlement agent has no cycles table, so from Task 18d the only reader of
	// that chain (api's settlementAsset) was asking one institution about
	// another's database and getting "no such table" for it.
	//
	// The agent has always known the answer without asking: positionsIn refuses
	// an instruction whose legs are not all in ONE asset, so the batch it posts
	// has exactly one and this records it.
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
	//
	// PostSettlementAdviceTx writes the row and posts the mirror leg in one unit
	// of work: a failed posting rolls the row back with it, and a successful one
	// supersedes it with AdvicePosted before the commit. The only arm that
	// commits this status is the zero-movement guard, and the settlement path
	// never reaches it — the central bank sends no statement for a position of
	// zero, because settlementLegsTx skips one.
	//
	// It is kept rather than deleted because it is the honest name for a state
	// this type has to be able to express, and because it becomes reachable the
	// moment the write and the posting stop sharing a unit of work. They must not
	// while a bank's mirror leg and its record of having made it have to be
	// atomic: a row asserting a booking that did not happen is worse than no row.
	//
	// So the detector for a bank that was told and did not book is the ABSENCE of
	// a row against a clearing suspense that has not returned to zero. This doc
	// used to claim the opposite, and so did four other layers; see
	// SettlementAdvice and PostSettlementAdviceTx.
	AdviceAdvised AdviceStatus = iota
	// AdvicePosted is booked: the mirror leg is in this bank's own ledger.
	AdvicePosted
)

// SettlementAdvice is a member bank's own record of a reserve movement it was
// told about — a cut-off's net settlement or a single return — and whether this
// bank has booked it yet: what its reserve moved by and what the central bank
// says it was left at.
//
// # It belongs to the BANK and not to the network
//
// Book is part of its identity, which is the whole point. A cycle is the
// clearing house's and a settlement is the central bank's; this is the member's,
// and under sub-project 8 it lives in that member's own store. Two banks advised
// of the same movement write two rows independently, so "one bank has booked
// this and the other has not" is expressible — as one row present and the other
// ABSENT, rather than as two rows at different statuses. See AdviceAdvised for
// why the difference matters and what it cost to get wrong.
//
// # Reference is opaque, and that is the point
//
// It is the AcctSvcrRef a statement carried, kept verbatim: a cycle id on the
// path SettleCycleTx builds today, a payment id on the return path sub-project
// 8's Task 16d adds. A member bank holds no cycles and no other institution's
// payment ids, so it cannot tell the two apart and has no reason to — Reference
// exists to be quoted back at the central bank and to be this row's own key, not
// to be resolved to anything this bank holds. There is deliberately no field
// saying which kind a row is: ids are unique across the store, and Task 19's
// reconciliation walks one shape, not two. See AdvisedMovement's doc and
// StatementMessage's.
//
// # ClosingBalance is stored and nothing checks it yet
//
// It is what the central bank says this bank's reserve stands at, and it is the
// only figure in this system that a bank can check its own books against without
// reading another institution's store. Task 19 is where that check happens. It is
// stored now because it arrives now, and a statement's balance discarded on
// receipt is a balance nobody can ever go back for.
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

// SettlementStatement is one member's share of a settlement, as the CENTRAL BANK
// saw it at the moment it posted: the movement on that member's reserve account
// and the balance the account was left at.
//
// It exists to be captured INSIDE the settling transaction's unit of work and
// returned from it, rather than re-read afterwards, because the closing balance
// is a claim about a moment. A balance read after the commit is a different
// number the instant anything else settles, and a statement asserting the wrong
// one is worse than no statement: the whole point of carrying Bal/CLBD is that a
// member can check its own posting against it. SettleCycleTx builds one per
// member, inside the unit of work and after the netting transaction has posted,
// and returns them beside the Settlement for exactly that reason.
//
// Movement is SIGNED — positive means the member's reserve went up — and the
// message it becomes is not: ActiveCurrencyAndAmount cannot be negative, so the
// direction travels as CdtDbtInd. See StatementMessage.
//
// Reference travels as AcctSvcrRef and StatementRef as Stmt/Id — the account
// servicer's own reference for the entry and for the statement it sits in.
// SettleCycleTx sets Reference to the cycle's id and StatementRef to the
// settlement row's, as string(SettlementID); the return path Task 16d adds sets
// Reference to a payment id and StatementRef to a value that is NOT a row's key
// (see AdvisedMovement's doc), which is why StatementRef is a plain string and
// not a typed SettlementID.
//
// It carried a Member beside Agent — the same bank as an id and as an address —
// until Task 18 made the two one value. Agent is the survivor for the reason the
// payments table's agent columns are: it is what goes on the wire, and the
// settlement agent that builds these holds no table that could turn one into the
// other. See payment.PartyRef, which is the same collapse on a payment's parties.
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
