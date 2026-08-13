package main

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/provision"
	"github.com/raphi011/cbs/store/storetest"
)

// The mesh, and the stream that keeps it live. Every institution holds one half
// of every crossing, so the whole picture is the DEPLOYMENT's to give — and the
// broadcast that makes it move must not take events the report needs.

func flowOn(t *testing.T, s *server, query string) api.NetworkFlowDTO {
	t.Helper()
	var out api.NetworkFlowDTO
	getJSON(t, cbSurface(s), "/network/flow"+query, &out)
	return out
}

// roleOf is what part the mesh says one address plays.
func roleOf(flow api.NetworkFlowDTO, bic string) string {
	for _, i := range flow.Institutions {
		if i.BIC == bic {
			return i.Role
		}
	}
	return ""
}

// wired is every host one address may dial.
func wired(flow api.NetworkFlowDTO, subscriber string) []string {
	var out []string
	for _, w := range flow.Wires {
		if w.Subscriber == subscriber {
			out = append(out, w.Host)
		}
	}
	slices.Sort(out)
	return out
}

// TestTheMeshIsEveryInstitutionAndTheWiresBetweenThem is the graph's shape: who
// is in this network, what part each plays, and who may dial whom.
func TestTheMeshIsEveryInstitutionAndTheWiresBetweenThem(t *testing.T) {
	s := newServer(t, nil)
	a, _, _ := threeBanks(t, s)
	// Founded and never admitted, which is a bank with a database, a listener and
	// no queue at either host.
	outsider := provisionBank(t, s, "OUTSDEFFXXX", "Outside Bank")

	flow := flowOn(t, s, "")
	cb, csm := string(testConfig.CentralBankBIC), string(testConfig.ClearingHouseBIC)

	for _, want := range []struct{ bic, role string }{
		{cb, api.RoleSettlementAgent},
		{csm, api.RoleClearingHouse},
		{a.bic, api.RoleMemberBank},
		{outsider, api.RoleMemberBank},
	} {
		if got := roleOf(flow, want.bic); got != want.role {
			t.Errorf("the mesh calls %s a %q, want %q", want.bic, got, want.role)
		}
	}

	// A member is a subscriber at both hosts; the clearing house is a subscriber
	// at the settlement agent and the settlement agent is one nowhere.
	if got, want := wired(flow, a.bic), []string{cb, csm}; !slices.Equal(got, want) {
		t.Errorf("%s dials %v, want %v", a.bic, got, want)
	}
	if got, want := wired(flow, csm), []string{cb}; !slices.Equal(got, want) {
		t.Errorf("the clearing house dials %v, want %v", got, want)
	}
	if got := wired(flow, cb); len(got) != 0 {
		t.Errorf("the settlement agent dials %v; it is a host and a subscriber nowhere", got)
	}
	// The bank the scheme never admitted is IN the mesh and on no wire, which is
	// the whole difference between holding a database and being reachable.
	if got := wired(flow, outsider); len(got) != 0 {
		t.Errorf("%s was never admitted and dials %v", outsider, got)
	}
}

