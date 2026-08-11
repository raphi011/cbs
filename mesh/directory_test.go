package mesh

import (
	"context"
	"errors"
	"testing"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// TestABankAdmittedAfterTheLastRefreshCannotBePaidUntilTheNextOne is the case
// that says what the routing directory IS.
//
// A member routes from a COPY of the clearing house's roster, pulled by that
// member and held in its own database. So admission does not make a bank
// payable: it makes it publishable. Between the two lies a refresh that somebody
// has to ask for, and until they do, a bank admitted this morning cannot be paid
// by a bank that pulled yesterday.
//
// # Why this is the first test of the design and not a footnote on it
//
// Every other property here would hold just as well if a payer's bank asked the
// clearing house per payment — and that system would be a different one, with no
// staleness, no subscription and a cross-institution read on the happy path of
// every submission. The refusal below is the only thing that tells the two apart
// from the outside, which is why it is written as a case rather than left to a
// comment on the table.
//
// # The refusal is safe, and one invariant is what makes it so
//
// A bank code is never reassigned. A copy that is behind is therefore INCOMPLETE
// and never WRONG: the failure mode is "I cannot route this yet" and never "I
// routed it to the wrong bank". Aurora is refused; Aurora is not made to pay a
// stranger.
//
// And it cannot say WHICH of the two situations it is in — no such member, or a
// copy behind the roster — because telling them apart would mean asking the
// clearing house, which is the lookup the subscription replaces. See
// payment.ErrBankCodeUnknown.
func TestABankAdmittedAfterTheLastRefreshCannotBePaidUntilTheNextOne(t *testing.T) {
	ctx := context.Background()
	h := newMeshHarness(t)

	// A third member, admitted after the two in the fixture pulled their copies.
	// Its admission is the whole conversation and it finishes: it holds a
	// settlement account, it is in the roster, and it has a customer with an
	// address of its own.
	joiner := h.provision(t, "Nordhaven Bank", "NORDSESSXXX", euroOnly)
	h.drain(t)
	joiner = h.getBank(t, joiner.ID)
	if joiner.Status != payment.BankMember {
		t.Fatalf("the joining bank is %q after its admission, want %q", joiner.Status, payment.BankMember)
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
	_, err := h.mesh.Submit(ctx, toNora)
	if !errors.Is(err, payment.ErrBankCodeUnknown) {
		t.Fatalf("paying a member admitted since the last refresh = %v, want ErrBankCodeUnknown", err)
	}
	// Nothing left Aurora, and nothing moved. A refusal that had already debited
	// the payer would be the failure SubmitAndInstruct exists to prevent, arriving
	// through a new door.
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

	// One request, made by the subscriber, and the same instruction goes through.
	if _, err := h.mesh.RefreshDirectory(ctx, h.debtorBIC); err != nil {
		t.Fatalf("RefreshDirectory: %v", err)
	}
	p, err := h.mesh.Submit(ctx, toNora)
	if err != nil {
		t.Fatalf("after the refresh, the same payment: %v", err)
	}
	h.drain(t)
	if got := h.payment(t, p.ID); got.Status != payment.Accepted {
		t.Fatalf("the payment after the refresh is %v, want Accepted", got.Status)
	}
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
//
// The BIC, because that is what a message is addressed to and what no arithmetic
// derives from an address. The allocation, because that is what an address
// carries. Nothing else: not the assets, so a copy that is behind cannot refuse a
// payment the clearing house would have taken, and not the admission reference,
// which decides between two institutions contending for an address and is the
// clearing house's own business.
//
// The refresh is not a message, either — nothing is queued and nobody answers
// later — which is why this asserts on the wire being silent. A directory that
// had become a conversation would be a delivery system, and the clearing house
// would be holding a subscriber list.
func TestARefreshCarriesTheAllocationAndTheAddressAndNothingElse(t *testing.T) {
	ctx := context.Background()
	h := newMeshHarness(t)
	mark := h.messagesSeen()

	got, err := h.mesh.RefreshDirectory(ctx, h.debtorBIC)
	if err != nil {
		t.Fatalf("RefreshDirectory: %v", err)
	}
	if n := len(h.messagesFrom(mark)); n != 0 {
		t.Errorf("a refresh put %d messages on the wire, want 0 — it is a file, not a conversation", n)
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
//
// It is the only event a member writes about institutions it does not act for,
// and it records no decision about any of them: a directory says where to send a
// message and never whether to. What makes the log worth having is the question a
// stale copy raises — not "is this bank behind" but "what did it believe when it
// refused that payment" — so the payload is the snapshot.
//
// Nothing lands in the clearing house's log, because the clearing house did not
// act: it published, and somebody read.
func TestARefreshIsRecordedInTheSubscribersOwnLog(t *testing.T) {
	ctx := context.Background()
	h := newMeshHarness(t)

	before := h.directoryEvents(t, h.debtorBIC)
	if _, err := h.mesh.RefreshDirectory(ctx, h.debtorBIC); err != nil {
		t.Fatalf("RefreshDirectory: %v", err)
	}

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

// directoryEvents is one member's own record of the snapshots it has pulled.
func (h *meshHarness) directoryEvents(t *testing.T, bic iso20022.BIC) []ledger.AuditEvent {
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
//
// This is the door decision 11 keeps open, and it is not hypothetical: a card
// PAN is issued by a scheme elsewhere and quoted, a proxy alias is resolved by a
// central service this system does not have, and a cross-border transfer's BIC
// genuinely is the payer's to supply. What changed is the SCOPE of the sentinel —
// it used to mean "this system has nowhere to get an agent from" and now means
// "not for this address".
//
// The refusal is asserted and the success is not: nothing in this mesh routes a
// PAN, so an instruction quoting one is refused a step later for a reason that
// has nothing to do with directories. What is worth pinning is that the address's
// kind, and not the presence of an agent, is what decides which refusal a payer
// meets.
func TestAnAddressWithNoDirectoryHereIsRefusedForWantOfABIC(t *testing.T) {
	h := newMeshHarness(t)

	req := h.creditTransferRequest(t)
	req.Creditor.Identifier = deposit.Identifier{Scheme: "PAN", Value: "4000000000000001"}

	_, err := h.mesh.Submit(context.Background(), req)
	if !errors.Is(err, payment.ErrCounterpartyAgentNotNamed) {
		t.Fatalf("an address no directory here covers = %v, want ErrCounterpartyAgentNotNamed", err)
	}
}
