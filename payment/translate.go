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

// reasonTable is the mapping sub-project 7a decided, made undriftable here.
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
// The admission refusals are the other kind — one of them does reach a
// counterparty, on an acmt.011, whose reason is free prose rather than a code.
// The codes here are the pacs.002's external set, so there is nothing in it for
// an account-opening refusal either way. Both blocks are below, each under its
// own heading.
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
	{ErrUnaddressableAccount, "ErrUnaddressableAccount", iso20022.StatusReasonMissingDebtorAccountOrIdentification},
	{ErrIdentifierMismatch, "ErrIdentifierMismatch", iso20022.StatusReasonMissingDebtorAccountOrIdentification},
	{ErrAmbiguousAddress, "ErrAmbiguousAddress", iso20022.StatusReasonMissingDebtorAccountOrIdentification},
	{ErrCycleNotOpen, "ErrCycleNotOpen", iso20022.StatusReasonInvalidCutOffTime},

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

	// --- Classified as never reaching a counterparty ---
	//
	// Each is a failure of THIS system's own bookkeeping rather than a
	// judgement about the instruction, so there is nothing truthful to tell
	// the sender. They are classified here as never reaching a counterparty,
	// and the mesh is where that classification is acted on: an actor that
	// gets one dead-letters it rather than putting it on the wire.
	//
	// ReasonFor itself still cannot tell one of these apart from an error the
	// table has never heard of at all — both come back MS03 through the same
	// fallback path, and TestReasonForEmptyCodeEntriesFallToMS03 pins that. So
	// the discrimination is the CALLER's, made by name, and the ones
	// discriminated that way are the ones reached on paths nothing is wrong
	// with:
	//
	//   - ErrInvalidStateTransition, an ordinary redelivery, in four places:
	//     bank.accept (which is where bank.receiveCreditTransfer and
	//     receiveDirectDebit both end up), csm.receiveStatus, csm.clear and
	//     centralBank.receiveReturn.
	//   - ErrNotThisBanksPayment, the ordinary happy path of EVERY push
	//     settlement. csm.tellSettled fans the ACSC to both banks; the payer's
	//     bank has no creditor leg, and PostCreditorLeg tells it so. Discarded
	//     by name in bank.receiveStatus.
	//   - ErrCycleNotClosed, an ordinary redelivered pacs.009 — a cycle already
	//     settled is not a rejection to answer. Discriminated in
	//     centralBank.receiveSettlement.
	//
	// This paragraph used to count them — "the one of the nine", then "the
	// other eight", then "THREE of the nine" — and every one of those numbers
	// was wrong within a task or two of being written, because a sentinel added
	// to errors.go adds a row here. So it says which errors and not how many,
	// and the misreading the old wording invited is worth naming: "the others
	// arise only from malformed messages" was never true, since two of them are
	// on the busiest path in the system.
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
	// A bank that is founded and not yet a member will make the second of these
	// an ordinary state rather than an inconsistency, and what it will be
	// answered with is an acmt.011 refusing the admission — not a payment
	// status. So this classification is the right one either way.
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

	// Cycle lifecycle errors reach only the operator who drove the cycle into
	// the wrong state; no counterparty ever sees one.
	{ErrCycleNotClosed, "ErrCycleNotClosed", ""},
	{ErrCycleAlreadyOpen, "ErrCycleAlreadyOpen", ""},

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
	// These three are empty for a different reason from the block above, and the
	// difference matters because one of them DOES reach a counterparty. An
	// admission refusal travels as an acmt.011, whose reason is RjctnRsn:
	// Max350Text, repeated, free prose. It is not a code, and the codes in this
	// table are the pacs.002's external set — so there is nothing here for one
	// of these to map to, and an entry with a code would put a payment status on
	// an account-opening refusal. See iso20022.AccountRejectionReferences, which
	// records that the standard itself makes this rejection prose where it makes
	// a payment rejection a code.
	//
	// ErrBICAlreadyAdmitted is the one that reaches a counterparty. The clearing
	// house makes it before it relays a request, and mesh.csm.relayAdmission
	// turns it into an acmt.011 addressed to the applicant — carrying the words
	// of the error itself, because RjctnRsn is where a reason goes on that
	// message and it is prose.
	//
	// ErrNotThisBanksAdmission would belong in the block above even if acmt.011
	// did carry codes. It is a bank refusing an acknowledgement that is not its
	// business, which is a defect in the ROUTING rather than a judgement the
	// sender can act on — ErrStatementNotForThisBank's classification, for
	// ErrStatementNotForThisBank's reason. Nothing answers it: the bank is the
	// last hop of an admission and has nobody to tell, so it becomes a dead
	// letter.
	{ErrBICAlreadyAdmitted, "ErrBICAlreadyAdmitted", ""},
	{ErrNotThisBanksAdmission, "ErrNotThisBanksAdmission", ""},
}

// borrowedReasons classifies the errors an actor's half produces that this
// package did not declare.
//
// It is a second table and not more rows in the first, because reasonTable's
// two guards are about payment/errors.go — every sentinel declared there must
// appear, and nothing else may — and an entry from another package would either
// break them or force them to be loosened into a check that no longer catches
// an unclassified sentinel.
//
// Both members are AM04, from two different layers, and they are two entries
// rather than one because they are two distinct error values that no unwrapping
// relates: neither wraps the other.
//
//   - deposit.ErrInsufficientAvailable, from two halves rather than one. It is
//     the direct debit's whole point: Scheme.Validate is a funds check run by
//     the DEBTOR's bank, and the deposit layer is the authority for it, so a
//     collection against an empty account comes back as deposit's error and not
//     as anything this package names. Since Task 16d it also comes from
//     PostReturnLegTx, where the RETURNING bank on a push checks its own payee
//     before it composes the pacs.004 — a clawback it cannot fund is refused
//     locally, so this reaches the operator who asked for the return rather than
//     a counterparty. The mapping is the same and the sentence a reader takes
//     from it is not: on a push return, AM04 can now be about the asking bank's
//     OWN customer, which no push flow could produce before.
//   - ledger.ErrInsufficientBalance is the same refusal one layer down and one
//     institution over: a net payer whose RESERVE cannot cover its position at
//     settlement. It used to come from the ledger itself, because SettleCycleTx
//     posted the member's mirror leg and ledger.PostTransactionTx refuses to take
//     an Asset account negative. That leg is the member's own now, so
//     SettleCycleTx checks each net payer's reserve at the central bank itself
//     and returns this same sentinel — deliberately this one, so that the code on
//     the wire did not change when the posting moved. See SettleCycleTx.
//
// Without an entry, ReasonFor falls through to MS03 — "the agent rejected it and
// the reason has no code" — for the two refusals that have the most specific
// code of all. AM04 is what a real debtor's bank sends and what a real
// settlement agent answers, and it is what the receiving system reads to decide
// whether to re-present or unwind.
//
// A new member belongs here only if the error reaches ReasonFor at all, which
// means a half that some mesh handler calls really returns it. This paragraph
// used to add that the push flow never produced one, on the grounds that its
// receiving half only checks whether the payee's account can take a CREDIT and
// every way that fails is already a payment sentinel. That half is unchanged and
// the conclusion no longer follows: a push RETURN checks whether the same
// account can fund a WITHDRAWAL, and it is the first place in a push flow where
// deposit's funds sentinel is produced.
var borrowedReasons = []reasonMapping{
	{deposit.ErrInsufficientAvailable, "deposit.ErrInsufficientAvailable", iso20022.StatusReasonInsufficientFunds},
	{ledger.ErrInsufficientBalance, "ledger.ErrInsufficientBalance", iso20022.StatusReasonInsufficientFunds},
}

