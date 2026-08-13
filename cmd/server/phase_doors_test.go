package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/raphi011/cbs/api"
	"github.com/raphi011/cbs/node"
	"github.com/raphi011/cbs/payment"
)

// The phase doors: the same business day AdvanceDay runs, one step at a time.
// What the day-order suite asserts about the declaration, these assert about
// what an operator can reach — and the one property a door must not acquire is
// the ability to compose a sequence of its own.

func phasesOn(t *testing.T, s *server) []api.PhaseDTO {
	t.Helper()
	var out []api.PhaseDTO
	getJSON(t, cbSurface(s), "/clock/phases", &out)
	return out
}

// step opens one door and hands back what it moved.
func step(t *testing.T, s *server, key string) api.PhaseReportDTO {
	t.Helper()
	rec := do(t, cbSurface(s), "POST", "/clock/phases/"+key, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /clock/phases/%s: got %d, want 200 (body: %s)", key, rec.Code, rec.Body.String())
	}
	var out api.PhaseReportDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
	return out
}

// TestEveryPhaseTheDayDeclaresIsADoorAndNothingElseIs is the listing held to
// the declaration: a door per phase, in the day's own order, and no door for
// anything a sequence composed.
func TestEveryPhaseTheDayDeclaresIsADoorAndNothingElseIs(t *testing.T) {
	s := newServer(t, nil)
	got := phasesOn(t, s)

	want := append(namesOf(beforeClock), namesOf(afterClock)...)
	names := make([]string, 0, len(got))
	for _, p := range got {
		names = append(names, p.Name)
	}
	if !slices.Equal(names, want) {
		t.Errorf("the doors are\n %s\nwant %s", strings.Join(names, " → "), strings.Join(want, " → "))
	}

	// The narrowed collection is a phase a SEQUENCE holds and the day does not
	// declare, so it is not a door. A caller composes a derived run out of the
	// doors; it does not reach for a phase that was cut to fit one.
	if slices.Contains(names, collectClearingHouseOnly.name) {
		t.Errorf("%q is a door, and the day declares no such phase", collectClearingHouseOnly.name)
	}
	assertStatus(t, cbSurface(s), "POST",
		"/clock/phases/"+phaseKey(collectClearingHouseOnly.name), "", http.StatusBadRequest)
}

// TestAPhaseIsNamedAndNeverParameterised is the property the doors exist to
// preserve. A door takes a name and nothing else: no list, no range, no phase
// the day did not declare.
func TestAPhaseIsNamedAndNeverParameterised(t *testing.T) {
	s := newServer(t, nil)

	for _, spelling := range []string{
		"clearing,settlement",     // two of them
		"clearing..settlement",    // and a range between two
		"clearing%20cut-off",      // a name that is not a phase's
		"Clearing",                // a phase's name, spelt otherwise
		"clearing-house%20cutoff", // and one nearly right
	} {
		t.Run(spelling, func(t *testing.T) {
			assertStatus(t, cbSurface(s), "POST", "/clock/phases/"+spelling, "", http.StatusBadRequest)
		})
	}

	// A key is a name spelt for a URL, so every one of them survives a round trip
	// through the path it is named in.
	for _, p := range phasesOn(t, s) {
		if p.Key != phaseKey(p.Name) {
			t.Errorf("the door for %q is keyed %q", p.Name, p.Key)
		}
		if strings.ContainsAny(p.Key, " /?&%") {
			t.Errorf("the key %q for %q needs escaping to be named in a path", p.Key, p.Name)
		}
	}
}

