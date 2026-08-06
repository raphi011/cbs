package mesh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/raphi011/cbs/iso20022"
	"github.com/raphi011/cbs/ledger"
	"github.com/raphi011/cbs/payment"
)

// ErrUnknownBIC is a send to a BIC no actor answers to. It becomes RC01 on the
// wire — BankIdentifierIncorrect — which is exactly what it is.
var ErrUnknownBIC = errors.New("mesh: no actor for this BIC")

// ErrAddressTaken is a claim on a BIC some other actor already answers to, or
// that another admission has already reserved. See addActors for why it is
// refused rather than absorbed, and Mesh.reserved for the second half.
//
// It is a sentinel and not just a message because the layer above has a REMEDY
// for it and none for the mesh's other refusals: a clashing address is fixed by
// admitting the bank on an address of its own, and an operator told that during
// a SHUTDOWN — the other way Admit can fail — would be sent to retry something
// that will fail the same way whatever address they choose. Telling the two
// apart is what api's handleAddParticipant uses it for.
//
// The consequence of following that advice used to be worse than a wasted
// retry: every attempt left a participant row behind, so an operator working
// through addresses accumulated orphans. Mesh.Admit claims the address before
// anything is written, so a refusal on either branch now costs nothing at all.
// The sentinel survives the fix because the ADVICE still differs, which is the
// smaller of the two reasons it was introduced for.
//
// It is a statement about CONNECTIVITY and not about membership. The clearing
// house's roster answers the second question, one institution over and keyed on
// the admission rather than on the address; see payment.ErrBICAlreadyAdmitted
// and csm.relayAdmission.
var ErrAddressTaken = errors.New("mesh: another actor already answers to this BIC")

// ErrAdmissionInFlight is the one case of ErrAddressTaken where nobody answers
// to the address yet: a second admission on a BIC whose first is still between
// claiming it and registering its actor.
//
// It WRAPS ErrAddressTaken, so every caller that asks "was this address refused"
// keeps its answer, and a caller that has different advice for the two can tell
// them apart. api's handleAddParticipant is that caller, and the difference is
// the whole reason this exists: the advice for a taken address is to admit the
// bank somewhere else, and the advice here is to wait — the address is not
// another institution's, it is this bank's own, a moment early.
//
// It is a value ErrAddressTaken does not cover on its own, and it is stated as
// its own sentinel rather than matched on the message so the distinction cannot
// be lost to a rewording. See Mesh.claimAddress, which is where the reservation
// is made and where this is returned from.
//
// # It is a TYPE, and not fmt.Errorf("%w: …", ErrAddressTaken)
//
// That is the obvious spelling and it was the first one, and it puts the parent's
// text into the child's message: the operator was told "another actor already
// answers to this BIC" — the exact false statement this sentinel exists to
// replace — because Error() on a wrapped error is the wrapper's text followed by
// the wrapped one's. Wrapping is about the ERROR CHAIN and inheriting the
// sentence is a side effect of the standard formatting, so the sentence is
// declared here and the chain is left to Unwrap.
var ErrAdmissionInFlight error = admissionInFlight{}

// admissionInFlight is ErrAdmissionInFlight's type: its own sentence, and
// ErrAddressTaken underneath it.
//
// An empty struct, so it is comparable and errors.Is finds it by equality the
// way it finds any sentinel. Unwrap is what keeps every caller that only asks
// "was the address refused" answered — mesh's own tests, and api's second
// branch, which is the one this must not fall through to.
type admissionInFlight struct{}

func (admissionInFlight) Error() string { return "mesh: an admission on this BIC is already under way" }
func (admissionInFlight) Unwrap() error { return ErrAddressTaken }

// ErrOnUsPayment is a submission whose payer and payee bank at the SAME
// institution.
//
// It is a statement about the ROUTE and not about the payment. Two customers of
// one bank paying each other is an ordinary thing to want; what it is not is a
// CLEARING payment. Nothing leaves the bank, so there is no interbank obligation
// for a clearing house to net, no reserves for a settlement agent to move, and
// no camt.053 that could tell a bank about a book it already holds. A real bank
// recognises the beneficiary as its own and books the transfer between two of
// its own deposit accounts; it never reaches a scheme at all.
//
// Submitted to clearing anyway, it produced three separate wrong answers, each
// in a different institution — a cycle that settled nothing and stranded at
// Cleared, a reserve mirror moved by an amount the central bank's own record did
// not move, and a returning bank refusing its own customer's unconditional
// refund because it was the returner on both legs. See Mesh.Submit, where it is
// refused, and payment.PostReturnLegTx, which states the return's rule so that
// it does not depend on this refusal holding.
//
// A sentinel and not just a message because the layer above has a remedy for
// it: api answers 422 and the caller asks its bank for a book transfer instead.
// Building that transfer is a task of its own and this system does not have it
// yet, which is what the refusal honestly says.
var ErrOnUsPayment = errors.New("mesh: both parties bank at the same institution, which is a book transfer and not a clearing payment")

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
	//
	// It is read only by actor goroutines and written only by New, before any of
	// them exists, which is what makes it safe without a lock.
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

// handler is what an actor does with a message: bytes in, an error out if it
// could not be dealt with at all.
//
// The signature carries the sender separately from the bytes because an
// unparseable message hides its own header — see item.
type handler func(ctx context.Context, from iso20022.BIC, raw []byte) error

// actor is one institution: an inbox, a goroutine reading it, and what it does
// with what it reads.
//
// One goroutine per actor, and only that goroutine ever runs the actor's
// handler. That is what makes a bank's own state safe to touch inside a handler
// without a lock of its own, and it is why the queue rather than the handler is
// the concurrent object here.
type actor struct {
	bic    iso20022.BIC
	name   string
	q      *queue
	handle handler
	// done is closed when the actor's goroutine has returned. Stop waits on
	// these, which is what makes "stopped" mean "no handler is still running"
	// rather than "no more messages will be picked up".
	done chan struct{}
}

// Mesh is the transport: N member banks, a clearing house and a central bank,
// each an actor with an unbounded inbox, and one send function that every
// message in this system passes through.
//
// See the package doc for what this is deliberately not.
type Mesh struct {
	net *payment.Network
	cfg Config
	log *slog.Logger

	// ctx is the lifetime of the mesh, not of any one call: every handler is
	// invoked with it, and Stop cancels it once the actors have joined. It is
	// written once, in Start, before any goroutine exists that could read it.
	ctx    context.Context
	cancel context.CancelFunc

	// tap, if non-nil, sees every message an actor is about to handle: who it
	// is for, who sent it, and the bytes. It is Config.Observe once New has taken
	// it, and this package's own tests set it directly.
	//
	// It earns its place because some of what this mesh does leaves no other
	// trace. An FF01 names no payment — it cannot, the file that provoked it did
	// not parse — so a test that could not see the message could not tell an
	// answered malformed envelope from a swallowed one. Nor could one tell a
	// submission that sent nothing from a submission whose message went somewhere
	// else. Nor, from outside this package, could one hold a conversation still
	// long enough to read what a caller was told at the moment it was told.
	//
	// It is written before Start and read only by actor goroutines, which is what
	// makes it safe without a lock: until Start runs, no goroutine exists that
	// could read it, and after Start nothing writes it.
	tap func(to, from iso20022.BIC, raw []byte)

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

	mu     sync.Mutex
	actors map[iso20022.BIC]*actor
	// reserved is the addresses an admission has CLAIMED and not yet given an
	// actor to, and it is the whole of the orphan defect's fix.
	//
	// A BIC is the only thing about an admission that can clash, and it used to
	// be checked last: the row was written and the address asked for afterwards,
	// so a refusal left a bank in the roster that could neither pay nor be paid.
	// Reversed, the claim has to be made BEFORE the bank exists — and a bank that
	// does not exist yet cannot have an actor, because an actor's handler is
	// bound to the bank's own identity. So the claim is a set membership rather
	// than an entry in the actor table.
	//
	// Everything that hands out an address consults it: addActors refuses a BIC
	// reserved by an admission in flight, and Admit refuses one twice over. It is
	// cleared in the same critical section that inserts the actor, so there is no
	// instant at which the address is free between the two — a gap there would
	// let a second Admit take the address out from under a bank that has already
	// committed its row, which is the defect this exists to remove, reintroduced
	// one lock apart.
	reserved map[iso20022.BIC]bool
	// banks is the member banks by participant, which is how Submit finds the
	// actor that plays a payer's own bank. Keyed by ParticipantID and not by BIC
	// because that is what an instruction names: a request says which
	// participant holds the payer's account, and turning that into a BIC to look
	// up an actor would be a store read to answer a question the roster already
	// answered at startup.
	banks    map[payment.ParticipantID]*bank
	inFlight int
	// quiet is closed when inFlight reaches zero and replaced when it leaves
	// zero. A channel rather than a sync.Cond because Drain must also wake on
	// a cancelled context, and Cond cannot select.
	quiet chan struct{}
	dead  []error
	// started, stopping and stopped are three states rather than two because
	// the middle one is reachable and is not the same as either neighbour: Stop
	// has closed the inboxes and taken its snapshot but has not joined yet, and
	// a Stop that times out leaves the mesh sitting there. stopping is what
	// addActors refuses on — waiting for stopped would let an actor be
	// registered into a shutdown that will never close or join it.
	started  bool
	stopping bool
	stopped  bool
	// busy names the actors currently working on a message and WHAT they are
	// doing with it, so a Drain that times out can say which one is stuck
	// instead of reporting a bare deadline.
	//
	// The phase matters because there are two of them and only one is the
	// actor's own code. An actor parked in Config.Observe has not entered its
	// handler at all — it is being held by whoever installed the hook — and
	// reporting that as "in a handler" would send a reader looking for a bug in
	// the wrong half of the system, at the exact moment they are debugging the
	// hook. See dispatch.
	busy map[iso20022.BIC]string
}