// TestAFileRestingOnTheWireIsDistinguishableFromADeliveredOne is what the mesh
// exists to draw: a crossing with a send and no take is a queue nobody has come
// for, and that gap is settle-before-release.
func TestAFileRestingOnTheWireIsDistinguishableFromADeliveredOne(t *testing.T) {
	s := newServer(t, nil)
	a, b, _ := threeBanks(t, s)
	pending(t, s, a, b, "mesh-e2e")

	csm := string(testConfig.ClearingHouseBIC)
	step(t, s, "bank-cut-off")

	uploaded := crossingBetween(t, flowOn(t, s, ""), a.bic, csm)
	if uploaded.SentAt == nil {
		t.Fatalf("the payer's bank uploaded a file the mesh says it never sent: %+v", uploaded)
	}
	if uploaded.ReceivedAt != nil {
		t.Errorf("%s is already delivered after the cut-off; the clearing house has not worked it yet", uploaded.OrderID)
	}
	if uploaded.PayloadSize == 0 {
		t.Errorf("the mesh says %s is an empty file", uploaded.OrderID)
	}
	if len(uploaded.Payments) != 1 {
		t.Errorf("the pacs.008 carried %d payments in the mesh, want the 1 that was submitted", len(uploaded.Payments))
	}

	// The clearing house takes it out of its order log, and the same crossing is
	// a delivered one — same order id, both halves, each end's own seq.
	step(t, s, "clearing")
	delivered := crossingBetween(t, flowOn(t, s, ""), a.bic, csm)
	if delivered.OrderID != uploaded.OrderID {
		t.Fatalf("the crossing changed order id from %s to %s", uploaded.OrderID, delivered.OrderID)
	}
	if delivered.ReceivedAt == nil {
		t.Fatalf("the clearing house worked %s and the mesh still has it resting on the wire", delivered.OrderID)
	}
	if delivered.SentSeq == 0 || delivered.ReceivedSeq == 0 {
		t.Errorf("%s names %d at its sender and %d at its receiver; a seq counts one institution's own log",
			delivered.OrderID, delivered.SentSeq, delivered.ReceivedSeq)
	}

	// And a file is put in one queue and taken out of another, so the answer moving
	// back the other way is a crossing of its own that nobody has collected.
	answer := crossingBetween(t, flowOn(t, s, ""), csm, a.bic)
	if answer.ReceivedAt != nil {
		t.Errorf("the payer's bank has collected %s without a collection phase having run", answer.OrderID)
	}
}

// TestTheMeshNeverPagesOutAFileRestingOnTheWire pins the one thing the limit
// may not drop. A queue nobody has come for is what this view is for.
func TestTheMeshNeverPagesOutAFileRestingOnTheWire(t *testing.T) {
	s := newServer(t, nil)
	a, b, _ := threeBanks(t, s)
	pending(t, s, a, b, "paged-e2e")
	step(t, s, "bank-cut-off")

	full := flowOn(t, s, "")
	if resting(full) == 0 {
		t.Fatal("nothing is resting on a wire, so this case asserts nothing")
	}
	if delivered(full) == 0 {
		t.Fatal("nothing has been delivered, so a limit would drop nothing")
	}

	narrow := flowOn(t, s, "?limit=1")
	if got := resting(narrow); got != resting(full) {
		t.Errorf("a page of 1 shows %d files resting on a wire, want all %d of them", got, resting(full))
	}
	if got := delivered(narrow); got != 1 {
		t.Errorf("a page of 1 shows %d delivered crossings, want 1", got)
	}
}

func resting(f api.NetworkFlowDTO) int   { return countCrossings(f, false) }
func delivered(f api.NetworkFlowDTO) int { return countCrossings(f, true) }

func countCrossings(f api.NetworkFlowDTO, arrived bool) int {
	var n int
	for _, c := range f.Crossings {
		if (c.ReceivedAt != nil) == arrived {
			n++
		}
	}
	return n
}

