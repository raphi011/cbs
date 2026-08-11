package mesh

import (
	"context"
	"errors"
	"fmt"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/mesh/wire"
	"github.com/raphi011/cbs/payment"
)

// The DOORS: everything that comes into this system from outside it.
//
// A message arrives in an inbox and is dealt with by an actor's handler, on that
// actor's own goroutine. Nothing below is a message. A customer instructs their
// bank, an operator reaches a cut-off or rejects a payment, a bank asks to join
// the scheme or moves cash onto reserve — each is an instruction from outside
// the mesh, so each runs the instructing institution's own half SYNCHRONOUSLY,
// on the caller's goroutine, and only then sends.
//
// Two properties are shared by every one of them and are stated here rather than
// six times. The synchronous half is what lets a caller be told "no" — a
// customer whose instruction fails their own bank's checks hears it then and
// there, which is why api can answer 422 rather than 202 followed by a rejection
// nobody can be told about. And the SEND IS OUTSIDE THE UNIT OF WORK: enqueueing
// while holding a transaction would schedule work against uncommitted state, or
// against state a rollback removed, and because the queue is unbounded it would
// not even deadlock — it would just be wrong. TestARolledBackSubmitSendsNothing
// pins the second.
//
// What none of them answers is what the far side thinks. That arrives later, at
// another actor, as a message; a caller that wants to know reads the store again
// after the conversation has finished, and a test drains first.

