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
// This file is payment's translation layer: the conversion between this system's
// domain types and the ISO 20022 messages that carry them between banks. It lives
// here rather than in iso20022 because that package imports nothing from this
// repository, deliberately — a translator inside it would be the same import
// pointing the other way, and the claim "these are the standard's types" would stop
// being checkable.
//
// Name duplicates the identifier as a string so the test can compare the table
// against errors.go's own declarations without reflection, which cannot see
// package-level var names. A second test asserts the Err values are pairwise
// distinct and as numerous as the declarations, so a mislabelled entry either
// collides or leaves a name uncovered.
type reasonMapping struct {
	Err  error
	Name string
	Code iso20022.StatusReason
}

// reasonTable maps this package's sentinels to wire codes, undriftably.
//
// EVERY sentinel in errors.go must appear. An error that gets no code is mapped to
// the empty one with a comment saying why, rather than omitted — omission is
// indistinguishable from an oversight, which is the exact failure this table exists
// to prevent. TestReasonTableCoversEverySentinel parses errors.go and fails on any
// gap.
//
// The empty code means two different things and the comment on each entry says
// which. Most are errors that CANNOT reach a counterparty: this system's own
// bookkeeping failing, with nothing truthful to tell a sender. The admission
// refusals are the other kind — each is a real answer, going back to whoever is
// provisioning the bank. The codes here are the pacs.002's external set, which holds
// nothing for an account-opening refusal either way.
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

	// The same code for a narrower fact, and the code set's own gloss covers both:
	// RC01 is "the BIC does not identify a reachable participant", true of a bank that
	// does not exist and equally of one this scheme has not admitted. It is in this
	// block because it does reach a counterparty — when the PAYEE's bank is the
	// non-member, the clearing house turns AcceptAtCSMTx's refusal into the RJCT its
	// submitter reverses on. In the other direction the bank to be told is the
	// non-member itself and has no download queue, so that direction is refused at the
	// submitting bank's own door instead.
	{ErrBankNotAdmitted, "ErrBankNotAdmitted", iso20022.StatusReasonBankIdentifierIncorrect},
	{ErrUnaddressableAccount, "ErrUnaddressableAccount", iso20022.StatusReasonMissingDebtorAccountOrIdentification},
	{ErrIdentifierMismatch, "ErrIdentifierMismatch", iso20022.StatusReasonMissingDebtorAccountOrIdentification},
	{ErrAmbiguousAddress, "ErrAmbiguousAddress", iso20022.StatusReasonMissingDebtorAccountOrIdentification},
	{ErrCycleNotOpen, "ErrCycleNotOpen", iso20022.StatusReasonInvalidCutOffTime},

	// A settlement instruction the agent cannot read as one batch, and unlike every
	// cycle error above it IS a judgement about the message. The clearing house sent
	// it and can fix it, so it is answered rather than swallowed.
	//
	// MS03, which is where ReasonFor's default would have put it — listed EXPLICITLY
	// because "no entry" and "classified as MS03 on purpose" are indistinguishable
	// from the table and only one is a decision anybody made.
	{ErrInvalidSettlement, "ErrInvalidSettlement", iso20022.StatusReasonNotSpecifiedAgentGenerated},

	// MS03 for a stronger reason than "no better code exists": in SEPA a currency
	// mismatch cannot happen, the scheme being euro-only, so the code set never needed
	// one. That this repository can produce the error at all is a consequence of its
	// multi-asset ledger.
	{ErrAssetMismatch, "ErrAssetMismatch", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrSchemeNotFound, "ErrSchemeNotFound", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrInvalidPaymentAmount, "ErrInvalidPaymentAmount", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrParticipantAssetNotFound, "ErrParticipantAssetNotFound", iso20022.StatusReasonNotSpecifiedAgentGenerated},
	{ErrSchemeUnsupportedReturn, "ErrSchemeUnsupportedReturn", iso20022.StatusReasonNotSpecifiedAgentGenerated},

	// An instruction naming no counterparty is refused at submission, before any leg
	// posts and before any message could exist to carry a reason back — classified
	// here alongside the other malformed-instruction refusals because nothing
	// distinguishes it from them.
	{ErrCounterpartyNotNamed, "ErrCounterpartyNotNamed", iso20022.StatusReasonNotSpecifiedAgentGenerated},

	// An on-us instruction is the same category again — an instruction this scheme
	// cannot carry, refused at submission — and MS03 because the code set has nothing
	// for "both of these parties are yours".
	//
	// It is the one refusal here that could never reach a counterparty even in
	// principle: the two agents are one bank, so the message would be addressed to its
	// sender. What the payer is told instead is api's 422 and the remedy beside it.
	{ErrOnUsPayment, "ErrOnUsPayment", iso20022.StatusReasonNotSpecifiedAgentGenerated},

	// Its sibling, and RC01 rather than MS03 because there IS a code for this one: "the
	// BIC does not identify a reachable participant" is exactly what an absent or
	// malformed CdtrAgt/DbtrAgt means. Classified here for that reason and not because
	// it travels — it too is refused at submission.
	{ErrCounterpartyAgentNotNamed, "ErrCounterpartyAgentNotNamed", iso20022.StatusReasonBankIdentifierIncorrect},

	// And the third of the address refusals, same RC01 for the same reason: an address
	// whose bank code resolves to nothing in this bank's copy of the directory is a
	// payee this scheme cannot be told to reach. The two sit together because a payer
	// cannot act on the difference.
	//
	// It is refused at submission and carries no message either. What the CODE is for
	// is the day this refusal is reached with a payment in hand — a relayed instruction
	// the receiving bank cannot route onward.
	{ErrBankCodeUnknown, "ErrBankCodeUnknown", iso20022.StatusReasonBankIdentifierIncorrect},

	// --- Classified as never reaching a counterparty ---
	//
	// Each is a failure of THIS system's own bookkeeping rather than a judgement about
	// the instruction, so there is nothing truthful to tell the sender. The
	// institutions in cmd/server act on the classification: one that gets a file back
	// with such an error records it against the order id in the day's report rather
	// than uploading an answer.
	//
	// ReasonFor itself cannot tell one of these apart from an error the table has never
	// heard of — both come back MS03 through the same fallback. So the discrimination
	// is the CALLER's, made by name, and the ones discriminated that way are the ones
	// reached on paths nothing is wrong with:
	//
	//   - ErrInvalidStateTransition, an ordinary redelivery, in three places.
	//   - ErrNotThisBanksPayment, the ordinary happy path of EVERY push settlement:
	//     the ACSC is fanned to both banks and the payer's has no creditor leg.
	//   - ErrCycleNotClosed, an ordinary redelivered pacs.009.
	//
	// The errors NOT in that list are not produced by the halves those handlers call,
	// except through a message quoting an identifier this network has never issued,
	// which answers MS03. That residue is stated rather than hidden; closing it needs a
	// two-valued ReasonFor, which is a change to this signature and every caller of it.

	// A lookup for an id this system generated and then could not find is a
	// bug here, not a defect in the message.
	{ErrPaymentNotFound, "ErrPaymentNotFound", ""},
	{ErrCycleNotFound, "ErrCycleNotFound", ""},
	{ErrSettlementNotFound, "ErrSettlementNotFound", ""},

	// A settlement advice is a bank's OWN row about its OWN cut-off, so a
	// missing one is a question this bank asked itself and got no answer to.
	// There is no counterparty in the conversation to tell.
	{ErrSettlementAdviceNotFound, "ErrSettlementAdviceNotFound", ""},

	// The two rows admission gives the other institutions, missing. Both are an
	// institution asking about its OWN record and not finding it, which is this
	// system's own inconsistency rather than a judgement about anybody's instruction —
	// and RC01, which is what ErrParticipantNotFound answers, would say the SENDER
	// quoted a bank that does not exist. It did not.
	//
	// A bank whose provisioning stopped half-way makes either reachable without the
	// store disagreeing with itself, and it is still not a judgement about an
	// instruction: it is a provisioning failure to retry, and a payment/recon finding.
	{ErrSettlementMemberNotFound, "ErrSettlementMemberNotFound", ""},
	{ErrRosterEntryNotFound, "ErrRosterEntryNotFound", ""},

	// All three mean a message arrived at the wrong bank — a settled-payment advice for
	// somebody else's customer, a reserve statement about somebody else's account, a
	// return naming a payment this bank is neither side of. That is a defect in the
	// ROUTING, and the sender is the clearing house or the settlement agent rather than
	// a counterparty with an instruction outstanding. There is no code for "you sent me
	// your own mistake".
	{ErrNotThisBanksPayment, "ErrNotThisBanksPayment", ""},
	{ErrStatementNotForThisBank, "ErrStatementNotForThisBank", ""},
	{ErrNotAPartyToThisReturn, "ErrNotAPartyToThisReturn", ""},

	// A mandate recorded at a bank that is not its creditor's. It reaches no message at
	// all: creating one is an operator's request on a bank's own console, and no file
	// any institution exchanges carries CreateMandateTx.
	{ErrNotThisBanksMandate, "ErrNotThisBanksMandate", ""},

	// One institution's act reached through another's Network, and the only entry here
	// that is not about a message at all.
	//
	// The three above are a message delivered to the wrong bank, a routing defect with
	// a real sender behind it. This one has no sender: caller and callee are the same
	// process, and something held a handle belonging to one institution and asked it to
	// perform another's act. A WIRING mistake — the same class as NewNetwork's zero
	// Identity, which panics — returned as an error only because the entity a Network
	// belongs to is decided far from where an act is called. So there is not merely no
	// truthful thing to tell a counterparty; there is no counterparty.
	{ErrNotThisInstitutionsAct, "ErrNotThisInstitutionsAct", ""},

	// Cycle lifecycle errors reach only the operator who drove the cycle into
	// the wrong state; no counterparty ever sees one.
	{ErrCycleNotClosed, "ErrCycleNotClosed", ""},
	{ErrCycleAlreadyOpen, "ErrCycleAlreadyOpen", ""},

	// A cut-off the clearing house will not settle because it could not release what it
	// settled. The empty code for the cycle-lifecycle reason and one of its own: this
	// refusal happens BEFORE any instruction is uploaded, so no settlement agent has
	// been asked and no submitting bank is owed a verdict.
	{ErrCycleNotReleasable, "ErrCycleNotReleasable", ""},

	// The clearing house holding no return for a payment an answer names. Not a verdict
	// on anybody's message: the answer this arrives with is forwarded to the bank that
	// asked either way, and what is missing is only the second hop of a conversation
	// that bank is not party to.
	{ErrHeldReturnNotFound, "ErrHeldReturnNotFound", ""},

	// A cut-off the settlement agent has already discharged: a redelivered pacs.009 and
	// nothing more. It takes ErrCycleNotClosed's empty code and its argument — it
	// describes THIS system's state, and MS03 would tell a clearing house that a cycle
	// which in fact settled was refused.
	//
	// A separate sentinel because the agent does not read the cycle to find out: it
	// holds no cycles table, so "have I settled this" is answered out of its own
	// settlement register.
	{ErrCycleAlreadySettled, "ErrCycleAlreadySettled", ""},

	// An illegal transition means this system tried to move a payment
	// somewhere its own state machine forbids. Telling the counterparty
	// "rejected, unspecified" would hide a defect behind a plausible message.
	{ErrInvalidStateTransition, "ErrInvalidStateTransition", ""},

	// A return the settlement agent has already settled: the redelivery case
	// ErrInvalidStateTransition covers on every other path, arriving from the one actor
	// that has no payment row to read a status off. Answering RJCT would tell the
	// returning bank that a return which in fact completed was refused.
	{ErrReturnAlreadySettled, "ErrReturnAlreadySettled", ""},

	// --- The admission refusals, which are answered off this code set ---
	//
	// These rows are empty for a different reason from the block above, and the
	// difference matters because a refusal here is a real answer where nothing
	// classified above is. Admitting a bank is not a conversation: each of the four
	// acts is called directly, one institution at a time, and a refusal goes back to
	// the caller that asked. The codes in this table are the pacs.002's external set,
	// so an entry with a code would put a payment status on an account-opening refusal.
	//
	// No paragraph here says how many rows there are. Any change to the thing counted
	// falsifies a count, and a removal is the one nobody rereads the prose for. The
	// ROWS have a guard; prose about them has none.
	{ErrBICAlreadyAdmitted, "ErrBICAlreadyAdmitted", ""},
	{ErrBankAlreadyAdmitted, "ErrBankAlreadyAdmitted", ""},
	{ErrAdmissionNotIdentified, "ErrAdmissionNotIdentified", ""},
	{ErrAdmittedAccountUnusable, "ErrAdmittedAccountUnusable", ""},
	{ErrSettlementAccountReplaced, "ErrSettlementAccountReplaced", ""},
	{ErrNotThisBanksAdmission, "ErrNotThisBanksAdmission", ""},
	// The three about addressing, on this path for the reason the six above are: every
	// one is refused during an admission, and no admission is answered on a wire.
	//
	// None of them reaches a PAYMENT. The refusal a payer meets when an address will not
	// resolve is ErrBankCodeUnknown, which has a row of its own with a code.
	{ErrBankCodeNotAllocated, "ErrBankCodeNotAllocated", ""},
	{ErrBankCodeTaken, "ErrBankCodeTaken", ""},
	{ErrBankCodeReplaced, "ErrBankCodeReplaced", ""},
}

