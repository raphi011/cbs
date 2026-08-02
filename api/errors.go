package api

import (
	"errors"
	"net/http"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/product"
)

// errorStatus maps a domain sentinel error to an HTTP status code. Unknown
// errors fall through to 500. The categories are:
//
//   - 404 Not Found: an entity referenced by ID does not exist.
//   - 409 Conflict: a duplicate or already-applied operation.
//   - 422 Unprocessable Entity: a well-formed request that violates business
//     state (insufficient funds, frozen account, invalid state transition).
//   - 400 Bad Request: malformed input (unbalanced/empty transaction, non-
//     positive amounts, text carrying a control character). JSON-decode and
//     enum-parse failures are reported as 400 directly by the handlers before
//     the core is ever called, as is a control character in the request target
//     (see screenRequestTarget).
func errorStatus(err error) int {
	switch {
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
		// A billing cycle already on the schedule is the already-applied
		// category exactly: the request is valid and the state already
		// reflects it. 409 is what tells a retrying proxy that its first
		// attempt landed, where 422 would read as "this line cannot be
		// billed".
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
		errors.Is(err, payment.ErrInvalidStateTransition),
		errors.Is(err, payment.ErrMandateRevoked),
		errors.Is(err, payment.ErrMandateMismatch),
		errors.Is(err, payment.ErrMandateExceeded),
		errors.Is(err, payment.ErrMandateRequired),
		errors.Is(err, payment.ErrSchemeUnsupportedReturn),
		errors.Is(err, payment.ErrAssetMismatch),
		// The bank exists and the asset exists; this bank simply holds no
		// accounts in it. 404 would read as "participant not found" on
		// POST /participants/{pid}/deposits and GET /central-bank/reserves/
		// {pid}. 422 matches the sibling underfunded-member failure. It no
		// longer reaches a settlement route, because there is none: settling is
		// performed on instruction now (mesh.centralBank), so this error comes
		// back to the clearing house as a pacs.002 rather than to a caller as a
		// status code.
		errors.Is(err, payment.ErrParticipantAssetNotFound),
		errors.Is(err, lending.ErrFacilityClosed),
		errors.Is(err, lending.ErrFacilityNotEmpty),
		errors.Is(err, lending.ErrLimitExceeded),
		errors.Is(err, lending.ErrAlreadyDisbursed),
		errors.Is(err, lending.ErrNothingOutstanding),
		// Its mirror: the bank owes this facility nothing to refund. Same
		// category and so the same status — a well-formed request against state
		// that has nothing for it to do.
		errors.Is(err, lending.ErrNoRefundOutstanding),
		// ErrWrongFacilityKind is 422 rather than 400 deliberately: the request
		// is well formed and the field values are valid, but this facility is
		// the wrong product for the operation — the same category as
		// ErrCycleNotOpen.
		errors.Is(err, lending.ErrWrongFacilityKind),
		// The catalogue's four refusals are the same category: each request is
		// well formed and its field values are valid, and the state is what
		// refuses it. A retired product is on the books but off sale, a
		// published version is frozen by design, a backdated publication is
		// refused outright because its blast radius is every account on the
		// product, and a terms row with no product cannot resolve a price.
		errors.Is(err, product.ErrProductRetired),
		errors.Is(err, product.ErrKindMismatch),
		errors.Is(err, product.ErrVersionPublished),
		errors.Is(err, product.ErrRetroactivePublish),
		errors.Is(err, deposit.ErrProductRequired),
		// The account exists and the request is well formed; it simply carries no
		// address in the kind the scheme routes on, or the address it quoted
		// belongs to a different account, or it quoted none and the account has
		// several the scheme could route on so initiation will not pick one. All
		// three are business-state refusals, the same category as a frozen
		// account or an unbalanced state transition — and the third stays 422
		// rather than joining ErrIdentifierAmbiguous at 409 because nothing in
		// the data is contested: the caller can fix it by naming an address.
		errors.Is(err, payment.ErrUnaddressableAccount),
		errors.Is(err, payment.ErrIdentifierMismatch),
		errors.Is(err, payment.ErrAmbiguousAddress),
		// The request is well-formed JSON that named no counterparty (or an
		// invalid one) — the same category as an unaddressable account: this
		// bank cannot look the other side up, so the instruction must carry it.
		errors.Is(err, payment.ErrCounterpartyNotNamed),
		// A malformed BIC is well-formed JSON naming a field that is not a
		// structurally valid ISO 9362 code — the same category as an
		// unaddressable account, not a decoding failure.
		errors.Is(err, iso20022.ErrBICFormat):
		return http.StatusUnprocessableEntity

	case errors.Is(err, ledger.ErrEmptyTransaction),
		errors.Is(err, ledger.ErrUnbalancedTransaction),
		errors.Is(err, ledger.ErrInvalidAmount),
		errors.Is(err, ledger.ErrInvalidText),
		// Assets are a fixed list in code, so an unknown code is a bad field
		// value like an unparseable account type — not a missing resource.
		errors.Is(err, ledger.ErrAssetNotFound),
		errors.Is(err, deposit.ErrInvalidAmount),
		errors.Is(err, deposit.ErrInvalidRate),
		errors.Is(err, product.ErrInvalidRate),
		errors.Is(err, product.ErrNameRequired),
		errors.Is(err, payment.ErrInvalidPaymentAmount),
		errors.Is(err, lending.ErrInvalidAmount),
		errors.Is(err, lending.ErrInvalidRate),
		errors.Is(err, lending.ErrInvalidTerm):
		return http.StatusBadRequest

	// product.ErrHashMismatch is deliberately NOT mapped, so it falls through to
	// 500. A published version whose content no longer matches its hash means
	// stored data was edited behind the system's back: the caller did nothing
	// wrong, and a 4xx would tell them to fix their request. It is the one
	// catalogue error that is the server's problem.
	default:
		return http.StatusInternalServerError
	}
}
