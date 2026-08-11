package mesh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"sync/atomic"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/mesh/wire"
	"github.com/raphi011/cbs/payment"
)

// ErrUnknownBIC is a send to a BIC no actor answers to. It becomes RC01 on the
// wire — BankIdentifierIncorrect — which is exactly what it is.
//
// The refusal is the transport's; this is the name the rest of the system knows
// it by. See wire.ErrUnknownBIC.
var ErrUnknownBIC = wire.ErrUnknownBIC

// ErrAddressTaken is a claim on a BIC some other actor on this bus already
// answers to.
//
// It is a statement about CONNECTIVITY and not about membership: it says an
// inbox is spoken for, not that a scheme has admitted anybody. The clearing
// house's roster answers the second question, one institution over and keyed on
// the admission rather than on the address — see payment.ErrBICAlreadyAdmitted.
//
// What reaches it is AddBank, giving a provisioned bank its actor. Two banks
// listed on one address in a deployment is the case it exists for, and the
// remedy is to give each an address of its own. The refusal itself is
// wire.ErrAddressTaken.
var ErrAddressTaken = wire.ErrAddressTaken

// Config names the two institutions. Member banks are discovered from the
// participant roster; the central bank and the clearing house have no store
// row, so their identities are configured.
//
// Without these two the header's Fr and To would be meaningful for banks and
// invented for the institutions, which is the sort of half-modelled thing this
// repository tries not to ship.
type Config struct {
	CentralBankBIC   iso20022.BIC
	ClearingHouseBIC iso20022.BIC

	// Observe, if non-nil, is called on the receiving actor's goroutine with
	// every message it is about to handle: who it is for, who sent it, and the
	// bytes. It is the transport's one observation point, and it is here rather
	// than unexported because a layer ABOVE the mesh needs it for the same reason
	// this package's own tests do.
	//
	// What it buys is the only honest way to observe a mesh MID-CONVERSATION.
	// Everything a message does leaves a trace afterwards, and Drain is how a
	// test waits for that; nothing else can say "the payment has not been carried
	// further YET", because reading the store races the actors. api's
	// TestSubmitAnswers202AndThePaymentIsNotYetAccepted is that assertion, and it
	// held it by scheduling luck until this existed — under contention it failed
	// roughly once in a hundred runs, with a message byte-identical to the one a
	// real regression produces.
	//
	// It may BLOCK, and blocking is the point. An Observe that waits holds the
	// receiving actor before its handler runs, so the conversation provably
	// cannot advance while the caller looks. What it must not do is wait for
	// anything that itself waits for the mesh: an Observe blocking one actor
	// while the goroutine that would release it is inside Drain deadlocks, and no
	// deadline here would make that correct.
	Observe func(to, from iso20022.BIC, raw []byte)
}

// validate refuses a configuration the mesh could not route by.
//
// The two identities must be present, structurally valid, and DIFFERENT. The
// last is the one worth stating: one BIC in both fields is not a mesh with two
// institutions that happen to share an address, it is a routing table with one
// entry, and the second actor would be the first one's duplicate — silently, at
// startup, in a system where the whole point of the central bank being separate
// from the clearing house is that clearing and settlement are different jobs.
func (c Config) validate() error {
	if err := c.CentralBankBIC.Validate(); err != nil {
		return fmt.Errorf("mesh: central bank BIC: %w", err)
	}
	if err := c.ClearingHouseBIC.Validate(); err != nil {
		return fmt.Errorf("mesh: clearing house BIC: %w", err)
	}
	if c.CentralBankBIC == c.ClearingHouseBIC {
		return fmt.Errorf("mesh: %s is configured as both the central bank and the clearing house", c.CentralBankBIC)
	}
	return nil
}

