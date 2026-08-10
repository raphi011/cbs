package wire

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/raphi011/cbs/iso20022"
)

// ErrUnknownBIC is a send to a BIC no actor answers to. The mesh turns it into
// RC01 on the wire — BankIdentifierIncorrect — which is exactly what it is.
var ErrUnknownBIC = errors.New("mesh: no actor for this BIC")

// ErrAddressTaken is a claim on a BIC some other actor already answers to. See
// AddActors for why it is refused rather than absorbed.
//
// It is a statement about CONNECTIVITY and nothing else, which is the whole of
// what this package's authority over an address amounts to. Whether a bank is a
// MEMBER is a different question, asked one institution over, of the clearing
// house's roster.
//
// The sentence carries the "mesh:" prefix every message in this system carries.
// This package is the mesh's transport, not a second system.
var ErrAddressTaken = errors.New("mesh: another actor already answers to this BIC")

// Handler is what an actor does with a message: bytes in, an error out if it
// could not be dealt with at all.
//
// The signature carries the sender separately from the bytes because an
// unparseable message hides its own header — see item.
type Handler func(ctx context.Context, from iso20022.BIC, raw []byte) error

// Actor is one institution: an inbox, a goroutine reading it, and what it does
// with what it reads.
//
// One goroutine per actor, and only that goroutine ever runs the actor's
// handler. That is what makes an institution's own state safe to touch inside a
// handler without a lock of its own, and it is why the queue rather than the
// handler is the concurrent object here.
type Actor struct {
	bic    iso20022.BIC
	name   string
	q      *queue
	handle Handler
	// done is closed when the actor's goroutine has returned. Stop waits on
	// these, which is what makes "stopped" mean "no handler is still running"
	// rather than "no more messages will be picked up".
	done chan struct{}
}

// BIC is the address this actor answers to.
func (a *Actor) BIC() iso20022.BIC { return a.bic }

// SetHandler replaces what this actor does with a message.
//
// It is for tests that plant a handler on an actor the bus already holds, and
// it must be called BEFORE Start: the field is read on the actor's own
// goroutine with no lock, which is what makes a handler's own state safe to
// touch, and a write racing that read is a data race rather than a late
// binding.
func (a *Actor) SetHandler(h Handler) { a.handle = h }

// ActorSpec is an actor before it exists: who it is and what it does.
type ActorSpec struct {
	BIC    iso20022.BIC
	Name   string
	Handle Handler
}

// ClaimResult is what stood in the way of a claim on an address, if anything.
//
// Four answers rather than an error and a bool, because the caller has
// different advice for each of the last two and no way to tell them apart from
// a sentence. See Bus.Claim.
type ClaimResult int

const (
	// Reserved: the address was free and is now the caller's.
	Reserved ClaimResult = iota
	// Redriving: an actor the caller vouched for already answers to it, and
	// nothing was reserved.
	Redriving
	// HeldByAnotherClaim: another Claim holds the address and has not
	// registered its actor yet. Nobody answers to it, so the caller's advice is
	// to wait rather than to choose another address.
	HeldByAnotherClaim
	// HeldByAnotherActor: an actor the caller did not vouch for answers to it.
	HeldByAnotherActor
)

