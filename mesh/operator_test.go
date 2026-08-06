package mesh

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
	"github.com/raphi011/cbs/store/storetest"
)

// The two entry points Task 14 needed and no message provokes: an operator
// rejecting a payment the clearing house is holding, and a bank joining a mesh
// that is already running.
//
// Both are the shape Mesh.Submit, Mesh.CloseCycle and Mesh.Return already have —
// synchronous, on the caller's goroutine, an error the caller can be answered
// with — because an instruction from outside the mesh is not a message arriving
// in an inbox, and the layer that instructs has somebody to tell.

// joinerIBAN addresses the customer of the bank that joins after Start. A third
// address, because an IBAN identifies an ACCOUNT: reusing either of the
// harness's two would be refused by the register, which holds one address on one
// account.
const joinerIBAN = "SE4550000000058398257466"

// An operator's rejection is TWO halves in two actors, and only the first one
// has anybody to answer.
//
// The clearing house's half is what Reject returns: the payment is Rejected and
// out of its cycle, then and there. Giving the payer their money back is their
// own bank's act, in their own bank's book, and it happens when the pacs.002
// gets there. Before the mesh, api ran both in one transaction and no caller
// could tell the two apart.
//
// # How that is measured, and how it deliberately is not
//
// Not by reading the suspense between the two calls, which is what a first draft
// did: the payer's bank actor runs concurrently with this goroutine, so
// "unchanged the moment Reject returned" is a race that happens to pass rather
// than an assertion. Nothing in this package may be timed.
//
// It is measured by WHICH BOOKS each actor's units of work reached, which the
// recording store answers exactly and without a clock. A clearing house that
// refunded the payer itself would have had to post in the payer's bank's book,
// and its set would say so. This is the rejection-shaped case of
// TestTheCSMTouchesOnlyTheNetworkBook.
func TestAnOperatorRejectionRefundsThePayerOnlyOnceTheMessageArrives(t *testing.T) {
	h := newMeshHarness(t)
	p := h.submitCreditTransfer(t)
	h.drain(t)

	rejected, err := h.mesh.Reject(context.Background(), p.ID, iso20022.StatusReasonNotSpecifiedAgentGenerated, "card lost")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.Status != payment.Rejected {
		t.Fatalf("Reject returned %v, want Rejected", rejected.Status)
	}
	h.drain(t)

	if bal := h.suspense(t, h.debtorPID); bal != 0 {
		t.Errorf("clearing suspense = %d after draining, want 0 — the pacs.002 is what tells the payer's bank to give the money back", bal)
	}
	if bal := h.balance(t, h.debtorPID, h.debtorAcct.ID); bal != harnessFunding {
		t.Errorf("payer's balance = %d after the rejection, want %d", bal, harnessFunding)
	}
	// The refund was posted by the payer's BANK and by nobody else. The clearing
	// house reached the network book, where a payment row lives, and no bank's.
	//
	// What that catches, exactly: a clearing house that WROTE in a member's
	// book. That used to be one method away — GetParticipant's live handles,
	// the hole ops.go named — and Task 17 closed that particular door; what it
	// did not close, and what no interface can, is that any posting method
	// takes its book as an ordinary argument. Adding a write to the payer's
	// bank in csm.reject fails this line with [bank_1 network].
	//
	// What it does not catch is a reject that forgot withActor. The clearing
	// house's own goroutine has already written the network book carrying this
	// payment, so the set contains it either way — worth saying, because the
	// natural reading of this line is that it holds the attribution too.
	assertBooksTouched(t, "the clearing house", h.booksTouchedBy(h.cfg.ClearingHouseBIC),
		[]ledger.BookID{ledger.NetworkBook})
	// The code the operator named travels, rather than being replaced by
	// whatever the clearing house would have said on its own behalf.
	h.assertLastTxStatusTo(t, h.debtorBIC, iso20022.TransactionStatusRejected)
	h.assertLastStatusTo(t, h.debtorBIC, iso20022.StatusReasonNotSpecifiedAgentGenerated)
}

