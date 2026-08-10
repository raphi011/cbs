package mesh

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/raphi011/cbs/deposit"
	"github.com/raphi011/cbs/iban"
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

// Admit brings a bank into being and applies to the scheme for it.
//
// What it does NOT answer is whether the scheme accepted — that arrives later,
// at two other actors, as a message. The bank it returns is Founded, which is a
// working bank that can open customer accounts and take cash in, and cannot
// lodge it.
//
// # The address is reserved first, and that is the orphan defect's fix
//
// A BIC is the only thing about an admission that can clash, and it is checked
// FIRST. The address is claimed before the bank's unit of work runs and released
// again if that unit of work fails, because an in-memory rollback is reliable
// and a rollback of a committed transaction is not. See claimAddress, and
// TestNothingIsWrittenWhenTheAddressIsRefused.
//
// # A taken address is two situations
//
// If it belongs to a bank this mesh founded that the roster has no entry for,
// the operator is re-driving an interrupted admission: nothing is founded twice
// and the acmt.007 goes out again. If it belongs to anybody else — a member, an
// actor that is not a bank, or an admission already in flight — it is refused.
// Both directions of getting this wrong have a name: refuse the first and a
// founded bank can never join, accept the second and admission overwrites an
// institution.
//
// The roster is read for that decision and the actor table decides it: the
// roster is the DOMAIN's truth about who holds an address, the actor table is
// the TRANSPORT's, and the lock is what makes the answer one answer.
//
// # A re-drive asks for the assets the bank actually has
//
// One acmt.007 asks for one currency, so this sends one per asset the bank
// operates in, in asset order. For a new bank that is what the caller named
// (with payment's joining default applied); for a RE-DRIVE it is the bank's own
// chart of accounts, and the caller's list is ignored — asking for a settlement
// account in an asset it holds none of would produce a reference it has nowhere
// to record.
//
// It mints a NEW process id every time, including on a re-drive. That is safe
// exactly because a re-drive is allowed only when the roster has no entry for
// the address, so the clearing house has no reference to compare against — and
// payment.Bank.AdmissionRef is empty until an acknowledgement is recorded.
//
// # A PARTLY admitted bank cannot be re-driven, and that is a gap with an owner
//
// A bank that recorded a membership in one asset and whose second asset's
// acknowledgement never arrived fails both conditions: it is a Member, so its
// row carries a reference, and it is in the roster, so this call refuses the
// address outright. A fresh admission would be refused by the clearing house and
// by the bank alike, correctly, since it really is a different admission.
//
// So there is no door. The bank clears in one asset and holds a settlement
// account at the central bank in a second that it does not know the number of —
// a deposit in that asset fails while the operator console reports the reserve,
// because the console reads the central bank's row. It is reachable only from a
// dead letter, since this transport carries every message exactly once.
//
// The obvious reconciliation does not find it. "A bank whose assets and the
// agent's accounts for its BIC do not match" MATCHES here: measured by parking
// the second acmt.010 before the bank, both are {EUR, USD}, because the agent
// opens the account before it acknowledges — what went missing is the bank's
// NOTE of the number and not the account. The discriminator is the one
// api.Server.reserveRows uses: an EMPTY settlement reference on the bank's own
// row in an asset, TOGETHER WITH a reserve row the agent answers for in that
// same asset. The same empty reference with NO reserve row is the other
// half-finished admission, which the mismatch comparison does find.
//
// Closing it needs a way to re-drive ONE asset of an existing admission, quoting
// the reference the bank already recorded rather than minting one — a decision
// about the flow rather than about this function.
//
// # The bank code is applied for, not brought
//
// country is the market the joining bank means to operate in, and it is the
// whole of what the caller says about addressing. The CODE its customers'
// addresses will carry is a national registry's allocation, and it arrives on
// the acknowledgement — which is why the bank this returns can open no
// addressable account yet (deposit.ErrNoIssuer), and why a re-drive reads the
// country off the bank's own row rather than off the caller.
//
// A caller that could supply the code would make the whole routing directory
// unnecessary and would be wrong about the world: a bank code has no computable
// relationship to a BIC, which is why a scheme has to publish the pairing.
func (m *Mesh) Admit(ctx context.Context, name string, bic iso20022.BIC, country iban.Country, assets []ledger.AssetCode) (*payment.Bank, error) {
	if m.nets == nil {
		return nil, errors.New("mesh: no network, so there is no bank to admit")
	}
	if err := bic.Validate(); err != nil {
		return nil, fmt.Errorf("mesh: %q: %w", name, err)
	}
	// Everything below is the joining bank's work, and is recorded as its own.
	// See wire.WithActor: the recorder has no other way to attribute a book, and
	// the bank whose book this is has no actor yet.
	ctx = wire.WithActor(ctx, bic)

	// The roster read is OUTSIDE the lock, for joinRoster's reason. What it
	// answers is the domain's question — is this address already a member's —
	// and the claim below is what turns the answer into a decision.
	_, err := m.clearingHouse.GetRosterEntryByBIC(ctx, bic)
	switch {
	case err == nil:
		return nil, fmt.Errorf("%w: %s is already a member of this scheme", ErrAddressTaken, bic)
	case !errors.Is(err, payment.ErrRosterEntryNotFound):
		return nil, fmt.Errorf("mesh: cannot tell whether %s is already admitted: %w", bic, err)
	}

	redriving, err := m.claimAddress(bic)
	if err != nil {
		return nil, err
	}

	// The APPLICANT's own network, over the applicant's own database: a bank's row
	// is the bank's, and the clearing house's schema has no table to write it to.
	// There is no counter to allocate an id from — a joining bank arrives knowing
	// its BIC, its BIC is its id, and asking the store set for that bank is what
	// creates the database. See payment.Stores.
	applicant, err := m.nets.Bank(ctx, payment.ParticipantID(bic))
	if err != nil {
		if !redriving {
			// A re-drive made no reservation, so there is none to give back.
			m.bus.Release(bic)
		}
		return nil, fmt.Errorf("mesh: opening %s's store: %w", bic, err)
	}

	var bank *payment.Bank
	if redriving {
		// A bank this mesh founded that the roster has no entry for. Its row is
		// read OUTSIDE the lock, for joinRoster's reason, and nothing is founded.
		if bank, err = applicant.GetBank(ctx, payment.ParticipantID(bic)); err != nil {
			return nil, fmt.Errorf("mesh: %s is re-driving its own interrupted admission and cannot read its row: %w", bic, err)
		}
	} else {
		if bank, err = applicant.FoundBank(ctx, name, bic, country, assets); err != nil {
			// The reservation goes back before the caller is told, so a refused
			// unit of work leaves the address exactly as free as it found it.
			m.bus.Release(bic)
			return nil, err
		}
		if err := m.AddBank(ctx, bank); err != nil {
			// Unreachable while the reservation holds — nothing else can have
			// taken the address — but a bank that is committed and unreachable is
			// the orphan again, so it is reported rather than assumed away.
			m.bus.Release(bic)
			return bank, fmt.Errorf("mesh: %s was founded and could not be given an actor: %w", bic, err)
		}
	}

	// The sends are OUTSIDE the unit of work, for the reason every door in this
	// file keeps: a request enqueued from inside FoundBankTx would be one the
	// scheme could act on against a bank the store then rolled back.
	ref := m.nextProcessID(bic)
	to := m.cfg.ClearingHouseBIC
	for _, asset := range slices.Sorted(maps.Keys(bank.Assets)) {
		env, err := payment.AdmissionMessage(
			// The country is the BANK's own, off the row, so a re-drive applies to
			// the register the interrupted admission applied to rather than to
			// whichever one this caller named. See payment.Bank.Issuer.
			payment.AdmissionRequest{
				Name: bank.Name, BIC: bank.BIC, Country: bank.Issuer.Country, Asset: asset, Ref: ref,
			},
			m.cfg.CentralBankBIC,
			payment.MessageContext{From: bank.BIC, To: to, MsgID: m.nextMsgID(bank.BIC), Now: m.now()},
		)
		if err != nil {
			return bank, fmt.Errorf("mesh: %s could not compose its application in %s: %w", bank.BIC, asset, err)
		}
		if err := m.send(bank.BIC, to, env); err != nil {
			return bank, fmt.Errorf("mesh: %s was founded and could not apply in %s: %w", bank.BIC, asset, err)
		}
	}
	return bank, nil
}

