package payment

import "errors"

// Sentinel errors returned by the Network. Callers can use errors.Is() to
// check for specific failure conditions, the same convention the ledger
// package uses.
var (
	// ErrParticipantNotFound is returned when a participant ID does not
	// match any bank registered with the system.
	ErrParticipantNotFound = errors.New("participant not found")

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

	// ErrCycleNotClosed is returned when settlement is attempted on a cycle
	// that has not been closed (its cut-off has not been reached).
	ErrCycleNotClosed = errors.New("clearing cycle is not closed")

	// ErrCycleAlreadyOpen is returned when opening a cycle for a scheme that
	// already has one open.
	ErrCycleAlreadyOpen = errors.New("clearing cycle already open for scheme")

	// ErrCycleNotFound is returned when a cycle ID does not match any cycle.
	ErrCycleNotFound = errors.New("clearing cycle not found")

	// ErrSettlementNotFound is returned when a settlement ID does not match
	// any settlement.
	ErrSettlementNotFound = errors.New("settlement not found")

	// ErrSchemeUnsupportedReturn is returned when a return is attempted on a
	// payment whose scheme does not support returns.
	ErrSchemeUnsupportedReturn = errors.New("scheme does not support returns")

	// ErrAccountNotInParticipant is returned when a party references an
	// account that does not exist in that participant's ledger.
	ErrAccountNotInParticipant = errors.New("account does not belong to participant")

	// ErrDuplicateEndToEndID is returned when a payment is submitted with an
	// end-to-end id that has already been used.
	ErrDuplicateEndToEndID = errors.New("end-to-end id already used")

	// ErrParticipantAssetNotFound is returned when a participant does not
	// operate in an asset it is being asked to settle in.
	ErrParticipantAssetNotFound = errors.New("participant does not hold accounts in this asset")

	// ErrAssetMismatch is returned when a payment's debtor or creditor
	// account is not denominated in the scheme's asset.
	//
	// What the ledger can and cannot catch here is worth being precise about.
	//
	// It cannot catch it at INITIATION. A payment is never one posting: the
	// debtor leg is a transaction in the payer's bank's book, the creditor leg
	// a separate transaction in the payee's, written later at settlement. The
	// debtor leg on its own —
	//
	//	Debit  Alice EUR      3000
	//	Credit Suspense EUR   3000
	//
	// — is impeccable double-entry within one asset, and nothing in that
	// posting contains the claim that some posting in another bank's book is
	// its other half. Only the payment layer holds both ends at once.
	//
	// It does catch it at SETTLEMENT. SettleCycleTx resolves the creditor's
	// suspense account with creditor.AccountsFor(scheme.Asset()), so the
	// creditor leg comes out as a EUR suspense debit against a BTC credit and
	// validateBalance refuses it with ledger.ErrUnbalancedAsset.
	//
	// That is a bad place to find out. Settlement is all-or-nothing, so one
	// mismatched payment fails the entire clearing cycle, and the error names
	// an unbalanced asset rather than the payment that caused it. This
	// sentinel is what turns a late, batch-wide, misattributed failure into an
	// immediate, correctly attributed one.
	//
	// TestCrossAssetPaymentSurvivesInitiationAndFailsTheWholeCycle in
	// system_test.go pins both halves of that.
	ErrAssetMismatch = errors.New("payment accounts are not denominated in the scheme's asset")

	// ErrUnaddressableAccount is returned when a party's account carries no
	// identifier in the scheme's identifier scheme — an account with no IBAN
	// cannot be a leg of a SEPA payment.
	ErrUnaddressableAccount = errors.New("account has no identifier in the scheme's addressing scheme")

	// ErrIdentifierMismatch is returned when a party quotes an identifier that
	// is not one of the named account's addresses in the scheme's addressing
	// scheme. The ids route the payment and the identifier records the address
	// used; the two disagreeing means one of them is wrong, and the system does
	// not get to choose which.
	//
	// It covers two shapes, because they are one question asked once: the
	// account does not hold the quoted address at all, and the account holds it
	// but under a different identifier scheme than the payment scheme routes on
	// — a card PAN quoted for a SEPA credit transfer. Both mean "that is not
	// this account's address for this scheme".
	ErrIdentifierMismatch = errors.New("quoted identifier is not one of the account's addresses in the scheme's addressing scheme")

	// ErrAmbiguousAddress is returned when a party quotes NO address and its
	// account holds more than one identifier in the scheme's addressing scheme,
	// so there is nothing to back-fill without choosing.
	//
	// Initiation fills in the address when there is exactly one candidate, which
	// is what makes "a payment records the address it was sent to" true for
	// every payment rather than only for the ones whose caller volunteered it.
	// Two candidates is the same situation as an ambiguous resolution and gets
	// the same answer: a refusal, not the first one in the set. Picking would
	// write a real, checkable address onto a settled payment on the strength of
	// slice order, and a customer reading their statement would see an IBAN
	// nobody quoted.
	ErrAmbiguousAddress = errors.New("account holds several identifiers in the scheme's addressing scheme; the payment must quote one")

	// ErrCounterpartyNotNamed is a submission that did not say who the other side
	// is. The instruction must carry the counterparty's agent and name because the
	// message it becomes must, and because this bank cannot look either up — the
	// account is at another bank, in a register it may not read.
	ErrCounterpartyNotNamed = errors.New("payment: the instruction does not name the counterparty")
)
