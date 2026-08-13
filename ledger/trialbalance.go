package ledger

import (
	"context"
	"time"

	"github.com/raphi011/cbs/internal/unit"
)

// ---------------------------------------------------------------------------
// The trial balance
// ---------------------------------------------------------------------------

// TrialBalance is every account's balance at a point in time, in the classic
// two-column form: each account on the side its balance falls, and the two
// columns equal WITHIN EACH ASSET.
type TrialBalance struct {
	AsOf time.Time

	// Rows is one per account, in the store's account listing order.
	Rows []TrialBalanceRow

	// Totals is one per asset, in the order the assets are first met walking
	// Rows — so a report prints the same asset first every time.
	Totals []TrialBalanceTotal
}

// TrialBalanceRow is one account's balance, restated onto the side it falls.
type TrialBalanceRow struct {
	Account AccountID
	Name    string
	Type    AccountType
	Asset   AssetCode

	// Control says this line stands for many subsidiaries rather than one
	// position.
	Control bool

	Debits  Amount
	Credits Amount

	// InFlight is the part of this balance that has been recorded and has NOT yet
	// taken economic effect by AsOf: the book balance less the value-dated one, in
	// the same debit-positive convention as the columns.
	InFlight Amount
}

// TrialBalanceTotal is one asset's two columns, and the pair that must agree.
type TrialBalanceTotal struct {
	Asset   AssetCode
	Debits  Amount
	Credits Amount

	// InFlight is the sum of the rows', and it is the figure this report exists to
	// make visible: exactly the amount by which a VALUE-DATED restatement of the
	// two columns above would fail to balance in this asset.
	InFlight Amount
}

// Balanced reports whether every asset's two columns agree.
func (b TrialBalance) Balanced() bool {
	for _, t := range b.Totals {
		if t.Debits != t.Credits {
			return false
		}
	}
	return true
}

// TrialBalance is TrialBalanceTx in its own read-only unit of work.
func (s *Book) TrialBalance(ctx context.Context, asOf time.Time) (TrialBalance, error) {
	return unit.Run(ctx, s.store.View, func(ctx context.Context, tx Tx) (TrialBalance, error) {
		return s.TrialBalanceTx(ctx, tx, asOf)
	})
}

// TrialBalanceTx is TrialBalance within a caller-supplied unit of work.
func (s *Book) TrialBalanceTx(ctx context.Context, tx Tx, asOf time.Time) (TrialBalance, error) {
	accounts, err := tx.ListAccounts(ctx, s.id)
	if err != nil {
		return TrialBalance{}, err
	}

	out := TrialBalance{AsOf: asOf, Rows: make([]TrialBalanceRow, 0, len(accounts))}
	// at indexes Totals by asset, so the slice keeps first-appearance order
	// rather than a map's.
	at := make(map[AssetCode]int, 2)
	before := NextDay(asOf)

	for _, a := range accounts {
		normal := a.Type.NormalBalance()
		book, err := BookBalance(ctx, tx, s.id, a.ID.Total(), normal)
		if err != nil {
			return TrialBalance{}, err
		}
		valueDated, err := ValueDateBalance(ctx, tx, s.id, a.ID.Total(), normal, before)
		if err != nil {
			return TrialBalance{}, err
		}

		// Restate onto the debit-positive convention every column here shares:
		// a credit-normal account's positive balance is a credit.
		debitPositive, inFlight := book, book-valueDated
		if normal == Credit {
			debitPositive, inFlight = -debitPositive, -inFlight
		}

		row := TrialBalanceRow{
			Account: a.ID, Name: a.Name, Type: a.Type, Asset: a.Asset,
			Control: a.Control, InFlight: inFlight,
		}
		if debitPositive >= 0 {
			row.Debits = debitPositive
		} else {
			row.Credits = -debitPositive
		}
		out.Rows = append(out.Rows, row)

		i, seen := at[a.Asset]
		if !seen {
			i = len(out.Totals)
			at[a.Asset] = i
			out.Totals = append(out.Totals, TrialBalanceTotal{Asset: a.Asset})
		}
		out.Totals[i].Debits += row.Debits
		out.Totals[i].Credits += row.Credits
		out.Totals[i].InFlight += inFlight
	}
	return out, nil
}