// A payment the clearing house cannot reject is refused SYNCHRONOUSLY, and
// nothing is sent.
//
// It is the answerable half of the split ruling 4 of this task's brief is about:
// a refusal decided inside the clearing house's own unit of work is one the
// operator who asked can be told about, with a status code, in the response to
// their request. What cannot come back that way is anything a counterparty
// decides — which for a rejection is the refund, three hops and one actor later.
func TestAnOperatorRejectionOfASettledPaymentIsRefusedAndSendsNothing(t *testing.T) {
	h := newMeshHarness(t)
	p := h.settledPayment(t)
	before := h.messagesSeen()

	if _, err := h.mesh.Reject(context.Background(), p.ID, iso20022.StatusReasonNotSpecifiedAgentGenerated, "too late"); err == nil {
		t.Fatal("Reject accepted a settled payment; the money has already moved between banks")
	}

	h.drain(t)
	if n := h.messagesSeen() - before; n != 0 {
		t.Errorf("%d messages were sent by a rejection that was refused, want 0", n)
	}
	if got := h.payment(t, p.ID); got.Status != payment.Settled {
		t.Errorf("payment is %v after a refused rejection, want Settled", got.Status)
	}
}

// A bank the mesh does not know about is reachable in BOTH directions once
// AddBank has been called, and unreachable until then.
//
// # Why the fixture builds it without the mesh
//
// Mesh.Admit registers the actor itself, before it writes anything, so a bank
// admitted through the mesh's own door is never in this state. What is, is a
// bank the mesh has not READ: api.Server.Reset truncates the store and rebuilds
// it underneath a live mesh, and every bank in the new roster is one no actor
// answers to until JoinRoster runs. storetest.Admit builds exactly that — three
// institutions' rows, no transport — which is why this test uses it rather than
// Admit.
//
// Such a bank is not slow, it is unreachable: it cannot pay, because Mesh.Submit
// has no actor to hand its customer's instruction to, and it cannot be paid,
// because the clearing house has nothing under its BIC to route a pacs.008 to.
//
// Both directions are asserted, because they fail for different reasons and one
// of them can be fixed without the other: the bank index answers "can it pay"
// and the actor table answers "can it be paid".
func TestABankAdmittedAfterStartCanPayAndBePaid(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	joiner, err := storetest.Admit(ctx, h.nets, "Nordhaven Bank", "NORDSESSXXX", euroOnly)
	if err != nil {
		t.Fatalf("admitting Nordhaven: %v", err)
	}
	acct := h.openCustomer(t, joiner, "Nora", "EUR", 0, joinerIBAN)
	if err := h.bank(joiner.ID).Deposit(ctx, joiner.ID, acct.ID, harnessFunding, "Opening deposit"); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	out := payment.InitiatePaymentRequest{
		Scheme:      payment.SchemeSEPACT,
		Debtor:      payment.PartyRef{Participant: joiner.ID, Account: acct.ID, Identifier: acct.Identifiers[0]},
		Creditor:    h.creditorRef(creditorIBAN),
		Amount:      harnessAmount,
		Description: "invoice 44",
		// Push: the creditor is the counterparty, so the request must name it —
		// name and BIC both, as everywhere else since Task 18a.
		CreditorDetails: payment.PartyDetails{Agent: h.creditorBIC, Name: h.creditorAcct.Name},
	}

	// In the roster, not in the mesh.
	if _, err := h.mesh.Submit(ctx, out); err == nil {
		t.Fatal("Submit accepted an instruction for a bank with no actor; nothing could ever have answered it")
	} else if !strings.Contains(err.Error(), "no bank actor") {
		t.Errorf("Submit refused with %v, want a refusal naming the missing actor", err)
	}

	if err := h.mesh.AddBank(joiner); err != nil {
		t.Fatalf("AddBank: %v", err)
	}

	sent, err := h.mesh.Submit(ctx, out)
	if err != nil {
		t.Fatalf("Submit after AddBank: %v", err)
	}
	h.drain(t)
	if got := h.payment(t, sent.ID); got.Status != payment.Accepted {
		t.Fatalf("the joining bank's own payment is %v, want Accepted", got.Status)
	}

	// And the other direction: a pacs.008 addressed to the new bank has an
	// inbox to land in, and the bank answers it.
	in := h.creditTransferRequest(t)
	in.Creditor = payment.PartyRef{
		Participant: joiner.ID,
		Account:     acct.ID,
		Identifier:  deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: joinerIBAN},
	}
	in.CreditorDetails = payment.PartyDetails{Agent: joiner.BIC, Name: acct.Name}
	received, err := h.mesh.Submit(ctx, in)
	if err != nil {
		t.Fatalf("Submit to the joining bank: %v", err)
	}
	h.drain(t)
	if got := h.payment(t, received.ID); got.Status != payment.Accepted {
		t.Fatalf("the payment addressed to the joining bank is %v, want Accepted", got.Status)
	}
	h.assertLastTxStatusTo(t, h.debtorBIC, iso20022.TransactionStatusAccepted)
}

