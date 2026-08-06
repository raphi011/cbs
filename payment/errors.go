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

	// ErrBankNotAdmitted is a clearing payment one of whose two banks the
	// clearing house does not route to.
	//
	// It is ErrRosterEntryNotFound turned into a JUDGEMENT. That one is a lookup
	// coming back empty and says nothing about whose fault it is; this one is the
	// clearing house declining to carry an instruction, and the difference is
	// that it is asked deliberately, of BOTH parties, at two points that both
	// exist for it.
	//
	// # What it stops, measured on both directions
	//
	// A bank that is founded and not yet admitted has a mesh actor from the
	// moment Mesh.Admit reserves its address, so before this sentinel existed it
	// was addressable in both directions and neither direction was refused:
	//
	//   - PAYING. Mesh.Submit accepted the submission, SubmitPaymentTx posted the
	//     debtor leg, the payee's bank accepted, and AcceptAtCSMTx took the
	//     payment into the cycle. The customer's account went to -250,000 against
	//     an arranged overdraft and the clearing suspense to +250,000.
	//   - BEING PAID. POST /payments to a founded bank answered 202 and the
	//     payment reached Cleared.
	//
	// Both ended at the same place: csm.settlementLegs turns a net position into
	// a BIC through the roster, cannot name a non-member, and the whole pacs.009
	// fails — so the cycle stays Closed with EVERY OTHER MEMBER's payments in it,
	// their payees unpaid and their payers' money in suspense. POST
	// /cycles/{id}/settle fails identically and rejecting the offending payment
	// is refused as an invalid state transition. Admitting the bank was the only
	// exit.
	//
	// # Why it is refused twice and what each refusal is for
	//
	// Mesh.Submit refuses it at the door, where mesh.ErrOnUsPayment is refused
	// and for that refusal's stated reason: Submit is synchronous, so a guard
	// placed any later has a committed debtor leg to unwind rather than an
	// instruction to decline. api answers 422 and the payer is told before any
	// money has moved.
	//
	// AcceptAtCSMTx refuses it again, and that one is not belt and braces. It is
	// the CLEARING HOUSE making the judgement from its own row, and it is what
	// protects the cycle from a payment that reached the acts by another route —
	// payment.Network's halves are separately callable and seed/seed.go composes
	// them directly, which is the same argument checkAcknowledgement makes about
	// the admission acts one flow over. Under Task 18's stores it is also the
	// only one of the two whose read is the clearing house's own database.
	//
	// # It is answered on the wire, and only in one direction
	//
	// Reaching the clearing house's refusal at all means the payment is in the
	// mesh, and csm.clear turns a refusal to clear into the RJCT the submitting
	// bank is sent. That answer arrives when it is the PAYEE's bank that is not
	// admitted, and its submitter reverses the debtor leg and refunds its
	// customer. It does not arrive when the submitter is itself the non-member:
	// csm.tell addresses the submitter through the roster too, so the pacs.002
	// dead-letters with "cannot address the bank that submitted". Measured, with
	// the door guard removed. That asymmetry is precisely why the door guard is
	// the one that carries the paying direction.
	//
	// reasonTable gives it RC01 — this repository's own gloss for that code is
	// "the BIC does not identify a reachable participant", which is the whole of
	// what this says.
	ErrBankNotAdmitted = errors.New("payment: this scheme does not clear for one of these two banks")

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

	// ErrBICAlreadyAdmitted is an acknowledgement arriving on a BIC the clearing
	// house already routes to, under a DIFFERENT admission.
	//
	// The qualification is the whole sentinel. Two things legitimately arrive on
	// a BIC that is already in the roster: the same bank's second currency —
	// one acmt.007 names one currency, so a bank clearing two schemes admits
	// twice — and an operator re-driving an admission that failed partway. A
	// refusal keyed on "is this BIC in the roster" would refuse exactly those
	// two and never fire on the case it exists for. Keyed on the admission
	// reference it separates them: same reference, extend the entry; different
	// reference, refuse.
	//
	// It is the clearing house's answer and not the mesh's. The mesh's actor map
	// refuses a taken address too, and that one is a statement about
	// connectivity; this is the statement about membership, made by the
	// institution that owns routing.
	ErrBICAlreadyAdmitted = errors.New("payment: this BIC is already admitted under another admission")

	// ErrBankAlreadyAdmitted is a bank recording an acknowledgement that belongs
	// to an admission other than the one it recorded a membership under.
	//
	// It is ErrBICAlreadyAdmitted one institution over and about a different row,
	// and the pair is deliberate rather than duplication. The clearing house
	// refuses a second INSTITUTION contending for an address, from the roster;
	// this is a BANK refusing a message about itself, from its own memory of what
	// it accepted (Bank.AdmissionRef). They are separate because the rows are
	// separate — Task 18 puts them in different databases — so a guard that
	// existed only at the clearing house would be a guard the split removes.
	//
	// What it stops is measured, not supposed: an acknowledgement naming a
	// member's own BIC and quoting an admission it never heard of moved that
	// bank's settlement reference onto an invented account, leaving its row
	// disagreeing with the settlement agent's about which account it holds.
	//
	// A bank that has recorded NO membership refuses nothing, which is what lets
	// an operator re-drive an interrupted admission under a new process id.
	ErrBankAlreadyAdmitted = errors.New("payment: this bank recorded its membership under another admission")

	// ErrAdmissionNotIdentified is an acknowledgement quoting no admission.
	//
	// Refs/PrcId is the conversation's only correlator, and on this side of the
	// wire "" is not a value but a SENTINEL: an empty Bank.AdmissionRef means
	// "this bank has accepted nothing yet", and an empty RosterEntry.AdmissionRef
	// would compare equal to any other empty one. So an acknowledgement carrying
	// none defeats both admission guards at once — measured, on the acts:
	//
	//   - the bank records a membership with no reference, and the NEXT
	//     acknowledgement, from any admission at all, moves its settlement
	//     reference. That is the overwrite ErrBankAlreadyAdmitted exists to stop,
	//     reopened through the guard's own empty case.
	//   - two institutions on one BIC both quoting "" compare equal, so
	//     AdmitMemberTx extends the first's entry instead of refusing the second.
	//
	// ReadAdmissionAcknowledgement refuses it on the way in from the wire and
	// checkAcknowledgement refuses it in the acts, which are separately callable.
	ErrAdmissionNotIdentified = errors.New("payment: this acknowledgement quotes no admission")

	// ErrAdmittedAccountUnusable is an acknowledgement whose accounts neither act
	// can file: NO account at all, one naming no asset, or an asset naming no
	// account.
	//
	// The asset is what decides which of a bank's internal account sets a
	// settlement reference belongs to and which schemes a member clears in, so an
	// account with none would be filed under the empty asset by both readers — a
	// reserve nothing settles through and a reference nothing quotes. An account
	// identifier that is empty is the same hole from the other end: a settlement
	// reference nothing can post to.
	//
	// The EMPTY LIST is the third arm and the one that cost most, because it is
	// the value that makes the other two not apply: a loop over no accounts
	// refuses nothing. Recorded, it wedges both institutions — a Member that
	// settles through no account, a roster entry that clears in no scheme, and
	// the true acknowledgement then refused for ever by the admission-reference
	// guards those two rows now carry.
	//
	// ReadAdmissionAcknowledgement refuses all three on the way in from the wire.
	// These are the same refusals in the acts, so that the reader's are defence
	// in depth rather than the only line — the rule Task 16e arrived at for
	// ReadReturn and SettleReturnTx after an implementer found the hole outside
	// its brief. See checkAcknowledgement, which sets the two lists side by side.
	//
	// # A fourth arm, which only the BANK can decide
	//
	// An acknowledgement naming accounts only in assets the bank operates in none
	// of is the empty list again, reached with a non-empty one: every account is
	// skipped, nothing is filed, and the bank would become a Member settling
	// through nothing with its AdmissionRef spent. RecordMembershipTx refuses it
	// and checkAcknowledgement cannot, because the same message is perfectly
	// usable to the CLEARING HOUSE — that institution records what the servicer
	// opened and never asks the bank what it holds.
	ErrAdmittedAccountUnusable = errors.New("payment: this acknowledgement does not name a usable account")

	// ErrSettlementAccountReplaced is an acknowledgement quoting a DIFFERENT
	// settlement account for an asset the bank has already recorded one in.
	//
	// It is the hole ErrBankAlreadyAdmitted's own guard left open, and it is the
	// fourth instance on this branch of one shape: a guard closes a case and its
	// own "does not apply" value stays reachable. That one refuses an
	// acknowledgement quoting ANOTHER admission; on the admission's OWN
	// reference it refuses nothing, and the loop below it wrote whatever arrived.
	//
	// Measured, on a healthy member: a second acknowledgement echoing the
	// admission the bank itself accepted, carrying an invented account, moved the
	// bank's euro settlement reference onto it — permanently. Afterwards
	// DepositTx answered a bare "account not found" (the bank quotes its own row
	// for the central bank's account), the operator console's ReserveBalance went
	// on reporting the healthy reserve (it reads the settlement agent's row,
	// which never moved), and re-driving the admission was refused with "already
	// a member of this scheme". Every door out was shut and each one said
	// something different.
	//
	// What it does NOT refuse is a redelivery or a second asset, and that is the
	// whole of why it compares rather than forbids: an acmt.010 lists every
	// account the servicer holds for the address, so the second currency's
	// acknowledgement repeats the first's account and the same message redelivered
	// repeats all of them. Equal is an extension; different is a claim about an
	// account this bank's settlement agent has never moved it to.
	//
	// The bank is the last hop of an admission and has nobody to tell, so this
	// becomes a dead letter like every other refusal it makes — see reasonTable,
	// where the admission block sets that out.
	ErrSettlementAccountReplaced = errors.New("payment: this acknowledgement moves a settlement account this bank already holds")

	// ErrNotThisBanksAdmission is an admission message whose party is not the
	// institution handling it, and it is made at BOTH ends of the conversation.
	//
	// The bank makes it about an ACKNOWLEDGEMENT addressed to another bank's BIC.
	// It is ErrStatementNotForThisBank one flow over, and for the same reason:
	// the actor passes its OWN id alongside a message it did not address, so
	// nothing in the signature stops a caller naming somebody else's. A bank
	// that recorded whatever arrived would write another member's settlement
	// account numbers onto its own row, and every reserve movement it made
	// afterwards would name an account it does not hold.
	//
	// The clearing house makes it about a REQUEST whose applicant is not its
	// sender (mesh.csm.relayAdmission). Same sentence, opposite direction, and it
	// is the only refusal on that path answered to somebody other than the
	// applicant — the sender is who asked, and the address it named never did.
	// Relayed instead, it would have the settlement agent open an account for an
	// address on the word of an institution that does not hold it, and an account
	// servicer asked about one BIC has no way to tell who asked.
	ErrNotThisBanksAdmission = errors.New("payment: this admission names a bank other than the one handling it")

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
