package main

import (
	"context"
	"errors"
	"testing"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/payment"
)

// The message log, asserted across the deployment — which is the only place it
// can be, because each institution holds one half of every crossing and no
// institution may read another's.

// messagesOf is one institution's own log, read through its own network.
func messagesOf(t *testing.T, list func(context.Context, payment.MessageFilter) ([]payment.Message, error),
	f payment.MessageFilter,
) []payment.Message {
	t.Helper()
	ms, err := list(context.Background(), f)
	if err != nil {
		t.Fatalf("listing messages: %v", err)
	}
	return ms
}

// logs is every institution's message log, keyed by the institution that holds
// it. Assembling it is the DEPLOYMENT's act, for payment/recon's reason: it may
// open every institution at once precisely because no institution may.
func (h *harness) logs(t *testing.T) map[iso20022.BIC][]payment.Message {
	t.Helper()
	out := map[iso20022.BIC][]payment.Message{}
	for _, bic := range []iso20022.BIC{h.debtorBIC, h.creditorBIC} {
		out[bic] = messagesOf(t, h.bank(bic).ListMessages, payment.MessageFilter{})
	}
	out[h.cfg.ClearingHouseBIC] = messagesOf(t, h.nets.ClearingHouse().ListMessages, payment.MessageFilter{})
	out[h.cfg.CentralBankBIC] = messagesOf(t, h.cb().ListMessages, payment.MessageFilter{})
	return out
}

// A crossing is an event BOTH endpoints observed, and each writes down its own
// half. So over a whole business day the files one institution says it sent to
// another are exactly the files that other says it received from it.
func TestEveryFileThatMovesIsLoggedAtBothEnds(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.day(t)

	logs := h.logs(t)

	// A crossing is named by its two ends and the order id the host minted, which
	// is what the two halves have in common.
	type crossing struct {
		from, to iso20022.BIC
		order    string
	}
	sent := map[crossing]int{}
	received := map[crossing]int{}
	for holder, ms := range logs {
		for _, m := range ms {
			switch m.Direction {
			case payment.MessageSent:
				sent[crossing{from: holder, to: m.Counterparty, order: m.OrderID}]++
			case payment.MessageReceived:
				received[crossing{from: m.Counterparty, to: holder, order: m.OrderID}]++
			default:
				t.Errorf("%s recorded a file as %q, which is neither half of a crossing", holder, m.Direction)
			}
		}
	}
	if len(sent) == 0 {
		t.Fatal("no institution recorded sending anything, so this test would pass on nothing")
	}

	for c, n := range sent {
		if got := received[c]; got != n {
			t.Errorf("%s says it sent %s to %s %d time(s) and %s says it received it %d time(s)",
				c.from, c.order, c.to, n, c.to, got)
		}
	}
	for c, n := range received {
		if got := sent[c]; got != n {
			t.Errorf("%s says it received %s from %s %d time(s) and %s says it sent it %d time(s)",
				c.to, c.order, c.from, n, c.from, got)
		}
	}
}

// A payment reaches its own documents, at every institution that holds a copy
// of it. The id on the wire is the submitting bank's and crosses unchanged,
// which is what lets three separate logs answer the same question.
func TestAPaymentReachesTheFilesThatCarriedIt(t *testing.T) {
	h := newHarness(t)
	p := h.submitCreditTransfer(t)
	h.day(t)

	carried := func(holder iso20022.BIC, list func(context.Context, payment.MessageFilter) ([]payment.Message, error)) []payment.Message {
		t.Helper()
		ms := messagesOf(t, list, payment.MessageFilter{PaymentID: p.ID})
		if len(ms) == 0 {
			t.Errorf("%s holds a copy of %s and no file it can reach from it", holder, p.ID)
		}
		for _, m := range ms {
			if m.MsgDefIdr == "" {
				t.Errorf("%s reached an unreadable file from %s; a document is what a viewer renders", holder, p.ID)
			}
		}
		return ms
	}

	// The submitting bank sent the pacs.008 that carried it, and was told what
	// became of it.
	submitted := carried(h.debtorBIC, h.bank(h.debtorBIC).ListMessages)
	if submitted[0].Direction != payment.MessageSent {
		t.Errorf("the first file the payer's bank reaches from %s is one it %s; it submitted the payment",
			p.ID, submitted[0].Direction)
	}
	if want := (iso20022.Pacs008{}).MessageDefinitionIdentifier(); submitted[0].MsgDefIdr != want {
		t.Errorf("the payer's bank submitted %s in a %s, want a %s", p.ID, submitted[0].MsgDefIdr, want)
	}

	// The clearing house was handed it and handed it on, so it holds both halves.
	csm := carried(h.cfg.ClearingHouseBIC, h.nets.ClearingHouse().ListMessages)
	var in, out int
	for _, m := range csm {
		switch m.Direction {
		case payment.MessageReceived:
			in++
		case payment.MessageSent:
			out++
		}
	}
	if in == 0 || out == 0 {
		t.Errorf("the clearing house reaches %d received and %d sent files from %s; it was handed the payment and handed it on",
			in, out, p.ID)
	}

	// And the receiving bank was handed it, which is the far end of the trail.
	carried(h.creditorBIC, h.bank(h.creditorBIC).ListMessages)

	// The settlement agent never sees this payment as a row, and its files name
	// the cut-off rather than the payments netted into it. Reaching it from a
	// payment id is not something this institution can do, and that is the domain.
	if ms := messagesOf(t, h.cb().ListMessages, payment.MessageFilter{PaymentID: p.ID}); len(ms) != 0 {
		t.Errorf("the settlement agent reaches %d files from %s; a cut-off's positions name no payment", len(ms), p.ID)
	}
}

