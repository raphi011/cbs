package payment

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// reasonMapping classifies one sentinel from errors.go.
//
// This file is payment's translation layer: the conversion between this
// system's domain types and the ISO 20022 messages that carry them between
// banks. It lives here rather than in iso20022 because iso20022 imports
// nothing from this repository, deliberately — see that package's doc. A
// translator inside it would be the same import pointing the other way, and
// the claim "these are the standard's types" would stop being checkable.
//
// Name duplicates the identifier as a string so the test can compare the table
// against errors.go's own declarations without reflection, which cannot see
// package-level var names. The pair is kept honest by a second test asserting
// that the Err values are pairwise distinct and as numerous as the
// declarations — a mislabelled entry then either collides or leaves a name
// uncovered.
type reasonMapping struct {
	Err  error
	Name string
	Code iso20022.StatusReason
}

// reasonTable maps this package's sentinels to wire codes, undriftably.
//
// EVERY sentinel in errors.go must appear. An error that gets no code is mapped
// to the empty one with a comment saying why, rather than omitted — omission is
// indistinguishable from an oversight, which is the exact failure this table
// exists to prevent. TestReasonTableCoversEverySentinel parses errors.go and
// fails on any gap.
//
// The empty code means two different things and the comment on each entry is
// what says which. Most of them are errors that CANNOT reach a counterparty:
// this system's own bookkeeping failing, with nothing truthful to tell a sender.
// The admission refusals are the other kind — each is a real answer, and it goes
// back to whoever is provisioning the bank rather than onto a wire. The codes
// here are the pacs.002's external set, so there is nothing in it for an
// account-opening refusal either way. Both blocks are below, each under its own
// heading.
var reasonTable = []reasonMapping{
	// --- Rejections a counterparty actually receives ---

	{ErrAccountNotInParticipant, "ErrAccountNotInParticipant", iso20022.StatusReasonIncorrectAccountNumber},
	{ErrDuplicateEndToEndID, "ErrDuplicateEndToEndID", iso20022.StatusReasonDuplication},

	// MD01 means there is NO mandate, which is the right and more serious
	// claim for a revoked one: a revoked mandate is precisely the absence of a
	// valid one.
	{ErrMandateRequired, "ErrMandateRequired", iso20022.StatusReasonNoMandate},
	{ErrMandateNotFound, "ErrMandateNotFound", iso20022.StatusReasonNoMandate},
	{ErrMandateRevoked, "ErrMandateRevoked", iso20022.StatusReasonNoMandate},

	// Not MD01. A valid mandate exists and this collection falls outside it,
	// which the external set has no code for; MD01 would put a false statement
	// on the wire. MS03 plus AddtlInf says less, accurately.
	{ErrMandateMismatch, "ErrMandateMismatch", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrMandateExceeded, "ErrMandateExceeded", iso20022.StatusReasonNotSpecifiedAgentGenerated},

	{ErrParticipantNotFound, "ErrParticipantNotFound", iso20022.StatusReasonBankIdentifierIncorrect},

	// The same code for a narrower fact, and the code set's own gloss covers
	// both: RC01 is "the BIC does not identify a reachable participant", which
	// is true of a bank that does not exist and equally true of one this scheme
	// has not admitted. It is classified in this block rather than below because
	// it does reach a counterparty — when the PAYEE's bank is the non-member,
	// the clearing house turns AcceptAtCSMTx's refusal into the RJCT its
	// submitter reverses on. In the other direction there is nowhere to put the
	// answer, because the bank to be told is the non-member itself and a
	// non-member has no download queue; that direction is
	// refused at the submitting bank's own door instead, before any message
	// exists. See ErrBankNotAdmitted, which sets out both.
	{ErrBankNotAdmitted, "ErrBankNotAdmitted", iso20022.StatusReasonBankIdentifierIncorrect},
	{ErrUnaddressableAccount, "ErrUnaddressableAccount", iso20022.StatusReasonMissingDebtorAccountOrIdentification},
	{ErrIdentifierMismatch, "ErrIdentifierMismatch", iso20022.StatusReasonMissingDebtorAccountOrIdentification},
	{ErrAmbiguousAddress, "ErrAmbiguousAddress", iso20022.StatusReasonMissingDebtorAccountOrIdentification},
	{ErrCycleNotOpen, "ErrCycleNotOpen", iso20022.StatusReasonInvalidCutOffTime},

	// A settlement instruction the agent cannot read as one batch, and unlike
	// every cycle error above it IS a judgement about the message: no legs, legs
	// naming two cycles or two assets, no single agent between them, or one
	// member twice. The clearing house sent it and the clearing house can fix it,
	// so it is answered rather than swallowed.
	//
	// MS03, which is also where ReasonFor's default would have put it — and it is
	// listed EXPLICITLY rather than left to fall through, because "no entry" and
	// "classified as MS03 on purpose" are indistinguishable from the table and
	// only one of them is a decision anybody made. There is no code for "your
	// batch does not describe one cut-off"; MS03 plus the AddtlInf that names the
	// element is what an operator can act on.
	{ErrInvalidSettlement, "ErrInvalidSettlement", iso20022.StatusReasonNotSpecifiedAgentGenerated},

	// MS03 for a stronger reason than "no better code exists": in SEPA a
	// currency mismatch cannot happen, because the scheme is euro-only, so the
	// code set never needed one. That this repository can produce the error at
	// all is a consequence of its multi-asset ledger, and the honest wire
	// representation of a condition the scheme does not contemplate is
	// "unspecified".
	{ErrAssetMismatch, "ErrAssetMismatch", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrSchemeNotFound, "ErrSchemeNotFound", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrInvalidPaymentAmount, "ErrInvalidPaymentAmount", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrParticipantAssetNotFound, "ErrParticipantAssetNotFound", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrSchemeUnsupportedReturn, "ErrSchemeUnsupportedReturn", iso20022.StatusReasonNotSpecifiedAgentGenerated},

	// An instruction naming no counterparty is refused at submission, before
	// any leg posts and before any message could exist to carry a reason back
	// — but it is classified here rather than below, alongside the other
	// malformed-instruction refusals, because nothing distinguishes it from
	// them: a bad amount and a missing counterparty are the same category of
	// defect, said about a different field.
	{ErrCounterpartyNotNamed, "ErrCounterpartyNotNamed", iso20022.StatusReasonNotSpecifiedAgentGenerated},

	// An on-us instruction is the same category again — an instruction this
	// scheme cannot carry, refused at submission — and MS03 for the same reason:
	// the code set has nothing for "both of these parties are yours".
	//
	// It is the one refusal here that could never reach a counterparty even in
	// principle, and not merely because no path exists today. The two agents are
	// one bank, so the message would be addressed to its sender. What the payer
	// is told instead is api's 422 and the remedy beside it: the same transfer,
	// as a book transfer. See ErrOnUsPayment.
	{ErrOnUsPayment, "ErrOnUsPayment", iso20022.StatusReasonNotSpecifiedAgentGenerated},

	// Its sibling, and RC01 rather than MS03 because there IS a code for this
	// one: "the BIC does not identify a reachable participant" is exactly what
	// an absent or malformed CdtrAgt/DbtrAgt means. It is classified here
	// alongside ErrParticipantNotFound for that reason and not because it
	// travels — like ErrCounterpartyNotNamed it is refused at submission, before
	// any message exists to carry it.
	{ErrCounterpartyAgentNotNamed, "ErrCounterpartyAgentNotNamed", iso20022.StatusReasonBankIdentifierIncorrect},

	// And the third of the address refusals, which gets the same RC01 for the
	// same reason: an address whose bank code resolves to nothing in this bank's
	// copy of the directory is a payee this scheme cannot be told to reach, which
	// is what RC01 says. The two sit together because a payer cannot act on the
	// difference — one asks for a BIC and one asks for a refresh, and both mean
	// "this instruction names nowhere to send it".
	//
	// It is refused at submission and carries no message either. What the CODE is
	// for is the day this refusal is reached with a payment in hand — a relayed
	// instruction the receiving bank cannot route onward — and the table classifies
	// every sentinel whether or not a path to the wire exists today.
	{ErrBankCodeUnknown, "ErrBankCodeUnknown", iso20022.StatusReasonBankIdentifierIncorrect},

	// --- Classified as never reaching a counterparty ---
	//
	// Each is a failure of THIS system's own bookkeeping rather than a
	// judgement about the instruction, so there is nothing truthful to tell
	// the sender. They are classified here as never reaching a counterparty,
	// and the institutions in cmd/server are where that classification is acted
	// on: one that gets a file back with such an error records it against the
	// order id in the day's report rather than uploading an answer.
	//
	// ReasonFor itself still cannot tell one of these apart from an error the
	// table has never heard of at all — both come back MS03 through the same
	// fallback path, and TestReasonForEmptyCodeEntriesFallToMS03 pins that. So
	// the discrimination is the CALLER's, made by name, and the ones
	// discriminated that way are the ones reached on paths nothing is wrong
	// with:
	//
	//   - ErrInvalidStateTransition, an ordinary redelivery, in three places:
	//     a bank applying a released file, the clearing house taking one in, and
	//     the settlement agent executing a return.
	//   - ErrNotThisBanksPayment, the ordinary happy path of EVERY push
	//     settlement. csm.tellSettled fans the ACSC to both banks; the payer's
	//     bank has no creditor leg, and PostCreditorLeg tells it so. Discarded
	//     by name in bank.receiveStatus.
	//   - ErrCycleNotClosed, an ordinary redelivered pacs.009 — a cycle already
	//     settled is not a rejection to answer. Discriminated in
	//     centralBank.receiveSettlement.
	//
	// Which errors and not how many: a sentinel added to errors.go adds a row
	// here.
	//
	// The errors NOT in the list above are not produced by the halves those
	// handlers call, except through a message quoting an identifier this
	// network has never issued, which answers MS03. That residue is stated
	// rather than hidden; closing it needs a two-valued ReasonFor, which is a
	// change to this signature and to every caller of it.

	// A lookup for an id this system generated and then could not find is a
	// bug here, not a defect in the message.
	{ErrPaymentNotFound, "ErrPaymentNotFound", ""},
	{ErrCycleNotFound, "ErrCycleNotFound", ""},
	{ErrSettlementNotFound, "ErrSettlementNotFound", ""},

	// A settlement advice is a bank's OWN row about its OWN cut-off, so a
	// missing one is a question this bank asked itself and got no answer to.
	// There is no counterparty in the conversation to tell.
	{ErrSettlementAdviceNotFound, "ErrSettlementAdviceNotFound", ""},

	// The two rows admission gives the other institutions, missing.
	//
	// Both are an institution asking about its OWN record and not finding it,
	// which is this system's own inconsistency rather than a judgement about
	// anybody's instruction: the settlement agent holds no account for a BIC it
	// is asked to settle for, the clearing house routes to no address for a
	// bank the network numbers. Nothing writes one of the three admission rows
	// without the other two, so reaching either means the store disagrees with
	// itself — and RC01, which is what ErrParticipantNotFound answers, would say
	// the SENDER quoted a bank that does not exist. It did not.
	//
	// A bank whose provisioning stopped half-way makes either of these reachable
	// without the store disagreeing with itself, and it is still not a judgement
	// about anybody's instruction: it is a provisioning failure to retry, and a
	// payment/recon finding. So this classification is the right one either way.
	{ErrSettlementMemberNotFound, "ErrSettlementMemberNotFound", ""},
	{ErrRosterEntryNotFound, "ErrRosterEntryNotFound", ""},

	// All three mean a message arrived at the wrong bank — a settled-payment
	// advice for somebody else's customer, a reserve statement about somebody
	// else's account, a return naming a payment this bank is neither side of.
	// That is a defect in the ROUTING, and the sender is the clearing house or
	// the settlement agent rather than a counterparty with an instruction
	// outstanding. There is no code for "you sent me your own mistake", and MS03
	// would report the receiving bank's correct refusal as a judgement about a
	// payment.
	{ErrNotThisBanksPayment, "ErrNotThisBanksPayment", ""},
	{ErrStatementNotForThisBank, "ErrStatementNotForThisBank", ""},
	{ErrNotAPartyToThisReturn, "ErrNotAPartyToThisReturn", ""},

	// A mandate recorded at a bank that is not its creditor's. It reaches no
	// message at all: creating one is an operator's request on a bank's own
	// console, answered with a status code, and no file any institution
	// exchanges carries CreateMandateTx. The empty code is therefore not a judgement about what to
	// tell a counterparty — there is no conversation this can occur in.
	{ErrNotThisBanksMandate, "ErrNotThisBanksMandate", ""},

	// One institution's act reached through another's Network, and it is the
	// only entry here that is not about a message at all.
	//
	// The three above are a message delivered to the wrong bank, which is a
	// routing defect with a real sender behind it. This one has no sender: the
	// caller and the callee are the same process, and what went wrong is that
	// something held a handle belonging to one institution and asked it to
	// perform another's act. It is a WIRING mistake — the same class as
	// NewNetwork's zero Identity, which panics — and it is a returned error here only because the entity a Network
	// belongs to is decided far from where an act is called.
	//
	// So the empty code, for a sharper version of the reason the three above
	// have it: there is not merely no truthful thing to tell a counterparty,
	// there is no counterparty. MS03 would report a defect in this process as
	// this agent's judgement about somebody's payment.
	{ErrNotThisInstitutionsAct, "ErrNotThisInstitutionsAct", ""},

	// Cycle lifecycle errors reach only the operator who drove the cycle into
	// the wrong state; no counterparty ever sees one.
	{ErrCycleNotClosed, "ErrCycleNotClosed", ""},
	{ErrCycleAlreadyOpen, "ErrCycleAlreadyOpen", ""},

	// A cut-off the clearing house will not settle because it could not release
	// what it settled. The empty code for the cycle-lifecycle reason and one of
	// its own: this refusal happens BEFORE any instruction is uploaded, so there
	// is no message for a counterparty to be answered about — no settlement agent
	// has been asked and no submitting bank is owed a verdict on its payment,
	// which is still exactly where the cut-off left it.
	{ErrCycleNotReleasable, "ErrCycleNotReleasable", ""},

	// The clearing house holding no return for a payment an answer names. The
	// empty code because it is not a verdict on anybody's message: the answer
	// this arrives with is forwarded to the bank that asked either way, carrying
	// the settlement agent's own code, and what is missing is only the second hop
	// of a conversation that bank is not party to.
	{ErrHeldReturnNotFound, "ErrHeldReturnNotFound", ""},

	// A cut-off the settlement agent has already discharged. It is a redelivered
	// pacs.009 and nothing more — the clearing house asked twice, or the queue
	// did — so it takes ErrCycleNotClosed's empty code and ErrCycleNotClosed's
	// argument: it describes THIS system's state and not a judgement about the
	// instruction, and MS03 would tell a clearing house that a cycle which in
	// fact settled was refused. The settlement agent's handler discriminates it
	// by name and reports it instead of answering.
	//
	// A separate sentinel because the agent does not read the cycle to find out:
	// it holds no cycles table, so "have I settled this" is answered out of its own
	// settlement register (Tx.GetSettlementByCycle). See SettleCycleTx.
	{ErrCycleAlreadySettled, "ErrCycleAlreadySettled", ""},

	// An illegal transition means this system tried to move a payment
	// somewhere its own state machine forbids. Telling the counterparty
	// "rejected, unspecified" would hide a defect behind a plausible message.
	{ErrInvalidStateTransition, "ErrInvalidStateTransition", ""},

	// A return the settlement agent has already settled. It is the redelivery
	// case ErrInvalidStateTransition covers on every other path, arriving from
	// the one actor that has no payment row to read a status off — so it is a
	// separate sentinel and it gets the same empty code for the same reason:
	// answering RJCT would tell the returning bank that a return which in fact
	// completed was refused. Dead-letter it.
	{ErrReturnAlreadySettled, "ErrReturnAlreadySettled", ""},

	// --- The admission refusals, which are answered off this code set ---
	//
	// These rows are empty for a different reason from the block above, and the
	// difference matters because a refusal here is a real answer where nothing
	// classified above is. Admitting a bank is not a conversation: each of the
	// four acts is called directly, one institution at a time, and a refusal goes
	// back to the caller that asked (see package provision). The codes in this
	// table are the pacs.002's external set, so there is nothing here for any of
	// them to map to and an entry with a code would put a payment status on an
	// account-opening refusal.
	//
	// So none of these reaches a wire at all, and the empty code is what says so.
	// A refusal that stops a bank being admitted stops it in the process doing
	// the admitting, which is where the operator is and where the retry is.
	//
	// No paragraph here says how many rows there are. Any change to the thing
	// counted falsifies a count, in either direction, and a removal is the one
	// nobody rereads the prose for. The ROWS have a guard — errors.go declares a
	// sentinel, translate_test.go requires a row for it and refuses a row naming
	// anything else — and the prose about them has none.
	{ErrBICAlreadyAdmitted, "ErrBICAlreadyAdmitted", ""},
	{ErrBankAlreadyAdmitted, "ErrBankAlreadyAdmitted", ""},
	{ErrAdmissionNotIdentified, "ErrAdmissionNotIdentified", ""},
	{ErrAdmittedAccountUnusable, "ErrAdmittedAccountUnusable", ""},
	{ErrSettlementAccountReplaced, "ErrSettlementAccountReplaced", ""},
	{ErrNotThisBanksAdmission, "ErrNotThisBanksAdmission", ""},
	// The three about addressing, and they are on this path for the same reason
	// the six above are: every one of them is refused during an admission, and no
	// admission is answered on a wire. ErrBankCodeNotAllocated and
	// ErrBankCodeTaken are the settlement agent's and the clearing house's
	// answers about the registry; ErrBankCodeReplaced is what either the roster
	// or the joining bank says to an acknowledgement that would move an address
	// range that is already being quoted.
	//
	// None of them reaches a PAYMENT. The refusal a payer meets when an address
	// will not resolve is ErrBankCodeUnknown, and it has a row of its own with a
	// code, because it happens to a payment and an answer goes back on a
	// pacs.002.
	{ErrBankCodeNotAllocated, "ErrBankCodeNotAllocated", ""},
	{ErrBankCodeTaken, "ErrBankCodeTaken", ""},
	{ErrBankCodeReplaced, "ErrBankCodeReplaced", ""},
}

