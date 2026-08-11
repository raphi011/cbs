package payment

import (
	"context"
	"fmt"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
)

// A member bank's routing directory: the copy it subscribes to, and the
// derivation that reads it.
//
// Every act here is a MEMBER BANK's and runs against that bank's own database.
// The clearing house publishes the roster and the settlement agent allocates the
// codes; neither of those tables is reachable from here, and that is the whole
// arrangement — a bank routes from a snapshot it holds, so no payment costs a
// read into another institution's database. See DirectoryEntry.

// RefreshDirectory is RefreshDirectoryTx in its own unit of work.
func (s *Network) RefreshDirectory(ctx context.Context, published []RosterEntry) ([]DirectoryEntry, error) {
	var out []DirectoryEntry
	err := s.store.Update(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = s.RefreshDirectoryTx(ctx, tx, published)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RefreshDirectoryTx takes delivery of a snapshot of the scheme's routing
// directory and replaces this bank's copy with it.
//
// # It is handed the roster rather than reading one
//
// A member cannot open the clearing house's database, so the rows arrive from
// outside the domain — mesh.Mesh.RefreshDirectory is what carries them, standing
// in for the file a subscriber downloads. What this act decides is not where
// they came from but WHAT SURVIVES the copy, and that is a domain ruling:
//
//   - the allocation and the BIC, because that pairing is the question a payer's
//     bank has to answer and cannot compute;
//   - not the ASSETS, deliberately. This copy may be behind, and a membership
//     refusal computed from data that is behind would refuse a payment the
//     clearing house would have accepted. bothBanksAreMembersTx makes that check
//     against the live roster, at the institution that holds it;
//   - not the admission reference, which decides between two institutions
//     contending for one address and is nobody's business but the clearing
//     house's.
//
// An entry the publisher has not allocated a code to is DROPPED rather than
// stored empty. Every member is allocated one at admission, so this is the
// unreachable case being made harmless rather than a filter with a caller: a row
// keyed by ("", "") would answer every unallocated lookup with whichever member
// was written last.
//
// # Wholesale, and one instant
//
// The replacement is total — a directory is a file, not a delta feed — and every
// row carries the same RefreshedAt, because a snapshot is one act. What that
// costs is that a member which has left the roster stops being routable here,
// which is the point of taking delivery rather than merging.
func (s *Network) RefreshDirectoryTx(ctx context.Context, tx Tx, published []RosterEntry) ([]DirectoryEntry, error) {
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
	// The event is this bank's own record that it pulled, and it is what makes
	// the subscription visible in a log rather than only in a column. It is keyed
	// by the acting bank, because the refresh is about the SUBSCRIBER and not
	// about any member in the snapshot — the alternative, one event per entry,
	// would record N facts where one act happened.
	self, err := s.selfBIC()
	if err != nil {
		return nil, err
	}
	if err := s.appendAuditTx(ctx, tx, ledger.EventDirectoryRefreshed, string(self), entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// ListDirectory is every entry this bank's copy holds, in the order the snapshot
// was published — oldest member of the scheme first.
//
// It answers what this bank believes and when it last asked, which is the only
// honest thing a subscriber can report. It is not "who is in the scheme": that
// question is the clearing house's ListRosterEntries, and the two disagreeing is
// legal.
func (s *Network) ListDirectory(ctx context.Context) ([]DirectoryEntry, error) {
	if _, err := s.self(); err != nil {
		return nil, err
	}
	var out []DirectoryEntry
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.ListDirectoryEntries(ctx)
		return err
	})
	return out, err
}

// routeTx turns a counterparty's ADDRESS into the bank that holds it, out of
// this bank's own copy of the routing directory.
//
// This is the derivation the whole task is for, and it is what makes an
// instruction here IBAN-only: a payer types an address and nothing else, and the
// BIC the message routes on is read out of a table this bank subscribed to. The
// alternative — the payer typing the BIC as well — is SEPA before February 2016
// and is still what a cross-border transfer is, which is the door below.
//
// # Three outcomes, and each has a different remedy
//
// An IBAN is derived from, and any BIC the caller also supplied is IGNORED
// rather than checked against the answer. There is nothing to check: the
// directory is this bank's only source for the pairing, so a disagreement would
// be the caller contradicting the only authority in the room. What that costs is
// that the payer can no longer choose which bank receives their money, which is
// the whole gain.
//
//   - The address parses and its code is in the copy: that entry's BIC.
//   - The address parses and its code is not: ErrBankCodeUnknown. Refresh, or
//     the payee's bank is not in this scheme, and this bank cannot tell which.
//   - The address does not parse: the iban package's own refusal, unwrapped, so
//     a payer who mistyped a digit is told that rather than told their payee's
//     bank is unreachable. This is the check digit earning its place — it is the
//     one refusal here made without any lookup at all.
//
// # The door for everything that is not an IBAN
//
// A scheme with no directory here — a card PAN, a proxy alias, an address in a
// country this system issues none in — falls back to the BIC the instruction
// asserted, and refuses with ErrCounterpartyAgentNotNamed if there is none. That
// sentinel means "not for THIS address" and not "this system has nowhere to get
// an agent from" — a much narrower claim, and the one a real scheme makes. Proxy aliases are resolved by a separate central service in the
// real world (the EPC's Proxy Lookup Service, UPI) precisely because no bank can
// guarantee they are unique, and this system has no such service.
func (s *Network) routeTx(ctx context.Context, tx Tx, ident deposit.Identifier, asserted iso20022.BIC) (iso20022.BIC, error) {
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
//
// It is the authoritative version of what every member holds a copy of, and it
// has exactly one caller: the clearing house's operator console, initiating a
// payment on a customer's behalf. That console has to hand the instruction to
// the bank whose act it is, and under IBAN-only an instruction names no bank —
// so the submitting bank is read out of the address, from the table this
// institution publishes.
//
// No BANK may call it, and none can: it is guarded to the clearing house, and a
// member asking "who really answers for this code" instead of consulting its own
// copy would be the per-payment lookup this whole design replaces with a
// subscription. What a member gets is ResolveBankCode, over its own snapshot.
//
// A miss is ErrRosterEntryNotFound — no member is published under this code —
// which is a different statement from a subscriber's ErrBankCodeUnknown for the
// reason those two sentinels set out: this institution CAN tell.
func (s *Network) RosterAgentFor(ctx context.Context, ident deposit.Identifier) (iso20022.BIC, error) {
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
	err = s.store.View(ctx, func(ctx context.Context, tx Tx) error {
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
//
// It is the lookup a send form makes once an IBAN's check digits pass, and it is
// the same read SubmitPaymentTx makes to derive a counterparty's agent — the
// same table, so a form that shows a BIC and a submission that routes to one
// cannot disagree. What it answers on a miss is ErrBankCodeUnknown, which cannot
// say whether the bank is absent from the scheme or merely absent from this copy.
func (s *Network) ResolveBankCode(ctx context.Context, issuer iban.Issuer) (DirectoryEntry, error) {
	if _, err := s.self(); err != nil {
		return DirectoryEntry{}, err
	}
	var out DirectoryEntry
	err := s.store.View(ctx, func(ctx context.Context, tx Tx) error {
		var err error
		out, err = tx.GetDirectoryEntry(ctx, issuer)
		return err
	})
	if err != nil {
		return DirectoryEntry{}, err
	}
	return out, nil
}
