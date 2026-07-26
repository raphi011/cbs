package payment

import (
	"context"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ledger"
)

// Participant is a bank (or payment service provider) that takes part in the
// clearing and settlement system.
//
// Each participant keeps its OWN book of accounts (Ledger) and a deposit layer
// (Deposit) over it. Banks only meet at the central bank, where each holds a
// reserve account. This mirrors reality and is what makes the distinction
// between clearing (exchanging instructions) and settlement (moving
// central-bank reserves) concrete.
//
// The books all live in the network's single store, told apart by BookID —
// which is what lets one transaction span several banks — but chart-of-accounts
// numbers, ID counters and idempotency keys are per book, so each bank still
// numbers its accounts independently.
//
// The internal accounts each participant needs:
//
//   - Customer deposit accounts: demand-deposit accounts managed by the
//     Deposit register, each backed by a Liability GL account. Opened via
//     OpenCustomerAccount.
//   - Clearing Suspense (Liability): an in-transit account holding funds
//     that have left a customer but not yet settled between banks. It
//     returns to zero once a cycle settles.
//   - Reserve at Central Bank (Asset): the bank's claim on the central bank.
//     It mirrors the bank's reserve account in the central-bank ledger and
//     moves only at settlement.
type Participant struct {
	ID   ParticipantID
	Name string

	// BookID is this bank's book within the network's store. It is
	// ledger.BookID(ID) — the participant is its own book.
	BookID ledger.BookID

	CustomerSubledger ledger.SubledgerID
	SuspenseAccount   ledger.AccountID // "Clearing Suspense" (Liability)
	ReserveAccount    ledger.AccountID // "Reserve at Central Bank" (Asset)

	// SettlementAccount is this participant's reserve account in the
	// central-bank ledger (the central bank's "vostro" view of the bank).
	SettlementAccount ledger.AccountID

	CreatedAt time.Time

	// Ledger and Deposit are live handles bound to BookID over the network's
	// store. They are NOT data and are not persisted: a Store returns a
	// Participant with both nil, and the Network binds them on the way out (see
	// Network.bind). Treat them as derived, exactly like a database row's
	// association objects.
	//
	// json:"-" for the same reason: the participant.added audit payload is a
	// snapshot of the stored row, and a handle is neither data nor meaningful
	// once serialized.
	Ledger  *ledger.Book      `json:"-"`
	Deposit *deposit.Register `json:"-"`
}

// OpenCustomerAccount opens a new customer deposit account at the bank.
//
// Customer deposits are demand-deposit accounts managed by the bank's deposit
// layer; each is backed by a Liability GL account, since the money belongs to
// the customer and the bank owes it to them. The account is opened with no
// overdraft.
//
// This opens its own unit of work, so it must not be called from inside one.
// A caller already holding a Tx should drive p.Deposit.OpenAccountTx instead —
// which is also how to open an account with an overdraft limit.
func (p *Participant) OpenCustomerAccount(ctx context.Context, name string) (deposit.Account, error) {
	return p.Deposit.OpenAccount(ctx, p.CustomerSubledger, name, 0)
}

// glAccountTx resolves a customer deposit account ID to the backing GL account
// ID used for ledger postings, within a caller-supplied unit of work. It returns
// ErrAccountNotInParticipant if the deposit account does not exist at this
// participant.
func (p *Participant) glAccountTx(ctx context.Context, tx Tx, id deposit.AccountID) (ledger.AccountID, error) {
	acct, err := tx.GetDepositAccount(ctx, p.BookID, id)
	if err != nil {
		return "", ErrAccountNotInParticipant
	}
	return acct.GLAccount, nil
}
