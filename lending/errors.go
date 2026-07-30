package lending

import "errors"

// Sentinel errors returned by the Portfolio. Callers use errors.Is, the same
// convention ledger, deposit and payment follow.
var (
	// ErrFacilityNotFound is returned when a facility ID matches nothing.
	ErrFacilityNotFound = errors.New("facility not found")

	// ErrFacilityClosed is returned when an operation is attempted on a closed
	// facility.
	ErrFacilityClosed = errors.New("facility is closed")

	// ErrFacilityNotEmpty is returned when closing a facility that still has
	// drawn principal or accrued interest.
	ErrFacilityNotEmpty = errors.New("facility must be repaid in full to close")

	// ErrWrongFacilityKind is returned when an operation does not apply to this
	// product — disbursing a revolving line, or drawing on a term loan.
	ErrWrongFacilityKind = errors.New("operation does not apply to this facility kind")

	// ErrAlreadyDisbursed is returned when a term loan is disbursed twice. A
	// term loan's principal is fixed and paid out once; a second disbursement
	// would be a different loan.
	ErrAlreadyDisbursed = errors.New("term loan is already disbursed")

	// ErrLimitExceeded is returned when a draw would take drawn principal past
	// the facility's commitment.
	ErrLimitExceeded = errors.New("draw exceeds the facility commitment")

	// ErrInvalidAmount is returned when an amount is zero or negative.
	ErrInvalidAmount = errors.New("amount must be positive")

	// ErrInvalidRate is returned when a rate or fraction is negative.
	ErrInvalidRate = errors.New("invalid interest rate")

	// ErrInvalidTerm is returned when a term loan's term is not a positive
	// number of months, or is longer than MaxTermMonths.
	ErrInvalidTerm = errors.New("term must be a positive number of months, at most MaxTermMonths")

	// ErrNothingOutstanding is returned when a repayment is made against a
	// facility that owes nothing.
	ErrNothingOutstanding = errors.New("facility has nothing outstanding")

	// ErrNoRefundOutstanding is returned when an interest refund is paid against
	// a facility the bank owes nothing on. It is ErrNothingOutstanding with the
	// money running the other way, and it is a separate sentinel because the two
	// mean opposite things to a caller: one says the borrower has already
	// settled, the other says the bank has.
	ErrNoRefundOutstanding = errors.New("facility has no interest refund outstanding")

	// ErrCycleAlreadyBilled is returned when a revolving line's billing cycle
	// is charged twice. Billing appends an instalment and capitalizes the
	// receivable, so a retried request — a proxy retry, a double-submitted
	// form — would leave the borrower owing two minimum payments for one
	// cycle and drifting into arrears from an infrastructure event.
	ErrCycleAlreadyBilled = errors.New("this billing cycle has already been charged")

	// ErrTermsNotFound is returned when no facility terms are in force on a
	// day. Every facility gets an opening terms row at origination, so the only
	// way to miss is to ask about a day before the facility existed.
	ErrTermsNotFound = errors.New("no facility terms in force on that day")

	// ErrScheduleWouldDiverge is returned when repricing a term loan would put
	// its instalment schedule out of step with its accrual. Two cases: the loan
	// already HAS a generated schedule, or the row would be effective AFTER the
	// day it is entered, which is the day a schedule generated later would be
	// pinned at.
	//
	// A term loan's instalments are generated once, at disbursement, from the
	// rate in force then, and stored as rows. If accrual followed a timeline
	// and the schedule did not, the plan and the actual accrual would drift
	// apart — beyond the ordinary plan-versus-actual divergence this package
	// already teaches, which 30/360 exists to keep small — and the final
	// instalment would silently absorb the difference, unnoticed until
	// maturity. Regenerating a schedule against repayments already posted
	// against it needs versioned schedule rows and open-item allocation, which
	// is its own topic. Repricing is allowed freely on a revolving line (no
	// schedule, and so nothing for a future-dated row to get ahead of) and on an
	// undisbursed term loan effective on or before today (no schedule yet, and
	// the one generated later will be pinned at this very row).
	ErrScheduleWouldDiverge = errors.New("repricing a term loan with a generated schedule would diverge from it")
)