// ReasonFor maps an error to the code a pacs.002 should carry.
//
// It is exported because the party that decides an error is worth telling a
// counterparty about is not this package: it is whoever holds the connection.
// In this repository that is mesh, whose bank and clearing-house handlers turn
// a refused half into a rejection on the wire, and which is therefore the first
// caller this function has ever had. It stayed unexported for as long as it had
// none.
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
// for a code at all, which is what mesh's handlers do with
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
	// bank that asked for one (mesh.csm.receiveReturnStatus): it decides
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
// The scale comes from the asset definition rather than from a constant,
// because this repository's ledger is multi-asset and a two-decimal assumption
// would be wrong the first time a scheme in another asset arrives — which is
// exactly the shape of mistake sub-project 1 recorded.
//
// The rendered amount is then checked, and the check ASKS THE CODEC rather than
// deciding for itself. That framing is the whole of it: this function does not
// know what ISO 20022 permits and does not try to, it asks iso20022 whether
// iso20022 can carry the value, and refuses early if not. So the bound below is
// a description of that package's Validate at the time of writing, and if
// Validate is ever tightened or loosened this function needs no change.
//
// Two ways an amount this ledger holds is refused, one large and one small:
//
//   - FRACTION DIGITS. The standard caps ActiveCurrencyAndAmount at five for any
//     currency, so an asset scaled finer than that has no representation on the
//     wire at all. Bitcoin, at eight, is one — and it is in this repository's
//     asset table today, so this is a live limit rather than a hypothetical.
//     ErrAmountScale. See TestSettlementMessageTakesItsScaleFromTheAsset.
//
//   - MAGNITUDE, and here the honest number is not the one you would guess. The
//     refusal is NOT the standard's eighteen-total-digit ceiling: iso20022's
//     Validate implements its shape check by calling Minor(5), which zero-pads
//     the fraction to five places and parses the result as an int64, so the real
//     bound is MaxInt64 / 10^(5-scale) — for a two-decimal asset,
//     9,223,372,036,854,775 minor units, at SIXTEEN rendered digits. That is an
//     int64 overflow inside the validator's own padding, an artifact of how the
//     check is written, not a property of ISO 20022. Below scale 5 it bites too
//     hard, refusing legal seventeen- and eighteen-digit values;
//     AT scale 5 exactly, the padding is a no-op and the check degenerates to
//     "fits in an int64", which ADMITS nineteen-digit values the schema's
//     totalDigits="18" forbids. So it is not merely conservative, and an earlier
//     version of this comment claiming "nothing invalid escapes" was wrong. The
//     gap is inert here only because no asset in ledger.Assets() is scaled to 5
//     — EUR and USD are 2, BTC is 8 and refused outright — and nothing enforces
//     that, so adding one would open it. See
//     TestValidateAdmitsNineteenDigitsAtScaleFive in iso20022.
//
//     It is recorded rather than worked around because a workaround here would
//     be this package second-guessing the codec, which is exactly what "ask the
//     codec" avoids. Enforcing the standard's actual ceiling belongs in
//     iso20022.ActiveCurrencyAndAmount.Validate, which is where the bound would
//     then also be testable. The sentinel is ErrAmountFormat, which is the wrong
//     one for a well-formed number and is part of the same artifact. Both edges
//     of the scale-2 bound are pinned by TestSettlementMessageAmountBound.
//
// Neither was found by FuzzTranslate, and that is worth recording rather than
// hiding. Two and a half million executions did not reach either, because the
// target fuzzes the AMOUNT and holds the asset at EUR, and a large amount only
// produces a message at all once the fuzzer has also assembled two valid IBANs
// and two non-empty names. A hand-written probe over ledger.Assets and the
// int64 boundaries found both in one run. A fuzz target explores the inputs it
// was given and no others, which is the honest limit of the technique.
//
// Refusing here rather than at Marshal is the same choice ibanOf makes. Both
// errors are correct; only the one raised here names the payment rather than an
// element inside a document, and only that one can be turned into a pacs.002 a
// customer can read.
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
// bank's own side from its own register, the counterparty's name from the
// instruction and its agent from the roster (see PartyDetails.Agent and
// SubmitPaymentTx). That is the whole of this change and the whole of why it matters:
// building an outbound message used to read the counterparty's deposit register
// for the name on the account, which is a read of another bank's book on the
// happy path of every submission.
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

// CreditTransferMessage renders a payment as the pacs.008 that carries it
// between banks.
//
// The two AGENTS are the two banks, and the header's To is whoever the message
// is being handed to next — the clearing house, on the first hop. Those are
// different questions with different answers, and conflating them would model a
// topology this system does not have: banks here meet at a CSM, never directly.
//
// It takes neither a context nor a Tx, and used not to: a payment recorded
// which participant and which account each side was, and building a message
// meant looking up the BIC and the account holder's NAME neither carried. Task
// 14.2 put both on the payment itself, at submission; Task 14.3 is what let
// partiesOf stop reading them off the store. So there is no I/O left here to
// need a unit of work for, and CreditTransferMessageTx — the variant that
// shared the caller's transaction — is gone with it: nothing downstream of
// SubmitAndInstruct needs to share a transaction with a function that reads
// nothing.
func (s *Network) CreditTransferMessage(p Payment, mc MessageContext) (iso20022.Envelope, error) {
	asset, err := s.assetOf(p)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	debtor, creditor := partiesOf(p)
	return creditTransfer(p, debtor, creditor, asset, mc)
}

