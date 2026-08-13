package main

import (
	"context"
	"errors"
	"testing"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/ebics"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/node/bank"
	"github.com/raphi011/cbs/payment"
)

// TestABankAdmittedAfterTheLastRefreshCannotBePaidUntilTheNextOne is the case
// that says what the routing directory IS.
func TestABankAdmittedAfterTheLastRefreshCannotBePaidUntilTheNextOne(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	// A third member, admitted after the two in the fixture pulled their copies.
	// Its admission finishes in full: it holds a settlement account, it is in the
	// roster, and it has a customer with an address of its own.
	joiner := h.provision(t, "Nordhaven Bank", "NORDSESSXXX", euroOnly)
	h.work(t)
	joiner = h.getBank(t, joiner.ID)
	if joiner.Assets["EUR"].Settlement == "" {
		t.Fatal("the joining bank holds no settlement account after its admission")
	}
	if _, err := h.net.GetRosterEntryByBIC(ctx, joiner.BIC); err != nil {
		t.Fatalf("the joining bank is not in the roster: %v", err)
	}
	nora := h.openCustomer(t, joiner, "Nora", "EUR", 0)

	// Alice pays Nora. The instruction names no bank — it never does — so Aurora
	// derives one from Nora's address, out of the copy it holds.
	toNora := payment.InitiatePaymentRequest{
		Scheme:          payment.SchemeSEPACT,
		Debtor:          h.debtorRef(),
		Creditor:        payment.PartyRef{Account: nora.ID, Identifier: nora.Identifiers[0]},
		Amount:          harnessAmount,
		Description:     "invoice 44",
		CreditorDetails: payment.PartyDetails{Name: nora.Name},
		DebtorDetails:   payment.PartyDetails{Agent: h.debtorBIC, Name: h.debtorAcct.Name},
	}

	mark := h.messagesSeen()
	_, err := h.dep.Submit(ctx, toNora)
	if !errors.Is(err, payment.ErrBankCodeUnknown) {
		t.Fatalf("paying a member admitted since the last refresh = %v, want ErrBankCodeUnknown", err)
	}
	// Nothing left Aurora, and nothing moved. A refusal that had already debited
	// the payer would be the failure payment.TakeInstruction exists to prevent,
	// arriving through a new door.
	if n := len(h.messagesFrom(mark)); n != 0 {
		t.Errorf("a refused submission put %d messages on the wire, want 0", n)
	}
	if bal := h.suspense(t, h.debtorPID); bal != 0 {
		t.Errorf("clearing suspense = %d after a refusal, want 0", bal)
	}

	// The roster has said so all along. What was missing is that nobody had told
	// Aurora, and this is the assertion that the refusal is about the COPY rather
	// than about the scheme.
	if _, err := h.bank(h.debtorBIC).ResolveBankCode(ctx, joiner.Issuer); !errors.Is(err, payment.ErrBankCodeUnknown) {
		t.Errorf("the payer's own copy answers %v for the joining bank's allocation, want ErrBankCodeUnknown", err)
	}

	// One collection, made by the subscriber, and the same instruction goes
	// through. The joining bank is already in the published table: admitting it
	// is what republished one.
	h.refresh(t, h.debtorBIC)
	p, err := h.dep.Submit(ctx, toNora)
	if err != nil {
		t.Fatalf("after the refresh, the same payment: %v", err)
	}
	h.work(t)
	if got := h.payment(t, p.ID); got.Status != payment.Accepted {
		t.Fatalf("the payment after the refresh is %v, want Accepted", got.Status)
	}
	// Through the cut-off, because the joining bank is handed the instruction
	// only once the cycle carrying it is final.
	h.closeCycle(t)
	h.work(t)
	// And it went where the address said, derived rather than typed.
	if got := h.payment(t, p.ID).CreditorDetails.Agent; got != joiner.BIC {
		t.Errorf("the derived creditor agent is %q, want the joining bank's %q", got, joiner.BIC)
	}
	if n := h.messagesSentTo(joiner.BIC, "pacs.008.001.08"); n != 1 {
		t.Errorf("the joining bank was handed %d credit transfers, want 1", n)
	}
}

// A refresh delivers ONLY the two things a member's copy carries, and each is
// there for a reason the others are not.
func TestARefreshCarriesTheAllocationAndTheAddressAndNothingElse(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	mark := h.messagesSeen()

	got := h.refresh(t, h.debtorBIC)

	// A refresh is ONE file collected, and it is the one file on this transport
	// that is not an ISO 20022 message: a routing table is host data, not a
	// conversation between two banks.
	crossed := h.messagesFrom(mark)
	if len(crossed) != 1 {
		t.Fatalf("a refresh put %d files on the wire, want 1", len(crossed))
	}
	if _, err := iso20022.Unmarshal(crossed[0].raw); err == nil {
		t.Error("the routing table parses as an ISO 20022 message, and a routing table is not one")
	}
	if crossed[0].to != h.debtorBIC || crossed[0].from != h.cfg.ClearingHouseBIC {
		t.Errorf("the routing table crossed from %s to %s, want the clearing house to the subscriber",
			crossed[0].from, crossed[0].to)
	}

	published, err := h.net.ListRosterEntries(ctx)
	if err != nil {
		t.Fatalf("ListRosterEntries: %v", err)
	}
	if len(got) != len(published) {
		t.Fatalf("the copy holds %d entries and the roster publishes %d", len(got), len(published))
	}
	byBIC := map[iso20022.BIC]payment.DirectoryEntry{}
	for _, e := range got {
		byBIC[e.BIC] = e
	}
	for _, want := range published {
		e, ok := byBIC[want.BIC]
		if !ok {
			t.Errorf("the copy holds no entry for %s", want.BIC)
			continue
		}
		if e.Issuer != want.Issuer {
			t.Errorf("%s is copied under %v, want the published %v", want.BIC, e.Issuer, want.Issuer)
		}
		if e.RefreshedAt.IsZero() {
			t.Errorf("%s is copied with no refresh instant; a console has nothing to show", want.BIC)
		}
	}

	// And the same rows through the domain's own read, which is what a console and
	// a send form both ask.
	resolved, err := h.bank(h.debtorBIC).ResolveBankCode(ctx, h.creditor.Issuer)
	if err != nil {
		t.Fatalf("resolving the other bank's allocation: %v", err)
	}
	if resolved.BIC != h.creditorBIC {
		t.Errorf("the payee's allocation resolves to %q, want %q", resolved.BIC, h.creditorBIC)
	}

	// An allocation this scheme has given nobody resolves to nothing, out of the
	// same copy and with the same sentinel a submission meets.
	_, err = h.bank(h.debtorBIC).ResolveBankCode(ctx,
		iban.Issuer{Country: h.creditor.Issuer.Country, BankCode: "10000000"})
	if !errors.Is(err, payment.ErrBankCodeUnknown) {
		t.Errorf("resolving an unallocated code = %v, want ErrBankCodeUnknown", err)
	}
}

