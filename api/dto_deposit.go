package api

import (
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/interest"
	"github.com/raphi011/cbs/ledger"
)

// Wire format for the demand-deposit layer: customer accounts, holds,
// balances, snapshots, and their requests.

// DepositAccountDTO carries the arranged overdraft's credit terms alongside the
// account.
type DepositAccountDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// ControlAccount is the chart-of-accounts line this account's money is pooled
	// in. There is no second field for the subsidiary within it: that is this
	// account's own ID, above.
	ControlAccount string `json:"controlAccount"`
	Asset          string `json:"asset"`
	Status         string `json:"status"`
	// Identifiers are the account's external addresses. Empty is normal.
	Identifiers []IdentifierDTO `json:"identifiers"`
	// ProductID is the catalogue entry pricing this account today. It varies
	// over the account's life, so it comes from the resolved terms rather than
	// from the account row.
	ProductID      string `json:"productId"`
	OverdraftLimit int64  `json:"overdraftLimit"`

	OverdraftRate  int64  `json:"overdraftRate"`
	UnarrangedRate int64  `json:"unarrangedRate"`
	RateScale      int64  `json:"rateScale"`
	DayCount       string `json:"dayCount"`
	// PricingSource is "product" when the rate above is the product's list price
	// and "negotiated" when this customer has an overlay.
	PricingSource   string `json:"pricingSource"`
	AccruedInterest int64  `json:"accruedInterest"`

	CreatedAt time.Time `json:"createdAt"`
}

// pricingSourceProduct and pricingSourceNegotiated are the two values
// DepositAccountDTO.PricingSource takes.
const (
	pricingSourceProduct    = "product"
	pricingSourceNegotiated = "negotiated"
)

func ToDepositAccountDTO(a deposit.Account, t deposit.EffectiveTerms, control ledger.AccountID) DepositAccountDTO {
	source := pricingSourceProduct
	if t.Negotiated {
		source = pricingSourceNegotiated
	}
	return DepositAccountDTO{
		ID:             string(a.ID),
		Name:           a.Name,
		ControlAccount: string(control),
		Asset:          string(a.Asset),
		Status:         a.Status.String(),
		Identifiers:    ToIdentifierDTOs(a.Identifiers),
		ProductID:      string(t.ProductID),
		OverdraftLimit: int64(t.Limit),

		OverdraftRate:   int64(t.Pricing.Rate),
		UnarrangedRate:  int64(t.Pricing.UnarrangedRate),
		RateScale:       interest.RateScale,
		DayCount:        t.Pricing.DayCount.String(),
		PricingSource:   source,
		AccruedInterest: int64(a.Accrued.Minor()),

		CreatedAt: a.CreatedAt,
	}
}

