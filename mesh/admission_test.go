package mesh

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// Admission, as a conversation between three institutions.
//
// It is the one flow this package carries that BRINGS AN ACTOR INTO EXISTENCE.
// Package mesh's doc has the whole list — a push, a pull, a cut-off, a return
// and this — and counting them is what keeps going stale, so it is described
// there and nowhere numbered. Everything else here starts from a mesh that already has
// the parties in it; this one starts from a bank nothing could address a moment
// earlier, and the first message on the wire is sent by that bank.
//
// The tests below are grouped as the flow is: what each institution ends up
// holding, what a refusal leaves behind, which addresses are refused and what
// that costs, and what actually goes on the wire.

// joinerBIC is the address the bank these tests admit applies on. It belongs to
// no bank the harness builds, which is what makes it admissible at all.
const joinerBIC iso20022.BIC = "NORDSESSXXX"

// joinerUSDIBAN is the joining bank's customer's DOLLAR account, used by the
// two-asset admission alone. Its euro sibling is joinerIBAN, in operator_test.go,
// where the bank that joins after Start already needed one.
const joinerUSDIBAN = "SE1250000000058398257467"

// TestAdmissionIsAConversation is the task's headline, asserted as the state
// each institution ends up in rather than as the messages that got there —
// TestTheMessagesAnAdmissionPutsOnTheWire below is what asserts those.
//
// Three rows, three institutions, and the assertion that matters most is the
// one comparing two of them: the bank's record of its settlement account and the
// central bank's record of the same account must be the same account. They are
// written in different units of work, by different actors, from a message — so
// "they agree" is a fact about the conversation and not about a struct.
func TestAdmissionIsAConversation(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	joiner, err := h.mesh.Admit(ctx, "Nordhaven Bank", joinerBIC, euroOnly)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	if joiner.Status != payment.BankFounded {
		t.Errorf("Admit returned status %q; the scheme's answer arrives as a message, not from this call", joiner.Status)
	}
	h.drain(t)

	// The bank is a member and knows its own account at the central bank.
	after := h.getBank(t, joiner.ID)
	if after.Status != payment.BankMember {
		t.Errorf("after the conversation the bank's status is %q, want %q", after.Status, payment.BankMember)
	}
	settlement := after.Assets["EUR"].Settlement
	if settlement == "" {
		t.Fatal("the bank was admitted and does not know its settlement account")
	}
	// The central bank's own record agrees with it, and is the row settlement
	// will actually read.
	member := h.getSettlementMember(t, joinerBIC)
	if member.Accounts["EUR"] != settlement {
		t.Errorf("the bank thinks its settlement account is %q; the central bank opened %q",
			settlement, member.Accounts["EUR"])
	}
	if member.Name != "Nordhaven Bank" {
		t.Errorf("the central bank's member row names %q, want the applicant's name", member.Name)
	}
	// And the clearing house can route to it, under the assets the SERVICER said
	// it opened accounts in rather than the ones the bank asked for.
	entry, err := h.getRosterEntry(joinerBIC)
	if err != nil {
		t.Fatalf("the bank was admitted and the clearing house cannot route to it: %v", err)
	}
	if len(entry.Assets) != 1 || entry.Assets[0] != "EUR" {
		t.Errorf("the roster entry clears in %v, want [EUR]", entry.Assets)
	}
	// And it records WHICH admission put it there, which is the field the
	// clearing house's refusal of a second institution reads and the only thing
	// on this row besides the address and the assets. See payment.RosterEntry on
	// why a member's legal name is not among them: the acmt.010 this row is
	// written from names nobody.
	if entry.AdmissionRef == "" {
		t.Error("the roster entry cites no admission; nothing else correlates this conversation")
	}
}

// TestAnAdmittedBankCanPayAndBePaid is the point of the whole conversation,
// asserted where a customer would notice it.
//
// The three rows above are not the outcome; a working bank is. This is the same
// pair of directions TestABankAdmittedAfterStartCanPayAndBePaid asserts about a
// bank the mesh had to be told about separately, made about one that came in
// through the mesh's own door — and the difference is that nothing here calls
// AddBank at all.
//
// Funding is what makes it a real check rather than a routing one: a deposit
// credits this bank's settlement account at the central bank, which is the
// reference the acknowledgement delivered. A bank that had been admitted without
// learning it would fail here rather than in a message.
func TestAnAdmittedBankCanPayAndBePaid(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	joiner, err := h.mesh.Admit(ctx, "Nordhaven Bank", joinerBIC, euroOnly)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	h.drain(t)
	joiner = h.getBank(t, joiner.ID)

	acct := h.openCustomer(t, joiner, "Nora", "EUR", 0, joinerIBAN)
	if err := h.bank(joiner.BIC).Deposit(ctx, joiner.ID, acct.ID, harnessFunding, "Opening deposit"); err != nil {
		t.Fatalf("funding a bank admitted through the mesh: %v", err)
	}

	out := h.creditTransferRequest(t)
	out.Debtor = payment.PartyRef{Account: acct.ID, Identifier: acct.Identifiers[0]}
	sent, err := h.mesh.Submit(ctx, out)
	if err != nil {
		t.Fatalf("the admitted bank could not submit: %v", err)
	}
	h.drain(t)
	if got := h.payment(t, sent.ID); got.Status != payment.Accepted {
		t.Fatalf("the joining bank's own payment is %v, want Accepted", got.Status)
	}

	in := h.creditTransferRequest(t)
	in.Creditor = payment.PartyRef{Account: acct.ID, Identifier: acct.Identifiers[0]}
	in.CreditorDetails = payment.PartyDetails{Agent: joiner.BIC, Name: acct.Name}
	received, err := h.mesh.Submit(ctx, in)
	if err != nil {
		t.Fatalf("Submit to the admitted bank: %v", err)
	}
	h.drain(t)
	if got := h.payment(t, received.ID); got.Status != payment.Accepted {
		t.Fatalf("the payment addressed to the admitted bank is %v, want Accepted", got.Status)
	}
}