// borrowedReasons classifies the errors an actor's half produces that this package
// did not declare.
//
// A second table and not more rows in the first, because reasonTable's two guards
// are about payment/errors.go — every sentinel declared there must appear, and
// nothing else may.
//
// Two of the three are AM04, from two different layers, and they are two entries
// because they are two distinct error values that no unwrapping relates:
//
//   - deposit.ErrInsufficientAvailable, from two halves. It is the direct debit's
//     whole point: Scheme.Validate is a funds check run by the DEBTOR's bank. It
//     also comes from PostReturnLegTx, where the RETURNING bank on a push checks its
//     own payee — so on a push return, AM04 can be about the asking bank's OWN
//     customer.
//   - ledger.ErrInsufficientBalance is the same refusal one layer down and one
//     institution over: a net payer whose RESERVE cannot cover its position.
//     SettleCycleTx returns this sentinel deliberately, so the code on the wire is
//     the one the ledger would have given.
//
// Without an entry, ReasonFor falls through to MS03 for the two refusals that have
// the most specific code of all. AM04 is what a real debtor's bank sends and what a
// receiving system reads to decide whether to re-present or unwind.
//
// The third is deposit.ErrAccountClosed, and it is AC04, one of the commonest return
// reasons in SEPA: the payee's own bank refusing a settled credit. It reaches a
// counterparty because a receiving bank after finality answers with a pacs.004
// rather than with silence.
//
// A new member belongs here only if the error reaches ReasonFor at all.
var borrowedReasons = []reasonMapping{
	{deposit.ErrInsufficientAvailable, "deposit.ErrInsufficientAvailable", iso20022.StatusReasonInsufficientFunds},
	{ledger.ErrInsufficientBalance, "ledger.ErrInsufficientBalance", iso20022.StatusReasonInsufficientFunds},
	{deposit.ErrAccountClosed, "deposit.ErrAccountClosed", iso20022.StatusReasonClosedAccountNumber},
}