// Bus is the transport: a set of actors, each with an unbounded inbox and a
// goroutine reading it, and one send that every message passes through.
//
// It carries BYTES and routes them by BIC. Nothing here parses a message, and
// nothing here knows what an institution is: which actor plays a bank and which
// plays the settlement agent is the mesh's question, and the answer never
// reaches this package.
//
// See the package doc for what this transport deliberately is not.
type Bus struct {
	log *slog.Logger

	// observe, if non-nil, sees every message an actor is about to handle: who
	// it is for, who sent it, and the bytes. It is the transport's one
	// observation point.
	//
	// It is written by New and read only by actor goroutines, which is what
	// makes it safe without a lock: until Start runs, no goroutine exists that
	// could read it, and after New nothing writes it.
	observe func(to, from iso20022.BIC, raw []byte)

	// ctx is the lifetime of the bus, not of any one call: every handler is
	// invoked with it, and Stop cancels it once the actors have joined. It is
	// written once, in Start, before any goroutine exists that could read it.
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	actors map[iso20022.BIC]*Actor
	// reserved is the addresses a Claim has taken and not yet registered an
	// actor for.
	//
	// It is a set beside the actor table rather than an entry in it because the
	// claim has to be made BEFORE the actor exists — a joining institution's
	// handler is bound to its own identity, which its caller is still writing.
	// Everything that hands out an address consults it, and Register clears it
	// in the same critical section that inserts the actor, so there is no
	// instant at which the address is free between the two.
	reserved map[iso20022.BIC]bool
	inFlight int
	// quiet is closed when inFlight reaches zero and replaced when it leaves
	// zero. A channel rather than a sync.Cond because Drain must also wake on
	// a cancelled context, and Cond cannot select.
	quiet chan struct{}
	dead  []error
	// started, stopping and stopped are three states rather than two because
	// the middle one is reachable and is not the same as either neighbour: Stop
	// has closed the inboxes and taken its snapshot but has not joined yet, and
	// a Stop that times out leaves the bus sitting there. stopping is what
	// AddActors refuses on — waiting for stopped would let an actor be
	// registered into a shutdown that will never close or join it.
	started  bool
	stopping bool
	stopped  bool
	// busy names the actors currently working on a message and WHAT they are
	// doing with it, so a Drain that times out can say which one is stuck
	// instead of reporting a bare deadline.
	//
	// The phase matters because there are two of them and only one is the
	// actor's own code. An actor parked in observe has not entered its handler
	// at all — it is being held by whoever installed the hook — and reporting
	// that as "in a handler" would send a reader looking for a bug in the wrong
	// half of the system, at the exact moment they are debugging the hook. See
	// dispatch.
	busy map[iso20022.BIC]string
}

// New builds an empty bus with no actors and no goroutines running yet.
//
// observe may be nil. When it is not, it is called on the receiving actor's
// goroutine with every message that actor is about to handle, and it may BLOCK
// — which is what lets a caller hold a conversation still mid-flight. What it
// must not do is wait for anything that itself waits for this bus.
func New(log *slog.Logger, observe func(to, from iso20022.BIC, raw []byte)) *Bus {
	if log == nil {
		log = slog.Default()
	}
	// A bus with nothing in flight starts quiet, so quiet starts closed. The
	// alternative — an open channel replaced on the first leave — would make
	// Drain block on a bus that had never sent anything.
	quiet := make(chan struct{})
	close(quiet)

	return &Bus{
		log:      log,
		observe:  observe,
		actors:   make(map[iso20022.BIC]*Actor),
		reserved: make(map[iso20022.BIC]bool),
		busy:     make(map[iso20022.BIC]string),
		quiet:    quiet,
	}
}

// AddActor registers one actor. See AddActors, which does the work.
func (b *Bus) AddActor(bic iso20022.BIC, name string, h Handler) error {
	return b.AddActors(ActorSpec{BIC: bic, Name: name, Handle: h})
}