// TestATwoAssetAdmissionRecordsBothSettlementAccounts is the inconsistency the
// hand-off carried into this task, closed and made falsifiable.
//
// # What was wrong
//
// One acmt.007 asks for ONE currency — the schema makes Acct/Ccy
// minOccurs="1" maxOccurs="1" — so a bank joining in two assets is answered
// twice. RecordMembershipTx used to take only a bank that was still Founded, so
// the FIRST acknowledgement took it to Member and the second was refused. The
// bank ended a Member with a dollar settlement reference it had never learned,
// while the central bank held a dollar reserve for it: a dollar deposit failed
// against a reserve the operator console reported perfectly well, because the
// console reads the central bank's row and the deposit reads the bank's.
//
// # What it asserts
//
// Both references, on both sides, and that they AGREE. Asserting only that the
// bank has two would pass on a bank that had recorded the euro account twice.
//
// The two-asset HARNESS is not used, and the distinction is worth stating: that
// fixture gives the network a dollar scheme and both incumbents a dollar side,
// which is what a two-asset SETTLEMENT needs. What is under test here is one
// bank's admission, so the joiner asks for both assets on a network that clears
// one, and the settlement agent opens both accounts either way — an account
// servicer answers about its own book and knows nothing about schemes.
func TestATwoAssetAdmissionRecordsBothSettlementAccounts(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	joiner, err := h.mesh.Admit(ctx, "Nordhaven Bank", joinerBIC, euroAndDollar)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	h.drain(t)

	after := h.getBank(t, joiner.ID)
	if after.Status != payment.BankMember {
		t.Fatalf("a two-asset admission left the bank %q, want %q", after.Status, payment.BankMember)
	}
	member := h.getSettlementMember(t, joinerBIC)
	for _, asset := range euroAndDollar {
		recorded := after.Assets[asset].Settlement
		if recorded == "" {
			t.Errorf("the bank does not know its %s settlement account; one acmt.007 asks for one currency, so this is the second acknowledgement's", asset)
			continue
		}
		if member.Accounts[asset] != recorded {
			t.Errorf("the bank thinks its %s settlement account is %q; the central bank opened %q",
				asset, recorded, member.Accounts[asset])
		}
	}
	if after.Assets["EUR"].Settlement == after.Assets["USD"].Settlement {
		t.Error("both assets record one settlement account; a reserve in euro is not a reserve in dollars")
	}
	// The clearing house learned BOTH assets, from the two acknowledgements it
	// extended one entry with.
	entry, err := h.getRosterEntry(joinerBIC)
	if err != nil {
		t.Fatalf("the two-asset bank is not in the roster: %v", err)
	}
	if len(entry.Assets) != 2 {
		t.Errorf("the roster entry clears in %v, want both assets", entry.Assets)
	}

	// And the money moves in both, which is the whole of what the missing
	// reference cost: a deposit credits the bank's settlement account at the
	// central bank, read off the bank's OWN row.
	joiner = h.getBank(t, joiner.ID)
	for asset, iban := range map[ledger.AssetCode]string{"EUR": joinerIBAN, "USD": joinerUSDIBAN} {
		acct := h.openCustomer(t, joiner, "Nora", asset, 0, iban)
		if err := h.bank(joiner.BIC).Deposit(ctx, joiner.ID, acct.ID, harnessFunding, "Opening deposit"); err != nil {
			t.Errorf("funding the admitted bank in %s: %v", asset, err)
		}
	}
}

// TestAMemberRefusesAnAcknowledgementOfAnotherAdmission is the guard the bank's
// own record of its admission exists for, and it is measured rather than
// reasoned about.
//
// # What it stops
//
// RecordMembershipTx records-or-extends: it has to, because one acmt.007 asks
// for one currency and a bank joining in two assets is answered twice. For one
// round it did that with no guard at all beside the BIC, and the argument was
// that a bank has no contender because the acknowledgement already names its own
// address. The BIC answers WHICH BANK. It does not answer WHICH ADMISSION.
//
// This is what that cost, before payment.Bank.AdmissionRef existed: an
// acknowledgement naming a member's own BIC, quoting an admission it had never
// heard of and carrying an invented account, moved that bank's euro settlement
// reference off the central bank's real account and onto the forged one —
//
//	before:            status="Member" EUR="200.100.003"
//	after forged ack:  status="Member" EUR="acc_bogus"
//	central bank still holds map[EUR:200.100.003]
//
// leaving the bank's own row disagreeing with the settlement agent's about which
// account it holds. payment.DepositTx reads the bank's.
//
// # Why the clearing house's refusal is not enough
//
// It is the only thing that stops such a message reaching this bank in the
// shipped system, and it is made from the ROSTER — a row this institution does
// not own and which Task 18 moves to another database. A guard that lives only
// there is a guard the split removes. This one is a bank comparing a message
// against its own memory of what it accepted, and it needs nobody else's store.
//
// So the message is INJECTED, straight into the bank's inbox, which is what the
// clearing house's refusal being one hop earlier looks like from here — and what
// any transport that could deliver to a bank directly would look like.
func TestAMemberRefusesAnAcknowledgementOfAnotherAdmission(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	joiner, err := h.mesh.Admit(ctx, "Nordhaven Bank", joinerBIC, euroOnly)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	h.drain(t)
	before := h.getBank(t, joiner.ID)
	if before.Status != payment.BankMember || before.Assets["EUR"].Settlement == "" {
		t.Fatalf("the bank is %q with settlement %q; this test measures a member",
			before.Status, before.Assets["EUR"].Settlement)
	}

	// An acknowledgement for this bank's own address, under an admission it has
	// never heard of, naming an account the settlement agent never opened.
	env, err := payment.AdmissionAcknowledgementMessage(payment.AdmissionAcknowledgement{
		BIC:      joinerBIC,
		Ref:      "a-completely-different-admission",
		Accounts: map[ledger.AssetCode]ledger.AccountID{"EUR": "acc_bogus"},
	}, payment.MessageContext{From: h.cfg.CentralBankBIC, To: joinerBIC, MsgID: "forged-1", Now: testTime})
	if err != nil {
		t.Fatalf("composing the acknowledgement: %v", err)
	}
	if err := h.mesh.send(h.cfg.ClearingHouseBIC, joinerBIC, env); err != nil {
		t.Fatalf("sending it: %v", err)
	}

	// A dead letter, because the bank is the LAST hop of an admission and has
	// nobody to answer. That is the refusal being visible rather than silent.
	err = h.drainErr(t)
	if err == nil {
		t.Fatal("the bank accepted an acknowledgement of another admission")
	}
	if !strings.Contains(err.Error(), "recorded its membership under another admission") {
		t.Errorf("the refusal reads %v; it must name the admission mismatch", err)
	}

	after := h.getBank(t, joiner.ID)
	if after.Assets["EUR"].Settlement != before.Assets["EUR"].Settlement {
		t.Errorf("the bank's settlement reference moved to %q; it was %q, and the central bank still holds %q",
			after.Assets["EUR"].Settlement, before.Assets["EUR"].Settlement,
			h.getSettlementMember(t, joinerBIC).Accounts["EUR"])
	}
	// And the two records still agree, which is the property the overwrite broke
	// and the one DepositTx depends on.
	if got := h.getSettlementMember(t, joinerBIC); got.Accounts["EUR"] != after.Assets["EUR"].Settlement {
		t.Errorf("the bank thinks its settlement account is %q; the central bank holds %q",
			after.Assets["EUR"].Settlement, got.Accounts["EUR"])
	}
}

