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
	//
	// Separate from ErrParticipantNotFound: that one means the network has no bank
	// under this id, this one that the CENTRAL BANK has never opened an account for
	// this address — the true state of a bank founded and not yet admitted.
	ErrSettlementMemberNotFound = errors.New("payment: the settlement agent holds no account for this BIC")

	// ErrBankCodeNotAllocated is a (country, code) the settlement agent's registry
	// has no row for.
	//
	// It is the ISSUER's answer and means the code was never given out. Contrast
	// ErrBankCodeUnknown, a subscriber's copy answering about itself, which cannot
	// tell "never allocated" from "allocated since I last refreshed".
	ErrBankCodeNotAllocated = errors.New("payment: no bank code has been allocated under this country and code")

	// ErrBankCodeUnknown is an address whose bank code resolves to nothing in the
	// routing directory THIS bank holds.
	//
	// It cannot say which of two situations it is in, and that is the design: either
	// no such bank is in this scheme, or one is and this bank's copy predates it.
	// Telling them apart would mean asking the clearing house per payment, which is
	// the lookup the subscription replaces.
	//
	// What makes the refusal safe rather than merely unhelpful is that a code is
	// never reassigned, so a copy that is behind is incomplete and never wrong: a
	// payer is refused, never paid to the wrong bank.
	ErrBankCodeUnknown = errors.New("payment: this bank's routing directory holds no entry for this bank code")

	// ErrRosterEntryNotFound is a BIC the clearing house does not route to. A third
	// institution's answer, and what it means is that this address is not in the
	// scheme — not that no such bank exists, and not that it holds no settlement
	// account.
	ErrRosterEntryNotFound = errors.New("payment: no member is routed to under this BIC")

	// ErrBankNotAdmitted is a clearing payment one of whose two banks the clearing
	// house does not route to.
	//
	// It is ErrRosterEntryNotFound turned into a JUDGEMENT: that one is a lookup
	// coming back empty and says nothing about whose fault it is, this one is the
	// clearing house declining to carry an instruction, asked deliberately of BOTH
	// parties.
	//
	// A bank founded and not yet admitted can still be an EBICS subscriber, so
	// without this it is addressable in both directions and neither is refused —
	// measured, and both directions ended at the same place. The clearing house turns
	// a net position into a BIC through the roster, cannot name a non-member, and the
	// whole pacs.009 fails: the cycle stays Closed with EVERY OTHER MEMBER's payments
	// in it, their payees unpaid and their payers' money in suspense. Rejecting the
	// offending payment is refused as an invalid state transition, so admitting the
	// bank was the only exit.
	//
	// It is refused twice. The submitting bank's door refuses it where ErrOnUsPayment
	// is refused, and for that reason: a submission is handled synchronously, so a
	// guard placed any later has a committed debtor leg to unwind rather than an
	// instruction to decline. AcceptAtCSMTx refuses it again from the CLEARING
	// HOUSE's own row, which is what protects the cycle from a payment that reached
	// the acts by another route — the halves are separately callable and seed
	// composes them directly.
	//
	// It is answered on the wire in ONE direction only. When the PAYEE's bank is not
	// admitted the submitter is sent an RJCT and reverses its debtor leg; when the
	// SUBMITTER is itself the non-member the clearing house addresses it through the
	// roster too, so the pacs.002 has no queue to be put in and becomes a problem in
	// the day's report. That asymmetry is why the door guard carries the paying
	// direction.
	//
	// Nothing can reach it today, every bank a deployment has being provisioned in
	// full — a claim about CALLERS rather than an invariant, which is why
	// payment/recon's partiesAreMembers checks it against the books. reasonTable
	// gives it RC01.
	ErrBankNotAdmitted = errors.New("payment: this scheme does not clear for one of these two banks")

	// ErrOnUsPayment is a submission whose payer and payee bank at the SAME
	// institution.
	//
	// It is a statement about the ROUTE and not about the payment. Two customers of
	// one bank paying each other is an ordinary thing to want; what it is not is a
	// CLEARING payment. Nothing leaves the bank, so there is no interbank obligation
	// to net, no reserves to move, and no camt.053 that could tell a bank about a
	// book it already holds.
	//
	// Submitted to clearing anyway, it produced three separate wrong answers, each in
	// a different institution: a cycle that settled nothing and stranded at Cleared, a
	// reserve mirror moved by an amount the central bank's own record did not move,
	// and a returning bank refusing its own customer's unconditional refund because it
	// was the returner on both legs. PostReturnLegTx states the return's rule so that
	// it does not depend on this refusal holding.
	//
	// A sentinel and not just a message because the layer above has a remedy: api
	// answers 422 and the caller asks its bank for a book transfer instead
	// (deposit.Register.TransferTx). A signpost rather than a dead end.
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

	// ErrCycleNotClosed is settlement attempted on a cycle whose cut-off has not been
	// reached.
	//
	// It is the CLEARING HOUSE's refusal and only its own: that institution holds the
	// cycle. The settlement agent has no cycles table — see ErrCycleAlreadySettled,
	// which is what it refuses a redelivery with instead.
	ErrCycleNotClosed = errors.New("clearing cycle is not closed")

	// ErrCycleAlreadySettled is the SETTLEMENT AGENT refusing to discharge a cut-off
	// it has already discharged.
	//
	// A redelivered pacs.009 cannot be caught by the cycle's status, which is a row
	// this institution cannot read. What it can read is its own settlement register.
	//
	// Like ErrCycleNotClosed it is classified with the EMPTY code in reasonTable: it
	// describes this system's own state rather than a judgement about the sender's
	// message, so the agent reports it in the day instead of telling a clearing house
	// that its settled cycle was rejected.
	ErrCycleAlreadySettled = errors.New("clearing cycle has already settled")

	// ErrCycleNotReleasable is the CLEARING HOUSE refusing to settle a cut-off whose
	// instructions it could not afterwards hand to the banks that have to apply them.
	//
	// Settle-before-release only protects a receiving bank if the release is certain
	// to follow. A share is built when a file is worked, so a cycle carrying a payment
	// put into it without a file behind it would settle finally at the agent and reach
	// nobody: reserves moved, and a payee whose bank was never told the payment
	// exists. The clearing house holds the shares, so it is the only institution that
	// can refuse this, and it refuses BEFORE the money rather than reporting after it.
	//
	// The cycle stays Closed and its payments stay Cleared, which is a state an
	// operator can act on. The same batch settled instead is a loss nothing in this
	// system can unwind.
	ErrCycleNotReleasable = errors.New("clearing cycle cannot be released to its receiving banks")

	// ErrHeldReturnNotFound is the clearing house holding no return for a payment an
	// answer names.
	//
	// An ordinary answer rather than a failure, which is why it is a sentinel a caller
	// matches and not a refusal that stops the day: an answer arriving for a payment
	// nothing is held for is still owed to the bank that asked for the return. Only
	// the second hop is missing.
	ErrHeldReturnNotFound = errors.New("payment: the clearing house holds no return for this payment")

	// ErrInvalidSettlement is a settlement instruction this agent cannot read as one
	// batch: no legs, legs referencing different cycles or assets, no single
	// settlement agent between them, or one member named twice.
	//
	// It exists because the agent works from the INSTRUCTION rather than from the
	// cycle it names (see positionsIn), and a message can be malformed where a row
	// read out of the clearing house's own database could not: a batch that does not
	// sum to zero moves money nobody computed.
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
	//
	// A different class from every other refusal in this file. The rest are about the
	// SUBJECT of an act — a payment this bank is not a party to, a statement about
	// another member's reserve account — and each is decided by comparing the act's
	// subject against the bank performing it. This one is about the PERFORMER, where
	// there is no subject to compare against because there is no member and no book to
	// be about.
	//
	// The two are not redundant: two member banks are both members, so nothing here
	// can tell one from the other. What this adds is the case a subject guard cannot
	// see.
	ErrNotThisInstitutionsAct = errors.New("payment: this act belongs to another institution")

	// ErrNotThisBanksMandate is a bank asked to record a mandate whose creditor banks
	// somewhere else.
	//
	// In SEPA the CREDITOR holds the mandate and it is the creditor's bank that checks
	// one at submission, so the row belongs to that bank and to no other. A bank that
	// recorded whatever it was handed would hold an authorisation over an account at a
	// third bank, on behalf of a customer that is not its own.
	//
	// It is ErrNotThisBanksPayment's shape one flow over, with one difference: that
	// one decides between two banks that are both parties to the payment. This is not
	// about a party at all — the DEBTOR is a party and is not this bank's customer
	// either, being recorded from what the creditor said.
	ErrNotThisBanksMandate = errors.New("payment: this mandate's creditor banks somewhere else")

	// ErrNotThisBanksPayment is a bank asked to post a leg for a payment whose party
	// is somebody else's customer.
	//
	// On a push the clearing house tells BOTH banks a payment settled — the payer's
	// because it has been waiting for the answer to its instruction, the payee's
	// because it has a leg to post — and only one of them may post. A system that let
	// either post would credit the payee twice or credit them in the wrong book.
	ErrNotThisBanksPayment = errors.New("payment: this bank is not the party whose leg this is")

	// ErrStatementNotForThisBank is a statement about an account this bank does not
	// hold.
	//
	// A member checks the account the statement names against its OWN reserve account
	// before booking anything from it. A bank that booked whatever arrived would move
	// its reserve mirror on another member's position — and under isolation a
	// misrouted statement has no second reader to catch it.
	ErrStatementNotForThisBank = errors.New("payment: this statement is about an account this bank does not hold")

	// ErrBICAlreadyAdmitted is an acknowledgement arriving on a BIC the clearing house
	// already routes to, under a DIFFERENT admission.
	//
	// The qualification is the whole sentinel. Two things legitimately arrive on a BIC
	// already in the roster: the same bank's second currency, and an operator
	// re-driving an admission that failed partway. A refusal keyed on "is this BIC in
	// the roster" would refuse exactly those two and never fire on the case it exists
	// for. Keyed on the admission reference it separates them.
	//
	// It is the clearing house's answer and not the transport's: enrolling a
	// subscriber twice at an EBICS host is a no-op, and this is the statement about
	// MEMBERSHIP, made by the institution that owns routing.
	ErrBICAlreadyAdmitted = errors.New("payment: this BIC is already admitted under another admission")

	// ErrBankCodeTaken is a bank code already allocated to a DIFFERENT institution.
	//
	// Two banks issuing addresses under one code is the defect the whole routing
	// directory would sit on top of: every address either minted would resolve to
	// whichever bank the reader's copy named last, and neither a payer nor a receiving
	// bank could tell.
	//
	// So it is refused twice, in two databases. The settlement agent refuses it at
	// ALLOCATION, where it can be refused for good — the registry is keyed by
	// (country, code). The clearing house refuses it again on its own roster, and that
	// one earns its place: the roster is what every member COPIES, so a duplicate
	// there would make one address ambiguous for the whole scheme, and the clearing
	// house cannot see the registry to check.
	//
	// Not ErrBICAlreadyAdmitted, which is two institutions contending for one ADDRESS.
	// A bank can hold a BIC nobody else wants and still be handed a code somebody else
	// holds.
	ErrBankCodeTaken = errors.New("payment: this bank code is already allocated to another institution")

	// ErrBankCodeReplaced is an acknowledgement that would move a bank's address
	// range: to another country, or to another code in the one it applied to.
	//
	// A CODE IS NEVER REASSIGNED, and this is the guard that says so from the bank's
	// own end. It is the invariant the subscribed-copy design rests on: a member's
	// routing directory can be INCOMPLETE and must never be WRONG — "I cannot route
	// this yet" is a state a payer can be told about and "I routed it to the wrong
	// bank" is not. A bank that accepted a second allocation would leave every address
	// it had issued pointing at a range it no longer holds, in every copy of the
	// roster in the scheme, with nothing saying so.
	//
	// ErrSettlementAccountReplaced's sibling, reaching further: that one costs the
	// bank its own reserve postings, this one costs every customer their address.
	ErrBankCodeReplaced = errors.New("payment: this acknowledgement would move the bank's address range")

	// ErrBankAlreadyAdmitted is a bank recording an acknowledgement that belongs to an
	// admission other than the one it recorded a membership under.
	//
	// ErrBICAlreadyAdmitted one institution over and about a different row: the
	// clearing house refuses a second INSTITUTION contending for an address, from the
	// roster; this is a BANK refusing a message about itself, from its own memory of
	// what it accepted. Separate because the rows are in different databases.
	//
	// What it stops is measured: an acknowledgement naming a member's own BIC and
	// quoting an admission it never heard of moved that bank's settlement reference
	// onto an invented account, leaving its row disagreeing with the settlement
	// agent's about which account it holds.
	//
	// A bank that has recorded NO membership refuses nothing, which is what lets an
	// operator re-drive an interrupted admission under a new process id.
	ErrBankAlreadyAdmitted = errors.New("payment: this bank recorded its membership under another admission")

	// ErrAdmissionNotIdentified is an acknowledgement quoting no admission.
	//
	// Refs/PrcId is the conversation's only correlator, and on this side of the wire
	// "" is not a value but a SENTINEL: an empty Bank.AdmissionRef means "this bank has
	// accepted nothing yet", and an empty RosterEntry.AdmissionRef compares equal to
	// any other empty one. So an acknowledgement carrying none defeats both admission
	// guards at once, measured on the acts — the bank records a membership with no
	// reference and the NEXT acknowledgement from any admission moves its settlement
	// reference, and two institutions on one BIC both quoting "" compare equal, so
	// AdmitMemberTx extends the first's entry instead of refusing the second.
	//
	// checkAcknowledgement is what refuses it, once, for both acts.
	ErrAdmissionNotIdentified = errors.New("payment: this acknowledgement quotes no admission")

	// ErrAdmittedAccountUnusable is an acknowledgement whose accounts neither act can
	// file: NO account at all, one naming no asset, or an asset naming no account.
	//
	// The asset decides which of a bank's internal account sets a settlement reference
	// belongs to and which schemes a member clears in, so an account with none would
	// be filed under the empty asset by both readers — a reserve nothing settles
	// through and a reference nothing quotes. An empty account identifier is the same
	// hole from the other end.
	//
	// The EMPTY LIST is the third arm and cost most, because it is the value that
	// makes the other two not apply: a loop over no accounts refuses nothing.
	// Recorded, it wedges both institutions — an admitted bank that settles through no
	// account, a roster entry that clears in no scheme, and the true acknowledgement
	// then refused for ever by the admission-reference guards those rows now carry.
	//
	// A fourth arm only the BANK can decide: an acknowledgement naming accounts only
	// in assets the bank operates in none of is the empty list reached with a non-empty
	// one. RecordMembershipTx refuses it and checkAcknowledgement cannot, because the
	// same message is perfectly usable to the CLEARING HOUSE, which records what the
	// servicer opened and never asks the bank what it holds.
	ErrAdmittedAccountUnusable = errors.New("payment: this acknowledgement does not name a usable account")

	// ErrSettlementAccountReplaced is an acknowledgement quoting a DIFFERENT
	// settlement account for an asset the bank has already recorded one in.
	//
	// It is the hole ErrBankAlreadyAdmitted's own guard left open, and the fourth
	// instance on this branch of one shape: a guard closes a case and its own "does
	// not apply" value stays reachable. That one refuses an acknowledgement quoting
	// ANOTHER admission; on the admission's OWN reference it refuses nothing.
	//
	// Measured, on a healthy member: a second acknowledgement echoing the admission
	// the bank itself accepted, carrying an invented account, moved the bank's euro
	// settlement reference onto it permanently. Afterwards DepositTx answered a bare
	// "account not found", the operator console's ReserveBalance went on reporting the
	// healthy reserve (it reads the settlement agent's row, which never moved), and
	// re-driving the admission was refused with "already a member of this scheme".
	// Every door out was shut and each one said something different.
	//
	// What it does NOT refuse is a redelivery or a second asset, which is why it
	// compares rather than forbids: an acknowledgement lists every account the
	// servicer holds, so the second currency's answer repeats the first's account.
	// Equal is an extension; different is a claim about an account this bank's
	// settlement agent has never moved it to.
	//
	// It reaches no counterparty, nothing carrying an admission between institutions:
	// it goes back to whoever drove the act.
	ErrSettlementAccountReplaced = errors.New("payment: this acknowledgement moves a settlement account this bank already holds")

	// ErrNotThisBanksAdmission is an acknowledgement addressed to another bank's BIC,
	// refused by the bank asked to record it.
	//
	// ErrStatementNotForThisBank one flow over, for the same reason: the caller passes
	// a bank's OWN network alongside an acknowledgement it did not address, so nothing
	// in the signature stops it naming somebody else's. A bank that recorded whatever
	// arrived would write another member's settlement account numbers onto its own
	// row.
	//
	// What makes it reachable is that nothing in this package composes the four acts:
	// the bank's own act is called with an answer somebody else obtained.
	ErrNotThisBanksAdmission = errors.New("payment: this admission names a bank other than the one handling it")

	// ErrSchemeUnsupportedReturn is returned when a return is attempted on a
	// payment whose scheme does not support returns.
	ErrSchemeUnsupportedReturn = errors.New("scheme does not support returns")

	// ErrReturnAlreadySettled is a return instruction the settlement agent has already
	// acted on.
	//
	// A redelivery, detected in the ledger rather than in a row of its own: the reserve
	// reversal carries "<payment>:return-settle" as its idempotency key, and that key
	// is the only record the agent has that it settled this return — it holds no
	// payment rows. SettleReturnTx reads the key before it posts, so the answer is this
	// rather than a funding refusal, and the ledger refuses the posting on the same
	// key, so two deliveries in flight at once cannot both pass the read.
	//
	// A statement about THIS system's state and not about the sender's message: a
	// caller answering a counterparty with it would report a return that in fact
	// happened as rejected. Dead-letter it.
	ErrReturnAlreadySettled = errors.New("payment: this return has already been settled")

	// ErrNotAPartyToThisReturn is a bank asked to post a return leg for a payment it
	// is neither side of.
	//
	// A return has exactly two customer legs and each belongs to one bank: the clawback
	// at the creditor's, the refund at the debtor's. Which one a bank posts follows
	// from which side it is on, and neither the caller nor the message gets to say. It
	// is ErrNotThisBanksPayment's counterpart on the return path, separate because that
	// one names the ONE bank a creditor leg belongs to where this names a bank that is
	// not either of two.
	//
	// It is nearly unreachable, and the reason is worth knowing before anyone deletes
	// it: a bank that is neither side holds no ROW for the payment, so
	// PostReturnLegTx's read fails with ErrPaymentNotFound first. What is left for this
	// to answer is a payment the bank DOES hold and is not a party to, which needs an
	// instruction naming agents that disagree with the row. The store is the STRONGER
	// of the two, because it cannot be got wrong by a comparison; this one states the
	// rule the store enforces by accident of where the rows are. Both stay.
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
	//
	// The ledger cannot catch it at INITIATION. A payment is never one posting: the
	// debtor leg is a transaction in the payer's bank's book, the creditor leg a
	// separate transaction in the payee's, written later at settlement. The debtor leg
	// on its own —
	//
	//	Debit  Alice EUR      3000
	//	Credit Suspense EUR   3000
	//
	// — is impeccable double-entry within one asset, and nothing in it contains the
	// claim that some posting in another bank's book is its other half. Only the
	// payment layer holds both ends at once.
	//
	// It does catch it at the CREDITOR LEG, which resolves the creditor's suspense
	// account by the scheme's asset, so the leg comes out as a EUR suspense debit
	// against a BTC credit and is refused with ledger.ErrUnbalancedAsset. That is
	// still a bad place to find out: the cut-off settles and this one payment stays
	// Cleared, the error names an unbalanced asset rather than the payment that caused
	// it, and it arrives long after the payer was debited. This sentinel turns a late,
	// misattributed failure into an immediate, correctly attributed one.
	ErrAssetMismatch = errors.New("payment accounts are not denominated in the scheme's asset")

	// ErrUnaddressableAccount is returned when a party's account carries no
	// identifier in the scheme's identifier scheme — an account with no IBAN
	// cannot be a leg of a SEPA payment.
	ErrUnaddressableAccount = errors.New("account has no identifier in the scheme's addressing scheme")

	// ErrIdentifierMismatch is a party quoting an identifier that is not one of the
	// named account's addresses in the scheme's addressing scheme. The ids route the
	// payment and the identifier records the address used; the two disagreeing means
	// one of them is wrong, and the system does not get to choose which.
	//
	// It covers two shapes because they are one question asked once: the account does
	// not hold the quoted address at all, and it holds it under a different identifier
	// scheme than the payment scheme routes on — a card PAN quoted for a SEPA credit
	// transfer.
	ErrIdentifierMismatch = errors.New("quoted identifier is not one of the account's addresses in the scheme's addressing scheme")

	// ErrAmbiguousAddress is a party quoting NO address whose account holds more than
	// one identifier in the scheme's addressing scheme, so there is nothing to
	// back-fill without choosing.
	//
	// Initiation fills the address in when there is exactly one candidate, which is
	// what makes "a payment records the address it was sent to" true for every payment
	// rather than only for the ones whose caller volunteered it. Two candidates gets a
	// refusal, not the first in the set: picking would write a real, checkable address
	// onto a settled payment on the strength of slice order, and a customer reading
	// their statement would see an IBAN nobody quoted.
	ErrAmbiguousAddress = errors.New("account holds several identifiers in the scheme's addressing scheme; the payment must quote one")

	// ErrCounterpartyNotNamed is a submission that did not say who the other side is.
	// The instruction must carry the counterparty's NAME because the message it becomes
	// must, and because submission looks it up nowhere: the account is at another bank,
	// and nothing on the path that builds a payment reads another bank's register.
	//
	// The NAME is the only thing an instruction still asserts about the other side. The
	// agent beside it is derived from the address (see ErrCounterpartyAgentNotNamed),
	// which is why this has no sibling for a payer who typed the wrong bank: there is
	// no field to type it in.
	ErrCounterpartyNotNamed = errors.New("payment: the instruction does not name the counterparty")

	// ErrCounterpartyAgentNotNamed is an address THIS system has no directory for, on
	// an instruction that named no BIC beside it — or named something that is not one.
	//
	// ONE sentinel for both, because the remedy is the same: an instruction carrying no
	// routing element and one carrying an unusable one are equally unsendable, and the
	// message names which it was.
	//
	// It is the narrow case. An IBAN is DERIVED from — routeTx reads the bank code out
	// of the address and resolves it in this bank's copy of the scheme's routing
	// directory — so a payer types an address and a name and nothing else, which is
	// what SEPA has been since February 2016 and is possible here for the same reason:
	// every member subscribes to a published table.
	//
	// What is left in it is real. A card PAN is issued by a scheme elsewhere; a proxy
	// alias is resolved by a central service this system does not have — the EPC's
	// Proxy Lookup Service, UPI — precisely because no bank can guarantee an alias is
	// unique; an address in a country nothing here issues in has no structure to read a
	// bank code out of. For all of those the BIC genuinely is the payer's to supply.
	//
	// The sibling refusal, for an address that DOES have a directory here and resolves
	// to nothing in it, is ErrBankCodeUnknown. Different remedies: supply a BIC, versus
	// refresh or give up.
	//
	// Refused at SUBMISSION rather than at the cut-off, because a submission that
	// committed the payer's debit and then failed to render an instruction hours later
	// is a payer short of money against a file that was never built.
	//
	// It does NOT claim a supplied BIC is right. Nothing here can check one: a
	// wrong-but-well-formed BIC puts the transaction in that bank's share of the file
	// and is refused THERE, with AC01, by a bank that does not hold the address.
	ErrCounterpartyAgentNotNamed = errors.New("payment: the instruction does not name a usable BIC for the counterparty's bank")
)
