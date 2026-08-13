package ledger

import (
	"context"
	"maps"
	"slices"
	"time"
)

// ---------------------------------------------------------------------------
// One account's history, and the balance decomposed into dated lots
// ---------------------------------------------------------------------------

// SubsidiaryBalance is one subsidiary's share of a control account. It is what a
// control line drills into: the detail whose sum IS the line's own balance, read
// from the same entries rather than from a second place that could disagree.
type SubsidiaryBalance struct {
	Subsidiary string
	Balance    Amount
}

// SubsidiaryBalances is every subsidiary under a control account, ordered by
// subsidiary, with the ones that net to zero left out — a customer who has
// repaid is not a row in what the bank owes. One scan of the pool answers it,
// which is the difference between this and a balance per subsidiary.
func SubsidiaryBalances(ctx context.Context, tx Tx, book BookID, account AccountID, normal Direction) ([]SubsidiaryBalance, error) {
	bySubsidiary := map[string]Amount{}
	for e, err := range tx.ScanEntries(ctx, book, account.Total(), EntryFilter{}) {
		if err != nil {
			return nil, err
		}
		bySubsidiary[e.Subsidiary] += signed(e, normal)
	}

	out := make([]SubsidiaryBalance, 0, len(bySubsidiary))
	for _, id := range slices.Sorted(maps.Keys(bySubsidiary)) {
		if bySubsidiary[id] == 0 {
			continue
		}
		out = append(out, SubsidiaryBalance{Subsidiary: id, Balance: bySubsidiary[id]})
	}
	return out, nil
}

// SubsidiaryBalances is every subsidiary under a control account. See the fold
// above; this is it in its own read-only unit of work, on this book.
func (s *Book) SubsidiaryBalances(ctx context.Context, account AccountID) ([]SubsidiaryBalance, error) {
	var out []SubsidiaryBalance
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.SubsidiaryBalancesTx(ctx, tx, account)
		return err
	})
	return out, err
}

// SubsidiaryBalancesTx is SubsidiaryBalances within a caller-supplied unit of
// work.
func (s *Book) SubsidiaryBalancesTx(ctx context.Context, tx Tx, account AccountID) ([]SubsidiaryBalance, error) {
	acct, err := tx.GetAccount(ctx, s.id, account)
	if err != nil {
		return nil, err
	}
	if !acct.Control {
		return []SubsidiaryBalance{}, nil
	}
	return SubsidiaryBalances(ctx, tx, s.id, account, acct.Type.NormalBalance())
}

// AccountHistory is every transaction that touched one account, in book order,
// each carrying the balance the account stood at once it had posted.
type AccountHistory struct {
	// Position is what was asked for: a whole account, or one subsidiary within a
	// control account.
	Position Position
	// Normal is the direction that INCREASES this account, taken off the account's
	// type. Every Movement and Running figure below is signed by it, so a caller
	// never has to know whether it is looking at an asset or a liability.
	Normal Direction
	Rows   []HistoryRow
	// Closing is the balance after the last row, and it equals BookBalance for
	// the same account. It is carried rather than left to the caller to sum
	// because an empty history has one too, and it is zero.
	Closing Amount
}

// HistoryRow is one TRANSACTION's effect on one account.
type HistoryRow struct {
	Transaction TransactionID
	BookingDate time.Time
	ValueDate   time.Time

	// Movement is this transaction's net effect on the account, signed by
	// AccountHistory.Normal: positive raises the balance.
	Movement Amount
	// Running is the balance after this row, in the same sign convention.
	Running Amount

	Description string
	Metadata    map[string]string
}

// AccountHistory is AccountHistoryTx in its own read-only unit of work.
func (s *Book) AccountHistory(ctx context.Context, pos Position) (AccountHistory, error) {
	var out AccountHistory
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.AccountHistoryTx(ctx, tx, pos)
		return err
	})
	return out, err
}