// Two banks on one BIC is refused at admission, and the mesh is left as it was.
//
// It is the same refusal Start makes about a roster it cannot route, arriving
// one bank at a time: the actor table would keep the second and drop the first,
// and the dropped bank's goroutine would read an inbox nothing could address.
// The bank INDEX must be left alone too — a Submit that found the newcomer under
// the old bank's actor would sign one bank's instruction with another's identity.
func TestAddingABankOnAnotherBanksBICIsRefusedAndChangesNothing(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	// FOUNDED and not admitted, because the clearing house would refuse to admit
	// a second institution on an address it already routes to
	// (payment.ErrBICAlreadyAdmitted) and this test is about the mesh's refusal
	// rather than the domain's. A founded bank is all AddBank needs.
	clash, err := h.net.FoundBank(ctx, "Aurora Bank (again)", h.debtorBIC, euroOnly)
	if err != nil {
		t.Fatalf("FoundBank: %v", err)
	}
	if err := h.mesh.AddBank(clash); err == nil {
		t.Fatal("AddBank accepted a second bank on an address another bank already answers to")
	}

	h.mesh.mu.Lock()
	_, indexed := h.mesh.banks[clash.ID]
	h.mesh.mu.Unlock()
	if indexed {
		t.Error("the refused bank is in the mesh's bank index; a refusal must leave it as it found it")
	}
}

// ---------------------------------------------------------------------------
// ForgetBanks and JoinRoster: the two halves of replacing the network under a
// live mesh
// ---------------------------------------------------------------------------
//
// api.Server.Reset is the only caller and the reason both exist: it truncates
// every table and reseeds, and the mesh survives that — Start ran once, at boot.
// Until these were tested the whole of their behaviour was prose, which on a
// pair of methods whose failure mode is "an address nobody can ever use again"
// is not good enough.

