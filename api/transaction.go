package api

import (
	"context"

	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
)

// Rendering a posting takes a read, and this file is where that read lives.
//
// An entry carries no asset of its own — see ToTransactionDTO — so putting one
// on the wire means resolving the account it posts to. That is I/O, and the
// dto_*.go files hold none: a To…DTO is a pure function of the values handed
// to it, which is what lets a listing render without a round trip per row.
//
// Every route that answers with a posting ends here rather than resolving its
// own assets, so the one-read-per-response property below is the package's
// rather than each handler's.

// entryAccountIDs collects the distinct account IDs referenced by any entry
// across one or more transactions, in first-seen order.
func entryAccountIDs(txs []ledger.Transaction) []ledger.AccountID {
	seen := make(map[ledger.AccountID]bool)
	var ids []ledger.AccountID
	for _, tx := range txs {
		for _, e := range tx.Entries {
			if !seen[e.AccountID] {
				seen[e.AccountID] = true
				ids = append(ids, e.AccountID)
			}
		}
	}
	return ids
}

// entryAssets resolves the asset of every account referenced by any entry across
// txs, in one Book.GetAccounts call. One Book.GetAccount per entry would be one
// BEGIN…COMMIT each, making a listing's cost scale with how many transactions it
// renders rather than with how much work it does.
func entryAssets(ctx context.Context, lb *ledger.Book, txs []ledger.Transaction) (map[ledger.AccountID]ledger.AssetCode, error) {
	ids := entryAccountIDs(txs)
	accts, err := lb.GetAccounts(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[ledger.AccountID]ledger.AssetCode, len(accts))
	for id, a := range accts {
		out[id] = a.Asset
	}
	return out, nil
}

// ResolveTransactionDTO renders one posting.
func ResolveTransactionDTO(ctx context.Context, lb *ledger.Book, tx ledger.Transaction) (TransactionDTO, error) {
	assets, err := entryAssets(ctx, lb, []ledger.Transaction{tx})
	if err != nil {
		return TransactionDTO{}, err
	}
	return ToTransactionDTO(tx, assets), nil
}

// ResolveTransactionDTOs renders a listing, resolving every account any of them
// names in ONE read — which is the whole reason this takes a slice rather than
// being called per row.
func ResolveTransactionDTOs(ctx context.Context, lb *ledger.Book, txs []ledger.Transaction) ([]TransactionDTO, error) {
	assets, err := entryAssets(ctx, lb, txs)
	if err != nil {
		return nil, err
	}
	out := make([]TransactionDTO, len(txs))
	for i, tx := range txs {
		out[i] = ToTransactionDTO(tx, assets)
	}
	return out, nil
}

// ResolveChargeDTO renders a billed cycle, and reads only when the cycle posted
// something: a cycle can bill an instalment without posting at all, and
// resolving assets for a transaction that is not there would be a store round
// trip to answer a question nothing asked. See ChargeDTO.
func ResolveChargeDTO(ctx context.Context, lb *ledger.Book, c lending.Charge) (ChargeDTO, error) {
	var assets map[ledger.AccountID]ledger.AssetCode
	if c.Posted() {
		var err error
		assets, err = entryAssets(ctx, lb, []ledger.Transaction{c.Transaction})
		if err != nil {
			return ChargeDTO{}, err
		}
	}
	return ToChargeDTO(c, assets), nil
}
