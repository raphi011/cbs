package ledger

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// The trial balance
// ---------------------------------------------------------------------------

// TrialBalance is every account's balance at a point in time, in the classic
// two-column form: each account on the side its balance falls, and the two
// columns equal WITHIN EACH ASSET.
//
// # It cannot fail arithmetically, which is why it is worth computing
//
// Every posting through this package is refused unless its debits equal its
// credits per asset, so a sum over the accounts they landed in cannot come out
// any other way. That makes this a control on the PIPELINE rather than on the
// arithmetic: what it would catch is a row written straight into the store, a
// fixture that bypassed PostTransaction, or a balance query that grew a
// predicate it should not have. Each of those is silent today.
//
// # Per asset, because a single total would balance by accident
//
// An Amount is an integer in its asset's minor units and nothing else, so a
// grand total across a multi-asset book is satisfied whenever the integers
// match — a hundred million euro against a hundred BTC nets to zero. Balance is
// checked per asset when a transaction posts, and it is reported per asset
// here, for the same reason and with the same absence of any exchange rate.
//
// # The columns are BOOK balances, and that is load-bearing
//
// A value-dated trial balance would not balance, and the reason is not a defect:
// the two legs of one event may legitimately take economic effect on different
// days — an outbound transfer debits the payer today while its clearing leg
// settles later. Only what the book has RECORDED is guaranteed to balance. What
// AsOf decides is InFlight, which measures exactly that divergence rather than
// hiding it in a total that no longer adds up.
type TrialBalance struct {
	AsOf time.Time

	// Rows is one per account, in the store's account listing order.
	Rows []TrialBalanceRow

	// Totals is one per asset, in the order the assets are first met walking
	// Rows — so a report prints the same asset first every time.
	Totals []TrialBalanceTotal
}

// TrialBalanceRow is one account's balance, restated onto the side it falls.
//
// Debits and Credits are never both non-zero: an account has one balance, and
// which column it prints in is what the statement says about it. An account
// with no entries prints as zero in both, and it is still listed — a chart of
// accounts line that has never been posted to is a fact about the chart.
type TrialBalanceRow struct {
	Account AccountID
	Name    string
	Type    AccountType
	Asset   AssetCode

	// Control says this line stands for many obligors rather than one position.
	// It is what makes the row count here a property of the INSTITUTION rather
	// than of its customer base, and it is carried so a reader can see which
	// lines those are without joining anything.
	Control bool

	Debits  Amount
	Credits Amount

	// InFlight is the part of this balance that has been recorded and has NOT
	// yet taken economic effect by AsOf: the book balance less the value-dated
	// one, in the same debit-positive convention as the columns. Positive means
	// the book stands further to the debit side than the value dates justify.
	InFlight Amount
}

// TrialBalanceTotal is one asset's two columns, and the pair that must agree.
type TrialBalanceTotal struct {
	Asset   AssetCode
	Debits  Amount
	Credits Amount

	// InFlight is the sum of the rows', and it is the figure this report exists
	// to make visible: exactly the amount by which a VALUE-DATED restatement of
	// the two columns above would fail to balance in this asset.
	//
	// It is not necessarily zero, and that is the whole point. A transaction's
	// legs balance, but they may take economic effect on different days — an
	// outbound transfer debits the payer today and settles its clearing leg
	// later — so at any AsOf between the two the value-dated view is genuinely
	// lopsided. Zero here means every event whose legs have landed has landed
	// them together; a non-zero figure names the gap rather than letting a
	// value-dated total quietly not add up.
	InFlight Amount
}

// Balanced reports whether every asset's two columns agree.
//
// An empty trial balance is balanced, and correctly so: a book with no accounts
// has nothing out of place.
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
	var out TrialBalance
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.TrialBalanceTx(ctx, tx, asOf)
		return err
	})
	return out, err
}

// TrialBalanceTx is TrialBalance within a caller-supplied unit of work.
//
// There is no store method behind this and no schema change under it: it walks
// the accounts the store already lists and asks for each one's balance, all
// inside the ONE unit of work the caller supplies, so the whole report is taken
// against a single consistent view of the book. A version that opened a View
// per account could read half a transaction.
//
// Every account is read WHOLE — a control account contributes its pool and not
// its obligors — which is what a chart of accounts is a listing of. One
// customer's share of a control line is BookBalanceTx over that obligor's
// position, and it is a different document.
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
		book, err := tx.BookBalance(ctx, s.id, a.ID.Total(), normal)
		if err != nil {
			return TrialBalance{}, err
		}
		valueDated, err := tx.ValueDateBalance(ctx, s.id, a.ID.Total(), normal, before)
		if err != nil {
			return TrialBalance{}, err
		}

		// Restate onto the debit-positive convention every column here shares:
		// a credit-normal account's positive balance is a credit.
		signed, inFlight := book, book-valueDated
		if normal == Credit {
			signed, inFlight = -signed, -inFlight
		}

		row := TrialBalanceRow{
			Account: a.ID, Name: a.Name, Type: a.Type, Asset: a.Asset,
			Control: a.Control, InFlight: inFlight,
		}
		if signed >= 0 {
			row.Debits = signed
		} else {
			row.Credits = -signed
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
