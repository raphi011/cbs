package main

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// The message log as a listener answers it. What the cases beside this one
// assert about the domain, these assert about the surface: one institution's
// traffic per port, and no route anywhere that answers the mesh.

// carriedPayment is the smallest fixture the log has anything to say about: two
// banks, one credit transfer between them, carried to finality so that both
// ends of every crossing have written their half down.
func carriedPayment(t *testing.T, s *server) (a, b seededBank, payid string) {
	t.Helper()
	a, b, _ = threeBanks(t, s)
	doJSON(t, csmSurface(s), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)
	payid = sct(t, s, a, b, "logged-e2e")
	settle(t, s)
	return a, b, payid
}

func messagesOn(t *testing.T, h http.Handler, query string) []api.MessageDTO {
	t.Helper()
	var out []api.MessageDTO
	getJSON(t, h, "/messages"+query, &out)
	return out
}

// A listener answers its own institution's log and there is no route on any of
// them that answers another's. A bank's counterparties are the two hosts it
// dials, and never the bank at the other end of the payment.
func TestAListenerAnswersOnlyItsOwnMessages(t *testing.T) {
	s := newServer(t, nil)
	a, b, _ := carriedPayment(t, s)

	mine := messagesOn(t, bankSurface(s, a.pid), "")
	if len(mine) == 0 {
		t.Fatal("the payer's bank served no messages at all, so this test would pass on nothing")
	}
	for _, m := range mine {
		if m.Counterparty == b.bic {
			t.Errorf("the payer's bank served a file %s the payee's bank; the two never address each other", m.Direction)
		}
	}

	// The payee's bank's log is its own and reached on its own port. The two
	// overlap in nothing, which is what N+2 databases behind N+2 listeners means.
	theirs := messagesOn(t, bankSurface(s, b.pid), "")
	if len(theirs) == 0 {
		t.Fatal("the payee's bank served no messages at all")
	}
	for _, m := range mine {
		for _, o := range theirs {
			if m.Seq == o.Seq && m.MsgID == o.MsgID && m.Direction == o.Direction {
				t.Errorf("both banks' listeners served the same file %s as %s", m.MsgID, m.Direction)
			}
		}
	}

	// The clearing house is an end of both banks' crossings, so its own log names
	// both — which is its half of each and not a merge of theirs.
	seen := map[string]bool{}
	for _, m := range messagesOn(t, csmSurface(s), "") {
		seen[m.Counterparty] = true
	}
	for _, bic := range []string{a.bic, b.bic} {
		if !seen[bic] {
			t.Errorf("the clearing house served no traffic with %s, which it clears for", bic)
		}
	}
}

// A payment reaches the files that carried it, over the route a payment detail
// page asks. The id on the wire is the submitting bank's and crosses unchanged,
// so each institution's own listener answers from its own log.
func TestAPaymentReachesItsDocumentsOverHTTP(t *testing.T) {
	s := newServer(t, nil)
	a, b, payid := carriedPayment(t, s)

	for _, who := range []struct {
		what string
		h    http.Handler
	}{
		{"the payer's bank", bankSurface(s, a.pid)},
		{"the clearing house", csmSurface(s)},
		{"the payee's bank", bankSurface(s, b.pid)},
	} {
		carried := messagesOn(t, who.h, "?payment="+payid)
		if len(carried) == 0 {
			t.Errorf("%s holds a copy of %s and served no file it can be reached from", who.what, payid)
			continue
		}
		for _, m := range carried {
			if len(m.Payments) == 0 || !strings.Contains(strings.Join(m.Payments, " "), payid) {
				t.Errorf("%s served %s as carrying %s, and it names %v", who.what, m.MsgID, payid, m.Payments)
			}
		}
	}

	// The settlement agent holds no copy of a payment, and its files name the
	// cut-off rather than the payments netted into it.
	if got := messagesOn(t, cbSurface(s), "?payment="+payid); len(got) != 0 {
		t.Errorf("the settlement agent served %d files for %s; a cut-off's positions name no payment", len(got), payid)
	}
}

// The listing is an INDEX and the document is a resource. A page of a log that
// keeps every file forever must not carry the files, and the second route is
// where the bytes are.
func TestAListingIndexesTheFilesAndOneRouteServesThem(t *testing.T) {
	s := newServer(t, nil)
	a, _, payid := carriedPayment(t, s)
	surface := bankSurface(s, a.pid)

	submitted := messagesOn(t, surface, "?payment="+payid+"&direction=sent")
	if len(submitted) == 0 {
		t.Fatal("the payer's bank served no file it says it sent carrying its own payment")
	}
	first := submitted[0]
	if want := (iso20022.Pacs008{}).MessageDefinitionIdentifier(); first.MsgDefIdr != want {
		t.Errorf("the payer's bank submitted %s in a %s, want a %s", payid, first.MsgDefIdr, want)
	}
	if first.PayloadSize == 0 {
		t.Error("the listing says the file is empty; a size is how an index says a document is there")
	}

	// The bytes arrive from the second route, and they are the document that
	// names the payment.
	path := "/messages/" + strconv.FormatInt(first.Seq, 10)
	var doc api.MessageDocumentDTO
	getJSON(t, surface, path, &doc)
	if doc.Seq != first.Seq || doc.MsgID != first.MsgID {
		t.Fatalf("%s served message %d (%s), want %d (%s)", path, doc.Seq, doc.MsgID, first.Seq, first.MsgID)
	}
	if len(doc.Document) != doc.PayloadSize {
		t.Errorf("the document is %d bytes and the index said %d", len(doc.Document), doc.PayloadSize)
	}
	if !strings.Contains(doc.Document, payid) {
		t.Errorf("the file that carried %s does not name it: %s", payid, doc.Document)
	}

	// And a seq is one institution's own: the same number on another listener
	// names that institution's message or nothing, never this one's.
	assertStatus(t, cbSurface(s), "GET", "/messages/999999", "", http.StatusNotFound)
	assertStatus(t, surface, "GET", "/messages/not-a-seq", "", http.StatusBadRequest)
}

// A direction that is neither half of a crossing is refused, rather than
// quietly answering the empty listing that would look like an institution with
// no traffic.
func TestAMessageListingRefusesADirectionThatIsNeitherHalf(t *testing.T) {
	s := newServer(t, nil)
	a, _, _ := carriedPayment(t, s)

	assertStatus(t, bankSurface(s, a.pid), "GET", "/messages?direction=sideways", "", http.StatusBadRequest)

	sent := messagesOn(t, bankSurface(s, a.pid), "?direction=sent")
	received := messagesOn(t, bankSurface(s, a.pid), "?direction=received")
	if len(sent) == 0 || len(received) == 0 {
		t.Fatalf("the payer's bank served %d sent and %d received files; it did both", len(sent), len(received))
	}
	for _, m := range sent {
		if m.Direction != string(payment.MessageSent) {
			t.Errorf("a listing narrowed to sent files served one it %s", m.Direction)
		}
	}
}
