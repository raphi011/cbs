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

// Slot is a role in a posting flow, and the constraints the account filling it
// must satisfy.
type Slot struct {
	// Key identifies the slot in storage.
	Key string

	// Type is the account type the slot requires.
	Type AccountType

	// Control says the slot requires an account that pools subsidiaries.
	Control bool

	// ByProduct says a product may override the bank-wide row for this slot.
	ByProduct bool
}

// SlotAccount is one row of the mapping: which account a (product, slot, asset)
// triple posts to.
type SlotAccount struct {
	Product string
	Slot    string
	Asset   AssetCode
	Account AccountID
}

// MapSlotTx points a slot at an account, or repoints it, within a
// caller-supplied unit of work.
func (s *Book) MapSlotTx(ctx context.Context, tx BankTx, product string, slot Slot, asset AssetCode, account AccountID) error {
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
func (s *Book) SlotAccountTx(ctx context.Context, tx SlotTx, product string, slot Slot, asset AssetCode) (AccountID, error) {
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

// SlotPositionTx is SlotAccountTx for a slot that pools subsidiaries: the
// account, with the subsidiary already on it.
func (s *Book) SlotPositionTx(ctx context.Context, tx SlotTx, product string, slot Slot, asset AssetCode, subsidiary string) (Position, error) {
	account, err := s.SlotAccountTx(ctx, tx, product, slot, asset)
	if err != nil {
		return Position{}, err
	}
	return account.For(subsidiary), nil
}

// ListSlotAccountsTx returns the whole mapping, ordered by slot, product and
// asset. It is what an operator reads to see where a bank's flows post.
func (s *Book) ListSlotAccountsTx(ctx context.Context, tx SlotTx) ([]SlotAccount, error) {
	return tx.ListSlotAccounts(ctx, s.id)
}
