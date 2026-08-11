package deposit

import (
	"context"

	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/ledger"
)

// Issuing an account's own address, and reissuing it.
//
// A BANK ISSUES; IT DOES NOT ACCEPT. Every IBAN in this register was minted here
// under this register's Issuer, which is what makes the bank code inside one a
// true statement about who holds the account. See ErrIBANIsIssued for what a
// caller-supplied address would cost.

// mintAddressTx allocates the next address in this bank's own range.
//
// The serial comes from a counter of this register's own, NOT from the shared id
// counter every other identifier in a book draws on. That counter interleaves —
// dep_1, tx_2, evt_3 — so addresses drawn from it would come out …0001, …0007,
// …0019 with the gaps saying how much unrelated work happened in between. An
// account number is a thing a customer reads out loud; it is worth a counter.
//
// It is allocated INSIDE the caller's unit of work, so an account open that
// rolls back does not burn an address — the same rule id_sequences states for
// every other counter in the schema, and the reason none of them is a database
// sequence.
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
//
// # Why this is one act and not two
//
// A remove plus an add does not compose, because a bank issues these: there is no
// call a caller could make to supply the replacement. Splitting it into
// "withdraw" and "mint" would also leave a real gap in between — an account with
// no address is unpayable, and a failure between the two calls would leave one
// there with nothing saying it had happened.
//
// # What it does not move
//
// The balance, the history, and every mandate on the account. That last one is
// the point rather than a side effect: a mandate authorises debits from an
// ACCOUNT and compares its parties by (participant, account), so reissuing the
// debtor's address leaves every mandate on it working. A payment already sent is
// equally untouched, because a payment records the address it was reached BY.
//
// What DOES break is anyone holding the old address — which is the whole cost of
// reissuing one, and is why it is an act somebody performs deliberately rather
// than something that happens.
func (r *Register) ReissueIdentifier(ctx context.Context, id AccountID) (Identifier, error) {
	var out Identifier
	err := r.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = r.ReissueIdentifierTx(ctx, tx, id)
		return err
	})
	return out, err
}

// ReissueIdentifierTx is ReissueIdentifier within a caller-supplied unit of
// work.
//
// An account holding no IBAN is reissued one rather than refused. That is not a
// state this system can currently reach — every account is opened with one — but
// the alternative is a method that refuses to give an unpayable account an
// address, which is the opposite of what it is for.
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

	// Two events and not one, because two things happened and a log that
	// collapses them cannot answer "when did this address stop working?" — the
	// question anybody reading this trail is here to ask. The withdrawal is
	// recorded first: that is the order they happened in from the outside, where
	// the old address stops resolving the moment the new one lands.
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