// AddActors registers a batch of actors under their BICs — all of them or none
// — and, if the bus is already running, starts their goroutines at once.
//
// Registering into a running bus is the normal case: a bank admitted over HTTP
// joins a mesh whose other actors are already reading their inboxes, and it has
// to be reachable before it can send its own application.
//
// All or none, because the batch is a roster. A half-registered batch would
// leave the bus holding some banks and not others, unstarted, and a caller that
// fixed the roster and retried would collide with the actors the failed attempt
// created. See TestStartRefusesTwoParticipantsWithOneBIC in the mesh package.
//
// Two actors under one BIC is refused, whether the clash is with an actor
// already registered or between two members of one batch: the map would keep the
// second and drop the first, whose goroutine would then read an inbox nothing
// could address.
//
// A bus that is STOPPED refuses, because its actors' goroutines have returned —
// a new actor would have an open inbox and nobody reading it, so a send would
// report success, count a message in flight, and hang the next Drain out to its
// deadline. A black hole that answers "sent" is worse than a refusal.
//
// A bus that is STOPPING refuses for a sharper reason. Stop snapshots the
// actors and closes their inboxes, then joins them; an actor registered after
// that snapshot is in neither list, so its inbox is never closed and its
// goroutine never joined — a permanent leak plus the same black hole. Refusing
// from the moment Stop begins, under the lock Stop takes its snapshot under,
// closes the window rather than narrowing it.
//
// A BIC a Claim has RESERVED is refused too: an address claimed by a
// registration in flight is one a second registration cannot have. See
// Bus.reserved.
func (b *Bus) AddActors(specs ...ActorSpec) error {
	for _, s := range specs {
		if err := s.BIC.Validate(); err != nil {
			return fmt.Errorf("mesh: actor %q: %w", s.Name, err)
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.addLocked(specs...)
}

// Register consumes an address's reservation and registers its actor in ONE
// critical section.
//
// The order matters in both directions. Clearing the reservation FIRST is what
// stops the duplicate check below refusing the very address this claim took;
// doing both under one lock is what stops a second Claim taking the address out
// from under a caller whose own work has already committed. A gap between them
// is the orphan this package's reservation exists to remove, reintroduced one
// lock apart.
//
// Its caller may hold a lock of its own across this call — the mesh does, to
// index the bank beside its actor — and that is the only nesting in this system:
// the bus never calls back out, so nothing ever takes these two the other way
// round.
func (b *Bus) Register(spec ActorSpec) error {
	if err := spec.BIC.Validate(); err != nil {
		return fmt.Errorf("mesh: actor %q: %w", spec.Name, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.reserved, spec.BIC)
	return b.addLocked(spec)
}

// addLocked is AddActors with the lock already held and the BICs already
// validated.
func (b *Bus) addLocked(specs ...ActorSpec) error {
	if b.stopping || b.stopped {
		return fmt.Errorf("mesh: stopping; %s would have no goroutine to read its inbox", specs[0].BIC)
	}
	batch := make(map[iso20022.BIC]bool, len(specs))
	for _, s := range specs {
		if _, dup := b.actors[s.BIC]; dup || batch[s.BIC] || b.reserved[s.BIC] {
			return fmt.Errorf("%w: two actors for %s (%s)", ErrAddressTaken, s.BIC, s.Name)
		}
		batch[s.BIC] = true
	}
	for _, s := range specs {
		a := &Actor{bic: s.BIC, name: s.Name, q: newQueue(), handle: s.Handle, done: make(chan struct{})}
		b.actors[s.BIC] = a
		if b.started {
			go b.run(b.ctx, a)
		}
	}
	return nil
}

// Claim reserves an address for a registration that is about to run, or reports
// what stood in the way.
//
// redrivable is the caller's answer to "is whatever answers to this address
// already mine?", asked because the bus cannot know: an actor is an address and
// a handler, and which of them the caller owns is the caller's own bookkeeping.
// When it is true and an actor is already there, nothing is reserved and the
// result is Redriving — the caller is re-driving work it interrupted.
//
// It reads no store and holds only this bus's lock, which is what lets a caller
// hold its own across the call. The interval that opens AFTER it returns is the
// caller's to serialise; this reservation only makes the address itself
// unambiguous.
func (b *Bus) Claim(bic iso20022.BIC, redrivable bool) (ClaimResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopping || b.stopped {
		return 0, errors.New("mesh: stopping; an actor registered now would have no goroutine to read its inbox")
	}
	if b.reserved[bic] {
		return HeldByAnotherClaim, nil
	}
	if _, taken := b.actors[bic]; taken {
		if redrivable {
			return Redriving, nil
		}
		return HeldByAnotherActor, nil
	}
	b.reserved[bic] = true
	return Reserved, nil
}

// Release gives a reservation back. See Bus.reserved.
func (b *Bus) Release(bic iso20022.BIC) {
	b.mu.Lock()
	delete(b.reserved, bic)
	b.mu.Unlock()
}

// Actor is the actor answering to a BIC, if any.
func (b *Bus) Actor(bic iso20022.BIC) (*Actor, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a, ok := b.actors[bic]
	return a, ok
}

// Has reports whether any actor answers to a BIC.
func (b *Bus) Has(bic iso20022.BIC) bool {
	_, ok := b.Actor(bic)
	return ok
}

// Addresses is every BIC this bus routes to, in no particular order.
func (b *Bus) Addresses() []iso20022.BIC {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Collect(maps.Keys(b.actors))
}

// Startable reports whether Start would be accepted, without starting.
//
// It exists because its caller has store I/O to do between deciding to start
// and starting — reading the roster that says which actors there are — and a
// caller that discovered "already started" only afterwards would have done that
// work for nothing. Start re-checks under the lock, so this is a fast refusal
// and not the decision.
func (b *Bus) Startable() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.startableLocked()
}

func (b *Bus) startableLocked() error {
	switch {
	case b.started:
		return errors.New("mesh: already started")
	case b.stopped:
		return errors.New("mesh: stopped meshes do not restart; build a new one")
	}
	return nil
}

// Start launches one goroutine per actor.
//
// The context is the bus's LIFETIME, not one call's: every handler runs with
// it, and Stop cancels it. Passing a request-scoped context here would cancel
// the whole transport when that request finished.
//
// A bus is started once. Restarting one that has been stopped is refused rather
// than half-supported: the actors' done channels have already been closed, so a
// second run of the same goroutines could not be waited for, and a Stop that
// cannot wait is the failure Stop exists to avoid.
func (b *Bus) Start(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.startableLocked(); err != nil {
		return err
	}
	b.ctx, b.cancel = context.WithCancel(ctx)
	b.started = true
	for _, a := range b.actors {
		go b.run(b.ctx, a)
	}
	return nil
}

// Forget closes, joins and removes every actor except the ones named, and
// reports which one would not let go.
//
// # It expects a quiet bus, and says so rather than assuming it
//
// Closing an inbox does not discard what is in it: pop hands over everything
// already queued before it reports the queue closed, so a message in flight
// would still be HANDLED — against a store its caller may be about to truncate.
// Its caller drains first, the same ordering Stop demands. The wait for each
// goroutine is what makes "forgotten" mean "no handler is still running".
//
// It is not itself a Drain and does not take the dead letters: anything a
// handler could not deal with on the way out stays for the next Drain, which is
// where a dead letter belongs.
//
// # A timeout leaves the actors joinable
//
// The deletions happen AFTER every goroutine has returned. Stop takes its
// snapshot from the actor table, so an actor deleted before it was joined would
// be a goroutine nothing could ever wait for — a permanent leak, and, if the
// caller retried, a store truncated underneath a live handler.
//
// So a Forget that times out leaves the bus as Stop leaves one that times out:
// inboxes closed, actors still in the table, nothing half torn down. Its caller
// may retry — closing a closed queue is a no-op — or give up and Stop.
func (b *Bus) Forget(ctx context.Context, keep ...iso20022.BIC) error {
	b.mu.Lock()
	if b.stopping || b.stopped {
		b.mu.Unlock()
		return errors.New("mesh: stopping; its actors are being joined by Stop, not forgotten")
	}
	gone := make([]*Actor, 0, len(b.actors))
	for bic, a := range b.actors {
		if slices.Contains(keep, bic) {
			continue
		}
		gone = append(gone, a)
		a.q.close()
	}
	b.mu.Unlock()

	for _, a := range gone {
		select {
		case <-a.done:
		case <-ctx.Done():
			return fmt.Errorf("mesh: forgetting actors (%s): %w", b.Stuck(), ctx.Err())
		}
	}

	b.mu.Lock()
	for _, a := range gone {
		delete(b.actors, a.bic)
	}
	b.mu.Unlock()
	return nil
}

// Stop closes every inbox, waits for the actors to finish what they are already
// doing, and returns the dead letters they produced, joined.
//
// It waits rather than cancelling, because its caller's next move is typically
// to tear the store down: a handler still inside a unit of work when the tables
// are truncated is the shutdown bug this ordering prevents. A handler that will
// not return times out and is NAMED, as in Drain.
//
// # Queued messages are delivered; chains are cut
//
// Nothing already queued is lost: closing an inbox stops it accepting NEW
// messages, and pop hands over everything already in it (see queue.pop).
//
// Chains, though, are CUT. Every inbox is closed in ONE step — under the bus
// lock, together with the snapshot of who the actors are — before any actor is
// joined, so a handler still running during shutdown cannot reach anybody at
// all. Its send is refused, and the refusal becomes a dead letter.
//
// Which is why Stop returns them: a shutdown that ended a conversation and said
// nothing would be the silent failure Drain's dead letters exist to prevent.
//
// # The deadline, and why every caller must Drain first
//
// The context bounds the handler in flight PLUS the whole depth of every inbox
// at the moment of the close. It is not a bound on one handler.
//
// Drain first, then Stop — and not merely to make the deadline predictable. It
// is the ONLY way a chain in flight completes at all: Stop cuts whatever is
// mid-conversation, so a shutdown or reset that has not drained ends payments
// halfway, with the debtor's bank debited and the pacs.002 that would have told
// it so never sent.
//
// # A Stop that times out leaves the bus running
//
// It names the actor that would not let go, hands back the dead letters it has,
// and deliberately does NOT cancel the bus context or mark the bus stopped:
// goroutines are still using both, and a half-torn-down transport is worse than
// one that plainly refused to stop. Call it again once the handler has come back
// — the inboxes are already closed, so a retry only re-joins.
func (b *Bus) Stop(ctx context.Context) error {
	b.mu.Lock()
	if !b.started {
		// Never started, or already stopped: there is nothing to join, and any
		// dead letters were taken by the Stop or the Drain that preceded this.
		b.mu.Unlock()
		return nil
	}
	// stopping, the snapshot and the closes are one critical section. The
	// snapshot is the set of actors this call will close and join, so an
	// actor registered after it would be neither — a goroutine nothing ever
	// joins, reading an inbox nothing ever closes. AddActors refuses from this
	// moment on, and it cannot slip in beside this because it takes the lock
	// too.
	//
	// Closing here rather than after the unlock also makes "a handler in
	// shutdown can reach nobody" true rather than nearly true: with the closes
	// outside the lock, a send racing the loop could still land in an inbox the
	// loop had not reached yet.
	b.stopping = true
	actors := make([]*Actor, 0, len(b.actors))
	for _, a := range b.actors {
		actors = append(actors, a)
		a.q.close()
	}
	cancel := b.cancel
	b.mu.Unlock()

	for _, a := range actors {
		select {
		case <-a.done:
		case <-ctx.Done():
			return errors.Join(
				fmt.Errorf("mesh: stopping (%s): %w", b.Stuck(), ctx.Err()),
				b.TakeDeadLetters(),
			)
		}
	}

	b.mu.Lock()
	b.started = false
	b.stopped = true
	b.mu.Unlock()
	cancel()

	// The dead letters last, after every actor has returned, so that a handler
	// whose send was refused by the shutdown itself is included rather than
	// raced.
	return b.TakeDeadLetters()
}

// run is one actor's goroutine: pop, handle, repeat, until the inbox is closed
// and empty.
func (b *Bus) run(ctx context.Context, a *Actor) {
	defer close(a.done)
	for {
		it, ok := a.q.pop()
		if !ok {
			return
		}
		b.dispatch(ctx, a, it)
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
func (b *Bus) dispatch(ctx context.Context, a *Actor, it item) {
	var err error
	defer func() { b.finish(a, err) }()
	// The observation phase is recorded separately, and only when there is
	// something to observe: an observer that blocks holds this actor, and Stuck
	// must be able to say that is what is holding it. See Bus.busy.
	if b.observe != nil {
		b.setBusy(a.bic, "in Observe")
		b.observe(a.bic, it.from, it.raw)
	}
	b.setBusy(a.bic, "in a handler")
	err = a.handle(WithActor(ctx, a.bic), it.from, it.raw)
}

// The context every handler runs with carries WHO IS ACTING, and that is the
// only thing about the transport a layer below it can see.
//
// It is there so that a unit of work can be attributed to the institution that
// opened it. Nothing in the domain reads it — payment neither knows nor cares —
// but the book recorder the mesh tests against does.
//
// A context value rather than an argument because it has to survive the trip
// through payment: a handler calls into the domain, which opens the unit of work
// several frames down, and no signature on that path has anywhere to put it.
type actorContextKey struct{}

// WithActor marks a context as belonging to one institution's work. dispatch
// does it for every message; the mesh does it by hand for the halves that run on
// a caller's goroutine.
func WithActor(ctx context.Context, bic iso20022.BIC) context.Context {
	return context.WithValue(ctx, actorContextKey{}, bic)
}

// ActorOf reports which institution's work this context belongs to, if any. A
// context with no actor is work no institution did: a seed, a test fixture, an
// operator's cut-off.
func ActorOf(ctx context.Context) (iso20022.BIC, bool) {
	bic, ok := ctx.Value(actorContextKey{}).(iso20022.BIC)
	return bic, ok
}

// finish ends one message: its dead letter, the actor's busy flag and the
// decrement, in ONE critical section.
//
// One, and not three, because Drain wakes on the decrement and then reads the
// dead letters under this same lock. Recorded afterwards — even one statement
// afterwards — and a Drain woken by the close could take the errors before this
// goroutine had added them, and report a clean bus over a failed handler. That
// would not be a test that fails sometimes; it would be a test that passes
// almost always, which is worse — and that is measured, not assumed: with the
// record moved into a second critical section after the decrement, 200 runs of
// TestDrainReportsHandlerErrors could not tell the difference, and only a 5ms
// delay planted in the gap made it fail. A window no test can see is a window
// that has to be closed by construction, and holding the lock across all three
// closes it.
//
// Logging happens after the unlock. A log handler is arbitrary code and can be
// slow; nothing else here should wait behind it.
func (b *Bus) finish(a *Actor, err error) {
	b.mu.Lock()
	delete(b.busy, a.bic)
	if err != nil {
		b.dead = append(b.dead, fmt.Errorf("%s (%s): %w", a.bic, a.name, err))
	}
	b.leaveLocked()
	b.mu.Unlock()

	if err != nil {
		b.log.Error("mesh: dead letter", "actor", a.bic, "name", a.name, "error", err)
	}
}

// enterLocked counts one message in. The caller holds the bus lock — see Send
// for why that is not an accident.
func (b *Bus) enterLocked() {
	if b.inFlight == 0 {
		b.quiet = make(chan struct{})
	}
	b.inFlight++
}

// leaveLocked counts one message out, waking any Drain when the last one goes.
// The caller holds the bus lock.
func (b *Bus) leaveLocked() {
	b.inFlight--
	if b.inFlight == 0 {
		close(b.quiet)
	}
}

func (b *Bus) leave() {
	b.mu.Lock()
	b.leaveLocked()
	b.mu.Unlock()
}

func (b *Bus) setBusy(bic iso20022.BIC, phase string) {
	b.mu.Lock()
	b.busy[bic] = phase
	b.mu.Unlock()
}

// Stuck names what the bus is holding: the actors inside a handler, and the
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
// It takes the bus lock and then each queue's own lock, which is the same order
// Send takes them in and the only order anything in this package takes them in.
func (b *Bus) Stuck() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	names := make([]string, 0, len(b.busy)+len(b.actors))
	for bic, phase := range b.busy {
		names = append(names, string(bic)+" "+phase)
	}
	for bic, a := range b.actors {
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

// Send is the single choke point. Every message in this system passes through
// here.
//
// It is one method and not a Transport interface: swapping this body for an
// HTTP POST is a contained change, and until someone wants that there is nothing
// for an interface to abstract over.
//
// It enqueues BYTES, and takes them. Structs never cross an actor boundary — if
// two actors exchanged a parsed document the message format would be decoration
// on a function call, malformed input would stop being a reachable failure mode,
// and the FF01 path would be untestable. That is why the marshalling is the
// mesh's and the queue is this package's.
//
// It never blocks, because the inbox it pushes into is unbounded. That is what
// lets two actors message each other: the clearing house sends to a bank while
// that bank is sending to the clearing house, and any fixed buffer between them
// wedges that pair. See TestMutuallyMessagingActorsDoNotDeadlock.
//
// # Counting in and enqueueing are one step
//
// The lookup, the increment and the push are one critical section, and what that
// buys is not an ordering but that the intermediate state is UNOBSERVABLE.
// Everything else that touches the counter takes the bus lock, so nobody can see
// a queue holding a message the counter does not, whichever way round the two
// statements are written. A later edit that swapped them would still be correct,
// which is the point: the invariant does not rest on anyone remembering the
// order.
//
// The increment-before-push order is kept anyway, and it is sufficient on its
// own: quiet is open exactly while inFlight > 0, a message reaches a queue only
// after its own increment, and is decremented only after it has been popped.
//
// The inversion IS broken, and no test catches it even with a delay planted in
// the gap — every message is sent from inside a handler whose own message is
// still counted in flight, and Send returns before that handler does, so the
// parent's count covers the child's gap. What is left uncovered is a send from a
// goroutine no in-flight message covers, racing a concurrent Drain, which is
// exactly the case Drain's own doc declines to promise anything about.
func (b *Bus) Send(from, to iso20022.BIC, raw []byte) error {
	b.mu.Lock()
	a, ok := b.actors[to]
	if !ok {
		b.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrUnknownBIC, to)
	}
	b.enterLocked()
	accepted := a.q.push(item{from: from, raw: raw})
	b.mu.Unlock()

	if !accepted {
		// The actor is stopped and will never read this. Give the counter back
		// — a message counted in and never delivered is a Drain that blocks for
		// ever. See TestSendAfterStopIsRefusedAndLeavesDrainQuiet.
		b.leave()
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
// bus reports nothing, so a test that asserts on a failure does not hand it to
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
// deadlock — no lock is held across either — but both wedge.
func (b *Bus) Drain(ctx context.Context) error {
	b.mu.Lock()
	ch := b.quiet
	b.mu.Unlock()

	select {
	case <-ch:
	case <-ctx.Done():
		return errors.Join(
			fmt.Errorf("mesh: draining (%s): %w", b.Stuck(), ctx.Err()),
			b.TakeDeadLetters(),
		)
	}
	return b.TakeDeadLetters()
}

// TakeDeadLetters returns the errors no handler had anyone to give back, and
// clears them.
//
// Taken rather than accumulated, so that one test's failure is not handed to
// the next drain — and so that a caller which has read them has really dealt
// with them.
func (b *Bus) TakeDeadLetters() error {
	b.mu.Lock()
	dead := b.dead
	b.dead = nil
	b.mu.Unlock()
	return errors.Join(dead...)
}
