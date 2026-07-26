package api

import (
	"fmt"
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Wire format for the general-ledger layer: ledgers, subledgers, accounts,
// transactions, and the requests that create them.

type ledgerDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

func toLedgerDTO(l ledger.Ledger) ledgerDTO {
	return ledgerDTO{ID: string(l.ID), Name: l.Name, CreatedAt: l.CreatedAt}
}

type subledgerDTO struct {
	ID        string    `json:"id"`
	LedgerID  string    `json:"ledgerId"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

func toSubledgerDTO(sl ledger.Subledger) subledgerDTO {
	return subledgerDTO{ID: string(sl.ID), LedgerID: string(sl.LedgerID), Name: sl.Name, CreatedAt: sl.CreatedAt}
}

type accountDTO struct {
	ID          string    `json:"id"`
	SubledgerID string    `json:"subledgerId"`
	Name        string    `json:"name"`
	Type        string    `json:"type"`
	CreatedAt   time.Time `json:"createdAt"`
}

func toAccountDTO(a ledger.Account) accountDTO {
	return accountDTO{
		ID:          string(a.ID),
		SubledgerID: string(a.SubledgerID),
		Name:        a.Name,
		Type:        a.Type.String(),
		CreatedAt:   a.CreatedAt,
	}
}

type entryDTO struct {
	ID        string `json:"id,omitempty"`
	AccountID string `json:"accountId"`
	Amount    int64  `json:"amount"`
	Direction string `json:"direction"`
}

type transactionDTO struct {
	ID             string            `json:"id"`
	IdempotencyKey string            `json:"idempotencyKey,omitempty"`
	Entries        []entryDTO        `json:"entries"`
	BookingDate    time.Time         `json:"bookingDate"`
	ValueDate      time.Time         `json:"valueDate"`
	Status         string            `json:"status"`
	Description    string            `json:"description,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	ReversalOf     string            `json:"reversalOf,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
}

func toTransactionDTO(tx ledger.Transaction) transactionDTO {
	entries := make([]entryDTO, len(tx.Entries))
	for i, e := range tx.Entries {
		entries[i] = entryDTO{
			ID:        string(e.ID),
			AccountID: string(e.AccountID),
			Amount:    e.Amount,
			Direction: e.Direction.String(),
		}
	}
	return transactionDTO{
		ID:             string(tx.ID),
		IdempotencyKey: tx.IdempotencyKey,
		Entries:        entries,
		BookingDate:    tx.BookingDate,
		ValueDate:      tx.ValueDate,
		Status:         tx.Status.String(),
		Description:    tx.Description,
		Metadata:       tx.Metadata,
		ReversalOf:     string(tx.ReversalOf),
		CreatedAt:      tx.CreatedAt,
	}
}

type createAccountRequest struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type postTransactionRequest struct {
	IdempotencyKey string            `json:"idempotencyKey"`
	Entries        []entryDTO        `json:"entries"`
	BookingDate    *time.Time        `json:"bookingDate"`
	ValueDate      *time.Time        `json:"valueDate"`
	Description    string            `json:"description"`
	Metadata       map[string]string `json:"metadata"`
}

// toDomain converts the request to a ledger.PostTransactionRequest, parsing
// each entry's direction. A bad direction string yields an error that the
// handler maps to 400.
func (req postTransactionRequest) toDomain() (ledger.PostTransactionRequest, error) {
	entries := make([]ledger.Entry, len(req.Entries))
	for i, e := range req.Entries {
		dir, err := directionFromString(e.Direction)
		if err != nil {
			return ledger.PostTransactionRequest{}, err
		}
		entries[i] = ledger.Entry{
			AccountID: ledger.AccountID(e.AccountID),
			Amount:    e.Amount,
			Direction: dir,
		}
	}
	out := ledger.PostTransactionRequest{
		IdempotencyKey: req.IdempotencyKey,
		Entries:        entries,
		Description:    req.Description,
		Metadata:       req.Metadata,
	}
	if req.BookingDate != nil {
		out.BookingDate = *req.BookingDate
	}
	if req.ValueDate != nil {
		out.ValueDate = *req.ValueDate
	}
	return out, nil
}

func accountTypeFromString(s string) (ledger.AccountType, error) {
	switch s {
	case "Asset":
		return ledger.Asset, nil
	case "Liability":
		return ledger.Liability, nil
	case "Equity":
		return ledger.Equity, nil
	case "Revenue":
		return ledger.Revenue, nil
	case "Expense":
		return ledger.Expense, nil
	default:
		return 0, fmt.Errorf("invalid account type %q (want Asset, Liability, Equity, Revenue, or Expense)", s)
	}
}

func directionFromString(s string) (ledger.Direction, error) {
	switch s {
	case "Debit":
		return ledger.Debit, nil
	case "Credit":
		return ledger.Credit, nil
	default:
		return 0, fmt.Errorf("invalid direction %q (want Debit or Credit)", s)
	}
}