// Submit runs the submitting bank's half synchronously and then sends.
//
// It returns an Initiated payment and nothing more, which is why api answers 202
// rather than 201.
//
// It runs on the CALLER's goroutine and not the bank actor's: an actor handles
// what arrives in its inbox, and a customer instruction comes in from outside
// the mesh. The work is still marked as the bank's, so the recorder attributes
// every book it touches to the bank rather than to whoever called in.
//
// # Which bank is handed the instruction
//
// The scheme's DIRECTION decides, and it is asked rather than assumed. A credit
// transfer goes to the payer's bank, because the payer is instructing their own
// bank to push; a direct debit goes to the PAYEE's bank, because a collection is
// the payee asking for money it is owed.
//
// Taking the debtor unconditionally is the wrong bank for every direct debit,
// and it is invisible until a pull exists: the payment goes through, the books
// balance, and the only thing wrong is that the wrong institution did the work
// and signed the message. api's handleSubmitPayment asks the same question one
// layer up.
//
// It reads the network to ask, so like Mesh.now it exists only on a mesh that
// has one.
//
// # And which payments there is no bank to hand it to at all
//
// An ON-US payment — one bank at both ends — is refused before any of that.
// Nothing leaves the institution, so no reserves move, no position nets and no
// settlement agent has anything to settle. That refusal is this system declining
// a ROUTE and not the payment; a book transfer between two customers of one bank
// is a real product and a different one, performed by the bank's own register.
// See ErrOnUsPayment.
//
// A payment to or from a bank the scheme has NOT ADMITTED is refused in the same
// place — see payment.ErrBankNotAdmitted.
func (m *Mesh) Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	scheme, ok := m.clearingHouse.Scheme(req.Scheme)
	if !ok {
		return payment.Payment{}, fmt.Errorf("mesh: no scheme %q, so no bank submits it: %w", req.Scheme, payment.ErrSchemeNotFound)
	}
	// A payment that never leaves one bank is not a payment this mesh carries.
	//
	// Both customers bank at the same institution, so the movement is between two
	// of that bank's own deposit accounts: no interbank obligation exists, so
	// there is nothing to clear and nothing to settle, and a real bank books it
	// internally without a scheme ever hearing about it. See ErrOnUsPayment for
	// the three things this system did instead when one was submitted anyway.
	//
	// Refused HERE, at the one door every submission comes through — api's two
	// handlers and this package's own tests all reach the mesh this way — and
	// before the submitting bank's half runs. Submit is synchronous, so a guard
	// placed any later would have to unwind a committed debtor leg rather than
	// decline it.
	//
	// It is asked of the two PARTIES and not of the submitter. Which bank submits
	// flips with the scheme's direction, and on-us is precisely the case where
	// both answers are the same institution; a guard that read the submitter
	// would be comparing a bank with itself.
	//
	// The two AGENTS are compared where an instruction names both, and the
	// counterparty's address is RESOLVED against the submitter's own register
	// further down — see the check after the actor lookup. Two guards for one
	// rule, and the reason is that they cover different instructions rather than
	// the same one twice: an instruction from a customer names its own side's bank
	// nowhere, because the submitting bank fills that in from its own row
	// (payment.SubmitPaymentTx), so this comparison cannot fire on one. What it
	// catches is a caller that names both — the seed, and this package's fixtures.
	//
	// What an instruction leaves unnamed is its OWN side rather than the other's:
	// a bank fills its own from its own register. See payment.PartyRef.
	if req.DebtorDetails.Agent != "" && req.DebtorDetails.Agent == req.CreditorDetails.Agent {
		return payment.Payment{}, fmt.Errorf("mesh: %s is both the payer's bank and the payee's for this instruction: %w",
			req.DebtorDetails.Agent, ErrOnUsPayment)
	}
	// And a payment one of whose banks the scheme has not admitted.
	//
	// Asked HERE, at the same door and of the same two parties, because the
	// answer that matters is the one given before the submitting bank's half
	// runs: that half posts the debtor leg, and every refusal downstream of it
	// has a committed posting to unwind. The clearing house asks it again from
	// its own row — payment.Network.bothBanksAreMembersTx — and that one is the
	// judgement; this one is the door declining to carry the instruction at all,
	// which is what makes the API answer a 422 rather than a 202 followed by a
	// rejection nobody can be told about. See payment.ErrBankNotAdmitted.
	//
	// It is a roster read and nothing else. What a request names is what the
	// roster is keyed by, so no bank row is read on the way. See
	// payment.Network.GetRosterEntryByBIC.
	//
	// A BIC nobody has been admitted on is not a member, which is a 422 and
	// ErrBankNotAdmitted rather than a 404. The clearing house cannot tell "no
	// such institution" from "an institution I have not admitted", and answering
	// 404 would be it reporting a bank row it should not be reading.
	//
	// # In practice it now fires on the SUBMITTING side only, and the other side
	// is covered by something stronger
	//
	// An instruction names no counterparty bank: the submitting bank derives one
	// from the counterparty's address, out of its own copy of this same roster.
	// So an address at a non-member resolves to nothing and is refused before the
	// debtor leg posts — payment.ErrBankCodeUnknown, at the payer's own bank,
	// which is EARLIER than this door and needs no read here at all. A copy of the
	// roster is a membership list, so subscribing subsumed half of this guard.
	//
	// The loop still walks both, because a caller that names both is a caller this
	// can answer precisely: the seed, this package's fixtures, and any future door
	// that fills the field in.
	for _, side := range []struct {
		role  string
		agent iso20022.BIC
	}{
		{"payer's bank", req.DebtorDetails.Agent},
		{"payee's bank", req.CreditorDetails.Agent},
	} {
		// A side naming no bank is skipped rather than refused, exactly as the
		// on-us guard above skips it: "not a member" is not the truth about a
		// party the request did not name, and what IS wrong with such a request
		// is SubmitPaymentTx's to say (ErrParticipantNotFound for the submitting
		// side, ErrCounterpartyNotNamed for the other).
		if side.agent == "" {
			continue
		}
		if _, err := m.clearingHouse.GetRosterEntryByBIC(ctx, side.agent); err != nil {
			if errors.Is(err, payment.ErrRosterEntryNotFound) {
				return payment.Payment{}, fmt.Errorf("mesh: the %s, %s, is not a member of %s: %w",
					side.role, side.agent, req.Scheme, payment.ErrBankNotAdmitted)
			}
			return payment.Payment{}, err
		}
	}
	submitter := submitterOf(scheme, req.DebtorDetails.Agent, req.CreditorDetails.Agent)

	b, ok := m.bankActor(submitter)
	if !ok {
		// The mesh has no actor to play this bank, so nothing it submitted could
		// ever be answered. Refusing here is better than accepting a payment that
		// would sit Initiated for ever.
		return payment.Payment{}, fmt.Errorf("mesh: no bank actor for %s", submitter)
	}
	// On-us, asked by ADDRESS, and this is the arm that fires for an instruction
	// a customer actually hands in.
	//
	// It RESOLVES rather than comparing BICs, and the reason survives the
	// derivation landing. A derived agent answers at INSTITUTION granularity, out
	// of a copy that pairs a bank code with a BIC — so "the counterparty's agent
	// is this bank" says the payee's address was issued under this bank's code,
	// and not that this bank holds the account. Those differ for exactly the case
	// that matters: an address under the right bank's code that the bank does not
	// hold, which is somebody else's customer or nobody's. What IS a fact this
	// bank holds is whether the address resolves in its own register.
	//
	// The participant comparison further up covers the instructions this cannot:
	// a caller that names both internal ids — the seed, and this package's
	// fixtures — has already said the answer, and the two guards cover different
	// instructions rather than the same one twice.
	//
	// It reads this bank's OWN register and no other, which is what makes it a
	// question this institution may ask at all. Marked as this bank's work so the
	// recorder attributes it here rather than to nobody.
	//
	// It cannot be asked before the actor lookup, because that lookup is what
	// says which bank is submitting. It is still before b.submit, which is the
	// ordering the paragraph above requires: Submit is synchronous, so a guard
	// any later would have to unwind a committed debtor leg.
	counterparty := req.Creditor
	if scheme.Direction() == payment.Pull {
		counterparty = req.Debtor
	}
	if counterparty.Identifier != (deposit.Identifier{}) {
		switch _, err := b.ops.ResolveIdentifier(wire.WithActor(ctx, b.bic), counterparty.Identifier); {
		case err == nil:
			return payment.Payment{}, fmt.Errorf("mesh: %s holds both the payer's account and the payee's for this instruction: %w",
				b.bic, ErrOnUsPayment)
		case errors.Is(err, deposit.ErrIdentifierNotFound):
			// The ordinary case: the payee is somebody else's customer, which is
			// the only thing this bank can conclude and the only thing it needs to.
		default:
			return payment.Payment{}, err
		}
	}
	return b.submit(ctx, req)
}

