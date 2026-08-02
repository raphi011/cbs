package mesh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	// is for, who sent it, and the bytes.
	//
	// It is a seam for THIS package's tests and for nothing else, and it earns
	// its place because some of what this mesh does leaves no other trace. An
	// FF01 names no payment — it cannot, the file that provoked it did not parse
	// — so a test that could not see the message could not tell an answered
	// malformed envelope from a swallowed one. Nor could one tell a submission
	// that sent nothing from a submission whose message went somewhere else.
	//
	// It is written before Start and read only by actor goroutines, which is what
	// makes it safe without a lock: until Start runs, no goroutine exists that
	// could read it, and after Start nothing writes it.
	tap func(to, from iso20022.BIC, raw []byte)

	// msgSeq numbers the messages this mesh emits. See nextMsgID.
	msgSeq atomic.Uint64

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
	// busy names the actors currently inside a handler, so a Drain that times
	// out can say which one is stuck instead of reporting a bare deadline.
	busy map[iso20022.BIC]bool
}

// New builds a mesh over a payment network, with an actor for each of the two
// configured institutions and no goroutines running yet.
//
// The member banks are NOT created here: they come from the participant roster,
// which is a store read, and a constructor that did I/O could not be called
// before the store was ready. Start reads the roster; addActor covers the banks
// that join after that, which in this repository is all of them, because the
// process starts the mesh before it seeds.
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
		actors: make(map[iso20022.BIC]*actor),
		banks:  make(map[payment.ParticipantID]*bank),
		busy:   make(map[iso20022.BIC]bool),
		quiet:  quiet,
	}
	// The clearing house gets its behaviour here, because it has no store row to
	// wait for: it IS the configuration. A mesh with no network keeps the
	// refusing placeholder instead — clearing is a thing you do to payments, and
	// there are none.
	clearing := unhandled("clearing house")
	if net != nil {
		clearing = (&csm{m: m, ops: net, bic: cfg.ClearingHouseBIC}).handle
	}
	if err := m.addActor(cfg.ClearingHouseBIC, "clearing house", clearing); err != nil {
		return nil, err
	}
	// The central bank keeps the placeholder either way. Settlement is Task 12's,
	// and an actor that answered a settlement instruction with silence would make
	// the missing half look like a working one.
	if err := m.addActor(cfg.CentralBankBIC, "central bank", unhandled("central bank")); err != nil {
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
// Two actors still have it after Task 10. The CENTRAL BANK has it always:
// settlement is Task 12's, and there is nothing yet for it to do. The CLEARING
// HOUSE has it only on a mesh built over no network — the transport's own tests
// — because clearing is something you do to payments and a mesh with no store
// has none.
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
// Registering into a running mesh is the normal case, not a convenience: the
// process starts the mesh before it seeds the participants, so every member
// bank joins a mesh whose other actors are already reading their inboxes. See
// TestAnActorAddedAfterStartReceives.
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
// not hypothetical: Task 16 starts the mesh before it seeds, so registering
// actors into a running mesh is the normal path, and a shutdown racing a seed
// is an ordinary thing to get wrong. Refusing from the moment Stop begins, and
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
			return fmt.Errorf("mesh: two actors for %s (%s)", s.bic, s.name)
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

	// The roster read happens outside the lock, because it is store I/O and
	// nothing else may be blocked on the mesh while it runs. The whole roster
	// is then registered in one batch, so a bank the mesh cannot route to
	// leaves the mesh as it found it.
	if m.net != nil {
		ps, err := m.net.ListParticipants(ctx)
		if err != nil {
			return fmt.Errorf("mesh: reading the participant roster: %w", err)
		}
		specs := make([]actorSpec, 0, len(ps))
		banks := make(map[payment.ParticipantID]*bank, len(ps))
		for _, p := range ps {
			b := &bank{m: m, ops: m.net, bic: p.BIC}
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
		m.banks = banks
		m.mu.Unlock()
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
// # The deadline, and why Tasks 14 and 16 must Drain first
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
// That is an instruction for Task 14's Server.Reset, which must drain before it
// truncates, and for Task 16's shutdown path. Neither does today — api holds no
// mesh at all yet.
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
	m.setBusy(a.bic)
	if m.tap != nil {
		m.tap(a.bic, it.from, it.raw)
	}
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

func (m *Mesh) setBusy(bic iso20022.BIC) {
	m.mu.Lock()
	m.busy[bic] = true
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
	for bic := range m.busy {
		names = append(names, string(bic)+" in a handler")
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
// a message. That is the whole difference the mesh makes, and it is why Task 14
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
