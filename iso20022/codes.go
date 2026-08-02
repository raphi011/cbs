package iso20022

// The code sets below are ISO 20022 EXTERNAL code lists: four-character values
// maintained outside the schema and published separately from it. They are what
// makes a rejection machine-actionable rather than a sentence, and they are the
// reason payment.Payment's free-text RejectReason is a simplification rather
// than a design.
//
// Only the members this system can produce are declared. A code with no
// condition behind it would be a constant nobody uses.

// SettlementMethod says how the interbank settlement of a payment happens.
type SettlementMethod string

// SettlementMethodClearing means the payment settles through a clearing system
// rather than through accounts the two agents hold with each other.
//
// It is the only method this system implements, which is not the same as the
// only one the scheme allows: the SCT Inter-PSP IG also permits INGA and INDA
// (idx 1.9), and the SDD Core IG restricts the element not at all (idx 1.10).
// See SettlementInstruction.
const SettlementMethodClearing SettlementMethod = "CLRG"

// ChargeBearer says who pays the transaction charges.
type ChargeBearer string

// ChargeBearerFollowingServiceLevel means charges follow the rules of the
// service level — for SEPA, that the debtor pays its bank's charges and the
// creditor its own, and that the transferred amount is never reduced. SEPA
// mandates it, so it is the only member declared.
const ChargeBearerFollowingServiceLevel ChargeBearer = "SLEV"

// ServiceLevel names the rulebook a payment is sent under.
type ServiceLevel string

// ServiceLevelSEPA is the SEPA rulebook.
const ServiceLevelSEPA ServiceLevel = "SEPA"

// LocalInstrument names the local instrument — the clearing-scheme-specific
// convention a payment or collection is processed under — in the standard's
// external local-instrument code list. See LocalInstrumentChoice for the
// element it fills.
type LocalInstrument string

// LocalInstrumentCore (CORE) identifies the SEPA Core Direct Debit scheme.
// It is the EPC guidelines' AT-20, mandatory on every SEPA Core collection —
// the ISO standard itself leaves LclInstrm optional, which is exactly why
// xmllint validating a collection without it proves nothing about EPC
// conformance. It is the only member declared: this package models Core
// Direct Debit and no other SEPA direct debit scheme (there is no B2B
// variant here).
const LocalInstrumentCore LocalInstrument = "CORE"

// SequenceType places a direct debit collection within its mandate's
// lifecycle. It is the EPC guidelines' AT-21, mandatory on every SEPA Core
// collection though the ISO standard again leaves it optional.
//
// This is the single element that most distinguishes a direct debit
// collection from a credit transfer: pacs.008 has nothing that answers "is
// this the first time this mandate has been exercised" because a credit
// transfer has no mandate to exercise. A debtor's bank reads SeqTp to decide
// whether it should expect a first-collection reconciliation or a routine
// recurring one.
//
// The ISO code set, SequenceType3Code, has a fifth member, RPRE
// (representment: a re-presentation of a collection returned earlier for
// insufficient funds). It is not declared here because it is not part of the
// SEPA Core rulebook this package models — the same reason LocalInstrument
// declares only CORE and not every member of its own external code list.
type SequenceType string

const (
	// SequenceTypeFirst (FRST): the first collection under this mandate.
	SequenceTypeFirst SequenceType = "FRST"
	// SequenceTypeRecurring (RCUR): a later collection under a mandate whose
	// first collection has already happened.
	SequenceTypeRecurring SequenceType = "RCUR"
	// SequenceTypeOneOff (OOFF): the only collection a mandate authorises.
	SequenceTypeOneOff SequenceType = "OOFF"
	// SequenceTypeFinal (FNAL): the last collection under a mandate that is
	// being closed out.
	SequenceTypeFinal SequenceType = "FNAL"
)

// GroupStatus is the status of a whole message in a pacs.002.
//
// It is separate from TransactionStatus because a message is a BULK: a file of
// a thousand credit transfers can be accepted with fifty rejections, which is
// what PART means. Collapsing the two would make a partly-rejected bulk
// inexpressible.
type GroupStatus string

const (
	// GroupStatusAccepted: every transaction in the message was accepted.
	GroupStatusAccepted GroupStatus = "ACCP"
	// GroupStatusPartiallyAccepted: some transactions were accepted and some
	// rejected. The per-transaction statuses say which.
	GroupStatusPartiallyAccepted GroupStatus = "PART"
	// GroupStatusRejected: the whole message was rejected, typically because it
	// could not be parsed or failed a file-level check.
	GroupStatusRejected GroupStatus = "RJCT"
)

// TransactionStatus is the status of one payment in a pacs.002.
//
// The four members map exactly onto payment.PaymentStatus — Accepted, Cleared,
// Settled, Rejected — which is a confirmation that the existing lifecycle was
// modelled on the right thing. payment.Returned has no member here: a return is
// a pacs.004, not a status.
type TransactionStatus string

const (
	// TransactionStatusAccepted: the instruction passed validation. The money
	// has not moved between banks.
	TransactionStatusAccepted TransactionStatus = "ACCP"
	// TransactionStatusSettlementInProcess: accepted and handed to settlement,
	// which has not completed. This is a cleared payment awaiting its cycle.
	TransactionStatusSettlementInProcess TransactionStatus = "ACSP"
	// TransactionStatusSettlementCompleted: settled. The point of finality.
	TransactionStatusSettlementCompleted TransactionStatus = "ACSC"
	// TransactionStatusRejected: refused, with a StatusReason saying why.
	TransactionStatusRejected TransactionStatus = "RJCT"
)

// StatusReason is a member of the external status-reason code set: why a
// transaction or group was rejected in a pacs.002.
type StatusReason string

