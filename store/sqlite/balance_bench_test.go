package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// What SQLite's aggregate buys over the fold that replaced it, in three arms:
// the deleted SQL, the shipped fold, and a two-column scan that shows how much
// of the fold's cost is the columns an Entry carries.

const benchBook ledger.BookID = "book_bench"

const (
	benchAccount ledger.AccountID = "901.001.001"
	benchOther   ledger.AccountID = "901.001.002"
)

// benchStore is a bank's database on disk holding n transactions against
// benchAccount, one entry each side.
func benchStore(b *testing.B, n int) *BankStore {
	b.Helper()
	ctx := context.Background()
	clock := func() time.Time { return time.Unix(0, 0).UTC() }

	s, err := OpenBank(ctx, benchBook, filepath.Join(b.TempDir(), "bench.db"), clock)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() {
		if err := s.Close(); err != nil {
			b.Errorf("close: %v", err)
		}
	})

	// One unit of work for the whole fixture: the cost being measured is the
	// read, and n separate commits would spend the setup in fsync.
	err = s.Update(ctx, func(ctx context.Context, tx payment.BankTx) error {
		for i := range n {
			day := time.Unix(0, 0).UTC().AddDate(0, 0, i%365)
			id := fmt.Sprintf("%06d", i)
			if err := tx.PutTransaction(ctx, benchBook, ledger.Transaction{
				ID:        ledger.TransactionID("txn_" + id),
				ValueDate: day,
				Entries: []ledger.Entry{
					{ID: ledger.EntryID("ent_d" + id), AccountID: benchAccount, Amount: 100, Direction: ledger.Debit, ValueDate: day},
					{ID: ledger.EntryID("ent_c" + id), AccountID: benchOther, Amount: 100, Direction: ledger.Credit, ValueDate: day},
				},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.Fatalf("fixture: %v", err)
	}
	return s
}

// sqlSumBalance is the aggregate ledger.BookBalance replaced, kept here so the
// comparison has two sides. The store no longer holds it: the seam yields
// entries and the domain folds them.
func sqlSumBalance(ctx context.Context, t *tx, book ledger.BookID, account ledger.AccountID,
	normal ledger.Direction,
) (ledger.Amount, error) {
	var balance ledger.Amount
	err := t.tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN e.direction = ? THEN e.amount ELSE -e.amount END), 0)
		FROM entries e WHERE e.book_id = ? AND e.account_id = ?`,
		int64(normal), string(book), string(account)).Scan(&balance)
	return balance, err
}

// goScanBalance is the sum written against the raw rows, kept beside the domain
// fold so the benchmark says what the iterator itself costs.
func goScanBalance(ctx context.Context, t *tx, book ledger.BookID, account ledger.AccountID,
	normal ledger.Direction,
) (ledger.Amount, error) {
	rows, err := t.tx.QueryContext(ctx, `
		SELECT e.direction, e.amount
		FROM entries e WHERE e.book_id = ? AND e.account_id = ?`,
		string(book), string(account))
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var balance ledger.Amount
	for rows.Next() {
		var (
			direction ledger.Direction
			amount    ledger.Amount
		)
		if err := rows.Scan(&direction, &amount); err != nil {
			return 0, err
		}
		if direction == normal {
			balance += amount
		} else {
			balance -= amount
		}
	}
	return balance, rows.Err()
}

func BenchmarkBookBalance(b *testing.B) {
	for _, n := range []int{100, 1_000, 10_000, 100_000} {
		s := benchStore(b, n)
		ctx := context.Background()

		// All three answers first, so a timing comparison cannot be between one
		// balance and something that is not one.
		if err := s.View(ctx, func(ctx context.Context, t payment.BankTx) error {
			folded, err := ledger.BookBalance(ctx, t, benchBook, ledger.Position{Account: benchAccount}, ledger.Debit)
			if err != nil {
				return err
			}
			aggregate, err := sqlSumBalance(ctx, t.(*tx), benchBook, benchAccount, ledger.Debit)
			if err != nil {
				return err
			}
			scanned, err := goScanBalance(ctx, t.(*tx), benchBook, benchAccount, ledger.Debit)
			if err != nil {
				return err
			}
			if folded != aggregate || folded != scanned || folded != ledger.Amount(100*n) {
				b.Fatalf("n=%d: fold %d, aggregate %d, scan %d", n, folded, aggregate, scanned)
			}
			return nil
		}); err != nil {
			b.Fatal(err)
		}

		b.Run(fmt.Sprintf("sql/%d", n), func(b *testing.B) {
			for b.Loop() {
				if err := s.View(ctx, func(ctx context.Context, t payment.BankTx) error {
					_, err := sqlSumBalance(ctx, t.(*tx), benchBook, benchAccount, ledger.Debit)
					return err
				}); err != nil {
					b.Fatal(err)
				}
			}
		})

		b.Run(fmt.Sprintf("fold/%d", n), func(b *testing.B) {
			for b.Loop() {
				if err := s.View(ctx, func(ctx context.Context, t payment.BankTx) error {
					_, err := ledger.BookBalance(ctx, t, benchBook, ledger.Position{Account: benchAccount}, ledger.Debit)
					return err
				}); err != nil {
					b.Fatal(err)
				}
			}
		})

		b.Run(fmt.Sprintf("goscan/%d", n), func(b *testing.B) {
			for b.Loop() {
				if err := s.View(ctx, func(ctx context.Context, t payment.BankTx) error {
					_, err := goScanBalance(ctx, t.(*tx), benchBook, benchAccount, ledger.Debit)
					return err
				}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