// borrowedReasons classifies the errors an actor's half produces that this
// package did not declare.
//
// It is a second table and not more rows in the first, because reasonTable's
// two guards are about payment/errors.go — every sentinel declared there must
// appear, and nothing else may.
//
// Two of the three are AM04, from two different layers, and they are two entries
// rather than one because they are two distinct error values that no unwrapping
// relates: neither wraps the other.
//
//   - deposit.ErrInsufficientAvailable, from two halves. It is the direct
//     debit's whole point: Scheme.Validate is a funds check run by the DEBTOR's
//     bank, and the deposit layer is the authority for it. It also comes from
//     PostReturnLegTx, where the RETURNING bank on a push checks its own payee
//     before it composes the pacs.004 — so on a push return, AM04 can be about
//     the asking bank's OWN customer.
//   - ledger.ErrInsufficientBalance is the same refusal one layer down and one
//     institution over: a net payer whose RESERVE cannot cover its position at
//     settlement. SettleCycleTx checks each net payer's reserve at the central
//     bank itself and returns this sentinel deliberately, so that the code on the
//     wire is the one the ledger would have given. See SettleCycleTx.
//
// Without an entry, ReasonFor falls through to MS03 — "the agent rejected it and
// the reason has no code" — for the two refusals that have the most specific
// code of all. AM04 is what a real debtor's bank sends and what a real
// settlement agent answers, and it is what the receiving system reads to decide
// whether to re-present or unwind.
//
// The third is deposit.ErrAccountClosed, and it is AC04, which the external set
// has a member for and which is one of the commonest return reasons in SEPA. It
// is the payee's own bank's refusal of a settled credit — the account exists and
// will not take one — and it reaches a counterparty because a receiving bank
// after finality answers with a pacs.004 rather than with silence.
//
// A new member belongs here only if the error reaches ReasonFor at all, which
// means a half that some institution's handler calls really returns it. A push
// RETURN is the first place in a push flow where deposit's funds sentinel is
// produced, because it checks whether the payee's account can fund a WITHDRAWAL.
var borrowedReasons = []reasonMapping{
	{deposit.ErrInsufficientAvailable, "deposit.ErrInsufficientAvailable", iso20022.StatusReasonInsufficientFunds},
	{ledger.ErrInsufficientBalance, "ledger.ErrInsufficientBalance", iso20022.StatusReasonInsufficientFunds},
	{deposit.ErrAccountClosed, "deposit.ErrAccountClosed", iso20022.StatusReasonClosedAccountNumber},
}

// ReasonFor maps an error to the code a pacs.002 should carry.
//
// It is exported because the party that decides an error is worth telling a
// counterparty about is not this package: it is whoever holds the connection.
// In this repository that is cmd/server, whose bank and clearing-house handlers
// turn a refused half into a rejection in a file they upload.
//
// It unwraps, because the payment layer wraps freely and a table keyed on
// identity alone would degrade to MS03 for most real failures — silently,
// which is the failure mode this whole arrangement exists to prevent.
//
// An error the table does not know is MS03 rather than a panic. An actor that
// crashed instead of answering would be a worse outcome than an imprecise
// code, and the exhaustiveness test is what stops that path being reachable
// for a sentinel.
//
// It consults borrowedReasons after reasonTable, and the order is stated rather
// than incidental: where both tables classify an error, this package's own
// classification of its own sentinel wins.
//
// The order does NOT protect reasonTable's empty-code entries, and that is the
// half worth being exact about. Those are decisions made here too — "this never
// reaches a counterparty" — but the m.Code != "" filter skips them, so an error
// wrapping one of them and also matching a borrowed row would come back with the
// borrowed code rather than falling to MS03. Nothing produces such an error
// today and TestReasonForEmptyCodeEntriesFallToMS03 pins the direct case; the
// protection an empty code gives is the CALLER's, made by name before it asks
// for a code at all, which is what cmd/server's handlers do with
// ErrInvalidStateTransition. See the note on the empty-code block above.
func ReasonFor(err error) iso20022.StatusReason {
	for _, table := range [][]reasonMapping{reasonTable, borrowedReasons} {
		for _, m := range table {
			if m.Code != "" && errors.Is(err, m.Err) {
				return m.Code
			}
		}
	}
	return iso20022.StatusReasonNotSpecifiedAgentGenerated
}

// Answerable reports whether an error is a judgement about the INSTRUCTION —
// something a counterparty can be told and can act on — as against a failure of
// this system's own bookkeeping, which nobody outside it could do anything with.
//
// It is the question ReasonFor cannot answer. ReasonFor always produces a code,
// because a caller that has already decided to answer needs one; MS03 is its
// floor. So a dropped connection and a payee's closed account come back as codes
// that look alike, and the caller has to have made the decision first.
//
// The tables are what make it: an error classified in either of them was
// classified BY SOMEBODY, with a comment saying why, and an error in neither has
// simply never been thought about as something to put on a wire. The empty-code
// entries are excluded for the same reason ReasonFor excludes them — an empty
// code IS the decision that this one never reaches a counterparty.
//
// What it is for is the receiving bank's half after finality, where the two
// outcomes are no longer both messages: a judgement becomes a pacs.004 that
// sends a settled payment back, and a bookkeeping failure becomes a line in the
// day's report. Returning money on a dropped connection is the mistake this
// exists to prevent.
func Answerable(err error) bool {
	for _, table := range [][]reasonMapping{reasonTable, borrowedReasons} {
		for _, m := range table {
			if m.Code != "" && errors.Is(err, m.Err) {
				return true
			}
		}
	}
	return false
}

// ReturnReasonFor maps an error to the code a pacs.004 should carry.
//
// It is ReasonFor's counterpart for the OTHER external set, and it goes through
// ReasonFor rather than holding a second table: the two sets share their
// spellings wherever both name the same fact, and a second table would be a
// second place for one classification to live and drift from.
//
// It is not a cast, and the switch is what stops it being one. A status reason
// with no member of its own in the return set — TM01, FF01, AM05's siblings —
// becomes MS03, because a pacs.004 may only carry a member of the return set and
// putting a rejection code in one would be putting a value on the wire that set
// does not define. iso20022.ReturnReason exists as a distinct type for exactly
// this reason.
func ReturnReasonFor(err error) iso20022.ReturnReason {
	switch ReasonFor(err) {
	case iso20022.StatusReasonIncorrectAccountNumber:
		return iso20022.ReturnReasonIncorrectAccountNumber
	case iso20022.StatusReasonClosedAccountNumber:
		return iso20022.ReturnReasonClosedAccountNumber
	case iso20022.StatusReasonInsufficientFunds:
		return iso20022.ReturnReasonInsufficientFunds
	case iso20022.StatusReasonDuplication:
		return iso20022.ReturnReasonDuplication
	case iso20022.StatusReasonNoMandate:
		return iso20022.ReturnReasonNoMandate
	case iso20022.StatusReasonBankIdentifierIncorrect:
		return iso20022.ReturnReasonBankIdentifierIncorrect
	case iso20022.StatusReasonMissingDebtorAccountOrIdentification:
		return iso20022.ReturnReasonMissingDebtorAccountOrIdentification
	default:
		return iso20022.ReturnReasonNotSpecifiedAgentGenerated
	}
}

// ---------------------------------------------------------------------------
// Outbound: payment types to messages
// ---------------------------------------------------------------------------

// MessageContext is everything a message needs that the payment itself does
// not carry: who is sending it, who to, what to call it, and when.
//
// It is a parameter rather than state on Network because the same payment
// produces different messages on different hops — the debtor's bank sends the
// pacs.008 to the CSM, the CSM sends the same instruction on to the creditor's
// bank — and the difference is entirely in here.
type MessageContext struct {
	From  iso20022.BIC
	To    iso20022.BIC
	MsgID string
	Now   time.Time

	// DecidedBy is who MADE the decision this message reports, when that is not
	// the sender. Empty means the sender decided it, which is the ordinary case
	// and every message but one.
	//
	// It exists because a relay cannot otherwise be honest. The clearing house
	// passes the settlement agent's refusal of a RETURN straight through to the
	// bank that asked for one (cmd/server's csm.receiveReturnStatus): it decides
	// nothing, adds nothing, and — with only From to go on — was stamping
	// itself as the originator of somebody else's refusal. That is precisely
	// what Orgtr exists to prevent, per iso20022.StatusReasonInformation: a
	// receiver reading it blames the wrong institution and investigates a
	// clearing house that had no view of the matter.
	//
	// It is separate from From rather than replacing it because the two are
	// different questions with different answers on exactly this hop, and
	// collapsing them is the bug. See orgtr.
	DecidedBy iso20022.BIC
}

func (mc MessageContext) header(msgDefIdr string) iso20022.AppHdr {
	return iso20022.AppHdr{
		Fr:        iso20022.NewAgent(mc.From),
		To:        iso20022.NewAgent(mc.To),
		BizMsgIdr: mc.MsgID,
		MsgDefIdr: msgDefIdr,
		CreDt:     iso20022.ISODateTime{Time: mc.Now},
	}
}

// orgtr is the party that DECIDED, as opposed to the party that sent.
//
// The two differ the moment a message passes through an intermediary: a status
// report travelling back from the creditor's bank through the clearing house is
// sent by the clearing house and originated by the bank.
//
// Almost every message this system emits is one it decided itself, so the
// originator defaults to mc.From — but it is written out rather than left to
// the header, because a receiver reading the header's Fr as the decider blames
// the wrong institution for every rejection that was relayed. See
// iso20022.StatusReasonInformation.
//
// The exception is the hop that DecidedBy exists for, and there is one: the
// clearing house passing the settlement agent's answer about a return back to
// the bank that asked. It decided nothing and says so.
func (mc MessageContext) orgtr() *iso20022.PartyIdentification {
	decider := mc.DecidedBy
	if decider == "" {
		decider = mc.From
	}
	return &iso20022.PartyIdentification{
		Id: &iso20022.PartyChoice{
			OrgId: &iso20022.OrganisationIdentification{AnyBIC: decider},
		},
	}
}

// messageParty is one side of a payment with everything a message says about
// it: the bank that holds the account, the name on the account, and the address
// the payment quoted to reach it.
//
// It exists so that the conversion itself — creditTransfer, directDebit — is a
// pure function of resolved data, decoupled from whatever produces that data.
// partiesOf is the one production producer, and it reads no store to build one
// (see partiesOf's own doc); fuzz_test.go is the other, deliberate producer:
// buildCreditTransfer constructs messageParty values directly, skipping
// partiesOf and CreditTransferMessage entirely, which is what lets
// FuzzTranslate drive the mapping over names and addresses no fixture builder —
// and now no submission path either — would have thought to try.
type messageParty struct {
	BIC        iso20022.BIC
	Name       string
	Identifier deposit.Identifier
}

// agentOf is the element that says WHICH BANK. In SEPA that is a BIC and
// nothing else.
func agentOf(b iso20022.BIC) iso20022.BranchAndFinancialInstitution {
	return iso20022.BranchAndFinancialInstitution{
		FinInstnId: iso20022.FinancialInstitutionIdentification{BICFI: b},
	}
}

// agentRef is agentOf as a pointer, for the elements — OrgnlTxRef's DbtrAgt
// and CdtrAgt — where the schema makes the agent optional and so the field is
// *BranchAndFinancialInstitution rather than the value every mandatory Dbtr/
// Cdtr on this package's other messages uses.
func agentRef(b iso20022.BIC) *iso20022.BranchAndFinancialInstitution {
	a := agentOf(b)
	return &a
}

// amountOf converts a ledger amount to the standard's decimal representation.
//
// The scale comes from the asset definition rather than from a constant, because
// this repository's ledger is multi-asset and a two-decimal assumption would be
// wrong the first time a scheme in another asset arrives.
//
// The rendered amount is then checked, and the check ASKS THE CODEC rather than
// deciding for itself: this function does not know what ISO 20022 permits and
// does not try to. So the bound below describes iso20022's Validate at the time
// of writing, and a Validate that tightened or loosened needs no change here.
//
// Two ways an amount this ledger holds is refused:
//
//   - FRACTION DIGITS. The standard caps ActiveCurrencyAndAmount at five for any
//     currency, so an asset scaled finer has no representation on the wire at
//     all. Bitcoin, at eight, is in this repository's asset table today, so this
//     is a live limit. ErrAmountScale.
//
//   - MAGNITUDE, and the honest bound is not the standard's eighteen-digit
//     ceiling. Validate calls Minor(5), which zero-pads the fraction to five
//     places and parses as an int64, so the real bound is MaxInt64/10^(5-scale)
//     — for a two-decimal asset, 9,223,372,036,854,775 minor units at SIXTEEN
//     digits. Below scale 5 it bites too hard, refusing legal seventeen- and
//     eighteen-digit values; AT scale 5 the padding is a no-op and the check
//     degenerates to "fits in an int64", which ADMITS nineteen-digit values the
//     schema forbids. The gap is inert only because no asset in ledger.Assets()
//     is scaled to 5, and nothing enforces that. See
//     TestValidateAdmitsNineteenDigitsAtScaleFive in iso20022.
//
//     It is recorded rather than worked around, because a workaround here would
//     be this package second-guessing the codec. Enforcing the standard's actual
//     ceiling belongs in iso20022.ActiveCurrencyAndAmount.Validate, where the
//     bound would then also be testable. The sentinel is ErrAmountFormat, which
//     is the wrong one for a well-formed number and is part of the same artifact.
//
// Neither was found by FuzzTranslate: the target fuzzes the AMOUNT and holds the
// asset at EUR, and a large amount only produces a message once the fuzzer has
// also assembled two valid IBANs and two non-empty names. A hand-written probe
// over ledger.Assets and the int64 boundaries found both in one run. A fuzz
// target explores the inputs it was given and no others.
//
// Refusing here rather than at Marshal is the same choice ibanOf makes: both
// errors are correct, and only this one names the payment rather than an element
// inside a document, so only this one can be turned into a pacs.002 a customer
// can read.
func amountOf(amt ledger.Amount, asset ledger.AssetCode) (iso20022.ActiveCurrencyAndAmount, error) {
	def, err := ledger.LookupAsset(asset)
	if err != nil {
		return iso20022.ActiveCurrencyAndAmount{}, err
	}
	out, err := iso20022.NewAmount(int64(amt), def.Scale, string(asset))
	if err != nil {
		return iso20022.ActiveCurrencyAndAmount{}, err
	}
	if err := out.Validate(); err != nil {
		return iso20022.ActiveCurrencyAndAmount{}, fmt.Errorf("%w: %d minor units of %s", err, amt, asset)
	}
	return out, nil
}

