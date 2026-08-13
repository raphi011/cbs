package payment

import (
	"context"
	"fmt"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/internal/unit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// A member bank's routing directory: the copy it subscribes to, and the
// derivation that reads it.

// RefreshDirectory is RefreshDirectoryTx in its own unit of work.
func (s *BankNetwork) RefreshDirectory(ctx context.Context, published []RosterEntry) ([]DirectoryEntry, error) {
	return unit.Run(ctx, s.store.Update, func(ctx context.Context, tx BankTx) ([]DirectoryEntry, error) {
		return s.RefreshDirectoryTx(ctx, tx, published)
	})
}

// RefreshDirectoryTx takes delivery of a snapshot of the scheme's routing
// directory and replaces this bank's copy with it.
func (s *BankNetwork) RefreshDirectoryTx(ctx context.Context, tx BankTx, published []RosterEntry) ([]DirectoryEntry, error) {
	if _, err := s.self(); err != nil {
		return nil, err
	}
	now := s.now()
	entries := make([]DirectoryEntry, 0, len(published))
	for _, e := range published {
		if !e.Issuer.Allocated() {
			continue
		}
		entries = append(entries, DirectoryEntry{Issuer: e.Issuer, BIC: e.BIC, RefreshedAt: now})
	}
	if err := tx.ReplaceRoutingDirectory(ctx, entries); err != nil {
		return nil, err
	}
	// The event is this bank's own record that it pulled, and it is what makes the
	// subscription visible in a log rather than only in a column.
	self, err := s.selfBIC()
	if err != nil {
		return nil, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventDirectoryRefreshed, string(self), entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// ListDirectory is every entry this bank's copy holds, in the order the
// snapshot was published — oldest member of the scheme first.
func (s *BankNetwork) ListDirectory(ctx context.Context) ([]DirectoryEntry, error) {
	if _, err := s.self(); err != nil {
		return nil, err
	}
	var out []DirectoryEntry
	err := s.store.View(ctx, func(ctx context.Context, tx BankTx) error {
		var err error
		out, err = tx.ListDirectoryEntries(ctx)
		return err
	})
	return out, err
}

// routeTx turns a counterparty's ADDRESS into the bank that holds it, out of
// this bank's own copy of the routing directory.
func (s *BankNetwork) routeTx(ctx context.Context, tx BankTx, ident deposit.Identifier, asserted iso20022.BIC) (iso20022.BIC, error) {
	if ident.Scheme != deposit.IdentifierIBAN {
		if asserted == "" {
			return "", ErrCounterpartyAgentNotNamed
		}
		if err := asserted.Validate(); err != nil {
			return "", fmt.Errorf("%w: %w", ErrCounterpartyAgentNotNamed, err)
		}
		return asserted, nil
	}
	addr, err := iban.Parse(ident.Value)
	if err != nil {
		return "", err
	}
	code, err := addr.BankCode()
	if err != nil {
		return "", err
	}
	e, err := tx.GetDirectoryEntry(ctx, iban.Issuer{Country: addr.Country(), BankCode: code})
	if err != nil {
		return "", err
	}
	return e.BIC, nil
}

// RosterAgentFor answers which member the scheme PUBLISHES an address's bank
// code against. It is the clearing house's own act, over its own roster.
func (s *ClearingHouseNetwork) RosterAgentFor(ctx context.Context, ident deposit.Identifier) (iso20022.BIC, error) {
	if err := s.clearingHouse(); err != nil {
		return "", err
	}
	if ident.Scheme != deposit.IdentifierIBAN {
		return "", ErrCounterpartyAgentNotNamed
	}
	addr, err := iban.Parse(ident.Value)
	if err != nil {
		return "", err
	}
	code, err := addr.BankCode()
	if err != nil {
		return "", err
	}
	var out RosterEntry
	err = s.store.View(ctx, func(ctx context.Context, tx CsmTx) error {
		var err error
		out, err = tx.GetRosterEntryByIssuer(ctx, iban.Issuer{Country: addr.Country(), BankCode: code})
		return err
	})
	if err != nil {
		return "", err
	}
	return out.BIC, nil
}

// ResolveBankCode answers which institution this bank would route an address
// under one allocation to, from its own copy and from nothing else.
func (s *BankNetwork) ResolveBankCode(ctx context.Context, issuer iban.Issuer) (DirectoryEntry, error) {
	if _, err := s.self(); err != nil {
		return DirectoryEntry{}, err
	}
	var out DirectoryEntry
	err := s.store.View(ctx, func(ctx context.Context, tx BankTx) error {
		var err error
		out, err = tx.GetDirectoryEntry(ctx, issuer)
		return err
	})
	if err != nil {
		return DirectoryEntry{}, err
	}
	return out, nil
}