// CloseCycle reaches a cut-off: the clearing house nets the batch and then
// instructs the central bank to settle it.
//
// The instructing party is what differs from Submit: a customer instructs their
// bank, and an operator (or api's POST /cycles/{cid}/close) reaches a cut-off.
// See csm.closeCycle.
//
// What it does NOT answer is whether the cycle settled. It returns the cycle as
// the clearing house left it — Closed, with net positions — and settlement
// happens later, at another actor, and comes back as a message.
//
// It reads the clearing house's handler directly, so like Submit and Mesh.now it
// exists only on a mesh that has a network. A mesh with none has no cycles to
// close, so this is a precondition rather than an outcome — and it is refused
// rather than dereferenced, because unlike Submit there is no map lookup on the
// way in that would have caught it.
func (m *Mesh) CloseCycle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	if m.csm == nil {
		return payment.ClearingCycle{}, errors.New("mesh: no network, so there is no cycle to close")
	}
	return m.csm.closeCycle(ctx, id)
}

// Settle asks the clearing house to instruct settlement of a closed cycle
// AGAIN, after the settlement agent refused the first instruction.
//
// It is CloseCycle's second half on its own, and it exists because a refusal is
// otherwise terminal: a net payer short of reserves comes back AM04, nothing
// moves, and the cycle sits Closed with every payer debited and every payee
// unpaid, with no transition out for any object. The operator funds the short
// member and calls this. See csm.settle, which has the whole of that state and
// both of the guards that stop it settling twice.
//
// Like CloseCycle it answers with the cycle rather than with a settlement, and
// refuses on a mesh with no network rather than dereferencing.
func (m *Mesh) Settle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	if m.csm == nil {
		return payment.ClearingCycle{}, errors.New("mesh: no network, so there is no cycle to settle")
	}
	return m.csm.settle(ctx, id)
}