// ibanOf turns a stored address into the message's account identification.
//
// It refuses an empty or non-IBAN address rather than emitting an empty
// element. A pacs.008 whose DbtrAcct has no IBAN is invalid, and producing one
// would move the failure from here to a counterparty's parser.
//
// It also COMPACTS and then checks the value. Compaction because an IBAN is
// stored here with display separators for readability and transmitted without
// them (see iso20022.IBAN). The check because a stored value that is not an
// IBAN at all would otherwise reach Marshal, and the error a caller then sees
// names an element inside a document rather than the account this system could
// not address — both errors are correct, and only this one can be turned into a
// pacs.002 for the customer. Delete the check and FuzzTranslate fails in under
// a second on the address "0"; that is what it is for.
//
// It returns the IBAN rather than the CashAccount that wraps it, because a
// pacs.003 needs the same value in two places — CdtrAcct and, standing in for
// the Creditor Identifier, CdtrSchmeId — and the alternative was reaching back
// into the built element for it through a pointer that only happens never to be
// nil. See cashAccount.
func ibanOf(field string, id deposit.Identifier) (iso20022.IBAN, error) {
	if id.Scheme != deposit.IdentifierIBAN || id.Value == "" {
		return "", fmt.Errorf("%w: %s", ErrUnaddressableAccount, field)
	}
	iban := iso20022.IBAN(id.Value).Compact()
	if err := iban.Validate(); err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrUnaddressableAccount, field, err)
	}
	return iban, nil
}

// cashAccount wraps a validated IBAN as the account element of a message.
//
// It takes the IBAN by value and takes its address here, which is the whole
// reason it exists: AccountIdentification4Choice's arms are pointers because
// encoding/xml cannot express an xsd:choice, and a pointer arm is an invitation
// to a nil dereference somewhere downstream. Building the choice in exactly one
// place, from a value that cannot be nil, means no other function in this file
// ever has to dereference it.
func cashAccount(iban iso20022.IBAN) iso20022.CashAccount {
	return iso20022.CashAccount{Id: iso20022.AccountIdentification4Choice{IBAN: &iban}}
}

// namedPartyOf is the customer element of a pacs.008 or a pacs.003.
//
// A nameless one is refused here rather than at Marshal, for the same reason
// ibanOf refuses an unaddressable account: EPC AT-P001 and AT-E001 make Nm
// mandatory on both sides of both messages, so a party with no name is a
// document a counterparty rejects. Delete the check and FuzzTranslate finds an
// empty creditor name in about half a second. The error is not one of
// payment's sentinels because there is no condition in this
// system's own vocabulary for "the account holder has no name" — it wraps
// iso20022.ErrMissingElement, which says exactly what went wrong, and falls to
// MS03 through ReasonFor's default like any other error the table has never
// heard of.
func namedPartyOf(element, name string) (iso20022.PartyIdentification, error) {
	if name == "" {
		return iso20022.PartyIdentification{}, fmt.Errorf("%w: %s/Nm", iso20022.ErrMissingElement, element)
	}
	return iso20022.PartyIdentification{Nm: name}, nil
}

// endToEndOf is the customer's own reference, or the value the guidelines
// reserve for its absence.
//
// EndToEndId is 1..1 in the schema and payment.Payment's EndToEndID is
// optional, so the gap has to be filled with something. NOTPROVIDED is the EPC
// convention, and it is filled in HERE rather than defaulted at initiation
// because it is a fact about the message and not about the payment: a receiver
// reading NOTPROVIDED knows the sender had no reference, whereas a payment
// carrying the literal string in its own record would have one that means
// nothing.
func endToEndOf(p Payment) string {
	if p.EndToEndID == "" {
		return "NOTPROVIDED"
	}
	return p.EndToEndID
}

func paymentIdentificationOf(p Payment) iso20022.PaymentIdentification {
	return iso20022.PaymentIdentification{
		EndToEndId: endToEndOf(p),
		TxId:       string(p.ID),
	}
}

// remittanceOf is what the payment is for, as far as the two customers are
// concerned. Nothing at all rather than an empty element: RmtInf with an empty
// Ustrd is a claim that the sender said something and it was blank.
func remittanceOf(text string) *iso20022.RemittanceInformation {
	if text == "" {
		return nil
	}
	return &iso20022.RemittanceInformation{Ustrd: text}
}

// settlementDateOf is the date the message asserts interbank settlement for.
//
// It is the payment's VALUE date and not the message's creation date: SEPA CT
// settles at T+1 and SDD at T+2, so the two are days apart by design and a
// message that used Now would instruct settlement on the wrong day.
//
// A payment with no value date has not been through SubmitPayment, and gets
// the message's own creation date rather than the zero time. The zero time
// marshals as 0001-01-01 — a schema-valid date asserting settlement in the
// first century, which is the same silent fabrication AppHdr.CreDt's validation
// exists to stop, arriving by a different door.
func settlementDateOf(p Payment, mc MessageContext) iso20022.ISODate {
	if p.ValueDate.IsZero() {
		return iso20022.ISODate{Time: mc.Now}
	}
	return iso20022.ISODate{Time: p.ValueDate}
}

// clearingSettlement is how every message this system sends settles: through a
// clearing system, rather than across accounts the two agents hold with each
// other. See iso20022.SettlementMethodClearing for why that is a property of
// THIS clearing house and not of the scheme.
func clearingSettlement() iso20022.SettlementInstruction {
	return iso20022.SettlementInstruction{SttlmMtd: iso20022.SettlementMethodClearing}
}

// assetOf is the unit a payment settles in, which is a property of its scheme
// and not of the payment. An unregistered scheme is ErrSchemeNotFound here for
// the same reason it is at initiation: this system cannot say what a payment
// under a scheme it does not implement is denominated in, and guessing euro
// would be the multi-asset mistake amountOf exists to avoid.
func (s *Network) assetOf(p Payment) (ledger.AssetCode, error) {
	sc, ok := s.scheme(p.Scheme)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrSchemeNotFound, p.Scheme)
	}
	return sc.Asset(), nil
}

// partiesOf resolves a payment's two sides to what a message says about them:
// each bank's BIC and each account holder's name.
//
// It reads NOTHING. Both are on the payment, put there at submission — the
// bank's own side from its own register, the counterparty's name and agent from
// the instruction (see PartyDetails.Agent and SubmitPaymentTx). Building an
// outbound message reads no other bank's book.
//
// So the comment this replaces — "the ONLY part of building an outbound message
// that touches the store" — is now false in the strongest way available: no part
// of it does. The four builders below were already pure and this joins them,
// which is why the two Network methods lose their context parameter.
//
// It is a PACKAGE function and not a method for the same reason, one step
// further: it held a *Network receiver it never named, and "it reads nothing" is
// worth having the compiler check rather than a reader. There is no store on the
// other side of a value it does not have.
func partiesOf(p Payment) (debtor, creditor messageParty) {
	return messageParty{
			BIC:        p.DebtorDetails.Agent,
			Name:       p.DebtorDetails.Name,
			Identifier: p.Debtor.Identifier,
		}, messageParty{
			BIC:        p.CreditorDetails.Agent,
			Name:       p.CreditorDetails.Name,
			Identifier: p.Creditor.Identifier,
		}
}

// outbound is one payment and everything a message builder needs that the
// payment does not carry: both sides as a message names them, the asset its
// scheme settles in, and, on a pull, the mandate that authorises it.
type outbound struct {
	payment  Payment
	mandate  Mandate
	debtor   messageParty
	creditor messageParty
	asset    ledger.AssetCode
}

// outboundOf is the one lookup a builder needs — the payment's scheme, for the
// asset it settles in. partiesOf reads nothing.
func (s *Network) outboundOf(p Payment) (outbound, error) {
	asset, err := s.assetOf(p)
	if err != nil {
		return outbound{}, err
	}
	debtor, creditor := partiesOf(p)
	return outbound{payment: p, debtor: debtor, creditor: creditor, asset: asset}, nil
}

// CreditTransferMessage renders a file of payments as the pacs.008 that carries
// them between banks.
//
// A FILE, because that is what a bank sends: instructions accumulate behind a
// cut-off and one message carries all of them. One payment is the smallest such
// file and not a different shape.
//
// The two AGENTS are the two banks, and the header's To is whoever the message
// is being handed to next — the clearing house, on the first hop. Those are
// different questions with different answers, and conflating them would model a
// topology this system does not have: banks here meet at a CSM, never directly.
//
// It takes neither a context nor a Tx: a payment carries both sides' names and
// agents, so there is no I/O left here to need a unit of work for.
func (s *Network) CreditTransferMessage(ps []Payment, mc MessageContext) (iso20022.Envelope, error) {
	out := make([]outbound, 0, len(ps))
	for _, p := range ps {
		o, err := s.outboundOf(p)
		if err != nil {
			return iso20022.Envelope{}, err
		}
		out = append(out, o)
	}
	return creditTransfer(out, mc)
}

