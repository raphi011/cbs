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

	// ErrBankCodeNotAllocated is a (country, code) the settlement agent's
	// registry has no row for.
	//
	// It is the ISSUER's answer and it means the code was never given out. That
	// is not the same statement as a bank's own directory failing to resolve one
	// — see ErrBankCodeUnknown, which is a subscriber's copy answering about
	// itself and cannot tell "never allocated" from "allocated since I last
	// refreshed". This one CAN tell, because the registry is where an allocation
	// comes into existence.
	ErrBankCodeNotAllocated = errors.New("payment: no bank code has been allocated under this country and code")

	// ErrBankCodeUnknown is an address whose bank code resolves to nothing in the
	// routing directory THIS bank holds.
	//
	// # It cannot say which of two situations it is in, and that is the design
	//
	// Either no such bank is in this scheme, or one is and this bank's copy of
	// the directory predates it. Those have different remedies — give up, or
	// refresh — and the refusing bank has no way to tell them apart, because
	// telling them apart would mean asking the clearing house per payment, which
	// is the lookup this whole design replaces with a subscription. A refusal
	// that named one of the two would be claiming to know something a subscriber
	// structurally does not.
	//
	// Contrast ErrBankCodeNotAllocated, which is the ISSUER's answer to the same
	// shape of question and CAN tell: the registry is where an allocation comes
	// into existence, so a miss there means the code was never given out. Two
	// sentinels because two institutions are answering, not because one lookup
	// failed twice.
	//
	// What makes the refusal safe rather than merely unhelpful is that a code is
	// never reassigned, so a copy that is behind is incomplete and never wrong.
	// A payer is refused; a payer is never paid to the wrong bank.
	ErrBankCodeUnknown = errors.New("payment: this bank's routing directory holds no entry for this bank code")

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
	// A bank that is founded and not yet admitted can still have a mesh actor —
	// giving one is a separate act from any of the four, and nothing orders the
	// two — so without this sentinel it is addressable in both directions and
	// neither direction is refused:
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
	// Mesh.Submit refuses it at the door, where ErrOnUsPayment is refused
	// and for that refusal's stated reason: Submit is synchronous, so a guard
	// placed any later has a committed debtor leg to unwind rather than an
	// instruction to decline. api answers 422 and the payer is told before any
	// money has moved.
	//
	// AcceptAtCSMTx refuses it again, and that one is not belt and braces. It is
	// the CLEARING HOUSE making the judgement from its own row, and it is what
	// protects the cycle from a payment that reached the acts by another route —
	// payment.Network's halves are separately callable and seed/seed.go composes
	// them directly. It is also the only one of the two whose read is the clearing
	// house's own database.
	//
	// # It is answered on the wire, and only in one direction
	//
	// Reaching the clearing house's refusal at all means the payment is in a file
	// that institution has taken in, and it turns a refusal to clear into the
	// RJCT the submitting bank is sent. That answer arrives when it is the PAYEE's bank that is not
	// admitted, and its submitter reverses the debtor leg and refunds its
	// customer. It does not arrive when the submitter is itself the non-member:
	// the clearing house addresses the submitter through the roster too, so the
	// pacs.002 dead-letters with "cannot address the bank that submitted". Measured, with
	// the door guard removed. That asymmetry is precisely why the door guard is
	// the one that carries the paying direction.
	//
	// # Nothing can reach it today, and it stays
	//
	// Which banks a deployment has is decided before the process starts and every
	// one of them is provisioned in full, so no submission can name a bank the
	// clearing house does not route to. That is a claim about today's CALLERS and
	// not an invariant — the halves are separately callable, which is the point
	// above — and what it would let through is the settlement measured above.
	// payment/recon's partiesAreMembers asks of the books what these two guards
	// ask of a submission, so the claim is checked rather than asserted.
	//
	// reasonTable gives it RC01 — this repository's own gloss for that code is
	// "the BIC does not identify a reachable participant", which is the whole of
	// what this says.
	ErrBankNotAdmitted = errors.New("payment: this scheme does not clear for one of these two banks")

	// ErrOnUsPayment is a submission whose payer and payee bank at the SAME
	// institution.
	//
	// It is a statement about the ROUTE and not about the payment. Two customers
	// of one bank paying each other is an ordinary thing to want; what it is not
	// is a CLEARING payment. Nothing leaves the bank, so there is no interbank
	// obligation for a clearing house to net, no reserves for a settlement agent
	// to move, and no camt.053 that could tell a bank about a book it already
	// holds. A real bank recognises the beneficiary as its own and books the
	// transfer between two of its own deposit accounts; it never reaches a scheme
	// at all.
	//
	// Submitted to clearing anyway, it produced three separate wrong answers,
	// each in a different institution — a cycle that settled nothing and stranded
	// at Cleared, a reserve mirror moved by an amount the central bank's own
	// record did not move, and a returning bank refusing its own customer's
	// unconditional refund because it was the returner on both legs. It is
	// refused at the door every submission comes through, and PostReturnLegTx
	// states the return's rule so that it does not depend on this refusal
	// holding.
	//
	// A sentinel and not just a message because the layer above has a remedy for
	// it: api answers 422 and the caller asks its bank for a book transfer
	// instead, which is deposit.Register.TransferTx and, over HTTP, POST
	// /transfers on that bank's own port. So this is a signpost rather than a
	// dead end — the same address, on the route that carries it.
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

	// ErrCycleNotClosed is returned when settlement is attempted on a cycle
	// that has not been closed (its cut-off has not been reached).
	//
	// It is the CLEARING HOUSE's refusal and only its own: that institution holds
	// the cycle and knows whether the cut-off has been reached. The settlement
	// agent has no cycles table — see ErrCycleAlreadySettled, which is what it
	// refuses a redelivery with instead.
	ErrCycleNotClosed = errors.New("clearing cycle is not closed")

	// ErrCycleAlreadySettled is the SETTLEMENT AGENT refusing to discharge a
	// cut-off it has already discharged.
	//
	// A redelivered pacs.009 cannot be caught by the cycle's status, which is a row
	// this institution cannot read. What it can read is its own settlement
	// register, and a settlement against this cycle is its own record of having
	// done the work.
	//
	// Like ErrCycleNotClosed it is classified with the EMPTY code in reasonTable:
	// it describes this system's own state rather than a judgement about the
	// sender's message, so the mesh dead-letters it instead of answering a
	// clearing house that its settled cycle was rejected.
	ErrCycleAlreadySettled = errors.New("clearing cycle has already settled")

	// ErrInvalidSettlement is a settlement instruction this agent cannot read as
	// one batch: no legs, legs referencing different cycles or assets, no single
	// settlement agent between them, or one member named twice.
	//
	// It exists because the agent works from the INSTRUCTION now rather than from
	// the cycle it names — see positionsIn. Under the shared store the positions
	// were read off the clearing house's own row and could not be malformed; a
	// message can be, and a batch that does not sum to zero moves money nobody
	// computed.
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

	// ErrNotThisInstitutionsAct is one institution's act reached through
	// another's Network.
	//
	// It is a different class from every other refusal in this file. The rest are
	// about the SUBJECT of an act — a payment this bank is not a party to, a
	// statement about another member's reserve account, an admission addressed to
	// somebody else — and each is decided by comparing the act's subject against
	// the bank performing it. This one is about the PERFORMER: a member bank's act
	// on the clearing house's network, or the settlement agent's on a bank's, where
	// there is no subject to compare against because there is no member and no book
	// to be about.
	//
	// The two are not redundant. Every subject guard still fires, and still has to:
	// two member banks are both members, so nothing here can tell one from the
	// other, and "this payment's creditor banks elsewhere" is the only thing that
	// ever could. What this adds is the case a subject guard cannot see.
	//
	// See payment.Identity, Network.self and Network.centralBankBook.
	ErrNotThisInstitutionsAct = errors.New("payment: this act belongs to another institution")

	// ErrNotThisBanksMandate is a bank asked to record a mandate whose creditor
	// banks somewhere else.
	//
	// In SEPA the CREDITOR holds the mandate — SDD.ValidateMandate says so and
	// it is the creditor's bank that checks one at submission — so the row
	// belongs to that bank and to no other. A bank that recorded whatever it was
	// handed would hold an authorisation over an account at a third bank, on
	// behalf of a customer that is not its own, and the validator that reads it
	// back would be reading another institution's record.
	//
	// It is ErrNotThisBanksPayment's shape one flow over, with one difference
	// worth naming: that one decides between two banks that are both parties to
	// the payment, and only one may post. This one is not about a party at all.
	// The DEBTOR is a party and is not this bank's customer either — it is
	// recorded from what the creditor said, as a payment's counterparty is.
	ErrNotThisBanksMandate = errors.New("payment: this mandate's creditor banks somewhere else")

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
	// one request names one currency, so a bank clearing two schemes admits
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

	// ErrBankCodeTaken is a bank code already allocated to a DIFFERENT
	// institution.
	//
	// Two banks issuing addresses under one code is the defect the whole routing
	// directory would sit on top of: every address either of them minted would
	// resolve to whichever bank the reader's copy named last, and a payer could
	// not tell, and neither could a receiving bank. So it is refused twice, in
	// two databases, by two institutions.
	//
	// The settlement agent refuses it at ALLOCATION, which is where it can be
	// refused for good: the registry is keyed by (country, code) and a second
	// allocation of one code cannot be written. The clearing house refuses it
	// again on its own roster, and that one is belt-and-braces that earns its
	// place — the roster is what every member COPIES, so a duplicate there would
	// make one address ambiguous for the whole scheme, and the clearing house
	// cannot see the registry to check.
	//
	// Not to be confused with ErrBICAlreadyAdmitted, which is two institutions
	// contending for one ADDRESS. A bank can hold a BIC nobody else wants and
	// still be handed a code somebody else holds.
	ErrBankCodeTaken = errors.New("payment: this bank code is already allocated to another institution")

	// ErrBankCodeReplaced is an acknowledgement that would move a bank's address
	// range: to another country, or to another code in the one it applied to.
	//
	// A CODE IS NEVER REASSIGNED, and this is the guard that says so from the
	// bank's own end. It is the invariant the whole subscribed-copy design rests
	// on: a member's routing directory can be behind, so it can be INCOMPLETE,
	// and it must never be WRONG — "I cannot route this yet" is a state a payer
	// can be told about and "I routed it to the wrong bank" is not. A bank that
	// accepted a second allocation would leave every address it had already
	// issued pointing at a range it no longer holds, in every copy of the roster
	// in the scheme, with nothing anywhere saying so.
	//
	// It is ErrSettlementAccountReplaced's sibling and reaches further. That one
	// costs the bank its own reserve postings; this one costs every customer
	// their address.
	ErrBankCodeReplaced = errors.New("payment: this acknowledgement would move the bank's address range")

	// ErrBankAlreadyAdmitted is a bank recording an acknowledgement that belongs
	// to an admission other than the one it recorded a membership under.
	//
	// It is ErrBICAlreadyAdmitted one institution over and about a different row.
	// The clearing house refuses a second INSTITUTION contending for an address,
	// from the roster; this is a BANK refusing a message about itself, from its own
	// memory of what it accepted (Bank.AdmissionRef). They are separate because the
	// rows are in different databases, so a guard only at the clearing house would
	// be no guard at all here.
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
	// checkAcknowledgement is what refuses it, once, for both acts.
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
	// refuses nothing. Recorded, it wedges both institutions — an admitted bank
	// that settles through no account, a roster entry that clears in no scheme, and
	// the true acknowledgement then refused for ever by the admission-reference
	// guards those two rows now carry.
	//
	// checkAcknowledgement refuses all three, once, for both acts.
	//
	// # A fourth arm, which only the BANK can decide
	//
	// An acknowledgement naming accounts only in assets the bank operates in none
	// of is the empty list again, reached with a non-empty one: every account is
	// skipped, nothing is filed, and the bank would be left settling through
	// nothing with its AdmissionRef spent. RecordMembershipTx refuses it
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
	// whole of why it compares rather than forbids: an acknowledgement lists every
	// account the servicer holds for the address, so the second currency's answer
	// repeats the first's account and a re-driven admission repeats all of them.
	// Equal is an extension; different is a claim about an account this bank's
	// settlement agent has never moved it to.
	//
	// It reaches no counterparty, because nothing carries an admission between
	// institutions: it goes back to whoever drove the act — see reasonTable, where
	// the admission block sets that out.
	ErrSettlementAccountReplaced = errors.New("payment: this acknowledgement moves a settlement account this bank already holds")

	// ErrNotThisBanksAdmission is an acknowledgement addressed to another bank's
	// BIC, refused by the bank asked to record it.
	//
	// It is ErrStatementNotForThisBank one flow over, and for the same reason: the
	// caller passes a bank's OWN network alongside an acknowledgement it did not
	// address, so nothing in the signature stops it naming somebody else's. A bank
	// that recorded whatever arrived would write another member's settlement
	// account numbers onto its own row, and every reserve movement it made
	// afterwards would name an account it does not hold.
	//
	// What makes it reachable is that nothing in this package composes the four
	// acts: the bank's own act is called with an answer somebody else obtained,
	// and this is what checks the two belong together.
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
	//
	// # It is nearly unreachable, and the reason is worth knowing before anyone
	// deletes it
	//
	// A bank that is neither side of a return holds no ROW for the payment —
	// each institution keeps its own copy and only the parties are ever sent one
	// — so PostReturnLegTx's read fails with ErrPaymentNotFound before this
	// comparison is made. What is left for this sentinel to answer is a payment
	// the bank DOES hold and is not a party to, which needs an instruction
	// naming agents that disagree with the row.
	//
	// That is not a defect and the guard is not redundant. The store is the
	// STRONGER of the two, because it cannot be got wrong by a comparison, and
	// this one states the rule the store enforces by accident of where the rows
	// are. Both stay: a guard that holds only because of how the data happens to
	// be laid out is a guard nobody is keeping.
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
	// That is still a bad place to find out. The leg is the payee's bank's own act,
	// so the cut-off settles and this one payment stays Cleared — but the error
	// names an unbalanced asset rather than the payment that caused it, and it
	// arrives long after the payer was debited. This sentinel is what turns a late,
	// misattributed failure into an immediate, correctly attributed one.
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
	// The NAME is the only thing an instruction still asserts about the other
	// side. The agent beside it is derived from the address (see
	// ErrCounterpartyAgentNotNamed), which is why this one has no sibling for a
	// payer who typed the wrong bank: there is no field to type it in.
	ErrCounterpartyNotNamed = errors.New("payment: the instruction does not name the counterparty")

	// ErrCounterpartyAgentNotNamed is an address THIS system has no directory for,
	// on an instruction that named no BIC beside it — or named something that is
	// not one.
	//
	// ONE sentinel for both, because the remedy is the same and the distinction
	// is not one a caller can act on differently: an instruction that carries no
	// routing element and one that carries an unusable one are equally
	// unsendable, and the message names which it was.
	//
	// # It is the narrow case now, and what is left in it is real
	//
	// An IBAN is DERIVED from — Network.routeTx reads the bank code out of the
	// address and resolves it in this bank's copy of the scheme's routing
	// directory — so a payer types an address and a name and nothing else. That is
	// what SEPA has been since February 2016, and it is possible here for the
	// reason it is possible there: every member subscribes to a published table.
	//
	// What this sentinel covers is everything that is not such an address. A card
	// PAN is issued by a scheme elsewhere and quoted; a proxy alias is resolved by
	// a central service this system does not have — the EPC's Proxy Lookup
	// Service, UPI — precisely because no bank can guarantee an alias is unique;
	// an address in a country nothing here issues in has no structure to read a
	// bank code out of. For all of those the BIC genuinely is the payer's to
	// supply, exactly as it is on a cross-border transfer.
	//
	// The sibling refusal, for an address that DOES have a directory here and
	// resolves to nothing in it, is ErrBankCodeUnknown. Different remedies: supply
	// a BIC, versus refresh or give up.
	//
	// Refused at SUBMISSION rather than at the cut-off, even though the missing
	// element is the message's, because a submission that committed the payer's
	// debit and then failed to render an instruction hours later is a payer short
	// of money against a file that was never built — see instructableTx.
	//
	// What this refusal does NOT claim is that a supplied BIC is right. Nothing
	// here can check one: this bank cannot read the counterparty's register, so a
	// wrong-but-well-formed BIC is delivered to the bank it names and refused
	// THERE, with AC01, by a bank that does not hold the address. See
	// mesh's TestAWrongCounterpartyAgentIsRefusedByTheBankItNames.
	ErrCounterpartyAgentNotNamed = errors.New("payment: the instruction does not name a usable BIC for the counterparty's bank")
)