// RefreshDirectory is one member bank subscribing: it takes the roster the
// clearing house publishes and replaces that bank's own copy with it.
//
// # It is not a message, and that is why it is here
//
// Every other hop in this package is an ISO 20022 document going into an inbox.
// This one is a FILE being delivered — the shape a real routing directory
// arrives in, since the EPC's Register of Participants is downloaded and not
// queried per payment — so there is no envelope, no correlation and no
// asynchrony to arrange. What the mesh is standing in for is the vendor, and the
// mesh is the only thing here that holds both institutions' handles.
//
// It runs on the CALLER's goroutine and reaches two databases, exactly as Admit
// does and for the same reason: the subscriber has an actor, but the act is not
// something that arrived in its inbox.
//
// The two reads cannot be one unit of work and must not look like one. The
// roster is read at the clearing house and committed there before the copy is
// written at the bank, so a member admitted between the two shows up on the next
// refresh — which is the staleness this design is built out of, arriving at the
// smallest scale it has.
//
// # Not a timer, and not a push
//
// A background poller would buy realism in a repository whose suites run on a
// fake clock and pay for it in flaky tests. A push would make the clearing house
// hold a subscriber list and a retry policy, which is a delivery system rather
// than a publisher — and the real vendor does not know who is listening.
func (m *Mesh) RefreshDirectory(ctx context.Context, bic iso20022.BIC) ([]payment.DirectoryEntry, error) {
	if m.nets == nil {
		return nil, errors.New("mesh: no network, so there is no directory to refresh")
	}
	published, err := m.clearingHouse.ListRosterEntries(ctx)
	if err != nil {
		return nil, fmt.Errorf("mesh: reading the published roster for %s: %w", bic, err)
	}
	subscriber, err := m.nets.Bank(ctx, payment.ParticipantID(bic))
	if err != nil {
		return nil, fmt.Errorf("mesh: opening %s's store: %w", bic, err)
	}
	return subscriber.RefreshDirectory(ctx, published)
}

// Reject is the clearing house declining a payment it is holding, on an
// operator's say-so rather than on a counterparty's.
//
// Every other rejection in this system is DECIDED by an actor that was sent a
// message. This one comes in from outside the mesh, exactly as the doors above
// do: an operator instructing is not a message arriving.
//
// # It is half of a rejection, and the other half is another actor's
//
// What runs here is the clearing house's own unit of work — the payment moves to
// Rejected and leaves its cycle — and that is what the caller is told about,
// synchronously. The payer's money is still in their own bank's clearing
// suspense at that point. Giving it back is the PAYER's BANK's act, in its own
// book, and the pacs.002 this sends is what tells that bank to do it
// (bank.receiveStatus). A caller that wants to see the refund reads the payer's
// balance after the conversation has finished; a test drains.
//
// That seam is the one RejectAtCSMTx names. It fails the way it really would:
// the rejection stands, the refund does not happen, and the mesh reports a dead
// letter rather than nothing.
//
// # The message it refers back to
//
// A pacs.002 says which message it is about, and there ISN'T one — no bank sent
// anything that provoked this. So the original is named as unavailable, by the
// NOTPROVIDED convention answerUnreadable uses: inventing a message id the
// payer's bank never sent would be worse than saying there was none. Nothing
// downstream needs it, because a bank matches a status to a payment by the
// transaction reference.
//
// Like CloseCycle it refuses on a mesh with no network rather than
// dereferencing: it reaches the clearing house's handler directly, so there is
// no map lookup on the way in that would have caught it.
func (m *Mesh) Reject(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, text string) (payment.Payment, error) {
	if m.csm == nil {
		return payment.Payment{}, errors.New("mesh: no network, so there is no payment to reject")
	}
	return m.csm.reject(ctx, id, code, text)
}

