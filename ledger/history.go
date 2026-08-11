package ledger

import (
	"context"
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
// subsidiary, with the ones that net to zero left out — a customer who has repaid
// is not a row in what the bank owes.
//
// A PLAIN account answers with nothing at all rather than with one unnamed row.
// It pools nobody, so there is no detail under it to show; its balance is
// BookBalance and that is the whole of it.
//
// Returns ErrAccountNotFound.
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
	return tx.SubsidiaryBalances(ctx, s.id, account, acct.Type.NormalBalance())
}

// AccountHistory is every transaction that touched one account, in book order,
// each carrying the balance the account stood at once it had posted.
//
// It answers a different question from every other read in this file, and the
// difference is what justifies it. BookBalance says what an account holds;
// SeriesTx says what it held on each value date. This says HOW it came to hold
// it — which transaction moved it, when, and what it stood at afterwards.
//
// Two readers want exactly that. A reconciliation holding a statement's closing
// balance against the book needs the balance AT THE POSTING the statement
// describes, not the balance now. An ageing report needs the same rows in the
// same order to work out which of them the current balance is still made of. One
// read serves both, which is why AgeAt below is a method on this rather than a
// second query.
//
// # It is a BOOKING-date view and deliberately not a value-dated one
//
// Rows are in the store's listing order — creation instant, then insertion
// sequence — which is the only total order a book has. A value-dated view of the
// same account is SeriesTx, and the two answer to different questions: value
// dating is about when money takes effect, and this is about what the book
// recorded and in what order it recorded it.
//
// # Reversed transactions count
//
// For BookBalance's reason: a reversal posts its own mirrored entries, and those
// are what cancel the original. A history that dropped either would not sum to
// the balance it claims to explain, which would make every check built on it
// wrong in the one case an auditor cares about.
type AccountHistory struct {
	// Position is what was asked for: a whole account, or one subsidiary within a
	// control account. Over a control account the two are different documents —
	// the pool's movements, or one customer's statement — and the second is the
	// one a customer recognises.
	Position Position
	// Normal is the direction that INCREASES this account, taken off the
	// account's type. Every Movement and Running figure below is signed by it,
	// so a caller never has to know whether it is looking at an asset or a
	// liability.
	Normal Direction
	Rows   []HistoryRow
	// Closing is the balance after the last row, and it equals BookBalance for
	// the same account. It is carried rather than left to the caller to sum
	// because an empty history has one too, and it is zero.
	Closing Amount
}

// HistoryRow is one TRANSACTION's effect on one account.
//
// One row per transaction and not per entry, because a transaction is what a
// reader recognises and what an idempotency key names. A transaction with two
// entries on the same account is netted into one row: nothing in this system
// posts one, and a reader that had to sum two rows to learn what one event did
// would be reading the wrong shape.
//
// BookingDate and ValueDate are the TRANSACTION's. An entry carries a value date
// of its own and the two legs of one event may differ, which is exactly why
// value dating is SeriesTx's subject and not this one's: a row here describes an
// event, and an event has one booking date.
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
//
// It reads the account first, for BookBalanceTx's reason: which way an account's
// balance runs is a property of the account, and a caller supplying the
// direction would be asserting something it cannot check. An unknown account is
// ErrAccountNotFound rather than an empty history — a caller asking about an
// account that is not there has a bug, and answering "no movements" would hide
// it.
//
// There is no store method behind this and no schema change under it. It walks
// the transactions the store already lists for the account and sums in Go,
// because the running balance is a property of the ORDER and the store's
// aggregate has no way to express one.
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
			if e.Direction == hist.Normal {
				movement += e.Amount
			} else {
				movement -= e.Amount
			}
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
//
// A balance is one number and says nothing about time. An in-transit account
// holding a thousand is business as usual if it arrived this morning and a
// finding if it arrived in March, and only a decomposition into dated lots can
// tell the two apart. This is that decomposition's unit.
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
//
// The rule is FIFO: a movement against the balance discharges the oldest lots
// first. That is the conventional one, and it is also the one that reads
// correctly for the accounts this exists for — a settlement discharges the batch
// that opened before it, not the one that opened after.
//
// What FIFO buys is that a NETTED movement, which carries no identity of its
// own, still consumes something identifiable. A member bank's mirror leg covers
// a whole cut-off in one figure and names no payment, so nothing can attribute
// it; FIFO attributes it anyway, by the only fact available, which is order.
// What survives is therefore an honest answer about AGE and a weaker one about
// WHICH payment — and where the postings do carry an identity, the lot's
// Metadata carries it through, so the caller gets the stronger answer for free
// on the accounts that can support it.
//
// Lots sum to Balance. That is the invariant this type exists to keep, and it
// holds for a balance that has gone the other way too: a movement that outruns
// every lot in the queue opens a lot of the new sign rather than a negative one
// beside the old.
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
//
// It does no I/O and touches no store, which is the point: the FIFO rule is
// arithmetic over rows already read, so it is testable with no database at all
// and one read of the account answers both the questions this file exists for.
//
// asOf sets only the ages. It does NOT cut the history off — a caller asking
// about a balance as at a past date wants a value-dated read, and that is
// SeriesTx.
func (h AccountHistory) AgeAt(asOf time.Time) Ageing {
	out := Ageing{Position: h.Position, AsOf: asOf, Balance: h.Closing}
	day := DayStart(asOf)

	for _, r := range h.Rows {
		m := r.Movement
		// Consume the front of the queue while this movement runs against it.
		// Both guards are load-bearing: an exhausted queue leaves the remainder
		// to open a lot of the new sign, and a movement that has been fully
		// absorbed must stop, because zero is opposite in sign to everything.
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
