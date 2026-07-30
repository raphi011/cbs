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
	ID             string `json:"id"`
	GLAccount      string `json:"glAccount"`
	Name           string `json:"name"`
	Asset          string `json:"asset"`
	Status         string `json:"status"`
	OverdraftLimit int64  `json:"overdraftLimit"`

	OverdraftRate   int64  `json:"overdraftRate"`
	UnarrangedRate  int64  `json:"unarrangedRate"`
	RateScale       int64  `json:"rateScale"`
	DayCount        string `json:"dayCount"`
	AccruedInterest int64  `json:"accruedInterest"`
	// InterestGLAccount is empty until the first non-zero rate is set — see
	// deposit.Register.SetOverdraftTerms.
	InterestGLAccount string `json:"interestGlAccount,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
}

func toDepositAccountDTO(a deposit.Account, t deposit.OverdraftTerms) depositAccountDTO {
	return depositAccountDTO{
		ID:             string(a.ID),
		GLAccount:      string(a.GLAccount),
		Name:           a.Name,
		Asset:          string(a.Asset),
		Status:         a.Status.String(),
		OverdraftLimit: int64(t.OverdraftLimit),

		OverdraftRate:     int64(t.Rate),
		UnarrangedRate:    int64(t.UnarrangedRate),
		RateScale:         interest.RateScale,
		DayCount:          t.DayCount.String(),
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
	OverdraftLimit int64     `json:"overdraftLimit"`
	Rate           int64     `json:"rate"`
	UnarrangedRate int64     `json:"unarrangedRate"`
	RateScale      int64     `json:"rateScale"`
	DayCount       string    `json:"dayCount"`
	CreatedAt      time.Time `json:"createdAt"`
}

func toOverdraftTermsDTO(t deposit.OverdraftTerms) overdraftTermsDTO {
	return overdraftTermsDTO{
		AccountID:      string(t.AccountID),
		EffectiveFrom:  t.EffectiveFrom,
		OverdraftLimit: int64(t.OverdraftLimit),
		Rate:           int64(t.Rate),
		UnarrangedRate: int64(t.UnarrangedRate),
		RateScale:      interest.RateScale,
		DayCount:       t.DayCount.String(),
		CreatedAt:      t.CreatedAt,
	}
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
type openDepositAccountRequest struct {
	Name           string `json:"name"`
	Asset          string `json:"asset"`
	OverdraftLimit int64  `json:"overdraftLimit"`
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

// setOverdraftTermsRequest carries an account's arranged overdraft limit and
// credit terms. Rate and UnarrangedRate are millionths, the same wire
// convention facilityDTO uses for a lending rate.
//
// effectiveFrom is the day the terms take economic effect, RFC3339, and may be
// in the past or the future: a repricing agreed on the 1st and entered on the
// 15th is the ordinary case, and a row effective next month is inert until the
// end-of-day runs reach it. Absent or null means today, which is what every
// caller before this field existed meant. A backdated row moves interest that
// has already been charged; the audit log is the only control on that.
type setOverdraftTermsRequest struct {
	Limit          int64      `json:"limit"`
	Rate           int64      `json:"rate"`
	UnarrangedRate int64      `json:"unarrangedRate"`
	DayCount       string     `json:"dayCount"`
	EffectiveFrom  *time.Time `json:"effectiveFrom"`
}

type chargeOverdraftInterestRequest struct {
	Date string `json:"date"`
}
