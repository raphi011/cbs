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
// from Arrears: a delinquent loan is Active, not some third state. Conflating
// them would make "is this facility still lending money?" and "is it being
// repaid on time?" one question with one answer.
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
//
// An arranged overdraft has no equivalent, deliberately: arrears count from a
// missed due date and an overdraft has none. It is repayable on demand, and the
// sanction for exceeding the limit is the unarranged rate. See the package doc.
type Arrears struct {
	// DaysPastDue is measured from the due date of the oldest instalment that
	// still has an unpaid amount.
	DaysPastDue int
	Bucket      Bucket
	// NonPerforming marks a facility at 90+ days past due. It MARKS ONLY: it
	// changes no accounting. Non-accrual — where a non-performing loan stops
	// recognizing interest into income — and expected-credit-loss provisioning
	// are recorded as future work in docs/expansion-roadmap.md, because each is
	// a domain of its own and neither is needed to teach delinquency.
	NonPerforming bool
	// OldestUnpaidDue is the due date DaysPastDue is measured from, zero when
	// the facility is current.
	OldestUnpaidDue time.Time
}

// Facility is a credit facility: a term loan or a revolving line.
//
// It is the mirror of deposit.Account. Where a deposit account wraps a backing
// Liability GL account, a facility wraps two Asset ones — drawn principal and
// accrued interest receivable — and the facility itself stores no money.
//
// The two accounts are separate because repayment allocates to interest before
// principal, and one account could not express the split.
type Facility struct {
	ID   FacilityID
	Kind FacilityKind
	Name string
	// Asset is fixed at opening, like every account's. Both GL accounts are
	// denominated in it, so a posting that mixed assets could not balance.
	Asset ledger.AssetCode
	// PrincipalGL is the drawn principal, an Asset account.
	PrincipalGL ledger.AccountID
	// InterestGL is accrued interest receivable, an Asset account.
	InterestGL ledger.AccountID
	// RefundGL is interest the bank owes this borrower back, a Liability
	// account. Unlike the two above it is created LAZILY — on the first
	// backdated correction that cuts accrued interest below what the borrower
	// has already settled in cash, and never on the ordinary path — so it is
	// empty on almost every facility. Zero means no correction has ever
	// overshot, which is indistinguishable from a zero balance and is why
	// RefundPayable reports 0 for it rather than reading a GL account.
	//
	// It is per facility rather than one pooled account per asset because the
	// balance is the answer to "what does the bank owe THIS borrower": a pooled
	// account has one balance and so cannot say who is owed what, which leaves
	// a discharge unbounded — able to pay one borrower out of another's money
	// and still balance. The Payables subledger's total is the pooled figure.
	//
	// Stored as an ID for the same reason PrincipalGL is, and not derived by
	// name: Name is a mutable column on this row, so a rename would otherwise
	// orphan the obligation.
	RefundGL ledger.AccountID

	// Commitment is what the bank has committed: a term loan's original
	// principal, a revolving line's limit. One field rather than two because it
	// plays the same role in both — the amount beyond which drawing is refused.
	Commitment ledger.Amount

	Rate     interest.Rate
	DayCount interest.DayCount

	// Method is the amortization method, term loans only.
	Method AmortMethod
	// TermMonths is the number of instalments, term loans only.
	TermMonths int
	// MinPayment is the share of drawn principal added to each cycle's minimum
	// payment on a revolving line, on top of the interest charged. A Fraction
	// rather than a Rate: it is not per annum, and the compiler should refuse
	// to swap them.
	MinPayment interest.Fraction

	// Accrued is interest earned and not yet settled, at sub-minor-unit
	// precision. InterestGL holds Accrued.Minor(); this holds the residue the
	// ledger cannot represent.
	Accrued interest.Accrued
	// AccruedGross is the interest the current terms window has produced in
	// total, recomputed from the facility's value-dated drawn balance on every
	// run. Accrued moves by the CHANGE in it, which is what lets a backdated
	// posting correct the interest charged on the days it takes effect over:
	// those days are re-derived with it in place, this figure moves, and the
	// next run posts the difference.
	//
	// Unlike Accrued it is never decremented by a repayment or a
	// capitalization — those settle the receivable, not the window — and it
	// resets when the window does, which today is the first advance.
	AccruedGross interest.Accrued
	// TermsEffectiveFrom is the start of the recompute window: where the
	// current terms took effect, which for a facility is its first advance.
	// Money not yet paid out earns nothing, so there is nothing earlier to
	// re-derive.
	//
	// It is ALSO bounded at the last repricing, which is the constraint the
	// deposit side has already shed: Rate and DayCount are still mutable
	// columns on this very row, so a window reaching back past a change to them
	// would re-derive past days at a rate that was not in force on them.
	// deposit.Account carried the same field for the same reason and no longer
	// does — its terms became the effective-dated deposit.OverdraftTerms
	// timeline, resolved per accrual day, and its recompute window now opens at
	// account inception. lending.FacilityTerms is the same move for this side,
	// not yet consumed here. Nothing in this package reprices a facility yet;
	// whatever does must move this field, and reset AccruedGross with it.
	//
	// Zero means nothing has been advanced and the facility accrues nothing.
	TermsEffectiveFrom time.Time
	// LastAccrualDate never moves backwards, which is what makes re-running an
	// end-of-day a no-op rather than a second day's interest — and why a
	// backdated posting is corrected by the next day's run rather than by
	// rewinding this one.
	LastAccrualDate time.Time

	Arrears Arrears
	Status  FacilityStatus

	OpenedAt time.Time
	// MaturityAt is zero for an open-ended revolving line.
	MaturityAt time.Time
}

// Installment is one scheduled payment.
//
// It serves both kinds. A term loan's rows are all generated at disbursement,
// from terms that are known in full at that moment. A revolving line has no
// schedule to generate up front, so it appends one row per billing cycle when
// interest is charged: the minimum payment due. That is how a revolving
// facility actually falls into arrears — by missing a minimum payment, not an
// amortization instalment — and it lets one arrears implementation serve both.
//
// Principal and Interest are the PLAN. What a repayment actually allocates to
// interest is the accrual, which under ACT/365 differs from the scheduled
// twelfth; see Portfolio.Repay.
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