// ReasonFor maps an error to the code a pacs.002 should carry.
//
// It is exported because the party that decides an error is worth telling a
// counterparty about is not this package: it is whoever holds the connection.
//
// It unwraps, because the payment layer wraps freely and a table keyed on identity
// alone would degrade to MS03 for most real failures — silently, which is the
// failure mode this whole arrangement exists to prevent. An error the table does not
// know is MS03 rather than a panic: an actor that crashed instead of answering would
// be worse than an imprecise code.
//
// It consults borrowedReasons after reasonTable, and the order is stated rather than
// incidental: where both classify an error, this package's own classification of its
// own sentinel wins.
//
// The order does NOT protect reasonTable's empty-code entries. Those are decisions
// made here too — "this never reaches a counterparty" — but the m.Code != "" filter
// skips them, so an error wrapping one and also matching a borrowed row would come
// back with the borrowed code rather than falling to MS03. Nothing produces such an
// error today; the protection an empty code gives is the CALLER's, made by name
// before it asks for a code at all.
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
// something a counterparty can be told and can act on — as against a failure of this
// system's own bookkeeping, which nobody outside it could do anything with.
//
// It is the question ReasonFor cannot answer. ReasonFor always produces a code,
// because a caller that has already decided to answer needs one, so a dropped
// connection and a payee's closed account come back as codes that look alike.
//
// The tables are what make it: an error classified in either was classified BY
// SOMEBODY, with a comment saying why, and an error in neither has never been
// thought about as something to put on a wire. Empty-code entries are excluded for
// the reason ReasonFor excludes them.
//
// What it is for is the receiving bank's half after finality, where the two outcomes
// are no longer both messages: a judgement becomes a pacs.004 that sends a settled
// payment back, and a bookkeeping failure becomes a line in the day's report.
// Returning money on a dropped connection is the mistake this exists to prevent.
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
// ReasonFor rather than holding a second table: the two sets share their spellings
// wherever both name the same fact, and a second table would be a second place for
// one classification to live and drift from.
//
// It is not a cast, and the switch is what stops it being one. A status reason with
// no member of its own in the return set becomes MS03, because a pacs.004 may only
// carry a member of the return set. iso20022.ReturnReason exists as a distinct type
// for exactly this reason.
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

// MessageContext is everything a message needs that the payment itself does not
// carry: who is sending it, who to, what to call it, and when.
//
// A parameter rather than state on Network because the same payment produces
// different messages on different hops — the debtor's bank sends the pacs.008 to the
// CSM, the CSM sends the same instruction on to the creditor's bank.
type MessageContext struct {
	From  iso20022.BIC
	To    iso20022.BIC
	MsgID string
	Now   time.Time

	// DecidedBy is who MADE the decision this message reports, when that is not the
	// sender. Empty means the sender decided it, which is every message but one.
	//
	// It exists because a relay cannot otherwise be honest. The clearing house passes
	// the settlement agent's refusal of a RETURN straight through to the bank that
	// asked for one: it decides nothing, adds nothing, and with only From to go on
	// would stamp itself as the originator of somebody else's refusal. That is what
	// Orgtr exists to prevent — a receiver reading it blames the wrong institution.
	//
	// Separate from From rather than replacing it because the two are different
	// questions with different answers on exactly this hop.
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
// The two differ the moment a message passes through an intermediary: a status report
// travelling back from the creditor's bank through the clearing house is sent by the
// clearing house and originated by the bank.
//
// Almost every message this system emits is one it decided itself, so the originator
// defaults to mc.From — written out rather than left to the header, because a
// receiver reading the header's Fr as the decider blames the wrong institution for
// every rejection that was relayed. The exception is the hop DecidedBy exists for.
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

// messageParty is one side of a payment with everything a message says about it: the
// bank that holds the account, the name on the account, and the address the payment
// quoted to reach it.
//
// It exists so that the conversion itself is a pure function of resolved data,
// decoupled from whatever produces that data. partiesOf is the one production
// producer; fuzz_test.go is the other, deliberate one, constructing messageParty
// values directly, which is what lets FuzzTranslate drive the mapping over names and
// addresses no fixture builder would have thought to try.
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

// agentRef is agentOf as a pointer, for the elements — OrgnlTxRef's DbtrAgt and
// CdtrAgt — where the schema makes the agent optional.
func agentRef(b iso20022.BIC) *iso20022.BranchAndFinancialInstitution {
	a := agentOf(b)
	return &a
}

// amountOf converts a ledger amount to the standard's decimal representation.
//
// The scale comes from the asset definition rather than from a constant, because this
// ledger is multi-asset and a two-decimal assumption would be wrong the first time a
// scheme in another asset arrives.
//
// The rendered amount is then checked, and the check ASKS THE CODEC rather than
// deciding for itself, so the bound below describes iso20022's Validate and a
// Validate that tightened or loosened needs no change here. Two ways an amount this
// ledger holds is refused:
//
//   - FRACTION DIGITS. The standard caps ActiveCurrencyAndAmount at five for any
//     currency, so an asset scaled finer has no representation on the wire at all.
//     Bitcoin, at eight, is in this repository's asset table today. ErrAmountScale.
//
//   - MAGNITUDE, and the honest bound is not the standard's eighteen-digit ceiling.
//     Validate calls Minor(5), which zero-pads the fraction to five places and parses
//     as an int64, so the real bound is MaxInt64/10^(5-scale) — for a two-decimal
//     asset, 9,223,372,036,854,775 minor units at SIXTEEN digits. Below scale 5 it
//     bites too hard, refusing legal seventeen- and eighteen-digit values; AT scale 5
//     the padding is a no-op and the check degenerates to "fits in an int64", which
//     ADMITS nineteen-digit values the schema forbids. The gap is inert only because
//     no asset in ledger.Assets() is scaled to 5, and nothing enforces that. See
//     TestValidateAdmitsNineteenDigitsAtScaleFive in iso20022.
//
//     Recorded rather than worked around: a workaround here would be this package
//     second-guessing the codec, and enforcing the standard's actual ceiling belongs
//     in iso20022.ActiveCurrencyAndAmount.Validate. The sentinel is ErrAmountFormat,
//     which is the wrong one for a well-formed number and part of the same artifact.
//
// Neither was found by FuzzTranslate: the target fuzzes the AMOUNT and holds the
// asset at EUR, and a large amount only produces a message once the fuzzer has also
// assembled two valid IBANs and two non-empty names. A fuzz target explores the
// inputs it was given and no others.
//
// Refusing here rather than at Marshal is the choice ibanOf makes: both errors are
// correct, and only this one names the payment rather than an element inside a
// document, so only this one can be turned into a pacs.002 a customer can read.
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
// It refuses an empty or non-IBAN address rather than emitting an empty element. A
// pacs.008 whose DbtrAcct has no IBAN is invalid, and producing one would move the
// failure from here to a counterparty's parser.
//
// It COMPACTS and then checks the value: compaction because an IBAN is stored here
// with display separators and transmitted without them, the check because a stored
// value that is not an IBAN at all would otherwise reach Marshal, where the error
// names an element inside a document rather than the account this system could not
// address. Delete the check and FuzzTranslate fails in under a second on the address
// "0".
//
// It returns the IBAN rather than the CashAccount that wraps it, because a pacs.003
// needs the same value in two places — CdtrAcct and, standing in for the Creditor
// Identifier, CdtrSchmeId.
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
// It takes the IBAN by value and takes its address here, which is the whole reason it
// exists: AccountIdentification4Choice's arms are pointers because encoding/xml
// cannot express an xsd:choice, and a pointer arm is an invitation to a nil
// dereference downstream. Building the choice in exactly one place, from a value that
// cannot be nil, means no other function in this file has to dereference it.
func cashAccount(iban iso20022.IBAN) iso20022.CashAccount {
	return iso20022.CashAccount{Id: iso20022.AccountIdentification4Choice{IBAN: &iban}}
}

// namedPartyOf is the customer element of a pacs.008 or a pacs.003.
//
// A nameless one is refused here rather than at Marshal, for the reason ibanOf
// refuses an unaddressable account: EPC AT-P001 and AT-E001 make Nm mandatory on both
// sides of both messages, so a party with no name is a document a counterparty
// rejects. Delete the check and FuzzTranslate finds an empty creditor name in about
// half a second. The error is not one of payment's sentinels because there is no
// condition in this system's own vocabulary for "the account holder has no name".
func namedPartyOf(element, name string) (iso20022.PartyIdentification, error) {
	if name == "" {
		return iso20022.PartyIdentification{}, fmt.Errorf("%w: %s/Nm", iso20022.ErrMissingElement, element)
	}
	return iso20022.PartyIdentification{Nm: name}, nil
}

// endToEndOf is the customer's own reference, or the value the guidelines reserve for
// its absence.
//
// EndToEndId is 1..1 in the schema and Payment.EndToEndID is optional, so the gap has
// to be filled. NOTPROVIDED is the EPC convention, filled in HERE rather than
// defaulted at initiation because it is a fact about the message and not about the
// payment: a receiver reading it knows the sender had no reference.
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
// It is the payment's VALUE date and not the message's creation date: SEPA CT settles
// at T+1 and SDD at T+2, so a message that used Now would instruct settlement on the
// wrong day.
//
// A payment with no value date has not been through SubmitPayment, and gets the
// message's own creation date rather than the zero time, which marshals as 0001-01-01
// — a schema-valid date asserting settlement in the first century.
func settlementDateOf(p Payment, mc MessageContext) iso20022.ISODate {
	if p.ValueDate.IsZero() {
		return iso20022.ISODate{Time: mc.Now}
	}
	return iso20022.ISODate{Time: p.ValueDate}
}

// clearingSettlement is how every message this system sends settles: through a
// clearing system, rather than across accounts the two agents hold with each other.
// See iso20022.SettlementMethodClearing for why that is a property of THIS clearing
// house and not of the scheme.
func clearingSettlement() iso20022.SettlementInstruction {
	return iso20022.SettlementInstruction{SttlmMtd: iso20022.SettlementMethodClearing}
}

// assetOf is the unit a payment settles in, which is a property of its scheme and not
// of the payment. An unregistered scheme is ErrSchemeNotFound here for the reason it
// is at initiation: this system cannot say what a payment under a scheme it does not
// implement is denominated in, and guessing euro would be the multi-asset mistake
// amountOf exists to avoid.
func (s *Network) assetOf(p Payment) (ledger.AssetCode, error) {
	sc, ok := s.scheme(p.Scheme)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrSchemeNotFound, p.Scheme)
	}
	return sc.Asset(), nil
}

