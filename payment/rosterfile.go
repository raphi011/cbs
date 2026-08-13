package payment

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/raphi011/cbs/iso20022"
)

// The scheme's routing table as a FILE: what the clearing house publishes and
// every member collects. It is not an ISO 20022 message, and the design record
// argues why a routing table is not one.

// A PublishedRoster is one snapshot of the routing table: who published it, when,
// and the roster as it stood. A RosterEntry travels whole, because the file is
// the roster.
type PublishedRoster struct {
	PublishedBy iso20022.BIC  `json:"publishedBy"`
	PublishedAt time.Time     `json:"publishedAt"`
	Members     []RosterEntry `json:"members"`
}

// RosterFile renders a snapshot for publication. A roster with no members is a
// scheme that has admitted nobody, which is a table worth publishing: a member
// collecting it learns that it can route to no one.
func RosterFile(by iso20022.BIC, at time.Time, members []RosterEntry) ([]byte, error) {
	if by == "" {
		return nil, fmt.Errorf("payment: a routing table names the institution that published it")
	}
	return json.Marshal(PublishedRoster{PublishedBy: by, PublishedAt: at, Members: members})
}

// ReadRosterFile parses a collected routing table.
func ReadRosterFile(raw []byte) (PublishedRoster, error) {
	var out PublishedRoster
	if err := json.Unmarshal(raw, &out); err != nil {
		return PublishedRoster{}, fmt.Errorf("payment: these %d bytes are not a routing table: %w", len(raw), err)
	}
	if out.PublishedBy == "" {
		return PublishedRoster{}, fmt.Errorf("payment: this routing table names no institution that published it")
	}
	return out, nil
}
