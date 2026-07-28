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
//
// Basis points alone would be too coarse. Retail rates are quoted in eighths of
// a percent, so 3.375% is 337.5 basis points and would not be an integer.
//
// A defined type distinct from Fraction, though both are millionths: a rate is
// per annum and a fraction is not, and the compiler should refuse to swap them.
type Rate int64

// Accrued is an amount of interest in micro-minor-units: ledger.Amount × 1e6.
//
// It exists because ledger.Amount is an integer of an asset's minor units and a
// day's interest on a small balance is mostly fraction — €50 at 15% accrues
// 2.054794 cents a day. Rounding that to 2 cents daily discards 0.054794 cents
// a day: 20.0 cents a year against 750 cents of annual interest, a 2.67% error.
// Accrued holds what the ledger structurally cannot; the ledger holds
// Accrued.Minor(), and the difference between them is the residue.
//
// int64 at this scale tops out near 9.2e12 minor units — €92 billion of accrued
// interest — which is not a bound any book here approaches.
type Accrued int64
