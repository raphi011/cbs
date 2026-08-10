package ledger

import (
	"context"
	"errors"
	"fmt"
)

// A slot is the role an account plays in a posting flow — the line a customer's
// money pools in, the receivable an accrual debits, the revenue it credits —
// and this file is what turns "which account does this flow post to?" from a
// string literal in a domain package into a row.
//
// # Why it is here rather than in deposit or lending
//
// Both of those layers ask the question, neither may import the other, and the
// answer is a fact about the chart of accounts, which is this package's. Naming
// the same account by the same literal in two packages is how one line ends up
// with two spellings; naming it by slot means the two either resolve the same
// row or visibly do not.
//
// This package still learns nothing about products. Product is an opaque string
// supplied by the layer above, exactly as Entry.Subsidiary is.

// Slot is a role in a posting flow, and the constraints the account filling it
// must satisfy. It is declared as a value in the package that owns the flow, so
// there is no registry to keep in step and no slot that means one thing on write
// and another on read.
//
// Type and Control are what MapSlot validates against: a slot filled by a
// Revenue account where an Asset was meant would post the right amount in the
// wrong direction on the balance sheet, and one filled by a plain account where
// a control account was meant would be refused at every posting afterwards
// rather than at the write that caused it.
type Slot struct {
	// Key identifies the slot in storage. It is namespaced by the layer that
	// declares it — "deposit.principal", "lending.interest_income" — because
	// two layers naming one role differently is exactly what a shared table
	// makes visible.
	Key string

	// Type is the account type the slot requires.
	Type AccountType

	// Control says the slot requires an account that pools obligors.
	Control bool

	// ByProduct says a product may override the bank-wide row for this slot.
	//
	// A slot that HOLDS A BALANCE never may, and that is the whole of this
	// field. Money already posted under an obligor stays in the line it was
	// posted to; if a later resolution answered with a different line, the
	// customer's balance would be the second half only and the first half would
	// be stranded where nothing reads it. Moving a balance between control
	// accounts is a reclassification journal, which this system does not have.
	//
	// An income or expense slot carries no balance anybody is owed, so a
	// product may earn into its own revenue line without anything being left
	// behind.
	ByProduct bool
}

// SlotAccount is one row of the mapping: which account a (product, slot, asset)
// triple posts to.
//
// Product is empty on the bank-wide row, which is the one every flow resolves
// unless a product-scoped row overrides it. It is not a foreign key and this
// package does not resolve it — see the file comment.
type SlotAccount struct {
	Product string
	Slot    string
	Asset   AssetCode
	Account AccountID
}

// MapSlot points a slot at an account, or repoints it. See MapSlotTx.
func (s *Book) MapSlot(ctx context.Context, product string, slot Slot, asset AssetCode, account AccountID) error {
	return s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		return s.MapSlotTx(ctx, tx, product, slot, asset, account)
	})
}

// MapSlotTx is MapSlot within a caller-supplied unit of work.
//
// The account must exist, be denominated in asset, and match the slot's Type and
// Control. All four are checked HERE, at the write, because a mapping is
// configuration and the alternative is a posting that fails — or worse, does
// not — long afterwards, at a moment nobody connects to the change that caused
// it. That is the new failure class this table introduces, and this is what is
// done about it.
//
// product must be empty unless the slot is ByProduct; see that field for what a
// product-scoped balance line would strand.
//
// Returns ErrAccountNotFound, ErrSlotAccountMismatch and ErrSlotNotProductScoped.
func (s *Book) MapSlotTx(ctx context.Context, tx Tx, product string, slot Slot, asset AssetCode, account AccountID) error {
	if err := ValidateText("slot", slot.Key); err != nil {
		return err
	}
	if err := ValidateText("asset", string(asset)); err != nil {
		return err
	}
	if product != "" {
		if err := ValidateText("product", product); err != nil {
			return err
		}
		if !slot.ByProduct {
			return fmt.Errorf("%w: %s", ErrSlotNotProductScoped, slot.Key)
		}
	}
	acct, err := tx.GetAccount(ctx, s.id, account)
	if err != nil {
		return err
	}
	switch {
	case acct.Asset != asset:
		return fmt.Errorf("%w: %s is in %s, not %s", ErrSlotAccountMismatch, account, acct.Asset, asset)
	case acct.Type != slot.Type:
		return fmt.Errorf("%w: %s wants %s, %s is %s", ErrSlotAccountMismatch, slot.Key, slot.Type, account, acct.Type)
	case acct.Control != slot.Control:
		return fmt.Errorf("%w: %s wants control=%t, %s has control=%t",
			ErrSlotAccountMismatch, slot.Key, slot.Control, account, acct.Control)
	}
	row := SlotAccount{Product: product, Slot: slot.Key, Asset: asset, Account: account}
	if err := tx.PutSlotAccount(ctx, s.id, row); err != nil {
		return err
	}
	return s.appendAuditTx(ctx, tx, ScopeLedger, EventSlotMapped, slot.Key, row)
}

// SlotAccountTx resolves which account a flow posts to.
//
// product is the product the flow is being run for, and "" is legitimate: it
// asks for the bank-wide row directly. A product with no row of its own falls
// back to that row rather than failing, so mapping a slot once is enough for
// every product a bank sells — and a product that wants its own revenue line
// says so with one row rather than with a copy of the whole mapping.
//
// Returns ErrSlotNotMapped, which is what a chart of accounts that has not been
// configured for this asset looks like from a posting path.
func (s *Book) SlotAccountTx(ctx context.Context, tx Tx, product string, slot Slot, asset AssetCode) (AccountID, error) {
	if product != "" && slot.ByProduct {
		account, err := tx.GetSlotAccount(ctx, s.id, product, slot.Key, asset)
		if err == nil {
			return account, nil
		}
		if !errors.Is(err, ErrSlotNotMapped) {
			return "", err
		}
	}
	return tx.GetSlotAccount(ctx, s.id, "", slot.Key, asset)
}

// SlotPositionTx is SlotAccountTx for a slot that pools obligors: the account,
// with the obligor already on it.
//
// The two travel together for the reason Position exists — a caller holding an
// account and an obligor apart eventually pairs one flow's account with
// another's obligor — and it is why nothing here hands back a bare AccountID for
// a control slot.
func (s *Book) SlotPositionTx(ctx context.Context, tx Tx, product string, slot Slot, asset AssetCode, obligor string) (Position, error) {
	account, err := s.SlotAccountTx(ctx, tx, product, slot, asset)
	if err != nil {
		return Position{}, err
	}
	return account.For(obligor), nil
}

// ListSlotAccounts returns the whole mapping, ordered by slot, product and
// asset. It is what an operator reads to see where a bank's flows post.
func (s *Book) ListSlotAccounts(ctx context.Context) ([]SlotAccount, error) {
	var out []SlotAccount
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListSlotAccounts(ctx, s.id)
		return err
	})
	return out, err
}
