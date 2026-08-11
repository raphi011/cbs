package deposit

import (
	"context"
	"fmt"

	"github.com/raphi011/cbs/ledger"
)

// The book transfer: two of this bank's own customers, one posting, nobody told.
//
// It is the act payment.ErrOnUsPayment sends a caller to. Both accounts are in this
// register's book by construction — a register spans one — so no obligation
// between institutions exists, there is nothing for a clearing house to net,
// nothing for a settlement agent to move, and no statement that could tell this
// bank about a book it already holds.
//
// It is the REGISTER's act rather than the payment layer's, because nothing in
// it needs to know what an institution is: it touches the two customers' own
// money and no plumbing account of the bank's. payment.Network.DepositTx is
// the contrast that settles where the line falls — cash over the counter is up
// there because it debits VAULT CASH, which is the bank's own.
//
// Both legs name ONE chart-of-accounts row, one customer apart, so the bank's
// total customer deposits does not move — which is exactly what happens
// economically when money changes hands inside a bank and nothing leaves it.

// Transfer moves amount between two deposit accounts in this bank's book. See
// TransferTx, which is where the rules are.
func (r *Register) Transfer(ctx context.Context, from, to AccountID, amount ledger.Amount, description string) (ledger.Transaction, error) {
	var out ledger.Transaction
	err := r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.TransferTx(ctx, tx, from, to, amount, description)
		return err
	})
	if err != nil {
		return ledger.Transaction{}, err
	}
	return out, nil
}

// TransferTx is Transfer within a caller-supplied unit of work.
//
// # Money out is harder than money in
//
// The payer is put through CheckWithdrawalTx and the payee through
// CheckCreditTx, and the asymmetry between them is the whole of what a transfer
// enforces. A withdrawal requires an ACTIVE account, so a frozen or dormant
// payer is refused, and it is measured against the AVAILABLE balance — which
// already carries the overdraft limit in force today, so a transfer may
// legitimately push the payer overdrawn, exactly as a card capture may. A credit
// refuses only a Closed payee: money lands in a frozen account, and landing in a
// dormant one is what revives it.
//
// Both checks are in the same unit of work as the postings, for the reason
// payment.Network.DepositTx states: an account must not be able to close between
// being checked and being posted to.
func (r *Register) TransferTx(ctx context.Context, tx Tx, from, to AccountID, amount ledger.Amount, description string) (ledger.Transaction, error) {
	// A transfer to the account it came from would post a self-cancelling pair
	// and log that something happened. Refused before anything is read, because
	// it is a statement about the request rather than about either account.
	if from == to {
		return ledger.Transaction{}, ErrSameAccount
	}
	if amount <= 0 {
		return ledger.Transaction{}, ErrInvalidAmount
	}
	if err := ledger.ValidateText("description", description); err != nil {
		return ledger.Transaction{}, err
	}
	payer, err := tx.GetDepositAccount(ctx, r.bookID, from)
	if err != nil {
		return ledger.Transaction{}, err
	}
	payee, err := tx.GetDepositAccount(ctx, r.bookID, to)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if payer.Asset != payee.Asset {
		return ledger.Transaction{}, fmt.Errorf("%w: %s out of %s, %s into %s",
			ErrAssetMismatch, payer.Asset, from, payee.Asset, to)
	}
	if err := r.CheckWithdrawalTx(ctx, tx, from, amount); err != nil {
		return ledger.Transaction{}, err
	}
	if err := r.CheckCreditTx(ctx, tx, to); err != nil {
		return ledger.Transaction{}, err
	}

	// Resolved once, after the assets have been compared: two accounts in one
	// asset are two customers under one control account, and asking twice would
	// be asking the same question twice.
	control, err := r.depositControlTx(ctx, tx, payer.Asset)
	if err != nil {
		return ledger.Transaction{}, err
	}

	// PostTransactionTx and not PostTransaction: a plain call here would open a
	// second unit of work inside this one, which the store refuses outright.
	glTx, err := r.gl.PostTransactionTx(ctx, tx, ledger.PostTransactionRequest{
		Description: description,
		Entries: []ledger.Entry{
			{AccountID: control, Subsidiary: string(from), Amount: amount, Direction: ledger.Debit},
			{AccountID: control, Subsidiary: string(to), Amount: amount, Direction: ledger.Credit},
		},
	})
	if err != nil {
		return ledger.Transaction{}, err
	}

	// ONE event, keyed by the payer. A transfer is one act by one institution and
	// the payer is who asked for it; the payload names both sides, and the two
	// legs are already in the ledger under one transaction id. Compare
	// ReissueIdentifierTx, which writes TWO because a withdrawal and a mint are
	// two things that could have happened apart — these two cannot.
	if err := r.appendAuditTx(ctx, tx, ledger.EventTransferPosted, string(from), map[string]string{
		"from":           string(from),
		"to":             string(to),
		"asset":          string(payer.Asset),
		"amount":         fmt.Sprint(int64(amount)),
		"transaction_id": string(glTx.ID),
	}); err != nil {
		return ledger.Transaction{}, err
	}
	return glTx, nil
}