// AccountHistoryTx is AccountHistory within a caller-supplied unit of work.
func (s *Book) AccountHistoryTx(ctx context.Context, tx Tx, pos Position) (AccountHistory, error) {
	acct, err := tx.GetAccount(ctx, s.id, pos.Account)
	if err != nil {
		return AccountHistory{}, err
	}
	txs, err := tx.ListTransactionsForPosition(ctx, s.id, pos)
	if err != nil {
		return AccountHistory{}, err
	}

	hist := AccountHistory{
		Position: pos,
		Normal:   acct.Type.NormalBalance(),
		Rows:     make([]HistoryRow, 0, len(txs)),
	}
	for _, t := range txs {
		var movement Amount
		for _, e := range t.Entries {
			// A transaction is listed whole, so a control account's rows carry
			// every subsidiary's legs; the position is what selects among them.
			if e.AccountID != pos.Account {
				continue
			}
			if pos.Subsidiary != "" && e.Subsidiary != pos.Subsidiary {
				continue
			}
			movement += signed(e, hist.Normal)
		}
		hist.Closing += movement
		hist.Rows = append(hist.Rows, HistoryRow{
			Transaction: t.ID,
			BookingDate: t.BookingDate,
			ValueDate:   t.ValueDate,
			Movement:    movement,
			Running:     hist.Closing,
			Description: t.Description,
			Metadata:    t.Metadata,
		})
	}
	return hist, nil
}

// ---------------------------------------------------------------------------
// Ageing
// ---------------------------------------------------------------------------

// Lot is one part of a balance, with the transaction that put it there and how
// long it has been there.
type Lot struct {
	Transaction TransactionID
	// Since is the booking date of the transaction this lot is left over from.
	Since time.Time
	// Days is whole days from Since to the asOf the ageing was taken at, both
	// truncated to UTC midnight. Same-day is 0.
	Days int
	// Amount is signed by the account's normal direction, like every other
	// figure here, and a lot's sign is always the balance's: a decomposition
	// with lots pointing both ways would not be one.
	Amount      Amount
	Description string
	Metadata    map[string]string
}

// Ageing is a balance decomposed into dated lots, oldest first.
type Ageing struct {
	Position Position
	AsOf     time.Time
	Balance  Amount
	Lots     []Lot
}

// Oldest is the age in days of the oldest lot, and false when there is nothing
// to age. A zero balance has no lots, and neither has an account that never
// moved.
func (a Ageing) Oldest() (int, bool) {
	if len(a.Lots) == 0 {
		return 0, false
	}
	return a.Lots[0].Days, true
}

// OlderThan is the lots that have sat for at least days, which is what a report
// prints in bold. It is a filter and not a judgement: what counts as too long is
// a rulebook's, and this package holds no rulebooks.
func (a Ageing) OlderThan(days int) []Lot {
	var out []Lot
	for _, l := range a.Lots {
		if l.Days >= days {
			out = append(out, l)
		}
	}
	return out
}

// AgeAt decomposes the history's closing balance into dated lots as at asOf.
func (h AccountHistory) AgeAt(asOf time.Time) Ageing {
	out := Ageing{Position: h.Position, AsOf: asOf, Balance: h.Closing}
	day := DayStart(asOf)

	for _, r := range h.Rows {
		m := r.Movement
		// Consume the front of the queue while this movement runs against it.
		for len(out.Lots) > 0 && m != 0 && opposed(out.Lots[0].Amount, m) {
			if abs(out.Lots[0].Amount) > abs(m) {
				out.Lots[0].Amount += m
				m = 0
				break
			}
			m += out.Lots[0].Amount
			out.Lots = out.Lots[1:]
		}
		if m == 0 {
			continue
		}
		out.Lots = append(out.Lots, Lot{
			Transaction: r.Transaction,
			Since:       r.BookingDate,
			Days:        int(day.Sub(DayStart(r.BookingDate)) / (24 * time.Hour)),
			Amount:      m,
			Description: r.Description,
			Metadata:    r.Metadata,
		})
	}
	return out
}

func opposed(a, b Amount) bool { return (a > 0) != (b > 0) }

func abs(a Amount) Amount {
	if a < 0 {
		return -a
	}
	return a
}