type HoldDTO struct {
	ID          string    `json:"id"`
	AccountID   string    `json:"accountId"`
	Amount      int64     `json:"amount"`
	ExpiresAt   time.Time `json:"expiresAt,omitempty"`
	Description string    `json:"description,omitempty"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
}

func ToHoldDTO(h deposit.Hold) HoldDTO {
	return HoldDTO{
		ID:          string(h.ID),
		AccountID:   string(h.AccountID),
		Amount:      int64(h.Amount),
		ExpiresAt:   h.ExpiresAt,
		Description: h.Description,
		Status:      h.Status.String(),
		CreatedAt:   h.CreatedAt,
	}
}

// BalanceDTO carries the asset alongside the three numbers.
type BalanceDTO struct {
	Asset     string `json:"asset"`
	Book      int64  `json:"book"`
	Holds     int64  `json:"holds"`
	Available int64  `json:"available"`
}

func ToBalanceDTO(b deposit.Balance, asset ledger.AssetCode) BalanceDTO {
	return BalanceDTO{
		Asset:     string(asset),
		Book:      int64(b.Book),
		Holds:     int64(b.Holds),
		Available: int64(b.Available),
	}
}

// OverdraftTermsDTO is one row of an account's effective-dated terms timeline.
type OverdraftTermsDTO struct {
	AccountID      string    `json:"accountId"`
	EffectiveFrom  time.Time `json:"effectiveFrom"`
	ProductID      string    `json:"productId"`
	OverdraftLimit int64     `json:"overdraftLimit"`
	// Floating says this row carries no negotiated price, so its rate comes from
	// the product version in force on each day rather than from the row.
	Floating       bool      `json:"floating"`
	Rate           int64     `json:"rate"`
	UnarrangedRate int64     `json:"unarrangedRate"`
	RateScale      int64     `json:"rateScale"`
	DayCount       string    `json:"dayCount"`
	CreatedAt      time.Time `json:"createdAt"`
}

func ToOverdraftTermsDTO(t deposit.OverdraftTerms) OverdraftTermsDTO {
	out := OverdraftTermsDTO{
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

type SnapshotDTO struct {
	AccountID string     `json:"accountId"`
	Date      time.Time  `json:"date"`
	Balance   BalanceDTO `json:"balance"`
	TakenAt   time.Time  `json:"takenAt"`
}

func ToSnapshotDTO(s deposit.Snapshot, asset ledger.AssetCode) SnapshotDTO {
	return SnapshotDTO{AccountID: string(s.AccountID), Date: s.Date, Balance: ToBalanceDTO(s.Balance, asset), TakenAt: s.TakenAt}
}

// OpenDepositAccountRequest carries a required asset, for the same reason
// CreateAccountRequest does: the asset is what decides which control account
// the money pools in, and it may not be guessed on the caller's behalf.
// productId is required for the same reason: every deposit account is opened
// FROM a product, because a floating terms row with no product would have
// nothing to float to.
type OpenDepositAccountRequest struct {
	Name           string `json:"name"`
	Asset          string `json:"asset"`
	ProductID      string `json:"productId"`
	OverdraftLimit int64  `json:"overdraftLimit"`
	// Identifiers are the external addresses to open the account with, in the
	// schemes SOMEBODY ELSE issues — a card PAN is the shape.
	Identifiers []IdentifierDTO `json:"identifiers"`
}

type StatusRequest struct {
	Action string `json:"action"`
}

type CreateHoldRequest struct {
	Amount      int64      `json:"amount"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	Description string     `json:"description"`
}

type CaptureHoldRequest struct {
	Counterparty string `json:"counterparty"`
	// Subsidiary names which one when Counterparty is a control account: a deposit
	// account's id when the money is going to another customer of this bank, and
	// empty for one of the bank's own accounts.
	Subsidiary  string `json:"subsidiary,omitempty"`
	Amount      int64  `json:"amount"`
	Description string `json:"description"`
}

type SnapshotRequest struct {
	Date string `json:"date"`
}

type FundRequest struct {
	Account     string `json:"account"`
	Amount      int64  `json:"amount"`
	Description string `json:"description"`
}

// TransferRequest is a book transfer between two of this bank's own customers.
type TransferRequest struct {
	From        string `json:"from"`
	To          string `json:"to"`
	Amount      int64  `json:"amount"`
	Description string `json:"description"`
}

// TransferDTO is the receipt: what the posting is called in the ledger, which
// account the address turned out to be, and what the PAYER has left.
type TransferDTO struct {
	TransactionID string     `json:"transactionId"`
	From          string     `json:"from"`
	To            string     `json:"to"`
	Balance       BalanceDTO `json:"balance"`
}

// The three requests that replaced setOverdraftTermsRequest, one per decision
// the old single call conflated.

// SetOverdraftLimitRequest changes what this customer may go overdrawn by. The
// limit is never part of a product: it is an underwriting decision about one
// customer, which is why product.OverdraftPricing cannot express one.
type SetOverdraftLimitRequest struct {
	Limit         int64      `json:"limit"`
	EffectiveFrom *time.Time `json:"effectiveFrom"`
}

// SetOverdraftPricingRequest gives this customer a negotiated price instead of
// the product's, or clears one.
type SetOverdraftPricingRequest struct {
	Pricing       *OverdraftPricingDTO `json:"pricing"`
	EffectiveFrom *time.Time           `json:"effectiveFrom"`
}

// OverdraftPricingDTO is the floating parameter group on the wire. Rate and
// UnarrangedRate are millionths, the same convention FacilityDTO uses.
type OverdraftPricingDTO struct {
	Rate           int64  `json:"rate"`
	UnarrangedRate int64  `json:"unarrangedRate"`
	DayCount       string `json:"dayCount"`
}

// ChangeProductRequest migrates an account onto another product. The days
// before effectiveFrom still resolve against the product that priced them: a
// migration is not a rewrite.
type ChangeProductRequest struct {
	ProductID     string     `json:"productId"`
	EffectiveFrom *time.Time `json:"effectiveFrom"`
}

type ChargeOverdraftInterestRequest struct {
	Date string `json:"date"`
}
