package api

import (
	"errors"
	"net/http"

	"github.com/raphi011/ledger"
	"github.com/raphi011/ledger/deposit"
	"github.com/raphi011/ledger/payment"
)

// errorStatus maps a domain sentinel error to an HTTP status code. Unknown
// errors fall through to 500. The categories are:
//
//   - 404 Not Found: an entity referenced by ID does not exist.
//   - 409 Conflict: a duplicate or already-applied operation.
//   - 422 Unprocessable Entity: a well-formed request that violates business
//     state (insufficient funds, frozen account, invalid state transition).
//   - 400 Bad Request: malformed input (unbalanced/empty transaction, non-
//     positive amounts). JSON-decode and enum-parse failures are reported as
//     400 directly by the handlers before the core is ever called.
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
		errors.Is(err, payment.ErrAccountNotInParticipant):
		return http.StatusNotFound

	case errors.Is(err, ledger.ErrDuplicateIdempotencyKey),
		errors.Is(err, ledger.ErrTransactionAlreadyReversed),
		errors.Is(err, payment.ErrDuplicateEndToEndID),
		errors.Is(err, payment.ErrCycleAlreadyOpen):
		return http.StatusConflict

	case errors.Is(err, ledger.ErrInsufficientBalance),
		errors.Is(err, deposit.ErrInsufficientAvailable),
		errors.Is(err, deposit.ErrAccountFrozen),
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
		errors.Is(err, payment.ErrSchemeUnsupportedReturn):
		return http.StatusUnprocessableEntity

	case errors.Is(err, ledger.ErrEmptyTransaction),
		errors.Is(err, ledger.ErrUnbalancedTransaction),
		errors.Is(err, ledger.ErrInvalidAmount),
		errors.Is(err, deposit.ErrInvalidAmount),
		errors.Is(err, payment.ErrInvalidPaymentAmount):
		return http.StatusBadRequest

	default:
		return http.StatusInternalServerError
	}
}
