package deposit

import (
	"context"
	"fmt"

	"github.com/raphi011/cbs/internal/unit"
	"github.com/raphi011/cbs/ledger"
)

// The book transfer: two of this bank's own customers, one posting, nobody
// told.

// Transfer moves amount between two deposit accounts in this bank's book. See
// TransferTx, which is where the rules are.
func (r *Register) Transfer(ctx context.Context, from, to AccountID, amount ledger.Amount, description string) (ledger.Transaction, error) {
	return unit.Run(ctx, r.store.Update, func(ctx context.Context, tx Tx) (ledger.Transaction, error) {
		return r.TransferTx(ctx, tx, from, to, amount, description)
	})
}

// TransferTx is Transfer within a caller-supplied unit of work.
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
	// legs are already in the ledger under one transaction id.
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
