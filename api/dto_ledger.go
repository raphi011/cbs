package api

import (
	"time"

	"github.com/raphi011/cbs/ledger"
)

// Wire format for the general-ledger layer: ledgers, subledgers, accounts,
// transactions, and the requests that create them.

type LedgerDTO struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

func ToLedgerDTO(l ledger.Ledger) LedgerDTO {
	return LedgerDTO{ID: string(l.ID), Name: l.Name, CreatedAt: l.CreatedAt}
}

type SubledgerDTO struct {
	ID        string    `json:"id"`
	LedgerID  string    `json:"ledgerId"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

func ToSubledgerDTO(sl ledger.Subledger) SubledgerDTO {
	return SubledgerDTO{ID: string(sl.ID), LedgerID: string(sl.LedgerID), Name: sl.Name, CreatedAt: sl.CreatedAt}
}

type AccountDTO struct {
	ID          string `json:"id"`
	SubledgerID string `json:"subledgerId"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Asset       string `json:"asset"`
	// Control says this line pools subsidiaries: it stands for many, and every
	// entry against it names which.
	Control   bool      `json:"control"`
	CreatedAt time.Time `json:"createdAt"`
}

func ToAccountDTO(a ledger.Account) AccountDTO {
	return AccountDTO{
		ID:          string(a.ID),
		SubledgerID: string(a.SubledgerID),
		Name:        a.Name,
		Type:        a.Type.String(),
		Asset:       string(a.Asset),
		Control:     a.Control,
		CreatedAt:   a.CreatedAt,
	}
}

// AccountBalanceDTO is the response of GET .../accounts/{aid}/balance.
type AccountBalanceDTO struct {
	AccountID string `json:"accountId"`
	// Subsidiary is which one the figures are for, absent when they are the whole
	// account's.
	Subsidiary string `json:"subsidiary,omitempty"`
	Asset      string `json:"asset"`
	Balance    int64  `json:"balance"`
	// ValueDateBalance is the balance as of the end of the requested day, counting
	// only entries that have taken economic effect.
	ValueDateBalance int64 `json:"valueDateBalance"`
}

// SubsidiaryBalanceDTO is one subsidiary's share of a control account. The
// asset travels with the number for AccountBalanceDTO's reason: an integer in
// minor units is not an amount without it.
type SubsidiaryBalanceDTO struct {
	Subsidiary string `json:"subsidiary"`
	Asset      string `json:"asset"`
	Balance    int64  `json:"balance"`
}

type EntryDTO struct {
	ID        string `json:"id,omitempty"`
	AccountID string `json:"accountId"`
	// Subsidiary is what this leg belongs to within a control account — a deposit
	// account's id, a facility's. Absent means the whole account, which is what a
	// leg against one of the bank's own positions carries.
	Subsidiary string `json:"subsidiary,omitempty"`
	Amount     int64  `json:"amount"`
	Direction  string `json:"direction"`
	// Asset is the entry's account's asset.
	Asset string `json:"asset,omitempty"`
	// ValueDate is when THIS LEG takes economic effect, which is not always the
	// transaction's: the SEPA debtor posting value-dates the payer's leg to the
	// debit date and the suspense leg to settlement, days apart.
	ValueDate *time.Time `json:"valueDate,omitempty"`
}

type TransactionDTO struct {
	ID             string            `json:"id"`
	IdempotencyKey string            `json:"idempotencyKey,omitempty"`
	Entries        []EntryDTO        `json:"entries"`
	BookingDate    time.Time         `json:"bookingDate"`
	ValueDate      time.Time         `json:"valueDate"`
	Status         string            `json:"status"`
	Description    string            `json:"description,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	ReversalOf     string            `json:"reversalOf,omitempty"`
	CreatedAt      time.Time         `json:"createdAt"`
}

// ToTransactionDTO renders a transaction, including each entry's asset.
func ToTransactionDTO(tx ledger.Transaction, assets map[ledger.AccountID]ledger.AssetCode) TransactionDTO {
	entries := make([]EntryDTO, len(tx.Entries))
	for i, e := range tx.Entries {
		entries[i] = EntryDTO{
			ID:         string(e.ID),
			AccountID:  string(e.AccountID),
			Subsidiary: e.Subsidiary,
			Amount:     int64(e.Amount),
			Direction:  e.Direction.String(),
			Asset:      string(assets[e.AccountID]),
			// Taken per leg, not from tx.ValueDate: on a payment's debtor posting the
			// two differ by the settlement delay, and collapsing them here is exactly
			// the bug this field fixes.
			ValueDate: valueDatePtr(e.ValueDate),
		}
	}
	return TransactionDTO{
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

// valueDatePtr renders an entry's value date, or nothing at all for a zero one.
func valueDatePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// CreateAccountRequest carries a required asset.
type CreateAccountRequest struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Asset string `json:"asset"`
}

type PostTransactionRequest struct {
	IdempotencyKey string            `json:"idempotencyKey"`
	Entries        []EntryDTO        `json:"entries"`
	BookingDate    *time.Time        `json:"bookingDate"`
	ValueDate      *time.Time        `json:"valueDate"`
	Description    string            `json:"description"`
	Metadata       map[string]string `json:"metadata"`
}

// ToDomain converts the request to a ledger.PostTransactionRequest, parsing
// each entry's direction. A bad direction string yields an error that the
// handler maps to 400.
func (req PostTransactionRequest) ToDomain() (ledger.PostTransactionRequest, error) {
	entries := make([]ledger.Entry, len(req.Entries))
	for i, e := range req.Entries {
		dir, err := DirectionFromString(e.Direction)
		if err != nil {
			return ledger.PostTransactionRequest{}, err
		}
		entries[i] = ledger.Entry{
			AccountID:  ledger.AccountID(e.AccountID),
			Subsidiary: e.Subsidiary,
			Amount:     ledger.Amount(e.Amount),
			Direction:  dir,
		}
		if e.ValueDate != nil {
			entries[i].ValueDate = *e.ValueDate
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

func AccountTypeFromString(s string) (ledger.AccountType, error) {
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
		return 0, BadRequest("invalid account type %q (want Asset, Liability, Equity, Revenue, or Expense)", s)
	}
}

func DirectionFromString(s string) (ledger.Direction, error) {
	switch s {
	case "Debit":
		return ledger.Debit, nil
	case "Credit":
		return ledger.Credit, nil
	default:
		return 0, BadRequest("invalid direction %q (want Debit or Credit)", s)
	}
}