// Mesh is the interbank network: N member banks, a clearing house and a central
// bank, each an actor on a wire.Bus, and the flows they carry between them.
//
// What it adds to the transport is everything the transport declines to know:
// which institution an actor IS, what a message MEANS, and which halves of a
// flow run on a caller's goroutine rather than in an inbox. See the package doc
// for the flows, and wire's for what delivery here is deliberately not.
type Mesh struct {
	// bus is the transport: inboxes, goroutines, the in-flight counter, the
	// dead letters, and the actor table. Every message this package sends goes
	// through it, and it knows nothing about what any of them say.
	bus *wire.Bus

	// nets mints one payment.Network per institution: each actor below is built
	// over the network of the institution it IS, so a bank handler's ops are a
	// bank's view and the clearing house's are the clearing house's.
	//
	// Nothing here holds more than one institution's view at a time. This is a
	// factory, not a collection: it opens nothing and remembers nothing, and
	// each Network it mints goes straight into the one actor that is that
	// institution.
	nets *payment.Networks

	// clearingHouse is the network the mesh's OWN reads go through — the ones
	// made on the caller's goroutine, before an actor has been chosen or when
	// none exists yet: is this address a member (Submit), which banks does the
	// roster name (joinRoster), which bank submits this payment (Return), and
	// which scheme is this.
	//
	// The clearing house is the right institution for every one of them, because
	// each is a question about its own rows.
	clearingHouse *payment.Network

	cfg Config
	log *slog.Logger

	// msgSeq numbers the messages this mesh emits. See nextMsgID.
	msgSeq atomic.Uint64

	// csm is the clearing house's behaviour, kept beside its actor because one
	// of the things a clearing house does does not arrive in an inbox. A cut-off
	// comes in from outside the mesh — an operator, or api's POST /cycles/{cid}/close —
	// the same way a customer's payment instruction does, so Mesh.CloseCycle
	// needs the handler itself and not the queue in front of it.
	//
	// It is nil on a mesh built over no network, which is the same precondition
	// Submit has and is documented there.
	//
	// Written once, in New, before any goroutine exists that could read it.
	csm *csm

	// mu guards banks, and nothing else: the actor table, the address
	// reservations and the in-flight accounting are the bus's.
	//
	// It is taken AROUND a bus call in the two places that must be atomic with a
	// registration — AddBank and claimAddress — which is the only nesting of the
	// two locks in this system. The order is always this one, and it is safe
	// because the bus never calls back into the mesh: a handler holds neither.
	mu sync.Mutex
	// banks is the member banks by ADDRESS, which is how Submit finds the actor
	// that plays a payer's own bank.
	banks map[iso20022.BIC]*bank
}

// New builds a mesh over a payment network, with an actor for each of the two
// configured institutions and no goroutines running yet.
//
// The member banks are NOT created here: they come from the CLEARING HOUSE's
// roster, which is a store read, and a constructor that did I/O could not be
// called before the store was ready. Start reads the roster, which is why
// cmd/server provisions first and starts the mesh second; AddBank is how a
// deployment gives one bank an actor without re-reading the whole roster.
//
// nets may be nil. A mesh with no networks has no roster and therefore no member
// banks, which is what the tests about routing and registration use: they need
// no store at all.
func New(nets *payment.Networks, cfg Config, log *slog.Logger) (*Mesh, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}

	m := &Mesh{
		bus:   wire.New(log, cfg.Observe),
		nets:  nets,
		cfg:   cfg,
		log:   log,
		banks: make(map[iso20022.BIC]*bank),
	}
	// Both institutions get their behaviour here, because neither has a store
	// row to wait for: they ARE the configuration. A mesh with no network keeps
	// the refusing placeholder for both — clearing and settlement are things you
	// do to payments and cycles, and there are none.
	//
	// The clearing house is kept as a field as well as an actor, because reaching
	// a cut-off is not a message: it comes in from outside the mesh, exactly as a
	// customer's instruction does, and Mesh.CloseCycle runs it on the caller's
	// goroutine. See Mesh.csm.
	clearing := unhandled("clearing house")
	settlement := unhandled("central bank")
	if nets != nil {
		// Each institution over its own network, which is what makes the three
		// interfaces in ops.go narrow by BOOK as well as by method: the central
		// bank's is the only one holding the central bank's book of accounts,
		// and a csmOps method reached through a bank's network would be refused
		// by the domain rather than merely unwritable here.
		m.clearingHouse = nets.ClearingHouse()
		m.csm = &csm{m: m, ops: m.clearingHouse, bic: cfg.ClearingHouseBIC, held: map[payment.PaymentID]heldReturn{}}
		clearing = m.csm.handle
		settlement = (&centralBank{m: m, ops: nets.CentralBank(), bic: cfg.CentralBankBIC}).handle
	}
	if err := m.bus.AddActor(cfg.ClearingHouseBIC, "clearing house", clearing); err != nil {
		return nil, err
	}
	if err := m.bus.AddActor(cfg.CentralBankBIC, "central bank", settlement); err != nil {
		return nil, err
	}
	return m, nil
}

