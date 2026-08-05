package payment

import "errors"

// Sentinel errors returned by the Network. Callers can use errors.Is() to
// check for specific failure conditions, the same convention the ledger
// package uses.
var (
	// ErrParticipantNotFound is returned when a participant ID does not
	// match any bank registered with the system.
	//
	// It keeps its name for the reason ParticipantID keeps its: the id is the
	// thing not found, and renaming the sentinel would ripple through the API's
	// status mapping and every caller comparing against it without changing
	// what is being said.
	ErrParticipantNotFound = errors.New("participant not found")

	// ErrSettlementMemberNotFound is a BIC the settlement agent holds no
	// account for.
	//
	// It is separate from ErrParticipantNotFound because the two are different
	// institutions' answers to different questions. That one means the network
	// has no bank under this id. This one means the CENTRAL BANK has never
	// opened an account for this address — which is the true state of a bank
	// that is founded and not yet admitted, and the answer a settlement agent
	// with its own store gives when it is asked to settle for a stranger.
	ErrSettlementMemberNotFound = errors.New("payment: the settlement agent holds no account for this BIC")

	// ErrRosterEntryNotFound is a BIC the clearing house does not route to.
	//
	// Separate from the two above for the reason they are separate from each
	// other: it is a third institution's answer, and what it means is that this
	// address is not in the scheme — not that no such bank exists, and not that
	// it holds no settlement account.
	ErrRosterEntryNotFound = errors.New("payment: no member is routed to under this BIC")

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

	// ErrSettlementAdviceNotFound is a cut-off this bank was never told about.
	ErrSettlementAdviceNotFound = errors.New("settlement advice not found")

	// ErrNotThisBanksPayment is a bank asked to post a leg for a payment whose
	// party is somebody else's customer.
	//
	// It is the direction rule said about a settlement advice. On a push the
	// clearing house tells BOTH banks a payment settled — the payer's bank
	// because it has been waiting for the answer to its instruction, the payee's
	// bank because it has a leg to post — and only one of them may post. A
	// system that let either post would credit the payee twice or credit them in
	// the wrong bank's book.
	ErrNotThisBanksPayment = errors.New("payment: this bank is not the party whose leg this is")

	// ErrStatementNotForThisBank is a statement about an account this bank does
	// not hold.
	//
	// A member checks the account the statement names against its OWN reserve
	// account before booking anything from it. A bank that booked whatever
	// arrived would move its reserve mirror on another member's position — and
	// under isolation a misrouted statement is exactly the failure that has no
	// second reader to catch it.
	ErrStatementNotForThisBank = errors.New("payment: this statement is about an account this bank does not hold")

	// ErrSchemeUnsupportedReturn is returned when a return is attempted on a
	// payment whose scheme does not support returns.
	ErrSchemeUnsupportedReturn = errors.New("scheme does not support returns")

	// ErrReturnAlreadySettled is a return instruction the settlement agent has
	// already acted on.
	//
	// It is a redelivery, and it is detected in the ledger rather than in a row
	// of its own: the reserve reversal carries "<payment>:return-settle" as its
	// idempotency key, and that key is the only record the settlement agent has
	// that it settled this return. It holds no payment rows — that is the whole
	// point of SettleReturnTx — so there is nowhere else it could have written
	// one. SettleReturnTx reads the key before it posts, so that the answer is
	// this rather than a funding refusal, and the ledger refuses the posting on
	// the same key, so that two deliveries in flight at once cannot both pass
	// the read.
	//
	// It is a statement about THIS system's state and not about the sender's
	// message, which is the same discrimination ErrInvalidStateTransition
	// carries on the cut-off path: a caller answering a counterparty with it
	// would report a return that in fact happened as rejected. Dead-letter it.
	ErrReturnAlreadySettled = errors.New("payment: this return has already been settled")

	// ErrNotAPartyToThisReturn is a bank asked to post a return leg for a
	// payment it is neither side of.
	//
	// A return has exactly two customer legs and each belongs to one bank: the
	// clawback at the creditor's, the refund at the debtor's. Which one a bank
	// posts follows from which side it is on, so a bank on neither side has no
	// leg to post at all — and neither the caller nor the message gets to say
	// which leg is which. It is ErrNotThisBanksPayment's counterpart on the
	// return path, and separate from it because that one names the ONE bank a
	// creditor leg belongs to, where this names a bank that is not either of
	// two.
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
	// It does catch it at the CREDITOR LEG. PostCreditorLegTx resolves the
	// creditor's suspense account with creditor.AccountsFor(scheme.Asset()), so
	// the leg comes out as a EUR suspense debit against a BTC credit and
	// validateBalance refuses it with ledger.ErrUnbalancedAsset.
	//
	// That is still a bad place to find out, though less bad than it was. Until
	// Task 15b.3 the leg was posted inside the settlement agent's unit of work,
	// so one mismatched payment failed the entire clearing cycle; it is the
	// payee's bank's own act now, so the cut-off settles and this one payment
	// stays Cleared. What has not improved is the error: it names an unbalanced
	// asset rather than the payment that caused it, and it arrives long after
	// the payer was debited. This sentinel is what turns a late, misattributed
	// failure into an immediate, correctly attributed one.
	//
	// TestCrossAssetPaymentSurvivesInitiationAndFailsAtThePayeesBank in
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
	// is. The instruction must carry the counterparty's NAME because the message
	// it becomes must, and because submission looks it up nowhere: the account is
	// at another bank, and nothing on the path that builds a payment reads
	// another bank's register. (GET /directory can resolve a name before any
	// instruction exists — see PartyDetails — but its answer is never wired into
	// the payment.)
	//
	// The counterparty's AGENT is not part of this refusal, and used to be. It is
	// derived from the roster rather than asserted — see PartyDetails.Agent — so
	// there is nothing for a caller to omit, and a name-only guard is the whole
	// of what "the instruction does not name the counterparty" can now mean. A
	// counterparty at a participant that does not exist is ErrParticipantNotFound
	// instead, which is the accurate statement: the instruction named a bank,
	// and no such bank is a member.
	ErrCounterpartyNotNamed = errors.New("payment: the instruction does not name the counterparty")
)
