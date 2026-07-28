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
	// number of months.
	ErrInvalidTerm = errors.New("term must be a positive number of months")

	// ErrNothingOutstanding is returned when a repayment is made against a
	// facility that owes nothing.
	ErrNothingOutstanding = errors.New("facility has nothing outstanding")
)
