package lending

import (
	"time"

	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// FacilityID is a defined type, not an alias, so the compiler prevents passing
// a ledger.AccountID where a FacilityID is expected.
type FacilityID string

// FacilityKind is the product. The two here are the credit products whose drawn
// amount exists independently of any other account; an arranged overdraft's
// does not, which is why it is not one of them. See the package doc.
type FacilityKind int

const (
	// TermLoan is a fixed principal disbursed once and amortized on a schedule
	// generated at disbursement.
	TermLoan FacilityKind = iota
	// RevolvingLine is a commitment that may be drawn and repaid repeatedly,
	// billed each cycle with a minimum payment.
	RevolvingLine
)

func (k FacilityKind) String() string {
	switch k {
	case TermLoan:
		return "TermLoan"
	case RevolvingLine:
		return "RevolvingLine"
	default:
		return "Unknown"
	}
}

// AmortMethod is how a term loan's schedule spreads principal across its
// instalments. Both are shipped because having only one makes the schedule look
// like a law of lending rather than a term of the contract.
type AmortMethod int

const (
	// Annuity gives every instalment the same total, so the interest portion
	// falls and the principal portion rises over the life of the loan. The
	// retail mortgage and personal-loan shape.
	Annuity AmortMethod = iota
	// EqualPrincipal repays the same principal each instalment, so the total
	// falls as interest does. Common in commercial lending.
	EqualPrincipal
)

func (m AmortMethod) String() string {
	switch m {
	case Annuity:
		return "Annuity"
	case EqualPrincipal:
		return "EqualPrincipal"
	default:
		return "Unknown"
	}
}

// FacilityStatus is the lifecycle of a facility, and is deliberately separate
// from Arrears: a delinquent loan is Active, not some third state.
type FacilityStatus int

const (
	// Pending is opened but with nothing drawn.
	Pending FacilityStatus = iota
	// Active has an outstanding balance.
	Active
	// Closed is terminal: principal and accrued interest are both zero and no
	// further drawing is permitted.
	Closed
)

func (s FacilityStatus) String() string {
	switch s {
	case Pending:
		return "Pending"
	case Active:
		return "Active"
	case Closed:
		return "Closed"
	default:
		return "Unknown"
	}
}

// Bucket is a delinquency band in days past due. These four boundaries are what
// essentially every regulatory and management report is keyed on, which is why
// a plain "late/not late" flag would not do.
type Bucket int

const (
	Current Bucket = iota
	D1_29
	D30_59
	D60_89
	D90Plus
)

func (b Bucket) String() string {
	switch b {
	case Current:
		return "Current"
	case D1_29:
		return "1-29"
	case D30_59:
		return "30-59"
	case D60_89:
		return "60-89"
	case D90Plus:
		return "90+"
	default:
		return "Unknown"
	}
}

// BucketFor maps days past due onto its band.
func BucketFor(days int) Bucket {
	switch {
	case days <= 0:
		return Current
	case days < 30:
		return D1_29
	case days < 60:
		return D30_59
	case days < 90:
		return D60_89
	default:
		return D90Plus
	}
}

// Arrears is how far behind a facility is, recomputed from its schedule at end
// of day rather than stored as events.
type Arrears struct {
	// DaysPastDue is measured from the due date of the oldest instalment that
	// still has an unpaid amount.
	DaysPastDue int
	Bucket      Bucket
	// NonPerforming marks a facility at 90+ days past due. It MARKS ONLY: it
	// changes no accounting.
	NonPerforming bool
	// OldestUnpaidDue is the due date DaysPastDue is measured from, zero when
	// the facility is current.
	OldestUnpaidDue time.Time
}

// Facility is a credit facility: a term loan or a revolving line.
type Facility struct {
	ID   FacilityID
	Kind FacilityKind
	Name string
	// Asset is fixed at opening, like every account's, and it is what decides
	// which three lines this facility posts to. All three are denominated in it,
	// so a posting that mixed assets could not balance.
	Asset ledger.AssetCode

	// Commitment is what the bank has committed: a term loan's original
	// principal, a revolving line's limit. One field rather than two because it
	// plays the same role in both — the amount beyond which drawing is refused.
	Commitment ledger.Amount

	// Method is the amortization method, term loans only.
	Method AmortMethod
	// TermMonths is the number of instalments, term loans only.
	TermMonths int
	// MinPayment is the share of drawn principal added to each cycle's minimum
	// payment on a revolving line, on top of the interest charged.
	MinPayment interest.Fraction

	// Accrued is interest earned and not yet settled, at sub-minor-unit
	// precision. The receivable holds Accrued.Minor() under this facility's id;
	// this holds the residue the ledger cannot represent.
	Accrued interest.Accrued
	// AccruedGross is the interest this facility has produced over its WHOLE LIFE,
	// recomputed from its value-dated drawn balance on every run.
	AccruedGross interest.Accrued
	// LastAccrualDate is the business date accrual has been recomputed through.
	LastAccrualDate time.Time

	Arrears Arrears
	Status  FacilityStatus

	OpenedAt time.Time
	// MaturityAt is zero for an open-ended revolving line.
	MaturityAt time.Time
}

// Installment is one scheduled payment.
type Installment struct {
	FacilityID FacilityID
	// Seq is 1-based and is the instalment's identity within its facility,
	// together with FacilityID.
	Seq     int
	DueDate time.Time

	Principal ledger.Amount
	Interest  ledger.Amount

	PaidPrincipal ledger.Amount
	PaidInterest  ledger.Amount
}

// Outstanding is what is still owed on an instalment under the plan.
func (i Installment) Outstanding() ledger.Amount {
	return (i.Principal - i.PaidPrincipal) + (i.Interest - i.PaidInterest)
}

// Total is the instalment's scheduled payment.
func (i Installment) Total() ledger.Amount { return i.Principal + i.Interest }

// FacilityWithTerms is a facility alongside the terms in force on a day —
// today, for every caller here.
type FacilityWithTerms struct {
	Facility Facility
	Terms    FacilityTerms
}
