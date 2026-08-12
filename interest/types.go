package interest

// Scales. All three are 1e6, and that is load-bearing rather than tidy: the
// accrual below multiplies by a Rate and divides into an Accrued, so the two
// scales cancel and no intermediate rescaling is needed. See Accrue.
const (
	FractionScale = 1_000_000
	RateScale     = 1_000_000
	AccruedScale  = 1_000_000
)

// Fraction is a dimensionless proportion in millionths: 1_000_000 is 100%,
// 20_000 is 2%.
type Fraction int64

// Rate is an annual interest rate in millionths: 1_000_000 is 100%, 150_000 is
// 15%, 33_750 is 3.375%.
type Rate int64

// Accrued is an amount of interest in micro-minor-units: ledger.Amount × 1e6.
type Accrued int64
