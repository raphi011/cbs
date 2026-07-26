package api

import (
	"time"

	"github.com/raphi011/cbs/deposit"
)

// Wire format for the demand-deposit layer: customer accounts, holds,
// balances, snapshots, and their requests.

type depositAccountDTO struct {
	ID             string    `json:"id"`
	GLAccount      string    `json:"glAccount"`
	Name           string    `json:"name"`
	Status         string    `json:"status"`
	OverdraftLimit int64     `json:"overdraftLimit"`
	CreatedAt      time.Time `json:"createdAt"`
}

func toDepositAccountDTO(a deposit.Account) depositAccountDTO {
	return depositAccountDTO{
		ID:             string(a.ID),
		GLAccount:      string(a.GLAccount),
		Name:           a.Name,
		Status:         a.Status.String(),
		OverdraftLimit: a.OverdraftLimit,
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
		Amount:      h.Amount,
		ExpiresAt:   h.ExpiresAt,
		Description: h.Description,
		Status:      h.Status.String(),
		CreatedAt:   h.CreatedAt,
	}
}

type balanceDTO struct {
	Book      int64 `json:"book"`
	Holds     int64 `json:"holds"`
	Available int64 `json:"available"`
}

func toBalanceDTO(b deposit.Balance) balanceDTO {
	return balanceDTO{Book: b.Book, Holds: b.Holds, Available: b.Available}
}

type snapshotDTO struct {
	AccountID string     `json:"accountId"`
	Date      time.Time  `json:"date"`
	Balance   balanceDTO `json:"balance"`
	TakenAt   time.Time  `json:"takenAt"`
}

func toSnapshotDTO(s deposit.Snapshot) snapshotDTO {
	return snapshotDTO{AccountID: string(s.AccountID), Date: s.Date, Balance: toBalanceDTO(s.Balance), TakenAt: s.TakenAt}
}

type openDepositAccountRequest struct {
	Name           string `json:"name"`
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
