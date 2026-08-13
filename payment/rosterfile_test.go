package payment_test

import (
	"strings"
	"testing"
	"time"

	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	. "github.com/raphi011/cbs/payment"
)

const publisher iso20022.BIC = "CSMXDEFFXXX"

// TestARoutingTableTravelsWhole is what a member collects, and it is the whole
// roster row rather than the two columns the member keeps.
func TestARoutingTableTravelsWhole(t *testing.T) {
	at := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	members := []RosterEntry{{
		BIC:          "AURODEFFXXX",
		Issuer:       iban.Issuer{Country: iban.DE, BankCode: "99999999"},
		Assets:       []ledger.AssetCode{"EUR", "USD"},
		AdmissionRef: "admitted-AURODEFFXXX",
		AdmittedAt:   at.Add(-24 * time.Hour),
	}}

	raw, err := RosterFile(publisher, at, members)
	if err != nil {
		t.Fatalf("RosterFile: %v", err)
	}
	// It is NOT an ISO 20022 message, and that is the claim: a scheme's routing
	// table is host data, and no payment message definition covers one.
	if _, err := iso20022.Unmarshal(raw); err == nil {
		t.Error("the routing table parses as an ISO 20022 message, and it is not one")
	}

	got, err := ReadRosterFile(raw)
	if err != nil {
		t.Fatalf("ReadRosterFile: %v", err)
	}
	if got.PublishedBy != publisher || !got.PublishedAt.Equal(at) {
		t.Errorf("the table says it was published by %s at %s, want %s at %s",
			got.PublishedBy, got.PublishedAt, publisher, at)
	}
	if len(got.Members) != 1 {
		t.Fatalf("the table carries %d members, want 1", len(got.Members))
	}
	m := got.Members[0]
	if m.BIC != members[0].BIC || m.Issuer != members[0].Issuer || m.AdmissionRef != members[0].AdmissionRef {
		t.Errorf("the member came back as %+v, want %+v", m, members[0])
	}
	if len(m.Assets) != 2 || m.Assets[0] != "EUR" || m.Assets[1] != "USD" {
		t.Errorf("the member clears in %v, want the two it was published with", m.Assets)
	}
	if !m.AdmittedAt.Equal(members[0].AdmittedAt) {
		t.Errorf("the member was admitted at %s, want %s", m.AdmittedAt, members[0].AdmittedAt)
	}
}

// A scheme that has admitted nobody publishes a table all the same: a member
// collecting it learns that it can route to no one, which is not the same as
// learning nothing.
func TestATableWithNoMembersIsStillATable(t *testing.T) {
	raw, err := RosterFile(publisher, time.Unix(0, 0).UTC(), nil)
	if err != nil {
		t.Fatalf("RosterFile: %v", err)
	}
	got, err := ReadRosterFile(raw)
	if err != nil {
		t.Fatalf("ReadRosterFile: %v", err)
	}
	if len(got.Members) != 0 {
		t.Errorf("an empty roster came back carrying %d members", len(got.Members))
	}
}

// A table nobody published is refused at both ends: it is the one field that
// says whose directory a member is about to route from.
func TestARoutingTableNamesWhoPublishedIt(t *testing.T) {
	if _, err := RosterFile("", time.Unix(0, 0).UTC(), nil); err == nil {
		t.Error("a table published by nobody was built")
	}
	if _, err := ReadRosterFile([]byte(`{"members":[]}`)); err == nil {
		t.Error("a table published by nobody was read")
	}
	if _, err := ReadRosterFile([]byte("not a routing table")); err == nil {
		t.Error("bytes that are not a table were read as one")
	} else if !strings.Contains(err.Error(), "routing table") {
		t.Errorf("reading rubbish says %q, and it should say what was expected", err)
	}
}