// ForgetBanks takes out every actor that is not one of the two institutions, and
// leaves those two exactly where they were.
//
// It is driven on a mesh with NO network, which is the honest fixture for what
// this method actually does: it reads the actor table, not the roster, and the
// two institutions are excluded by identity. A network here would add a store to
// a test about the transport.
func TestForgetBanksLeavesTheTwoInstitutionsAndNothingElse(t *testing.T) {
	m := newTestMesh(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	if err := m.ForgetBanks(drainCtx(t)); err != nil {
		t.Fatalf("ForgetBanks: %v", err)
	}

	m.mu.Lock()
	left := make([]iso20022.BIC, 0, len(m.actors))
	for bic := range m.actors {
		left = append(left, bic)
	}
	m.mu.Unlock()
	slices.Sort(left)
	want := []iso20022.BIC{testConfig.CentralBankBIC, testConfig.ClearingHouseBIC}
	slices.Sort(want)
	if !slices.Equal(left, want) {
		t.Errorf("the mesh holds %v after ForgetBanks, want the two institutions %v", left, want)
	}

	// And the addresses are free again, which is the whole point: a BIC an
	// unjoined actor still answered to is one no operator could ever admit.
	if err := m.addActor("AAAADEFFXXX", "A again", func(context.Context, iso20022.BIC, []byte) error { return nil }); err != nil {
		t.Errorf("re-registering a forgotten address: %v", err)
	}
}

// An actor the bank index does not name is forgotten anyway.
//
// This is the residue of a real race and not a hypothetical: AddBank writes the
// actor and the index entry under separate locks, on a different goroutine from
// joinRoster, so an index entry can be missing while its actor runs. Forgetting
// by index left that actor unforgettable — its address taken for the life of the
// process, and every later reset failing on it. Forgetting by actor table makes
// it total.
//
// The state is set up directly rather than by racing two goroutines, because
// what has to be pinned is that this SHAPE is recoverable, and a race that
// reproduces it once in a hundred runs would pin nothing on the other
// ninety-nine.
func TestForgetBanksRemovesAnActorTheBankIndexDoesNotName(t *testing.T) {
	h := newMeshHarness(t)

	h.mesh.mu.Lock()
	delete(h.mesh.banks, h.debtorPID)
	h.mesh.mu.Unlock()

	if err := h.mesh.ForgetBanks(drainCtx(t)); err != nil {
		t.Fatalf("ForgetBanks: %v", err)
	}
	h.mesh.mu.Lock()
	_, stranded := h.mesh.actors[h.debtorBIC]
	h.mesh.mu.Unlock()
	if stranded {
		t.Fatalf("%s still answers after ForgetBanks; an actor its index forgot is unforgettable", h.debtorBIC)
	}
	// The address is usable again, which is the failure this closes: it stayed
	// taken for the life of the process.
	if err := h.mesh.AddBank(h.debtor); err != nil {
		t.Errorf("re-admitting the stranded bank's address: %v", err)
	}
}

// JoinRoster merges into the bank index rather than replacing it, so a bank
// registered beside it keeps its entry.
//
// The interleaving this stands for is a POST /members committing between
// JoinRoster's roster read and its write. An assignment there dropped the new
// bank's index entry while leaving its actor running — a bank that answers every
// read and carries no payment, with nothing anywhere saying so.
//
// The joining bank is a participant the ROSTER does not hold, which is what that
// interleaving leaves behind when the admission's row is truncated away, and
// what makes the two writes distinguishable at all: a bank in the roster would
// be registered by JoinRoster itself and the merge would prove nothing.
func TestJoinRosterKeepsABankRegisteredBesideIt(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	if err := h.mesh.ForgetBanks(ctx); err != nil {
		t.Fatalf("ForgetBanks: %v", err)
	}
	beside := &payment.Bank{ID: "bank_beside", Name: "Beside Bank", BIC: "BSDEDEFFXXX"}
	if err := h.mesh.AddBank(beside); err != nil {
		t.Fatalf("AddBank: %v", err)
	}

	if err := h.mesh.JoinRoster(ctx); err != nil {
		t.Fatalf("JoinRoster: %v", err)
	}

	h.mesh.mu.Lock()
	_, kept := h.mesh.banks[beside.ID]
	_, rejoined := h.mesh.banks[h.debtorPID]
	h.mesh.mu.Unlock()
	if !kept {
		t.Error("the bank registered beside JoinRoster lost its index entry; its actor is running and nothing can reach it")
	}
	if !rejoined {
		t.Error("the roster's own banks are not in the index after JoinRoster")
	}
}

// JoinRoster is all-or-none: one address already taken and the whole roster is
// refused, with the mesh left as it was.
//
// The batch is a roster, and half a roster is worse than none — a mesh holding
// some banks and not others, with a caller that fixed the clash and retried
// colliding with the actors its own failed attempt created.
func TestJoinRosterRefusesTheWholeRosterWhenOneAddressIsTaken(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	if err := h.mesh.ForgetBanks(ctx); err != nil {
		t.Fatalf("ForgetBanks: %v", err)
	}
	// Somebody else answering to the payer's bank's address.
	squatter := &payment.Bank{ID: "bank_squatter", Name: "Squatter", BIC: h.debtorBIC}
	if err := h.mesh.AddBank(squatter); err != nil {
		t.Fatalf("AddBank: %v", err)
	}

	err := h.mesh.JoinRoster(ctx)
	if err == nil {
		t.Fatal("JoinRoster accepted a roster whose address another actor already answers to")
	}
	if !errors.Is(err, ErrAddressTaken) {
		t.Errorf("JoinRoster failed with %v, want ErrAddressTaken", err)
	}
	// None of it landed: the OTHER bank in the roster, which did not clash, must
	// not have been registered either.
	h.mesh.mu.Lock()
	_, partial := h.mesh.actors[h.creditorBIC]
	_, indexed := h.mesh.banks[h.creditorPID]
	h.mesh.mu.Unlock()
	if partial || indexed {
		t.Error("a refused roster left one of its banks behind; the batch is all-or-none")
	}
}

// ForgetBanks refuses a mesh that is stopping or stopped.
//
// Its whole contract is that a forgotten actor has been JOINED, and during a
// shutdown Stop owns that: it has taken its snapshot and is joining them itself.
// Two callers joining the same goroutines is not a race this package needs to
// win — it is one it declines.
func TestForgetBanksRefusesAMeshThatIsStopping(t *testing.T) {
	m := newTestMesh(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if err := m.ForgetBanks(context.Background()); err == nil {
		t.Fatal("ForgetBanks accepted a stopped mesh, whose actors Stop has already joined")
	}
}

// A ForgetBanks that times out leaves the actors where Stop can still find them.
//
// This is the property the first version of this method got backwards. It
// deleted from the actor table before the join, so a timeout left goroutines
// nobody could ever wait for: Stop snapshots m.actors, so what is not in it is
// not joined, and a caller that retried its reset would truncate the store
// underneath a handler still running against it.
//
// Stop's own timeout does the opposite and says so at length; this is the same
// rule, and it is asserted the same way — the mesh is left whole, the call can
// be retried, and Stop still works.
func TestForgetBanksThatTimesOutLeavesTheActorsForStopToJoin(t *testing.T) {
	m := newTestMesh(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	m.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		close(entered)
		<-release
		return nil
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.send("CCCCDEFFXXX", "AAAADEFFXXX", testEnvelope("CCCCDEFFXXX", "AAAADEFFXXX", "x")); err != nil {
		t.Fatalf("send: %v", err)
	}
	<-entered

	// An already-cancelled context rather than a short one: the handler is
	// provably inside its call, so the select below has exactly one ready arm
	// and no duration decides anything.
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	err := m.ForgetBanks(dead)
	if err == nil {
		t.Fatal("ForgetBanks returned nil while a handler was still inside its call")
	}
	if !strings.Contains(err.Error(), "AAAADEFFXXX") {
		t.Errorf("the timeout error %q does not name the actor holding it up", err)
	}

	m.mu.Lock()
	_, joinable := m.actors["AAAADEFFXXX"]
	m.mu.Unlock()
	if !joinable {
		t.Fatal("the actor was removed from the table before it was joined; Stop can no longer wait for it")
	}

	// And the retry finishes the job, which is what "closing a closed queue is a
	// no-op" buys.
	close(release)
	if err := m.ForgetBanks(drainCtx(t)); err != nil {
		t.Fatalf("retrying ForgetBanks after the handler returned: %v", err)
	}
	m.mu.Lock()
	_, gone := m.actors["AAAADEFFXXX"]
	m.mu.Unlock()
	if gone {
		t.Error("the retry did not forget the actor it was able to join")
	}
	if err := m.Stop(drainCtx(t)); err != nil {
		t.Fatalf("Stop after a timed-out ForgetBanks: %v", err)
	}
}

// An actor parked in Config.Observe is named as such, and not as one inside its
// handler.
//
// The distinction is the whole value of the message: an Observe that blocks is
// being held by whoever installed the hook, and a reader told the actor was "in
// a handler" would go looking for a bug in the actor's own code — at exactly the
// moment they are debugging the hook. api's meshGate holds an actor this way in
// every one of its tests.
func TestAnActorHeldInObserveIsNamedAsHeldInObserve(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	m, err := New(nil, Config{
		CentralBankBIC:   testConfig.CentralBankBIC,
		ClearingHouseBIC: testConfig.ClearingHouseBIC,
		Observe: func(to, from iso20022.BIC, raw []byte) {
			close(entered)
			<-release
		},
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.addActor("AAAADEFFXXX", "A", func(context.Context, iso20022.BIC, []byte) error { return nil }); err != nil {
		t.Fatalf("addActor: %v", err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { close(release); _ = m.Stop(context.Background()) })

	if err := m.send("CBSEDEFFXXX", "AAAADEFFXXX", testEnvelope("CBSEDEFFXXX", "AAAADEFFXXX", "x")); err != nil {
		t.Fatalf("send: %v", err)
	}
	<-entered

	if got := m.stuck(); !strings.Contains(got, "AAAADEFFXXX in Observe") {
		t.Errorf("stuck() = %q, want it to say the actor is held in Observe", got)
	}
}
