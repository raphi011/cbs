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
	// entry against it names which. A client needs it for two things it cannot
	// get right otherwise — a posting form must ask for the subsidiary, and an
	// account page must offer the detail under the line rather than only the
	// total.
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
//
// Balance is an integer in the account's minor units, so the asset it is
// denominated in travels with it — the same rule BalanceDTO follows on the
// deposit layer. A number with no asset is not an amount.
type AccountBalanceDTO struct {
	AccountID string `json:"accountId"`
	// Subsidiary is which one the figures are for, absent when they are the
	// whole account's. It is echoed back because a client asking for one
	// customer's balance and a client asking for the pool's send the same route
	// two different questions.
	Subsidiary string `json:"subsidiary,omitempty"`
	Asset      string `json:"asset"`
	Balance    int64  `json:"balance"`
	// ValueDateBalance is the balance as of the end of the requested day,
	// counting only entries that have taken economic effect. It is what
	// interest is computed from, and it differs from Balance whenever a
	// posting is value-dated away from its booking date.
	ValueDateBalance int64 `json:"valueDateBalance"`
}

// SubsidiaryBalanceDTO is one subsidiary's share of a control account. The asset
// travels with the number for AccountBalanceDTO's reason: an integer in minor
// units is not an amount without it.
//
// What a subsidiary IS — a deposit account, a facility — is not said here and
// cannot be: the ledger holds an opaque string, and the layer that knows what it
// names is the one rendering the link.
type SubsidiaryBalanceDTO struct {
	Subsidiary string `json:"subsidiary"`
	Asset      string `json:"asset"`
	Balance    int64  `json:"balance"`
}

type EntryDTO struct {
	ID        string `json:"id,omitempty"`
	AccountID string `json:"accountId"`
	// Subsidiary is what this leg belongs to within a control account — a
	// deposit account's id, a facility's. Absent means the whole account,
	// which is what a leg against one of the bank's own positions carries.
	//
	// It travels in both directions and it is not optional in the domain sense:
	// a control account named without one is refused, and a plain account named
	// with one is too, so a client that drops it on the way in cannot post a
	// customer's money and a client that ignores it on the way out cannot tell
	// whose a leg was.
	Subsidiary string `json:"subsidiary,omitempty"`
	Amount     int64  `json:"amount"`
	Direction  string `json:"direction"`
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
	// Per LEG, because a posting's two legs can take economic effect on different
	// days: the transaction-level date alone would hide that.
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

// ToTransactionDTO renders a transaction, including each entry's asset. An
// entry carries no asset of its own — Amount balances per asset precisely
// because the asset is a property of the account it posts to — so rendering
// it means resolving each entry's account. assets is the pre-resolved
// account-to-asset map; ToTransactionDTO does no I/O of its own, so a caller
// rendering several transactions can resolve the whole batch's accounts once
// and reuse the map across every call. The resolving is in transaction.go,
// which is where every file in this package that reads a store lives.
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
			// Taken per leg, not from tx.ValueDate: on a payment's debtor
			// posting the two differ by the settlement delay, and collapsing
			// them here is exactly the bug this field fixes. A stored entry
			// always carries a concrete date, so the address is never of a zero
			// time — but a leg written before this field existed would render
			// as an absent valueDate rather than as 0001-01-01.
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
// A zero time.Time marshals as "0001-01-01T00:00:00Z", which reads as a real
// date a client would render and compare against; absent says what is actually
// true, that this leg has no date of its own to report.
func valueDatePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// CreateAccountRequest carries a required asset. There is no default: an
// account and its asset are inseparable, and quietly booking a new account in
// euro because the caller forgot to say is the bug the asset dimension exists
// to prevent. A missing asset is a 400, not an assumption.
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
//
// An entry's value date is passed through as given, INCLUDING absent: a zero
// ValueDate is what the domain reads as "inherit the transaction's", and
// PostTransaction resolves it there. Substituting req.ValueDate here would
// duplicate that rule in a second place, and would keep working right up until
// the two disagreed.
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
