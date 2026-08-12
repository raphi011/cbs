package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/store/sqlite"
	"github.com/raphi011/cbs/store/testenv"
)

// Two postings that cannot both spend the same balance, driven through
// ledger.Book on a FILE.
func TestConcurrentPostingsOnAFileReachTheDomainGuard(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.OpenBank(ctx, testenv.BankBook, filepath.Join(t.TempDir(), "postings.db"), frozen)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})

	book := ledger.NewBook(s.Ledger(), testenv.BankBook, frozen)
	cash, equity := fundedChart(t, book, 1_000)

	const hold = 50 * time.Millisecond
	const n = 2
	var opened sync.WaitGroup
	opened.Add(n)
	release := make(chan struct{})
	errs := make([]error, n)
	var done sync.WaitGroup

	for i := range n {
		done.Add(1)
		go func() {
			defer done.Done()
			first := true
			errs[i] = s.Ledger().Update(ctx, func(ctx context.Context, tx ledger.Tx) error {
				// Only the first attempt waits at the barrier. A retry must not,
				// or the retries wait for racers that have finished.
				if first {
					first = false
					opened.Done()
					<-release
				}
				_, err := book.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
					Description: "withdrawal",
					Entries: []ledger.Entry{
						{AccountID: equity, Amount: 600, Direction: ledger.Debit},
						{AccountID: cash, Amount: 600, Direction: ledger.Credit},
					},
				})
				if err != nil {
					return err
				}
				// The winner keeps the write lock, so the loser's first retry
				// reads a value that has not been committed yet.
				time.Sleep(hold)
				return nil
			})
		}()
	}
	opened.Wait()
	close(release)
	done.Wait()

	winners := 0
	for i, err := range errs {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ledger.ErrInsufficientBalance):
		default:
			t.Errorf("racer %d lost for the wrong reason: %v — the caller is owed the domain's refusal, not a lock error", i, err)
		}
	}
	if winners != 1 {
		t.Errorf("winners = %d, want exactly 1", winners)
	}

	balance, err := book.BookBalance(ctx, cash.Total())
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if balance != 400 {
		t.Errorf("cash balance after the race = %d, want 400", balance)
	}
}

// fundedChart builds a minimal chart of accounts and funds the cash account,
// returning the Asset account under test and an Equity counterparty. Equity is
// not balance-checked, so it can absorb any leg the test needs.
func fundedChart(t *testing.T, book *ledger.Book, amount ledger.Amount) (cash, equity ledger.AccountID) {
	t.Helper()
	ctx := context.Background()

	gl, err := book.CreateLedger(ctx, "General Ledger")
	mustNot(t, err)
	assets, err := book.CreateSubledger(ctx, gl.ID, "Bank Assets")
	mustNot(t, err)
	capital, err := book.CreateSubledger(ctx, gl.ID, "Capital")
	mustNot(t, err)

	cashAcct, err := book.CreateAccount(ctx, assets.ID, "Cash", ledger.Asset, "EUR")
	mustNot(t, err)
	equityAcct, err := book.CreateAccount(ctx, capital.ID, "Share Capital", ledger.Equity, "EUR")
	mustNot(t, err)

	_, err = book.PostTransaction(ctx, ledger.PostTransactionRequest{
		Description: "opening capital",
		Entries: []ledger.Entry{
			{AccountID: cashAcct.ID, Amount: amount, Direction: ledger.Debit},
			{AccountID: equityAcct.ID, Amount: amount, Direction: ledger.Credit},
		},
	})
	mustNot(t, err)

	return cashAcct.ID, equityAcct.ID
}

func mustNot(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
