package api

import (
	"context"
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
	Asset       string    `json:"asset"`
	CreatedAt   time.Time `json:"createdAt"`
}

func toAccountDTO(a ledger.Account) accountDTO {
	return accountDTO{
		ID:          string(a.ID),
		SubledgerID: string(a.SubledgerID),
		Name:        a.Name,
		Type:        a.Type.String(),
		Asset:       string(a.Asset),
		CreatedAt:   a.CreatedAt,
	}
}

// accountBalanceDTO is the response of GET .../accounts/{aid}/balance.
//
// Balance is an integer in the account's minor units, so the asset it is
// denominated in travels with it — the same rule balanceDTO follows on the
// deposit layer. This response used to be a bare `{accountId, balance}` map
// literal; a number with no asset is not an amount.
type accountBalanceDTO struct {
	AccountID string `json:"accountId"`
	Asset     string `json:"asset"`
	Balance   int64  `json:"balance"`
	// ValueDateBalance is the balance as of the end of the requested day,
	// counting only entries that have taken economic effect. It is what
	// interest is computed from, and it differs from Balance whenever a
	// posting is value-dated away from its booking date.
	ValueDateBalance int64 `json:"valueDateBalance"`
}

type entryDTO struct {
	ID        string `json:"id,omitempty"`
	AccountID string `json:"accountId"`
	Amount    int64  `json:"amount"`
	Direction string `json:"direction"`
	// Asset is the entry's account's asset. It is never sent by a client — a
	// transaction request names accounts, not assets, and the asset a leg
	// posts in is decided by the account it debits or credits — so this is
	// populated only when rendering a response.
	Asset string `json:"asset,omitempty"`
	// ValueDate is when THIS LEG takes economic effect, which is not always the
	// transaction's: the SEPA debtor posting value-dates the payer's leg to the
	// debit date and the suspense leg to settlement, days apart. See
	// ledger.Entry.ValueDate.
	//
	// It travels in both directions, and the pointer is what makes that work.
	// Absent on a request means "the transaction's", which is the domain's own
	// rule for a zero value date — sending the transaction's date on every leg
	// instead would be indistinguishable from deliberately pinning them
	// together, and a client that simply does not care about per-leg dates
	// should not have to compute one. On a response it is always populated:
	// PostTransaction resolves every leg's date before storing it, so no reader
	// has to fall back to the parent.
	//
	// Rendering only the transaction-level date, as this used to, showed one
	// date for a posting whose legs disagree — the one place the ledger's
	// per-leg value dating was invisible from outside Go.
	ValueDate *time.Time `json:"valueDate,omitempty"`
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

// entryAccountIDs collects the distinct account IDs referenced by any entry
// across one or more transactions, in first-seen order. It is the input to
// entryAssets, kept separate so a caller with just one transaction can build
// it without allocating a slice of transactions.
func entryAccountIDs(txs []ledger.Transaction) []ledger.AccountID {
	seen := make(map[ledger.AccountID]bool)
	var ids []ledger.AccountID
	for _, tx := range txs {
		for _, e := range tx.Entries {
			if !seen[e.AccountID] {
				seen[e.AccountID] = true
				ids = append(ids, e.AccountID)
			}
		}
	}
	return ids
}

// entryAssets resolves the asset of every account referenced by any entry
// across txs, in one Book.GetAccounts call. Rendering a listing of N
// transactions used to call Book.GetAccount once per entry — one BEGIN…COMMIT
// each — which made a listing's cost scale with how many transactions it
// rendered rather than how much work it actually did. Resolving the whole batch's accounts up front, once, is what keeps a
// listing at one round trip regardless of N.
func entryAssets(ctx context.Context, lb *ledger.Book, txs []ledger.Transaction) (map[ledger.AccountID]ledger.AssetCode, error) {
	ids := entryAccountIDs(txs)
	accts, err := lb.GetAccounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[ledger.AccountID]ledger.AssetCode, len(accts))
	for id, a := range accts {
		out[id] = a.Asset
	}
	return out, nil
}

// toTransactionDTO renders a transaction, including each entry's asset. An
// entry carries no asset of its own — Amount balances per asset precisely
// because the asset is a property of the account it posts to — so rendering
// it means resolving each entry's account. assets is the pre-resolved
// account-to-asset map (see entryAssets); toTransactionDTO does no I/O of its
// own, so a caller rendering several transactions can resolve the whole
// batch's accounts once and reuse the map across every call.
func toTransactionDTO(tx ledger.Transaction, assets map[ledger.AccountID]ledger.AssetCode) transactionDTO {
	entries := make([]entryDTO, len(tx.Entries))
	for i, e := range tx.Entries {
		entries[i] = entryDTO{
			ID:        string(e.ID),
			AccountID: string(e.AccountID),
			Amount:    int64(e.Amount),
			Direction: e.Direction.String(),
			Asset:     string(assets[e.AccountID]),
			// Taken per leg, not from tx.ValueDate: on a payment's debtor
			// posting the two differ by the settlement delay, and collapsing
			// them here is exactly the bug this field fixes. A stored entry
			// always carries a concrete date, so the address is never of a zero
			// time — but a leg written before this field existed would render
			// as an absent valueDate rather than as 0001-01-01.
			ValueDate: valueDatePtr(e.ValueDate),
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

// valueDatePtr renders an entry's value date, or nothing at all for a zero one.
// A zero time.Time marshals as "0001-01-01T00:00:00Z", which reads as a real
// date a client would render and compare against; absent says what is actually
// true, that this leg has no date of its own to report.
func valueDatePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// createAccountRequest carries a required asset. There is no default: an
// account and its asset are inseparable, and quietly booking a new account in
// euro because the caller forgot to say is the bug the asset dimension exists
// to prevent. A missing asset is a 400, not an assumption.
type createAccountRequest struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Asset string `json:"asset"`
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
//
// An entry's value date is passed through as given, INCLUDING absent: a zero
// ValueDate is what the domain reads as "inherit the transaction's", and
// PostTransaction resolves it there. Substituting req.ValueDate here would
// duplicate that rule in a second place, and would keep working right up until
// the two disagreed.
func (req postTransactionRequest) toDomain() (ledger.PostTransactionRequest, error) {
	entries := make([]ledger.Entry, len(req.Entries))
	for i, e := range req.Entries {
		dir, err := directionFromString(e.Direction)
		if err != nil {
			return ledger.PostTransactionRequest{}, err
		}
		entries[i] = ledger.Entry{
			AccountID: ledger.AccountID(e.AccountID),
			Amount:    ledger.Amount(e.Amount),
			Direction: dir,
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
