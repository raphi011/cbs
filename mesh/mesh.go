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
	"github.com/raphi011/cbs/payment"
)

// ErrUnknownBIC is a send to a BIC no actor answers to. It becomes RC01 on the
// wire — BankIdentifierIncorrect — which is exactly what it is.
var ErrUnknownBIC = errors.New("mesh: no actor for this BIC")

// ErrAddressTaken is a registration for a BIC some other actor already answers
// to. See addActors for why it is refused rather than absorbed.
//
// It is a sentinel and not just a message because the layer above has a REMEDY
// for it and none for the mesh's other refusals: a clashing address is fixed by
// admitting the bank on one of its own, and an operator told that during a
// shutdown — the other way AddBank can fail — would follow it into a second
// orphaned row. See api's handleAddParticipant.
var ErrAddressTaken = errors.New("mesh: another actor already answers to this BIC")

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
// The member banks are NOT created here: they come from the participant roster,
// which is a store read, and a constructor that did I/O could not be called
// before the store was ready. Start reads the roster, which is why cmd/server
// seeds first and starts the mesh second; AddBank covers the banks that join
// after that, which is every bank a human admits over HTTP.
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
		net:    net,
		cfg:    cfg,
		log:    log,
		tap:    cfg.Observe,
		actors: make(map[iso20022.BIC]*actor),
		banks:  make(map[payment.ParticipantID]*bank),
		busy:   make(map[iso20022.BIC]string),
		quiet:  quiet,
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
		m.csm = &csm{m: m, ops: net, bic: cfg.ClearingHouseBIC}
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
// inboxes, and it has to be reachable before the request that admitted it has
// answered. See TestAnActorAddedAfterStartReceives and AddBank.
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
// AddBank), so that is the normal path, and a shutdown racing an admission is an
// ordinary thing to get wrong. Refusing from the moment Stop begins, and
// setting that flag under the same lock Stop takes its snapshot under, closes
// the window rather than narrowing it.
func (m *Mesh) addActors(specs ...actorSpec) error {
	for _, s := range specs {
		if err := s.bic.Validate(); err != nil {
			return fmt.Errorf("mesh: actor %q: %w", s.name, err)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopping || m.stopped {
		return fmt.Errorf("mesh: stopping; %s would have no goroutine to read its inbox", specs[0].bic)
	}
	batch := make(map[iso20022.BIC]bool, len(specs))
	for _, s := range specs {
		if _, dup := m.actors[s.bic]; dup || batch[s.bic] {
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

// joinRoster gives every member bank in the participant roster an actor, in one
// batch.
//
// The roster read happens OUTSIDE m.mu, because it is store I/O and nothing else
// may be blocked on the mesh while it runs. The whole roster is then registered
// in one batch, so a bank the mesh cannot route to leaves the mesh as it found
// it — see addActors on why all-or-none is the roster's shape and not a
// convenience.
//
// It MERGES into the bank index rather than assigning it, which matters for one
// interleaving and not for the ordinary case. Both callers run against an empty
// index — Start on a new mesh, JoinRoster on one ForgetBanks has just emptied —
// so on any sequential path the two are the same. What they are not the same
// about is a bank admitted CONCURRENTLY: AddBank commits its actor and its index
// entry between the roster read above and this write, and an assignment would
// silently drop that entry while leaving its actor running. The result was an
// actor no index named, which nothing could then reach and — before ForgetBanks
// stopped forgetting by index — nothing could remove either.
//
// Nothing else is protected by that, and it is worth being exact: a reset racing
// an admission is still a mess, and can still refuse the admission (its BIC is
// taken by the roster read) or fail this call (the roster now holds a BIC the
// admission already registered). What the merge removes is the SILENT outcome —
// a bank that answers every read and carries no payment, with nothing anywhere
// saying so. api.Server.resetMu does not cover POST /members, and making it do
// so would serialise admission behind every reset for a race this closes.
func (m *Mesh) joinRoster(ctx context.Context) error {
	if m.net == nil {
		return nil
	}
	ps, err := m.net.ListParticipants(ctx)
	if err != nil {
		return fmt.Errorf("mesh: reading the participant roster: %w", err)
	}
	specs := make([]actorSpec, 0, len(ps))
	banks := make(map[payment.ParticipantID]*bank, len(ps))
	for _, p := range ps {
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
// state is reachable, because AddBank writes the actor and the index entry under
// separate locks from a different goroutine than joinRoster. Reading the actor
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

// AddBank gives one member bank an actor, and is how a bank that joined after
// Start becomes reachable.
//
// Start reads the roster ONCE, which covers every bank that existed when it ran.
// Admission is the other door: a bank admitted at runtime — the central bank's
// POST /members — has a participant row, a chart of accounts and a reserve
// account, and no actor. A bank with no actor is not a slow bank, it is an
// unreachable one: Mesh.Submit refuses its customers' instructions outright, and
// every pacs.008 addressed to it comes back RC01 from the clearing house,
// because there is nothing in the routing table under its BIC. So the handler
// that admits a bank is the handler that registers it, in that order, and
// api.Server.handleAddParticipant is where that happens.
//
// It refuses a BIC some other actor already answers to, for addActors' reason
// and with the consequence that matters here: two participants on one address
// are not two banks in a mesh, they are one routing-table entry and one
// goroutine reading an inbox nobody can address. The refusal reaches the
// operator as the failure of the admission that caused it.
//
// It is the caller's job to notice. There is no way for this to be retried
// later: a bank the mesh refused stays in the roster and stays unreachable until
// the process restarts and Start reads the roster again — at which point Start
// refuses the whole roster instead. See api's handleAddParticipant, which says
// so in the response.
func (m *Mesh) AddBank(p *payment.Participant) error {
	if m.net == nil {
		return errors.New("mesh: no network, so there are no member banks to give actors to")
	}
	b := &bank{m: m, ops: m.net, bic: p.BIC, pid: p.ID}
	// The actor first, so that a BIC this mesh refuses leaves the bank index as
	// it found it — the same ordering, for the same reason, as Start's.
	if err := m.addActor(p.BIC, p.Name, b.handle); err != nil {
		return err
	}
	m.mu.Lock()
	m.banks[p.ID] = b
	m.mu.Unlock()
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
func (m *Mesh) Submit(ctx context.Context, req payment.InitiatePaymentRequest) (payment.Payment, error) {
	scheme, ok := m.net.Scheme(req.Scheme)
	if !ok {
		return payment.Payment{}, fmt.Errorf("mesh: no scheme %q, so no bank submits it: %w", req.Scheme, payment.ErrSchemeNotFound)
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

// Return sends a settled payment back: the R-transaction, and the last of this
// system's four flows.
//
// It is Submit's and CloseCycle's third sibling — synchronous, on the caller's
// goroutine, sending only after the returning bank's half has run — because a
// return arrives from outside the mesh in the same way both of those do. An
// operator (or api's POST /payments/{payid}/return) asks for it; no inbox is involved.
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
// Not with a payment, deliberately. The returning bank's half posts nothing and
// decides nothing beyond whether there is a settled payment to return at all,
// so the Payment it would hand back is the one the caller could already read —
// unchanged, still Settled. What the caller actually wants to know happens
// later, at the settlement agent, and arrives as a message. Submit returns an
// Initiated payment because it CREATED one; there is no such value here, and
// inventing one by re-reading the row after the send would be a race dressed up
// as a result.
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
