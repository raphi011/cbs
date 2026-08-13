package api

import (
	"errors"
	"net/http"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
)

// errorStatus maps a domain sentinel error to an HTTP status code. Unknown
// errors fall through to 500.
func errorStatus(err error) int {
	var refused badRequest
	switch {
	case errors.As(err, &refused):
		return http.StatusBadRequest

	case errors.Is(err, ledger.ErrLedgerNotFound),
		errors.Is(err, ledger.ErrSubledgerNotFound),
		errors.Is(err, ledger.ErrAccountNotFound),
		errors.Is(err, ledger.ErrTransactionNotFound),
		errors.Is(err, deposit.ErrAccountNotFound),
		errors.Is(err, deposit.ErrHoldNotFound),
		errors.Is(err, deposit.ErrSnapshotNotFound),
		errors.Is(err, deposit.ErrIdentifierNotFound),
		errors.Is(err, payment.ErrParticipantNotFound),
		errors.Is(err, payment.ErrPaymentNotFound),
		errors.Is(err, payment.ErrMessageNotFound),
		errors.Is(err, payment.ErrMandateNotFound),
		errors.Is(err, payment.ErrCycleNotFound),
		errors.Is(err, payment.ErrSettlementNotFound),
		errors.Is(err, payment.ErrSchemeNotFound),
		errors.Is(err, payment.ErrAccountNotInParticipant),
		errors.Is(err, lending.ErrFacilityNotFound),
		errors.Is(err, product.ErrProductNotFound),
		// A product with no published version in force is a price that does not
		// exist yet, which is a missing resource rather than a refused one.
		errors.Is(err, product.ErrVersionNotFound):
		return http.StatusNotFound

	case errors.Is(err, ledger.ErrDuplicateIdempotencyKey),
		errors.Is(err, ledger.ErrTransactionAlreadyReversed),
		errors.Is(err, payment.ErrDuplicateEndToEndID),
		errors.Is(err, payment.ErrCycleAlreadyOpen),
		// A billing cycle already on the schedule is the already-applied category
		// exactly: the request is valid and the state already reflects it. 409 is
		// what tells a retrying proxy that its first attempt landed, where 422 would
		// read as "this line cannot be billed".
		errors.Is(err, lending.ErrCycleAlreadyBilled),
		// An identifier another account at this bank already holds is the same
		// already-applied shape: the request is well formed, and what stops it is
		// that the address is spoken for.
		errors.Is(err, deposit.ErrIdentifierTaken),
		// An address that resolves to more than one account is a conflict IN THE
		// DATA, not a malformed request: the answer exists and is contested, which
		// is what tells a client the situation needs a human rather than a retry.
		errors.Is(err, deposit.ErrIdentifierAmbiguous):
		return http.StatusConflict

	case errors.Is(err, ledger.ErrInsufficientBalance),
		errors.Is(err, deposit.ErrInsufficientAvailable),
		errors.Is(err, deposit.ErrAccountFrozen),
		// Dormancy is an ordinary account state with an ordinary rule — credits
		// revive it, debits wait for a reactivation — so a refused debit is a
		// business-state violation like a frozen one, not malformed input.
		errors.Is(err, deposit.ErrAccountDormant),
		errors.Is(err, deposit.ErrAccountClosed),
		errors.Is(err, deposit.ErrAccountNotEmpty),
		errors.Is(err, deposit.ErrHoldNotActive),
		errors.Is(err, deposit.ErrInvalidStatusTransition),
		errors.Is(err, payment.ErrCycleNotOpen),
		errors.Is(err, payment.ErrCycleNotClosed),
		// The cycle is closed and its positions are netted; what stops it is that
		// this clearing house holds no output file for a payment in it.
		errors.Is(err, payment.ErrCycleNotReleasable),
		errors.Is(err, payment.ErrInvalidStateTransition),
		errors.Is(err, payment.ErrMandateRevoked),
		errors.Is(err, payment.ErrMandateMismatch),
		errors.Is(err, payment.ErrMandateExceeded),
		errors.Is(err, payment.ErrMandateRequired),
		errors.Is(err, payment.ErrSchemeUnsupportedReturn),
		errors.Is(err, payment.ErrAssetMismatch),
		// The bank exists and the asset exists; this bank simply holds no accounts in
		// it. 404 would read as "participant not found", and 422 matches the sibling
		// underfunded-member failure.
		errors.Is(err, payment.ErrParticipantAssetNotFound),
		// A bank the settlement agent holds no account for is a bank that has founded
		// itself and not yet joined — a legitimate state since admission became a
		// conversation, and a refusal about that bank's membership rather than about
		// anything in the request. 422 for the same reason the asset case above is:
		// the request is well formed and the state refuses it.
		errors.Is(err, payment.ErrSettlementMemberNotFound),
		errors.Is(err, lending.ErrFacilityClosed),
		errors.Is(err, lending.ErrFacilityNotEmpty),
		errors.Is(err, lending.ErrLimitExceeded),
		errors.Is(err, lending.ErrAlreadyDisbursed),
		errors.Is(err, lending.ErrNothingOutstanding),
		// Its mirror: the bank owes this facility nothing to refund. Same
		// category and so the same status — a well-formed request against state
		// that has nothing for it to do.
		errors.Is(err, lending.ErrNoRefundOutstanding),
		// ErrWrongFacilityKind is 422 rather than 400 deliberately: the request is
		// well formed and the field values are valid, but this facility is the wrong
		// product for the operation — the same category as ErrCycleNotOpen.
		errors.Is(err, lending.ErrWrongFacilityKind),
		// The catalogue's four refusals are the same category: each request is well
		// formed and its field values are valid, and the state is what refuses it.
		errors.Is(err, product.ErrProductRetired),
		errors.Is(err, product.ErrKindMismatch),
		errors.Is(err, product.ErrVersionPublished),
		errors.Is(err, product.ErrRetroactivePublish),
		errors.Is(err, deposit.ErrProductRequired),
		// Two accounts at one bank in two currencies.
		errors.Is(err, deposit.ErrAssetMismatch),
		// The account exists and the request is well formed; it simply carries no
		// address in the kind the scheme routes on, or the address it quoted belongs
		// to a different account, or it quoted none and the account has several the
		// scheme could route on so initiation will not pick one.
		errors.Is(err, payment.ErrUnaddressableAccount),
		errors.Is(err, payment.ErrIdentifierMismatch),
		errors.Is(err, payment.ErrAmbiguousAddress),
		// The request is well-formed JSON that named no counterparty (or an invalid
		// one) — the same category as an unaddressable account: nothing on the
		// submission path looks the other side up, so the instruction must carry it.
		errors.Is(err, payment.ErrCounterpartyNotNamed),
		// Its sibling, and now the narrow case: the counterparty's BIC is derived
		// from their address, so it is only ever asserted for an address no directory
		// here covers.
		errors.Is(err, payment.ErrCounterpartyAgentNotNamed),
		// The address is well formed, its check digits verify, and this bank's copy
		// of the routing directory has no entry for the code inside it.
		errors.Is(err, payment.ErrBankCodeUnknown),
		// And the clearing house's version of the same shape, from the one route that
		// reads the roster to decide which bank an instruction belongs to: an address
		// under a code no member is published under.
		errors.Is(err, payment.ErrRosterEntryNotFound),
		// A malformed BIC is well-formed JSON naming a field that is not a
		// structurally valid ISO 9362 code — the same category as an
		// unaddressable account, not a decoding failure.
		errors.Is(err, iso20022.ErrBICFormat),
		// The same category one identifier along: a bank code that is not the width
		// its country allocates, a country nothing here issues in, or an address
		// whose check digits do not verify.
		errors.Is(err, iban.ErrUnknownCountry),
		errors.Is(err, iban.ErrLength),
		errors.Is(err, iban.ErrCharacter),
		errors.Is(err, iban.ErrCheckDigits),
		errors.Is(err, iban.ErrNationalCheck),
		errors.Is(err, iban.ErrBankCodeWidth),
		errors.Is(err, iban.ErrSerialTooLarge),
		// A caller supplying an address the BANK issues. Well-formed, and refused
		// because it is not the caller's to say — see deposit.ErrIBANIsIssued.
		errors.Is(err, deposit.ErrIBANIsIssued),
		// A bank asked to open an account before any registry allocated it a bank
		// code. A real state, and 422 rather than 409: nothing is contested, the
		// bank simply has no addresses to give out yet.
		errors.Is(err, deposit.ErrNoIssuer),
		// An on-us payment is well formed, names two real accounts and is a perfectly
		// legitimate thing to want; what refuses it is that this route does not carry
		// it.
		errors.Is(err, payment.ErrOnUsPayment),
		// An instruction posted to the bank that does not submit it, and the same
		// category once more: it is well formed, and what refuses it is that this
		// console is the wrong bank's. The other bank's console carries it.
		errors.Is(err, payment.ErrNotTheSubmittingAgent),
		// A payment to or from a bank the scheme has not admitted is the same
		// category again: the request is well formed, both accounts are real, and
		// what refuses it is that this route does not carry it — a founded bank has a
		// licence and a book and no place in a clearing scheme.
		errors.Is(err, payment.ErrBankNotAdmitted):
		return http.StatusUnprocessableEntity

	case errors.Is(err, ledger.ErrEmptyTransaction),
		errors.Is(err, ledger.ErrUnbalancedTransaction),
		errors.Is(err, ledger.ErrInvalidAmount),
		errors.Is(err, ledger.ErrInvalidText),
		// A leg that names a control account and no subsidiary, or a plain account
		// and one. 400 for ErrUnbalancedTransaction's reason: the request is wrong
		// about what it is posting to, and no account's state would let it through on
		// a retry.
		errors.Is(err, ledger.ErrSubsidiaryRequired),
		errors.Is(err, ledger.ErrSubsidiaryNotAllowed),
		// Assets are a fixed list in code, so an unknown code is a bad field
		// value like an unparseable account type — not a missing resource.
		errors.Is(err, ledger.ErrAssetNotFound),
		errors.Is(err, deposit.ErrInvalidAmount),
		errors.Is(err, deposit.ErrInvalidRate),
		// A transfer naming one account twice. 400 and not 422 because nothing
		// about either account's state refuses it: the request contradicts
		// itself, exactly as an unbalanced transaction does.
		errors.Is(err, deposit.ErrSameAccount),
		errors.Is(err, product.ErrInvalidRate),
		errors.Is(err, product.ErrNameRequired),
		errors.Is(err, payment.ErrInvalidPaymentAmount),
		errors.Is(err, lending.ErrInvalidAmount),
		errors.Is(err, lending.ErrInvalidRate),
		errors.Is(err, lending.ErrInvalidTerm):
		return http.StatusBadRequest

	// product.ErrHashMismatch is deliberately NOT mapped, so it falls through to
	// 500.
	default:
		return http.StatusInternalServerError
	}
}