// creditTransfer builds the message from data its caller already holds.
// Everything it does is a pure function of its arguments, which is what
// FuzzTranslate needs: CreditTransferMessage reads no store either (see its
// own doc), so there is no I/O anywhere on this path for a fuzz input to
// depend on.
func creditTransfer(p Payment, debtor, creditor messageParty, asset ledger.AssetCode, mc MessageContext) (iso20022.Envelope, error) {
	amt, err := amountOf(p.Amount, asset)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	dbtr, err := namedPartyOf("Dbtr", debtor.Name)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	dbtrIBAN, err := ibanOf("DbtrAcct", debtor.Identifier)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	cdtr, err := namedPartyOf("Cdtr", creditor.Name)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	cdtrIBAN, err := ibanOf("CdtrAcct", creditor.Identifier)
	if err != nil {
		return iso20022.Envelope{}, err
	}

	// The field order is the schema's, and it is the payment's own path: party,
	// its bank, the other bank, the other party. LclInstrm is absent because
	// SEPA credit transfer does not populate it, and SeqTp is absent because
	// pacs.008 has no such element at all — see iso20022.PaymentTypeInformation.
	txs := []iso20022.CreditTransferTransaction{{
		PmtId: paymentIdentificationOf(p),
		PmtTpInf: &iso20022.PaymentTypeInformation{
			SvcLvl: &iso20022.ServiceLevelChoice{Cd: iso20022.ServiceLevelSEPA},
		},
		IntrBkSttlmAmt: amt,
		ChrgBr:         iso20022.ChargeBearerFollowingServiceLevel,
		Dbtr:           dbtr,
		DbtrAcct:       cashAccount(dbtrIBAN),
		DbtrAgt:        agentOf(debtor.BIC),
		CdtrAgt:        agentOf(creditor.BIC),
		Cdtr:           cdtr,
		CdtrAcct:       cashAccount(cdtrIBAN),
		RmtInf:         remittanceOf(p.Description),
	}}

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
			IntrBkSttlmDt: settlementDateOf(p, mc),
			SttlmInf:      clearingSettlement(),
		},
		CdtTrfTxInf: txs,
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// DirectDebitMessage renders a collection as the pacs.003 that carries it.
//
// It is the mirror of CreditTransferMessage in the one way that matters: the
// SENDER is the party being paid. A push scheme's message travels with the
// money; a pull scheme's travels against it, which is why the creditor and its
// agent come first in the transaction and why the mandate has to travel too.
//
// It takes neither a context nor a Tx, for the same reason CreditTransferMessage
// stopped taking them: partiesOf reads nothing, and the mandate — the one piece
// of I/O this message ever needed — arrives already resolved as m, loaded by
// InstructionTx's tx.GetMandate before this is called. DirectDebitMessageTx,
// CreditTransferMessageTx's counterpart, is gone with it for the same reason.
func (s *Network) DirectDebitMessage(p Payment, m Mandate, mc MessageContext) (iso20022.Envelope, error) {
	asset, err := s.assetOf(p)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	debtor, creditor := partiesOf(p)
	return directDebit(p, m, debtor, creditor, asset, mc)
}