// TestARefusedAdmissionLeavesAFoundedBank is the orphan defect, inverted. What
// used to be left behind was a roster row for a bank that could neither pay nor
// be paid, with no way back. What is left behind now is a bank that exists and
// has not joined — which is a true state, and which re-calling Admit re-drives.
//
// # The refusal is INJECTED, and that is a finding rather than a shortcut
//
// The plan for this test admitted a bank in an asset nobody issues and expected
// the central bank to refuse it. Nothing reaches the central bank that way:
// FoundBankTx checks every asset against ledger.LookupAsset before it writes a
// thing, so an unknown asset is refused synchronously, at the applicant's own
// bank, before a message exists (TestAnUnknownAssetIsRefusedBeforeAnythingIsWritten
// is what pins that half).
//
// That is true of every refusal on this path, and it is the design rather than
// an accident: Mesh.Admit claims the address before anything is written, so an
// impostor never gets a message out, and the two institutions downstream refuse
// only what a message could carry them. So the acmt.011 arm is reachable in this
// system only from a message Mesh.Admit did not compose — which is what a real
// counterparty sends, and what this test injects.
//
// The bank is founded and given an actor without an admission, which is exactly
// the state an interrupted one leaves; then a request it could have sent itself
// is put on the wire, in an asset the settlement agent cannot open an account
// in.
func TestARefusedAdmissionLeavesAFoundedBank(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	joiner, err := h.net.FoundBank(ctx, "Nordhaven Bank", joinerBIC, euroOnly)
	if err != nil {
		t.Fatalf("FoundBank: %v", err)
	}
	if err := h.mesh.AddBank(joiner); err != nil {
		t.Fatalf("AddBank: %v", err)
	}
	// An asset nobody issues. The message is well formed — the schema's Ccy is
	// three upper-case letters and says nothing about which — so it travels, and
	// the settlement agent is what refuses it.
	env, err := payment.AdmissionMessage(
		payment.AdmissionRequest{Name: joiner.Name, BIC: joiner.BIC, Asset: "XYZ", Ref: "adm-refused"},
		h.cfg.CentralBankBIC,
		payment.MessageContext{From: joiner.BIC, To: h.cfg.ClearingHouseBIC, MsgID: "nord-1", Now: testTime},
	)
	if err != nil {
		t.Fatalf("composing the application: %v", err)
	}
	if err := h.mesh.send(joiner.BIC, h.cfg.ClearingHouseBIC, env); err != nil {
		t.Fatalf("sending the application: %v", err)
	}
	h.drain(t)

	// The refusal reached the applicant, and it is an acmt.011: a status report
	// is about a payment transaction and this is not one.
	if n := h.messagesSentTo(joiner.BIC, "acmt.011.001.03"); n != 1 {
		t.Errorf("the refused applicant was sent %d acmt.011, want 1", n)
	}

	after := h.getBank(t, joiner.ID)
	if after.Status != payment.BankFounded {
		t.Errorf("a refused admission left the bank %q, want %q", after.Status, payment.BankFounded)
	}
	// It exists, with a working book, and it is not in the roster. Those two
	// together are the whole of what replaced the orphan row.
	if _, err := h.getRosterEntry(joinerBIC); !errors.Is(err, payment.ErrRosterEntryNotFound) {
		t.Errorf("a refused admission put the bank in the roster: %v", err)
	}
	if _, err := after.OpenCustomerAccount(ctx, "Ada", "EUR"); err != nil {
		t.Errorf("a founded bank cannot open a customer account: %v", err)
	}
	// And re-driving it works, which is what makes the state recoverable rather
	// than merely honest. Through the mesh's own door this time, which is the
	// only one there is: the bank was founded in euro and the asset nobody
	// issues never touched its chart of accounts — it existed only in the message
	// injected above, which is what the paragraph on this test's own doc explains.
	if _, err := h.mesh.Admit(ctx, "Nordhaven Bank", joinerBIC, euroOnly); err != nil {
		t.Fatalf("re-driving a refused admission: %v", err)
	}
	h.drain(t)
	if got := h.getBank(t, joiner.ID); got.Status != payment.BankMember {
		t.Errorf("after re-driving, the bank is %q, want %q", got.Status, payment.BankMember)
	}
	// One bank, not two. The retry must not have founded a second one.
	assertBankCount(t, h, joinerBIC, 1)
}

// TestAnUnknownAssetIsRefusedBeforeAnythingIsWritten is the other half of the
// paragraph above: an admission in an asset this system does not know never
// becomes a message at all.
//
// FoundBankTx checks every asset before it writes, so the refusal is the
// caller's answer and the address is given back — which is what makes the second
// Admit below work. A reservation left behind by a failed unit of work would
// take that address for the life of the process, which is the orphan defect in
// its new clothes.
func TestAnUnknownAssetIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	before := h.bankCount(t)
	mark := h.messagesSeen()
	if _, err := h.mesh.Admit(ctx, "Nordhaven Bank", joinerBIC, []ledger.AssetCode{"XYZ"}); err == nil {
		t.Fatal("Admit accepted an asset nobody issues")
	}
	h.drain(t)

	if after := h.bankCount(t); after != before {
		t.Errorf("a refused admission wrote %d bank row(s)", after-before)
	}
	if n := len(h.messagesFrom(mark)); n != 0 {
		t.Errorf("a refused admission put %d messages on the wire, want none", n)
	}
	// The address is free again, which is what the release on failure buys.
	if _, err := h.mesh.Admit(ctx, "Nordhaven Bank", joinerBIC, euroOnly); err != nil {
		t.Fatalf("re-admitting after a refused unit of work: %v", err)
	}
}

