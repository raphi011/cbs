package api

import (
	"errors"
	"net/http"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/payment"
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
		errors.Is(err, payment.ErrParticipantNotFound),
		errors.Is(err, payment.ErrPaymentNotFound),
		errors.Is(err, payment.ErrMandateNotFound),
		errors.Is(err, payment.ErrCycleNotFound),
		errors.Is(err, payment.ErrSettlementNotFound),
		errors.Is(err, payment.ErrSchemeNotFound),
		errors.Is(err, payment.ErrAccountNotInParticipant),
		errors.Is(err, lending.ErrFacilityNotFound):
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
		errors.Is(err, lending.ErrCycleAlreadyBilled):
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
		// {pid}, and as "cycle not found" on POST /cycles/{id}/settle. 422
		// matches the sibling underfunded-member failure.
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
		errors.Is(err, lending.ErrWrongFacilityKind):
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
		errors.Is(err, payment.ErrInvalidPaymentAmount),
		errors.Is(err, lending.ErrInvalidAmount),
		errors.Is(err, lending.ErrInvalidRate),
		errors.Is(err, lending.ErrInvalidTerm):
		return http.StatusBadRequest

	default:
		return http.StatusInternalServerError
	}
}