// claimAddress takes a BIC for an admission that is about to run, or reports
// that the bank at that address is re-driving an interrupted one.
//
// Three answers and two locks, held in that order and never the other. A free
// address is RESERVED and redriving is false. An address a bank of this mesh
// already answers to comes back redriving, no reservation is made, and the
// caller founds nothing — the roster has already been asked and holds no entry
// for it, so this is a re-drive. Anything else is ErrAddressTaken.
//
// "Anything else" is worth spelling out because two of its three cases have
// nothing to do with banks: an address one of the two INSTITUTIONS answers to,
// and an address another admission has reserved and not yet registered. Both are
// refusals about connectivity rather than about membership, which is the whole
// of what the mesh's authority over an address amounts to.
//
// Whether the incumbent is one of OURS is the mesh's question and not the
// transport's — an actor is an address and a handler, and which of them is a
// member bank is this package's own bookkeeping — so m.mu is held across the
// claim and the bank index is read inside it. See wire.Bus.Claim.
//
// The second refusal is ErrAdmissionInFlight, which wraps ErrAddressTaken so it
// is still a taken address to anything that only asks that question. It is
// separate because it is the one refusal here where NOBODY answers to the
// address yet: the bank claiming it is the same bank, a moment early.
//
// It reads no store, which is why it can hold m.mu — see joinRoster on why that
// combination is the one to avoid. A re-drive's bank row is read by the caller,
// afterwards and unlocked, and the interval that opens is an operator racing
// their own retry: two re-drives of one address would each send a set of
// requests. The domain is what serialises that, not this lock — see
// payment.AdmitMemberTx, which draws an id before it decides.
func (m *Mesh) claimAddress(bic iso20022.BIC) (redriving bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ours := m.banks[bic]
	switch claim, err := m.bus.Claim(bic, ours); {
	case err != nil:
		return false, err
	case claim == wire.Redriving:
		return true, nil
	case claim == wire.HeldByAnotherClaim:
		return false, fmt.Errorf("%w: %s", ErrAdmissionInFlight, bic)
	case claim == wire.HeldByAnotherActor:
		return false, fmt.Errorf("%w: %s", ErrAddressTaken, bic)
	}
	return false, nil
}

// nextProcessID mints the identifier one admission travels under: acmt
// Refs/PrcId, echoed by every message of that admission.
//
// It is nextMsgID's sibling and shares its counter, so a process id and a
// message id can never collide and both are unique under the frozen clock these
// tests run on. What it is NOT is a message id: several messages carry this one
// value, which is the whole reason the element exists — an acmt.010 carries no
// back-reference to the request that caused it, so this is the only thing that
// says two messages are one admission.
func (m *Mesh) nextProcessID(from iso20022.BIC) string {
	return fmt.Sprintf("%s-adm-%d", from, m.msgSeq.Add(1))
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
