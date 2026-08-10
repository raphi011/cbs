package wire

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raphi011/cbs/iso20022"
)

// newTestBus builds a bus of three actors with no-op handlers, not started.
//
// The messages below are arbitrary BYTES and not marshalled documents, which is
// the whole point of testing this layer on its own: a transport test that needed
// a valid pacs.002 to run would be asserting on the marshaller.
func newTestBus(t *testing.T) *Bus {
	t.Helper()
	return newTestBusObserving(t, nil)
}

func newTestBusObserving(t *testing.T, observe func(to, from iso20022.BIC, raw []byte)) *Bus {
	t.Helper()
	b := New(slog.New(slog.DiscardHandler), observe)
	for _, bic := range []iso20022.BIC{"AAAADEFFXXX", "BBBBDEFFXXX", "CCCCDEFFXXX"} {
		if err := b.AddActor(bic, string(bic), func(context.Context, iso20022.BIC, []byte) error { return nil }); err != nil {
			t.Fatalf("AddActor %s: %v", bic, err)
		}
	}
	return b
}

func drainCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// A message that begets a message must keep Drain blocked. This is the whole
// correctness argument for the in-flight counter: increment before enqueue,
// decrement only after the handler that consumed it has returned, so a chain
// never shows as quiet mid-flight.
func TestDrainWaitsForMessagesThatBegetMessages(t *testing.T) {
	b := newTestBus(t)
	seen := &recorder{}

	b.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		seen.record("A")
		return b.Send("AAAADEFFXXX", "BBBBDEFFXXX", []byte("hop-2"))
	}
	b.actors["BBBBDEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		seen.record("B")
		return b.Send("BBBBDEFFXXX", "CCCCDEFFXXX", []byte("hop-3"))
	}
	b.actors["CCCCDEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		seen.record("C")
		return nil
	}

	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = b.Stop(context.Background()) })

	if err := b.Send("CCCCDEFFXXX", "AAAADEFFXXX", []byte("hop-1")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := b.Drain(drainCtx(t)); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := seen.String(); got != "ABC" {
		t.Fatalf("handlers ran %q, want ABC — Drain returned before the chain finished", got)
	}
}

// recorder is what the three handlers above append to, under a lock of its own.
//
// The lock is not decoration, and it is not there because the race detector
// asked for it: with the bus behaving, the appends are already ordered — each
// handler appends before it sends, and the inbox mutex the next actor pops
// under is a happens-before edge, so `var seen []string` runs clean under -race
// (measured: 1,800 runs at GOMAXPROCS 1 and 8, no report). It is there because
// that ordering is exactly what the test EXISTS to check. A bus that let two
// handlers run at once, or that woke Drain early, would turn those appends into
// a genuine race — and the test would then abort with a race report instead of
// the assertion failure that says which behaviour broke. The lock keeps the
// failure legible.
type recorder struct {
	mu   sync.Mutex
	seen []string
}

func (r *recorder) record(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, s)
}

func (r *recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.seen, "")
}

// Two actors messaging each other must not wedge. This is what the unbounded
// queue buys, and it is the failure a buffered channel would produce.
func TestMutuallyMessagingActorsDoNotDeadlock(t *testing.T) {
	b := newTestBus(t)
	hops := 0
	b.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		if hops >= 50 {
			return nil
		}
		hops++
		return b.Send("AAAADEFFXXX", "BBBBDEFFXXX", []byte("ping"))
	}
	b.actors["BBBBDEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		return b.Send("BBBBDEFFXXX", "AAAADEFFXXX", []byte("pong"))
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = b.Stop(context.Background()) })

	if err := b.Send("CCCCDEFFXXX", "AAAADEFFXXX", []byte("start")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := b.Drain(drainCtx(t)); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

// A handler error with nobody to tell must not vanish. Every draining test above
// this package therefore asserts, for free, that nothing was silently eaten.
func TestDrainReportsHandlerErrors(t *testing.T) {
	b := newTestBus(t)
	boom := errors.New("boom")
	b.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error { return boom }

	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = b.Stop(context.Background()) })

	_ = b.Send("CCCCDEFFXXX", "AAAADEFFXXX", []byte("x"))
	err := b.Drain(drainCtx(t))
	if !errors.Is(err, boom) {
		t.Fatalf("Drain = %v, want the handler's error", err)
	}
	// And it is taken, not accumulated: a second Drain on a quiet bus is clean.
	if err := b.Drain(drainCtx(t)); err != nil {
		t.Fatalf("second Drain = %v, want nil", err)
	}
}

