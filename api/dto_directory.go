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
type AccountDirectoryEntryDTO struct {
	// Agent is the BIC of the bank the address resolves at, and it is always the
	// asking listener's own — the lookup searches that bank's register and no
	// other, so there is no other answer it could carry.
	Agent      string        `json:"agent"`
	Account    string        `json:"account"`
	Identifier IdentifierDTO `json:"identifier"`
}

// RoutingEntryDTO is one row of the copy of the scheme's routing directory that
// one bank holds. See payment.DirectoryEntry.
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
type RosterEntryDTO struct {
	BIC string `json:"bic"`
	// The allocation this member issues its customers' addresses under, which is
	// what makes this list a routing directory rather than a list of addresses the
	// scheme will talk to.
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
