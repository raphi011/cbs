package api

import (
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// Wire format for the demand-deposit layer: customer accounts, holds,
// balances, snapshots, and their requests.

// depositAccountDTO carries the arranged overdraft's credit terms alongside
// the account. Rate crosses the wire as its millionths integer with RateScale
// beside it, the same convention facilityDTO follows for the same reason: an
// integer whose scale a client learns from documentation is an integer a
// client renders wrong.
//
// OverdraftLimit, OverdraftRate, UnarrangedRate and DayCount are RESOLVED AS OF
// TODAY from the account's effective-dated terms timeline rather than read off
// the account row — the account has no such columns any more. So this is what
// the product costs today and nothing else: a future-dated repricing is not
// visible here until it takes effect, and a past one is not visible here at
// all. GET /participants/{pid}/deposit-accounts/{did}/overdraft-terms is what
// shows the whole timeline.
type depositAccountDTO struct {
	ID        string `json:"id"`
	GLAccount string `json:"glAccount"`
	Name      string `json:"name"`
	Asset     string `json:"asset"`
	Status    string `json:"status"`
	// Identifiers are the account's external addresses. Empty is normal.
	Identifiers []identifierDTO `json:"identifiers"`
	// ProductID is the catalogue entry pricing this account today. It varies
	// over the account's life, so it comes from the resolved terms rather than
	// from the account row.
	ProductID      string `json:"productId"`
	OverdraftLimit int64  `json:"overdraftLimit"`

	OverdraftRate  int64  `json:"overdraftRate"`
	UnarrangedRate int64  `json:"unarrangedRate"`
	RateScale      int64  `json:"rateScale"`
	DayCount       string `json:"dayCount"`
	// PricingSource is "product" when the rate above is the product's list
	// price and "negotiated" when this customer has an overlay.
	//
	// A client that cannot tell the two apart cannot show a customer why their
	// rate did not move when the product was repriced — which is the single
	// question a negotiated rate generates.
	PricingSource   string `json:"pricingSource"`
	AccruedInterest int64  `json:"accruedInterest"`
	// InterestGLAccount is empty until the account first accrues — the
	// receivable is created by the accrual, not by a register call, because a
	// floating account's rate lives in the catalogue.
	InterestGLAccount string `json:"interestGlAccount,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
}

// pricingSourceProduct and pricingSourceNegotiated are the two values
// depositAccountDTO.PricingSource takes.
const (
	pricingSourceProduct    = "product"
	pricingSourceNegotiated = "negotiated"
)

func toDepositAccountDTO(a deposit.Account, t deposit.EffectiveTerms) depositAccountDTO {
	source := pricingSourceProduct
	if t.Negotiated {
		source = pricingSourceNegotiated
	}
	return depositAccountDTO{
		ID:             string(a.ID),
		GLAccount:      string(a.GLAccount),
		Name:           a.Name,
		Asset:          string(a.Asset),
		Status:         a.Status.String(),
		Identifiers:    toIdentifierDTOs(a.Identifiers),
		ProductID:      string(t.ProductID),
		OverdraftLimit: int64(t.Limit),

		OverdraftRate:     int64(t.Pricing.Rate),
		UnarrangedRate:    int64(t.Pricing.UnarrangedRate),
		RateScale:         interest.RateScale,
		DayCount:          t.Pricing.DayCount.String(),
		PricingSource:     source,
		AccruedInterest:   int64(a.Accrued.Minor()),
		InterestGLAccount: string(a.InterestGL),

		CreatedAt: a.CreatedAt,
	}
}

type holdDTO struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"accountId"`
	Amount      int64     `json:"amount"`
	ExpiresAt   time.Time `json:"expiresAt,omitempty"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
}

func toHoldDTO(h deposit.Hold) holdDTO {
	return holdDTO{
		ID:          string(h.ID),
		AccountID:   string(h.AccountID),
		Amount:      int64(h.Amount),
		ExpiresAt:   h.ExpiresAt,
		Description: h.Description,
		Status:      h.Status.String(),
		CreatedAt:   h.CreatedAt,
	}
}

// balanceDTO carries the asset alongside the three numbers.
//
// Every one of them is an integer in the account's minor units, and a client
// cannot render any of them without the asset's scale. Without this field a
// balance costs three requests to display — the balance, then the account for
// its code, then the asset list for that code's scale — and the number is on
// screen before the scale that gives it meaning, which is what an amount
// rendered at a guessed scale looks like just before it is wrong.
type balanceDTO struct {
	Asset     string `json:"asset"`
	Book      int64  `json:"book"`
	Holds     int64  `json:"holds"`
	Available int64  `json:"available"`
}

func toBalanceDTO(b deposit.Balance, asset ledger.AssetCode) balanceDTO {
	return balanceDTO{
		Asset:     string(asset),
		Book:      int64(b.Book),
		Holds:     int64(b.Holds),
		Available: int64(b.Available),
	}
}

// overdraftTermsDTO is one row of an account's effective-dated terms timeline.
//
// effectiveFrom is the day the row takes economic effect and createdAt is when
// it was entered: the booking-date/value-date distinction applied to
// configuration, and the reason a repricing agreed on the 1st and entered on
// the 15th can be recorded honestly. A row whose effectiveFrom is in the future
// appears here before it applies; the account's own resolved fields show the
// row in force today.
type overdraftTermsDTO struct {
	AccountID      string    `json:"accountId"`
	EffectiveFrom  time.Time `json:"effectiveFrom"`
	ProductID      string    `json:"productId"`
	OverdraftLimit int64     `json:"overdraftLimit"`
	// Floating says this row carries no negotiated price, so its rate comes
	// from the product version in force on each day rather than from the row.
	// The three rate fields below are then ZERO because the row holds nothing,
	// which is emphatically not "interest-free" — a client that renders them as
	// a price would show every floating account as free.
	Floating       bool      `json:"floating"`
	Rate           int64     `json:"rate"`
	UnarrangedRate int64     `json:"unarrangedRate"`
	RateScale      int64     `json:"rateScale"`
	DayCount       string    `json:"dayCount"`
	CreatedAt      time.Time `json:"createdAt"`
}

func toOverdraftTermsDTO(t deposit.OverdraftTerms) overdraftTermsDTO {
	out := overdraftTermsDTO{
		AccountID:      string(t.AccountID),
		EffectiveFrom:  t.EffectiveFrom,
		ProductID:      string(t.ProductID),
		OverdraftLimit: int64(t.OverdraftLimit),
		Floating:       t.Pricing == nil,
		RateScale:      interest.RateScale,
		CreatedAt:      t.CreatedAt,
	}
	if t.Pricing != nil {
		out.Rate = int64(t.Pricing.Rate)
		out.UnarrangedRate = int64(t.Pricing.UnarrangedRate)
		out.DayCount = t.Pricing.DayCount.String()
	}
	return out
}

type snapshotDTO struct {
	AccountID string     `json:"accountId"`
	Date      time.Time  `json:"date"`
	Balance   balanceDTO `json:"balance"`
	TakenAt   time.Time  `json:"takenAt"`
}

func toSnapshotDTO(s deposit.Snapshot, asset ledger.AssetCode) snapshotDTO {
	return snapshotDTO{AccountID: string(s.AccountID), Date: s.Date, Balance: toBalanceDTO(s.Balance, asset), TakenAt: s.TakenAt}
}

// openDepositAccountRequest carries a required asset, for the same reason
// createAccountRequest does: the deposit account's asset is its backing GL
// account's, and neither may be guessed on the caller's behalf.
// productId is required for the same reason: every deposit account is opened
// FROM a product, because a floating terms row with no product would have
// nothing to float to.
type openDepositAccountRequest struct {
	Name           string `json:"name"`
	Asset          string `json:"asset"`
	ProductID      string `json:"productId"`
	OverdraftLimit int64  `json:"overdraftLimit"`
	// Identifiers are the external addresses to open the account with — an
	// IBAN, say. Optional: most accounts open with none and gain one later
	// through POST .../identifiers, and a scheme that requires one to be
	// addressed will refuse a payment rather than let this go unnoticed.
	Identifiers []identifierDTO `json:"identifiers"`
}

type statusRequest struct {
	Action string `json:"action"`
}

type createHoldRequest struct {
	Amount      int64      `json:"amount"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	Description string     `json:"description"`
}

type captureHoldRequest struct {
	Counterparty string `json:"counterparty"`
	Amount       int64  `json:"amount"`
	Description  string `json:"description"`
}

type snapshotRequest struct {
	Date string `json:"date"`
}

type fundRequest struct {
	Account     string `json:"account"`
	Amount      int64  `json:"amount"`
	Description string `json:"description"`
}

// The three requests that replaced setOverdraftTermsRequest, one per decision
// the old single call conflated.
//
// effectiveFrom is common to all three: the day the change takes economic
// effect, RFC3339, and it may be in the past or the future. A repricing agreed
// on the 1st and entered on the 15th is the ordinary case, and a row effective
// next month is inert until the end-of-day runs reach it. Absent or null means
// today on the register's clock rather than on this process's wall clock.

// setOverdraftLimitRequest changes what this customer may go overdrawn by. The
// limit is never part of a product: it is an underwriting decision about one
// customer, which is why product.OverdraftPricing cannot express one.
type setOverdraftLimitRequest struct {
	Limit         int64      `json:"limit"`
	EffectiveFrom *time.Time `json:"effectiveFrom"`
}

// setOverdraftPricingRequest gives this customer a negotiated price instead of
// the product's, or clears one.
//
// A NULL pricing clears the overlay and puts the account back on its product —
// at whatever the product costs by then, not at what it cost when the overlay
// was set. That is emphatically not "interest-free": an interest-free account
// is a pricing with a zero rate, which is a different and deliberate statement.
//
// A backdated overlay moves interest that has already been charged to ONE named
// customer, and the audit log is the only control on it. The catalogue refuses
// the same thing outright, because its blast radius is every account on the
// product.
type setOverdraftPricingRequest struct {
	Pricing       *overdraftPricingDTO `json:"pricing"`
	EffectiveFrom *time.Time           `json:"effectiveFrom"`
}

// overdraftPricingDTO is the floating parameter group on the wire. Rate and
// UnarrangedRate are millionths, the same convention facilityDTO uses.
type overdraftPricingDTO struct {
	Rate           int64  `json:"rate"`
	UnarrangedRate int64  `json:"unarrangedRate"`
	DayCount       string `json:"dayCount"`
}

// changeProductRequest migrates an account onto another product. The days
// before effectiveFrom still resolve against the product that priced them: a
// migration is not a rewrite.
type changeProductRequest struct {
	ProductID     string     `json:"productId"`
	EffectiveFrom *time.Time `json:"effectiveFrom"`
}

type chargeOverdraftInterestRequest struct {
	Date string `json:"date"`
}
