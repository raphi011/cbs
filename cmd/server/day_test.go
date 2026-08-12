package main

import (
	"testing"
	"time"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// The business day, which is the one thing in this package that is new rather
// than moved.

// One advance is the whole life of a payment, and that is the shape of a
// deployment that closes one cycle a day.
func TestOneBusinessDayCarriesAPaymentFromSubmissionToFinality(t *testing.T) {
	h := newHarness(t)
	p := h.submitCreditTransfer(t)

	// Before the day: the file is in the clearing house's order log and no
	// institution has looked inside it.
	if got := h.bankPayment(t, h.debtorBIC, p.ID); got.Status != payment.Initiated {
		t.Fatalf("the payer's bank records %v before the day runs, want Initiated", got.Status)
	}

	report := h.day(t)

	if !report.Ran.SettlementDay {
		t.Fatalf("the fixture's first day is %s, which is not a settlement day; every flow test here assumes one",
			report.Ran.Date.Format(time.DateOnly))
	}
	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Fatalf("the payment is %v after one business day, want Settled", got.Status)
	}
	// And the money is where a settled payment puts it: out of the payer's
	// account, into the payee's.
	if bal := h.balance(t, h.creditorPID, h.creditorAcct.ID); bal != harnessAmount {
		t.Errorf("the payee holds %d after one business day, want %d", bal, harnessAmount)
	}
	if bal := h.suspense(t, h.debtorPID); bal != 0 {
		t.Errorf("the payer's bank still holds %d in clearing suspense, want 0", bal)
	}
	if len(report.Problems) != 0 {
		t.Errorf("an ordinary day reported %d problems: %v", len(report.Problems), report.Problems)
	}
}

// The clock moves ONE CALENDAR DAY, weekends included.
func TestABusinessDayLeavesTheClockOnTheNextCalendarDay(t *testing.T) {
	h := newHarness(t)

	// Wednesday, Thursday, Friday, Saturday: four advances from the fixture's
	// own anchor, and the fourth is the one that finds the scheme shut.
	want := []struct {
		ran        string
		settlement bool
	}{
		{"2025-01-15", true},  // Wednesday
		{"2025-01-16", true},  // Thursday
		{"2025-01-17", true},  // Friday
		{"2025-01-18", false}, // Saturday
	}
	for _, w := range want {
		report := h.day(t)
		if got := report.Ran.Date.Format(time.DateOnly); got != w.ran {
			t.Fatalf("a day ran on %s, want %s", got, w.ran)
		}
		if report.Ran.SettlementDay != w.settlement {
			t.Errorf("%s reports settlementDay=%v, want %v", w.ran, report.Ran.SettlementDay, w.settlement)
		}
		// A weekend is shut and has no NAME, which is the distinction
		// calendar.Holiday draws: only a TARGET holiday is a closure a console can
		// name.
		if report.Ran.Holiday != "" {
			t.Errorf("%s is named %q; only a TARGET holiday has a name", w.ran, report.Ran.Holiday)
		}
	}
}

// Nothing clears on a day the scheme is shut, and the date moves anyway.
func TestNothingClearsOnADayTheSchemeIsShut(t *testing.T) {
	h := newHarness(t)

	// Three advances to reach the Saturday, and the payment is submitted after
	// them so that the shut day is the first that could have carried it.
	for range 3 {
		h.day(t)
	}
	p := h.submitCreditTransfer(t)

	report := h.day(t)
	if report.Ran.SettlementDay {
		t.Fatalf("this fixture reached %s, which is open; the test needs a day the scheme is shut",
			report.Ran.Date.Format(time.DateOnly))
	}
	if got := h.bankPayment(t, h.debtorBIC, p.ID); got.Status != payment.Initiated {
		t.Errorf("the payer's bank records %v after a day the scheme was shut, want Initiated", got.Status)
	}
	// NO file moved at all.
	if len(report.Files) != 0 {
		t.Errorf("a shut day moved %v; no file may cross on one", report.Files)
	}
	if len(report.Outcomes) != 0 {
		t.Errorf("a shut day decided %v; no institution may decide anything about a payment on one", report.Outcomes)
	}

	// And the next open day carries it, which is what makes the wait a delay
	// rather than a loss.
	h.day(t)
	h.day(t)
	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Errorf("the payment is %v after the scheme reopened, want Settled", got.Status)
	}
}

