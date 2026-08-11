package api

import (
	"time"

	"github.com/raphi011/cbs/payment"
)

// Wire format for the two directories a bank reads and the one the clearing
// house publishes.

// AccountDirectoryEntryDTO is what GET /directory/accounts answers: which of a
// bank's accounts holds the address, plus the identifier that was resolved,
// echoed back for a client that fired several lookups at once.
//
// # It carries no Name and no Asset
//
// Both would be a JOIN on top of the resolution: bind the resolved bank's
// deposit register and read the account's display name and asset out of it. On a
// BANK's port — where this route is registered — that is one bank reading
// another bank's register for the payee's name, over HTTP rather than through a
// message.
//
// Removing them is not a loss of information the caller was entitled to. A bank
// resolving its OWN customer has the name and the asset already, from every
// other route it has about that account; a bank asking about somebody else's
// gets ErrIdentifierNotFound now, and the name of another bank's customer is not
// something this system will tell it by any route at all. What a payer knows
// about a payee is what the payer typed — see payment.PartyDetails.
type AccountDirectoryEntryDTO struct {
	// Agent is the BIC of the bank the address resolves at, and it is always the
	// asking listener's own — the lookup searches that bank's register and no
	// other, so there is no other answer it could carry. It was `participant`, off
	// the ref the domain returned; a payment.PartyRef names no bank now, and this
	// is the asking bank naming itself. See payment.ResolveIdentifierTx.
	Agent      string        `json:"agent"`
	Account    string        `json:"account"`
	Identifier IdentifierDTO `json:"identifier"`
}

// RoutingEntryDTO is one row of the copy of the scheme's routing directory that
// one bank holds. See payment.DirectoryEntry.
//
// It carries a BIC and no name, which is the whole of what the roster it was
// copied from carries. A client resolving an IBAN gets AURODEFFXXX back and
// cannot get "Aurora Bank", and that is the documented absence rather than a
// field this DTO trims.
//
// RefreshedAt is on every row rather than beside the list, because it is a fact
// about the row: a snapshot replaces the table wholesale, so every row of one
// pull carries one instant, and a client can render "refreshed 3 days ago" from
// any of them. It is the only field here that is about the COPY rather than
// about the member, and it is what makes the subscription visible in a console.
type RoutingEntryDTO struct {
	Country     string    `json:"country"`
	BankCode    string    `json:"bankCode"`
	BIC         string    `json:"bic"`
	RefreshedAt time.Time `json:"refreshedAt"`
}

func RoutingEntryOf(e payment.DirectoryEntry) RoutingEntryDTO {
	return RoutingEntryDTO{
		Country:     string(e.Issuer.Country),
		BankCode:    string(e.Issuer.BankCode),
		BIC:         string(e.BIC),
		RefreshedAt: e.RefreshedAt,
	}
}

// RosterEntryDTO is one row of the clearing house's routing directory.
//
// It is what payment.RosterEntry holds and nothing more, which is the point of
// putting it on a surface at all: this row is the whole of what one institution
// knows about another in this system. An address, the assets it clears in, the
// admission it was admitted under, and when.
//
// No NAME, and its absence is domain content rather than an omission. The
// acknowledgement this row is written from carries none, so the clearing house
// has never been told one; the name a console shows beside a BIC comes from GET
// /members, which is a different question asked of a different table. See
// payment.RosterEntry.
type RosterEntryDTO struct {
	BIC string `json:"bic"`
	// The allocation this member issues its customers' addresses under, which is
	// what makes this list a routing directory rather than a list of addresses
	// the scheme will talk to. Two fields, because a bank code is unique within
	// one country and this roster has members in four.
	//
	// It is what a member COPIES. Everything else on this row stays here: the
	// assets are the clearing house's own membership check and a copy of them
	// would let a stale subscriber refuse what the clearing house would accept,
	// and the admission reference decides between two institutions contending for
	// an address, which is nobody's question but this one's.
	Country      string    `json:"country"`
	BankCode     string    `json:"bankCode"`
	Assets       []string  `json:"assets"`
	AdmissionRef string    `json:"admissionRef"`
	AdmittedAt   time.Time `json:"admittedAt"`
}

func ToRosterEntryDTO(e payment.RosterEntry) RosterEntryDTO {
	assets := make([]string, len(e.Assets))
	for i, a := range e.Assets {
		assets[i] = string(a)
	}
	return RosterEntryDTO{
		BIC:          string(e.BIC),
		Country:      string(e.Issuer.Country),
		BankCode:     string(e.Issuer.BankCode),
		Assets:       assets,
		AdmissionRef: e.AdmissionRef,
		AdmittedAt:   e.AdmittedAt,
	}
}