// Return sends a settled payment back: the R-transaction, and the last thing
// that can happen to a payment.
//
// The returning bank's half POSTS before the message exists, which is what makes
// this door's synchronous half different from the others': see
// bank.returnPayment.
//
// # Which bank is handed the instruction
//
// The bank that RECEIVED the original instruction, which is never the one that
// submitted it. On a push that is the payee's bank, which was credited and has
// discovered it cannot apply the money; on a pull it is the payer's bank, whose
// customer disputes the collection. Both are the far side of the message that
// started the payment, which is what makes a return the only flow here that
// begins at the bank that answered. See returnerOf.
//
// # It answers with an error and nothing else
//
// The returning bank's half can refuse, which is why an error is the whole of
// what a caller needs: on a push a payee who has spent the money comes back AM04
// and nothing was sent.
//
// A PAYMENT would be no use. The row the caller could read is still Settled —
// one leg is posted, and a return is not finished until the other bank posts at
// another actor after this call has returned — so handing it back would say less
// than the caller already knows, and re-reading the row after the send would be
// a race dressed up as a result.
//
// So a send that fails after the leg is posted comes back as an error alone. The
// half-happened state is real and is recorded on the payment row — this bank's
// leg id, with the status still Settled — and nothing above this reads a Payment
// it could be carried in.
//
// Like Mesh.Submit it DEREFERENCES the network rather than checking, because a
// mesh with no network has no bank actors and every return to it was already an
// error. Mesh.CloseCycle refuses explicitly instead, because it has no map
// lookup on the way in that would have caught the case.
func (m *Mesh) Return(ctx context.Context, id payment.PaymentID, reason iso20022.ReturnReason, text string) error {
	// The routing question, and only that: which bank's instruction is this?
	// It is asked here rather than inside the bank for the reason Submit asks
	// the scheme here — the answer is what CHOOSES the actor, so no actor can
	// have made it. The read costs no book: a payment is a network-scoped row,
	// and reading one records nothing at all (see books_test.go).
	p, err := m.clearingHouse.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	scheme, ok := m.clearingHouse.Scheme(p.Scheme)
	if !ok {
		return fmt.Errorf("mesh: no scheme %q, so no bank returns %s: %w", p.Scheme, p.ID, payment.ErrSchemeNotFound)
	}
	returner := returnerOf(scheme, p.DebtorDetails.Agent, p.CreditorDetails.Agent)

	b, ok := m.bankActor(returner)
	if !ok {
		// Same refusal Submit makes, for the same reason: a return this mesh
		// has no actor to send would be one nobody could ever act on.
		return fmt.Errorf("mesh: no bank actor for %s", returner)
	}
	return b.returnPayment(ctx, id, reason, text)
}

// Lodge is a member bank moving its own vault cash onto reserve at the central
// bank.
//
// What comes back from this call is the INSTRUCTION that was sent, not a
// confirmation — the camt.025 arrives at bank.receiveLodgementReceipt after a
// Drain, exactly as an admission's acknowledgement does.
//
// # Why the caller names the bank and the asset
//
// A lodgement is one institution's decision about its own liquidity, so there is
// no routing question to answer and no scheme to consult: the acting bank IS the
// subject. That is the whole difference from Submit, which has to work out which
// of two banks submits before it can pick an actor.
//
// The bank is named by its ADDRESS, which is what the caller holds — api's
// listener is bound to one, and the operator console shows one. It took a
// ParticipantID while the bank index was keyed by that type; the two are one
// value now, so this is the type the argument always meant.
//
// The ASSET is named because a bank operating in two of them holds two reserve
// accounts and two vaults, and nothing about "move cash onto reserve" says which.
// There is no default and deliberately not the euro one joiningAssets applies: a
// bank that founded itself in dollars would have a euro lodgement invented for it.
//
// # The actor lookup refuses for Submit's reason
//
// A bank this mesh has no actor for could send nothing and be answered by nobody,
// so its lodgement would post a leg against a message that never left. Refused
// before the bank's half runs, which is the ordering every door in this file
// keeps.
func (m *Mesh) Lodge(ctx context.Context, bic iso20022.BIC, asset ledger.AssetCode,
	amount ledger.Amount) (payment.LodgementInstruction, error) {

	b, ok := m.bankActor(bic)
	if !ok {
		return payment.LodgementInstruction{}, fmt.Errorf("mesh: no bank actor for %s", bic)
	}
	return b.lodge(ctx, asset, amount)
}
