package deposit

import (
	"context"

	"github.com/raphi011/cbs/ledger"
)

// The deposit package's own tests live in package deposit_test, because they
// build a Register over a store from store/testenv, which reaches store/sqlite,
// which imports deposit — an in-package test file importing it would be an
// import cycle.

// Book exposes the general ledger the register is layered on, so tests can fund
// a deposit account and inspect the chart of accounts directly.
func (r *Register) Book() *ledger.Book { return r.gl }

// Receivable is where an account's accrued interest sits: the bank's
// accrued-interest control account, under this account's id.
func (r *Register) Receivable(ctx context.Context, id AccountID) (ledger.Position, error) {
	var out ledger.Position
	err := r.store.View(ctx, func(ctx context.Context, tx Tx) error {
		acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
		if err != nil {
			return err
		}
		at, err := r.interestAccountsTx(ctx, tx, acct)
		out = at.Receivable
		return err
	})
	return out, err
}