// partiesOf resolves a payment's two sides to what a message says about them: each
// bank's BIC and each account holder's name.
//
// It reads NOTHING. Both are on the payment, put there at submission — the bank's own
// side from its own register, the counterparty's name and agent from the instruction.
// Building an outbound message reads no other bank's book.
//
// A package function and not a method for the same reason: "it reads nothing" is
// worth having the compiler check rather than a reader, and there is no store on the
// other side of a receiver it does not have.
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

// CreditTransferMessage renders a file of payments as the pacs.008 that carries them
// between banks.
//
// A FILE, because that is what a bank sends: instructions accumulate behind a cut-off
// and one message carries all of them. One payment is the smallest such file and not
// a different shape.
//
// The two AGENTS are the two banks, and the header's To is whoever the message is
// being handed to next — the clearing house, on the first hop. Conflating them would
// model a topology this system does not have: banks here meet at a CSM, never
// directly.
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

// creditTransfer builds the message from data its caller already holds. Everything it
// does is a pure function of its arguments, which is what FuzzTranslate needs: there
// is no I/O anywhere on this path for a fuzz input to depend on.
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
			// NbOfTxs is what the SENDER asserts, not a derivation the receiver would
			// recompute. A receiver that recomputed it instead of checking it would never
			// notice a truncated file, which is the whole reason the element exists.
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
// The field order is the schema's, and it is the payment's own path: party, its bank,
// the other bank, the other party. LclInstrm is absent because SEPA credit transfer
// does not populate it, and SeqTp because pacs.008 has no such element at all.
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
// transactions settle on different days says something false about some of them. A
// cut-off builds one file per scheme and a scheme's value date follows from the
// scheme, so agreement is how the payments arrive rather than something a caller
// arranges — and the file that disagrees anyway is refused here instead of being
// given one of its dates and being wrong about the rest.
//
// An EMPTY file is refused in the same breath: there is no date to assert, and a
// pacs.008 with no transactions is a cut-off that produced a message nobody needed.
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