// unhandled is the handler an actor has until some task gives it one.
//
// It refuses rather than discards. A message arriving at an actor that has no
// behaviour yet is a bug in whoever sent it; swallowing it would make the
// missing half look like a working one. Drain returns it, which is exactly the
// dead-letter path this package exists to keep visible.
//
// It is reached on ONE kind of mesh: one built over no network, which is the
// transport's own tests. Both institutions keep it there, because clearing and
// settlement are things you do to payments and cycles and a mesh with no store
// has neither.
func unhandled(name string) wire.Handler {
	return func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		return fmt.Errorf("mesh: %s has no handler for the %d bytes %s sent it", name, len(raw), from)
	}
}

// Start reads the participant roster, gives every member bank an actor, and
// launches one goroutine per actor.
//
// The context is the mesh's LIFETIME, not one call's: every handler runs with
// it, and Stop cancels it. Passing a request-scoped context here would cancel
// the whole mesh when that request finished.
//
// The roster read sits BETWEEN the transport's two questions — may this bus
// start, and start it — because it is store I/O and nothing may be blocked on
// the mesh while it runs. Two concurrent Starts can therefore both read the
// roster, and only one of them starts: wire.Bus.Start re-checks under its own
// lock, which is what makes the answer one answer.
func (m *Mesh) Start(ctx context.Context) error {
	if err := m.bus.Startable(); err != nil {
		return err
	}
	if err := m.joinRoster(ctx); err != nil {
		return err
	}
	return m.bus.Start(ctx)
}

// Stop closes every inbox, waits for the actors to finish what they are already
// doing, and returns the dead letters they produced, joined. See wire.Bus.Stop,
// which is all of it: every caller must Drain first, and a Stop that times out
// leaves the mesh running and may be retried.
func (m *Mesh) Stop(ctx context.Context) error { return m.bus.Stop(ctx) }

// Drain blocks until no message is in flight, then returns any errors handlers
// could not answer, joined. See wire.Bus.Drain, including what it does not
// promise and why no test in this repository waits for a duration instead.
func (m *Mesh) Drain(ctx context.Context) error { return m.bus.Drain(ctx) }

