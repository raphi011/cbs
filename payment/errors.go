package payment

import "errors"

// Sentinel errors returned by the Network. Callers use errors.Is, the same
// convention the ledger package uses.
var (
	// ErrParticipantNotFound is a participant ID matching no bank registered with
	// the system. It keeps its name for the reason ParticipantID keeps its: the id
	// is the thing not found.
	ErrParticipantNotFound = errors.New("participant not found")

	// ErrSettlementMemberNotFound is a BIC the settlement agent holds no account
	// for.
	ErrSettlementMemberNotFound = errors.New("payment: the settlement agent holds no account for this BIC")

	// ErrBankCodeNotAllocated is a (country, code) the settlement agent's registry
	// has no row for.
	ErrBankCodeNotAllocated = errors.New("payment: no bank code has been allocated under this country and code")

	// ErrBankCodeUnknown is an address whose bank code resolves to nothing in the
	// routing directory THIS bank holds.
	ErrBankCodeUnknown = errors.New("payment: this bank's routing directory holds no entry for this bank code")

	// ErrRosterEntryNotFound is a BIC the clearing house does not route to.
	ErrRosterEntryNotFound = errors.New("payment: no member is routed to under this BIC")

	// ErrBankNotAdmitted is a clearing payment one of whose two banks the clearing
	// house does not route to.
	ErrBankNotAdmitted = errors.New("payment: this scheme does not clear for one of these two banks")

	// ErrOnUsPayment is a submission whose payer and payee bank at the SAME
	// institution.
	ErrOnUsPayment = errors.New("payment: both parties bank at the same institution, which is a book transfer and not a clearing payment")

	// ErrPaymentNotFound is returned when a payment ID does not match any
	// payment in the system.
	ErrPaymentNotFound = errors.New("payment not found")

	// ErrSchemeNotFound is returned when a payment references a scheme that
	// has not been registered with the system.
	ErrSchemeNotFound = errors.New("payment scheme not registered")

	// ErrMandateNotFound is returned when a direct debit references a
	// mandate that does not exist.
	ErrMandateNotFound = errors.New("mandate not found")

	// ErrMandateRequired is returned when a pull scheme (e.g. direct debit)
	// is used without supplying a mandate.
	ErrMandateRequired = errors.New("scheme requires a mandate")

	// ErrMandateRevoked is returned when a direct debit references a mandate
	// that has been revoked.
	ErrMandateRevoked = errors.New("mandate has been revoked")

	// ErrMandateMismatch is returned when the mandate's debtor or creditor
	// does not match the payment's debtor or creditor.
	ErrMandateMismatch = errors.New("mandate does not match payment parties")

	// ErrMandateExceeded is returned when a direct debit amount exceeds the
	// mandate's maximum permitted amount.
	ErrMandateExceeded = errors.New("payment exceeds mandate maximum amount")

	// ErrInvalidPaymentAmount is returned when a payment amount is not
	// positive.
	ErrInvalidPaymentAmount = errors.New("payment amount must be positive")

	// ErrInvalidStateTransition is returned when an operation is attempted
	// on a payment whose current status does not permit it (for example,
	// returning a payment that has not settled).
	ErrInvalidStateTransition = errors.New("invalid payment state transition")

	// ErrCycleNotOpen is returned when a payment is submitted but no
	// clearing cycle is open for its scheme, or when an operation requires
	// an open cycle that is not open.
	ErrCycleNotOpen = errors.New("clearing cycle is not open")

	// ErrCycleNotClosed is settlement attempted on a cycle whose cut-off has not
	// been reached.
	ErrCycleNotClosed = errors.New("clearing cycle is not closed")

	// ErrCycleAlreadySettled is the SETTLEMENT AGENT refusing to discharge a
	// cut-off it has already discharged.
	ErrCycleAlreadySettled = errors.New("clearing cycle has already settled")

	// ErrCycleNotReleasable is the CLEARING HOUSE refusing to settle a cut-off
	// whose instructions it could not afterwards hand to the banks that have to
	// apply them.
	ErrCycleNotReleasable = errors.New("clearing cycle cannot be released to its receiving banks")

	// ErrHeldReturnNotFound is the clearing house holding no return for a payment
	// an answer names.
	ErrHeldReturnNotFound = errors.New("payment: the clearing house holds no return for this payment")

	// ErrInvalidSettlement is a settlement instruction this agent cannot read as
	// one batch: no legs, legs referencing different cycles or assets, no single
	// settlement agent between them, or one member named twice.
	ErrInvalidSettlement = errors.New("settlement instruction cannot be read as one batch")

	// ErrCycleAlreadyOpen is returned when opening a cycle for a scheme that
	// already has one open.
	ErrCycleAlreadyOpen = errors.New("clearing cycle already open for scheme")

	// ErrCycleNotFound is returned when a cycle ID does not match any cycle.
	ErrCycleNotFound = errors.New("clearing cycle not found")

	// ErrSettlementNotFound is returned when a settlement ID does not match
	// any settlement.
	ErrSettlementNotFound = errors.New("settlement not found")

	// ErrSettlementAdviceNotFound is a cut-off this bank was never told about.
	ErrSettlementAdviceNotFound = errors.New("settlement advice not found")

	// ErrNotThisInstitutionsAct is one institution's act reached through another's
	// Network.
	ErrNotThisInstitutionsAct = errors.New("payment: this act belongs to another institution")

	// ErrNotThisBanksMandate is a bank asked to record a mandate whose creditor
	// banks somewhere else.
	ErrNotThisBanksMandate = errors.New("payment: this mandate's creditor banks somewhere else")

	// ErrNotThisBanksPayment is a bank asked to post a leg for a payment whose
	// party is somebody else's customer.
	ErrNotThisBanksPayment = errors.New("payment: this bank is not the party whose leg this is")

	// ErrStatementNotForThisBank is a statement about an account this bank does
	// not hold.
	ErrStatementNotForThisBank = errors.New("payment: this statement is about an account this bank does not hold")

	// ErrBICAlreadyAdmitted is an acknowledgement arriving on a BIC the clearing
	// house already routes to, under a DIFFERENT admission.
	ErrBICAlreadyAdmitted = errors.New("payment: this BIC is already admitted under another admission")

	// ErrBankCodeTaken is a bank code already allocated to a DIFFERENT
	// institution.
	ErrBankCodeTaken = errors.New("payment: this bank code is already allocated to another institution")

	// ErrBankCodeReplaced is an acknowledgement that would move a bank's address
	// range: to another country, or to another code in the one it applied to.
	ErrBankCodeReplaced = errors.New("payment: this acknowledgement would move the bank's address range")

	// ErrBankAlreadyAdmitted is a bank recording an acknowledgement that belongs
	// to an admission other than the one it recorded a membership under.
	ErrBankAlreadyAdmitted = errors.New("payment: this bank recorded its membership under another admission")

	// ErrAdmissionNotIdentified is an acknowledgement quoting no admission.
	ErrAdmissionNotIdentified = errors.New("payment: this acknowledgement quotes no admission")

	// ErrAdmittedAccountUnusable is an acknowledgement whose accounts neither act
	// can file: NO account at all, one naming no asset, or an asset naming no
	// account.
	ErrAdmittedAccountUnusable = errors.New("payment: this acknowledgement does not name a usable account")

	// ErrSettlementAccountReplaced is an acknowledgement quoting a DIFFERENT
	// settlement account for an asset the bank has already recorded one in.
	ErrSettlementAccountReplaced = errors.New("payment: this acknowledgement moves a settlement account this bank already holds")

	// ErrNotThisBanksAdmission is an acknowledgement addressed to another bank's
	// BIC, refused by the bank asked to record it.
	ErrNotThisBanksAdmission = errors.New("payment: this admission names a bank other than the one handling it")

	// ErrSchemeUnsupportedReturn is returned when a return is attempted on a
	// payment whose scheme does not support returns.
	ErrSchemeUnsupportedReturn = errors.New("scheme does not support returns")

	// ErrReturnAlreadySettled is a return instruction the settlement agent has
	// already acted on.
	ErrReturnAlreadySettled = errors.New("payment: this return has already been settled")

	// ErrNotAPartyToThisReturn is a bank asked to post a return leg for a payment
	// it is neither side of.
	ErrNotAPartyToThisReturn = errors.New("payment: this bank is neither side of this return")

	// ErrAccountNotInParticipant is returned when a party references an
	// account that does not exist in that participant's ledger.
	ErrAccountNotInParticipant = errors.New("account does not belong to participant")

	// ErrDuplicateEndToEndID is returned when a payment is submitted with an
	// end-to-end id that has already been used.
	ErrDuplicateEndToEndID = errors.New("end-to-end id already used")

	// ErrParticipantAssetNotFound is returned when a participant does not
	// operate in an asset it is being asked to settle in.
	ErrParticipantAssetNotFound = errors.New("participant does not hold accounts in this asset")

	// ErrAssetMismatch is a payment whose debtor or creditor account is not
	// denominated in the scheme's asset.
	ErrAssetMismatch = errors.New("payment accounts are not denominated in the scheme's asset")

	// ErrUnaddressableAccount is returned when a party's account carries no
	// identifier in the scheme's identifier scheme — an account with no IBAN
	// cannot be a leg of a SEPA payment.
	ErrUnaddressableAccount = errors.New("account has no identifier in the scheme's addressing scheme")

	// ErrIdentifierMismatch is a party quoting an identifier that is not one of
	// the named account's addresses in the scheme's addressing scheme.
	ErrIdentifierMismatch = errors.New("quoted identifier is not one of the account's addresses in the scheme's addressing scheme")

	// ErrAmbiguousAddress is a party quoting NO address whose account holds more
	// than one identifier in the scheme's addressing scheme, so there is nothing
	// to back-fill without choosing.
	ErrAmbiguousAddress = errors.New("account holds several identifiers in the scheme's addressing scheme; the payment must quote one")

	// ErrCounterpartyNotNamed is a submission that did not say who the other side
	// is.
	ErrCounterpartyNotNamed = errors.New("payment: the instruction does not name the counterparty")

	// ErrCounterpartyAgentNotNamed is an address THIS system has no directory for,
	// on an instruction that named no BIC beside it — or named something that is
	// not one.
	ErrCounterpartyAgentNotNamed = errors.New("payment: the instruction does not name a usable BIC for the counterparty's bank")
)