func directDebit(p Payment, m Mandate, debtor, creditor messageParty, asset ledger.AssetCode, mc MessageContext) (iso20022.Envelope, error) {
	amt, err := amountOf(p.Amount, asset)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	cdtr, err := namedPartyOf("Cdtr", creditor.Name)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	cdtrIBAN, err := ibanOf("CdtrAcct", creditor.Identifier)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	dbtr, err := namedPartyOf("Dbtr", debtor.Name)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	dbtrIBAN, err := ibanOf("DbtrAcct", debtor.Identifier)
	if err != nil {
		return iso20022.Envelope{}, err
	}
	if m.ID == "" {
		return iso20022.Envelope{}, ErrMandateRequired
	}

	local := iso20022.LocalInstrumentCore
	seq := iso20022.SequenceTypeRecurring
	txs := []iso20022.DirectDebitTransactionInformation{{
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
		CdtrAgt:  agentOf(creditor.BIC),
		Dbtr:     dbtr,
		DbtrAcct: cashAccount(dbtrIBAN),
		DbtrAgt:  agentOf(debtor.BIC),
		RmtInf:   remittanceOf(p.Description),
	}}

	doc := &iso20022.Pacs003{FIToFICstmrDrctDbt: iso20022.FIToFICustomerDirectDebit{
		GrpHdr: iso20022.DirectDebitGroupHeader{
			MsgId:         mc.MsgID,
			CreDtTm:       iso20022.ISODateTime{Time: mc.Now},
			NbOfTxs:       strconv.Itoa(len(txs)),
			IntrBkSttlmDt: settlementDateOf(p, mc),
			SttlmInf:      clearingSettlement(),
		},
		DrctDbtTxInf: txs,
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
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
// This is a Network method although it reads no store, and it takes no
// context for exactly that reason. Task 14.3 removed the asymmetry this
// comment used to name: CreditTransferMessage and DirectDebitMessage read no
// store either, now, so all three outbound builders share this shape. What
// still makes ReturnMessage a method rather than a function is narrower than
// "no I/O": the amount's scale comes from the scheme's asset and only the
// Network holds the scheme registry, which is an in-memory map, not a store —
// there is nothing here to cancel. A pacs.004 names no ACCOUNT HOLDERS: it
// refers to the original payment by identifier and carries amounts, so
// nothing in it needs a customer's name looked up, which is also why it is
// the one outbound builder with no messageParty in its signature at all. It
// does name a party — RtrRsnInf.Orgtr, the bank that decided to send the
// money back, see mc.orgtr() — and now two AGENTS too, see OrgnlTxRef below,
// both of which come off the payment's own DebtorDetails/CreditorDetails,
// which SubmitPaymentTx already resolved, so this needs no lookup of its own
// either.
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
		// The settlement agent that must reverse this return's two reserve
		// legs holds no payment row under sub-project 8 — see
		// iso20022.OriginalTransactionReference. Both agents are already on
		// the payment: DebtorDetails/CreditorDetails were resolved once, at
		// submission, and are not looked up again here.
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
// The direction sub-project 7b exists for. Everything above renders something
// this system already decided; everything below reads an instruction another
// bank decided, and the difference is that nothing here can be trusted to be
// self-consistent. A received message is the only thing the receiver has, and it
// carries no internal identifier of this system's — which is the constraint that
// shapes the whole half.

// CreditTransferRequest turns a received pacs.008 into a request this system
// can act on.
//
// A pacs.008 travels FROM the debtor's bank, routed by CdtrAgt, so the bank
// reading this message is the CREDITOR's — that is what routed the message
// here in the first place, and it is the only party this bank has any standing
// to resolve. It is resolved by ADDRESS, exactly as before —
// Network.ResolveIdentifierTx against the IBAN the message carries — and never
// by an internal account id, because the message has no element for one. That
// is also why an unresolvable creditor IBAN comes back as
// ErrAccountNotInParticipant and becomes AC01 on the wire: from the receiver's
// side, an address it cannot resolve for ITS OWN customer IS an incorrect
// account number.
//
// The DEBTOR is not resolved at all. It is the sending bank's customer, this
// bank's directory has no standing authority over whether that account exists,
// and sweeping the network for it produced a refusal — AC01 for a debtor IBAN
// nobody in this network holds — that was never this bank's to make. What comes
// back for the debtor is what the message itself says: the address it quoted,
// on Debtor, and the agent and name it asserted, on DebtorDetails. See
// localPartyIn.
//
// The scheme is the MESSAGE's, and it takes two elements to name it rather than
// one. Being a pacs.008 says the payment is a PUSH; the currency says which
// asset it settles in; and this network has one scheme for that pair or it
// refuses the message. Reading SchemeSEPACT off the message definition alone was
// right while SEPA's was the only push scheme registered, and wrong the moment a
// second one is — see schemeSettling, which is also where the refusal lives. What
// the message does NOT get to decide is that it is settled at all: a currency no
// push scheme settles in is refused rather than reinterpreted, because a receiver
// that took the sender's currency and some scheme's scale would book a number
// neither bank wrote down.
//
// It returns a REQUEST and not a Payment: nothing here is accepted, deduplicated
// or posted. That is SubmitPaymentTx's job, and the separation is what lets the
// mesh translate a message without a write and reject it before one.
func (s *Network) CreditTransferRequest(ctx context.Context, doc *iso20022.Pacs008) (InitiatePaymentRequest, error) {
	body := doc.FIToFICstmrCdtTrf
	tx, err := onlyTransaction("CdtTrfTxInf", body.CdtTrfTxInf, body.GrpHdr.NbOfTxs)
	if err != nil {
		return InitiatePaymentRequest{}, err
	}
	scheme, amount, err := s.schemeSettling(Push, tx.IntrBkSttlmAmt)
	if err != nil {
		return InitiatePaymentRequest{}, err
	}
	dbtrID, err := identifierIn("DbtrAcct", tx.DbtrAcct)
	if err != nil {
		return InitiatePaymentRequest{}, err
	}
	cdtrID, err := identifierIn("CdtrAcct", tx.CdtrAcct)
	if err != nil {
		return InitiatePaymentRequest{}, err
	}
	// The creditor is this bank's own customer on a push; the debtor is the
	// sending bank's and is recorded, not resolved.
	creditor, err := s.localPartyIn(ctx, cdtrID)
	if err != nil {
		return InitiatePaymentRequest{}, err
	}
	debtor := PartyRef{Identifier: dbtrID}
	return InitiatePaymentRequest{
		Scheme:          scheme,
		Debtor:          debtor,
		Creditor:        creditor,
		Amount:          amount,
		EndToEndID:      endToEndIn(tx.PmtId.EndToEndId),
		Description:     remittanceIn(tx.RmtInf),
		DebtorDetails:   PartyDetails{Agent: agentIn(tx.DbtrAgt), Name: nameIn(tx.Dbtr)},
		CreditorDetails: PartyDetails{Agent: agentIn(tx.CdtrAgt), Name: nameIn(tx.Cdtr)},
	}, nil
}

// DirectDebitRequest turns a received pacs.003 into a request this system can
// act on.
//
// A pacs.003 travels FROM the creditor's bank, routed by DbtrAgt, so the bank
// reading this message is the DEBTOR's — the mirror of CreditTransferRequest in
// exactly the way the direction implies, and no further: the DEBTOR is resolved
// by ADDRESS, exactly as before — Network.ResolveIdentifierTx against the IBAN
// the message carries, which still lists every participant and reads every
// register; narrowing WHICH party is put through that sweep did not narrow the
// sweep itself. The CREDITOR is recorded from what the message says and not
// resolved, for the identical reason CreditTransferRequest does not resolve a
// pacs.008's debtor — the creditor is the SENDING bank's customer, not this
// bank's to look up. The account elements are read for what they are rather
// than by position — a reader that took the first account element as the
// debtor's, as it is in a pacs.008, would produce a collection pointing the
// wrong way and resolve successfully while doing it.
//
// And it carries a mandate. An empty MndtId is refused here rather than left for
// SDD.Validate, which would refuse it too: this is another bank's claim on this
// bank's customer's account, the mandate is the only thing that makes it
// authorised, and the message that came with no mandate should not become a
// request that looks like one.
func (s *Network) DirectDebitRequest(ctx context.Context, doc *iso20022.Pacs003) (InitiatePaymentRequest, error) {
	body := doc.FIToFICstmrDrctDbt
	tx, err := onlyTransaction("DrctDbtTxInf", body.DrctDbtTxInf, body.GrpHdr.NbOfTxs)
	if err != nil {
		return InitiatePaymentRequest{}, err
	}
	scheme, amount, err := s.schemeSettling(Pull, tx.IntrBkSttlmAmt)
	if err != nil {
		return InitiatePaymentRequest{}, err
	}
	mandate := tx.DrctDbtTx.MndtRltdInf.MndtId
	if mandate == "" {
		return InitiatePaymentRequest{}, fmt.Errorf("%w: DrctDbtTx/MndtRltdInf/MndtId", ErrMandateRequired)
	}
	dbtrID, err := identifierIn("DbtrAcct", tx.DbtrAcct)
	if err != nil {
		return InitiatePaymentRequest{}, err
	}
	cdtrID, err := identifierIn("CdtrAcct", tx.CdtrAcct)
	if err != nil {
		return InitiatePaymentRequest{}, err
	}
	// The debtor is this bank's own customer on a pull; the creditor is the
	// sending bank's and is recorded, not resolved.
	debtor, err := s.localPartyIn(ctx, dbtrID)
	if err != nil {
		return InitiatePaymentRequest{}, err
	}
	creditor := PartyRef{Identifier: cdtrID}
	return InitiatePaymentRequest{
		Scheme:          scheme,
		Debtor:          debtor,
		Creditor:        creditor,
		Amount:          amount,
		MandateID:       MandateID(mandate),
		EndToEndID:      endToEndIn(tx.PmtId.EndToEndId),
		Description:     remittanceIn(tx.RmtInf),
		DebtorDetails:   PartyDetails{Agent: agentIn(tx.DbtrAgt), Name: nameIn(tx.Dbtr)},
		CreditorDetails: PartyDetails{Agent: agentIn(tx.CdtrAgt), Name: nameIn(tx.Cdtr)},
	}, nil
}

// onlyTransaction is the receiver's side of NbOfTxs, plus this system's own
// limit of one payment per message.
//
// The count is CHECKED and not recomputed, which is the whole reason the element
// is a string carrying the sender's assertion — see CreditTransferGroupHeader.
// TestSettlementMessageNbOfTxsSurvivesATruncatedFile pins that neither the
// encoder nor the decoder touches it, so a file that lost a transaction in
// transit arrives saying so; this is the only place in the system that acts on
// it. A truncated file read as a complete one is a payment that silently never
// happened.
//
// More than one transaction is refused rather than partially read. The standard
// is a bulk format and this system is not: an InitiatePaymentRequest is one
// payment, and quietly translating the first of five would drop four.
//
// Neither refusal is a sentinel from errors.go, and that is deliberate. There is
// no condition in this system's own vocabulary for "the file you sent
// contradicts itself" or "this agent does not do bulk" — the same situation
// namedPartyOf is in — so both fall to MS03 through ReasonFor's default, which
// says "this agent could not carry it out". The free text beside the code is
// where the detail reaches the sender, and it says which count disagreed.
func onlyTransaction[T any](element string, txs []T, nbOfTxs string) (T, error) {
	var zero T
	if err := checkNbOfTxs(element, txs, nbOfTxs); err != nil {
		return zero, err
	}
	if len(txs) != 1 {
		return zero, fmt.Errorf("payment: %s carries %d transactions; this system reads one payment per message",
			element, len(txs))
	}
	return txs[0], nil
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
// asymmetrical in a way nothing but a second scheme could reveal. The mesh's
// two-asset settlement fixture is what revealed it.
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
// It used to resolve BOTH sides — partiesIn swept every member's register for
// both addresses — and that sweep is what let a receiving bank reach every
// bank's book (mesh/books_test.go), and what turned a debtor IBAN nobody in
// this network holds into an AC01 that was never this bank's refusal to make:
// the debtor is the SENDING bank's customer, and this bank's directory has no
// authority over whether that account exists. The counterparty is not resolved
// at all now; it is recorded from what the message itself says — see
// CreditTransferRequest's DebtorDetails/CreditorDetails and agentIn/nameIn.
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
// It drives ResolveIdentifierTx rather than reimplementing the sweep, which
// matters for a property that is easy to lose: two banks holding one identifier
// is ErrIdentifierAmbiguous there, not the first hit, and choosing between them
// here would route another bank's payment on the strength of listing order.
//
// # Only a NOT-FOUND becomes a domain error
//
// The outbound direction used to have the identical discipline, in partyTx —
// see PartyDetails' doc comment on why that function is gone. It is a sharper
// hazard here, because for a directory lookup a not-found IS the expected
// failure and `if err != nil { return ErrAccountNotInParticipant }` is the
// obvious shape. It is wrong: that error becomes AC01 "incorrect account
// number" in the pacs.002 this system sends back, so a dropped connection or a
// cancelled context would tell another bank its customer's IBAN was bad and send
// it hunting a fault that does not exist.
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
// document. What compaction cannot do is run backwards, and this repository
// stores the DISPLAY form: seed.go writes SE89-AURORA-1001 for an account whose
// pacs.008 carries SE89AURORA1001.
//
// Matching those is not this function's job and must not be, because a fallback
// here would help no other caller and would mean this package second-guessing
// the directory it was told to use. It belongs to the comparison itself, and
// that is where it now lives: deposit.Identifier.MatchValue canonicalises BOTH
// sides for the IBAN scheme, ListDepositAccountsByIdentifier compares with it in
// both stores, and storetest holds the two implementations together. What
// reaches ResolveIdentifierTx from here is therefore an address it can resolve
// whichever form the account was opened with. See
// TestCreditTransferRoundTripsThroughTheWireForSeedShapedAddresses.
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
// Two return values because they are two different facts, and the mesh acts on
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
// see iso20022.OriginalTransactionReference for why a settlement agent under
// sub-project 8 has no row to read them from instead.
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
// different questions: mesh.rejectionText, over iso20022.StatusReason, and
// ReturnReason below, over iso20022.ReturnReason — the sibling external code
// set pacs004.go keeps as a separate type precisely so that a rejection
// reason cannot be used as a return reason with nothing to notice. It lives
// here rather than in mesh because ReturnReason does, and mesh already
// imports payment; mesh.rejectionText calls through to this rather than
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
// It moved here from mesh/centralbank.go, where it used to live beside
// mesh.rejectionText (mesh/bank.go, which reads the sibling StatusReason code
// set for a rejection), because ReadReturn needs this exact reading a second
// time — for ReturnInstruction.Reason above — and mesh cannot be imported
// from this package (mesh already imports payment). mesh.receiveReturn now
// calls straight through to this instead of keeping its own copy;
// TestTheReturnsReasonTravelsFromTheAskingBankToTheLedgers still passes
// unchanged, because it asserts through the mesh, not against this function
// directly.
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

// checkNbOfTxs holds a sender to its own count. It is onlyTransaction's first
// half, split out for the messages this system reads in bulk — a settlement
// instruction is many legs by nature, and ReadReturn's pacs.004 is shaped for
// several returns per file even though this system's own mesh only ever
// sends one — so the count matters for both and the one-transaction limit
// does not apply to either.
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
				// Named after the reference and nothing more, for the reason the
				// paragraph above gives: a cycle id and a payment id are equally
				// opaque to the member reading this, and the sender saying which
				// kind it sent would be telling that bank something it has no row
				// to resolve. It used to read "Settlement of clearing cycle …",
				// which was accurate while the cut-off was the only thing that
				// settled and became a false statement on the wire the moment a
				// return could produce a statement too.
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
// what the sender knew; this is what the receiver can learn, and they are not the
// same set of facts. ParticipantID never reaches the wire at all — nothing in the
// message carries it, because a member bank has no use for the central bank's
// internal name for itself. StatementRef is on the wire, in Stmt/Id, but
// ReadStatement does not surface it here: it is the account servicer's own
// reference for the STATEMENT, not for the movement — the settlement row's id on
// the cut-off path, and on the return path (sub-project 8's Task 16d) a value
// that is not any row's key at all, see SettlementStatement's doc. A member has
// nothing to do with either and no way to check it against anything it holds,
// and carrying it through would let a caller mistake the sender's own reference
// for something the receiver can act on. Collapsing the two types would put
// fields on the receiving side that are either always empty or copied from the
// wire with nothing to verify them against, and invite a caller to trust them
// either way.
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
	// consequence, and belongs with Task 19's closing-balance check rather than
	// beside it.
	ValueDate time.Time
}

// ReadStatement reads a received camt.053 as the movements it advises.
//
// One entry per statement is REQUIRED and more is refused whole. This system's
// central bank posts exactly one netting movement per member per cycle, so a
// statement carrying two is one this reader has no rule for — and posting the
// first while dropping the second would move a bank's reserve mirror by the wrong
// amount with nothing anywhere recording it. It is onlyTransaction's argument,
// made about a different message.
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
// Admission
// ---------------------------------------------------------------------------
//
// Three messages, and between them they carry a whole admission: the bank asks
// for a settlement account, the settlement agent says which accounts it now
// holds, or it refuses. Nothing here says anything about SCHEME MEMBERSHIP,
// which is contractual and travels on no message at all — the clearing house's
// routing entry falls out of the acknowledgement, which is why that institution
// writes a row from a message it did not originate and is not addressed on. See
// iso20022's Acmt007, which records the whole of that framing and the fact that
// this is not how a real RTGS account is opened.
//
// ONE CURRENCY PER REQUEST, and every asymmetry below follows from it.
// acmt.007's Acct/Ccy is minOccurs="1" maxOccurs="1", so a bank clearing a euro
// scheme and a dollar scheme sends two requests; acmt.010's AccountForAction1 is
// unbounded, so one acknowledgement lists every account the servicer holds for
// that address. The consequence is not the extra message: it is that
// Refs/PrcId — the process id, mandatory on all three — is the conversation's
// ONLY correlator, because the acknowledgement carries no back-reference to the
// request that caused it. AdmissionRequest.Ref and
// AdmissionAcknowledgement.Ref are that value on this side of the wire.

// countryOf is the country an institution is addressed in, read out of its own
// BIC.
//
// Characters five and six of a BIC are its ISO 3166 country code — that is what
// ISO 9362 puts there — so a bank that has told this system its address has
// already told it its country. The acmt.007's Org is an Organisation33, which
// makes CtryOfOpr and a legal address mandatory because eBAM was written for a
// corporate opening an account at its bank; this system holds no address for a
// bank and will not invent one, so the country is derived and the rest of
// PostalAddress24 is absent rather than empty (see iso20022.PostalAddress).
//
// It is only ever called on a BIC that has been through Validate, which
// guarantees six leading letters, so the slice cannot be out of range. Every
// caller below validates first for exactly that reason.
func countryOf(b iso20022.BIC) string { return string(b)[4:6] }

// AdmissionMessage renders one asset's half of an admission as the acmt.007 a
// bank sends to apply for a settlement account.
//
// ONE message per asset, because the schema carries one currency per request.
// A bank joining in two assets sends two of these under one process id, and it
// is the process id — not any back-reference — that says they are one
// admission.
//
// servicer is separate from mc.To because the two are different questions on
// this hop. The header says who this message is being handed to, which is the
// CLEARING HOUSE, because a member bank in this system addresses the clearing
// house and nothing else; AcctSvcrId says which institution is being asked to
// open the account, which is the settlement agent the clearing house relays it
// to. That element is written and never read back — nothing in this system acts
// on it — for the reason the legal name and the country are: the schema makes
// it mandatory. See ReadAdmissionRequest, which says what a receiver does take
// off this message.
//
// It is a free function and not a method on Network because it reads no store
// and needs no scheme: everything on the wire is on the request or in the
// context. SettlementMessage and StatusMessage are free for the same reason.
func AdmissionMessage(in AdmissionRequest, servicer iso20022.BIC, mc MessageContext) (iso20022.Envelope, error) {
	if err := in.BIC.Validate(); err != nil {
		return iso20022.Envelope{}, fmt.Errorf("payment: the applicant's own address: %w", err)
	}
	if err := servicer.Validate(); err != nil {
		return iso20022.Envelope{}, fmt.Errorf("payment: AcctSvcrId: %w", err)
	}
	if in.Name == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: Org/FullLglNm", iso20022.ErrMissingElement)
	}
	if in.Asset == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: Acct/Ccy", iso20022.ErrMissingElement)
	}
	if in.Ref == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: Refs/PrcId/Id", iso20022.ErrMissingElement)
	}
	country := countryOf(in.BIC)
	doc := &iso20022.Acmt007{AcctOpngReq: iso20022.AccountOpeningRequest{
		Refs: iso20022.AccountRequestReferences{
			MsgId: iso20022.MessageIdentification{Id: mc.MsgID, CreDtTm: iso20022.ISODateTime{Time: mc.Now}},
			// The process id is echoed and its instant is stamped now, because
			// this system keeps no record of when an admission began — the
			// correlator is the Id, and MessageIdentification1 makes the instant
			// mandatory beside it. Said here rather than left to look like the
			// process's own creation time.
			PrcId: iso20022.MessageIdentification{Id: in.Ref, CreDtTm: iso20022.ISODateTime{Time: mc.Now}},
		},
		Acct:       iso20022.RequestedAccount{Ccy: string(in.Asset)},
		AcctSvcrId: agentOf(servicer),
		Org: iso20022.AccountOwner{
			FullLglNm: in.Name,
			CtryOfOpr: country,
			LglAdr:    iso20022.PostalAddress{Ctry: country},
			OrgId:     iso20022.OrganisationIdentification{AnyBIC: in.BIC},
		},
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// ReadAdmissionRequest reads a received acmt.007 as the request it carries: who
// is applying, in which asset, and which admission this is one turn of.
//
// # The BIC is the load-bearing element and is checked here
//
// It is what the settlement agent keys its own member row by
// (settlement_members.bic in store/pg) and what the clearing house keys its
// routing entry by. A request carrying an empty or malformed address would open
// an account for a bank nobody can address and file it under a key no later
// message can reach — OpenSettlementAccountTx says so in as many words and names
// this reader as what has to run Validate before it. So this refuses both, and
// it does the STRUCTURAL check rather than only the presence one, because a
// malformed BIC is as unaddressable as an absent one.
//
// The asset is the second: it decides which account is being asked for, and a
// request with no currency is one an account servicer cannot act on.
//
// The process id is the third, and it is about the refusal rather than the
// account. The clearing house's ErrBICAlreadyAdmitted is keyed on it, so a
// request quoting nothing would make every admission of a taken address look
// like the same one.
//
// # It does not assume validate() ran
//
// iso20022.Unmarshal validates, so a document that arrived on the wire has been
// through AccountOpeningRequest.validate and every check below has already
// passed. This checks anyway, for ReadReturn's reason: a document handed to this
// function need not have come from Unmarshal at all, and the cost of being wrong
// is a row keyed by nothing in another institution's store.
//
// AcctSvcrId, the legal name's country and the address are read by nothing. The
// name IS read — the settlement agent names the account it opens after the
// member it opened it for — so an empty one is refused too.
func ReadAdmissionRequest(doc *iso20022.Acmt007) (AdmissionRequest, error) {
	req := doc.AcctOpngReq
	bic := req.Org.OrgId.AnyBIC
	if bic == "" {
		return AdmissionRequest{}, fmt.Errorf(
			"payment: Org/OrgId/AnyBIC is absent; this request names no applicant and its account would be keyed by nothing")
	}
	if err := bic.Validate(); err != nil {
		return AdmissionRequest{}, fmt.Errorf("payment: Org/OrgId/AnyBIC: %w", err)
	}
	if req.Org.FullLglNm == "" {
		return AdmissionRequest{}, fmt.Errorf("%w: Org/FullLglNm", iso20022.ErrMissingElement)
	}
	if req.Acct.Ccy == "" {
		return AdmissionRequest{}, fmt.Errorf(
			"%w: Acct/Ccy; this request says which bank and not which account", iso20022.ErrMissingElement)
	}
	if req.Refs.PrcId.Id == "" {
		return AdmissionRequest{}, fmt.Errorf(
			"%w: Refs/PrcId/Id; nothing else in this family correlates one admission", iso20022.ErrMissingElement)
	}
	return AdmissionRequest{
		Name:  req.Org.FullLglNm,
		BIC:   bic,
		Asset: ledger.AssetCode(req.Acct.Ccy),
		Ref:   req.Refs.PrcId.Id,
	}, nil
}

// AdmissionAcknowledgementMessage renders an account servicer's answer as the
// acmt.010 that says which accounts it now holds for an address.
//
// EVERY account, not the one this request asked for. AccountForAction1 is
// unbounded and the servicer's row carries its whole set, so a bank's second
// acknowledgement lists both of its accounts — which is what lets the two
// readers on the far side of the relay each take what they need in one pass.
//
// The accounts are emitted in ASSET ORDER. Accounts is a map, Go randomises its
// iteration, and a message whose transactions came out in a different order on
// every identical build would be two byte sequences for one answer — the same
// argument csm.settlementLegs makes about a settlement instruction's legs, and
// the same one AdmitMemberTx makes about the assets it appends to a roster
// entry.
//
// # It names NOBODY, and that is the schema's doing
//
// acmt.010 identifies the account owner with an OrganisationIdentification29 —
// a BIC, an LEI and generic identifiers — and there is no legal name, no country
// and no address anywhere on the message. The request names the applicant with
// an Organisation33 and the acknowledgement does not.
//
// So nothing downstream of this message can learn a member's NAME from it, and
// nothing downstream needs to. The clearing house writes routing; the joining
// bank writes its own row and knows its own name. AdmissionAcknowledgement has
// no Name field for the same reason — it briefly did, and RosterEntry records
// why both went.
func AdmissionAcknowledgementMessage(ack AdmissionAcknowledgement, mc MessageContext) (iso20022.Envelope, error) {
	if err := ack.BIC.Validate(); err != nil {
		return iso20022.Envelope{}, fmt.Errorf("payment: the account owner's address: %w", err)
	}
	if ack.Ref == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: Refs/PrcId/Id", iso20022.ErrMissingElement)
	}
	if len(ack.Accounts) == 0 {
		return iso20022.Envelope{}, fmt.Errorf(
			"%w: AcctId; an acknowledgement naming no account would admit a bank to nothing", iso20022.ErrMissingElement)
	}
	accounts := make([]iso20022.OpenedAccount, 0, len(ack.Accounts))
	for _, asset := range slices.Sorted(maps.Keys(ack.Accounts)) {
		id := ack.Accounts[asset]
		if id == "" {
			return iso20022.Envelope{}, fmt.Errorf("%w: AcctId/Id/Othr/Id for %s", iso20022.ErrMissingElement, asset)
		}
		accounts = append(accounts, iso20022.OpenedAccount{
			// The generic arm, not the IBAN one, for camt.053's reason one
			// message over: a reserve account at a central bank is not a payment
			// address and no customer ever quotes it.
			Id:  iso20022.AccountIdentification4Choice{Othr: &iso20022.GenericAccountIdentification{Id: string(id)}},
			Ccy: string(asset),
		})
	}

	doc := &iso20022.Acmt010{AcctReqAck: iso20022.AccountRequestAcknowledgement{
		Refs: iso20022.AccountAcknowledgementReferences{
			// What this acknowledges. An AcctReqAck's element name says it
			// acknowledges something without saying what, which is why
			// References5 makes ReqTp mandatory where References4 has no such
			// element at all.
			ReqTp: iso20022.UseCaseAccountOpening,
			MsgId: iso20022.MessageIdentification{Id: mc.MsgID, CreDtTm: iso20022.ISODateTime{Time: mc.Now}},
			PrcId: iso20022.MessageIdentification{Id: ack.Ref, CreDtTm: iso20022.ISODateTime{Time: mc.Now}},
		},
		AcctId:     accounts,
		OrgId:      iso20022.OrganisationIdentification{AnyBIC: ack.BIC},
		AcctSvcrId: agentOf(mc.From),
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}

// ReadAdmissionAcknowledgement reads a received acmt.010 as what the account
// servicer says it holds: whose address, which accounts, and which admission.
//
// # An account with no currency is refused, and that is the guard
//
// The currency is what decides which asset's account this is, and both readers
// of this value key on it: the clearing house records the assets a member
// clears in, and the bank records a settlement reference against its own
// internal accounts for that asset. An account with an empty currency would be
// filed under the empty asset in both — a reserve nothing settles through and a
// reference nothing quotes. It is the same shape of hole ReadReturn's empty
// OrgnlTxId was, which would have moved real reserves under the key
// ":return-settle".
//
// Two accounts in ONE currency are refused as well, whole rather than
// last-wins. This system's servicer opens one account per (BIC, asset) and
// cannot produce such a message; a message that carried one would be a servicer
// saying it holds two reserves in one asset for one member, which no reader here
// has a rule for.
//
// # It cannot answer "whose bank is this", and nothing asks it to
//
// There is no legal name on an acmt.010 — see AdmissionAcknowledgementMessage —
// so what this reader produces is an ADDRESS, a set of accounts and an admission
// reference. Both acts driven from it get on with exactly that: AdmitMemberTx
// writes routing, and RecordMembershipTx is a bank writing its own row.
func ReadAdmissionAcknowledgement(doc *iso20022.Acmt010) (AdmissionAcknowledgement, error) {
	ack := doc.AcctReqAck
	bic := ack.OrgId.AnyBIC
	if bic == "" {
		return AdmissionAcknowledgement{}, fmt.Errorf(
			"payment: OrgId/AnyBIC is absent; this acknowledgement names no account owner")
	}
	if err := bic.Validate(); err != nil {
		return AdmissionAcknowledgement{}, fmt.Errorf("payment: OrgId/AnyBIC: %w", err)
	}
	if ack.Refs.PrcId.Id == "" {
		return AdmissionAcknowledgement{}, fmt.Errorf(
			"%w: Refs/PrcId/Id; nothing else in this family correlates one admission", iso20022.ErrMissingElement)
	}
	if len(ack.AcctId) == 0 {
		return AdmissionAcknowledgement{}, fmt.Errorf(
			"%w: AcctId; an acknowledgement naming no account would admit a bank to nothing", iso20022.ErrMissingElement)
	}
	accounts := make(map[ledger.AssetCode]ledger.AccountID, len(ack.AcctId))
	for i, a := range ack.AcctId {
		if a.Ccy == "" {
			return AdmissionAcknowledgement{}, fmt.Errorf(
				"%w: AcctId[%d]/Ccy; without it nothing says which asset's account this is", iso20022.ErrMissingElement, i)
		}
		if a.Id.Othr == nil || a.Id.Othr.Id == "" {
			return AdmissionAcknowledgement{}, fmt.Errorf(
				"%w: AcctId[%d]/Id/Othr/Id; a reserve account has no IBAN and this one has no identifier either",
				iso20022.ErrMissingElement, i)
		}
		asset := ledger.AssetCode(a.Ccy)
		if _, dup := accounts[asset]; dup {
			return AdmissionAcknowledgement{}, fmt.Errorf(
				"payment: AcctId names two %s accounts; a servicer holds one settlement account per asset per member", asset)
		}
		accounts[asset] = ledger.AccountID(a.Id.Othr.Id)
	}
	return AdmissionAcknowledgement{
		BIC:      bic,
		Accounts: accounts,
		Ref:      ack.Refs.PrcId.Id,
	}, nil
}

// AdmissionRejectionMessage renders a refusal as the acmt.011 that ends an
// admission the other way.
//
// It carries no account, because none was opened, and the applicant stays
// whatever it was before it asked — Founded, in this system, which is a working
// bank that cannot pay.
//
// # The reason is PROSE, and that is the standard's decision rather than this
// system's
//
// References6 makes RjctnRsn a repeated Max350Text: an account-management
// refusal is free text where a payment rejection is an external code set. That
// is why payment's reasonTable gives the admission sentinels the EMPTY code —
// there is nothing on this message for a StatusReason to go in — and why the
// refusal a counterparty reads here is the Go error's own words. See
// iso20022.AccountRejectionReferences, which records the contrast.
//
// It is refused if the reason is blank, because Max350Text has minLength 1 and a
// refusal that says nothing is one nobody can act on.
//
// # rejected is the request being refused, quoted back
//
// RjctdReqId is the back-reference the ACKNOWLEDGEMENT does not have: on a
// rejection the schema makes naming the refused request mandatory. It is passed
// in whole — the identifier and the instant the sender stamped on it — rather
// than rebuilt here, so the element says what the request said instead of what
// this message's clock says.
func AdmissionRejectionMessage(in AdmissionRequest, rejected iso20022.MessageIdentification,
	reason string, mc MessageContext) (iso20022.Envelope, error) {

	if err := in.BIC.Validate(); err != nil {
		return iso20022.Envelope{}, fmt.Errorf("payment: the refused applicant's address: %w", err)
	}
	if in.Ref == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: Refs/PrcId/Id", iso20022.ErrMissingElement)
	}
	if reason == "" {
		return iso20022.Envelope{}, fmt.Errorf(
			"%w: Refs/RjctnRsn; a refusal that says nothing is one nobody can act on", iso20022.ErrMissingElement)
	}
	if rejected.Id == "" {
		return iso20022.Envelope{}, fmt.Errorf("%w: Refs/RjctdReqId/Id", iso20022.ErrMissingElement)
	}
	if rejected.CreDtTm.IsZero() {
		return iso20022.Envelope{}, fmt.Errorf("%w: Refs/RjctdReqId/CreDtTm", iso20022.ErrMissingElement)
	}
	doc := &iso20022.Acmt011{AcctReqRjctn: iso20022.AccountRequestRejection{
		Refs: iso20022.AccountRejectionReferences{
			RjctdReqTp: iso20022.UseCaseAccountOpening,
			RjctnRsn:   []string{reason},
			RjctdReqId: rejected,
			MsgId:      iso20022.MessageIdentification{Id: mc.MsgID, CreDtTm: iso20022.ISODateTime{Time: mc.Now}},
			PrcId:      iso20022.MessageIdentification{Id: in.Ref, CreDtTm: iso20022.ISODateTime{Time: mc.Now}},
		},
		AcctSvcrId: agentOf(mc.From),
		OrgId:      iso20022.OrganisationIdentification{AnyBIC: in.BIC},
	}}
	return iso20022.Envelope{
		AppHdr:   mc.header(doc.MessageDefinitionIdentifier()),
		Document: doc,
	}, nil
}