// A refresh is the SUBSCRIBER's own act and is recorded in the subscriber's own
// log, keyed by the subscriber.
func TestARefreshIsRecordedInTheSubscribersOwnLog(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	before := h.directoryEvents(t, h.debtorBIC)
	h.refresh(t, h.debtorBIC)

	after := h.directoryEvents(t, h.debtorBIC)
	if len(after) != len(before)+1 {
		t.Fatalf("the subscriber's log holds %d refreshes, want %d", len(after), len(before)+1)
	}
	last := after[len(after)-1]
	if last.EntityID != string(h.debtorBIC) {
		t.Errorf("the refresh is keyed by %q, want the subscriber's own %q", last.EntityID, h.debtorBIC)
	}
	if last.BookID != ledger.BookID(h.debtorBIC) {
		t.Errorf("the refresh is in %q's log, want the subscriber's own", last.BookID)
	}

	// The other member's log says nothing about it: subscribing is one bank's act
	// and reaches no other institution's records.
	if n := len(h.directoryEvents(t, h.creditorBIC)); n != len(before) {
		t.Errorf("the other member's log gained %d refreshes from a refresh it did not make",
			n-len(before))
	}
	csm, err := h.net.ListAudit(ctx, ledger.AuditFilter{
		BookID: payment.ClearingHouseBook, Scope: ledger.ScopePayment,
		Type: ledger.EventDirectoryRefreshed,
	})
	if err != nil {
		t.Fatalf("reading the clearing house's log: %v", err)
	}
	if len(csm) != 0 {
		t.Errorf("the clearing house recorded %d refreshes; it published, it did not act", len(csm))
	}
}

// A clearing house that is publishing nothing leaves a member with the copy it
// already has, which is the difference between STALE and empty: one is a state
// this scheme models and the other would take every payment down.
func TestAMemberKeepsItsCopyWhenNothingIsPublished(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	before, err := h.bank(h.debtorBIC).ListDirectory(ctx)
	if err != nil {
		t.Fatalf("ListDirectory: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("the fixture's subscriber holds no directory to keep")
	}

	// The host loses what it was offering, which is what a restart before the
	// first publish looks like from the member's side.
	h.dep.ClearingHouse().Host().Reset()
	h.dep.ClearingHouse().Host().Enrol(ebics.SubscriberID(h.debtorBIC))

	b, err := h.dep.Bank(ctx, h.debtorPID)
	if err != nil {
		t.Fatalf("opening %s: %v", h.debtorBIC, err)
	}
	if _, err := b.RefreshDirectory(ctx); !errors.Is(err, bank.ErrNoRoutingTable) {
		t.Fatalf("collecting from a host publishing nothing = %v, want ErrNoRoutingTable", err)
	}

	after, err := h.bank(h.debtorBIC).ListDirectory(ctx)
	if err != nil {
		t.Fatalf("ListDirectory after the failed collection: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("the subscriber holds %d entries after a collection that found nothing, want the %d it had",
			len(after), len(before))
	}
	// And it still routes, which is the whole point of keeping the copy.
	if _, err := h.dep.Submit(ctx, h.creditTransferRequest(t)); err != nil {
		t.Errorf("the subscriber cannot route from the copy it kept: %v", err)
	}
}

// directoryEvents is one member's own record of the snapshots it has pulled.
func (h *harness) directoryEvents(t *testing.T, bic iso20022.BIC) []ledger.AuditEvent {
	t.Helper()
	got, err := h.bank(bic).ListAudit(context.Background(), ledger.AuditFilter{
		BookID: ledger.BookID(bic),
		Scope:  ledger.ScopePayment,
		Type:   ledger.EventDirectoryRefreshed,
	})
	if err != nil {
		t.Fatalf("reading %s's log: %v", bic, err)
	}
	return got
}

// An address with no directory here still takes a BIC beside it, and refuses
// without one.
func TestAnAddressWithNoDirectoryHereIsRefusedForWantOfABIC(t *testing.T) {
	h := newHarness(t)

	req := h.creditTransferRequest(t)
	req.Creditor.Identifier = deposit.Identifier{Scheme: "PAN", Value: "4000000000000001"}

	_, err := h.dep.Submit(context.Background(), req)
	if !errors.Is(err, payment.ErrCounterpartyAgentNotNamed) {
		t.Fatalf("an address no directory here covers = %v, want ErrCounterpartyAgentNotNamed", err)
	}
}
