package deposit

import (
	"context"

	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/internal/unit"
	"github.com/raphi011/cbs/ledger"
)

// Issuing an account's own address, and reissuing it.

// mintAddressTx allocates the next address in this bank's own range.
func (r *Register) mintAddressTx(ctx context.Context, tx Tx) (Identifier, error) {
	if !r.issuer.Allocated() {
		return Identifier{}, ErrNoIssuer
	}
	serial, err := tx.NextAddressSerial(ctx, r.bookID)
	if err != nil {
		return Identifier{}, err
	}
	addr, err := iban.New(r.issuer.Country, r.issuer.BankCode, serial)
	if err != nil {
		return Identifier{}, err
	}
	return Identifier{Scheme: IdentifierIBAN, Value: string(addr)}, nil
}

// ReissueIdentifier gives an account a new IBAN and withdraws its old one,
// together.
func (r *Register) ReissueIdentifier(ctx context.Context, id AccountID) (Identifier, error) {
	return unit.Run(ctx, r.store.Update, func(ctx context.Context, tx Tx) (Identifier, error) {
		return r.ReissueIdentifierTx(ctx, tx, id)
	})
}

// ReissueIdentifierTx is ReissueIdentifier within a caller-supplied unit of
// work.
func (r *Register) ReissueIdentifierTx(ctx context.Context, tx Tx, id AccountID) (Identifier, error) {
	acct, err := tx.GetDepositAccount(ctx, r.bookID, id)
	if err != nil {
		return Identifier{}, err
	}
	fresh, err := r.mintAddressTx(ctx, tx)
	if err != nil {
		return Identifier{}, err
	}

	kept := make([]Identifier, 0, len(acct.Identifiers)+1)
	var withdrawn []Identifier
	for _, got := range acct.Identifiers {
		if got.Scheme == IdentifierIBAN {
			withdrawn = append(withdrawn, got)
			continue
		}
		kept = append(kept, got)
	}
	acct.Identifiers = append([]Identifier{fresh}, kept...)
	if err := tx.PutDepositAccount(ctx, r.bookID, acct); err != nil {
		return Identifier{}, err
	}

	// Two events and not one, because two things happened and a log that collapses
	// them cannot answer "when did this address stop working?" — the question
	// anybody reading this trail is here to ask.
	for _, old := range withdrawn {
		if err := r.appendAuditTx(ctx, tx, ledger.EventIdentifierRemoved, string(id), old); err != nil {
			return Identifier{}, err
		}
	}
	if err := r.appendAuditTx(ctx, tx, ledger.EventIdentifierAdded, string(id), fresh); err != nil {
		return Identifier{}, err
	}
	return fresh, nil
}