// A seq is one an institution's own listing named, and one it never allocated
// is not found. The first seq is 1, so a zero is a caller that composed one.
func TestASeqNoListingNamedIsNotFound(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.day(t)

	for _, seq := range []int64{0, -1} {
		if m, err := h.bank(h.debtorBIC).GetMessage(context.Background(), seq); !errors.Is(err, payment.ErrMessageNotFound) {
			t.Errorf("seq %d answered %q (%v), want it not found; a filter reads a seq below the first as no filter at all",
				seq, m.MsgDefIdr, err)
		}
	}
}

// A seq counts ONE institution's own traffic, so the same number at another
// institution names that institution's message or nothing at all. It is never
// the same file, which is why nothing may carry a seq across a listener.
func TestASeqNamesNothingAtAnotherInstitution(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.day(t)

	mine := messagesOf(t, h.bank(h.debtorBIC).ListMessages, payment.MessageFilter{})
	if len(mine) == 0 {
		t.Fatal("the payer's bank recorded no traffic at all, so there is no seq to carry")
	}
	elsewhere := []struct {
		holder iso20022.BIC
		get    func(context.Context, int64) (payment.Message, error)
	}{
		{h.creditorBIC, h.bank(h.creditorBIC).GetMessage},
		{h.cfg.ClearingHouseBIC, h.nets.ClearingHouse().GetMessage},
		{h.cfg.CentralBankBIC, h.cb().GetMessage},
	}
	for _, m := range mine {
		for _, other := range elsewhere {
			got, err := other.get(context.Background(), m.Seq)
			if errors.Is(err, payment.ErrMessageNotFound) {
				continue
			}
			if err != nil {
				t.Fatalf("%s answering seq %d: %v", other.holder, m.Seq, err)
			}
			if got.MsgID == m.MsgID && got.Direction == m.Direction {
				t.Errorf("seq %d names the same file at %s and at %s; a seq that travelled between "+
					"two logs would let a viewer read one institution's document off another's listing",
					m.Seq, h.debtorBIC, other.holder)
			}
		}
	}
}

// One institution's log is one institution's, and the filter is what a listing
// is built out of.
func TestAnInstitutionsLogHoldsOnlyItsOwnTraffic(t *testing.T) {
	h := newHarness(t)
	h.submitCreditTransfer(t)
	h.day(t)

	// The payer's bank dials the clearing house and the settlement agent and
	// nothing else, so its counterparties are those two and never the other bank.
	for _, m := range messagesOf(t, h.bank(h.debtorBIC).ListMessages, payment.MessageFilter{}) {
		if m.Counterparty == h.creditorBIC {
			t.Errorf("the payer's bank recorded a file %s the payee's bank; the two never address each other", m.Direction)
		}
	}

	// And a counterparty filter answers one connection's traffic.
	toCSM := messagesOf(t, h.bank(h.debtorBIC).ListMessages,
		payment.MessageFilter{Counterparty: h.cfg.ClearingHouseBIC})
	if len(toCSM) == 0 {
		t.Fatal("the payer's bank recorded no traffic with the clearing house at all")
	}
	for _, m := range toCSM {
		if m.Counterparty != h.cfg.ClearingHouseBIC {
			t.Errorf("a filtered listing answered a file with %s", m.Counterparty)
		}
	}
}