// InstructionMessage renders one cut-off file: the pacs.008 or pacs.003 a bank hands
// the clearing house when it reaches its cut-off.
//
// One file, one SCHEME, because a scheme decides both the message definition and the
// asset every amount in the file is denominated in. A bank operating two schemes
// reaches one cut-off and uploads two files, which is the shape a real hub has.
//
// It is the only builder that reads a store, and only on a pull: a pacs.003 carries
// each debtor's own mandate and a payment holds nothing of one but its id. The read
// is a View over the submitting bank's own database — a mandate is held by the
// CREDITOR's bank in SEPA, which is the bank reaching this cut-off — and that read is
// why this is a BANK's act rather than the core's.
func (s *BankNetwork) InstructionMessage(ctx context.Context, ps []Payment, mc MessageContext) (iso20022.Envelope, error) {
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
	err := s.store.View(ctx, func(ctx context.Context, tx BankTx) error {
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
// Building it is the only honest way to know it can be built. The alternative is a
// second list of the elements a message needs — both accounts addressable, both
// parties named, the amount representable — and two lists of one rule are two things
// that drift.
//
// It runs at SUBMISSION, inside the unit of work that posts the debtor leg. The file
// itself is not built until the cut-off, hours later; a payer debited now against an
// instruction that turns out to be unsendable then would be short of money against a
// payment nobody could answer. So the refusal happens while the debit can still roll
// back with it, and the API answers 422. It costs one rendering per payment that is
// thrown away and rendered again at the cut-off — the price of the check being the
// real thing rather than a summary of it.
func (s *BankNetwork) instructableTx(ctx context.Context, tx BankTx, p Payment) error {
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
// payment holds only its MandateID. A file of collections is a file of pairs: one
// debtor's authority says nothing about the next.
type Collection struct {
	Payment Payment
	Mandate Mandate
}

// DirectDebitMessage renders a file of collections as the pacs.003 that carries them.
//
// It is the mirror of CreditTransferMessage in the one way that matters: the SENDER
// is the party being paid. A push scheme's message travels with the money; a pull
// scheme's travels against it, which is why the creditor and its agent come first in
// the transaction and why the mandate has to travel too.
//
// It takes neither a context nor a Tx: partiesOf reads nothing, and the mandate — the
// one piece of I/O this message ever needed — arrives already resolved on the
// Collection.
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
			// SeqTp is RCUR for every collection, including the first. This system does
			// not record whether a mandate has been exercised before, so FRST is a claim
			// it cannot make; the 2016 SEPA Core rulebook removed the requirement to send
			// FRST first and permits RCUR throughout, which is what makes the weaker
			// statement the correct one rather than merely the safe one.
			SeqTp: &seq,
		},
		IntrBkSttlmAmt: amt,
		ChrgBr:         iso20022.ChargeBearerFollowingServiceLevel,
		DrctDbtTx: iso20022.DirectDebitTransaction{
			MndtRltdInf: iso20022.MandateRelatedInformation{
				MndtId: string(m.ID),
				// DtOfSgntr maps Mandate.CreatedAt, and the elision is deliberate. The EPC
				// makes the date of signature mandatory; payment.Mandate has no signature
				// date, because in this system a mandate is created at the moment it is
				// authorised, so the two are the same fact and a second column would have no
				// independent source. A reader meeting a real mandate — signed on paper, keyed
				// in a week later — finds the difference stated here rather than assumed away.
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

// creditorSchemeIdentification is AT-02, the Creditor Identifier — and it is the
// creditor's own IBAN, which is the second elision this message makes and the larger.
//
// A real Creditor Identifier is issued by a national scheme, which is precisely what
// makes it unforgeable and therefore worth checking a mandate against. This
// repository models no such authority, so it has no such value, and deriving one from
// a name or a BIC would fabricate the one property the element has. The creditor's
// IBAN identifies the creditor uniquely within this network, which is the property a
// debtor's bank reads the element FOR. What it is not is a Creditor Identifier: a
// debtor's bank holding a mandate scoped to a real CI would find a value it cannot
// look up. That is the cost, recorded rather than assumed away.
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

// OriginalMessage identifies the message a status report is about. A status report
// refers BACKWARDS and by nothing else: the sender of the original has no other way
// to find what this is about, which is what makes clearing asynchronous rather than
// merely delayed.
type OriginalMessage struct {
	MsgID     string
	MsgDefIdr string
	CreDtTm   time.Time
}

// TransactionStatusReport is the outcome of one transaction in that message.
//
// Code and Text are both carried because they say different things: the code is what
// makes a rejection machine-actionable, and the text says the part no code can —
// which ceiling was exceeded, what the available balance was.
type TransactionStatusReport struct {
	EndToEndID string
	TxID       string
	Status     iso20022.TransactionStatus
	Code       iso20022.StatusReason
	Text       string
}

// StatusMessage renders the fate of an earlier message as the pacs.002 that reports
// it. Not a Network method because nothing in it comes from the store: a status is
// about a message, and the message is what the caller has.
func StatusMessage(orig OriginalMessage, sts []TransactionStatusReport, mc MessageContext) (iso20022.Envelope, error) {
	txs := make([]iso20022.PaymentTransactionStatus, 0, len(sts))
	for i, s := range sts {
		txs = append(txs, iso20022.PaymentTransactionStatus{
			// StsId names THIS status, not the payment it is about. It is derived from the
			// message's own identifier and the position within it, so that two statuses in
			// one report are distinguishable and a later query can name one of them.
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

// originalCreationOf echoes when the reported-on message was created, and omits the
// element entirely when the caller does not know. The alternative is
// 0001-01-01T00:00:00Z, a timestamp asserting the original was written two thousand
// years ago.
func originalCreationOf(orig OriginalMessage) *iso20022.ISODateTime {
	if orig.CreDtTm.IsZero() {
		return nil
	}
	return &iso20022.ISODateTime{Time: orig.CreDtTm}
}

// statusReasonOf is why, and it is present only for a rejection.
//
// An acceptance has no reason: StatusReasonChoice requires exactly one of a code and
// a proprietary text, so an accepted transaction with a reason element would have to
// invent one. A rejection with no code is not representable and not reachable either
// — ReasonFor returns MS03 for every error — so an empty Code here is a caller that
// built the report by hand. It gets MS03 for the reason ReasonFor's default does:
// the weakest true statement available beats a rejection nobody can act on.
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
// It is derived and the per-transaction statuses are not, which is the one place in
// this file where a derivation is the right answer: GrpSts is a summary of what the
// sender is saying in the SAME message, not a count a receiver would use to detect a
// loss. A file of a thousand transfers accepted with fifty rejections is neither
// accepted nor rejected, and PART is the only truthful answer.
//
// A report with no transaction statuses leaves the element empty rather than
// guessing: it is optional precisely so a report can decline to characterise a group
// it says nothing about.
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

// ReturnMessage renders a settled payment coming back as the pacs.004 that carries
// it.
//
// A distinct MESSAGE and not a status precisely because settlement was final: a
// pacs.002 says an instruction was refused, a pacs.004 says a completed payment is
// being reversed by sending an equal and opposite one.
//
// The reason is an iso20022.ReturnReason and not a StatusReason, the two types being
// separate even where their members coincide. Payment.RejectCode is therefore NOT the
// source of it; the caller supplies the return's own reason.
//
// A Network method although it reads no store, and it takes no context for that
// reason: the amount's scale comes from the scheme's asset and only the Network holds
// the scheme registry. A pacs.004 names no ACCOUNT HOLDERS, which is why it is the
// one outbound builder with no messageParty in its signature; it does name a party
// (the bank that decided to send the money back) and two AGENTS, both off the
// payment's own details.
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
// Everything above renders something this system already decided; everything below
// reads an instruction another bank decided, and the difference is that nothing here
// can be trusted to be self-consistent. A received message is the only thing the
// receiver has, and it carries no internal identifier of this system's.

// CreditTransferRequest turns a received pacs.008 into the requests this system can
// act on: one per transaction in the file, in the file's own order.
//
// A pacs.008 travels FROM the debtor's bank, routed by CdtrAgt, so the bank reading
// it SHOULD be the CREDITOR's, and the creditor is the only party it has any standing
// to resolve. It is resolved by ADDRESS — against the IBAN the message carries, IN
// THIS BANK'S OWN REGISTER — and never by an internal account id, the message having
// no element for one. So an unresolvable creditor IBAN is ErrAccountNotInParticipant
// and becomes AC01: from the receiver's side, an address it cannot resolve for its
// own customer IS an incorrect account number.
//
// SHOULD, because CdtrAgt is what the SENDING bank asserted and nothing checked it
// against a register — nor could it, once each bank holds only its own row. A payer
// who names the wrong bank has the message delivered to that bank, and this function
// refuses it. Own-register resolution is the guard: a sweep would have found the
// payee at the RIGHT bank and gone on to act on somebody else's customer.
//
// The bank reading the message is this network's own identity, which makes "its own
// register" a property of the handle rather than of what the caller passed.
//
// The DEBTOR is not resolved: it is the sending bank's customer, and this bank has no
// standing over whether that account exists. What comes back for it is what the
// message says.
//
// The scheme is the MESSAGE's, and it takes two elements to name it. Being a pacs.008
// says the payment is a PUSH; the currency says which asset it settles in; and this
// network has one scheme for that pair or it refuses the message (schemeSettling). A
// currency no push scheme settles in is refused rather than reinterpreted, because a
// receiver that took the sender's currency and some scheme's scale would book a
// number neither bank wrote down.
//
// It returns REQUESTS and not Payments: nothing here is accepted, deduplicated or
// posted. That separation is what lets a receiving bank translate a file without a
// write and reject it before one.
func (s *BankNetwork) CreditTransferRequest(ctx context.Context, doc *iso20022.Pacs008) ([]InboundTransaction, error) {
	// Before the message is read at all, and the placement is what makes the paragraph
	// above exact. The identity is otherwise reached only through localPartyIn, at the
	// end, so a MALFORMED pacs.008 handed to the clearing house would come back a parse
	// error — a true statement about the message and a silence about the institution.
	// Who is reading is not a question about what arrived.
	if _, err := s.self(); err != nil {
		return nil, err
	}
	txs, err := s.creditTransferIn(doc)
	if err != nil {
		return nil, err
	}
	// The creditor is this bank's own customer on a push; the debtor is the sending
	// bank's and is recorded, not resolved. creditTransferIn left the creditor as the
	// ADDRESS the message quoted.
	for i := range txs {
		if err := s.localSideIn(ctx, &txs[i], &txs[i].Request.Creditor); err != nil {
			return nil, err
		}
	}
	return txs, nil
}

// InboundTransaction is one transaction read out of a file: what it instructs, the id
// the SUBMITTING bank minted for it, and — where this bank has already decided it
// cannot act on this one — why.
//
// The id travels beside the request rather than inside it because it is the one thing
// on the message that is not part of an instruction. Every institution keys its own
// row by it, and nothing on this path allocates one.
//
// Refusal is what makes a file's answers per transaction. A file is READ whole or not
// at all, so a transaction that cannot be PARSED takes the file with it. Resolution
// is a different question: whether THIS bank holds the address one line quotes says
// nothing about the next line, and a file of a thousand refused because the seventh
// names a closed account would tell nine hundred and ninety-nine payers their payees'
// IBANs were wrong. So an addressing refusal lands here rather than on the return
// value. A Refusal is always something this bank can SAY — see localSideIn.
type InboundTransaction struct {
	ID      PaymentID
	Request InitiatePaymentRequest
	Refusal error
}

// localSideIn resolves one transaction's own side, and decides whether a failure
// belongs to that transaction or to the whole file.
//
// Two refusals are about the ADDRESS and about nothing else — this bank holds no such
// account (AC01), and this bank holds two (MS03) — so each answers the transaction
// that quoted it and the rest of the file is unaffected.
//
// Everything else fails the FILE, the same distinction addressedPartyTx draws one
// level down: a dropped connection is not news about a payee's IBAN. Reported as a
// file-level failure it becomes a problem in the day's report and the file is
// answered by nobody, which is truthful; reported per transaction it would tell a
// thousand payers their payees' accounts were bad.
func (s *BankNetwork) localSideIn(ctx context.Context, tx *InboundTransaction, side *PartyRef) error {
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
// It is the half of CreditTransferRequest that needs no register, split out because
// the clearing house has none and has to record the payments anyway. Both parties
// come back as the address the message quoted and no account id; the receiving bank's
// own resolution then replaces its own side, and the clearing house's copy keeps both
// as they arrived.
//
// A transaction that cannot be read fails the WHOLE file, which is the argument
// NbOfTxs exists for: a reader that returned the four transactions it could parse and
// dropped the fifth would produce a payment that arrived, was acknowledged at the
// message level, and never happened.
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

// DirectDebitRequest turns a received pacs.003 into the requests this system can act
// on: one per collection in the file, in the file's own order.
//
// A pacs.003 travels FROM the creditor's bank, routed by DbtrAgt, so the bank reading
// it is the DEBTOR's — the mirror of CreditTransferRequest in exactly the way the
// direction implies and no further: the DEBTOR is resolved by ADDRESS in this bank's
// own register, and the CREDITOR is recorded from what the message says, being the
// SENDING bank's customer. The account elements are read for what they are rather
// than by position — a reader that took the first account element as the debtor's, as
// it is in a pacs.008, would produce a collection pointing the wrong way and resolve
// successfully while doing it.
//
// A collection addressed to the wrong bank is refused here, and on a pull that
// matters more than on a push: the receiving bank is the one that POSTS, so a
// collector who named itself as the payer's bank would otherwise have the payer
// resolved at the payer's real bank and the debit posted in that bank's book.
//
// And it carries a mandate. An empty MndtId is refused here rather than left for
// SDD.Validate, which would refuse it too: this is another bank's claim on this
// bank's customer's account, and a message that came with no mandate should not
// become a request that looks like one.
func (s *BankNetwork) DirectDebitRequest(ctx context.Context, doc *iso20022.Pacs003) ([]InboundTransaction, error) {
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

// schemeSettling is which of this network's schemes an inbound message travelled
// under, together with its amount in minor units.
//
// The MESSAGE DEFINITION decides the direction and the CURRENCY decides the asset,
// and between them there is exactly one scheme or there is a refusal. Both elements
// are needed: reading the scheme off the message definition alone would say a
// pacs.008 IS a SEPA credit transfer, which holds only while SEPA's is the only push
// scheme, and the outbound side already renders a payment in whatever asset its
// scheme settles in — so a system that could SEND a dollar credit transfer and not
// RECEIVE one would be asymmetrical.
//
// The message does not get to choose. A currency no scheme in this direction settles
// in is refused rather than reinterpreted, because a receiver that took the sender's
// currency and some scheme's scale would book a number neither bank wrote down.
//
// Two schemes in one direction and one asset is AMBIGUOUS and is refused too.
// Nothing here registers such a pair, and a receiver that picked one — the first in
// ID order, say — would resolve every message under a rulebook chosen by alphabet.
// There is no information in a pacs.008 that could break the tie, which is exactly
// why this is a refusal rather than a rule: what a real network has here is the
// clearing arrangement the message arrived over, and this system has one clearing
// house and no such element.
//
// The scale comes from ledger.LookupAsset on the currency the message names, as
// amountOf takes it from the asset on the way out. Reading the amount FIRST, before
// any scheme is chosen, is what keeps a mismatch reported as what it is — a message
// no scheme settles — rather than as a complaint about the number's shape.
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

// amountIn is the inverse of amountOf: a decimal on the wire becomes minor units of
// the asset the message names, at that asset's own scale.
//
// A currency this ledger does not define is refused rather than read at some default,
// because there is no default that is right. Minor refuses a value with more fraction
// digits than the scale allows rather than rounding it, which keeps a message this
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

// localPartyIn resolves the ONE side of a received message that belongs to this bank,
// by the address the message quotes for it.
//
// Which side that is follows from the direction and from nothing else: the clearing
// house routed a pacs.008 by CdtrAgt and a pacs.003 by DbtrAgt, so the bank holding
// this message is the creditor's on a push and the debtor's on a pull. Its two
// callers each pass the one identifier that is theirs to resolve.
//
// The counterparty is not resolved at all; it is recorded from what the message says.
// The debtor on a push is the SENDING bank's customer, and this bank's directory has
// no authority over whether that account exists.
//
// LOCAL means whose register is searched: this network's own identity rather than an
// argument the caller supplies.
func (s *BankNetwork) localPartyIn(ctx context.Context, ident deposit.Identifier) (PartyRef, error) {
	var ref PartyRef
	err := s.store.View(ctx, func(ctx context.Context, tx BankTx) error {
		var err error
		ref, err = s.addressedPartyTx(ctx, tx, ident)
		return err
	})
	return ref, err
}

// addressedPartyTx turns one quoted address into the party it names.
//
// It drives ResolveIdentifierTx rather than reimplementing the lookup, which matters
// for a property that is easy to lose: two of this bank's accounts holding one
// identifier is ErrIdentifierAmbiguous there, not the first hit, and choosing between
// them here would route a payment on the strength of listing order.
//
// Only a NOT-FOUND becomes a domain error, and the hazard is sharper here than
// outbound: for a directory lookup a not-found IS the expected failure, so
// `if err != nil { return ErrAccountNotInParticipant }` is the obvious shape. It is
// wrong — that error becomes AC01 "incorrect account number" in the pacs.002 this
// system sends back, so a dropped connection would tell another bank its customer's
// IBAN was bad.
//
// An AMBIGUOUS address is passed through unchanged for the same reason: AC01 would
// again be a false statement about the sender's data — the IBAN is fine, and it is
// this network's own directory that cannot say whose it is — so it falls to MS03,
// which claims only that this agent could not carry the instruction out.
func (s *BankNetwork) addressedPartyTx(ctx context.Context, tx BankTx, ident deposit.Identifier) (PartyRef, error) {
	ref, err := s.ResolveIdentifierTx(ctx, tx, ident)
	if errors.Is(err, deposit.ErrIdentifierNotFound) {
		return PartyRef{}, fmt.Errorf("%w: %s", ErrAccountNotInParticipant, ident.Value)
	}
	if err != nil {
		return PartyRef{}, err
	}
	return ref, nil
}

// identifierIn reads the address a received message quotes for one party. It is the
// inverse of ibanOf and cashAccount, and where the nil arm they never produce
// actually arrives.
//
// AccountIdentification4Choice's arms are pointers because encoding/xml cannot
// express an xsd:choice. Outbound, cashAccount builds the choice in one place from a
// value that cannot be nil. Inbound there is no such guarantee: Othr is a legal arm,
// a counterparty is free to send it, and this system cannot resolve a card PAN to an
// account. That is ErrUnaddressableAccount, and it is a refusal rather than a
// dereference of a pointer that is nil for an ordinary reason.
//
// The value is COMPACTED here, which is a no-op for anything that came off the wire —
// the schema's pattern admits no separators — and is done anyway so that what this
// returns is in one canonical form regardless of who built the document. A register
// stores that same form, so what reaches ResolveIdentifierTx matches literally.
//
// Where the two forms still part company is a person: an IBAN read off a statement is
// grouped in fours, and one typed may be lower-cased. Reconciling that is not this
// function's job and must not be — a fallback here would help no other caller and
// would mean second-guessing the directory. It belongs to the comparison itself:
// deposit.Identifier.MatchValue canonicalises BOTH sides for the IBAN scheme.
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
// agentOf, and — unlike identifierIn — it never refuses: the codec already required
// BICFI on the way in for any message that reached Unmarshal, so there is nothing
// left to check. What this reads is recorded on the payment's details, never resolved
// against this bank's own directory.
func agentIn(fi iso20022.BranchAndFinancialInstitution) iso20022.BIC {
	return fi.FinInstnId.BICFI
}

// nameIn reads the name a message gives for one party. It is the inverse of
// namedPartyOf, and — like agentIn, for the identical reason — it never refuses: the
// codec already requires a non-empty Nm on both Dbtr and Cdtr for any message that
// reached Unmarshal.
func nameIn(p iso20022.PartyIdentification) string {
	return p.Nm
}

// endToEndIn is the inverse of endToEndOf: the value the guidelines reserve for "the
// sender had no reference" comes back as no reference.
//
// Storing the literal would not merely be untidy. EndToEndID is deduplicated —
// SubmitPaymentTx refuses a reference it has already seen, and an empty one is never
// an identity — so every reference-less payment in the network would share one
// reference, and the second to arrive would be rejected as a duplicate of the first.
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

// ReadStatus reads a received pacs.002 as what it says about the original message and
// what it says about each transaction in it.
//
// Two return values because they are two different facts, acted on at different
// granularities: the OriginalMessage is how a status is matched back to something
// this system sent — there is no other link — and each report is the fate of one
// payment inside it.
//
// It returns no error, and that is a claim rather than an omission. Every element it
// reads is either mandatory in a document that has been through Unmarshal, or
// optional and absent-means-absent: a missing OrgnlCreDtTm is the zero time because
// the sender did not say, and an acceptance carries no reason element.
//
// Two things in the message are deliberately dropped. GrpSts is derived from the
// per-transaction statuses by the sender, so keeping it would be keeping a summary
// beside the thing it summarises, and a receiver that trusted the summary over the
// detail would act on the wrong one when they disagreed. StsId names the STATUS
// rather than the payment, and nothing in this system asks about one yet.
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
		// The code and the text are both kept, because they say different things: the
		// code is what makes a rejection machine-actionable and the text is what says
		// the part no code can. Dropping either is a silent loss.
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
// Each leg's scale comes from ledger.LookupAsset on that leg's OWN currency, never
// from a constant and never from the first leg's: one settlement message may carry
// legs from several cycles, and a cycle's asset is a property of its scheme. That is
// the same reason TtlIntrBkSttlmAmt is absent on the way out — a sum across assets is
// not a number.
//
// The sender's NbOfTxs is checked before any leg is read. This is a settlement
// instruction: a leg lost in transit is a bank that does not get paid, and reading
// the survivors as a complete instruction would settle the cycle and leave no record
// that anything went missing.
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

// ReturnInstruction is one payment being returned, as a settlement agent needs it:
// which two agents move reserves, how much, and why. DebtorAgent and CreditorAgent
// come off OrgnlTxRef and not off a payment row — a settlement agent has none to read
// them from.
type ReturnInstruction struct {
	PaymentID     PaymentID
	EndToEndID    string
	DebtorAgent   iso20022.BIC
	CreditorAgent iso20022.BIC
	Amount        ledger.Amount
	Asset         ledger.AssetCode
	Reason        string
}

// CodeAndText is a reason code and the free text beside it, joined for a ledger
// description.
//
// Both, because they say different things — the code is what makes a rejection or a
// return machine-actionable in a statement or an exception queue, and the text is the
// part no code can say. The word for "neither was given" is the CALLER's, because
// this serves two callers answering different questions: one over
// iso20022.StatusReason and ReturnReason below over iso20022.ReturnReason, the
// sibling external set kept as a separate type precisely so a rejection reason cannot
// be used as a return reason with nothing to notice.
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

// ReturnReason is what a return is described as where a CUSTOMER's money moves: the
// reason the returning bank gave, code and text.
//
// It lives here rather than beside its caller because ReadReturn needs this exact
// reading for ReturnInstruction.Reason, and cmd/server cannot be imported from this
// package.
//
// The two CUSTOMER legs, and not the reserve reversal between the two banks:
// PostReturnLegTx writes this into the payer's refund and the payee's clawback, while
// SettleReturnTx describes the reversal as the settlement it is, a bank's own
// position moving carrying no customer's reason.
//
// Both arms of the choice are read, because both are legal and the codec refuses a
// return that has neither, so what arrives is one or the other. The nil case is a
// caller's guard rather than a message: RtrRsnInf is mandatory in a pacs.004 that has
// been through Unmarshal.
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
// The scale comes from ledger.LookupAsset on the transaction's OWN currency, never
// from a constant: nothing here may assume every return in a bulk shares one asset
// just because this system's returns happen to be EUR-only today.
//
// A transaction that names no PAYMENT is refused first, and that one is about money
// rather than about resolution. OrgnlTxId is optional in the schema, and SettleReturnTx
// derives the idempotency key of the reserve reversal from it, so an empty one would
// move reserves under ":return-settle" and make every later nameless return look like
// a redelivery of the first. This is the last point at which nothing has happened yet.
//
// A transaction whose OrgnlTxRef is absent, names only one agent, or names an agent
// with no BICFI, is refused rather than half-read. The codec makes OrgnlTxRef
// optional — a hard requirement would make a counterparty that has not adopted it
// unreadable — so a document that has been through Unmarshal can still reach here
// with nothing to resolve accounts from. This does not assume the codec's validation
// ran at all: it checks both agents' presence AND their BICFI itself.
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
			// RtrdIntrBkSttlmAmt, not OrgnlIntrBkSttlmAmt: the two are equal in this
			// system's own returns, which are always whole, but a settlement agent moves
			// the amount actually coming back, and only RtrdIntrBkSttlmAmt says that
			// under the standard's own partial-return shape.
			Reason: ReturnReason(tx.RtrRsnInf),
		})
	}
	return ins, nil
}

// checkNbOfTxs holds a sender to its own count, and every reader of a file begins
// with it.
//
// The count is CHECKED and not recomputed, which is the whole reason the element is a
// string carrying the sender's assertion. A receiver that recomputed it would never
// notice a truncated file, and a truncated file read as a complete one is a payment
// that silently never happened.
//
// Neither refusal is a sentinel from errors.go, deliberately: there is no condition in
// this system's own vocabulary for "the file you sent contradicts itself" — the same
// situation namedPartyOf is in — so both fall to MS03, and the free text says which
// count disagreed.
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

// SettlementLeg is one bank's movement in a settlement instruction: who pays, who is
// paid, how much, and which closed cycle it discharges.
//
// It carries its own Asset because a settlement message may hold legs from several
// cycles, and a cycle's asset is a property of its scheme. Both parties are BICs
// rather than participant ids because this message goes to the central bank, which
// knows banks by BIC and has never heard of this system's ids.
type SettlementLeg struct {
	From      iso20022.BIC
	To        iso20022.BIC
	Amount    ledger.Amount
	Asset     ledger.AssetCode
	Reference string
}

// SettlementLegsOf turns a closed cycle's net positions into the legs a settlement
// instruction carries.
//
// Every position is a claim on or an obligation to the SETTLEMENT AGENT and never a
// bilateral one between two members — that is precisely what netting destroys — so a
// net payer's leg runs from that bank TO the central bank and a net receiver's from
// the central bank to it. The reference on every leg is the CYCLE, which is what the
// central bank reads to know what it is being asked to discharge.
//
// Members are visited in sorted BIC order rather than map order. The legs are the
// message's transactions AND the entries of the settlement agent's own posting, so
// map iteration would produce a different byte sequence on the wire and a different
// stored transaction on every run.
//
// A position of zero is left out: it nets to nothing, and a leg for it would be an
// IntrBkSttlmAmt of zero, which the codec refuses and which would say a bank was
// instructed to move nothing.
//
// It lives here because two callers have to agree. The settlement agent works from
// the LEGS rather than from the cycle, holding no cycles table, so both the clearing
// house rendering a pacs.009 and the seed settling its own cut-offs must produce the
// same legs. Two renderings of one intent are two things that can drift.
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

// NetsToNothing reports whether a cycle leaves nobody with anything to discharge:
// every member's position is zero, or it took nothing in at all.
//
// It is the whole-cycle form of the rule SettlementLegsOf applies per position, and a
// state with two quite different causes — a scheme that saw no traffic, and a batch in
// which every bank's payments and receipts cancelled exactly. Neither yields an
// instruction, so such a cut-off is discharged by the clearing house itself and NO
// settlement is recorded against it anywhere. Two callers need that rule: the
// institution that would otherwise wait for an answer that never comes, and the
// reconciliation harness, which would otherwise read a settled cycle with no
// settlement row as a break.
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
// A pacs.009 and not a pacs.008 because BOTH parties are banks: a pacs.008 moves a
// customer's money and names two customers, this moves a bank's own money and names
// two banks. The compiler enforces the difference — Dbtr and Cdtr here are agents,
// not parties — which is why a customer cannot end up in a settlement instruction by
// mistake.
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
			// NbOfTxs is what the SENDER asserts, not a derivation the receiver would
			// recompute — and this is a settlement instruction, where a silently dropped leg
			// is a bank that does not get paid.
			NbOfTxs: strconv.Itoa(len(txs)),
			// TtlIntrBkSttlmAmt is deliberately absent. The legs of one message may be
			// denominated in different assets, a sum across assets is not a number, and the
			// standard's single total has nowhere to say which asset it is in.
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

// StatementMessage renders one member's share of a settlement as the camt.053 that
// tells it.
//
// One member per message, because each goes to a different bank and a statement is
// addressed to the holder of the account it is about. The standard permits several
// Stmt elements in one document and this system never builds one: a second statement
// here would be about an account the recipient does not hold.
//
// The SIGN leaves the amount and becomes a word. ActiveCurrencyAndAmount cannot be
// negative, so the magnitude goes in Amt and the direction in CdtDbtInd, on BOTH the
// entry and the balance — the same separation ledger.Entry makes with Direction.
// Reconstructing it is ReadStatement's job, and losing it there would make a member
// post its mirror leg backwards.
//
// The REFERENCE rides on AcctSvcrRef. A member bank has no cycles and no other
// institution's payment ids, so the only way it can tell what a reserve movement
// discharged is for the central bank to say. AcctSvcrRef is the servicer's own
// reference for the entry, which is what a cycle id is from the central bank's side
// on the cut-off path and what a payment id is on the return path.
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
				// What kind of movement this is, and the schema's one mandatory child of
				// an entry. See iso20022.BankTransactionCode for why the proprietary arm
				// rather than a domain code, and why there is one code rather than one
				// per flow.
				BkTxCd: iso20022.BankTransactionCode{
					Prtry: iso20022.ProprietaryBankTransactionCode{
						Cd:   iso20022.BankTransactionCodeSettlement,
						Issr: iso20022.BankTransactionCodeIssuer,
					},
				},
				// Named after the reference and nothing more: a cycle id and a payment id
				// are equally opaque to the member reading this, and saying which kind it
				// sent would be telling that bank something it has no row to resolve.
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
// Zero is a CREDIT of nothing. It is reachable on a BALANCE — a member whose reserve
// is exactly empty — and never on an entry, a position of zero producing no leg.
// Choosing CRDT for it is arbitrary and stated rather than left to be inferred.
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

// AdvisedMovement is what a member bank can see in a statement about its own reserve
// account: the movement, the balance it was left at, and the reference that names what
// caused it.
//
// A DIFFERENT type from SettlementStatement, deliberately: that one is what the sender
// knew, this is what the receiver can learn. ParticipantID never reaches the wire,
// because a member bank has no use for the central bank's internal name for itself.
// StatementRef is on the wire but ReadStatement does not surface it: it is the
// servicer's reference for the STATEMENT, and on the return path it is not any row's
// key at all. Collapsing the two types would put fields on the receiving side that are
// either always empty or copied from the wire with nothing to verify them against.
//
// Reference is the one identifier that DOES cross: it is AcctSvcrRef, read back
// exactly as StatementMessage wrote it, and exactly as opaque to this bank as
// StatementRef is. It exists to be quoted back and to key this bank's own advice row,
// not to be resolved to anything.
type AdvisedMovement struct {
	Account        ledger.AccountID
	Asset          ledger.AssetCode
	Movement       ledger.Amount
	ClosingBalance ledger.Amount
	Reference      string

	// ValueDate is CARRIED AND UNREAD, and that is recorded rather than left to be
	// discovered. It makes the full round trip — out in Ntry/ValDt, back here — and
	// PostSettlementAdviceTx posts the mirror leg without it, so the ledger resolves
	// that posting's value date to its booking date instead.
	//
	// It is read here rather than dropped because the alternative is worse: a value date
	// discarded on receipt is one nobody can go back for — the argument ClosingBalance
	// is stored under, and ClosingBalance is unread too.
	//
	// Using it would alter every mirror-leg transaction this system stores, and the
	// question it settles — whether a bank's reserve mirror takes effect on the
	// settlement agent's value date or on the day the bank actually booked it — is a
	// domain decision with a reconciliation consequence.
	ValueDate time.Time
}

// ReadStatement reads a received camt.053 as the movements it advises.
//
// One entry per statement is REQUIRED and more is refused whole. This system's central
// bank posts exactly one netting movement per member per cycle, so a statement
// carrying two is one this reader has no rule for — and posting the first while
// dropping the second would move a bank's reserve mirror by the wrong amount with
// nothing recording it.
//
// A statement with no CLBD balance is refused for the reason camt.053 was chosen over
// camt.054: without it there is nothing to check a posting against, and a message that
// cannot be checked is a notification wearing a statement's name.
//
// The scale comes from ledger.LookupAsset on the entry's OWN currency, never from a
// constant.
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

// signedAmountIn puts the sign back on: the magnitude the standard carried and the
// word beside it become one signed ledger amount.
//
// An indicator that is neither CRDT nor DBIT is REFUSED rather than defaulted.
// Defaulting to credit would turn an unreadable direction into a reserve increase,
// which is the most expensive way to be wrong about a settlement.
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

// closingBalanceIn finds the CLBD balance among however many a statement carries, and
// reports whether there was one.
//
// It searches rather than taking Bal[0] because the standard permits several and their
// order is not fixed; a reader that took the first would eventually read an opening
// balance as a closing one, and the two differ by exactly the entries this message
// exists to advise.
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

// LodgementMessage renders a member's request for a reserve credit as the camt.050
// that carries it. A free function and not a method: it reads no store and needs no
// scheme, everything on the wire being on the instruction or in the context.
//
// It names the account it CREDITS and not the one it debits. CdtrAcct is the reserve
// account, in the central bank's book, and it is the whole instruction. DbtrAcct is
// left ABSENT — the account being debited is this bank's vault cash, in this bank's
// own book, which the central bank neither keeps nor may read. Naming it would be
// quoting a servicer the number of an account in a ledger it has no access to, the
// same reason a pacs.008 does not carry the payer's GL account.
//
// The reference is the MESSAGE ID, and it has to be: there is no process id above the
// two messages, and what the receipt quotes back is this document's own MsgId. So the
// instruction's Ref and the context's MsgID must be the same value, and this refuses
// a caller that disagrees rather than silently picking one — a receipt correlated
// against the wrong one would match nothing the bank ever sent.
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
				// The generic arm, not the IBAN one: a reserve account at a central bank has
				// no IBAN, because it is not a payment address and no customer ever quotes
				// it. The same choice camt.053's Acct makes.
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
// The HEADER is compared against the document, and that is the point. Dbtr and Cdtr
// both duplicate something the AppHdr already says, and this is where the duplication
// earns its keep: a message whose Dbtr is not its sender is a member lodging on
// somebody else's behalf, and one whose Cdtr is not its recipient asks THIS servicer
// to post in ANOTHER servicer's book. Neither is detectable from the document alone.
//
// So this takes the header as an argument, the one reader in this file that does. The
// payment family's readers do not, and the difference is real: a pacs.008 is RELAYED,
// so its Fr is the last hop rather than the debtor's agent and a comparison there
// would refuse every legitimate message. A camt.050 goes straight from the member to
// its central bank, one hop, so the header and the body must agree.
//
// It does not assume the codec's validation ran, for ReadReturn's reason: a document
// handed to this function need not have come from Unmarshal, and the cost of being
// wrong is a credit posted to a reserve account on nobody's authority.
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
	// The generic arm. An IBAN here would be a reserve account addressed as a payment
	// address, which is what GenericAccountIdentification exists to avoid.
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

// LodgementReceiptMessage renders an account servicer's answer as the camt.025 that
// carries it.
//
// The reason is TRUNCATED here rather than discovered on the wire. Desc is Max140Text,
// and the refusals that reach it are error strings quoting a BIC, an asset and two
// account ids, which can exceed 140 characters between them. A document that would
// not marshal is worse than a shortened reason: the member would be told nothing at
// all, and the servicer's handler would report its own answer as a file it could not
// build. So it truncates VISIBLY, with an ellipsis. The limit is called out on
// iso20022.RequestHandling because it is the schema's rather than this system's.
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
		// A refusal that says nothing is one nobody can act on, and the schema does not
		// require a reason here — so the requirement is this system's.
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
				// The request's message definition, so that a member holding more than one
				// kind of outstanding request can dispatch this answer without a table only
				// it has.
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

// truncateTo shortens a reason to fit an element's maximum length, marking that it
// was shortened.
//
// The ellipsis is inside the limit rather than added to it, which is the whole reason
// this is a function: the obvious version returns limit+1 characters and produces
// exactly the invalid document it was written to prevent.
//
// It cuts on a RUNE boundary. Max140Text counts characters and Go slices bytes, so
// cutting at 140 bytes through a multi-byte rune would emit invalid UTF-8.
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
// ONE receipt detail and one handling, and this refuses more rather than reading the
// first. The schema permits a servicer to acknowledge several requests in one
// document; this system asks about one lodgement at a time, so a receipt naming two is
// one it has no rule for, and acting on the first while dropping the second would
// leave a member believing a lodgement was answered when the answer was about another.
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
