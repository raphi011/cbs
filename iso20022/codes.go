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
// rather than through accounts the two agents hold with each other. It is the
// only method SEPA uses, and the only one this system implements.
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
	// This is the honest home for two conditions this system can produce and
	// the code set has no member for: a collection within a valid mandate but
	// over its ceiling, and a currency mismatch — which in a euro-only scheme
	// cannot arise at all, and only exists here because this repository's
	// ledger is multi-asset. Reaching for the nearest-looking code would put a
	// false statement on the wire; MS03 says less, accurately.
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