func TestSendToAnUnknownBICIsAnError(t *testing.T) {
	b := newTestBus(t)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = b.Stop(context.Background()) })

	err := b.Send("AAAADEFFXXX", "ZZZZDEFFXXX", []byte("x"))
	if !errors.Is(err, ErrUnknownBIC) {
		t.Fatalf("Send to an unrouted BIC = %v, want ErrUnknownBIC", err)
	}
}

// A wedged handler must report as itself, naming the actor, rather than as
// "test timed out after 10m".
func TestDrainTimesOutNamingTheActor(t *testing.T) {
	b := newTestBus(t)
	release := make(chan struct{})
	b.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		<-release
		return nil
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { close(release); _ = b.Stop(context.Background()) })

	_ = b.Send("CCCCDEFFXXX", "AAAADEFFXXX", []byte("x"))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := b.Drain(ctx)
	if err == nil {
		t.Fatal("Drain returned nil while a handler was still running")
	}
	if !strings.Contains(err.Error(), "AAAADEFFXXX") {
		t.Errorf("Drain error %q does not name the stuck actor", err)
	}
}

// An actor registered after Start still gets its goroutine. This is not a
// convenience: the process starts the mesh before it seeds the participants
// (cmd/server does, deliberately, so that nothing is listening while the seed
// runs), so every member bank's actor is added to a bus that is already running.
func TestAnActorAddedAfterStartReceives(t *testing.T) {
	b := newTestBus(t)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = b.Stop(context.Background()) })

	got := make(chan iso20022.BIC, 1)
	if err := b.AddActor("DDDDDEFFXXX", "D", func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		got <- from
		return nil
	}); err != nil {
		t.Fatalf("AddActor: %v", err)
	}
	if err := b.Send("AAAADEFFXXX", "DDDDDEFFXXX", []byte("late")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := b.Drain(drainCtx(t)); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	select {
	case from := <-got:
		if from != "AAAADEFFXXX" {
			t.Errorf("handler saw sender %q, want AAAADEFFXXX", from)
		}
	default:
		t.Fatal("the actor added after Start never ran; its goroutine was not started")
	}
}

// Two actors under one BIC is a routing table that silently drops one of them.
func TestAddingTwoActorsForOneBICIsAnError(t *testing.T) {
	b := newTestBus(t)
	err := b.AddActor("AAAADEFFXXX", "again", func(context.Context, iso20022.BIC, []byte) error { return nil })
	if !errors.Is(err, ErrAddressTaken) {
		t.Fatalf("AddActor = %v, want ErrAddressTaken", err)
	}
}

// A batch of actors with two on one address registers NOBODY.
//
// Two actors sharing a BIC is a routing table that cannot say which one a
// message is for, and one of the two would end up with a goroutine reading an
// inbox nothing could address. The batch is refused whole rather than stopping
// where the clash was noticed: registering the first and failing on the second
// leaves a half-populated bus that a retry cannot fix, because the retry
// collides with what the failed attempt itself created.
//
// It is provoked HERE and not through the mesh's Start, and the reason is a
// fact about the layer above: a bank's database is named by its address
// (store/sqlite.Set), so two bank rows claiming one BIC are one database, and
// the roster the mesh reads is keyed by address too. There is no roster a mesh
// could read that would present this batch. The guard is still the only thing
// between this transport and an unreachable actor, so it is measured where it
// lives.
func TestActorRegistrationIsAllOrNoneOnAClashingBatch(t *testing.T) {
	b := newTestBus(t)
	nothing := func(context.Context, iso20022.BIC, []byte) error { return nil }
	before := len(b.Addresses())

	err := b.AddActors(
		ActorSpec{BIC: "NORDSESSXXX", Name: "Nordhaven Bank", Handle: nothing},
		ActorSpec{BIC: "BANKDEFFXXX", Name: "Bankhaus Meridian", Handle: nothing},
		ActorSpec{BIC: "NORDSESSXXX", Name: "Nordhaven Bank (again)", Handle: nothing},
	)
	if !errors.Is(err, ErrAddressTaken) {
		t.Fatalf("AddActors = %v, want ErrAddressTaken", err)
	}
	if got := len(b.Addresses()); got != before {
		t.Fatalf("the bus holds %d actors, want the %d it had before the refused batch", got, before)
	}
	for _, bic := range []iso20022.BIC{"NORDSESSXXX", "BANKDEFFXXX"} {
		if b.Has(bic) {
			t.Errorf("%s was registered by a batch that was refused", bic)
		}
	}
}

