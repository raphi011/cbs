package payment

import (
	"context"
	"fmt"
	"time"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/lending"
	"github.com/raphi011/cbs/product"
)

// BankAccounts are the internal accounts a bank needs for one asset.
type BankAccounts struct {
	Suspense ledger.AccountID
	Reserve  ledger.AccountID

	// Unclaimed is where a credit goes when the payee's account will not take it —
	// closed, and therefore terminal — or when this bank cannot find the payee at
	// all behind the address a settled instruction quoted.
	Unclaimed ledger.AccountID

	// ReturnsReceivable is money the bank has paid out on a scheme obligation it
	// could not fund from the customer it belongs to. An Asset — the bank is owed
	// this — and the mirror image of Unclaimed, which is owed BY the bank.
	ReturnsReceivable ledger.AccountID

	// VaultCash is the cash this bank is holding: notes in a drawer, and the asset
	// side of a deposit paid in over the counter.
	VaultCash ledger.AccountID

	Settlement ledger.AccountID
}

// Bank is one bank's own record of itself: its book, its chart of accounts, and
// the product it opens customer accounts from.
type Bank struct {
	ID   ParticipantID
	Name string

	// BIC is this bank's ISO 9362 business identifier code: what a counterparty
	// addresses it by, and what names its download queue.
	BIC iso20022.BIC

	// Issuer is the country and bank code this bank mints its customers' addresses
	// under, and it is a SECOND identifier that has nothing to do with the BIC
	// above.
	Issuer iban.Issuer

	// BookID is this bank's book within the network's store. It is
	// ledger.BookID(ID) — the bank is its own book.
	BookID ledger.BookID

	CustomerSubledger ledger.SubledgerID

	// ProductID is the catalogue entry OpenCustomerAccount opens accounts from,
	// configured per bank exactly as CustomerSubledger is.
	ProductID product.ID

	// AdmissionRef is the reference of the admission this bank recorded a
	// membership under: what it accepted, not what anybody else says about it.
	AdmissionRef string

	// Assets holds one set of internal accounts per asset the bank operates in,
	// keyed by asset code.
	Assets map[ledger.AssetCode]BankAccounts

	CreatedAt time.Time

	// Ledger, Deposit and Lending are live handles bound to BookID over the
	// network's store.
	Ledger  *ledger.Book      `json:"-"`
	Deposit *deposit.Register `json:"-"`

	// Lending is a live handle like Ledger and Deposit, bound by Network.bind.
	Lending *lending.Portfolio `json:"-"`

	// Catalogue is a live handle like Ledger, Deposit and Lending, bound by
	// Network.bind. It is this bank's product catalogue: products are
	// book-scoped, so the same ID in two banks is two products.
	Catalogue *product.Catalogue `json:"-"`

	// store is this bank's own store, bound with the handles above, and it is what
	// makes this type the one that can COMPOSE them: a BankTx spans the ledger,
	// the deposit register and the lending portfolio at once, where each handle's
	// own store reaches one layer.
	store BankStore
}

// AccountsFor returns the bank's internal accounts for an asset.
func (b *Bank) AccountsFor(asset ledger.AssetCode) (BankAccounts, error) {
	accts, ok := b.Assets[asset]
	if !ok {
		return BankAccounts{}, fmt.Errorf("%w: %s in %s", ErrParticipantAssetNotFound, asset, b.Name)
	}
	return accts, nil
}

// OpenCustomerAccount opens a new customer deposit account at the bank,
// denominated in asset.
func (b *Bank) OpenCustomerAccount(ctx context.Context, name string, asset ledger.AssetCode) (deposit.Account, error) {
	return b.Deposit.OpenAccount(ctx, name, asset, b.ProductID, 0)
}

// RunEndOfDay runs this bank's end-of-day batches for one business date: the
// deposit layer accrues overdraft interest, the lending layer accrues facility
// interest and recomputes arrears.
func (b *Bank) RunEndOfDay(ctx context.Context, date time.Time) error {
	return b.store.Update(ctx, func(ctx context.Context, tx BankTx) error {
		if err := b.Deposit.RunEndOfDayTx(ctx, tx, date); err != nil {
			return err
		}
		return b.Lending.RunEndOfDayTx(ctx, tx, date)
	})
}

// Repay applies a repayment to one of this bank's facilities out of one of its
// customers' deposit accounts.
func (b *Bank) Repay(ctx context.Context, id lending.FacilityID, from deposit.AccountID,
	amount ledger.Amount, date time.Time, description string,
) (ledger.Transaction, error) {
	var out ledger.Transaction
	err := b.store.Update(ctx, func(ctx context.Context, tx BankTx) error {
		acct, err := tx.GetDepositAccount(ctx, b.BookID, from)
		if err != nil {
			return err
		}
		if err := b.Deposit.CheckWithdrawalTx(ctx, tx, acct.ID, amount); err != nil {
			return err
		}
		pos, err := b.Deposit.PositionTx(ctx, tx, acct.ID)
		if err != nil {
			return err
		}
		out, err = b.Lending.RepayTx(ctx, tx, id, pos, amount, date, description)
		return err
	})
	return out, err
}

// positionTx resolves a customer deposit account ID to the position its money
// occupies in this bank's ledger — the customer-deposit control account for its
// asset, under the account's own id — within a caller-supplied unit of work.
func (b *Bank) positionTx(ctx context.Context, tx deposit.Tx, id deposit.AccountID) (ledger.Position, error) {
	pos, err := b.Deposit.PositionTx(ctx, tx, id)
	if err != nil {
		return ledger.Position{}, ErrAccountNotInParticipant
	}
	return pos, nil
}
