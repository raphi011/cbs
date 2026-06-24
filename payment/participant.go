package payment

import (
	"github.com/raphi011/cbs"
	"github.com/raphi011/cbs/deposit"
)

// Participant is a bank (or payment service provider) that takes part in the
// clearing and settlement system.
//
// Each participant keeps its OWN general ledger (Ledger) and a deposit layer
// (Deposit) over it. Banks only meet at the central bank, where each holds a
// reserve account. This mirrors reality and is what makes the distinction
// between clearing (exchanging instructions) and settlement (moving
// central-bank reserves) concrete.
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
	ID      ParticipantID
	Name    string
	Ledger  *ledger.Book
	Deposit *deposit.Register

	CustomerSubledger ledger.SubledgerID
	SuspenseAccount   ledger.AccountID // "Clearing Suspense" (Liability)
	ReserveAccount    ledger.AccountID // "Reserve at Central Bank" (Asset)

	// SettlementAccount is this participant's reserve account in the
	// central-bank ledger (the central bank's "vostro" view of the bank).
	SettlementAccount ledger.AccountID
}

// OpenCustomerAccount opens a new customer deposit account at the bank.
//
// Customer deposits are demand-deposit accounts managed by the bank's deposit
// layer; each is backed by a Liability GL account, since the money belongs to
// the customer and the bank owes it to them. The account is opened with no
// overdraft.
func (p *Participant) OpenCustomerAccount(name string) (deposit.Account, error) {
	return p.Deposit.OpenAccount(p.CustomerSubledger, name, 0)
}

// glAccount resolves a customer deposit account ID to the backing GL account
// ID used for ledger postings. It returns ErrAccountNotInParticipant if the
// deposit account does not exist at this participant.
func (p *Participant) glAccount(id deposit.AccountID) (ledger.AccountID, error) {
	acct, err := p.Deposit.GetAccount(id)
	if err != nil {
		return "", ErrAccountNotInParticipant
	}
	return acct.GLAccount, nil
}
