package iso20022

// The code sets below are ISO 20022 EXTERNAL code lists: four-character values
// maintained outside the schema and published separately from it.

// SettlementMethod says how the interbank settlement of a payment happens.
type SettlementMethod string

// SettlementMethodClearing means the payment settles through a clearing system
// rather than through accounts the two agents hold with each other.
const SettlementMethodClearing SettlementMethod = "CLRG"

// ChargeBearer says who pays the transaction charges.
type ChargeBearer string

// ChargeBearerFollowingServiceLevel means charges follow the rules of the
// service level — for SEPA, that the debtor pays its bank's charges and the
// creditor its own, and that the transferred amount is never reduced.
const ChargeBearerFollowingServiceLevel ChargeBearer = "SLEV"

// ServiceLevel names the rulebook a payment is sent under.
type ServiceLevel string

// ServiceLevelSEPA is the SEPA rulebook.
const ServiceLevelSEPA ServiceLevel = "SEPA"

// LocalInstrument names the local instrument — the clearing-scheme-specific
// convention a payment or collection is processed under — in the standard's
// external local-instrument code list.
type LocalInstrument string

// LocalInstrumentCore (CORE) identifies the SEPA Core Direct Debit scheme.
const LocalInstrumentCore LocalInstrument = "CORE"

// SequenceType places a direct debit collection within its mandate's lifecycle.
// It is the EPC guidelines' AT-21, mandatory on every SEPA Core collection
// though the ISO standard again leaves it optional.
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
	// StatusReasonNotSpecifiedAgentGenerated (MS03): the agent rejected it and the
	// reason has no code.
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
type ReturnReason string

const (
	// ReturnReasonIncorrectAccountNumber (AC01): the agent that was handed the
	// settled payment holds no such account.
	ReturnReasonIncorrectAccountNumber ReturnReason = "AC01"
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
	// ReturnReasonMissingDebtorAccountOrIdentification (RR01): the account the
	// settled payment named carries no address of the kind the scheme routes on.
	ReturnReasonMissingDebtorAccountOrIdentification ReturnReason = "RR01"
)

// CreditDebitCode says which way an amount runs, from the point of view of the
// account the statement is about.
type CreditDebitCode string

const (
	// CreditDebitCredit is money INTO the account the statement is about.
	CreditDebitCredit CreditDebitCode = "CRDT"
	// CreditDebitDebit is money OUT of it.
	CreditDebitDebit CreditDebitCode = "DBIT"
)

// BalanceType names which balance a Bal element carries.
type BalanceType string

const (
	// BalanceTypeClosingBooked is CLBD: the balance after every entry in this
	// statement has been booked.
	BalanceTypeClosingBooked BalanceType = "CLBD"
)

// EntryStatus is whether an entry is booked or merely expected.
type EntryStatus string

const (
	// EntryStatusBooked is BOOK: the entry is on the account.
	EntryStatusBooked EntryStatus = "BOOK"
)