// TestAdmissionRefusesAnAddressAnotherBankAnswersTo pins the resolution of the
// two authorities. The roster is the domain's truth; the actor map is the
// transport's. A BIC belonging to a bank this mesh founded but never admitted is
// a RETRY and is not refused — get that backwards and a founded bank can never
// join.
func TestAdmissionRefusesAnAddressAnotherBankAnswersTo(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()
	// h.debtorBIC belongs to a bank the harness already admitted.
	if _, err := h.mesh.Admit(ctx, "Impostor Bank", h.debtorBIC, euroOnly); err == nil {
		t.Fatal("admitting a second institution on a member's BIC succeeded")
	} else if !errors.Is(err, ErrAddressTaken) {
		t.Fatalf("admitting on a member's BIC: %v, want ErrAddressTaken", err)
	}
	// The member is untouched: still routable, still a member, and still the
	// institution the settlement agent opened an account for. The last of those
	// is where a name lives — the settlement agent learns one from the acmt.007
	// and the clearing house learns none from the acmt.010 — so it is the row
	// that can say WHICH institution holds the address.
	if _, err := h.getRosterEntry(h.debtorBIC); err != nil {
		t.Fatalf("the incumbent is no longer in the roster: %v", err)
	}
	if got := h.getSettlementMember(t, h.debtorBIC); got.Name == "Impostor Bank" {
		t.Error("the refused admission took the incumbent's settlement account")
	}
	if got := h.getBank(t, h.debtorPID); got.Status != payment.BankMember {
		t.Errorf("the incumbent is %q after the refusal, want %q", got.Status, payment.BankMember)
	}
}

// TestAnAdmissionAlreadyUnderWayIsItsOwnRefusal pins the one case of a taken
// address where nobody answers to the address yet.
//
// The three other ways ErrAddressTaken is reached are all somebody else holding
// it: a member (the roster read), an institution, another bank of this mesh. A
// RESERVED address is none of those — it is this same bank, between claiming its
// address and getting its actor — and the difference is the whole reason the
// layer above needs to tell them apart. api.handleAddParticipant answers the
// other three with "admit this bank on an address of its own", which is advice
// that cannot be followed here: the address is not the problem, and the fix is to
// wait for the application that is already out.
//
// The reservation is made through claimAddress, which is the call Admit itself
// makes, because the window it opens is otherwise a few microseconds wide and
// nothing outside this package can hold it open. What it costs is a white-box
// test; what it buys is a deterministic one.
func TestAnAdmissionAlreadyUnderWayIsItsOwnRefusal(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	// An admission on this address is in flight: claimed, and not yet
	// registered.
	if redriving, err := h.mesh.claimAddress(joinerBIC); err != nil || redriving != "" {
		t.Fatalf("claiming a free address: (%q, %v), want (\"\", nil)", redriving, err)
	}

	before := h.bankCount(t)
	_, err := h.mesh.Admit(ctx, "Nordhaven Bank", joinerBIC, euroOnly)
	if err == nil {
		t.Fatal("a second admission on an address already claimed succeeded")
	}
	if !errors.Is(err, ErrAdmissionInFlight) {
		t.Errorf("Admit on a reserved address: %v, want ErrAdmissionInFlight", err)
	}
	// And still a taken address, so nothing that only asks that question has
	// stopped getting an answer.
	if !errors.Is(err, ErrAddressTaken) {
		t.Errorf("ErrAdmissionInFlight does not wrap ErrAddressTaken: %v", err)
	}
	if after := h.bankCount(t); after != before {
		t.Errorf("the refused admission wrote %d bank row(s)", after-before)
	}

	// The reservation goes back, and the address was never anybody else's: the
	// bank whose admission was in flight can still be admitted on it.
	h.mesh.releaseAddress(joinerBIC)
	joiner, err := h.mesh.Admit(ctx, "Nordhaven Bank", joinerBIC, euroOnly)
	if err != nil {
		t.Fatalf("admitting once the reservation is released: %v", err)
	}
	h.drain(t)
	if got := h.getBank(t, joiner.ID); got.Status != payment.BankMember {
		t.Errorf("the bank is %q, want %q", got.Status, payment.BankMember)
	}
}

// TestNothingIsWrittenWhenTheAddressIsRefused is the ordering, made falsifiable.
// The address is claimed before the bank's unit of work runs, so a clash writes
// no row at all — where the call this replaced wrote the row and THEN asked.
//
// # Two refused addresses, and only one of them pins the ordering
//
// A MEMBER's address is refused by the roster read, which happens before
// anything else in Admit, so it would write nothing whichever order the two
// steps came in. It is here because it is the case an operator actually hits.
//
// The CLEARING HOUSE's address is the one that bites. It is in no roster — the
// two institutions have no bank row and no entry — so the roster read passes and
// the only thing that refuses it is the actor table. Swap Admit's claim and its
// unit of work and this is the address that leaves a bank row behind.
func TestNothingIsWrittenWhenTheAddressIsRefused(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	for _, tc := range []struct {
		what string
		bic  iso20022.BIC
	}{
		{"a member's address", h.debtorBIC},
		{"the clearing house's address, which is in no roster", h.cfg.ClearingHouseBIC},
	} {
		before := h.bankCount(t)
		if _, err := h.mesh.Admit(ctx, "Impostor Bank", tc.bic, euroOnly); err == nil {
			t.Fatalf("Admit on %s succeeded", tc.what)
		} else if !errors.Is(err, ErrAddressTaken) {
			t.Fatalf("Admit on %s: %v, want ErrAddressTaken", tc.what, err)
		}
		if after := h.bankCount(t); after != before {
			t.Errorf("%s wrote %d bank row(s); the address is claimed BEFORE the unit of work runs",
				tc.what, after-before)
		}
	}
}