const (
	// StatusReasonIncorrectAccountNumber (AC01): the account does not exist at
	// the agent it was addressed to.
	StatusReasonIncorrectAccountNumber StatusReason = "AC01"
	// StatusReasonClosedAccountNumber (AC04): the account exists and is closed.
	StatusReasonClosedAccountNumber StatusReason = "AC04"
	// StatusReasonInsufficientFunds (AM04): the debtor cannot cover the amount.
	StatusReasonInsufficientFunds StatusReason = "AM04"
	// StatusReasonDuplication (AM05): this instruction has been seen before.
	StatusReasonDuplication StatusReason = "AM05"
	// StatusReasonNoMandate (MD01): no valid mandate authorises the collection.
	// A REVOKED mandate is this and not a mandate-specific code, because a
	// revoked mandate is precisely the absence of a valid one.
	StatusReasonNoMandate StatusReason = "MD01"
	// StatusReasonNotSpecifiedAgentGenerated (MS03): the agent rejected it and
	// the reason has no code.
	//
	// This is the honest home for conditions the external code set has no
	// member for, because the rulebook behind the set does not contemplate
	// them: a mandate that exists but does not cover the parties or the
	// amount actually presented, a currency or asset mismatch that a
	// single-currency scheme's code set never needed a member for, and
	// various shapes of "this agent will not carry out what was asked" — an
	// unrecognised scheme, a non-positive amount, an asset the agent does
	// not operate in, or a return the scheme does not permit. Reaching for
	// the nearest-looking code in any of these cases would put a false
	// statement on the wire; MS03 says less, accurately.
	StatusReasonNotSpecifiedAgentGenerated StatusReason = "MS03"
	// StatusReasonBankIdentifierIncorrect (RC01): the BIC does not identify a
	// reachable participant.
	StatusReasonBankIdentifierIncorrect StatusReason = "RC01"
	// StatusReasonMissingDebtorAccountOrIdentification (RR01): the account
	// carries no address of the kind the scheme routes on.
	StatusReasonMissingDebtorAccountOrIdentification StatusReason = "RR01"
	// StatusReasonInvalidCutOffTime (TM01): the message arrived outside the
	// window that could accept it — here, with no clearing cycle open.
	StatusReasonInvalidCutOffTime StatusReason = "TM01"
	// StatusReasonInvalidFileFormat (FF01): the message could not be parsed.
	StatusReasonInvalidFileFormat StatusReason = "FF01"
)

// ReturnReason is a member of the external RETURN-reason code set: why a
// settled payment is being sent back in a pacs.004.
//
// It is a separate type from StatusReason even though the two sets share
// members, because they are separate sets in the standard and a code valid in
// one is not automatically valid in the other. Collapsing them would let a
// rejection reason be used as a return reason silently.
type ReturnReason string

const (
	// ReturnReasonClosedAccountNumber (AC04): the creditor account was closed
	// by the time the credit was applied.
	ReturnReasonClosedAccountNumber ReturnReason = "AC04"
	// ReturnReasonInsufficientFunds (AM04): used on a direct debit, where the
	// debtor's shortfall is discovered by the debtor's own bank after
	// settlement.
	ReturnReasonInsufficientFunds ReturnReason = "AM04"
	// ReturnReasonDuplication (AM05): the same collection settled twice.
	ReturnReasonDuplication ReturnReason = "AM05"
	// ReturnReasonNoMandate (MD01): the debtor disputes that any mandate
	// authorised the collection.
	ReturnReasonNoMandate ReturnReason = "MD01"
	// ReturnReasonNotSpecifiedAgentGenerated (MS03): returned by the agent for
	// a reason with no code.
	ReturnReasonNotSpecifiedAgentGenerated ReturnReason = "MS03"
	// ReturnReasonBankIdentifierIncorrect (RC01): the payment reached the wrong
	// agent.
	ReturnReasonBankIdentifierIncorrect ReturnReason = "RC01"
)

// CreditDebitCode says which way an amount runs, from the point of view of the
// account the statement is about.
//
// It is a separate element rather than a sign on the amount because
// ActiveCurrencyAndAmount cannot be negative — NewAmount refuses one — and that
// is the standard's shape throughout: money is a magnitude and the direction is
// said in words. It is the same separation ledger.Entry makes with Direction,
// arrived at independently.
type CreditDebitCode string

const (
	// CreditDebitCredit is money INTO the account the statement is about.
	CreditDebitCredit CreditDebitCode = "CRDT"
	// CreditDebitDebit is money OUT of it.
	CreditDebitDebit CreditDebitCode = "DBIT"
)

// BalanceType names which balance a Bal element carries.
//
// Only the closing booked balance is produced here, and that is the one this
// system needs: it is the figure a member reconciles its own reserve mirror
// against. OPBD (opening booked) is the obvious companion and is deliberately
// absent — it is derivable from CLBD and the entries, so asserting it would be a
// second source of truth for one fact, which is the shape of error a statement
// exists to detect rather than to introduce.
type BalanceType string

const (
	// BalanceTypeClosingBooked is CLBD: the balance after every entry in this
	// statement has been booked.
	BalanceTypeClosingBooked BalanceType = "CLBD"
)

// EntryStatus is whether an entry is booked or merely expected.
//
// Only BOOK is produced. A settlement statement is sent AFTER the netting
// transaction has committed at the central bank — settlement is final at that
// moment, which is the whole premise of this conversation — so there is no
// pending entry for this system to report. PDNG would be a claim about a
// movement that might not happen, and none of those exists here.
type EntryStatus string

const (
	// EntryStatusBooked is BOOK: the entry is on the account.
	EntryStatusBooked EntryStatus = "BOOK"
)