// creditTransfer builds the message from data its caller already holds.
// Everything it does is a pure function of its arguments, which is what
// FuzzTranslate needs: CreditTransferMessage reads no store either (see its
// own doc), so there is no I/O anywhere on this path for a fuzz input to
// depend on.
func creditTransfer(out []outbound, mc MessageContext) (iso20022.Envelope, error) {
	settled, err := groupSettlementDate(out, mc)
	if err != nil {
		return iso20022.Envelope{}, err
	}

	txs := make([]iso20022.CreditTransferTransaction, 0, len(out))
	for _, o := range out {
		tx, err := creditTransferTx(o)
		if err != nil {
			return iso20022.Envelope{}, err
		}
		txs = append(txs, tx)
	}

	doc := &iso20022.Pacs008{FIToFICstmrCdtTrf: iso20022.FIToFICustomerCreditTransfer{
		GrpHdr: iso20022.CreditTransferGroupHeader{
			MsgId:   mc.MsgID,
			CreDtTm: iso20022.ISODateTime{Time: mc.Now},
			// NbOfTxs is what the SENDER asserts, not a derivation the receiver
			// would recompute. A receiver that recomputed it instead of checking
			// it would never notice a truncated file, which is the whole reason
			// the element exists. See
			// TestSettlementMessageNbOfTxsSurvivesATruncatedFile.
			NbOfTxs:       strconv.Itoa(len(txs)),
			IntrBkSttlmDt: settled,
			SttlmInf:      clearingSettlement(),
		},
		CdtTrfTxInf: txs,
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// creditTransferTx is one transaction of a pacs.008.
//
// The field order is the schema's, and it is the payment's own path: party, its
// bank, the other bank, the other party. LclInstrm is absent because SEPA credit
// transfer does not populate it, and SeqTp is absent because pacs.008 has no
// such element at all — see iso20022.PaymentTypeInformation.
func creditTransferTx(o outbound) (iso20022.CreditTransferTransaction, error) {
	var zero iso20022.CreditTransferTransaction

	amt, err := amountOf(o.payment.Amount, o.asset)
	if err != nil {
		return zero, err
	}
	dbtr, err := namedPartyOf("Dbtr", o.debtor.Name)
	if err != nil {
		return zero, err
	}
	dbtrIBAN, err := ibanOf("DbtrAcct", o.debtor.Identifier)
	if err != nil {
		return zero, err
	}
	cdtr, err := namedPartyOf("Cdtr", o.creditor.Name)
	if err != nil {
		return zero, err
	}
	cdtrIBAN, err := ibanOf("CdtrAcct", o.creditor.Identifier)
	if err != nil {
		return zero, err
	}

	return iso20022.CreditTransferTransaction{
		PmtId: paymentIdentificationOf(o.payment),
		PmtTpInf: &iso20022.PaymentTypeInformation{
			SvcLvl: &iso20022.ServiceLevelChoice{Cd: iso20022.ServiceLevelSEPA},
		},
		IntrBkSttlmAmt: amt,
		ChrgBr:         iso20022.ChargeBearerFollowingServiceLevel,
		Dbtr:           dbtr,
		DbtrAcct:       cashAccount(dbtrIBAN),
		DbtrAgt:        agentOf(o.debtor.BIC),
		CdtrAgt:        agentOf(o.creditor.BIC),
		Cdtr:           cdtr,
		CdtrAcct:       cashAccount(cdtrIBAN),
		RmtInf:         remittanceOf(o.payment.Description),
	}, nil
}

// groupSettlementDate is the one interbank settlement date a file asserts.
//
// IntrBkSttlmDt is the GROUP's element and there is one of it, so a file whose
// transactions settle on different days says something false about some of
// them. A cut-off builds one file per scheme and a scheme's value date follows
// from the scheme, so agreement is how the payments arrive rather than
// something a caller arranges — and the file that disagrees anyway is refused
// here instead of being given one of its dates and being wrong about the rest.
//
// An EMPTY file is refused in the same breath. There is no date to assert, and
// a pacs.008 with no transactions is not a quiet day: it is a cut-off that
// produced a message nobody needed to send.
func groupSettlementDate(out []outbound, mc MessageContext) (iso20022.ISODate, error) {
	if len(out) == 0 {
		return iso20022.ISODate{}, fmt.Errorf("payment: a file with no transactions is not a message")
	}
	settled := settlementDateOf(out[0].payment, mc)
	for _, o := range out[1:] {
		if d := settlementDateOf(o.payment, mc); !d.Time.Equal(settled.Time) {
			return iso20022.ISODate{}, fmt.Errorf("payment: one file cannot assert two settlement dates, %s and %s",
				settled.Time.Format(time.DateOnly), d.Time.Format(time.DateOnly))
		}
	}
	return settled, nil
}

// InstructionMessage renders one cut-off file: the pacs.008 or pacs.003 a bank
// hands the clearing house when it reaches its cut-off.
//
// One file, one SCHEME, because a scheme decides both the message definition and
// the asset every amount in the file is denominated in. A bank operating two
// schemes reaches one cut-off and uploads two files, which is the shape a real
// hub has: a SEPA credit transfer bulk and a SEPA direct debit bulk are two
// submissions to the same clearing house on the same morning.
//
// It is the only builder that reads a store, and only on a pull: a pacs.003
// carries each debtor's own mandate and a payment holds nothing of one but its
// id. The read is a View over the submitting bank's own database — a mandate is
// held by the CREDITOR's bank in SEPA, which is the bank reaching this cut-off.
func (s *Network) InstructionMessage(ctx context.Context, ps []Payment, mc MessageContext) (iso20022.Envelope, error) {
	if len(ps) == 0 {
		return iso20022.Envelope{}, fmt.Errorf("payment: a file with no transactions is not a message")
	}
	scheme, ok := s.scheme(ps[0].Scheme)
	if !ok {
		return iso20022.Envelope{}, fmt.Errorf("%w: %s", ErrSchemeNotFound, ps[0].Scheme)
	}
	for _, p := range ps[1:] {
		if p.Scheme != ps[0].Scheme {
			return iso20022.Envelope{}, fmt.Errorf("payment: one file cannot carry both %s and %s", ps[0].Scheme, p.Scheme)
		}
	}
	if scheme.Direction() != Pull {
		return s.CreditTransferMessage(ps, mc)
	}
	var cs []Collection
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		cs = make([]Collection, 0, len(ps))
		for _, p := range ps {
			m, err := tx.GetMandate(ctx, p.MandateID)
			if err != nil {
				return err
			}
			cs = append(cs, Collection{Payment: p, Mandate: m})
		}
		return nil
	})
	if err != nil {
		return iso20022.Envelope{}, err
	}
	return s.DirectDebitMessage(cs, mc)
}

// instructableTx proves that a payment can be put in a file, by building the
// transaction it would travel as and keeping nothing.
//
// Building it is the only honest way to know it can be built. The alternative is
// a second list of the elements a message needs — both accounts addressable,
// both parties named, the amount representable in the standard's decimal — and
// two lists of one rule are two things that drift.
//
// It runs at SUBMISSION, inside the unit of work that posts the debtor leg, and
// that placement is the whole point. The file itself is not built until the
// cut-off, hours later; a payer debited now against an instruction that turns out
// to be unsendable then would be short of money against a payment nobody could
// ever answer. So the refusal happens while the debit can still roll back with
// it, and the API answers 422.
//
// What it costs is one rendering per payment that is thrown away, and the
// transaction it renders is rendered again at the cut-off. That is the price of
// the check being the real thing rather than a summary of it.
func (s *Network) instructableTx(ctx context.Context, tx Tx, p Payment) error {
	scheme, ok := s.scheme(p.Scheme)
	if !ok {
		return fmt.Errorf("%w: %s", ErrSchemeNotFound, p.Scheme)
	}
	o, err := s.outboundOf(p)
	if err != nil {
		return err
	}
	if scheme.Direction() != Pull {
		_, err = creditTransferTx(o)
		return err
	}
	if o.mandate, err = tx.GetMandate(ctx, p.MandateID); err != nil {
		return err
	}
	_, err = directDebitTx(o)
	return err
}

// Collection is a direct debit and the mandate that authorises it.
//
// They travel together because a pacs.003 carries the mandate's own terms and a
// payment holds only its MandateID. A file of collections is a file of pairs,
// each with its own mandate: one debtor's authority says nothing about the next.
type Collection struct {
	Payment Payment
	Mandate Mandate
}

// DirectDebitMessage renders a file of collections as the pacs.003 that carries
// them.
//
// It is the mirror of CreditTransferMessage in the one way that matters: the
// SENDER is the party being paid. A push scheme's message travels with the
// money; a pull scheme's travels against it, which is why the creditor and its
// agent come first in the transaction and why the mandate has to travel too.
//
// It takes neither a context nor a Tx, for the same reason CreditTransferMessage
// does not: partiesOf reads nothing, and the mandate — the one piece of I/O this
// message ever needed — arrives already resolved on the Collection, loaded by
// InstructionMessage before this is called.
func (s *Network) DirectDebitMessage(cs []Collection, mc MessageContext) (iso20022.Envelope, error) {
	out := make([]outbound, 0, len(cs))
	for _, c := range cs {
		o, err := s.outboundOf(c.Payment)
		if err != nil {
			return iso20022.Envelope{}, err
		}
		o.mandate = c.Mandate
		out = append(out, o)
	}
	return directDebit(out, mc)
}

func directDebit(out []outbound, mc MessageContext) (iso20022.Envelope, error) {
	settled, err := groupSettlementDate(out, mc)
	if err != nil {
		return iso20022.Envelope{}, err
	}

	txs := make([]iso20022.DirectDebitTransactionInformation, 0, len(out))
	for _, o := range out {
		tx, err := directDebitTx(o)
		if err != nil {
			return iso20022.Envelope{}, err
		}
		txs = append(txs, tx)
	}

	doc := &iso20022.Pacs003{FIToFICstmrDrctDbt: iso20022.FIToFICustomerDirectDebit{
		GrpHdr: iso20022.DirectDebitGroupHeader{
			MsgId:         mc.MsgID,
			CreDtTm:       iso20022.ISODateTime{Time: mc.Now},
			NbOfTxs:       strconv.Itoa(len(txs)),
			IntrBkSttlmDt: settled,
			SttlmInf:      clearingSettlement(),
		},
		DrctDbtTxInf: txs,
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// directDebitTx is one collection of a pacs.003.
func directDebitTx(o outbound) (iso20022.DirectDebitTransactionInformation, error) {
	var zero iso20022.DirectDebitTransactionInformation

	p, m := o.payment, o.mandate
	amt, err := amountOf(p.Amount, o.asset)
	if err != nil {
		return zero, err
	}
	cdtr, err := namedPartyOf("Cdtr", o.creditor.Name)
	if err != nil {
		return zero, err
	}
	cdtrIBAN, err := ibanOf("CdtrAcct", o.creditor.Identifier)
	if err != nil {
		return zero, err
	}
	dbtr, err := namedPartyOf("Dbtr", o.debtor.Name)
	if err != nil {
		return zero, err
	}
	dbtrIBAN, err := ibanOf("DbtrAcct", o.debtor.Identifier)
	if err != nil {
		return zero, err
	}
	if m.ID == "" {
		return zero, ErrMandateRequired
	}

	local := iso20022.LocalInstrumentCore
	seq := iso20022.SequenceTypeRecurring
	return iso20022.DirectDebitTransactionInformation{
		PmtId: paymentIdentificationOf(p),
		PmtTpInf: &iso20022.PaymentTypeInformation{
			SvcLvl:    &iso20022.ServiceLevelChoice{Cd: iso20022.ServiceLevelSEPA},
			LclInstrm: &iso20022.LocalInstrumentChoice{Cd: &local},
			// SeqTp is RCUR for every collection, including the first.
			//
			// This system does not record whether a mandate has been exercised
			// before, so FRST is a claim it cannot make; the 2016 SEPA Core
			// rulebook removed the requirement to send FRST first and permits
			// RCUR throughout, which is what makes the weaker statement the
			// correct one rather than merely the safe one.
			SeqTp: &seq,
		},
		IntrBkSttlmAmt: amt,
		ChrgBr:         iso20022.ChargeBearerFollowingServiceLevel,
		DrctDbtTx: iso20022.DirectDebitTransaction{
			MndtRltdInf: iso20022.MandateRelatedInformation{
				MndtId: string(m.ID),
				// DtOfSgntr maps Mandate.CreatedAt, and this elision is
				// deliberate.
				//
				// The EPC makes the date of signature mandatory. payment.Mandate
				// has no signature date, and adding one was considered and
				// rejected in the spec: in this system a mandate is created at
				// the moment it is authorised, so the two are the same fact and a
				// second column would be a field with no independent source. A
				// reader meeting a real mandate — signed on paper, keyed in a
				// week later — finds the difference stated here rather than
				// assumed away. See
				// TestDirectDebitMessageDatesTheSignatureFromTheMandate.
				DtOfSgntr: iso20022.ISODate{Time: m.CreatedAt},
			},
			CdtrSchmeId: creditorSchemeIdentification(cdtrIBAN),
		},
		Cdtr:     cdtr,
		CdtrAcct: cashAccount(cdtrIBAN),
		CdtrAgt:  agentOf(o.creditor.BIC),
		Dbtr:     dbtr,
		DbtrAcct: cashAccount(dbtrIBAN),
		DbtrAgt:  agentOf(o.debtor.BIC),
		RmtInf:   remittanceOf(p.Description),
	}, nil
}

// creditorSchemeIdentification is AT-02, the Creditor Identifier — and it is
// the creditor's own IBAN, which is the second elision this message makes and
// the larger of the two.
//
// A real Creditor Identifier is issued by a national scheme: a country's
// central bank or an equivalent authority assigns it, which is precisely what
// makes it unforgeable and therefore worth checking a mandate against. This
// repository models no such authority, so it has no such value, and deriving
// one from a name or a BIC would fabricate the one property the element has.
// The creditor's IBAN is used instead because it identifies the creditor
// uniquely within this network, which is the property a debtor's bank reads the
// element FOR. What it is not is a Creditor Identifier: a debtor's bank holding
// a mandate scoped to a real CI would find a value it cannot look up. That is
// the cost, recorded rather than assumed away. See
// TestDirectDebitMessageStandsTheCreditorsIBANInForTheCreditorIdentifier.
func creditorSchemeIdentification(iban iso20022.IBAN) iso20022.CreditorSchemeIdentification {
	return iso20022.CreditorSchemeIdentification{
		Id: iso20022.PartyChoice{
			PrvtId: &iso20022.PersonIdentification{
				Othr: iso20022.GenericPersonIdentification{
					Id:      string(iban),
					SchmeNm: iso20022.PersonIdentificationScheme{Prtry: "SEPA"},
				},
			},
		},
	}
}

// OriginalMessage identifies the message a status report is about.
//
// A status report refers BACKWARDS and by nothing else: the sender of the
// original has no other way to find what this is about, which is what makes
// clearing asynchronous rather than merely delayed.
type OriginalMessage struct {
	MsgID     string
	MsgDefIdr string
	CreDtTm   time.Time
}

// TransactionStatusReport is the outcome of one transaction in that message.
//
// Code and Text are both carried because they say different things: the code is
// what makes a rejection machine-actionable, and the text is what says the part
// no code can — which ceiling was exceeded, what the available balance was. See
// iso20022.StatusReasonInformation.
type TransactionStatusReport struct {
	EndToEndID string
	TxID       string
	Status     iso20022.TransactionStatus
	Code       iso20022.StatusReason
	Text       string
}

// StatusMessage renders the fate of an earlier message as the pacs.002 that
// reports it.
//
// This is not a Network method because nothing in it comes from the store: a
// status is about a message, and the message is what the caller has.
func StatusMessage(orig OriginalMessage, sts []TransactionStatusReport, mc MessageContext) (iso20022.Envelope, error) {
	txs := make([]iso20022.PaymentTransactionStatus, 0, len(sts))
	for i, s := range sts {
		txs = append(txs, iso20022.PaymentTransactionStatus{
			// StsId names THIS status, not the payment it is about — see
			// iso20022.PaymentTransactionStatus. It is derived from the
			// message's own identifier and the position within it, so that two
			// statuses in one report are distinguishable and a later query can
			// name one of them.
			StsId:           fmt.Sprintf("%s-%d", mc.MsgID, i+1),
			OrgnlEndToEndId: s.EndToEndID,
			OrgnlTxId:       s.TxID,
			TxSts:           s.Status,
			StsRsnInf:       statusReasonOf(s, mc),
		})
	}

	doc := &iso20022.Pacs002{FIToFIPmtStsRpt: iso20022.FIToFIPaymentStatusReport{
		GrpHdr: iso20022.StatusGroupHeader{
			MsgId:   mc.MsgID,
			CreDtTm: iso20022.ISODateTime{Time: mc.Now},
		},
		OrgnlGrpInfAndSts: iso20022.OriginalGroupHeader{
			OrgnlMsgId:   orig.MsgID,
			OrgnlMsgNmId: orig.MsgDefIdr,
			OrgnlCreDtTm: originalCreationOf(orig),
			GrpSts:       groupStatusOf(sts),
		},
		TxInfAndSts: txs,
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// originalCreationOf echoes when the reported-on message was created, and omits
// the element entirely when the caller does not know. The alternative is
// 0001-01-01T00:00:00Z, a timestamp asserting that the original was written two
// thousand years ago — the fabrication AppHdr.CreDt's own validation exists to
// stop.
func originalCreationOf(orig OriginalMessage) *iso20022.ISODateTime {
	if orig.CreDtTm.IsZero() {
		return nil
	}
	return &iso20022.ISODateTime{Time: orig.CreDtTm}
}

// statusReasonOf is why, and it is present only for a rejection.
//
// An acceptance has no reason: StatusReasonChoice requires exactly one of a
// code and a proprietary text, so an accepted transaction with a reason element
// would have to invent one. A rejection with no code is likewise not
// representable, and is not reachable either — ReasonFor returns MS03 rather
// than the empty string for every error, including ones it has never heard of —
// so a rejection whose Code is empty is a caller that built the report by hand
// and left it out. It gets MS03 for the same reason ReasonFor's default does:
// "this agent refused it and the reason has no code" is the weakest true
// statement available, and it is better than a rejection a receiver cannot act
// on at all.
func statusReasonOf(s TransactionStatusReport, mc MessageContext) *iso20022.StatusReasonInformation {
	if s.Status != iso20022.TransactionStatusRejected {
		return nil
	}
	code := s.Code
	if code == "" {
		code = iso20022.StatusReasonNotSpecifiedAgentGenerated
	}
	return &iso20022.StatusReasonInformation{
		Orgtr:    mc.orgtr(),
		Rsn:      iso20022.StatusReasonChoice{Cd: &code},
		AddtlInf: s.Text,
	}
}

// groupStatusOf describes the whole bulk.
//
// It is derived and the per-transaction statuses are not, which is the one
// place in this file where a derivation is the right answer: GrpSts is a
// summary of what the sender is saying in the SAME message, not a count a
// receiver would use to detect a loss. A file of a thousand transfers accepted
// with fifty rejections is neither accepted nor rejected, and PART is the only
// truthful answer — see iso20022.OriginalGroupHeader.
//
// A report with no transaction statuses at all leaves the element empty rather
// than guessing: it is optional in the standard precisely so that a report can
// decline to characterise a group it says nothing about.
func groupStatusOf(sts []TransactionStatusReport) iso20022.GroupStatus {
	if len(sts) == 0 {
		return ""
	}
	var rejected int
	for _, s := range sts {
		if s.Status == iso20022.TransactionStatusRejected {
			rejected++
		}
	}
	switch rejected {
	case 0:
		return iso20022.GroupStatusAccepted
	case len(sts):
		return iso20022.GroupStatusRejected
	default:
		return iso20022.GroupStatusPartiallyAccepted
	}
}

// ReturnMessage renders a settled payment coming back as the pacs.004 that
// carries it.
//
// It is a distinct MESSAGE and not a status precisely because settlement was
// final: a pacs.002 says an instruction was refused, a pacs.004 says a
// completed payment is being reversed by sending an equal and opposite one.
//
// The reason is an iso20022.ReturnReason and not a StatusReason, and the two
// types are separate even where their members coincide. Payment.RejectCode is
// therefore NOT the source of it — see that field — and the caller supplies the
// return's own reason.
//
// This is a Network method although it reads no store, and it takes no context
// for that reason: the amount's scale comes from the scheme's asset and only the
// Network holds the scheme registry, which is an in-memory map. A pacs.004 names
// no ACCOUNT HOLDERS — it refers to the original payment by identifier and
// carries amounts — which is why it is the one outbound builder with no
// messageParty in its signature. It does name a party (RtrRsnInf.Orgtr, the bank
// that decided to send the money back, see mc.orgtr()) and two AGENTS (see
// OrgnlTxRef below), both off the payment's own DebtorDetails/CreditorDetails.
func (s *Network) ReturnMessage(p Payment, reason iso20022.ReturnReason, text string, mc MessageContext) (iso20022.Envelope, error) {
	asset, err := s.assetOf(p)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	amt, err := amountOf(p.Amount, asset)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	if reason == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: RtrRsnInf/Rsn", iso20022.ErrMissingElement)
	}

	txs := []iso20022.ReturnTransaction{{
		RtrId:           string(p.ID) + ":rtr",
		OrgnlEndToEndId: p.EndToEndID,
		OrgnlTxId:       string(p.ID),
		// The two amounts are equal because this system's returns are whole —
		// nothing in the domain takes a partial amount. They are two elements because the
		// standard is shaped for partial returns; see iso20022.ReturnTransaction.
		OrgnlIntrBkSttlmAmt: amt,
		RtrdIntrBkSttlmAmt:  amt,
		ChrgBr:              iso20022.ChargeBearerFollowingServiceLevel,
		RtrRsnInf: &iso20022.ReturnReasonInformation{
			Orgtr:    mc.orgtr(),
			Rsn:      iso20022.ReturnReasonChoice{Cd: &reason},
			AddtlInf: text,
		},
		// The settlement agent that must reverse this return's two reserve legs
		// holds no payment row — see iso20022.OriginalTransactionReference. Both
		// agents are already on the payment, resolved once at submission.
		OrgnlTxRef: &iso20022.OriginalTransactionReference{
			DbtrAgt: agentRef(p.DebtorDetails.Agent),
			CdtrAgt: agentRef(p.CreditorDetails.Agent),
		},
	}}

	doc := &iso20022.Pacs004{PmtRtr: iso20022.PaymentReturn{
		GrpHdr: iso20022.ReturnGroupHeader{
			MsgId:         mc.MsgID,
			CreDtTm:       iso20022.ISODateTime{Time: mc.Now},
			NbOfTxs:       strconv.Itoa(len(txs)),
			IntrBkSttlmDt: iso20022.ISODate{Time: mc.Now},
			SttlmInf:      clearingSettlement(),
		},
		TxInf: txs,
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// ---------------------------------------------------------------------------
// Inbound: messages to payment requests
// ---------------------------------------------------------------------------
//
// Everything above renders something this system already decided; everything
// below reads an instruction another bank decided, and the difference is that
// nothing here can be trusted to be self-consistent. A received message is the
// only thing the receiver has, and it carries no internal identifier of this
// system's.

// CreditTransferRequest turns a received pacs.008 into the requests this system
// can act on: one per transaction in the file, in the file's own order.
//
// A pacs.008 travels FROM the debtor's bank, routed by CdtrAgt, so the bank
// reading it SHOULD be the CREDITOR's, and the creditor is the only party it has
// any standing to resolve. It is resolved by ADDRESS — ResolveIdentifierTx
// against the IBAN the message carries, IN THIS BANK'S OWN REGISTER — and never
// by an internal account id, because the message has no element for one. Which
// is why an unresolvable creditor IBAN is ErrAccountNotInParticipant and becomes
// AC01: from the receiver's side, an address it cannot resolve for its own
// customer IS an incorrect account number.
//
// SHOULD, because CdtrAgt is what the SENDING bank asserted and nothing checked
// it against a register — nor could it, once each bank holds only its own row. A
// payer who names the wrong bank has the message delivered to that bank, and
// this function is what refuses it. Own-register resolution is the guard: a
// sweep would have found the payee at the RIGHT bank and gone on to act on
// somebody else's customer.
//
// The bank reading the message is this network's own identity, which is what
// makes "its own register" a property of the handle rather than of what the
// caller passed. A clearing house's network cannot read a pacs.008 into a
// request at all, and that is ErrNotThisInstitutionsAct rather than a resolution
// that quietly finds nothing.
//
// The DEBTOR is not resolved: it is the sending bank's customer, and this bank
// has no standing over whether that account exists. What comes back for it is
// what the message says — the address it quoted, on Debtor, and the agent and
// name it asserted, on DebtorDetails. See localPartyIn.
//
// The scheme is the MESSAGE's, and it takes two elements to name it. Being a
// pacs.008 says the payment is a PUSH; the currency says which asset it settles
// in; and this network has one scheme for that pair or it refuses the message
// (schemeSettling). What the message does NOT get to decide is that it is
// settled at all: a currency no push scheme settles in is refused rather than
// reinterpreted, because a receiver that took the sender's currency and some
// scheme's scale would book a number neither bank wrote down.
//
// It returns REQUESTS and not Payments: nothing here is accepted, deduplicated
// or posted. That is AcceptInboundTx's job, and the separation is what lets a
// receiving bank translate a file without a write and reject it before one.
func (s *Network) CreditTransferRequest(ctx context.Context, doc *iso20022.Pacs008) ([]InboundTransaction, error) {
	// Before the message is read at all, and the placement is what makes the
	// paragraph above exact rather than nearly true. The identity is reached
	// otherwise only through localPartyIn, at the end, so a MALFORMED pacs.008
	// handed to the clearing house came back as a parse error — a true statement
	// about the message and a silence about the institution, which is the one
	// thing this guard exists to say. Who is reading is not a question about
	// what arrived.
	if _, err := s.self(); err != nil {
		return nil, err
	}
	txs, err := s.creditTransferIn(doc)
	if err != nil {
		return nil, err
	}
	// The creditor is this bank's own customer on a push; the debtor is the
	// sending bank's and is recorded, not resolved. creditTransferIn left the
	// creditor as the ADDRESS the message quoted, which is exactly what this
	// resolution consumes.
	for i := range txs {
		if err := s.localSideIn(ctx, &txs[i], &txs[i].Request.Creditor); err != nil {
			return nil, err
		}
	}
	return txs, nil
}

// InboundTransaction is one transaction read out of a file: what it instructs,
// the id the SUBMITTING bank minted for it, and — where this bank has already
// decided it cannot act on this one — why.
//
// The id travels beside the request rather than inside it because it is the one
// thing on the message that is not part of an instruction. Every institution
// keys its own row by it, and nothing on this path allocates one — see
// SubmitPaymentTx, the only place in the system that does.
//
// # Refusal is what makes a file's answers per transaction
//
// A file is READ whole or not at all, and a transaction that cannot be parsed
// therefore takes the file with it. Resolution is a different question: whether
// THIS bank holds the address one line quotes says nothing about the next line,
// and a file of a thousand refused because the seventh names a closed account
// would tell nine hundred and ninety-nine payers their payees' IBANs were wrong.
//
// So an addressing refusal lands here rather than on the return value, and the
// caller answers that transaction and carries on with the rest. A Refusal is
// always something this bank can SAY — see localSideIn for what does not
// qualify.
type InboundTransaction struct {
	ID      PaymentID
	Request InitiatePaymentRequest
	Refusal error
}

// localSideIn resolves one transaction's own side, and decides whether a failure
// belongs to that transaction or to the whole file.
//
// Two refusals are about the ADDRESS and about nothing else — this bank holds no
// such account (AC01), and this bank holds two (MS03) — so each is an answer to
// the transaction that quoted it and the rest of the file is unaffected.
//
// Everything else fails the file, and the distinction is the same one
// addressedPartyTx draws one level down: a dropped connection or a cancelled
// context is not news about a payee's IBAN. Reported as a file-level failure it
// becomes a problem in the day's report and the file is answered by nobody,
// which is truthful; reported per transaction it would tell a thousand payers
// their payees' accounts were bad.
func (s *Network) localSideIn(ctx context.Context, tx *InboundTransaction, side *PartyRef) error {
	ref, err := s.localPartyIn(ctx, side.Identifier)
	switch {
	case err == nil:
		*side = ref
		return nil
	case errors.Is(err, ErrAccountNotInParticipant), errors.Is(err, deposit.ErrIdentifierAmbiguous):
		tx.Refusal = err
		return nil
	default:
		return err
	}
}

// creditTransferIn is everything a pacs.008 SAYS, resolving nobody.
//
// It is the half of CreditTransferRequest that needs no register, split out
// because the clearing house has none and has to record the payments anyway.
// Both parties come back as the address the message quoted and no account id;
// the receiving bank's own resolution then replaces its own side, and the
// clearing house's copy keeps both as they arrived. See
// RecordRelayedCreditTransfer.
//
// A transaction that cannot be read fails the WHOLE file, and that is the same
// argument NbOfTxs exists for: a reader that returned the four transactions it
// could parse and dropped the fifth would produce a payment that arrived, was
// acknowledged at the message level, and never happened. A file is accepted
// entire or refused entire.
func (s *Network) creditTransferIn(doc *iso20022.Pacs008) ([]InboundTransaction, error) {
	body := doc.FIToFICstmrCdtTrf
	if err := checkNbOfTxs("CdtTrfTxInf", body.CdtTrfTxInf, body.GrpHdr.NbOfTxs); err != nil {
		return nil, err
	}

	out := make([]InboundTransaction, 0, len(body.CdtTrfTxInf))
	for _, tx := range body.CdtTrfTxInf {
		scheme, amount, err := s.schemeSettling(Push, tx.IntrBkSttlmAmt)
		if err != nil {
			return nil, err
		}
		dbtrID, err := identifierIn("DbtrAcct", tx.DbtrAcct)
		if err != nil {
			return nil, err
		}
		cdtrID, err := identifierIn("CdtrAcct", tx.CdtrAcct)
		if err != nil {
			return nil, err
		}
		out = append(out, InboundTransaction{
			ID: PaymentID(tx.PmtId.TxId),
			Request: InitiatePaymentRequest{
				Scheme:          scheme,
				Debtor:          PartyRef{Identifier: dbtrID},
				Creditor:        PartyRef{Identifier: cdtrID},
				Amount:          amount,
				EndToEndID:      endToEndIn(tx.PmtId.EndToEndId),
				Description:     remittanceIn(tx.RmtInf),
				DebtorDetails:   PartyDetails{Agent: agentIn(tx.DbtrAgt), Name: nameIn(tx.Dbtr)},
				CreditorDetails: PartyDetails{Agent: agentIn(tx.CdtrAgt), Name: nameIn(tx.Cdtr)},
			},
		})
	}
	return out, nil
}

// DirectDebitRequest turns a received pacs.003 into the requests this system can
// act on: one per collection in the file, in the file's own order.
//
// A pacs.003 travels FROM the creditor's bank, routed by DbtrAgt, so the bank
// reading this message is the DEBTOR's — the mirror of CreditTransferRequest in
// exactly the way the direction implies, and no further: the DEBTOR is resolved
// by ADDRESS, in this bank's own register. The CREDITOR is recorded from what
// the message says and not resolved, for the identical reason
// CreditTransferRequest does not resolve a pacs.008's debtor — the creditor is
// the SENDING bank's customer, not this bank's to look up. The account elements
// are read for what they are rather than by position — a reader that took the
// first account element as the debtor's, as it is in a pacs.008, would produce a
// collection pointing the wrong way and resolve successfully while doing it.
//
// A collection addressed to the wrong bank is refused here, and on a pull that
// matters more than on a push: the receiving bank is the one that POSTS, so
// under the sweep a collector who named itself as the payer's bank resolved the
// payer at the payer's real bank and posted the debit in that bank's book.
//
// And it carries a mandate. An empty MndtId is refused here rather than left for
// SDD.Validate, which would refuse it too: this is another bank's claim on this
// bank's customer's account, the mandate is the only thing that makes it
// authorised, and the message that came with no mandate should not become a
// request that looks like one.
func (s *Network) DirectDebitRequest(ctx context.Context, doc *iso20022.Pacs003) ([]InboundTransaction, error) {
	// First, for CreditTransferRequest's reason.
	if _, err := s.self(); err != nil {
		return nil, err
	}
	txs, err := s.directDebitIn(doc)
	if err != nil {
		return nil, err
	}
	// The debtor is this bank's own customer on a pull; the creditor is the
	// sending bank's and is recorded, not resolved. See creditTransferIn's
	// mirror note on what directDebitIn left behind for this line to consume.
	for i := range txs {
		if err := s.localSideIn(ctx, &txs[i], &txs[i].Request.Debtor); err != nil {
			return nil, err
		}
	}
	return txs, nil
}

// directDebitIn is everything a pacs.003 SAYS, resolving nobody. It is
// creditTransferIn's mirror; see that function for why the split exists, what
// the PaymentID beside each request is, and why a transaction it cannot read
// fails the whole file.
func (s *Network) directDebitIn(doc *iso20022.Pacs003) ([]InboundTransaction, error) {
	body := doc.FIToFICstmrDrctDbt
	if err := checkNbOfTxs("DrctDbtTxInf", body.DrctDbtTxInf, body.GrpHdr.NbOfTxs); err != nil {
		return nil, err
	}

	out := make([]InboundTransaction, 0, len(body.DrctDbtTxInf))
	for _, tx := range body.DrctDbtTxInf {
		scheme, amount, err := s.schemeSettling(Pull, tx.IntrBkSttlmAmt)
		if err != nil {
			return nil, err
		}
		mandate := tx.DrctDbtTx.MndtRltdInf.MndtId
		if mandate == "" {
			return nil, fmt.Errorf("%w: DrctDbtTx/MndtRltdInf/MndtId", ErrMandateRequired)
		}
		dbtrID, err := identifierIn("DbtrAcct", tx.DbtrAcct)
		if err != nil {
			return nil, err
		}
		cdtrID, err := identifierIn("CdtrAcct", tx.CdtrAcct)
		if err != nil {
			return nil, err
		}
		out = append(out, InboundTransaction{
			ID: PaymentID(tx.PmtId.TxId),
			Request: InitiatePaymentRequest{
				Scheme:          scheme,
				Debtor:          PartyRef{Identifier: dbtrID},
				Creditor:        PartyRef{Identifier: cdtrID},
				Amount:          amount,
				MandateID:       MandateID(mandate),
				EndToEndID:      endToEndIn(tx.PmtId.EndToEndId),
				Description:     remittanceIn(tx.RmtInf),
				DebtorDetails:   PartyDetails{Agent: agentIn(tx.DbtrAgt), Name: nameIn(tx.Dbtr)},
				CreditorDetails: PartyDetails{Agent: agentIn(tx.CdtrAgt), Name: nameIn(tx.Cdtr)},
			},
		})
	}
	return out, nil
}

// schemeSettling is which of this network's schemes an inbound message
// travelled under, together with its amount in minor units.
//
// The MESSAGE DEFINITION decides the direction and the CURRENCY decides the
// asset, and between them there is exactly one scheme or there is a refusal.
// That is a change from reading the scheme off the message definition alone,
// which said a pacs.008 IS a SEPA credit transfer — true while the only push
// scheme was SEPA's, and a real defect the moment a second one is registered:
// this network's outbound side has always rendered a payment in whatever asset
// its scheme settles in (amountOf takes the scale from the asset), so a system
// that could SEND a dollar credit transfer and not RECEIVE one was
// asymmetrical in a way nothing but a second scheme could reveal. The
// two-asset settlement fixture in cmd/server is what reveals it.
//
// What has NOT changed is that the message does not get to choose. A currency no
// scheme in this direction settles in is refused rather than reinterpreted,
// because a receiver that took the sender's currency and some scheme's scale
// would book a number neither bank wrote down; it is the same ErrAssetMismatch,
// carrying the same meaning, and TestCreditTransferRequestRefusesAMessageInAnotherAsset
// is still what pins it.
//
// Two schemes in one direction and one asset is AMBIGUOUS and is refused too.
// Nothing in this repository registers such a pair, and a receiver that picked
// one of them — the first in ID order, say — would resolve every message under a
// rulebook chosen by alphabet. There is no information in a pacs.008 that could
// break the tie, which is exactly why this is a refusal rather than a rule: what
// a real network has here is the clearing arrangement the message arrived over,
// and this system has one clearing house and no such element.
//
// The scale comes from ledger.LookupAsset on the currency the message names,
// exactly as amountOf takes it from the asset on the way out. Reading the amount
// FIRST, before any scheme is chosen, is what keeps a mismatch reported as what
// it is — a message no scheme settles — rather than as a complaint about the
// number's shape.
func (s *Network) schemeSettling(dir SchemeDirection, amt iso20022.ActiveCurrencyAndAmount) (SchemeID, ledger.Amount, error) {
	minor, asset, err := amountIn(amt)
	if err != nil {
		return "", 0, err
	}
	var found []SchemeID
	for _, sc := range s.ListSchemes() {
		if sc.Direction() == dir && sc.Asset() == asset {
			found = append(found, sc.ID())
		}
	}
	switch len(found) {
	case 1:
		return found[0], minor, nil
	case 0:
		return "", 0, fmt.Errorf("%w: message is in %s and no %s scheme in this network settles in it",
			ErrAssetMismatch, asset, dir)
	default:
		return "", 0, fmt.Errorf("%w: message is in %s and %s schemes %v all settle in it, so nothing in it says which",
			ErrAssetMismatch, asset, dir, found)
	}
}

// amountIn is the inverse of amountOf: a decimal on the wire becomes minor units
// of the asset the message names, at that asset's own scale.
//
// A currency this ledger does not define is refused rather than read at some
// default, because there is no default that is right — the same refusal amountOf
// makes on the way out. Minor refuses a value with more fraction digits than the
// scale allows rather than rounding it, which is what keeps a message this
// system cannot represent exactly from becoming a number it can.
func amountIn(amt iso20022.ActiveCurrencyAndAmount) (ledger.Amount, ledger.AssetCode, error) {
	asset := ledger.AssetCode(amt.Ccy)
	def, err := ledger.LookupAsset(asset)
	if err != nil {
		return 0, "", err
	}
	minor, err := amt.Minor(def.Scale)
	if err != nil {
		return 0, "", fmt.Errorf("%w: %s %s", err, amt.Ccy, amt.Value)
	}
	return ledger.Amount(minor), asset, nil
}

// localPartyIn resolves the ONE side of a received message that belongs to
// this bank, by the address the message quotes for it.
//
// Which side that is follows from the direction and from nothing else: the
// clearing house routed a pacs.008 by CdtrAgt and a pacs.003 by DbtrAgt, so the
// bank holding this message is the creditor's on a push and the debtor's on a
// pull — see CreditTransferRequest and DirectDebitRequest, which are the only
// two callers and each passes the one identifier that is theirs to resolve.
//
// The counterparty is not resolved at all; it is recorded from what the message
// says — see CreditTransferRequest's DebtorDetails/CreditorDetails and
// agentIn/nameIn. The debtor on a push is the SENDING bank's customer, and this
// bank's directory has no authority over whether that account exists.
//
// LOCAL means whose register is searched: this network's own identity rather
// than an argument the caller supplies — see ResolveIdentifier.
func (s *Network) localPartyIn(ctx context.Context, ident deposit.Identifier) (PartyRef, error) {
	var ref PartyRef
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		ref, err = s.addressedPartyTx(ctx, tx, ident)
		return err
	})
	return ref, err
}

// addressedPartyTx turns one quoted address into the party it names.
//
// It drives ResolveIdentifierTx rather than reimplementing the lookup, which
// matters for a property that is easy to lose: two of this bank's accounts
// holding one identifier is ErrIdentifierAmbiguous there, not the first hit, and
// choosing between them here would route a payment on the strength of listing
// order.
//
// # Only a NOT-FOUND becomes a domain error
//
// The outbound direction has the identical discipline. It is a sharper hazard
// here, because for a directory lookup a not-found IS the expected failure and
// `if err != nil { return ErrAccountNotInParticipant }` is the obvious shape. It
// is wrong: that error becomes AC01 "incorrect account number" in the pacs.002
// this system sends back, so a dropped connection or a cancelled context would
// tell another bank its customer's IBAN was bad.
//
// An AMBIGUOUS address is passed through unchanged for the same reason rather
// than a different one. AC01 would again be a false statement about the sender's
// data — the IBAN is fine, and it is this network's own directory that cannot
// say whose it is — so it falls to MS03 through ReasonFor's default, which
// claims only that this agent could not carry the instruction out. See
// TestCreditTransferRequestRefusesAnAddressTwoBanksClaim and
// TestCreditTransferRequestDoesNotBlameTheCounterpartyForAStoreFailure; both
// fail on the collapsed shape.
func (s *Network) addressedPartyTx(ctx context.Context, tx Tx, ident deposit.Identifier) (PartyRef, error) {
	ref, err := s.ResolveIdentifierTx(ctx, tx, ident)
	if errors.Is(err, deposit.ErrIdentifierNotFound) {
		return PartyRef{}, fmt.Errorf("%w: %s", ErrAccountNotInParticipant, ident.Value)
	}
	if err != nil {
		return PartyRef{}, err
	}
	return ref, nil
}

// identifierIn reads the address a received message quotes for one party. It is
// the inverse of ibanOf and cashAccount, and it is where the nil arm they never
// produce actually arrives.
//
// AccountIdentification4Choice's arms are pointers because encoding/xml cannot
// express an xsd:choice. Outbound, cashAccount builds the choice in one place
// from a value that cannot be nil, so no code above ever dereferences it.
// Inbound there is no such guarantee: Othr is a legal arm, a counterparty is
// free to send it, and this system cannot resolve a card PAN or a generic
// identifier to an account. That is ErrUnaddressableAccount — the message
// addresses something this bank cannot address — and it is a refusal rather
// than a dereference of a pointer that is nil for a perfectly ordinary reason.
//
// # Compaction, and why this function does not have to solve it
//
// The value is compacted here, which is a no-op for anything that came off the
// wire — the schema's pattern admits no separators — and is done anyway so that
// what this returns is in one canonical form regardless of who built the
// document. A register stores that same form, so what reaches
// ResolveIdentifierTx from here matches an account's stored address literally.
//
// Where the two forms still part company is a person: an IBAN read off a
// statement is grouped in fours, and one typed may be lower-cased. Reconciling
// that is not this function's job and must not be — a fallback here would help
// no other caller and would mean this package second-guessing the directory it
// was told to use. It belongs to the comparison itself:
// deposit.Identifier.MatchValue canonicalises BOTH sides for the IBAN scheme,
// ListDepositAccountsByIdentifier compares with it, and storetest holds the
// store's SQL to what that Go function says. See
// TestATypedAddressReachesTheStoredOne.
func identifierIn(element string, acct iso20022.CashAccount) (deposit.Identifier, error) {
	if acct.Id.IBAN == nil {
		return deposit.Identifier{}, fmt.Errorf("%w: %s/Id/IBAN", ErrUnaddressableAccount, element)
	}
	iban := acct.Id.IBAN.Compact()
	if err := iban.Validate(); err != nil {
		return deposit.Identifier{}, fmt.Errorf("%w: %s: %w", ErrUnaddressableAccount, element, err)
	}
	return deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: string(iban)}, nil
}

// agentIn reads which bank a message names for one party. It is the inverse of
// agentOf, and — unlike identifierIn — it never refuses: BranchAndFinancialInstitution.validate()
// already required BICFI on the way in for any message that reached Unmarshal
// (see CreditTransferTransaction.validate), so by the time a caller here holds
// a *iso20022.Pacs008 or *iso20022.Pacs003 there is nothing left to check. What
// this reads is recorded on the payment's DebtorDetails/CreditorDetails, never
// resolved against this bank's own directory — that is the whole point of
// localPartyIn's narrowing.
func agentIn(fi iso20022.BranchAndFinancialInstitution) iso20022.BIC {
	return fi.FinInstnId.BICFI
}

// nameIn reads the name a message gives for one party. It is the inverse of
// namedPartyOf, and — like agentIn, for the identical reason — it never
// refuses: validateNamedParty already requires a non-empty Nm on both Dbtr and
// Cdtr for any message that reached Unmarshal (CreditTransferTransaction.validate
// in pacs008.go, DirectDebitTransactionInformation.validate in pacs003.go), so
// a *iso20022.Pacs008 or *iso20022.Pacs003 held here never carries an empty
// one — there is nothing left to check.
func nameIn(p iso20022.PartyIdentification) string {
	return p.Nm
}

// endToEndIn is the inverse of endToEndOf: the value the guidelines reserve for
// "the sender had no reference" comes back as no reference.
//
// Storing the literal would not merely be untidy. EndToEndID is deduplicated —
// SubmitPaymentTx refuses a reference it has already seen, and an empty one is
// never an identity — so every reference-less payment in the network would share
// one reference, and the second to arrive would be rejected as a duplicate of
// the first. Two unrelated customers, colliding on a value neither of them
// chose. See TestCreditTransferRequestReadsNOTPROVIDEDBackAsNoReference, which
// initiates twice for exactly that reason.
func endToEndIn(id string) string {
	if id == "NOTPROVIDED" {
		return ""
	}
	return id
}

// remittanceIn is what the payment is for, or nothing at all. The element is
// optional and the pointer is genuinely nil for most messages; see
// remittanceOf, which omits it rather than sending an empty one.
func remittanceIn(r *iso20022.RemittanceInformation) string {
	if r == nil {
		return ""
	}
	return r.Ustrd
}

// ReadStatus reads a received pacs.002 as what it says about the original
// message and what it says about each transaction in it.
//
// Two return values because they are two different facts, and the bank that
// collected the file acts on
// them at different granularities: the OriginalMessage is how a status is
// matched back to something this system sent — there is no other link, which is
// what makes clearing asynchronous rather than merely delayed — and each report
// is the fate of one payment inside it.
//
// It returns no error, and that is a claim rather than an omission. Every
// element it reads is either mandatory in a document that has been through
// Unmarshal, or optional and absent-means-absent: a missing OrgnlCreDtTm is the
// zero time because the sender did not say, and an acceptance carries no reason
// element because StatusReasonChoice requires exactly one arm and there is
// nothing truthful to put in one. There is no way to hand this function a
// pacs.002 it must refuse, so it does not pretend there is.
//
// Two things in the message are deliberately dropped. GrpSts is derived from the
// per-transaction statuses by the sender — see groupStatusOf — so keeping it
// would be keeping a summary beside the thing it summarises, and a receiver that
// trusted the summary over the detail would act on the wrong one when they
// disagreed. StsId names the STATUS rather than the payment; it is what a later
// query would quote to ask about one particular status, and nothing in this
// system asks such a question yet. Both are recorded here rather than left as
// silent omissions.
func ReadStatus(doc *iso20022.Pacs002) (OriginalMessage, []TransactionStatusReport) {
	rpt := doc.FIToFIPmtStsRpt
	orig := OriginalMessage{
		MsgID:     rpt.OrgnlGrpInfAndSts.OrgnlMsgId,
		MsgDefIdr: rpt.OrgnlGrpInfAndSts.OrgnlMsgNmId,
	}
	if t := rpt.OrgnlGrpInfAndSts.OrgnlCreDtTm; t != nil {
		orig.CreDtTm = t.Time
	}
	out := make([]TransactionStatusReport, 0, len(rpt.TxInfAndSts))
	for _, s := range rpt.TxInfAndSts {
		r := TransactionStatusReport{
			EndToEndID: s.OrgnlEndToEndId,
			TxID:       s.OrgnlTxId,
			Status:     s.TxSts,
		}
		// The code and the text are both kept, because they say different
		// things: the code is what makes a rejection machine-actionable and the
		// text is what says the part no code can. Dropping either is a silent
		// loss — see TransactionStatusReport.
		if s.StsRsnInf != nil {
			r.Text = s.StsRsnInf.AddtlInf
			if s.StsRsnInf.Rsn.Cd != nil {
				r.Code = *s.StsRsnInf.Rsn.Cd
			}
		}
		out = append(out, r)
	}
	return orig, out
}

// ReadSettlement reads a received pacs.009 as the legs it instructs.
//
// Each leg's scale comes from ledger.LookupAsset on that leg's OWN currency,
// never from a constant and never from the first leg's: one settlement message
// may carry legs from several cycles, and a cycle's asset is a property of its
// scheme. That is the same reason TtlIntrBkSttlmAmt is absent on the way out —
// a sum across assets is not a number.
//
// The sender's NbOfTxs is checked against what arrived before any leg is read.
// This is a settlement instruction: a leg lost in transit is a bank that does
// not get paid, and reading the survivors as a complete instruction would settle
// the cycle and leave no record that anything went missing.
func ReadSettlement(doc *iso20022.Pacs009) ([]SettlementLeg, error) {
	body := doc.FICdtTrf
	if err := checkNbOfTxs("CdtTrfTxInf", body.CdtTrfTxInf, body.GrpHdr.NbOfTxs); err != nil {
		return nil, err
	}
	legs := make([]SettlementLeg, 0, len(body.CdtTrfTxInf))
	for i, tx := range body.CdtTrfTxInf {
		amount, asset, err := amountIn(tx.IntrBkSttlmAmt)
		if err != nil {
			return nil, fmt.Errorf("CdtTrfTxInf[%d]: %w", i, err)
		}
		legs = append(legs, SettlementLeg{
			From:      tx.Dbtr.FinInstnId.BICFI,
			To:        tx.Cdtr.FinInstnId.BICFI,
			Amount:    amount,
			Asset:     asset,
			Reference: tx.PmtId.EndToEndId,
		})
	}
	return legs, nil
}

// ReturnInstruction is one payment being returned, as a settlement agent needs
// it: which two agents move reserves, how much, and why.
//
// DebtorAgent and CreditorAgent come off OrgnlTxRef, not off a payment row —
// see iso20022.OriginalTransactionReference for why a settlement agent has no
// row to read them from.
type ReturnInstruction struct {
	PaymentID     PaymentID
	EndToEndID    string
	DebtorAgent   iso20022.BIC
	CreditorAgent iso20022.BIC
	Amount        ledger.Amount
	Asset         ledger.AssetCode
	Reason        string
}

// CodeAndText is a reason code and the free text beside it, joined for a
// ledger description.
//
// Both, because they say different things — the code is what makes a
// rejection or a return machine-actionable in a statement or an exception
// queue, and the text is the part no code can say. The word for "neither was
// given" is the caller's, because CodeAndText serves two callers answering
// different questions: cmd/server's rejectionText, over iso20022.StatusReason,
// and ReturnReason below, over iso20022.ReturnReason — the sibling external code
// set pacs004.go keeps as a separate type precisely so that a rejection
// reason cannot be used as a return reason with nothing to notice. It lives
// here rather than beside its caller because ReturnReason does, and cmd/server
// already imports payment; rejectionText calls through to this rather than
// keeping its own copy of the same four lines.
func CodeAndText(code, text, none string) string {
	switch {
	case code == "" && text == "":
		return none
	case text == "":
		return code
	case code == "":
		return text
	default:
		return code + ": " + text
	}
}

// ReturnReason is what a return is described as where a CUSTOMER's money
// moves: the reason the returning bank gave, code and text.
//
// It lives here rather than beside its caller because ReadReturn needs this
// exact reading for ReturnInstruction.Reason, and cmd/server cannot be imported
// from this package. Bank.receiveReturn calls straight through to it.
//
// The two CUSTOMER legs, and not the reserve reversal between the two banks.
// PostReturnLegTx writes this into the payer's refund and into the payee's
// clawback; SettleReturnTx describes the reversal as the settlement it is,
// because a bank's own position moving carries no customer's reason.
//
// Both arms of the choice are read, because both are legal — iso20022's
// ReturnReasonChoice requires exactly one of a code and a proprietary text,
// and refuses a return that has neither, so what arrives is one or the
// other. The nil case is a caller's guard rather than a message: RtrRsnInf is
// mandatory in a pacs.004 that has been through Unmarshal.
func ReturnReason(info *iso20022.ReturnReasonInformation) string {
	if info == nil {
		return "returned"
	}
	var code string
	switch {
	case info.Rsn.Cd != nil:
		code = string(*info.Rsn.Cd)
	case info.Rsn.Prtry != nil:
		code = *info.Rsn.Prtry
	}
	return CodeAndText(code, info.AddtlInf, "returned")
}

// ReadReturn reads a received pacs.004 as the instructions it carries.
//
// The scale comes from ledger.LookupAsset on the transaction's OWN currency,
// never from a constant — ReadSettlement's reason, one message over: nothing
// here may assume every return in a bulk shares one asset just because this
// system's returns happen to be EUR-only today.
//
// A transaction that names no PAYMENT is refused first, and that one is about
// money rather than about resolution. OrgnlTxId is optional in the schema —
// iso20022.ReturnTransaction.validate accepts a transaction referring back by
// OrgnlEndToEndId alone — and SettleReturnTx derives the idempotency key of the
// reserve reversal from it, so an empty one would move reserves under
// ":return-settle" and make every later nameless return look like a redelivery
// of the first. This is the last point at which nothing has happened yet.
//
// A transaction whose OrgnlTxRef is absent, names only one agent, or names an
// agent with no BICFI, is refused rather than half-read.
// iso20022.ReturnTransaction.validate makes OrgnlTxRef optional — a hard
// requirement would make a return built before this task, or a counterparty
// that has not adopted it, unreadable — so a document that has been through
// Unmarshal can still reach here with nothing to resolve accounts from. This
// function does not assume validate ran at all: it checks both agents'
// presence AND their BICFI itself, rather than trusting either the pointer or
// the value validate would otherwise have guaranteed. ReadSettlement's
// argument applies unchanged: a settlement instruction that cannot be
// resolved to accounts must not be half-acted-on, so this refuses the whole
// message rather than returning some instructions and silently dropping
// others.
func ReadReturn(doc *iso20022.Pacs004) ([]ReturnInstruction, error) {
	body := doc.PmtRtr
	if err := checkNbOfTxs("TxInf", body.TxInf, body.GrpHdr.NbOfTxs); err != nil {
		return nil, err
	}
	ins := make([]ReturnInstruction, 0, len(body.TxInf))
	for i, tx := range body.TxInf {
		if tx.OrgnlTxId == "" {
			return nil, fmt.Errorf(
				"payment: TxInf[%d]: OrgnlTxId is absent; this return names no payment and its reserve reversal would be keyed by nothing",
				i)
		}
		ref := tx.OrgnlTxRef
		if ref == nil || ref.DbtrAgt == nil || ref.CdtrAgt == nil ||
			ref.DbtrAgt.FinInstnId.BICFI == "" || ref.CdtrAgt.FinInstnId.BICFI == "" {
			return nil, fmt.Errorf(
				"payment: TxInf[%d]: OrgnlTxRef names no agents; a settlement agent with no payment row cannot resolve this return",
				i)
		}
		amount, asset, err := amountIn(tx.RtrdIntrBkSttlmAmt)
		if err != nil {
			return nil, fmt.Errorf("TxInf[%d]: %w", i, err)
		}
		ins = append(ins, ReturnInstruction{
			PaymentID:     PaymentID(tx.OrgnlTxId),
			EndToEndID:    tx.OrgnlEndToEndId,
			DebtorAgent:   ref.DbtrAgt.FinInstnId.BICFI,
			CreditorAgent: ref.CdtrAgt.FinInstnId.BICFI,
			Amount:        amount,
			Asset:         asset,
			// RtrdIntrBkSttlmAmt, not OrgnlIntrBkSttlmAmt: the two are equal in
			// this system's own returns, which are always whole, but a
			// settlement agent moves the amount actually coming back, and only
			// RtrdIntrBkSttlmAmt says that under the standard's own partial-
			// return shape — see the comment on ReturnMessage's construction of
			// the two, and iso20022.ReturnTransaction.
			Reason: ReturnReason(tx.RtrRsnInf),
		})
	}
	return ins, nil
}

// checkNbOfTxs holds a sender to its own count, and every reader of a file
// begins with it.
//
// The count is CHECKED and not recomputed, which is the whole reason the element
// is a string carrying the sender's assertion — see CreditTransferGroupHeader. A
// receiver that recomputed it would never notice a truncated file, and a
// truncated file read as a complete one is a payment that silently never
// happened. TestSettlementMessageNbOfTxsSurvivesATruncatedFile pins that neither
// the encoder nor the decoder touches it, so a file that lost a transaction in
// transit arrives saying so.
//
// Neither refusal is a sentinel from errors.go, and that is deliberate. There is
// no condition in this system's own vocabulary for "the file you sent
// contradicts itself" — the same situation namedPartyOf is in — so both fall to
// MS03 through ReasonFor's default, which says "this agent could not carry it
// out". The free text beside the code is where the detail reaches the sender,
// and it says which count disagreed.
func checkNbOfTxs[T any](element string, txs []T, nbOfTxs string) error {
	declared, err := strconv.Atoi(nbOfTxs)
	if err != nil {
		return fmt.Errorf("payment: GrpHdr/NbOfTxs is %q, which is not a count", nbOfTxs)
	}
	if declared != len(txs) {
		return fmt.Errorf("payment: GrpHdr/NbOfTxs declares %d transactions and %s carries %d",
			declared, element, len(txs))
	}
	return nil
}

// SettlementLeg is one bank's movement in a settlement instruction: who pays,
// who is paid, how much, and which closed cycle it discharges.
//
// It carries its own Asset because a settlement message may hold legs from
// several cycles, and a cycle's asset is a property of its scheme. Both parties
// are BICs rather than participant ids because this message goes to the central
// bank, which knows banks by BIC and has never heard of this system's ids.
type SettlementLeg struct {
	From      iso20022.BIC
	To        iso20022.BIC
	Amount    ledger.Amount
	Asset     ledger.AssetCode
	Reference string
}

// SettlementLegsOf turns a closed cycle's net positions into the legs a
// settlement instruction carries.
//
// Every position is a claim on or an obligation to the SETTLEMENT AGENT and
// never a bilateral one between two members — that is precisely what netting
// destroys — so a net payer's leg runs from that bank TO the central bank and a
// net receiver's from the central bank to it. The reference on every leg is the
// CYCLE, which is what the central bank reads to know what it is being asked to
// discharge.
//
// Members are visited in sorted BIC order rather than map order. The legs are
// the message's transactions AND the entries of the settlement agent's own
// posting, so map iteration would produce a different byte sequence on the wire
// and a different stored transaction on every run.
//
// A position of zero is left out. It nets to nothing, and a leg for it would be
// an IntrBkSttlmAmt of zero, which the codec refuses (ActiveCurrencyAndAmount
// requires a positive amount) and which would say a bank was instructed to move
// nothing.
//
// # It lives here because two callers have to agree
//
// The settlement agent works from the LEGS rather than from the cycle, because
// it holds no cycles table. So two callers have to produce the same legs: the
// clearing house, which renders them into a pacs.009, and the seed, which
// settles its own cut-offs by playing all three institutions rather than
// instructing one. Two renderings of one intent are two things that can drift,
// and this is the one that decides what settles.
func SettlementLegsOf(c ClearingCycle, asset ledger.AssetCode, centralBank iso20022.BIC) []SettlementLeg {
	legs := make([]SettlementLeg, 0, len(c.NetPositions))
	// A cycle's positions are keyed by BIC and a leg is addressed by BIC, so there
	// is nothing between the two. See ClearingCycle.NetPositions.
	for _, bic := range slices.Sorted(maps.Keys(c.NetPositions)) {
		net := c.NetPositions[bic]
		if net == 0 {
			continue
		}
		leg := SettlementLeg{From: bic, To: centralBank, Amount: -net, Asset: asset, Reference: string(c.ID)}
		if net > 0 {
			leg.From, leg.To, leg.Amount = centralBank, bic, net
		}
		legs = append(legs, leg)
	}
	return legs
}

// NetsToNothing reports whether a cycle leaves nobody with anything to
// discharge: every member's position is zero, or it took nothing in at all.
//
// It is the whole-cycle form of the rule SettlementLegsOf applies per position,
// and it is a state with two quite different causes — a scheme that saw no
// traffic today, and a batch in which every bank's payments and receipts
// cancelled exactly. Neither yields an instruction, because a pacs.009 with no
// transaction is not a message, so such a cut-off is discharged by the clearing
// house itself and NO settlement is recorded against it anywhere. Two callers
// need that rule: the institution that would otherwise wait for an answer that
// never comes, and the reconciliation harness, which would otherwise read a
// settled cycle with no settlement row as a break.
func NetsToNothing(c ClearingCycle) bool {
	for _, net := range c.NetPositions {
		if net != 0 {
			return false
		}
	}
	return true
}

// SettlementMessage renders a closed cycle's net positions as the pacs.009 that
// instructs the central bank to settle them.
//
// A pacs.009 and not a pacs.008 because BOTH parties are banks: a pacs.008
// moves a customer's money and names two customers, this moves a bank's own
// money and names two banks. The compiler enforces the difference — Dbtr and
// Cdtr here are agents, not parties — which is why a customer cannot end up in
// a settlement instruction by mistake.
func SettlementMessage(legs []SettlementLeg, mc MessageContext) (iso20022.Envelope, error) {
	if len(legs) == 0 {
		return iso20022.Envelope{}, fmt.Errorf("%w: CdtTrfTxInf", iso20022.ErrMissingElement)
	}
	txs := make([]iso20022.FinancialInstitutionCreditTransferTransaction, 0, len(legs))
	for _, leg := range legs {
		amt, err := amountOf(leg.Amount, leg.Asset)
		if err != nil {
			return iso20022.Envelope{}, err
		}
		txs = append(txs, iso20022.FinancialInstitutionCreditTransferTransaction{
			PmtId:          iso20022.PaymentIdentification{EndToEndId: leg.Reference},
			IntrBkSttlmAmt: amt,
			IntrBkSttlmDt:  iso20022.ISODate{Time: mc.Now},
			Dbtr:           agentOf(leg.From),
			Cdtr:           agentOf(leg.To),
		})
	}

	doc := &iso20022.Pacs009{FICdtTrf: iso20022.FIToFIFinancialInstitutionCreditTransfer{
		GrpHdr: iso20022.CreditTransferGroupHeader{
			MsgId:   mc.MsgID,
			CreDtTm: iso20022.ISODateTime{Time: mc.Now},
			// NbOfTxs is what the SENDER asserts, not a derivation the receiver
			// would recompute. A receiver that recomputed it instead of checking
			// it would never notice a truncated file, which is the whole reason
			// the element exists — and it is a settlement instruction, where a
			// silently dropped leg is a bank that does not get paid. See
			// TestSettlementMessageNbOfTxsSurvivesATruncatedFile.
			NbOfTxs: strconv.Itoa(len(txs)),
			// TtlIntrBkSttlmAmt is deliberately absent. The legs of one message
			// may be denominated in different assets, and a sum across assets is
			// not a number; the standard's single total has nowhere to say which
			// asset it is in. Omitting an optional element beats asserting a
			// figure that is only correct when every leg happens to agree.
			IntrBkSttlmDt: iso20022.ISODate{Time: mc.Now},
			SttlmInf:      clearingSettlement(),
		},
		CdtTrfTxInf: txs,
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// ---------------------------------------------------------------------------
// The statement
// ---------------------------------------------------------------------------

// StatementMessage renders one member's share of a settlement as the camt.053
// that tells it.
//
// One member per message, because each goes to a different bank and a statement
// is addressed to the holder of the account it is about. The standard permits
// several Stmt elements in one document and this system never builds one: a
// second statement in this message would be about an account the recipient does
// not hold.
//
// # The sign leaves the amount and becomes a word
//
// ActiveCurrencyAndAmount cannot be negative — NewAmount refuses it — so the
// magnitude goes in Amt and the direction in CdtDbtInd, on BOTH the entry and
// the balance. That is the standard's shape everywhere and it is the same
// separation ledger.Entry makes with Direction. Reconstructing the sign is
// ReadStatement's job, and losing it there would make a member post its mirror
// leg backwards.
//
// # The reference rides on AcctSvcrRef
//
// A member bank has no cycles — it never sees a batch — and no other
// institution's payment ids either, so the only way it can tell what a reserve
// movement discharged is for the central bank to say. AcctSvcrRef is the
// servicer's own reference for the entry, which is exactly what a cycle id is
// from the central bank's side on the cut-off path, and what a payment id is on
// the return path — both equally opaque to the member reading them.
func StatementMessage(st SettlementStatement, mc MessageContext) (iso20022.Envelope, error) {
	if st.Account == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: Stmt/Acct/Id/Othr/Id", iso20022.ErrMissingElement)
	}
	if st.Reference == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: Ntry/AcctSvcrRef", iso20022.ErrMissingElement)
	}
	entryAmt, entryInd, err := signedAmountOf(st.Movement, st.Asset)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	balAmt, balInd, err := signedAmountOf(st.ClosingBalance, st.Asset)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	day := iso20022.ISODate{Time: ledger.DayStart(st.ValueDate)}

	doc := &iso20022.Camt053{BkToCstmrStmt: iso20022.BankToCustomerStatement{
		GrpHdr: iso20022.StatementGroupHeader{
			MsgId:   mc.MsgID,
			CreDtTm: iso20022.ISODateTime{Time: mc.Now},
		},
		Stmt: []iso20022.AccountStatement{{
			Id:      st.StatementRef,
			CreDtTm: iso20022.ISODateTime{Time: mc.Now},
			Acct: iso20022.CashAccount{Id: iso20022.AccountIdentification4Choice{
				Othr: &iso20022.GenericAccountIdentification{Id: string(st.Account)},
			}},
			Bal: []iso20022.CashBalance{{
				Tp:        iso20022.BalanceTypeChoice{CdOrPrtry: iso20022.BalanceTypeCode{Cd: iso20022.BalanceTypeClosingBooked}},
				Amt:       balAmt,
				CdtDbtInd: balInd,
				Dt:        iso20022.DateAndDateTime{Dt: &day},
			}},
			Ntry: []iso20022.StatementEntry{{
				Amt:         entryAmt,
				CdtDbtInd:   entryInd,
				Sts:         iso20022.EntryStatusChoice{Cd: iso20022.EntryStatusBooked},
				BookgDt:     iso20022.DateAndDateTime{Dt: &day},
				ValDt:       iso20022.DateAndDateTime{Dt: &day},
				AcctSvcrRef: st.Reference,
				// What kind of movement this is, and the schema's one mandatory
				// child of an entry. See iso20022.BankTransactionCode for why the
				// proprietary arm rather than a domain code, and for why there is
				// one code here rather than one per flow.
				BkTxCd: iso20022.BankTransactionCode{
					Prtry: iso20022.ProprietaryBankTransactionCode{
						Cd:   iso20022.BankTransactionCodeSettlement,
						Issr: iso20022.BankTransactionCodeIssuer,
					},
				},
				// Named after the reference and nothing more: a cycle id and a payment
				// id are equally opaque to the member reading this, and the sender
				// saying which kind it sent would be telling that bank something it has
				// no row to resolve.
				AddtlNtryInf: "Settlement of " + st.Reference,
			}},
		}},
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// signedAmountOf splits a signed ledger amount into the magnitude the standard
// carries and the word that says which way it runs.
//
// Zero is a CREDIT of nothing. It is reachable on a BALANCE — a member whose
// reserve is exactly empty — and never on an entry, because a position of zero
// produces no leg and therefore no statement. Choosing CRDT for it is arbitrary
// and stated rather than left to be inferred; DBIT would read the same on the
// wire and neither means anything.
func signedAmountOf(amt ledger.Amount, asset ledger.AssetCode) (iso20022.ActiveCurrencyAndAmount, iso20022.CreditDebitCode, error) {
	ind := iso20022.CreditDebitCredit
	magnitude := amt
	if amt < 0 {
		ind, magnitude = iso20022.CreditDebitDebit, -amt
	}
	out, err := amountOf(magnitude, asset)
	if err != nil {
		return iso20022.ActiveCurrencyAndAmount{}, "", err
	}
	return out, ind, nil
}

// AdvisedMovement is what a member bank can see in a statement about its own
// reserve account: the movement, the balance it was left at, and the reference
// that names what caused it.
//
// It is a DIFFERENT type from SettlementStatement, deliberately. That one is
// what the sender knew; this is what the receiver can learn. ParticipantID never
// reaches the wire at all, because a member bank has no use for the central
// bank's internal name for itself. StatementRef is on the wire, in Stmt/Id, but
// ReadStatement does not surface it: it is the account servicer's own reference
// for the STATEMENT, and on the return path it is not any row's key at all (see
// SettlementStatement). A member has no way to check either against anything it
// holds. Collapsing the two types would put fields on the receiving side that
// are either always empty or copied from the wire with nothing to verify them
// against.
//
// Reference is the one identifier that DOES cross: it is AcctSvcrRef, read back
// exactly as StatementMessage wrote it, and it is exactly as opaque to this bank
// as StatementRef is — a cycle id or a payment id, and a member bank holds
// neither. See SettlementAdvice's doc for why that opacity is fine: Reference
// exists to be quoted back and to key this bank's own advice row, not to be
// resolved to anything.
type AdvisedMovement struct {
	Account        ledger.AccountID
	Asset          ledger.AssetCode
	Movement       ledger.Amount
	ClosingBalance ledger.Amount
	Reference      string

	// ValueDate is CARRIED AND UNREAD, and that is recorded rather than left to
	// be discovered.
	//
	// It makes the full round trip — SettlementStatement.ValueDate goes out in
	// Ntry/ValDt, ReadStatement puts it back here — and PostSettlementAdviceTx
	// posts the mirror leg without it, so the ledger resolves that posting's
	// value date to its booking date instead.
	//
	// It is read here rather than dropped because the alternative is worse: a
	// reader that skipped ValDt would make the field unavailable to the caller
	// that eventually wants it, and a value date discarded on receipt is one
	// nobody can go back for — the same argument ClosingBalance is stored
	// under, and ClosingBalance is unread too.
	//
	// Using it is not a one-line change and is not obviously right, which is why
	// this is a note and not a fix. It would alter every mirror-leg transaction
	// this system stores, and the question it settles — whether a bank's reserve
	// mirror takes effect on the settlement agent's value date or on the day the
	// bank actually booked it — is a domain decision with a reconciliation
	// consequence.
	ValueDate time.Time
}

// ReadStatement reads a received camt.053 as the movements it advises.
//
// One entry per statement is REQUIRED and more is refused whole. This system's
// central bank posts exactly one netting movement per member per cycle, so a
// statement carrying two is one this reader has no rule for — and posting the
// first while dropping the second would move a bank's reserve mirror by the wrong
// amount with nothing anywhere recording it. It is the argument creditTransferIn
// makes for refusing a file entire, made about a message that carries one entry
// by nature rather than by accumulation.
//
// A statement with no CLBD balance is refused for the reason camt.053 was chosen
// over camt.054: without it there is nothing to check a posting against, and a
// message that cannot be checked is a notification wearing a statement's name.
//
// The scale comes from ledger.LookupAsset on the entry's OWN currency, never
// from a constant — the same rule ReadSettlement follows one message over.
func ReadStatement(doc *iso20022.Camt053) ([]AdvisedMovement, error) {
	stmts := doc.BkToCstmrStmt.Stmt
	out := make([]AdvisedMovement, 0, len(stmts))
	for i, s := range stmts {
		if len(s.Ntry) != 1 {
			return nil, fmt.Errorf("payment: Stmt[%d] carries %d entries; a settlement statement advises one movement", i, len(s.Ntry))
		}
		entry := s.Ntry[0]
		movement, asset, err := signedAmountIn(entry.Amt, entry.CdtDbtInd)
		if err != nil {
			return nil, fmt.Errorf("Stmt[%d]/Ntry/Amt: %w", i, err)
		}
		closing, ok, err := closingBalanceIn(s.Bal)
		if err != nil {
			return nil, fmt.Errorf("Stmt[%d]/Bal: %w", i, err)
		}
		if !ok {
			return nil, fmt.Errorf("payment: Stmt[%d] carries no CLBD balance; a statement with nothing to check against is a notification", i)
		}
		acct := s.Acct.Id
		if acct.Othr == nil {
			return nil, fmt.Errorf("payment: Stmt[%d]/Acct is not identified by Othr; a reserve account has no IBAN", i)
		}
		day := time.Time{}
		if entry.ValDt.Dt != nil {
			day = entry.ValDt.Dt.Time
		}
		out = append(out, AdvisedMovement{
			Account:        ledger.AccountID(acct.Othr.Id),
			Asset:          asset,
			Movement:       movement,
			ClosingBalance: closing,
			Reference:      entry.AcctSvcrRef,
			ValueDate:      day,
		})
	}
	return out, nil
}

// signedAmountIn puts the sign back on: the magnitude the standard carried and
// the word beside it become one signed ledger amount.
//
// An indicator that is neither CRDT nor DBIT is REFUSED rather than defaulted.
// Defaulting to credit would turn an unreadable direction into a reserve
// increase, which is the most expensive way to be wrong about a settlement.
func signedAmountIn(amt iso20022.ActiveCurrencyAndAmount, ind iso20022.CreditDebitCode) (ledger.Amount, ledger.AssetCode, error) {
	value, asset, err := amountIn(amt)
	if err != nil {
		return 0, "", err
	}
	switch ind {
	case iso20022.CreditDebitCredit:
		return value, asset, nil
	case iso20022.CreditDebitDebit:
		return -value, asset, nil
	default:
		return 0, "", fmt.Errorf("payment: CdtDbtInd is %q, which says neither credit nor debit", ind)
	}
}

// closingBalanceIn finds the CLBD balance among however many a statement
// carries, and reports whether there was one.
//
// It searches rather than taking Bal[0] because the standard permits several and
// their order is not fixed; a reader that took the first would eventually read
// an opening balance as a closing one, and the two differ by exactly the entries
// this message exists to advise.
func closingBalanceIn(bals []iso20022.CashBalance) (ledger.Amount, bool, error) {
	for _, b := range bals {
		if b.Tp.CdOrPrtry.Cd != iso20022.BalanceTypeClosingBooked {
			continue
		}
		value, _, err := signedAmountIn(b.Amt, b.CdtDbtInd)
		if err != nil {
			return 0, false, err
		}
		return value, true, nil
	}
	return 0, false, nil
}

// ---------------------------------------------------------------------------
// The lodgement: camt.050 out, camt.025 back
// ---------------------------------------------------------------------------

// LodgementMessage renders a member's request for a reserve credit as the
// camt.050 that carries it.
//
// It is a free function and not a method on Network: it reads no store and needs
// no scheme, because everything on the wire is on the instruction or in the
// context.
//
// # It names the account it credits and not the one it debits
//
// CdtrAcct is the reserve account, in the central bank's book, and it is the whole
// instruction. DbtrAcct is left ABSENT — the account being debited is this bank's
// vault cash, in this bank's own book, which the central bank neither keeps nor
// may read. Naming it would be quoting a servicer the number of an account in a
// ledger it has no access to, which is the same reason a pacs.008 does not carry
// the payer's GL account. See iso20022.LiquidityTransfer, where the asymmetry is
// recorded on the type, and TestTheLodgementNamesTheAccountItCreditsAndNotTheOneItDebits.
//
// # The reference is the message id, and it has to be
//
// There is no process id above the two messages, because a lodgement is one
// request and one answer; what the receipt quotes back is this document's own
// MsgId. So the instruction's Ref and
// the context's MsgID must be the same value, and this refuses a caller that
// disagrees rather than silently picking one — a receipt correlated against the
// wrong one of the two would match nothing the bank ever sent.
func LodgementMessage(in LodgementInstruction, mc MessageContext) (iso20022.Envelope, error) {
	if err := in.BIC.Validate(); err != nil {
		return iso20022.Envelope{}, fmt.Errorf("payment: the lodging member's address: %w", err)
	}
	if err := mc.To.Validate(); err != nil {
		return iso20022.Envelope{}, fmt.Errorf("payment: Cdtr: %w", err)
	}
	if in.Account == "" {
		return iso20022.Envelope{}, fmt.Errorf(
			"%w: CdtrAcct/Id; a lodgement naming no reserve account is one no servicer can post",
			iso20022.ErrMissingElement)
	}
	if in.Ref == "" {
		return iso20022.Envelope{}, fmt.Errorf(
			"%w: LqdtyTrfId/EndToEndId; nothing else correlates the receipt", iso20022.ErrMissingElement)
	}
	if in.Ref != mc.MsgID {
		return iso20022.Envelope{}, fmt.Errorf(
			"payment: this lodgement is referenced %q and the message it would travel as is %q; "+
				"the receipt quotes the message id, so the two cannot differ", in.Ref, mc.MsgID)
	}
	amount, err := amountOf(in.Amount, in.Asset)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	doc := &iso20022.Camt050{LqdtyCdtTrf: iso20022.LiquidityCreditTransferV5{
		MsgHdr: iso20022.MessageHeader{MsgId: mc.MsgID, CreDtTm: iso20022.ISODateTime{Time: mc.Now}},
		LqdtyCdtTrf: iso20022.LiquidityTransfer{
			LqdtyTrfId: iso20022.LiquidityTransferIdentification{EndToEndId: in.Ref},
			Cdtr:       agentOf(mc.To),
			CdtrAcct: iso20022.CashAccount{Id: iso20022.AccountIdentification4Choice{
				// The generic arm, not the IBAN one: a reserve account at a central
				// bank has no IBAN, because it is not a payment address and no
				// customer ever quotes it. The same choice camt.053's Acct makes,
				// for the same reason.
				Othr: &iso20022.GenericAccountIdentification{Id: string(in.Account)},
			}},
			TrfdAmt: iso20022.TransferredAmount{AmtWthCcy: amount},
			Dbtr:    agentOf(in.BIC),
		},
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// ReadLodgement reads a received camt.050 as the instruction it carries: which
// member, which account, how much, and in what.
//
// # The header is compared against the document, and that is the point
//
// Dbtr and Cdtr both duplicate something the AppHdr already says — Fr and To —
// and this is where the duplication earns its keep. A message whose Dbtr is not
// its sender is a member lodging on somebody else's behalf, and a message whose
// Cdtr is not its recipient asks THIS servicer to post in ANOTHER servicer's
// book. Neither is a thing this system permits, and neither is detectable from
// the document alone.
//
// So this takes the header as an argument rather than only the document, which is
// the one reader in this file that does. The payment family's readers do not, and
// the difference is real: a pacs.008 is RELAYED — it travels bank to clearing
// house to bank, so its Fr is the last hop rather than the debtor's agent, and a
// comparison there would refuse every legitimate message. A camt.050 goes
// straight from the member to its central bank, one hop, so the header and the
// body must agree.
//
// # It does not assume validate() ran
//
// iso20022.Unmarshal validates, so every element checked below has already been
// through LiquidityTransfer.validate. This checks anyway, for ReadReturn's
// reason: a document handed to this function need not have come from Unmarshal at
// all, and the cost of being wrong is a credit posted to a reserve account on
// nobody's authority.
func ReadLodgement(hdr iso20022.AppHdr, doc *iso20022.Camt050) (LodgementInstruction, error) {
	body := doc.LqdtyCdtTrf
	trf := body.LqdtyCdtTrf

	ref := body.MsgHdr.MsgId
	if ref == "" {
		return LodgementInstruction{}, fmt.Errorf(
			"%w: MsgHdr/MsgId; this request cannot be answered because nothing would correlate the receipt",
			iso20022.ErrMissingElement)
	}
	member := trf.Dbtr.FinInstnId.BICFI
	if member == "" {
		return LodgementInstruction{}, fmt.Errorf(
			"%w: Dbtr/FinInstnId/BICFI; this request names no member and its reserve would be keyed by nothing",
			iso20022.ErrMissingElement)
	}
	if err := member.Validate(); err != nil {
		return LodgementInstruction{}, fmt.Errorf("payment: Dbtr/FinInstnId/BICFI: %w", err)
	}
	if from := hdr.Fr.FIId.FinInstnId.BICFI; from != member {
		return LodgementInstruction{}, fmt.Errorf(
			"payment: this lodgement was sent by %s and asks to debit %s; a member lodges its own cash",
			from, member)
	}
	agent := trf.Cdtr.FinInstnId.BICFI
	if agent == "" {
		return LodgementInstruction{}, fmt.Errorf(
			"%w: Cdtr/FinInstnId/BICFI; this request names no account servicer", iso20022.ErrMissingElement)
	}
	if to := hdr.To.FIId.FinInstnId.BICFI; to != agent {
		return LodgementInstruction{}, fmt.Errorf(
			"payment: this lodgement was addressed to %s and names %s as the account servicer; "+
				"one servicer cannot post in another's book", to, agent)
	}
	// The generic arm. An IBAN here would be a reserve account addressed as a
	// payment address, which is what GenericAccountIdentification exists to avoid
	// — see LodgementMessage.
	if trf.CdtrAcct.Id.Othr == nil || trf.CdtrAcct.Id.Othr.Id == "" {
		return LodgementInstruction{}, fmt.Errorf(
			"%w: CdtrAcct/Id/Othr/Id; this request says which member and not which account",
			iso20022.ErrMissingElement)
	}
	amount, asset, err := amountIn(trf.TrfdAmt.AmtWthCcy)
	if err != nil {
		return LodgementInstruction{}, err
	}
	if amount <= 0 {
		return LodgementInstruction{}, fmt.Errorf("%w: TrfdAmt is %s", ErrInvalidPaymentAmount, trf.TrfdAmt.AmtWthCcy.Value)
	}
	return LodgementInstruction{
		BIC:     member,
		Agent:   agent,
		Account: ledger.AccountID(trf.CdtrAcct.Id.Othr.Id),
		Asset:   asset,
		Amount:  amount,
		Ref:     ref,
	}, nil
}

// LodgementReceiptMessage renders an account servicer's answer as the camt.025
// that carries it.
//
// # The reason is truncated here rather than discovered on the wire
//
// Desc is Max140Text, and the refusals that reach it are Go error strings built
// by ReceiveLodgementTx and settlementAccountTx — which quote a BIC, an asset and
// two account ids, and can exceed 140 characters between them. A document that
// would not marshal is worse than a shortened reason: the member would be told
// nothing at all, and the servicer's handler would report its own answer as a
// file it could not build.
//
// So this truncates, and it truncates VISIBLY, with an ellipsis, so that a reader
// of a shortened reason can tell it was shortened. The limit is called out on
// iso20022.RequestHandling rather than left to be met here, because it is the
// schema's rather than this system's.
func LodgementReceiptMessage(r LodgementReceipt, mc MessageContext) (iso20022.Envelope, error) {
	if r.Ref == "" {
		return iso20022.Envelope{}, fmt.Errorf(
			"%w: RctDtls/OrgnlMsgId/MsgId; a receipt naming no request answers nothing",
			iso20022.ErrMissingElement)
	}
	if r.Status == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: RctDtls/ReqHdlg/StsCd", iso20022.ErrMissingElement)
	}
	handling := iso20022.RequestHandling{StsCd: string(r.Status)}
	if !r.Accepted() {
		// A refusal that says nothing is one nobody can act on, and the schema does
		// not require a reason here — so the requirement is this system's.
		if r.Reason == "" {
			return iso20022.Envelope{}, fmt.Errorf(
				"%w: RctDtls/ReqHdlg/Desc on a refusal", iso20022.ErrMissingElement)
		}
		handling.Desc = truncateTo(r.Reason, 140)
	}
	doc := &iso20022.Camt025{Rct: iso20022.ReceiptV5{
		MsgHdr: iso20022.ReceiptMessageHeader{MsgId: mc.MsgID, CreDtTm: iso20022.ISODateTime{Time: mc.Now}},
		RctDtls: []iso20022.ReceiptDetails{{
			OrgnlMsgId: iso20022.OriginalMessageAndIssuer{
				MsgId: r.Ref,
				// The request's message definition, so that a member holding more
				// than one kind of outstanding request can dispatch this answer
				// without a table only it has. See iso20022.OriginalMessageAndIssuer.
				MsgNmId: iso20022.Camt050{}.MessageDefinitionIdentifier(),
			},
			ReqHdlg: []iso20022.RequestHandling{handling},
		}},
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// truncateTo shortens a reason to fit an element's maximum length, marking that
// it was shortened.
//
// The ellipsis is inside the limit rather than added to it, which is the whole
// reason this is a function: the obvious version returns limit+1 characters and
// produces exactly the invalid document it was written to prevent.
//
// It cuts on a RUNE boundary. Max140Text counts characters and Go slices bytes,
// so cutting at 140 bytes through a multi-byte rune would emit invalid UTF-8 —
// which ledger.ValidateText refuses elsewhere in this repository and which no
// XML parser will accept.
func truncateTo(s string, limit int) string {
	if len([]rune(s)) <= limit {
		return s
	}
	const ellipsis = "…"
	runes := []rune(s)
	return string(runes[:limit-len([]rune(ellipsis))]) + ellipsis
}

// ReadLodgementReceipt reads a received camt.025 as the answer it carries.
//
// ONE receipt detail and one handling, and this refuses more rather than reading
// the first. The schema permits a servicer to acknowledge several requests in one
// document; this system asks about one lodgement at a time, so a receipt naming
// two is one it has no rule for — and acting on the first while dropping the
// second is cycleOf's mistake, which would leave a member believing a lodgement
// was answered when the answer was about a different one.
//
// It reads no amount, because there is none to read. See iso20022.Camt025.
func ReadLodgementReceipt(doc *iso20022.Camt025) (LodgementReceipt, error) {
	details := doc.Rct.RctDtls
	if n := len(details); n != 1 {
		return LodgementReceipt{}, fmt.Errorf(
			"payment: this system asks about one lodgement at a time and RctDtls carries %d", n)
	}
	d := details[0]
	if d.OrgnlMsgId.MsgId == "" {
		return LodgementReceipt{}, fmt.Errorf(
			"%w: RctDtls/OrgnlMsgId/MsgId; nothing on this receipt says which request it answers",
			iso20022.ErrMissingElement)
	}
	if n := len(d.ReqHdlg); n != 1 {
		return LodgementReceipt{}, fmt.Errorf(
			"payment: one request was made and ReqHdlg carries %d outcomes for it", n)
	}
	h := d.ReqHdlg[0]
	if h.StsCd == "" {
		return LodgementReceipt{}, fmt.Errorf(
			"%w: RctDtls/ReqHdlg/StsCd; this receipt says a request arrived and not what became of it",
			iso20022.ErrMissingElement)
	}
	return LodgementReceipt{
		Ref:    d.OrgnlMsgId.MsgId,
		Status: iso20022.TransactionStatus(h.StsCd),
		Reason: h.Desc,
	}, nil
}