// A send to a stopped actor must be refused, not accepted and dropped. Send
// counts the message as in flight before it pushes, so an item the queue threw
// away would leave the counter above zero for ever: Drain would block until its
// deadline and report a bus that is doing nothing as busy.
func TestSendAfterStopIsRefusedAndLeavesDrainQuiet(t *testing.T) {
	b := newTestBus(t)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := b.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := b.Send("AAAADEFFXXX", "BBBBDEFFXXX", []byte("x")); err == nil {
		t.Fatal("Send to a stopped bus reported success")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := b.Drain(ctx); err != nil {
		t.Fatalf("Drain after a refused send = %v; the refused message is still counted in flight", err)
	}
}

// Stop waits for the handler that is running when it is called, and then
// returns cleanly. A Stop that returned early would let its caller truncate the
// store underneath a handler that was still writing to it.
//
// The negative half is asserted by waiting: Stop runs in a goroutine and must
// still be blocked while the handler is. This is the one assertion in the
// package that is not deterministic, and it is one-sided — a Stop that did not
// wait returns in microseconds, so a machine wedged for 50 milliseconds makes
// this pass when it should fail, and nothing makes it fail when it should pass.
// The counterfactual confirms it bites: with the join loop removed it fails on
// every run.
func TestStopWaitsForARunningHandler(t *testing.T) {
	b := newTestBus(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	b.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		close(entered)
		<-release
		close(finished)
		return nil
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := b.Send("CCCCDEFFXXX", "AAAADEFFXXX", []byte("x")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	<-entered

	// The context is built here rather than inside the goroutine: drainCtx
	// touches t, and t belongs to the test's own goroutine.
	stopCtx := drainCtx(t)
	stopped := make(chan error, 1)
	go func() { stopped <- b.Stop(stopCtx) }()
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned %v while the handler was still inside its call", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-stopped; err != nil {
		t.Fatalf("Stop: %v", err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("Stop returned before the handler finished")
	}
}

// Stop does not throw away what is already queued. Closing an inbox stops it
// taking NEW messages; everything in it when Stop was called is still handled.
//
// This is the property Stop's deadline has to be sized against: queued messages
// are NOT discarded, so a caller is sizing a context for the whole queue rather
// than for one handler.
func TestStopDeliversWhatIsAlreadyQueued(t *testing.T) {
	b := newTestBus(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var handled atomic.Int32
	b.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		if handled.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := b.Send("CCCCDEFFXXX", "AAAADEFFXXX", []byte("1")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// Only once the first message is inside the handler is the second one
	// certain to be waiting in the inbox rather than already dealt with.
	<-entered
	if err := b.Send("CCCCDEFFXXX", "AAAADEFFXXX", []byte("2")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Closing the inboxes is Stop's first act, and it is done here directly so that
	// it cannot race the handler waking up. From a goroutine beside close(release)
	// it does race, and a test written that way asserts which of the two won rather
	// than what Stop guarantees.
	for _, a := range b.actors {
		a.q.close()
	}
	close(release)

	if err := b.Stop(drainCtx(t)); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := handled.Load(); got != 2 {
		t.Fatalf("the actor delivered %d of the 2 messages; the queued one was dropped when its inbox closed", got)
	}
}

// A shutdown that cuts a conversation says which one. Stop closes every inbox
// before it joins anybody, so a handler still running cannot reach any actor —
// its send is refused, that refusal is its error, and that error is a dead
// letter. A Stop that returned bare nil would make an interrupted chain fail
// silently, which is the whole failure Drain's dead letters exist to prevent.
func TestStopReportsWhatAHandlerCouldNotDoDuringShutdown(t *testing.T) {
	b := newTestBus(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	b.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		close(entered)
		<-release
		// The next hop of the chain, attempted after the shutdown began.
		return b.Send("AAAADEFFXXX", "BBBBDEFFXXX", []byte("hop-2"))
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := b.Send("CCCCDEFFXXX", "AAAADEFFXXX", []byte("hop-1")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	<-entered

	// The first Stop is what closes the inboxes — its own close, not one staged
	// by this test. It cannot get past the join while A is wedged, so when it
	// returns, every inbox is closed and A has not moved: the handler's send
	// below is certainly attempted after the shutdown, with nothing raced.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := b.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the first Stop = %v, want the deadline while A is wedged", err)
	}

	close(release)

	err := b.Stop(drainCtx(t))
	if err == nil {
		t.Fatal("Stop returned nil after cutting a chain; the handler's failure was swallowed")
	}
	if !strings.Contains(err.Error(), "BBBBDEFFXXX") {
		t.Errorf("Stop error %q does not say which send the shutdown refused", err)
	}
}

// A Stop that gives up hands over the failures it collected, for the same
// reason Drain does: held back, they surface on some later call and get blamed
// on the wrong thing.
func TestStopThatTimesOutStillReportsTheDeadLetters(t *testing.T) {
	b := newTestBus(t)
	boom := errors.New("boom")
	blocked := make(chan struct{})
	release := make(chan struct{})
	var n atomic.Int32
	// One actor, two messages, one goroutine: the first message's dead letter
	// is recorded before the second is popped, so the deadline races nothing.
	b.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		if n.Add(1) == 1 {
			return boom
		}
		close(blocked)
		<-release
		return nil
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { close(release); _ = b.Stop(context.Background()) })
	for _, id := range []string{"1", "2"} {
		if err := b.Send("CCCCDEFFXXX", "AAAADEFFXXX", []byte(id)); err != nil {
			t.Fatalf("Send %s: %v", id, err)
		}
	}
	<-blocked

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := b.Stop(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stop = %v, want the deadline", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("Stop = %v; the dead letter it already had was left behind for some later call", err)
	}
}

// An actor registered while Stop is in progress would be in neither of Stop's
// two lists: its inbox never closed, its goroutine never joined. A timed-out
// Stop leaves the bus in exactly that state, which is what makes this
// deterministic — no racing with a join loop.
func TestAddingAnActorWhileStoppingIsAnError(t *testing.T) {
	b := newTestBus(t)
	release := make(chan struct{})
	b.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		<-release
		return nil
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { close(release); _ = b.Stop(context.Background()) })
	if err := b.Send("CCCCDEFFXXX", "AAAADEFFXXX", []byte("x")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := b.Stop(ctx); err == nil {
		t.Fatal("Stop returned nil while a handler was wedged")
	}

	// The bus is mid-shutdown: inboxes closed, snapshot taken, actors not yet
	// joined. It is not "stopped" — a guard that waited for that would accept
	// this actor.
	if err := b.AddActor("DDDDDEFFXXX", "D", func(context.Context, iso20022.BIC, []byte) error { return nil }); err == nil {
		t.Fatal("a bus that is stopping accepted a new actor; nothing would ever close or join it")
	}
	if b.Has("DDDDDEFFXXX") {
		t.Error("the refused actor was registered anyway")
	}
}

// A Stop that times out leaves the bus running, so calling it again finishes
// the job. The alternative — marking the bus stopped on the way out — would
// return nil from the second call while an actor was still going, and would
// leak the bus context for the life of the process.
func TestStopCanBeRetriedAfterATimeout(t *testing.T) {
	b := newTestBus(t)
	release := make(chan struct{})
	b.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		<-release
		return nil
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := b.Send("CCCCDEFFXXX", "AAAADEFFXXX", []byte("x")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := b.Stop(ctx); err == nil {
		t.Fatal("Stop returned nil while a handler was wedged")
	}

	close(release)
	if err := b.Stop(drainCtx(t)); err != nil {
		t.Fatalf("the retried Stop: %v", err)
	}
	if b.ctx.Err() == nil {
		t.Error("a completed Stop left the bus context live; every handler context leaks with it")
	}
}

// A Stop that cannot land says which actor would not let go, for the same
// reason Drain does.
func TestStopTimesOutNamingTheActor(t *testing.T) {
	b := newTestBus(t)
	release := make(chan struct{})
	b.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		<-release
		return nil
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { close(release); _ = b.Stop(context.Background()) })
	if err := b.Send("CCCCDEFFXXX", "AAAADEFFXXX", []byte("x")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := b.Stop(ctx)
	if err == nil {
		t.Fatal("Stop returned nil while a handler was still running")
	}
	if !strings.Contains(err.Error(), "AAAADEFFXXX") {
		t.Errorf("Stop error %q does not name the stuck actor", err)
	}
}

func TestStartTwiceIsAnError(t *testing.T) {
	b := newTestBus(t)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = b.Stop(context.Background()) })
	if err := b.Start(context.Background()); err == nil {
		t.Fatal("the second Start reported success; the actors would have two goroutines each")
	}
	if err := b.Startable(); err == nil {
		t.Fatal("Startable accepted a bus that is already running; its caller would do the roster read for nothing")
	}
}

// A stopped bus is finished. Restarting it would give every actor a second
// goroutine writing to a done channel that is already closed, and Stop would
// panic rather than wait.
func TestAStoppedBusDoesNotRestart(t *testing.T) {
	b := newTestBus(t)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := b.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := b.Start(context.Background()); err == nil {
		t.Fatal("a stopped bus restarted")
	}
}

// An actor registered on a stopped bus would have no goroutine reading its
// inbox: a send to it would report success, count a message in flight and hang
// the next Drain out to its deadline. A black hole that answers "sent" is worse
// than a refusal.
func TestAddingAnActorToAStoppedBusIsAnError(t *testing.T) {
	b := newTestBus(t)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := b.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := b.AddActor("DDDDDEFFXXX", "D", func(context.Context, iso20022.BIC, []byte) error { return nil }); err == nil {
		t.Fatal("a stopped bus accepted a new actor, whose inbox nothing will ever read")
	}
	if b.Has("DDDDDEFFXXX") {
		t.Error("the refused actor was registered anyway")
	}
}

// A Drain that hangs on a message nobody has picked up must say so. Reporting
// "(idle)" while an inbox holds work is the bare deadline this design says it
// avoids, at the moment naming something would help most.
func TestDrainNamesAnInboxNobodyIsReading(t *testing.T) {
	// Deliberately NOT started: no goroutine exists, so the message is certain
	// to sit unread rather than being picked up before the deadline.
	b := newTestBus(t)
	if err := b.Send("AAAADEFFXXX", "BBBBDEFFXXX", []byte("x")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := b.Drain(ctx)
	if err == nil {
		t.Fatal("Drain returned nil with a message sitting in an inbox")
	}
	if !strings.Contains(err.Error(), "BBBBDEFFXXX") {
		t.Errorf("Drain error %q does not name the inbox holding the message", err)
	}
}

// A Drain that gives up still hands over the failures it collected. Holding
// them back would attribute a handler's error to whichever later Drain happened
// to pick it up.
func TestDrainThatTimesOutStillReportsTheDeadLetters(t *testing.T) {
	b := newTestBus(t)
	boom := errors.New("boom")
	blocked := make(chan struct{})
	release := make(chan struct{})
	var n atomic.Int32
	// One actor, two messages, one goroutine: the first message's dead letter
	// is recorded before the second is even popped, so there is nothing here
	// for a deadline to race.
	b.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		if n.Add(1) == 1 {
			return boom
		}
		close(blocked)
		<-release
		return nil
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { close(release); _ = b.Stop(context.Background()) })

	for _, id := range []string{"1", "2"} {
		if err := b.Send("CCCCDEFFXXX", "AAAADEFFXXX", []byte(id)); err != nil {
			t.Fatalf("Send %s: %v", id, err)
		}
	}
	<-blocked

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := b.Drain(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain = %v, want the deadline", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("Drain = %v; the dead letter it already had was left behind for some later Drain", err)
	}
}

// A bus that has never sent anything is quiet, so Drain returns at once. The
// counter's channel therefore starts CLOSED: an open one, replaced on the first
// leave, would make the first Drain of a freshly started process block until
// something happened to be in flight.
func TestDrainOnABusThatHasSentNothingReturnsAtOnce(t *testing.T) {
	b := newTestBus(t)
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = b.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := b.Drain(ctx); err != nil {
		t.Fatalf("Drain on an idle bus = %v", err)
	}
}

// An actor held inside the observation hook is named as held THERE, and not as
// being in a handler it has not entered.
//
// The two phases are different places to be stuck and they have different
// owners: a handler that will not return is this system's bug, and an observer
// that will not return belongs to whoever installed it. Reporting the second as
// the first sends a reader looking in the wrong half of the system at the exact
// moment they are debugging their own hook.
func TestAnActorHeldInObserveIsNamedAsHeldInObserve(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	b := newTestBusObserving(t, func(to, from iso20022.BIC, raw []byte) {
		once.Do(func() { close(entered) })
		<-release
	})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { close(release); _ = b.Stop(context.Background()) })

	if err := b.Send("CCCCDEFFXXX", "AAAADEFFXXX", []byte("x")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	<-entered

	if got := b.Stuck(); !strings.Contains(got, "AAAADEFFXXX in Observe") {
		t.Errorf("Stuck = %q; an actor parked in the observer is not named as being there", got)
	}
}

// The four answers a claim on an address can get, and the two that are refusals
// are refusals for different reasons: one address has an actor answering to it
// and the other has nobody at all, only a claim.
//
// redrivable is the caller's own bookkeeping and the bus takes it on trust —
// which actor is "the caller's" is a question this package has no way to ask.
func TestClaimingAnAddress(t *testing.T) {
	b := newTestBus(t)

	if got, err := b.Claim("DDDDDEFFXXX", false); err != nil || got != Reserved {
		t.Fatalf("claiming a free address = (%v, %v), want Reserved", got, err)
	}
	if got, err := b.Claim("DDDDDEFFXXX", false); err != nil || got != HeldByAnotherClaim {
		t.Fatalf("claiming a reserved address = (%v, %v), want HeldByAnotherClaim", got, err)
	}
	if got, err := b.Claim("AAAADEFFXXX", false); err != nil || got != HeldByAnotherActor {
		t.Fatalf("claiming an actor's address = (%v, %v), want HeldByAnotherActor", got, err)
	}
	if got, err := b.Claim("AAAADEFFXXX", true); err != nil || got != Redriving {
		t.Fatalf("re-claiming the caller's own address = (%v, %v), want Redriving", got, err)
	}

	// A reservation blocks an ordinary registration too: an address claimed by a
	// caller whose actor does not exist yet is not one somebody else may have.
	nothing := func(context.Context, iso20022.BIC, []byte) error { return nil }
	if err := b.AddActor("DDDDDEFFXXX", "D", nothing); !errors.Is(err, ErrAddressTaken) {
		t.Fatalf("AddActor over a reservation = %v, want ErrAddressTaken", err)
	}
	// Register is the half that consumes it, and the address is free again
	// afterwards only in the sense that an actor now holds it.
	if err := b.Register(ActorSpec{BIC: "DDDDDEFFXXX", Name: "D", Handle: nothing}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !b.Has("DDDDDEFFXXX") {
		t.Fatal("Register consumed the reservation and registered no actor")
	}

	// And a released reservation leaves the address exactly as free as it was.
	if _, err := b.Claim("EEEEDEFFXXX", false); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	b.Release("EEEEDEFFXXX")
	if err := b.AddActor("EEEEDEFFXXX", "E", nothing); err != nil {
		t.Fatalf("AddActor after the reservation was released: %v", err)
	}
}
