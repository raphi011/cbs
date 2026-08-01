package payment

import (
	"errors"

	"github.com/raphi011/cbs/iso20022"
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
// EVERY sentinel in errors.go must appear. An error that cannot reach a
// counterparty is mapped to the empty code with a comment saying why, rather
// than omitted — omission is indistinguishable from an oversight, which is the
// exact failure this table exists to prevent. TestReasonTableCoversEverySentinel
// parses errors.go and fails on any gap.
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

	// --- Classified as never reaching a counterparty ---
	//
	// Each is a failure of THIS system's own bookkeeping rather than a
	// judgement about the instruction, so there is nothing truthful to tell the
	// sender. They surface as dead letters in the mesh, which is louder than a
	// misleading code.

	// A lookup for an id this system generated and then could not find is a
	// bug here, not a defect in the message.
	{ErrPaymentNotFound, "ErrPaymentNotFound", ""},
	{ErrCycleNotFound, "ErrCycleNotFound", ""},
	{ErrSettlementNotFound, "ErrSettlementNotFound", ""},

	// Cycle lifecycle errors reach only the operator who drove the cycle into
	// the wrong state; no counterparty ever sees one.
	{ErrCycleNotClosed, "ErrCycleNotClosed", ""},
	{ErrCycleAlreadyOpen, "ErrCycleAlreadyOpen", ""},

	// An illegal transition means this system tried to move a payment
	// somewhere its own state machine forbids. Telling the counterparty
	// "rejected, unspecified" would hide a defect behind a plausible message.
	{ErrInvalidStateTransition, "ErrInvalidStateTransition", ""},
}

// reasonFor maps an error to the code a pacs.002 should carry.
//
// It unwraps, because the payment layer wraps freely and a table keyed on
// identity alone would degrade to MS03 for most real failures — silently,
// which is the failure mode this whole arrangement exists to prevent.
//
// An error the table does not know is MS03 rather than a panic. An actor that
// crashed instead of answering would be a worse outcome than an imprecise
// code, and the exhaustiveness test is what stops that path being reachable
// for a sentinel.
func reasonFor(err error) iso20022.StatusReason {
	for _, m := range reasonTable {
		if m.Code != "" && errors.Is(err, m.Err) {
			return m.Code
		}
	}
	return iso20022.StatusReasonNotSpecifiedAgentGenerated
}