// TestTheMessagesAnAdmissionPutsOnTheWire is the counterpart of
// TestTheMessagesACutOffPutsOnTheWire and TestTheMessagesAReturnPutsOnTheWire:
// four messages, in order, between the institutions that send them.
//
// # It is a SEQUENCE and not a set, unlike the other two
//
// Every message here is sent from the handler of the one before it, so there is
// no interleaving to be undetermined about — which is not true of a cut-off,
// where the settlement agent addresses several banks at once. The tap fires on
// the RECEIVING actor's goroutine, so what is asserted is handling order, and a
// chain of four is the one shape in which handling order is the same as send
// order.
//
// The FIRST message is the one worth naming: it is sent by a bank that was not
// addressable a moment earlier, which is true of no other flow in this system.
func TestTheMessagesAnAdmissionPutsOnTheWire(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	mark := h.messagesSeen()
	if _, err := h.mesh.Admit(ctx, "Nordhaven Bank", joinerBIC, euroOnly); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	h.drain(t)

	assertWire(t, h.messagesFrom(mark), []hop{
		{joinerBIC, h.cfg.ClearingHouseBIC, "acmt.007.001.03"},
		{h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC, "acmt.007.001.03"},
		{h.cfg.CentralBankBIC, h.cfg.ClearingHouseBIC, "acmt.010.001.03"},
		{h.cfg.ClearingHouseBIC, joinerBIC, "acmt.010.001.03"},
	})
}