// New builds a mesh over a payment network, with an actor for each of the two
// configured institutions and no goroutines running yet.
//
// The member banks are NOT created here: they come from the CLEARING HOUSE's
// roster, which is a store read, and a constructor that did I/O could not be
// called before the store was ready. Start reads the roster, which is why
// cmd/server seeds first and starts the mesh second; Admit covers the banks that
// join after that, which is every bank a human admits over HTTP.
//
// Admit claims the ADDRESS before it writes anything and registers the actor
// once the bank's own unit of work has committed. The two are not the same step
// and this sentence used to run them together: the claim is what makes a clash
// cost nothing, and the actor is what makes the bank reachable. See Mesh.Admit
// and Mesh.reserved.
//
// net may be nil. A mesh with no network has no roster and therefore no member
// banks; that is what the transport's own tests use, and it is the reason this
// task needs no store at all.
func New(net *payment.Network, cfg Config, log *slog.Logger) (*Mesh, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	// A mesh with nothing in flight starts quiet, so quiet starts closed. The
	// alternative — an open channel replaced on the first leave — would make
	// Drain block on a mesh that had never sent anything.
	quiet := make(chan struct{})
	close(quiet)

	m := &Mesh{
		net:      net,
		cfg:      cfg,
		log:      log,
		tap:      cfg.Observe,
		actors:   make(map[iso20022.BIC]*actor),
		reserved: make(map[iso20022.BIC]bool),
		banks:    make(map[payment.ParticipantID]*bank),
		busy:     make(map[iso20022.BIC]string),
		quiet:    quiet,
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
	if net != nil {
		m.csm = &csm{m: m, ops: net, bic: cfg.ClearingHouseBIC, held: map[payment.PaymentID]heldReturn{}}
		clearing = m.csm.handle
		settlement = (&centralBank{m: m, ops: net, bic: cfg.CentralBankBIC}).handle
	}
	if err := m.addActor(cfg.ClearingHouseBIC, "clearing house", clearing); err != nil {
		return nil, err
	}
	if err := m.addActor(cfg.CentralBankBIC, "central bank", settlement); err != nil {
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
// After Task 12 it is reached on ONE kind of mesh: one built over no network,
// which is the transport's own tests. Both institutions keep it there, because
// clearing and settlement are things you do to payments and cycles and a mesh
// with no store has neither. On a mesh with a network, every actor — both
// institutions and every member bank — now has a real handler.
func unhandled(name string) handler {
	return func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		return fmt.Errorf("mesh: %s has no handler for the %d bytes %s sent it", name, len(raw), from)
	}
}

// actorSpec is an actor before it exists: who it is and what it does.
type actorSpec struct {
	bic    iso20022.BIC
	name   string
	handle handler
}

// addActor registers one actor. See addActors, which does the work.
func (m *Mesh) addActor(bic iso20022.BIC, name string, h handler) error {
	return m.addActors(actorSpec{bic: bic, name: name, handle: h})
}

// addActors registers a batch of actors under their BICs — all of them or none
// — and, if the mesh is already running, starts their goroutines at once.
//
// Registering into a running mesh is the normal case, not a convenience: a bank
// admitted over HTTP joins a mesh whose other actors are already reading their
// inboxes, and it has to be reachable before it can send its own application —
// which is the first message of the flow that admits it. See
// TestAnActorAddedAfterStartReceives and Mesh.Admit.
//
// All or none, because the batch is a roster. A registration that failed
// halfway would leave the mesh holding some banks and not others, unstarted,
// and a caller that fixed the roster and retried would then collide with the
// actors the failed attempt had itself created. See
// TestStartRefusesTwoParticipantsWithOneBIC, which asserts the mesh is
// unchanged after the refusal.
//
// Two actors under one BIC is refused, whether the clash is with an actor
// already registered or between two members of the same batch. The map would
// keep the second and drop the first, and the dropped one's goroutine would
// read an inbox nothing could ever address.
//
// A mesh that is stopping or stopped refuses. The obvious case is the stopped
// one: its actors' goroutines have returned, so a new actor would have an open
// inbox and nobody reading it — a send to it would report success, count a
// message in flight, and hang the next Drain out to its deadline. A black hole
// that answers "sent" is worse than a refusal.
//
// The case that actually bit is the one in between. Stop takes a snapshot of
// the actors and closes their inboxes, then joins them; an actor registered
// after that snapshot is in neither list, so its inbox is never closed and its
// goroutine is never joined — a permanent leak, plus the same black hole. It is
// not hypothetical: a bank admitted over HTTP registers into a running mesh (see
// Mesh.Admit), so that is the normal path, and a shutdown racing an admission is
// an ordinary thing to get wrong. Refusing from the moment Stop begins, and
// setting that flag under the same lock Stop takes its snapshot under, closes
// the window rather than narrowing it.
// A BIC an admission has RESERVED is refused too, and for the same reason a
// registered one is: an address claimed by an admission whose bank is being
// written is an address a second registration cannot have. See Mesh.reserved.
func (m *Mesh) addActors(specs ...actorSpec) error {
	for _, s := range specs {
		if err := s.bic.Validate(); err != nil {
			return fmt.Errorf("mesh: actor %q: %w", s.name, err)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	return m.addActorsLocked(specs...)
}

// addActorsLocked is addActors with the lock already held and the BICs already
// validated. It is split out for admitBank, which has to clear an address
// reservation and register its actor in ONE critical section — see Mesh.reserved
// for what a gap between the two would cost.
func (m *Mesh) addActorsLocked(specs ...actorSpec) error {
	if m.stopping || m.stopped {
		return fmt.Errorf("mesh: stopping; %s would have no goroutine to read its inbox", specs[0].bic)
	}
	batch := make(map[iso20022.BIC]bool, len(specs))
	for _, s := range specs {
		if _, dup := m.actors[s.bic]; dup || batch[s.bic] || m.reserved[s.bic] {
			return fmt.Errorf("%w: two actors for %s (%s)", ErrAddressTaken, s.bic, s.name)
		}
		batch[s.bic] = true
	}
	for _, s := range specs {
		a := &actor{bic: s.bic, name: s.name, q: newQueue(), handle: s.handle, done: make(chan struct{})}
		m.actors[s.bic] = a
		if m.started {
			go m.run(m.ctx, a)
		}
	}
	return nil
}

// Start reads the participant roster, gives every member bank an actor, and
// launches one goroutine per actor.
//
// The context is the mesh's LIFETIME, not one call's: every handler runs with
// it, and Stop cancels it. Passing a request-scoped context here would cancel
// the whole mesh when that request finished.
//
// A mesh is started once. Restarting one that has been stopped is refused
// rather than half-supported: the actors' done channels have already been
// closed, so a second run of the same goroutines could not be waited for, and a
// Stop that cannot wait is the failure this package's Stop exists to avoid.
func (m *Mesh) Start(ctx context.Context) error {
	m.mu.Lock()
	switch {
	case m.started:
		m.mu.Unlock()
		return errors.New("mesh: already started")
	case m.stopped:
		m.mu.Unlock()
		return errors.New("mesh: stopped meshes do not restart; build a new one")
	}
	m.mu.Unlock()

	if err := m.joinRoster(ctx); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.ctx, m.cancel = context.WithCancel(ctx)
	m.started = true
	for _, a := range m.actors {
		go m.run(m.ctx, a)
	}
	return nil
}

// joinRoster gives every bank the CLEARING HOUSE routes to an actor, in one
// batch.
//
// # It reads the roster and not the banks, and that is a behaviour change
//
// The roster is what says who is a member. A bank that has been founded and not
// admitted has a row of its own and no entry there, and it gets no actor: it
// cannot pay, because Mesh.Submit has nobody to hand its customer's instruction
// to, and it cannot be paid, because the clearing house would answer RC01 for
// its address. That is the truth about such a bank rather than a limitation —
// it has a licence, a book and customers, and no scheme has admitted it — and
// the way in is Mesh.Admit, which registers the actor itself.
//
// It reads BOTH the roster and the bank rows, and the second read is a crossing
// rather than a convenience: Mesh.banks is keyed by ParticipantID, because that
// is what a payment instruction names, and only a bank's own row knows which id
// belongs to which address. Task 18 is where a payment carries BICs and this
// read goes; see payment.Network.GetRosterEntry, which records the same crossing
// pointing the other way.
//
// Both reads happen OUTSIDE m.mu, because they are store I/O and nothing else
// may be blocked on the mesh while they run. The whole roster is then registered
// in one batch, so a bank the mesh cannot route to leaves the mesh as it found
// it — see addActors on why all-or-none is the roster's shape and not a
// convenience.
//
// It MERGES into the bank index rather than assigning it, which matters for one
// interleaving and not for the ordinary case. Both callers run against an empty
// index — Start on a new mesh, JoinRoster on one ForgetBanks has just emptied —
// so on any sequential path the two are the same. What they are not the same
// about is a bank admitted CONCURRENTLY: an admission commits its actor and its
// index entry between the reads above and this write, and an assignment would
// silently drop that entry while leaving its actor running. The result was an
// actor no index named, which nothing could then reach and — before ForgetBanks
// stopped forgetting by index — nothing could remove either.
//
// This call is itself the other half of that shape, and it is why forgetting by
// index is still wrong: addActors above and the copy below are two separate
// critical sections, so between them an actor exists that the index does not
// name. admitBank closes that window for the admission path and cannot close it
// here, because a batch that registered and indexed under one lock would hold
// m.mu across every bank in the roster.
//
// Nothing else is protected by the merge, and it is worth being exact: a reset
// racing an admission is still a mess, and can still refuse the admission (its
// BIC is taken by the roster read) or fail this call (the roster now holds a BIC
// the admission already registered). What the merge removes is the SILENT
// outcome — a bank that answers every read and carries no payment, with nothing
// anywhere saying so. api.Server.resetMu does not cover POST /members, and
// making it do so would serialise admission behind every reset for a race this
// closes.
func (m *Mesh) joinRoster(ctx context.Context) error {
	if m.net == nil {
		return nil
	}
	entries, err := m.net.ListRosterEntries(ctx)
	if err != nil {
		return fmt.Errorf("mesh: reading the clearing house's roster: %w", err)
	}
	if len(entries) == 0 {
		return nil
	}
	members := make(map[iso20022.BIC]bool, len(entries))
	for _, e := range entries {
		members[e.BIC] = true
	}
	ps, err := m.net.ListBanks(ctx)
	if err != nil {
		return fmt.Errorf("mesh: reading the banks behind the roster: %w", err)
	}
	specs := make([]actorSpec, 0, len(entries))
	banks := make(map[payment.ParticipantID]*bank, len(entries))
	for _, p := range ps {
		if !members[p.BIC] {
			continue
		}
		b := &bank{m: m, ops: m.net, bic: p.BIC, pid: p.ID}
		banks[p.ID] = b
		specs = append(specs, actorSpec{bic: p.BIC, name: p.Name, handle: b.handle})
	}
	if len(specs) > 0 {
		if err := m.addActors(specs...); err != nil {
			return err
		}
	}
	// After addActors, so that a roster the mesh refused to route leaves the
	// bank index as it found it too.
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

// ForgetBanks removes every member bank's actor: closes its inbox, waits for its
// goroutine to return, and drops it from the routing table and the bank index.
// The two institutions are untouched.
//
// # Why a mesh needs this at all
//
// Because the store can be replaced underneath it. api.Server.Reset truncates
// every table and rebuilds the sample dataset, and the mesh survives that — it
// is not restarted, and Start is never run again. Without this the actor table
// keeps describing the roster that was there before: a BIC the operator can
// never admit again, because an actor already answers to it, and an entry in the
// bank index pointing at a goroutine whose bank has been deleted. That is not
// hypothetical — it was reachable over HTTP by admitting a bank, resetting, and
// admitting the same BIC again.
//
// It is the counterpart of JoinRoster and is called immediately before the
// truncate, so that between the two the mesh routes to no member bank at all
// rather than to the wrong one. A submission during that window is refused —
// "no bank actor" — which is the truth: the network is being replaced.
//
// # It expects a quiet mesh, and says so rather than assuming it
//
// Closing an inbox does not discard what is in it: pop hands over everything
// already queued before it reports the queue closed, so a message in flight here
// would still be HANDLED, against a store that is about to be truncated. Its
// caller drains first, which is the same ordering Stop's doc demands and for the
// same reason. The wait for each goroutine is what makes "forgotten" mean "no
// handler is still running" rather than "no more messages will be picked up".
//
// It is not itself a Drain and does not take the dead letters. Anything a
// handler could not deal with on the way out stays on the mesh for the next
// Drain, which is where a dead letter belongs and how it stops being attributed
// to whatever happened to collect it.
//
// # Which actors it forgets, and why not the bank index
//
// Every actor that is not one of the two INSTITUTIONS. The obvious reading —
// walk the bank index — is wrong in a way that took a reset to show: an actor
// whose index entry is missing would be unforgettable, so its address would be
// taken for the life of the process and every later reset would fail on it. That
// state is reachable, because joinRoster writes the actors and the index entries
// under separate locks and a bank can be admitted between the two. AddBank does
// both under one lock and cannot produce it; joinRoster cannot use that, because
// a batch registered and indexed under one lock would hold m.mu across a whole
// roster. Reading the actor
// table instead makes "forgotten" total: after this, m.actors holds exactly the
// central bank and the clearing house, whatever the index said.
//
// The two institutions are excluded by identity rather than by kind, because
// that is the only durable distinction here: they are the configuration, they
// have no participant row, and a reset does not touch them.
//
// # A timeout leaves the actors joinable
//
// The deletions happen AFTER every goroutine has returned, never before, and
// that ordering is the whole of what a timeout costs. Stop takes its snapshot
// from m.actors, so an actor deleted before it was joined would be a goroutine
// nothing could ever wait for — a permanent leak in a process that is otherwise
// still running, and, if the caller retried its reset, a store truncated
// underneath a live handler.
//
// So a ForgetBanks that times out leaves the mesh exactly as Stop leaves one
// that times out: the inboxes are closed, the actors are still in the table, and
// the mesh is not half torn down. Its caller may retry — closing a closed queue
// is a no-op and a retry only re-joins — or give up and Stop, which will find
// them. See TestForgetBanksThatTimesOutLeavesTheActorsForStopToJoin.
func (m *Mesh) ForgetBanks(ctx context.Context) error {
	m.mu.Lock()
	if m.stopping || m.stopped {
		m.mu.Unlock()
		return errors.New("mesh: stopping; its member banks are being joined by Stop, not forgotten")
	}
	gone := make([]*actor, 0, len(m.actors))
	for bic, a := range m.actors {
		if bic == m.cfg.CentralBankBIC || bic == m.cfg.ClearingHouseBIC {
			continue
		}
		gone = append(gone, a)
		a.q.close()
	}
	m.mu.Unlock()

	for _, a := range gone {
		select {
		case <-a.done:
		case <-ctx.Done():
			return fmt.Errorf("mesh: forgetting member banks (%s): %w", m.stuck(), ctx.Err())
		}
	}

	// Only now, with every one of them returned. The bank index is cleared of
	// whatever pointed at a forgotten actor, including entries whose actor was
	// already gone — the index is derived from the actor table, not the other way
	// round.
	m.mu.Lock()
	for _, a := range gone {
		delete(m.actors, a.bic)
	}
	for pid, b := range m.banks {
		if _, still := m.actors[b.bic]; !still {
			delete(m.banks, pid)
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
// # What it no longer is
//
// It used to be the second half of an admission over HTTP: api's handler wrote
// the participant row and then asked the mesh for the address, which is the
// ordering that left a bank in the roster that could neither pay nor be paid.
// That ordering is gone. Mesh.Admit claims the address BEFORE anything is
// written and turns the claim into this registration afterwards, so there is no
// longer a moment at which a committed bank can be refused an address. This runs
// in the middle of Admit — after the bank's unit of work commits and before the
// first acmt.007 is sent, since a bank that cannot be reached is one whose
// application nobody could answer. Its other callers are the mesh's own tests,
// which use it to plant an actor without a conversation.
//
// The registration and the index entry are ONE critical section, and the
// reservation an admission made is cleared inside it. Both matter: a gap before
// the index write leaves an actor no index names — reachable by a message and
// unable to submit — and a gap after clearing the reservation would let a second
// admission take the address out from under a bank whose row is already
// committed. joinRoster still has the first of those windows and says so; it
// cannot use this, because a batch registered and indexed under one lock would
// hold m.mu across a whole roster.
func (m *Mesh) AddBank(p *payment.Bank) error {
	if m.net == nil {
		return errors.New("mesh: no network, so there are no member banks to give actors to")
	}
	if err := p.BIC.Validate(); err != nil {
		return fmt.Errorf("mesh: actor %q: %w", p.Name, err)
	}
	b := &bank{m: m, ops: m.net, bic: p.BIC, pid: p.ID}

	m.mu.Lock()
	defer m.mu.Unlock()
	// This bank's own reservation, consumed. Cleared before the duplicate check
	// below, which would otherwise refuse the very address this admission
	// claimed; and inside the same lock, so no other caller sees the address
	// free in between.
	delete(m.reserved, p.BIC)
	if err := m.addActorsLocked(actorSpec{bic: p.BIC, name: p.Name, handle: b.handle}); err != nil {
		return err
	}
	m.banks[p.ID] = b
	return nil
}

// Stop closes every inbox, waits for the actors to finish what they are already
// doing, and returns the dead letters they produced, joined.
//
// It waits, rather than cancelling and returning, because its caller's next
// move is typically to tear the store down: a handler still inside a unit of
// work when the tables are truncated is the shutdown bug this ordering exists
// to prevent. A handler that will not return therefore times out and is NAMED,
// the same as in Drain.
//
// # Queued messages are delivered; chains are cut, and Stop says so
//
// Nothing already queued is lost. Closing an inbox stops it accepting NEW
// messages, and pop hands over everything already in it before it reports the
// queue closed (see queue.pop, where that is deliberate), so every message
// enqueued before Stop was called is still handled.
//
// Chains, though, are CUT. Every inbox is closed in ONE step — under m.mu,
// together with the snapshot of who the actors are — before any actor is
// joined, so a handler still running during shutdown cannot reach anybody at
// all, not even an actor that has not been joined yet. Its send is refused, the
// refusal becomes that handler's error, and the error becomes a dead letter.
//
// Which is why Stop returns them. A shutdown that ended a conversation and said
// nothing would be the silent failure Drain's dead letters exist to prevent,
// reintroduced one method over. Stop's contract is Drain's: what handlers could
// not deal with comes back to the caller. See
// TestStopReportsWhatAHandlerCouldNotDoDuringShutdown.
//
// # The deadline, and why every caller must Drain first
//
// The context given here bounds the handler in flight PLUS the whole depth of
// every inbox at the moment of the close. It is not a bound on one handler.
// Nothing further can be enqueued, because by then every inbox is closed.
//
// Drain first, then Stop — and not merely to make the deadline predictable.
// It is the ONLY way a chain that is in flight completes at all. Stop cuts
// whatever is mid-conversation, so a shutdown or a reset that has not drained
// first ends payments halfway: the debtor's bank debited and the pacs.002 that
// would have told it so never sent. Draining leaves Stop nothing to do but
// join.
//
// Both callers in this repository do it. api.Server.Reset drains before it
// truncates, so no handler is left writing into a store the reset has emptied;
// cmd/server drains before it stops, so a payment in flight when the process is
// interrupted still reaches its end. Neither discards what comes back.
//
// # A Stop that times out leaves the mesh running
//
// It names the actor that would not let go, hands back the dead letters it has
// so far, and deliberately does NOT cancel the mesh context or mark the mesh
// stopped: goroutines are still using both, and a half-torn-down mesh is worse
// than one that plainly refused to stop. Call it again once the handler has
// come back — the inboxes are already closed, so a retry only re-joins — and
// the second call finishes the job. Start keeps refusing throughout, and so
// does addActor. See TestStopCanBeRetriedAfterATimeout.
func (m *Mesh) Stop(ctx context.Context) error {
	m.mu.Lock()
	if !m.started {
		// Never started, or already stopped: there is nothing to join, and any
		// dead letters were taken by the Stop or the Drain that preceded this.
		m.mu.Unlock()
		return nil
	}
	// stopping, the snapshot and the closes are one critical section. The
	// snapshot is the set of actors this call will close and join, so an
	// actor registered after it would be neither — a goroutine nothing ever
	// joins, reading an inbox nothing ever closes. addActors refuses from this
	// moment on, and it cannot slip in beside this because it takes m.mu too.
	//
	// Closing here rather than after the unlock also makes "a handler in
	// shutdown can reach nobody" true rather than nearly true: with the closes
	// outside the lock, a send racing the loop could still land in an inbox the
	// loop had not reached yet.
	m.stopping = true
	actors := make([]*actor, 0, len(m.actors))
	for _, a := range m.actors {
		actors = append(actors, a)
		a.q.close()
	}
	cancel := m.cancel
	m.mu.Unlock()

	for _, a := range actors {
		select {
		case <-a.done:
		case <-ctx.Done():
			return errors.Join(
				fmt.Errorf("mesh: stopping (%s): %w", m.stuck(), ctx.Err()),
				m.takeDeadLetters(),
			)
		}
	}

	m.mu.Lock()
	m.started = false
	m.stopped = true
	m.mu.Unlock()
	cancel()

	// The dead letters last, after every actor has returned, so that a handler
	// whose send was refused by the shutdown itself is included rather than
	// raced.
	return m.takeDeadLetters()
}

// run is one actor's goroutine: pop, handle, repeat, until the inbox is closed
// and empty.
func (m *Mesh) run(ctx context.Context, a *actor) {
	defer close(a.done)
	for {
		it, ok := a.q.pop()
		if !ok {
			return
		}
		m.dispatch(ctx, a, it)
	}
}

// dispatch runs one message through one actor's handler, and is where the
// in-flight counter is given back.
//
// finish is DEFERRED, so the message stops counting as in flight only after the
// handler has returned — by which time any messages that handler sent are
// themselves counted in. That ordering is the whole correctness argument for
// Drain: a chain never shows a moment of quiet in the middle of itself.
// TestDrainWaitsForMessagesThatBegetMessages is the pin, and it bites — move
// this call ahead of the handler instead of after it and that test fails on
// every one of 50 runs.
//
// The error is captured by the closure rather than passed, because the deferred
// call has to see what the handler returned AND run even if it returns by
// panicking.
// The context every handler runs with carries WHO IS ACTING, and that is the
// only thing about the mesh a layer below it can see.
//
// It is there so that a unit of work can be attributed to the institution that
// opened it. Nothing in the domain reads it — payment neither knows nor cares —
// but the book recorder this package tests against does, and "which books did
// the payer's bank reach" has no other answer under a shared store: one process,
// one store, and four actors driving it concurrently, so neither the goroutine
// nor the store nor the call stack can say. Sub-project 8 replaces the question
// with a store per entity; until then this is how it is asked.
//
// A context value rather than an argument because it has to survive the trip
// through payment: a handler calls AcceptInbound, payment opens the unit of work
// several frames down, and no signature on that path has anywhere to put it.
type actorContextKey struct{}

// withActor marks a context as belonging to one institution's work. Mesh.Submit
// does it for the submitting bank's synchronous half; dispatch does it for
// everything else.
func withActor(ctx context.Context, bic iso20022.BIC) context.Context {
	return context.WithValue(ctx, actorContextKey{}, bic)
}

// actorOf reports which institution's work this context belongs to, if any. A
// context with no actor is work no institution did: a seed, a test fixture, an
// operator's cut-off.
func actorOf(ctx context.Context) (iso20022.BIC, bool) {
	bic, ok := ctx.Value(actorContextKey{}).(iso20022.BIC)
	return bic, ok
}

func (m *Mesh) dispatch(ctx context.Context, a *actor, it item) {
	var err error
	defer func() { m.finish(a, err) }()
	// The observation phase is recorded separately, and only when there is
	// something to observe: an Observe that blocks holds this actor, and stuck()
	// must be able to say that is what is holding it. See Mesh.busy.
	if m.tap != nil {
		m.setBusy(a.bic, "in Observe")
		m.tap(a.bic, it.from, it.raw)
	}
	m.setBusy(a.bic, "in a handler")
	err = a.handle(withActor(ctx, a.bic), it.from, it.raw)
}

// now is the clock every header this mesh writes is stamped from, and it is the
// payment network's own.
//
// A mesh with a clock of its own would be a second answer to what time it is,
// and under the frozen clock these tests run on the two would be years apart: a
// pacs.008 dated 2026 carrying a payment booked in 2025. The package doc says
// the actors share one clock; this is that sentence's implementation.
//
// It is only ever called from a handler or from Submit, both of which exist only
// on a mesh that has a network.
func (m *Mesh) now() time.Time { return m.net.Now() }

// nextMsgID mints the identifier a message travels under: the sender's BIC and a
// number nobody else in this mesh will use.
//
// Per-mesh and not per-sender, which is a simplification worth naming. A real
// BizMsgIdr is unique within the sender and nowhere else, so two banks
// legitimately emit the same one; a receiver that assumed otherwise would
// deduplicate one bank's message against another's. Here one counter serves
// every actor, which is strictly stronger than the standard requires and cannot
// therefore hide a bug the standard would allow. What it does buy is that a
// message id is unique under a FROZEN clock, which a timestamp-derived one would
// not be — and this package's tests run on one.
func (m *Mesh) nextMsgID(from iso20022.BIC) string {
	return fmt.Sprintf("%s-%d", from, m.msgSeq.Add(1))
}

// finish ends one message: its dead letter, the actor's busy flag and the
// decrement, in ONE critical section.
//
// One, and not three, because Drain wakes on the decrement and then reads the
// dead letters under this same lock. Recorded afterwards — even one statement
// afterwards — and a Drain woken by the close could take the errors before this
// goroutine had added them, and report a clean mesh over a failed handler. That
// would not be a test that fails sometimes; it would be a test that passes
// almost always, which is worse — and that is measured, not assumed: with the
// record moved into a second critical section after the decrement, 200 runs of
// TestDrainReportsHandlerErrors could not tell the difference, and only a 5ms
// delay planted in the gap made it fail. A window no test can see is a window
// that has to be closed by construction, and holding the lock across all three
// closes it.
//
// Logging happens after the unlock. A log handler is arbitrary code and can be
// slow; nothing else in this mesh should wait behind it.
func (m *Mesh) finish(a *actor, err error) {
	m.mu.Lock()
	delete(m.busy, a.bic)
	if err != nil {
		m.dead = append(m.dead, fmt.Errorf("%s (%s): %w", a.bic, a.name, err))
	}
	m.leaveLocked()
	m.mu.Unlock()

	if err != nil {
		m.log.Error("mesh: dead letter", "actor", a.bic, "name", a.name, "error", err)
	}
}

// enterLocked counts one message in. The caller holds m.mu — see send for why
// that is not an accident.
func (m *Mesh) enterLocked() {
	if m.inFlight == 0 {
		m.quiet = make(chan struct{})
	}
	m.inFlight++
}

// leaveLocked counts one message out, waking any Drain when the last one goes.
// The caller holds m.mu.
func (m *Mesh) leaveLocked() {
	m.inFlight--
	if m.inFlight == 0 {
		close(m.quiet)
	}
}

func (m *Mesh) leave() {
	m.mu.Lock()
	m.leaveLocked()
	m.mu.Unlock()
}

func (m *Mesh) setBusy(bic iso20022.BIC, phase string) {
	m.mu.Lock()
	m.busy[bic] = phase
	m.mu.Unlock()
}

// stuck names what the mesh is holding: the actors inside a handler, and the
// inboxes with something in them. Sorted, or "idle" if there is neither.
//
// The inboxes matter as much as the handlers, and they were missing at first.
// A Drain that hangs on a message nobody has picked up would otherwise report
// "(idle)" — the bare deadline this exists to avoid — and it would do it at the
// exact moment naming something would help most, because "queued and unread" is
// a stuck actor, not a quiet one. See TestDrainNamesAnInboxNobodyIsReading.
//
// Sorted because an error message assembled from a map is a different string on
// every run, and an error a test can only match loosely is an error nobody can
// assert on.
//
// It takes m.mu and then each queue's own lock, which is the same order send
// takes them in and the only order anything in this package takes them in.
func (m *Mesh) stuck() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	names := make([]string, 0, len(m.busy)+len(m.actors))
	for bic, phase := range m.busy {
		names = append(names, string(bic)+" "+phase)
	}
	for bic, a := range m.actors {
		if n := a.q.depth(); n > 0 {
			names = append(names, fmt.Sprintf("%s with %d queued", bic, n))
		}
	}
	if len(names) == 0 {
		return "idle"
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// send is the single choke point. Every message in this system passes through
// here.
//
// It is one function and not a Transport interface deliberately. The roadmap's
// objection to a one-implementor abstraction stands, and one function answers
// it just as well: swapping this body for an HTTP POST is a contained change,
// and until someone wants that, there is nothing for an interface to abstract
// over.
//
// It enqueues BYTES. Structs never cross an actor boundary — if two actors
// exchanged *Pacs008 the message format would be decoration on a function
// call, malformed input would stop being a reachable failure mode, and the
// FF01 path would be untestable.
//
// It never blocks, because the inbox it pushes into is unbounded. That is what
// lets two actors message each other: the CSM sends to a bank while that bank
// is sending to the CSM, and with any fixed buffer between them that pair
// wedges. See TestMutuallyMessagingActorsDoNotDeadlock.
//
// # Counting in and enqueueing are one step
//
// The lookup, the increment and the push are one critical section. What that
// buys is not an ordering; it is that the intermediate state is UNOBSERVABLE.
// Everything else that touches the counter — leaveLocked, Drain,
// takeDeadLetters — must take m.mu, so while it is held nobody can see a queue
// holding a message the counter does not, or a counter holding one the queue
// does not, whichever way round the two statements happen to be written. A
// later edit that swapped them would still be correct, and that is the point:
// the invariant does not rest on anyone remembering which line comes first.
//
// The increment-before-push order is kept anyway, and it is worth being
// straight about why it is not what makes this safe: that order is sufficient
// on its own, even with the two steps separately locked. quiet is open exactly
// while inFlight > 0; a message reaches a queue only after its own increment,
// and is decremented only after it has been popped; so at every instant a
// message sits in a queue, inFlight >= 1 and any Drain blocks. The single lock
// is belt and braces over a correct ordering, not the thing that rescues a
// broken one.
//
// The inversion — push, then increment — IS broken, and no test in this package
// catches it, even with a delay planted in the gap. That is worth understanding
// rather than filing as a hole in the suite: every message in this system is
// sent from inside a handler whose own message is still counted in flight, and
// send returns, increment done, before that handler returns. The parent's count
// covers the child's gap unconditionally. What is left uncovered is a send from
// a goroutine that no in-flight message covers, racing a concurrent Drain —
// which is exactly the case Drain's own doc declines to promise anything about.
// A test-only hook to force that window would buy nothing the lock has not
// already bought.
func (m *Mesh) send(from, to iso20022.BIC, env iso20022.Envelope) error {
	raw, err := iso20022.Marshal(env)
	if err != nil {
		return fmt.Errorf("mesh: marshalling for %s: %w", to, err)
	}
	return m.sendRaw(from, to, raw)
}

// sendRaw is send with the marshalling already done: the bytes, and who they are
// from.
//
// It is split out because bytes are what a queue carries, and because a message
// that DID NOT come from this system's marshaller is a thing a receiver must
// cope with. Nothing in production calls it directly — send is the only door in
// — but without it every message in this package would be one iso20022.Marshal
// had blessed, and FF01 would be a code with no path to it. See
// TestAMalformedEnvelopeIsAnsweredWithFF01.
func (m *Mesh) sendRaw(from, to iso20022.BIC, raw []byte) error {
	m.mu.Lock()
	a, ok := m.actors[to]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrUnknownBIC, to)
	}
	m.enterLocked()
	accepted := a.q.push(item{from: from, raw: raw})
	m.mu.Unlock()

	if !accepted {
		// The actor is stopped and will never read this. Give the counter back
		// — a message counted in and never delivered is a Drain that blocks for
		// ever. See TestSendAfterStopIsRefusedAndLeavesDrainQuiet.
		m.leave()
		return fmt.Errorf("mesh: %s is stopped and cannot be sent to", to)
	}
	return nil
}

// Drain blocks until no message is in flight, then returns any errors handlers
// could not answer, joined.
//
// Returning the dead letters is the load-bearing part. An actor handler has
// nobody to return an error to, so without this every test that drained would
// pass over a swallowed failure. This repository has recorded three separate
// incidents of prose asserting behaviour the code did not have; an actor that
// eats errors is the same failure with a concurrency wrapper on it.
//
// The errors are TAKEN, not accumulated: a second Drain over the same quiet
// mesh reports nothing, so a test that asserts on a failure does not hand it to
// the next one.
//
// What it does NOT promise: that no message will arrive after it returns. It
// waits for the work that was in flight when it was called to finish, and a
// caller that submits a payment from another goroutine at the same moment may
// see that one land afterwards. Every message this system sends is sent by a
// handler, so a chain started before Drain is entirely covered; a chain started
// beside it is the caller's own race.
//
// A Drain that times out reports the deadline AND the dead letters it has,
// joined. Leaving them behind would hand a handler's failure to whichever later
// Drain happened to collect it, which is how an error ends up attributed to the
// wrong payment. See TestDrainThatTimesOutStillReportsTheDeadLetters.
//
// Do not call it from inside a handler. The message being handled is itself in
// flight, so a handler that drains waits for itself; the same goes for Stop,
// which waits for the goroutine it is called on. Neither is a lock-order
// deadlock — no lock is held across either — but both wedge, and the layer that
// wires handlers to the API in Tasks 10-14 is where that becomes tempting.
func (m *Mesh) Drain(ctx context.Context) error {
	m.mu.Lock()
	ch := m.quiet
	m.mu.Unlock()

	select {
	case <-ch:
	case <-ctx.Done():
		return errors.Join(
			fmt.Errorf("mesh: draining (%s): %w", m.stuck(), ctx.Err()),
			m.takeDeadLetters(),
		)
	}
	return m.takeDeadLetters()
}

// takeDeadLetters returns the errors no handler had anyone to give back, and
// clears them.
//
// Taken rather than accumulated, so that one test's failure is not handed to
// the next drain — and so that a caller which has read them has really dealt
// with them.
func (m *Mesh) takeDeadLetters() error {
	m.mu.Lock()
	dead := m.dead
	m.dead = nil
	m.mu.Unlock()
	return errors.Join(dead...)
}

// Submit runs the submitting bank's half synchronously and then sends.
//
// The send is OUTSIDE the unit of work, deliberately. A handler that enqueued
// while holding a transaction would schedule work against uncommitted state, or
// against state a rollback removed — and because the queue is unbounded it would
// not even deadlock, it would just be wrong. TestARolledBackSubmitSendsNothing
// pins it.
//
// What that test can actually see is worth being exact about, because the claim
// above is wider than the check. It provokes a submission that fails inside its
// unit of work and asserts the mesh carried nothing, which falsifies any
// arrangement that sends on a path the store rolled back. It does NOT
// distinguish this ordering from an enqueue INSIDE the transaction, and cannot:
// SubmitPaymentTx has no failure after the point at which a message could be
// built, so there is no way to reach "sent, then rolled back" at all. The
// ordering here is what keeps it that way as the submitting half grows.
//
// Synchronously, because the caller has an error to answer with. A customer
// whose instruction fails their own bank's checks — no funds, an account that is
// not theirs, a duplicate reference — is told so, then and there. What they are
// NOT told is what the far side thinks: this returns an Initiated payment and
// nothing more, and the fate of that payment arrives later, at another actor, as
// a message. That is the whole difference the mesh makes, and it is why api
// answers this with 202 Accepted rather than 201 Created.
//
// It runs on the CALLER's goroutine, not the bank actor's, and that is not a
// shortcut: an actor's goroutine handles what arrives in its inbox, and a
// customer instruction does not arrive in an inbox — it comes in from outside
// the mesh entirely. The work is still the bank's, and it is marked as the
// bank's, so the recorder attributes every book it touches to the bank and not
// to whoever called in.
//
// # Which bank is handed the instruction
//
// The scheme's DIRECTION decides, and it is asked here rather than assumed. A
// credit transfer is handed to the payer's bank, because the payer is
// instructing their own bank to push. A direct debit is handed to the PAYEE's
// bank, because a collection is the payee asking for money it is owed: the
// payer's bank is the counterparty on that one and hears about it as a message
// like any other counterparty does.
//
// Taking the debtor unconditionally — which is what this did through Task 10 —
// is the wrong bank for every direct debit, and it is invisible until a pull
// exists: the payment goes through, the books balance, and the only thing wrong
// is that the wrong institution did the work and signed the message. api's
// handleSubmitPayment asks the same question, for the same reason, one layer up.
//
// It reads the network to ask, so like Mesh.now it exists only on a mesh that
// has one. A mesh built over no network has no participant roster and therefore
// no bank actors, so every submission to it was already an error; this makes
// that a precondition instead of an outcome.
//
// # And which payments there is no bank to hand it to at all
//
// An ON-US payment — one bank at both ends — is refused before any of that. It
// is not a clearing payment: nothing leaves the institution, so no reserves
// move, no position nets and no settlement agent has anything to settle. See
// ErrOnUsPayment. That refusal is this system declining a ROUTE and not the
// payment; a book transfer between two customers of one bank is a real product
// and a task of its own.
func (m *Mesh) Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	scheme, ok := m.net.Scheme(req.Scheme)
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
	if req.Debtor.Participant != "" && req.Debtor.Participant == req.Creditor.Participant {
		return payment.Payment{}, fmt.Errorf("mesh: %s is both the payer's bank and the payee's for this instruction: %w",
			req.Debtor.Participant, ErrOnUsPayment)
	}
	submitter := submitterOf(scheme, req.Debtor, req.Creditor).Participant

	m.mu.Lock()
	b, ok := m.banks[submitter]
	m.mu.Unlock()
	if !ok {
		// The mesh has no actor to play this bank, so nothing it submitted could
		// ever be answered. Refusing here is better than accepting a payment that
		// would sit Initiated for ever.
		return payment.Payment{}, fmt.Errorf("mesh: no bank actor for participant %s", submitter)
	}
	return b.submit(ctx, req)
}

// Admit brings a bank into being and applies to the scheme for it.
//
// Its synchronous half is Mesh.Submit's: the bank's own work, on the caller's
// goroutine, marked as the bank's so the recorder attributes it correctly, and
// committed before anything is sent. What it does NOT answer is whether the
// scheme accepted — that arrives later, at two other actors, as a message. The
// bank it returns is Founded, which is a working bank that can open customer
// accounts and cannot fund one. See "A founded bank cannot be paid is the
// intent" in doc.go for what this transport does and does not enforce about the
// rest of it.
//
// # The address is reserved first, and that is the orphan defect's fix
//
// A BIC is the only thing about an admission that can clash, and it used to be
// checked LAST: api.handleAddParticipant wrote the participant row and then
// asked the mesh for the address, so a refusal left a bank in the roster that
// could neither pay nor be paid, with no way back. Reversed here. The address is
// claimed before the bank's unit of work runs and released again if that unit of
// work fails, because an in-memory rollback is reliable and a rollback of a
// committed transaction is not. See Mesh.reserved, and
// TestNothingIsWrittenWhenTheAddressIsRefused, which is what makes the ordering
// falsifiable.
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
// The roster is read for that decision and the actor table decides it, which is
// the two authorities in one function: the roster is the DOMAIN's truth about
// who holds an address, the actor table is the TRANSPORT's. They can disagree
// only in one direction here — a bank in the roster with no actor is a mesh that
// has not read the roster since it was admitted — and the lock is what makes the
// answer one answer.
//
// # A re-drive asks for the assets the bank actually has
//
// One acmt.007 asks for one currency, so this sends one per asset the bank
// operates in, in asset order. For a new bank that is exactly what the caller
// named (with payment's joining default applied); for a RE-DRIVE it is the
// bank's own chart of accounts, and the caller's list is ignored — the bank
// exists and its internal accounts are what they are, and asking for a
// settlement account in an asset it holds none of would produce a reference it
// has nowhere to record.
//
// # It mints a new process id every time, including on a re-drive
//
// Refs/PrcId is the conversation's only correlator, and a re-drive gets a fresh
// one rather than reusing what the interrupted attempt quoted. That is safe
// exactly because a re-drive is only allowed when the roster has NO entry for
// the address: the clearing house's refusal compares the request's reference
// with the one on the entry it already holds, and there is none. The bank's own
// row is in the same position — payment.Bank.AdmissionRef is empty until an
// acknowledgement is recorded, so a bank that never got one has nothing to
// disagree with.
//
// # A PARTLY admitted bank cannot be re-driven, and that is a gap with an owner
//
// The two conditions above are the same condition seen twice, and there is a
// state that fails both: a bank that recorded a membership in one asset and
// whose second asset's acknowledgement never arrived. It is a Member, so its row
// carries a reference; it is in the roster, so this call refuses the address
// outright — and a fresh admission would be refused by the clearing house and by
// the bank alike, correctly, since it really is a different admission.
//
// So there is no door. The bank clears in one asset and holds a settlement
// account at the central bank in a second that it does not know the number of,
// which is the inconsistency RecordMembershipTx's own doc describes: a deposit
// in that asset fails while the operator console reports the reserve, because
// the console reads the central bank's row.
//
// It is reachable only from a dead letter — every message of an admission is
// carried exactly once and in order by this transport, so the acknowledgement
// goes missing only if a handler could not act on it — and it is Task 19's, with
// the rest of this system's half-finished conversations.
//
// That task's reconciliation is what has to FIND it, and the obvious comparison
// does not. "A bank whose assets and the settlement agent's accounts for its BIC
// do not match" is what this said, and in this state they MATCH: measured by
// parking the second acmt.010 of a two-asset admission before the bank, the
// bank's assets are {EUR, USD} and the agent's accounts for its BIC are
// {EUR, USD}. They match for the reason the gap exists at all — the settlement
// agent opens the account before it acknowledges (centralBank.receiveAdmission),
// so what went missing is the bank's NOTE of the number and not the account.
//
// The discriminator is the one api.Server.reserveRows uses: an EMPTY settlement
// reference on the bank's own row in an asset, TOGETHER WITH a reserve row the
// agent answers for in that same asset. The same empty reference with NO reserve
// row is the other half-finished admission — an acmt.007 that never arrived, or
// one the agent refused — and the mismatch comparison does find THAT one, which
// is the whole of why it looked right for this one. Closing
// it needs a way to re-drive one asset of an existing admission, which means
// quoting the reference the bank already recorded rather than minting one, and
// that is a decision about the flow rather than about this function.
// csm.relayReturn and centralBank.advise record their own version of the same
// class of gap.
func (m *Mesh) Admit(ctx context.Context, name string, bic iso20022.BIC, assets []ledger.AssetCode) (*payment.Bank, error) {
	if m.net == nil {
		return nil, errors.New("mesh: no network, so there is no bank to admit")
	}
	if err := bic.Validate(); err != nil {
		return nil, fmt.Errorf("mesh: %q: %w", name, err)
	}
	// Everything below is the joining bank's work, and is recorded as its own.
	// See withActor: the recorder has no other way to attribute a book, and the
	// bank whose book this is has no actor yet.
	ctx = withActor(ctx, bic)

	// The roster read is OUTSIDE the lock, for joinRoster's reason. What it
	// answers is the domain's question — is this address already a member's —
	// and the lock below is what turns the answer into a decision.
	_, err := m.net.GetRosterEntryByBIC(ctx, bic)
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

	var bank *payment.Bank
	if redriving != "" {
		// A bank this mesh founded that the roster has no entry for. Its row is
		// read OUTSIDE the lock, for joinRoster's reason, and nothing is founded.
		if bank, err = m.net.GetBank(ctx, redriving); err != nil {
			return nil, fmt.Errorf("mesh: %s is re-driving the admission of %s and cannot read it: %w", bic, redriving, err)
		}
	} else {
		if bank, err = m.net.FoundBank(ctx, name, bic, assets); err != nil {
			// The reservation goes back before the caller is told, so a refused
			// unit of work leaves the address exactly as free as it found it.
			m.releaseAddress(bic)
			return nil, err
		}
		if err := m.AddBank(bank); err != nil {
			// Unreachable while the reservation holds — nothing else can have
			// taken the address — but a bank that is committed and unreachable is
			// the orphan again, so it is reported rather than assumed away.
			m.releaseAddress(bic)
			return bank, fmt.Errorf("mesh: %s was founded and could not be given an actor: %w", bic, err)
		}
	}

	// The sends are OUTSIDE the unit of work, for Mesh.Submit's reason: a request
	// enqueued from inside FoundBankTx would be one the scheme could act on
	// against a bank the store then rolled back.
	ref := m.nextProcessID(bic)
	to := m.cfg.ClearingHouseBIC
	for _, asset := range slices.Sorted(maps.Keys(bank.Assets)) {
		env, err := payment.AdmissionMessage(
			payment.AdmissionRequest{Name: bank.Name, BIC: bank.BIC, Asset: asset, Ref: ref},
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

// claimAddress takes a BIC for an admission that is about to run, or names the
// bank whose interrupted admission is being re-driven.
//
// Three answers and one lock. A free address is RESERVED and the empty id comes
// back. An address a bank of this mesh already answers to comes back as that
// bank's id, no reservation is made, and the caller founds nothing — the roster
// has already been asked and holds no entry for it, so this is a re-drive.
// Anything else is ErrAddressTaken.
//
// "Anything else" is worth spelling out because two of its three cases have
// nothing to do with banks: an address one of the two INSTITUTIONS answers to,
// and an address another admission has reserved and not yet registered. Both are
// refusals about connectivity rather than about membership, which is the whole
// of what the mesh's authority over an address amounts to.
//
// The second of those is ErrAdmissionInFlight, which wraps ErrAddressTaken so it
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
func (m *Mesh) claimAddress(bic iso20022.BIC) (payment.ParticipantID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping || m.stopped {
		return "", errors.New("mesh: stopping; a bank admitted now would have no goroutine to read its inbox")
	}
	if m.reserved[bic] {
		return "", fmt.Errorf("%w: %s", ErrAdmissionInFlight, bic)
	}
	if _, taken := m.actors[bic]; taken {
		for pid, b := range m.banks {
			if b.bic == bic {
				return pid, nil
			}
		}
		return "", fmt.Errorf("%w: %s", ErrAddressTaken, bic)
	}
	m.reserved[bic] = true
	return "", nil
}

// releaseAddress gives a reservation back. See Mesh.reserved.
func (m *Mesh) releaseAddress(bic iso20022.BIC) {
	m.mu.Lock()
	delete(m.reserved, bic)
	m.mu.Unlock()
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
// It is Submit's counterpart and shares its whole shape — synchronous, on the
// caller's goroutine, marked as the clearing house's work, sending only after
// the unit of work has committed — for the reasons csm.closeCycle sets out. What
// differs is who is instructing: a customer instructs their bank, and an
// operator (or api's POST /cycles/{cid}/close) reaches a cut-off. Neither arrives in an
// inbox, which is why neither is a message.
//
// What it does NOT answer is whether the cycle settled. It returns the cycle as
// the clearing house left it — Closed, with net positions — and settlement
// happens later, at another actor, and comes back as a message. A caller that
// wants to know reads the cycle again after the conversation has finished; a
// test drains first. That is the same difference Submit makes, one layer up.
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
// Like CloseCycle it is synchronous up to the send, answers with the cycle
// rather than with a settlement, and refuses on a mesh with no network rather
// than dereferencing.
func (m *Mesh) Settle(ctx context.Context, id payment.CycleID) (payment.ClearingCycle, error) {
	if m.csm == nil {
		return payment.ClearingCycle{}, errors.New("mesh: no network, so there is no cycle to settle")
	}
	return m.csm.settle(ctx, id)
}

// Reject is the clearing house declining a payment it is holding, on an
// operator's say-so rather than on a counterparty's.
//
// Every other rejection in this system is DECIDED by an actor that was sent a
// message: the payee's bank cannot apply a credit, the payer's bank has no
// funds, the clearing house has no open cycle. This one comes in from outside
// the mesh, exactly as Submit, CloseCycle and Return do, and for the same
// reason: an operator instructing is not a message arriving.
//
// # It is half of a rejection, and the other half is another actor's
//
// What runs here is the clearing house's own unit of work — the payment moves to
// Rejected and leaves its cycle — and that is what the caller is told about,
// synchronously, because it is what an operator can be answered about. The
// payer's money is still in their own bank's clearing suspense at that point.
// Giving it back is the PAYER's BANK's act, in its own book, and it happens
// later: the pacs.002 this sends is what tells that bank to do it, and
// bank.receiveStatus is what does it. A caller that wants to see the refund
// reads the payer's balance after the conversation has finished; a test drains.
//
// That seam is the one RejectAtCSMTx's doc names and says the mesh is where it
// stops being hidden. Before this, api ran both halves in one transaction — see
// the deleted rejectWholePayment — so a reversal that failed took the rejection
// down with it, which is an outcome two separate institutions cannot produce. It
// now fails the way it really would: the rejection stands, the refund does not
// happen, and the mesh reports a dead letter rather than nothing.
//
// # The message it refers back to
//
// A pacs.002 says which message it is about, and there ISN'T one — no bank sent
// anything that provoked this. So the original message is named as unavailable,
// by the same NOTPROVIDED convention answerUnreadable uses and for the same
// honest reason: inventing a message id the payer's bank never sent would be
// worse than saying there was none. Nothing downstream needs it — a bank matches
// a status to a payment by the transaction reference, which is real here.
//
// Like CloseCycle it refuses on a mesh with no network rather than
// dereferencing, and for CloseCycle's reason: it reaches the clearing house's
// handler directly, so there is no map lookup on the way in that would have
// caught it. See Mesh.Return for the other half of that asymmetry.
func (m *Mesh) Reject(ctx context.Context, id payment.PaymentID, code iso20022.StatusReason, text string) (payment.Payment, error) {
	if m.csm == nil {
		return payment.Payment{}, errors.New("mesh: no network, so there is no payment to reject")
	}
	return m.csm.reject(ctx, id, code, text)
}

// Return sends a settled payment back: the R-transaction, and the last thing
// that can happen to a payment.
//
// It is Submit's and CloseCycle's third sibling — synchronous, on the caller's
// goroutine, sending only after the returning bank's half has run — because a
// return arrives from outside the mesh in the same way both of those do. An
// operator (or api's POST /payments/{payid}/return) asks for it; no inbox is involved.
// Since Task 16e "after the returning bank's half has run" means after that bank
// has POSTED, not merely after it has checked; see bank.returnPayment.
//
// # Which bank is handed the instruction
//
// The bank that RECEIVED the original instruction, which is never the one that
// submitted it. On a push that is the payee's bank, which was credited and has
// discovered it cannot apply the money — a closed account, an account that
// cannot take a credit. On a pull it is the payer's bank, whose customer
// disputes the collection. Both are the far side of the message that started
// the payment, which is what makes a return the only flow here that begins at
// the bank that answered. See returnerOf.
//
// # It answers with an error and nothing else
//
// Not with a payment, and the reason has changed under it. This used to say the
// returning bank's half posts nothing and decides nothing beyond whether there
// is a settled payment to return. That half posts now — its own customer leg,
// before the message exists — and it can refuse there, which is why an error is
// the whole of what a caller needs: on a push a payee who has spent the money
// comes back AM04 and nothing was sent.
//
// What survives is the reason a PAYMENT would be no use. The row the caller
// could read is still Settled — one leg is posted, and a return is not finished
// until the other bank posts, which happens at another actor after this call has
// returned — so handing it back would say less than the caller already knows.
// Submit returns an Initiated payment because it CREATED one; there is no such
// value here, and inventing one by re-reading the row after the send would be a
// race dressed up as a result.
//
// It follows that a send that fails after the leg is posted comes back as an
// error alone rather than with the payment beside it, which is where this
// differs from bank.submit. The half-happened state is real and is recorded on
// the payment row — this bank's leg id, with the status still Settled — and
// nothing above this reads a Payment it could be carried in. See
// bank.returnPayment.
//
// Like Mesh.Submit it reads the network, so it exists only on a mesh that has
// one — and like Submit it DEREFERENCES rather than checking: a mesh with no
// network has no participant roster and therefore no bank actors, so every
// return to it was already an error. Mesh.CloseCycle refuses explicitly
// instead, and the difference is not inconsistency: it has no map lookup on the
// way in that would have caught the case, and this does.
func (m *Mesh) Return(ctx context.Context, id payment.PaymentID, reason iso20022.ReturnReason, text string) error {
	// The routing question, and only that: which bank's instruction is this?
	// It is asked here rather than inside the bank for the reason Submit asks
	// the scheme here — the answer is what CHOOSES the actor, so no actor can
	// have made it. The read costs no book: a payment is a network-scoped row,
	// and reading one records nothing at all (see books_test.go).
	p, err := m.net.GetPayment(ctx, id)
	if err != nil {
		return err
	}
	scheme, ok := m.net.Scheme(p.Scheme)
	if !ok {
		return fmt.Errorf("mesh: no scheme %q, so no bank returns %s: %w", p.Scheme, p.ID, payment.ErrSchemeNotFound)
	}
	returner := returnerOf(scheme, p.Debtor, p.Creditor).Participant

	m.mu.Lock()
	b, ok := m.banks[returner]
	m.mu.Unlock()
	if !ok {
		// Same refusal Submit makes, for the same reason: a return this mesh
		// has no actor to send would be one nobody could ever act on.
		return fmt.Errorf("mesh: no bank actor for participant %s", returner)
	}
	return b.returnPayment(ctx, id, reason, text)
}

// submitterOf is the party whose bank hands a payment to the clearing house.
//
// One rule, two directions, and every actor in this package that has to name the
// instructing agent uses it: Mesh.Submit to choose whose goroutine does the
// submitting half, and csm.receiveStatus to choose whose inbox the answer goes
// back to. Written once because the two must agree — an answer addressed to a
// bank that did not submit is a message nobody was waiting for.
//
// It takes the two refs rather than a Payment, because Submit has only a request
// and a request is not yet a payment.
func submitterOf(scheme payment.Scheme, debtor, creditor payment.PartyRef) payment.PartyRef {
	if scheme.Direction() == payment.Pull {
		return creditor
	}
	return debtor
}

// returnerOf is the party whose bank sends a settled payment back: submitterOf's
// counterpart in both senses, the other party and the other role.
//
// The rule itself is payment.ReturnerOf, and that is where its reasoning lives.
// It moved out of this file when the domain acquired a second use for it —
// payment.PostReturnLegTx decides whether a bank may REFUSE its leg by asking
// whether that bank is the returner — and two copies would have been free to
// disagree about who the returner is. This stays as a delegation so that the
// call sites in this package, which read as mesh-local rules beside
// submitterOf, do not have to.
func returnerOf(scheme payment.Scheme, debtor, creditor payment.PartyRef) payment.PartyRef {
	return payment.ReturnerOf(scheme, debtor, creditor)
}

// returnMsgDef is the pacs.004's message name, which two actors here dispatch a
// pacs.002 by.
//
// A status report says which message definition it is ABOUT — OrgnlMsgNmId,
// written by payment.StatusMessage and read back by payment.ReadStatus — and
// that element is load-bearing in this mesh rather than decorative. The clearing
// house is answered by the settlement agent about a CYCLE it instructed and
// about a PAYMENT whose return it forwarded, and the two answers are the same
// message definition arriving from the same BIC; reading one as the other would
// look a cycle id up as a payment. A bank is answered about the instruction it
// submitted and about the return it asked for, and only the first is ever a
// reason to give a payer their money back.
//
// Taken from the codec rather than written out as a literal, so that it cannot
// drift from the identifier iso20022 actually puts on the wire.
var returnMsgDef = iso20022.Pacs004{}.MessageDefinitionIdentifier()

// isAbout reports whether a status answers a message of the given definition.
//
// It parses the document a second time, which is cheap and deliberate:
// payment.ReadStatus is pure, and the alternative is threading the parse
// through a dispatch that exists precisely to decide WHICH handler should do
// the reading.
func isAbout(doc *iso20022.Pacs002, msgDef string) bool {
	orig, _ := payment.ReadStatus(doc)
	return orig.MsgDefIdr == msgDef
}

// notProvided is what a message says where a reference is genuinely unavailable.
//
// It is the EPC's convention, already used by payment for a credit transfer with
// no end-to-end reference, and it is here for the one case that has no reference
// at all: a pacs.002 answering a file that did not parse. That report cannot
// quote the original's message id, its message name or the transaction's
// references, because the bytes carrying them were unreadable — and this
// package's pacs.002 makes all four mandatory (iso20022.OriginalGroupHeader and
// PaymentTransactionStatus, the latter needing at least one back-reference).
//
// A real network would not have the problem: an unreadable file is answered with
// admi.002 MessageReject, which refers back by nothing and carries a reason on
// its own. This system has no admi.002, so the FF01 goes in the message it does
// have, with the unavailable references named as unavailable rather than
// invented. The substitution is recorded here rather than hidden in the one
// function that makes it.
const notProvided = "NOTPROVIDED"

// answerUnreadable tells whoever sent a message that it could not be parsed.
//
// This is why item carries the sender beside the bytes. A message whose header
// is unreadable cannot say who it is from, so a receiver that had only the bytes
// could DETECT a malformed file and not answer it — which would make FF01, the
// one rejection every real receiver must be able to send, the one this system
// could not.
//
// The answer is a pacs.002 and not a dead letter, and the handler that calls
// this returns nil: a message it answered is a message it dealt with. What
// cannot be dealt with is the answer failing to send, which comes back as the
// dead letter it is.
func (m *Mesh) answerUnreadable(self, sender iso20022.BIC, cause error) error {
	env, err := payment.StatusMessage(
		payment.OriginalMessage{MsgID: notProvided, MsgDefIdr: notProvided},
		[]payment.TransactionStatusReport{{
			EndToEndID: notProvided,
			Status:     iso20022.TransactionStatusRejected,
			Code:       iso20022.StatusReasonInvalidFileFormat,
			Text:       cause.Error(),
		}},
		payment.MessageContext{From: self, To: sender, MsgID: m.nextMsgID(self), Now: m.now()},
	)
	if err != nil {
		return fmt.Errorf("mesh: %s could not build the FF01 for %s: %w", self, sender, err)
	}
	return m.send(self, sender, env)
}