// crossingBetween is the one crossing the mesh holds between two addresses,
// failing unless there is exactly one.
func crossingBetween(t *testing.T, f api.NetworkFlowDTO, from, to string) api.CrossingDTO {
	t.Helper()
	var found []api.CrossingDTO
	for _, c := range f.Crossings {
		if c.From == from && c.To == to {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the mesh holds %d crossings from %s to %s, want 1", len(found), from, to)
	}
	return found[0]
}

// TestEveryCrossingTheMeshShowsWasObservedAtBothEnds is the merge held to what
// the two logs say: over a whole business day nothing is left half-recorded,
// and no crossing is one end's opinion alone.
func TestEveryCrossingTheMeshShowsWasObservedAtBothEnds(t *testing.T) {
	s := newServer(t, nil)
	a, b, _ := threeBanks(t, s)
	doJSON(t, csmSurface(s), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	sct(t, s, a, b, "paired-e2e")
	if _, err := s.dep.AdvanceDay(t.Context()); err != nil {
		t.Fatalf("AdvanceDay: %v", err)
	}

	flow := flowOn(t, s, "")
	if len(flow.Crossings) == 0 {
		t.Fatal("a whole business day crossed no files, so this test would pass on nothing")
	}
	for _, c := range flow.Crossings {
		if c.SentAt == nil {
			t.Errorf("%s→%s %s was received and never sent; one end is missing a record",
				c.From, c.To, c.OrderID)
		}
		if c.ReceivedAt == nil {
			t.Errorf("%s→%s %s is still resting on a wire after a whole day", c.From, c.To, c.OrderID)
		}
		if c.MsgDefIdr == "" {
			t.Errorf("%s→%s %s names no message definition", c.From, c.To, c.OrderID)
		}
	}
}

// TestAPublishedFilePairsOnItsMessageID: the roster is minted no order id
// because nothing was queued for anybody, so the only thing its two ends hold
// in common is the header its sender put on it.
func TestAPublishedFilePairsOnItsMessageID(t *testing.T) {
	const (
		host   iso20022.BIC = "CSMXFRPPXXX"
		member iso20022.BIC = "AURODEFFXXX"
	)
	published := func(seq int64, dir payment.MessageDirection, other iso20022.BIC, msgID string) payment.Message {
		return payment.Message{Seq: seq, Direction: dir, Counterparty: other, MsgID: msgID}
	}

	// The two ends of one published file, each recorded under its own seq.
	sent := keyOf(host, published(7, payment.MessageSent, member, "HRD-1"))
	taken := keyOf(member, published(3, payment.MessageReceived, host, "HRD-1"))
	if sent != taken {
		t.Errorf("the two halves of one published file keyed as %v and %v; "+
			"the mesh would show it both resting on the wire and never sent", sent, taken)
	}

	// And two published files between the same ends stay two crossings.
	if next := keyOf(member, published(4, payment.MessageReceived, host, "HRD-2")); next == taken {
		t.Error("two published files between one pair of ends merged into one crossing")
	}

	// A half with neither an order id nor a message id pairs with nothing, which
	// is the honest answer rather than a merge with every other such half.
	blank := keyOf(member, published(9, payment.MessageReceived, host, ""))
	if blank == keyOf(host, published(5, payment.MessageSent, member, "")) {
		t.Error("two files with no handle at all paired; nothing says they are the same file")
	}
}

// TestNoInstitutionServesTheMesh is the boundary the mesh's placement exists
// for: a bank's console and the clearing house's answer their own traffic and
// nobody's else, however the URL is spelt.
func TestNoInstitutionServesTheMesh(t *testing.T) {
	s := newServer(t, nil)
	a, _, _ := threeBanks(t, s)

	for _, path := range []string{"/network/flow", "/network/flow/events"} {
		assertStatus(t, bankSurface(s, a.pid), "GET", path, "", http.StatusNotFound)
		assertStatus(t, csmSurface(s), "GET", path, "", http.StatusNotFound)
	}
}

// TestABroadcastDoesNotConsumeWhatAReportNeeds is the property that makes a
// second subscriber safe. take empties the journal and a watcher does not, so
// the two see the same events and neither takes them from the other.
func TestABroadcastDoesNotConsumeWhatAReportNeeds(t *testing.T) {
	s := newServer(t, nil)
	a, b, _ := threeBanks(t, s)
	pending(t, s, a, b, "watched-e2e")

	// Emptied first: the fixture above lodged and carried, and a journal is
	// drained by whoever reports rather than by the clock.
	s.dep.journal.take()

	events, release := s.dep.hub.watch()
	defer release()

	// A watcher is told as each event is recorded, so everything the phase moved
	// is already buffered by the time it answers.
	report := step(t, s, "bank-cut-off")
	if len(report.Files) == 0 {
		t.Fatal("the cut-off moved no files, so this test would pass on nothing")
	}

	var told []api.FileMovedDTO
	for draining := true; draining; {
		select {
		case e := <-events:
			if e.Name == api.EventFile {
				told = append(told, e.Data.(api.FileMovedDTO))
			}
		default:
			draining = false
		}
	}
	if !slices.Equal(told, report.Files) {
		t.Errorf("the watcher was told %+v and the report carries %+v; one of them consumed the other's events",
			told, report.Files)
	}

	// And the journal is empty afterwards either way: a watcher takes nothing out
	// of it, so the next phase reports only its own work.
	next := step(t, s, "clearing")
	for _, f := range next.Files {
		if slices.Contains(report.Files, f) {
			t.Errorf("the clearing phase re-reported %v, which the cut-off already moved", f)
		}
	}
}

// TestAWatcherThatFallsBehindIsDropped is what a full buffer means: nothing an
// institution does waits on a browser, and the watcher is cut off rather than
// fed a picture with holes in it.
func TestAWatcherThatFallsBehindIsDropped(t *testing.T) {
	var h hub
	events, release := h.watch()
	defer release()

	for i := 0; i <= watcherBuffer; i++ {
		h.publish(api.StreamEvent{Name: api.EventFile, Data: api.FileMovedDTO{OrderID: "o"}})
	}
	if got := h.watching(); got != 0 {
		t.Errorf("%d watchers are still held after one fell behind, want none", got)
	}
	for range watcherBuffer {
		<-events
	}
	if _, open := <-events; open {
		t.Error("a dropped watcher's channel is still open; nothing tells its browser to reconnect")
	}
}

// TestAWatcherIsToldWhatMovedWithoutAsking is the push channel over a real
// connection: an operator steps a phase in one request and a page that asked
// for nothing is told what moved.
func TestAWatcherIsToldWhatMovedWithoutAsking(t *testing.T) {
	s := newServer(t, nil)
	a, b, _ := threeBanks(t, s)
	pending(t, s, a, b, "streamed-e2e")

	console := httptest.NewServer(cbSurface(s))
	defer console.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", console.URL+"/network/flow/events", nil)
	if err != nil {
		t.Fatalf("building the watch request: %v", err)
	}
	// The response arrives once the headers are flushed, which is what says the
	// stream is open before anything has happened on it.
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("watching the network: %v", err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("the watch route answered %q, want an event stream", got)
	}

	step(t, s, "bank-cut-off")

	var name, data string
	lines := bufio.NewScanner(resp.Body)
	for lines.Scan() && data == "" {
		switch line := lines.Text(); {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && name == api.EventFile:
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	if data == "" {
		t.Fatalf("the stream said nothing about a phase that moved files: %v", lines.Err())
	}
	if !strings.Contains(data, `"from":"`+a.bic+`"`) || !strings.Contains(data, `"movement":"put"`) {
		t.Errorf("the watcher was told %s, want the payer's bank putting a file on the wire", data)
	}

	// And a watcher that goes away is released, so a hub does not grow a channel
	// per page load that ever happened.
	cancel()
	for i := 0; s.dep.hub.watching() != 0; i++ {
		if i == 100 {
			t.Fatalf("%d watchers are still held after the connection closed", s.dep.hub.watching())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// pending submits a credit transfer and does NOT carry it, so it is sitting in
// its bank's hub with nothing having crossed a wire on its behalf yet.
func pending(t *testing.T, s *server, from, to seededBank, e2e string) {
	t.Helper()
	doJSON(t, csmSurface(s), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	doJSON(t, csmSurface(s), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+from.account+`","identifier":{"scheme":"IBAN","value":"`+from.iban+`"}},
		"creditor":{"account":"`+to.account+`","identifier":{"scheme":"IBAN","value":"`+to.iban+`"}},
		"amount":10000,
		"endToEndId":"`+e2e+`",
		"creditorName":"`+to.accountName+`"
	}`, http.StatusAccepted)
}

// provisionBank founds a bank and does NOT admit it: a database and a listener,
// and no queue at either host. AddBank is what a member gets and this skips it.
func provisionBank(t *testing.T, s *server, bic, name string) string {
	t.Helper()
	p, err := provision.Bank(t.Context(), s.nets, provision.BankSpec{
		Name: name, BIC: iso20022.BIC(bic), Country: storetest.FixtureCountry,
	})
	if err != nil {
		t.Fatalf("provisioning %s (%s): %v", name, bic, err)
	}
	if _, err := s.dep.mintBank(t.Context(), p.BIC); err != nil {
		t.Fatalf("minting %s: %v", bic, err)
	}
	return string(p.BIC)
}