// TestTheMessagesATwoAssetAdmissionPutsOnTheWire is the same chain twice, and
// the schema is why.
//
// acmt.007's Acct/Ccy is minOccurs="1" maxOccurs="1", so a bank joining in two
// assets sends two applications and is answered twice — eight messages for one
// admission, under one process id. The alternative shape, one request naming two
// currencies, is not a message the standard has.
//
// The two chains do NOT interleave, and that is a fact about this transport
// rather than about the design: Mesh.send pushes onto the target's queue
// synchronously on the sender's goroutine, and each actor pops its own queue in
// order, so the first application is carried the whole way before the second is
// picked up. A real network would interleave them freely, and nothing in the
// flow depends on it not doing so — which is why what is asserted per hop is a
// COUNT, and only the first message's identity is asserted positionally.
func TestTheMessagesATwoAssetAdmissionPutsOnTheWire(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	mark := h.messagesSeen()
	if _, err := h.mesh.Admit(ctx, "Nordhaven Bank", joinerBIC, euroAndDollar); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	h.drain(t)

	seen := h.messagesFrom(mark)
	want := map[hop]int{
		{joinerBIC, h.cfg.ClearingHouseBIC, "acmt.007.001.03"}:            2,
		{h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC, "acmt.007.001.03"}: 2,
		{h.cfg.CentralBankBIC, h.cfg.ClearingHouseBIC, "acmt.010.001.03"}: 2,
		{h.cfg.ClearingHouseBIC, joinerBIC, "acmt.010.001.03"}:            2,
	}
	got := map[hop]int{}
	for i, m := range seen {
		hp := hopOf(t, m)
		got[hp]++
		if i == 0 && hp != (hop{joinerBIC, h.cfg.ClearingHouseBIC, "acmt.007.001.03"}) {
			t.Errorf("the admission's first message is %s -> %s (%s), want the applicant's own request",
				hp.from, hp.to, hp.msgDef)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("a two-asset admission put %v on the wire, want %v", got, want)
	}
	for h, n := range want {
		if got[h] != n {
			t.Errorf("%s -> %s (%s) travelled %d times, want %d", h.from, h.to, h.msgDef, got[h], n)
		}
	}
	// One admission and not two: every message of it echoes one process id.
	assertOneProcessID(t, seen)
}

// TestABankToldItIsAMemberIsAlreadyRoutable is the ordering inside
// csm.receiveAdmissionStatus, made falsifiable.
//
// The clearing house writes its routing entry BEFORE it forwards the
// acknowledgement, so a bank told it is a member is one this institution can
// already route to. The other order leaves an interval in which the bank has
// recorded its membership and a pacs.008 addressed to it comes back RC01 — the
// settlement path's statement-before-answer rule in a new place.
//
// # How it is observed, and why nothing after the drain could see it
//
// Both writes have happened by the time the conversation ends, so no assertion
// made afterwards can tell the two orders apart. The tap is what can: it runs on
// the RECEIVING actor's goroutine, before that actor's handler, so at the moment
// the joining bank is handed its acknowledgement this reads the clearing house's
// own row and records whether it was there.
//
// Swapping the two statements makes this a RACE rather than an inversion — the
// clearing house's unit of work is usually quicker than the bank's actor being
// scheduled — so a plain swap leaves it green. A swap plus a delay before the
// write fails it every run. That is the same trap TestTheMessagesACutOffPutsOnTheWire
// and TestTheMessagesAReturnPutsOnTheWire both record, hit a third time on a
// different pair.
func TestABankToldItIsAMemberIsAlreadyRoutable(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	var routable, told bool
	h.watch(func(to, from iso20022.BIC, raw []byte) {
		if to != joinerBIC {
			return
		}
		env, err := iso20022.Unmarshal(raw)
		if err != nil || env.AppHdr.MsgDefIdr != "acmt.010.001.03" {
			return
		}
		told = true
		_, err = h.getRosterEntry(joinerBIC)
		routable = err == nil
	})

	if _, err := h.mesh.Admit(ctx, "Nordhaven Bank", joinerBIC, euroOnly); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	h.drain(t)

	if !told {
		t.Fatal("the joining bank was never handed an acknowledgement; this test measured nothing")
	}
	if !routable {
		t.Error("the bank was told it is a member before the clearing house could route to it; " +
			"the routing entry is written before the acknowledgement is forwarded")
	}
}

// TestTheClearingHouseRefusesASecondInstitutionOnAnAdmittedAddress is the
// domain's authority over membership, exercised where it can be reached.
//
// # Why it is injected
//
// Mesh.Admit claims the address at the mesh before anything is written or sent,
// so an impostor never gets a message onto the wire — which means the only
// requests that reach this actor on a BIC already in its roster are the same
// bank's second currency and an operator re-driving. Both are relayed. The
// refusal exists for a request that did NOT come through Mesh.Admit, which in a
// real network is any request from a counterparty, and here is this injection.
//
// That is not a gap in the design; it is what "two authorities on one address"
// resolves to. The mesh's refusal is about connectivity and gets there first;
// the clearing house's is about MEMBERSHIP and is what an institution with its
// own store would still have to make.
//
// # What it refuses on
//
// The admission reference and not the address. A request quoting the reference
// the entry already holds is the same admission and is relayed — asserted here
// too, because a refusal keyed on the address alone would refuse exactly the two
// cases it must allow and never fire on this one.
func TestTheClearingHouseRefusesASecondInstitutionOnAnAdmittedAddress(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	// A member of this scheme, admitted through the mesh's own door.
	if _, err := h.mesh.Admit(ctx, "Nordhaven Bank", joinerBIC, euroOnly); err != nil {
		t.Fatalf("Admit: %v", err)
	}
	h.drain(t)
	entry, err := h.getRosterEntry(joinerBIC)
	if err != nil {
		t.Fatalf("the admitted bank is not in the roster: %v", err)
	}
	member := h.getSettlementMember(t, joinerBIC)

	apply := func(t *testing.T, ref string) {
		t.Helper()
		env, err := payment.AdmissionMessage(
			payment.AdmissionRequest{Name: "Impostor Bank", BIC: joinerBIC, Asset: "EUR", Ref: ref},
			h.cfg.CentralBankBIC,
			payment.MessageContext{
				From: joinerBIC, To: h.cfg.ClearingHouseBIC, MsgID: "impostor-" + ref, Now: testTime,
			})
		if err != nil {
			t.Fatalf("composing the application: %v", err)
		}
		if err := h.mesh.send(joinerBIC, h.cfg.ClearingHouseBIC, env); err != nil {
			t.Fatalf("sending the application: %v", err)
		}
		h.drain(t)
	}

	t.Run("a different admission is refused before it is relayed", func(t *testing.T) {
		mark := h.messagesSeen()
		apply(t, "some-other-admission")

		for _, m := range h.messagesFrom(mark) {
			if hopOf(t, m).to == h.cfg.CentralBankBIC {
				t.Error("the request was relayed to the settlement agent; the refusal is the clearing house's and comes first")
			}
		}
		if n := countHops(t, h.messagesFrom(mark), hop{h.cfg.ClearingHouseBIC, joinerBIC, "acmt.011.001.03"}); n != 1 {
			t.Errorf("the applicant was sent %d acmt.011, want 1", n)
		}
		// Routing points where it pointed, and the settlement agent opened
		// nothing: the refusal is BEFORE the relay, so the account the impostor
		// asked for was never even requested.
		//
		// The ADMISSION REFERENCE is what shows the first of those. It is the
		// incumbent's row or the impostor's, and the two differ in nothing else —
		// same address, same asset — which is exactly why the refusal is keyed on
		// it. The settlement agent's row still names the incumbent, which is the
		// second: that institution DOES learn a name, from the acmt.007's
		// Org/FullLglNm, and the impostor's request never reached it.
		if got, err := h.getRosterEntry(joinerBIC); err != nil || got.AdmissionRef != entry.AdmissionRef {
			t.Errorf("the roster entry is now %+v (%v), want the incumbent's %+v", got, err, entry)
		}
		if got := h.getSettlementMember(t, joinerBIC); got.Name != member.Name {
			t.Errorf("the settlement agent's row now names %q, want %q", got.Name, member.Name)
		}
	})

	t.Run("the same admission is relayed", func(t *testing.T) {
		mark := h.messagesSeen()
		apply(t, entry.AdmissionRef)

		if n := countHops(t, h.messagesFrom(mark), hop{h.cfg.ClearingHouseBIC, h.cfg.CentralBankBIC, "acmt.007.001.03"}); n != 1 {
			t.Error("a request quoting the entry's own admission reference was not relayed; " +
				"a second currency and a re-drive both look like this")
		}
	})
}

// TestTheClearingHouseRefusesAnApplicationOnAnotherBanksAddress is the
// comparison the relay did not make: an acmt.007 names its applicant in the
// DOCUMENT and its sender in the HEADER, and nothing required them to agree.
//
// payment.RecordMembershipTx makes exactly this comparison one hop later, and
// the two are not the same check made twice: that one is a bank refusing a
// message about somebody else, made from its own id, and this one is a clearing
// house refusing an applicant who is not the sender. Without it a bank could
// apply on an address it does not hold, and what it would get is a settlement
// account opened in the central bank's book for that address and a routing entry
// written from the answer — neither of which the impostor ever touches, and
// neither of which the settlement agent could have refused, because an account
// servicer asked about one address cannot tell who asked.
//
// The refusal is answered to the SENDER, which is the one message on this path
// addressed to somebody other than the applicant. The applicant never asked for
// anything.
func TestTheClearingHouseRefusesAnApplicationOnAnotherBanksAddress(t *testing.T) {
	h := newMeshHarness(t)

	mark := h.messagesSeen()
	env, err := payment.AdmissionMessage(
		payment.AdmissionRequest{Name: "Nordhaven Bank", BIC: joinerBIC, Asset: "EUR", Ref: "not-my-address"},
		h.cfg.CentralBankBIC,
		payment.MessageContext{
			From: h.debtorBIC, To: h.cfg.ClearingHouseBIC, MsgID: "on-behalf-of", Now: testTime,
		})
	if err != nil {
		t.Fatalf("composing the application: %v", err)
	}
	if err := h.mesh.send(h.debtorBIC, h.cfg.ClearingHouseBIC, env); err != nil {
		t.Fatalf("sending the application: %v", err)
	}
	h.drain(t)

	for _, m := range h.messagesFrom(mark) {
		if hopOf(t, m).to == h.cfg.CentralBankBIC {
			t.Error("the request was relayed to the settlement agent; the applicant is not who sent it")
		}
	}
	if n := countHops(t, h.messagesFrom(mark), hop{h.cfg.ClearingHouseBIC, h.debtorBIC, "acmt.011.001.03"}); n != 1 {
		t.Errorf("the SENDER was sent %d acmt.011, want 1", n)
	}
	if n := countHops(t, h.messagesFrom(mark), hop{h.cfg.ClearingHouseBIC, joinerBIC, "acmt.011.001.03"}); n != 0 {
		t.Errorf("the named applicant was sent %d acmt.011; it never asked for anything", n)
	}
	// And nothing was written for the address the impostor named.
	if _, err := h.getRosterEntry(joinerBIC); !errors.Is(err, payment.ErrRosterEntryNotFound) {
		t.Errorf("the clearing house routes to %s: %v", joinerBIC, err)
	}
	err = h.net.Store().View(context.Background(), func(ctx context.Context, tx payment.Tx) error {
		_, err := tx.GetSettlementMember(ctx, joinerBIC)
		return err
	})
	if !errors.Is(err, payment.ErrSettlementMemberNotFound) {
		t.Errorf("the settlement agent holds an account for %s: %v", joinerBIC, err)
	}
}

// TestAFoundedBankIsNotAdmittedByARestart is the behaviour change joinRoster's
// doc describes, asserted rather than stated.
//
// The mesh gives an actor to every bank the CLEARING HOUSE routes to, and a
// founded, unadmitted bank is in no roster. Reading the bank rows instead would
// hand it an actor and make it look reachable — and it would not be, because the
// clearing house would answer RC01 for its address and the settlement agent
// holds no account for it.
//
// It is driven through ForgetBanks and JoinRoster rather than a second mesh,
// because that pair is what a reset does and is the only way a live mesh re-reads
// the roster.
func TestAFoundedBankIsNotAdmittedByARestart(t *testing.T) {
	h := newMeshHarness(t)
	ctx := context.Background()

	joiner, err := h.net.FoundBank(ctx, "Nordhaven Bank", joinerBIC, euroOnly)
	if err != nil {
		t.Fatalf("FoundBank: %v", err)
	}
	if err := h.mesh.ForgetBanks(drainCtx(t)); err != nil {
		t.Fatalf("ForgetBanks: %v", err)
	}
	if err := h.mesh.JoinRoster(ctx); err != nil {
		t.Fatalf("JoinRoster: %v", err)
	}

	h.mesh.mu.Lock()
	_, actor := h.mesh.actors[joinerBIC]
	_, indexed := h.mesh.banks[joiner.ID]
	_, incumbent := h.mesh.actors[h.debtorBIC]
	h.mesh.mu.Unlock()

	if actor || indexed {
		t.Error("a founded, unadmitted bank was given an actor; the roster is what says who is a member")
	}
	if !incumbent {
		t.Error("the admitted banks were not rejoined")
	}
	// And its address is still free, which is what makes finishing its admission
	// possible at all.
	if _, err := h.mesh.Admit(ctx, "Nordhaven Bank", joinerBIC, euroOnly); err != nil {
		t.Errorf("finishing a founded bank's admission after a rejoin: %v", err)
	}
}

// TestAFoundedBankCanNeitherPayNorBePaid is the enforcement of the sentence this
// sub-project's spec has stated since it was written, and which was measured
// FALSE in both halves before this test existed.
//
// The two halves failed for two different reasons and ended in one place:
//
//   - Paying. "DepositTx refuses a founded bank, so it has no funded customer to
//     pay from" is a claim about ONE way of funding a customer. An arranged
//     overdraft is another, and lending.DisburseTx is a third, and neither
//     consults membership. "postDebtorLegTx refuses an asset it holds no accounts
//     in" never fires either: FoundBankTx gives a bank internal accounts in every
//     asset it is founded with.
//   - Being paid. A founded bank has a mesh actor from the moment Mesh.Admit
//     reserves its address, and nothing on the way in consults the roster.
//
// Both reached Cleared, and at the cut-off csm.settlementLegs could not name a
// non-member in the pacs.009 — so the whole cycle stayed Closed with every other
// member's payments in it. That is the blast radius this test's third assertion
// is about: the other member's payment must SETTLE.
//
// The refusal is asserted at the door, which is where it is made. The clearing
// house makes it again from its own row and
// TestTheClearingHouseWillNotClearForANonMember in package payment is what holds
// that one — measured, the clearing house's refusal alone leaves the paying
// direction's money in a suspense, because the pacs.002 that would reverse it is
// addressed through the roster too and dead-letters at the non-member.
func TestAFoundedBankCanNeitherPayNorBePaid(t *testing.T) {
	const foundedIBAN = "FR7630006000011234567890189"

	// found builds the state Mesh.Admit's synchronous half leaves: a bank with a
	// book and an actor, in no roster, whose customer has spendable money from an
	// arranged overdraft rather than from a deposit.
	found := func(t *testing.T, h *meshHarness) (*payment.Bank, deposit.Account) {
		t.Helper()
		ctx := context.Background()
		b, err := h.net.FoundBank(ctx, "Nordhaven Bank", joinerBIC, euroOnly)
		if err != nil {
			t.Fatalf("FoundBank: %v", err)
		}
		if err := h.mesh.AddBank(b); err != nil {
			t.Fatalf("AddBank: %v", err)
		}
		return b, h.openCustomer(t, b, "Nora", "EUR", harnessFunding, foundedIBAN)
	}

	for _, tc := range []struct {
		name string
		req  func(t *testing.T, h *meshHarness, b *payment.Bank, acct deposit.Account) payment.InitiatePaymentRequest
		// payer names the bank whose clearing suspense the refused instruction
		// would have posted into, so the assertion reads the right book.
		payer func(h *meshHarness, founded *payment.Bank) payment.ParticipantID
		want  string
	}{
		{
			name:  "it pays",
			payer: func(_ *meshHarness, founded *payment.Bank) payment.ParticipantID { return founded.ID },
			req: func(t *testing.T, h *meshHarness, b *payment.Bank, acct deposit.Account) payment.InitiatePaymentRequest {
				return payment.InitiatePaymentRequest{
					Scheme: payment.SchemeSEPACT,
					Debtor: payment.PartyRef{
						Account:    acct.ID,
						Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: foundedIBAN},
					},
					Creditor:        h.creditorRef(creditorIBAN),
					Amount:          harnessAmount,
					Description:     "from a bank no scheme has admitted",
					CreditorDetails: payment.PartyDetails{Agent: h.creditorBIC, Name: h.creditorAcct.Name},
				}
			},
			want: "payer's bank",
		},
		{
			name:  "it is paid",
			payer: func(h *meshHarness, _ *payment.Bank) payment.ParticipantID { return h.debtorPID },
			req: func(t *testing.T, h *meshHarness, b *payment.Bank, acct deposit.Account) payment.InitiatePaymentRequest {
				return payment.InitiatePaymentRequest{
					Scheme: payment.SchemeSEPACT,
					Debtor: h.debtorRef(),
					Creditor: payment.PartyRef{
						Account:    acct.ID,
						Identifier: deposit.Identifier{Scheme: deposit.IdentifierIBAN, Value: foundedIBAN},
					},
					Amount:          harnessAmount,
					Description:     "to a bank no scheme has admitted",
					CreditorDetails: payment.PartyDetails{Agent: b.BIC, Name: "Nora"},
				}
			},
			want: "payee's bank",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newMeshHarness(t)
			ctx := context.Background()
			b, acct := found(t, h)

			// One healthy payment between two members, submitted BEFORE the
			// refused one so that it is in the same open cycle. It is the other
			// member whose money used to stop.
			healthy := h.submitCreditTransfer(t)
			h.drain(t)

			// The payer on the refused instruction, whichever of the two it is.
			// Its suspense is read either side of the refusal rather than
			// compared with zero: on the "it is paid" case the payer is also the
			// bank that submitted the healthy payment, whose own money is
			// legitimately sitting there.
			payer := h.getBank(t, tc.payer(h, b))
			before, err := payer.Ledger.BookBalance(ctx, payer.Assets["EUR"].Suspense)
			if err != nil {
				t.Fatalf("reading the payer's clearing suspense: %v", err)
			}

			_, err = h.mesh.Submit(ctx, tc.req(t, h, b, acct))
			if !errors.Is(err, payment.ErrBankNotAdmitted) {
				t.Fatalf("submitting %s a founded bank: %v, want %v", tc.name, err, payment.ErrBankNotAdmitted)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the refusal says %q and does not name the %s", err, tc.want)
			}

			// Refused at the door means refused before the submitting bank's
			// half ran, so no debtor leg was posted. Task 16's on-us guard is in
			// the same place for the same reason.
			after, err := payer.Ledger.BookBalance(ctx, payer.Assets["EUR"].Suspense)
			if err != nil {
				t.Fatalf("reading the payer's clearing suspense: %v", err)
			}
			if after != before {
				t.Errorf("the refused submission moved the payer's clearing suspense from %d to %d", before, after)
			}

			// And the cut-off completes, so the other member is paid. This is
			// the assertion the whole finding was about: before the guard the
			// cycle stayed Closed with this payment in it.
			h.closeCycle(t)
			h.drain(t)
			if got := h.payment(t, healthy.ID); got.Status != payment.Settled {
				t.Errorf("the other member's payment is %v, want %v", got.Status, payment.Settled)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Reading the wire
// ---------------------------------------------------------------------------

// hop is one message as this file asserts on it: who sent it, who received it,
// and which message definition it was.
type hop struct {
	from, to iso20022.BIC
	msgDef   string
}

// hopOf parses one tapped message into the three things a wire assertion is
// about. It fails the test on a message that does not parse, because every
// message these tests provoke was built by this system's own codec.
func hopOf(t *testing.T, m tappedMessage) hop {
	t.Helper()
	env, err := iso20022.Unmarshal(m.raw)
	if err != nil {
		t.Fatalf("a message from %s to %s does not parse: %v", m.from, m.to, err)
	}
	return hop{m.from, m.to, env.AppHdr.MsgDefIdr}
}

// countHops is how many of the messages are that hop.
func countHops(t *testing.T, seen []tappedMessage, want hop) int {
	t.Helper()
	var n int
	for _, m := range seen {
		if hopOf(t, m) == want {
			n++
		}
	}
	return n
}

// assertWire compares what travelled against an expected SEQUENCE, whole and in
// order.
//
// In order, which the other two wire tests in this package deliberately do not
// do: a cut-off and a return both have a moment where one actor addresses two
// others, so their orders are partly undetermined and they assert a set plus the
// pairs that are really forced. An admission is a chain — every message is sent
// from the handler of the one before it — so the order is the whole of what
// there is to get wrong.
func assertWire(t *testing.T, seen []tappedMessage, want []hop) {
	t.Helper()
	got := make([]hop, len(seen))
	for i, m := range seen {
		got[i] = hopOf(t, m)
	}
	if len(got) != len(want) {
		t.Fatalf("the admission put %d messages on the wire, want %d:\ngot  %v\nwant %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d is %s -> %s (%s), want %s -> %s (%s)",
				i, got[i].from, got[i].to, got[i].msgDef, want[i].from, want[i].to, want[i].msgDef)
		}
	}
}

// assertOneProcessID reads Refs/PrcId off every acmt message that travelled and
// checks they are all the same value.
//
// It is what says a two-asset admission is ONE admission. The acknowledgement
// carries no back-reference to the request that caused it — the rejection does,
// at RjctdReqId, and the acknowledgement does not — so this element is the only
// thing in the family that correlates the conversation, and every message of one
// admission has to echo it.
func assertOneProcessID(t *testing.T, seen []tappedMessage) {
	t.Helper()
	var first string
	for _, m := range seen {
		env, err := iso20022.Unmarshal(m.raw)
		if err != nil {
			t.Fatalf("a message from %s does not parse: %v", m.from, err)
		}
		var ref string
		switch doc := env.Document.(type) {
		case *iso20022.Acmt007:
			ref = doc.AcctOpngReq.Refs.PrcId.Id
		case *iso20022.Acmt010:
			ref = doc.AcctReqAck.Refs.PrcId.Id
		case *iso20022.Acmt011:
			ref = doc.AcctReqRjctn.Refs.PrcId.Id
		default:
			continue
		}
		if ref == "" {
			t.Errorf("a %s carries no process id; nothing else correlates this conversation", env.AppHdr.MsgDefIdr)
			continue
		}
		if first == "" {
			first = ref
			continue
		}
		if ref != first {
			t.Errorf("a %s quotes admission %q and the first message quoted %q", env.AppHdr.MsgDefIdr, ref, first)
		}
	}
	if first == "" {
		t.Fatal("no acmt message travelled; this assertion measured nothing")
	}
}