// joinRoster gives every bank the CLEARING HOUSE routes to an actor, in one
// batch.
//
// # It reads the roster and nothing else
//
// The roster is what says who is a member, so a bank founded and not admitted
// gets no actor after a restart and this transport cannot reach it. That is not
// what stops it being paid — payment.ErrBankNotAdmitted is, at Mesh.Submit and
// again at the clearing house — and it is not true in the process that admitted
// it, where a deployment can call AddBank for a bank the roster does not yet
// name.
//
// No bank row is read: the id IS the address, and the clearing house's schema
// has no banks table anyway. So the actor is named by its address rather than by
// the bank's legal NAME, which a roster entry does not carry (see
// payment.RosterEntry). The name lives on the bank's own row, which is where an
// operator asking about a bank reads it.
//
// The read happens OUTSIDE m.mu, because it is store I/O and nothing else may be
// blocked on the mesh while it runs. The whole roster is then registered in one
// batch, so a bank the mesh cannot route to leaves the mesh as it found it — see
// wire.Bus.AddActors.
//
// # It MERGES into the bank index rather than assigning it
//
// Both callers run against an empty index — Start on a new mesh, JoinRoster on
// one ForgetBanks has just emptied — so on any sequential path the two are the
// same. What differs is a bank admitted CONCURRENTLY: it commits its actor and
// its index entry between the read above and this write, and an assignment would
// drop that entry while leaving its actor running — an actor no index names,
// which nothing can reach and nothing can remove.
//
// The same window exists here and cannot be closed: the registration and the
// copy below are two critical sections, so between them an actor exists that the
// index does not name. AddBank closes it for one bank at a time; a batch that
// registered and indexed under one lock would hold m.mu across every bank in the
// roster.
//
// Nothing else is protected by the merge. A reset racing an AddBank is still a
// mess and can still refuse it or fail this call; what the merge removes is the
// SILENT outcome — a bank that answers every read and carries no
// payment, with nothing anywhere saying so.
func (m *Mesh) joinRoster(ctx context.Context) error {
	if m.nets == nil {
		return nil
	}
	entries, err := m.clearingHouse.ListRosterEntries(ctx)
	if err != nil {
		return fmt.Errorf("mesh: reading the clearing house's roster: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}
	specs := make([]wire.ActorSpec, 0, len(entries))
	banks := make(map[iso20022.BIC]*bank, len(entries))
	for _, e := range entries {
		// Each member's own database, opened here. This is the one place in the
		// mesh where a roster read turns into N database opens, and it is the
		// shape the split gives startup: the roster says who is a member and the
		// set of databases is what those members ARE.
		ops, err := m.nets.Bank(ctx, payment.ParticipantID(e.BIC))
		if err != nil {
			return fmt.Errorf("mesh: opening member %s's store: %w", e.BIC, err)
		}
		b := &bank{m: m, ops: ops, bic: e.BIC}
		banks[e.BIC] = b
		specs = append(specs, wire.ActorSpec{BIC: e.BIC, Name: string(e.BIC), Handle: b.handle})
	}
	if err := m.bus.AddActors(specs...); err != nil {
		return err
	}
	// After the registration, so that a roster the mesh refused to route leaves
	// the bank index as it found it too.
	m.mu.Lock()
	maps.Copy(m.banks, banks)
	m.mu.Unlock()
	return nil
}

// JoinRoster re-reads the participant roster and gives every bank in it an
// actor. It is what Start does, on a mesh that is already running.
//
// It exists for ONE caller and one situation: a store that has been emptied and
// rebuilt underneath a live mesh — api.Server.Reset. The roster the mesh read at
// Start is gone, and the banks that replaced it have never been registered, so
// without this the actor table describes a network that no longer exists. See
// ForgetBanks, which is the half that runs first.
//
// It is NOT an incremental sync: it registers the whole roster and expects to
// find no member banks already registered, which is exactly the state
// ForgetBanks leaves. Calling it on a mesh that still holds one of the roster's
// addresses is refused, all-or-none, and leaves the mesh as it found it — see
// TestJoinRosterRefusesTheWholeRosterWhenOneAddressIsTaken.
func (m *Mesh) JoinRoster(ctx context.Context) error { return m.joinRoster(ctx) }

// CentralBankBIC is the address this mesh routes settlement instructions to.
//
// It is configured rather than discovered — the settlement agent has no roster
// row, because it is not a member of the scheme it settles — so a caller that
// needs to name it has nowhere else to read it from. The seed is the caller: it
// plays every institution in one process and sends no messages, and a settlement
// instruction names the agent at one end of every leg. See
// payment.SettlementLegsOf.
//
// The clearing house's has no accessor because nothing outside this package
// needs to name it: every act addressed to it is one of the exported methods
// above, which fill the address in themselves.
func (m *Mesh) CentralBankBIC() iso20022.BIC { return m.cfg.CentralBankBIC }

// ForgetBanks removes every BANK's actor — every one the bus holds that is not
// one of the two institutions, including a bank AddBank gave one to that the
// roster does not name: closes its inbox, waits for its goroutine to return, and
// drops it from the routing table and the bank index.
//
// # Why a mesh needs this at all
//
// Because the store can be replaced underneath it. api.Server.Reset truncates
// every table and rebuilds the sample dataset, and the mesh is not restarted.
// Without this the actor table keeps describing the roster that was there
// before: a BIC the operator can never admit again, and an index entry pointing
// at a goroutine whose bank has been deleted. Reachable over HTTP by admitting a
// bank, resetting, and admitting the same BIC again.
//
// It is called immediately before the truncate, so that between the two the mesh
// routes to no member bank at all rather than to the wrong one. A submission in
// that window is refused — "no bank actor" — which is the truth.
//
// # It forgets by ACTOR TABLE, not by bank index
//
// Walking the index is wrong in a way a reset shows: an actor whose index entry
// is missing would be unforgettable, so its address would be taken for the life
// of the process and every later reset would fail on it. That state is reachable
// because joinRoster writes the actors and the index entries under separate
// locks. Reading the actor table makes "forgotten" total: afterwards the bus
// holds exactly the two institutions, whatever the index said. They are excluded
// by identity rather than by kind, because that is the durable distinction —
// they are configuration, they have no participant row, and a reset does not
// touch them.
//
// The index is pruned AFTERWARDS and against the bus, not against the list of
// what was closed, for that same reason: what belongs in the index is exactly
// what still has an actor.
//
// A timeout leaves the actors joinable and the index untouched; see
// wire.Bus.Forget, and TestForgetBanksThatTimesOutLeavesTheActorsForStopToJoin.
func (m *Mesh) ForgetBanks(ctx context.Context) error {
	if err := m.bus.Forget(ctx, m.cfg.CentralBankBIC, m.cfg.ClearingHouseBIC); err != nil {
		return err
	}
	m.mu.Lock()
	for bic := range m.banks {
		if !m.bus.Has(bic) {
			delete(m.banks, bic)
		}
	}
	m.mu.Unlock()
	return nil
}

// AddBank gives one member bank an actor and an index entry, in one critical
// section.
//
// It is what makes a bank REACHABLE, and reachability is the whole of what it
// says. A bank with no actor is not a slow bank: Mesh.Submit refuses its
// customers' instructions outright because there is nobody to hand them to, and
// every pacs.008 addressed to it comes back RC01 from the clearing house because
// nothing in the routing table answers to its BIC. Two banks on one address are
// not two banks in a mesh either, they are one routing-table entry and one
// goroutine reading an inbox nobody can address, so a taken BIC is refused with
// ErrAddressTaken.
//
// It runs after a bank has been provisioned, because there is nothing to give an
// actor to before that: a deployment provisions its banks and then gives each of
// them one, and a mesh already running takes a later bank through this rather
// than through another roster read.
//
// The registration, the reservation it consumes and the index entry are ONE
// critical section, which is why m.mu is held ACROSS the bus call rather than
// after it: a gap before the index write leaves an actor no index names —
// reachable by a message and unable to submit. wire.Register is the other half,
// and its doc has why the reservation goes with them. joinRoster still has the
// first of those windows and says so; it cannot use this, because a batch
// registered and indexed under one lock would hold m.mu across a whole roster.
//
// It takes a context because it opens the bank's database — every act this
// actor will perform runs against it — and the open happens BEFORE the lock, for
// the reason every other store read in this file is outside one.
func (m *Mesh) AddBank(ctx context.Context, p *payment.Bank) error {
	if m.nets == nil {
		return errors.New("mesh: no network, so there are no member banks to give actors to")
	}
	if err := p.BIC.Validate(); err != nil {
		return fmt.Errorf("mesh: actor %q: %w", p.Name, err)
	}
	ops, err := m.nets.Bank(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("mesh: opening %s's store: %w", p.BIC, err)
	}
	b := &bank{m: m, ops: ops, bic: p.BIC}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.bus.Register(wire.ActorSpec{BIC: p.BIC, Name: p.Name, Handle: b.handle}); err != nil {
		return err
	}
	m.banks[p.BIC] = b
	return nil
}

// bankActor is the actor playing one member bank, if this mesh has one.
func (m *Mesh) bankActor(bic iso20022.BIC) (*bank, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.banks[bic]
	return b, ok
}