// The report is what an operator watches, and it names three different kinds of
// thing: which files moved, who decided what about which payment, and what
// nobody could get through.
func TestTheDayReportNamesWhatMovedAndWhoDecidedWhat(t *testing.T) {
	h := newHarness(t)
	p := h.submitCreditTransfer(t)
	report := h.day(t)

	// Every file names both ends and the order id its host minted. An order id is
	// what a subscriber asks HAC about, so a report without one would describe a
	// file nobody could go and look up.
	for _, f := range report.Files {
		if f.From == "" || f.To == "" || f.OrderID == "" || f.OrderType == "" {
			t.Errorf("a file in the report is %+v; every one names both ends, a type and an order id", f)
		}
	}
	// The payment's own life, in the order it was decided — and both decisions are
	// the CLEARING HOUSE's, which is what settling before releasing did to the
	// report.
	var decided []iso20022.BIC
	for _, o := range report.Outcomes {
		if o.Payment != p.ID {
			continue
		}
		decided = append(decided, o.DecidedBy)
	}
	want := []iso20022.BIC{h.cfg.ClearingHouseBIC, h.cfg.ClearingHouseBIC}
	if len(decided) != len(want) {
		t.Fatalf("the report carries %v for this payment, want %v", decided, want)
	}
	for i := range want {
		if decided[i] != want[i] {
			t.Errorf("decision %d was made by %s, want %s", i, decided[i], want[i])
		}
	}
}

// The report is TAKEN at the end of a day, so no file is reported twice.
func TestTwoDaysDoNotReportTheSameFileTwice(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)

	first := h.day(t)
	if len(first.Files) == 0 {
		t.Fatal("the first day moved no files, so this test would pass on nothing")
	}
	second := h.day(t)
	if len(second.Files) != 0 {
		t.Errorf("the second day re-reported %v; a report carries what happened since the last one", second.Files)
	}
	if len(second.Outcomes) != 0 {
		t.Errorf("the second day re-reported %v decisions", len(second.Outcomes))
	}
}

// The day cuts every open cycle off and opens a fresh one per scheme, which is
// what makes the NEXT day able to clear anything at all.
func TestADayLeavesOneOpenCyclePerScheme(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.day(t)

	open := map[payment.SchemeID]int{}
	for _, c := range h.cycles(t) {
		if c.Status == payment.CycleOpen {
			open[c.Scheme]++
		}
	}
	for _, scheme := range h.net.ListSchemes() {
		if open[scheme.ID()] != 1 {
			t.Errorf("%s has %d open cycles after a business day, want exactly 1", scheme.ID(), open[scheme.ID()])
		}
	}
	// And the cycle the payment cleared in is settled rather than left standing.
	var settled int
	for _, c := range h.cycles(t) {
		if c.Status == payment.CycleSettled {
			settled++
		}
	}
	if settled == 0 {
		t.Error("no cycle settled, so the cut-off this day reached instructed nothing")
	}
}

// A cycle the day opens is stamped with the day it will accept payments on, not
// the one that just ran.
func TestTheCycleADayOpensNamesTheDayItWillClear(t *testing.T) {
	h := newHarness(t)
	report := h.day(t)

	for _, c := range h.cycles(t) {
		if c.Status != payment.CycleOpen {
			continue
		}
		if got := c.OpenedAt.Format(time.DateOnly); got != report.Next.Date.Format(time.DateOnly) {
			t.Errorf("%s stands open having been opened on %s; the day it accepts payments on is %s",
				c.ID, got, report.Next.Date.Format(time.DateOnly))
		}
	}
}

// Every member's routing directory is refreshed FIRST, before anything is
// routed.
func TestADayRefreshesEveryMembersRoutingDirectoryBeforeItRoutesAnything(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()

	// A third bank, admitted after the fixture's own members last pulled. Nobody
	// can address it yet: the payer's own copy of the directory does not carry
	// its bank code, so an address issued under it resolves to nothing.
	joiner := h.provision(t, "Nordhaven Bank", "NORDSESSXXX", euroOnly)
	acct := h.openCustomer(t, joiner, "Nora", "EUR", 0)

	req := h.creditTransferRequestTo(t, addressOf(t, acct))
	if _, err := h.dep.Submit(ctx, req); err == nil {
		t.Fatal("the payer's bank addressed a bank its own directory does not carry")
	}

	h.day(t)

	// And now it can, on a copy nothing but the day refreshed.
	p, err := h.dep.Submit(ctx, req)
	if err != nil {
		t.Fatalf("submitting after the day refreshed the directory: %v", err)
	}
	h.day(t)
	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Fatalf("the payment to the newly admitted bank is %v, want Settled", got.Status)
	}
}
