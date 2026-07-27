package api

import (
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
)

// Wire format for the demand-deposit layer: customer accounts, holds,
// balances, snapshots, and their requests.

type depositAccountDTO struct {
	ID             string    `json:"id"`
	GLAccount      string    `json:"glAccount"`
	Name           string    `json:"name"`
	Asset          string    `json:"asset"`
	Status         string    `json:"status"`
	OverdraftLimit int64     `json:"overdraftLimit"`
	CreatedAt      time.Time `json:"createdAt"`
}

func toDepositAccountDTO(a deposit.Account) depositAccountDTO {
	return depositAccountDTO{
		ID:             string(a.ID),
		GLAccount:      string(a.GLAccount),
		Name:           a.Name,
		Asset:          string(a.Asset),
		Status:         a.Status.String(),
		OverdraftLimit: int64(a.OverdraftLimit),
		CreatedAt:      a.CreatedAt,
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