// TestAReaderCanRunTheClearingAndStop is what the doors are for: a payment
// carried one hop at a time, with the clock standing still and each phase
// moving exactly its own files.
func TestAReaderCanRunTheClearingAndStop(t *testing.T) {
	s := newServer(t, nil)
	a, b, _ := threeBanks(t, s)
	doJSON(t, csmSurface(s), "POST", "/cycles", `{"scheme":"sepa.ct"}`, http.StatusCreated)

	// Submitted and NOT carried: the instruction is sitting in its bank's hub,
	// which is where every one of these fixtures starts.
	payid := doJSON(t, csmSurface(s), "POST", "/payments", `{
		"scheme":"sepa.ct",
		"debtor":{"account":"`+a.account+`","identifier":{"scheme":"IBAN","value":"`+a.iban+`"}},
		"creditor":{"account":"`+b.account+`","identifier":{"scheme":"IBAN","value":"`+b.iban+`"}},
		"amount":10000,
		"endToEndId":"stepped-e2e",
		"creditorName":"`+b.accountName+`"
	}`, http.StatusAccepted)["id"].(string)

	day := doJSON(t, cbSurface(s), "GET", "/clock", "", http.StatusOK)["date"]

	// One: the payer's bank reaches its cut-off, and the pacs.008 is uploaded.
	cutoff := step(t, s, "bank-cut-off")
	assertPutTo(t, cutoff, a.bic, string(testConfig.ClearingHouseBIC))
	if got := paymentOn(t, bankSurface(s, a.pid), payid); got.Status != payment.Initiated.String() {
		t.Errorf("the payer's bank calls its payment %s once the file is uploaded, want %s — nobody has answered it yet",
			got.Status, payment.Initiated)
	}

	// Two: the clearing house works through what it was sent, and answers. Its
	// own copy exists from here, and the payer's bank has not heard yet.
	work := step(t, s, "clearing")
	if len(work.Outcomes) == 0 {
		t.Fatal("the clearing phase decided nothing about the transaction it was sent")
	}
	if got := paymentOn(t, csmSurface(s), payid); got.Status != payment.Accepted.String() {
		t.Errorf("the clearing house calls the payment %s, want %s", got.Status, payment.Accepted)
	}
	if got := paymentOn(t, bankSurface(s, a.pid), payid); got.Status != payment.Initiated.String() {
		t.Errorf("the payer's bank calls its payment %s before collecting, want %s — the answer is in a queue",
			got.Status, payment.Initiated)
	}

	// Three: it collects, and only now does it know. That gap between a file put
	// in a queue and a file taken out of one is the thing the doors make visible.
	collect := step(t, s, "collection")
	assertTakenBy(t, collect, a.bic)
	if got := paymentOn(t, bankSurface(s, a.pid), payid); got.Status != payment.Accepted.String() {
		t.Errorf("the payer's bank calls its payment %s after collecting, want %s", got.Status, payment.Accepted)
	}

	// And the payee's bank still holds nothing at all: the cycle has not settled,
	// so the output file has not been released. Settle-before-release, stopped
	// in the middle and looked at.
	var theirs []api.PaymentDTO
	getJSON(t, bankSurface(s, b.pid), "/payments", &theirs)
	if len(theirs) != 0 {
		t.Errorf("the payee's bank holds %d payments before the cycle settled, want none", len(theirs))
	}

	// Through all of it the clock stood still. A phase is a step inside a day and
	// only advancing the day ends one.
	if now := doJSON(t, cbSurface(s), "GET", "/clock", "", http.StatusOK)["date"]; now != day {
		t.Errorf("the business date moved from %v to %v while phases ran", day, now)
	}
	if cutoff.Ran.Date != day {
		t.Errorf("a phase reported running on %s, want %v", cutoff.Ran.Date, day)
	}
}

// TestAPhaseRunsOnADayNothingSettles pins settlementOnly as AdvanceDay's
// question rather than a door's: a caller that named a phase has already
// decided it wants it.
func TestAPhaseRunsOnADayNothingSettles(t *testing.T) {
	s := newServer(t, nil)
	for _, p := range phasesOn(t, s) {
		if p.Name == "clearing" && !p.SettlementOnly {
			t.Fatal("the clearing phase is no longer settlement-only, and this case asserts nothing")
		}
	}
	// The fixed date the suites run on settles, so the weekend is reached by
	// advancing to it rather than by asserting anything about the calendar here.
	for i := 0; s.dep.BusinessDate().SettlementDay; i++ {
		if i == 7 {
			t.Fatal("a week of advances reached no day the scheme is shut on")
		}
		if _, err := s.dep.AdvanceDay(t.Context()); err != nil {
			t.Fatalf("AdvanceDay: %v", err)
		}
	}
	step(t, s, "clearing")
}

// assertPutTo fails unless the report holds a file one institution put where
// another could collect it.
func assertPutTo(t *testing.T, r api.PhaseReportDTO, from, to string) {
	t.Helper()
	for _, f := range r.Files {
		if f.From == from && f.To == to && f.Movement == string(node.FilePut) {
			return
		}
	}
	t.Errorf("no file was put by %s for %s; the phase moved %+v", from, to, r.Files)
}

// assertTakenBy fails unless the report holds a file an institution collected.
func assertTakenBy(t *testing.T, r api.PhaseReportDTO, to string) {
	t.Helper()
	for _, f := range r.Files {
		if f.To == to && f.Movement == string(node.FileTaken) {
			return
		}
	}
	t.Errorf("%s collected nothing; the phase moved %+v", to, r.Files)
}

func paymentOn(t *testing.T, h http.Handler, payid string) api.PaymentDTO {
	t.Helper()
	var out api.PaymentDTO
	getJSON(t, h, "/payments/"+payid, &out)
	return out
}
