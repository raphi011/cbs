package mesh

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

// A message that begets a message must keep Drain blocked. This is the whole
// correctness argument for the in-flight counter: increment before enqueue,
// decrement only after the handler that consumed it has returned, so a chain
// never shows as quiet mid-flight.
func TestDrainWaitsForMessagesThatBegetMessages(t *testing.T) {
	m := newTestMesh(t)
	seen := &recorder{}

	m.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		seen.record("A")
		return m.send("AAAADEFFXXX", "BBBBDEFFXXX", testEnvelope("AAAADEFFXXX", "BBBBDEFFXXX", "hop-2"))
	}
	m.actors["BBBBDEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		seen.record("B")
		return m.send("BBBBDEFFXXX", "CCCCDEFFXXX", testEnvelope("BBBBDEFFXXX", "CCCCDEFFXXX", "hop-3"))
	}
	m.actors["CCCCDEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		seen.record("C")
		return nil
	}

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	if err := m.send("CCCCDEFFXXX", "AAAADEFFXXX", testEnvelope("CCCCDEFFXXX", "AAAADEFFXXX", "hop-1")); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := m.Drain(drainCtx(t)); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if got := seen.String(); got != "ABC" {
		t.Fatalf("handlers ran %q, want ABC — Drain returned before the chain finished", got)
	}
}

// recorder is what the three handlers above append to, under a lock of its own.
//
// The lock is not decoration, and it is not there because the race detector
// asked for it: with the mesh behaving, the appends are already ordered — each
// handler appends before it sends, and the inbox mutex the next actor pops
// under is a happens-before edge, so `var seen []string` runs clean under -race
// (measured: 1,800 runs at GOMAXPROCS 1 and 8, no report). It is there because
// that ordering is exactly what the test EXISTS to check. A mesh that let two
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
	m := newTestMesh(t)
	hops := 0
	m.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		if hops >= 50 {
			return nil
		}
		hops++
		return m.send("AAAADEFFXXX", "BBBBDEFFXXX", testEnvelope("AAAADEFFXXX", "BBBBDEFFXXX", "ping"))
	}
	m.actors["BBBBDEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		return m.send("BBBBDEFFXXX", "AAAADEFFXXX", testEnvelope("BBBBDEFFXXX", "AAAADEFFXXX", "pong"))
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	if err := m.send("CCCCDEFFXXX", "AAAADEFFXXX", testEnvelope("CCCCDEFFXXX", "AAAADEFFXXX", "start")); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := m.Drain(drainCtx(t)); err != nil {
		t.Fatalf("Drain: %v", err)
	}
}

// A handler error with nobody to tell must not vanish. Every draining test in
// this package therefore asserts, for free, that nothing was silently eaten.
func TestDrainReportsHandlerErrors(t *testing.T) {
	m := newTestMesh(t)
	boom := errors.New("boom")
	m.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error { return boom }

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	_ = m.send("CCCCDEFFXXX", "AAAADEFFXXX", testEnvelope("CCCCDEFFXXX", "AAAADEFFXXX", "x"))
	err := m.Drain(drainCtx(t))
	if !errors.Is(err, boom) {
		t.Fatalf("Drain = %v, want the handler's error", err)
	}
	// And it is taken, not accumulated: a second Drain on a quiet mesh is clean.
	if err := m.Drain(drainCtx(t)); err != nil {
		t.Fatalf("second Drain = %v, want nil", err)
	}
}

func TestSendToAnUnknownBICIsAnError(t *testing.T) {
	m := newTestMesh(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	err := m.send("AAAADEFFXXX", "ZZZZDEFFXXX", testEnvelope("AAAADEFFXXX", "ZZZZDEFFXXX", "x"))
	if !errors.Is(err, ErrUnknownBIC) {
		t.Fatalf("send to an unrouted BIC = %v, want ErrUnknownBIC", err)
	}
}

// A wedged handler must report as itself, naming the actor, rather than as
// "test timed out after 10m".
func TestDrainTimesOutNamingTheActor(t *testing.T) {
	m := newTestMesh(t)
	release := make(chan struct{})
	m.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		<-release
		return nil
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { close(release); _ = m.Stop(context.Background()) })

	_ = m.send("CCCCDEFFXXX", "AAAADEFFXXX", testEnvelope("CCCCDEFFXXX", "AAAADEFFXXX", "x"))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := m.Drain(ctx)
	if err == nil {
		t.Fatal("Drain returned nil while a handler was still running")
	}
	if !strings.Contains(err.Error(), "AAAADEFFXXX") {
		t.Errorf("Drain error %q does not name the stuck actor", err)
	}
}

func drainCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// testTime is the instant every test envelope is stamped with. Fixed, because a
// message whose bytes depend on the clock cannot be compared.
var testTime = time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)

// testConfig names the two institutions. Their actors exist in every test mesh
// alongside A, B and C; nothing in this file addresses them, which is the point
// — an actor nobody talks to must not disturb Drain.
var testConfig = Config{CentralBankBIC: "CBSEDEFFXXX", ClearingHouseBIC: "CSMXFRPPXXX"}

// newTestMesh builds a mesh of three actors with no-op handlers and NO
// payment.Network. Nothing in this file needs one: this task is transport, and
// a transport test that needed a store to run would be testing the wrong thing.
func newTestMesh(t *testing.T) *Mesh {
	t.Helper()
	m, err := New(nil, testConfig, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, bic := range []iso20022.BIC{"AAAADEFFXXX", "BBBBDEFFXXX", "CCCCDEFFXXX"} {
		if err := m.addActor(bic, string(bic), func(context.Context, iso20022.BIC, []byte) error { return nil }); err != nil {
			t.Fatalf("addActor %s: %v", bic, err)
		}
	}
	return m
}

// testEnvelope is a minimal but VALID pacs.002. Valid matters: send marshals
// before it routes, so an envelope that failed validation would never reach a
// queue and every test here would be asserting on the marshaller instead of on
// the transport. TestTestEnvelopeMarshals holds the helper to it.
func testEnvelope(from, to iso20022.BIC, id string) iso20022.Envelope {
	return iso20022.Envelope{
		AppHdr: iso20022.AppHdr{
			Fr:        iso20022.NewAgent(from),
			To:        iso20022.NewAgent(to),
			BizMsgIdr: id,
			MsgDefIdr: "pacs.002.001.10",
			CreDt:     iso20022.ISODateTime{Time: testTime},
		},
		Document: &iso20022.Pacs002{
			FIToFIPmtStsRpt: iso20022.FIToFIPaymentStatusReport{
				GrpHdr: iso20022.StatusGroupHeader{
					MsgId:   id,
					CreDtTm: iso20022.ISODateTime{Time: testTime},
				},
				OrgnlGrpInfAndSts: iso20022.OriginalGroupHeader{
					OrgnlMsgId:   "orig-" + id,
					OrgnlMsgNmId: "pacs.008.001.08",
					GrpSts:       iso20022.GroupStatusAccepted,
				},
				TxInfAndSts: []iso20022.PaymentTransactionStatus{{
					StsId:     id,
					OrgnlTxId: "tx-" + id,
					TxSts:     iso20022.TransactionStatusAccepted,
				}},
			},
		},
	}
}

// The helper the other tests depend on has to be checked by a test of its own.
// If testEnvelope stopped being valid, send would fail at the marshaller and
// several tests here would go on passing for the wrong reason —
// TestDrainReportsHandlerErrors would still see a nil second Drain, and
// TestSendToAnUnknownBICIsAnError's error would come from the wrong place.
func TestTestEnvelopeMarshals(t *testing.T) {
	if _, err := iso20022.Marshal(testEnvelope("AAAADEFFXXX", "BBBBDEFFXXX", "x")); err != nil {
		t.Fatalf("the test envelope does not marshal: %v", err)
	}
}

// An actor registered after Start still gets its goroutine. This is not a
// convenience: the process starts the mesh before it seeds the participants
// (cmd/server does, deliberately, so that nothing is listening while the seed
// runs), so every member bank's actor is added to a mesh that is already
// running.
func TestAnActorAddedAfterStartReceives(t *testing.T) {
	m := newTestMesh(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	got := make(chan iso20022.BIC, 1)
	if err := m.addActor("DDDDDEFFXXX", "D", func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		got <- from
		return nil
	}); err != nil {
		t.Fatalf("addActor: %v", err)
	}
	if err := m.send("AAAADEFFXXX", "DDDDDEFFXXX", testEnvelope("AAAADEFFXXX", "DDDDDEFFXXX", "late")); err != nil {
		t.Fatalf("send: %v", err)
	}
	if err := m.Drain(drainCtx(t)); err != nil {
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
	m := newTestMesh(t)
	err := m.addActor("AAAADEFFXXX", "again", func(context.Context, iso20022.BIC, []byte) error { return nil })
	if err == nil {
		t.Fatal("addActor accepted a second actor for AAAADEFFXXX")
	}
}

// A send to a stopped actor must be refused, not accepted and dropped. send
// counts the message as in flight before it pushes, so an item the queue threw
// away would leave the counter above zero for ever: Drain would block until its
// deadline and report a mesh that is doing nothing as busy.
func TestSendAfterStopIsRefusedAndLeavesDrainQuiet(t *testing.T) {
	m := newTestMesh(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := m.send("AAAADEFFXXX", "BBBBDEFFXXX", testEnvelope("AAAADEFFXXX", "BBBBDEFFXXX", "x")); err == nil {
		t.Fatal("send to a stopped mesh reported success")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Drain(ctx); err != nil {
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
	m := newTestMesh(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	m.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		close(entered)
		<-release
		close(finished)
		return nil
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.send("CCCCDEFFXXX", "AAAADEFFXXX", testEnvelope("CCCCDEFFXXX", "AAAADEFFXXX", "x")); err != nil {
		t.Fatalf("send: %v", err)
	}
	<-entered

	// The context is built here rather than inside the goroutine: drainCtx
	// touches t, and t belongs to the test's own goroutine.
	stopCtx := drainCtx(t)
	stopped := make(chan error, 1)
	go func() { stopped <- m.Stop(stopCtx) }()
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
// This is the property Stop's deadline has to be sized against, and the one its
// doc comment used to state backwards — it said queued messages were discarded,
// which would have made a shutdown lose work silently and made a caller size a
// context for one handler when it is really sizing it for the whole queue.
func TestStopDeliversWhatIsAlreadyQueued(t *testing.T) {
	m := newTestMesh(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var handled atomic.Int32
	m.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		if handled.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := m.send("CCCCDEFFXXX", "AAAADEFFXXX", testEnvelope("CCCCDEFFXXX", "AAAADEFFXXX", "1")); err != nil {
		t.Fatalf("send: %v", err)
	}
	// Only once the first message is inside the handler is the second one
	// certain to be waiting in the inbox rather than already dealt with.
	<-entered
	if err := m.send("CCCCDEFFXXX", "AAAADEFFXXX", testEnvelope("CCCCDEFFXXX", "AAAADEFFXXX", "2")); err != nil {
		t.Fatalf("send: %v", err)
	}

	// Closing the inboxes is Stop's first act, and it is done here directly so
	// that it cannot race the handler waking up. Called from a goroutine beside
	// close(release) it did race, and the earlier version of this test passed
	// with the queue reporting closed-before-drained — it was asserting which
	// of the two won, not what Stop guarantees.
	for _, a := range m.actors {
		a.q.close()
	}
	close(release)

	if err := m.Stop(drainCtx(t)); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := handled.Load(); got != 2 {
		t.Fatalf("the actor delivered %d of the 2 messages; the queued one was dropped when its inbox closed", got)
	}
}

// A Stop that times out leaves the mesh running, so calling it again finishes
// the job. The alternative — marking the mesh stopped on the way out — would
// return nil from the second call while an actor was still going, and would
// leak the mesh context for the life of the process.
func TestStopCanBeRetriedAfterATimeout(t *testing.T) {
	m := newTestMesh(t)
	release := make(chan struct{})
	m.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		<-release
		return nil
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.send("CCCCDEFFXXX", "AAAADEFFXXX", testEnvelope("CCCCDEFFXXX", "AAAADEFFXXX", "x")); err != nil {
		t.Fatalf("send: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := m.Stop(ctx); err == nil {
		t.Fatal("Stop returned nil while a handler was wedged")
	}

	close(release)
	if err := m.Stop(drainCtx(t)); err != nil {
		t.Fatalf("the retried Stop: %v", err)
	}
	if m.ctx.Err() == nil {
		t.Error("a completed Stop left the mesh context live; every handler context leaks with it")
	}
}

// A Stop that cannot land says which actor would not let go, for the same
// reason Drain does.
func TestStopTimesOutNamingTheActor(t *testing.T) {
	m := newTestMesh(t)
	release := make(chan struct{})
	m.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		<-release
		return nil
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { close(release); _ = m.Stop(context.Background()) })
	if err := m.send("CCCCDEFFXXX", "AAAADEFFXXX", testEnvelope("CCCCDEFFXXX", "AAAADEFFXXX", "x")); err != nil {
		t.Fatalf("send: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := m.Stop(ctx)
	if err == nil {
		t.Fatal("Stop returned nil while a handler was still running")
	}
	if !strings.Contains(err.Error(), "AAAADEFFXXX") {
		t.Errorf("Stop error %q does not name the stuck actor", err)
	}
}

func TestStartTwiceIsAnError(t *testing.T) {
	m := newTestMesh(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("the second Start reported success; the actors would have two goroutines each")
	}
}

// A stopped mesh is finished. Restarting it would give every actor a second
// goroutine writing to a done channel that is already closed, and Stop would
// panic rather than wait.
func TestAStoppedMeshDoesNotRestart(t *testing.T) {
	m := newTestMesh(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := m.Start(context.Background()); err == nil {
		t.Fatal("a stopped mesh restarted")
	}
}

// An actor registered on a stopped mesh would have no goroutine reading its
// inbox: a send to it would report success, count a message in flight and hang
// the next Drain out to its deadline. A black hole that answers "sent" is worse
// than a refusal.
func TestAddingAnActorToAStoppedMeshIsAnError(t *testing.T) {
	m := newTestMesh(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := m.addActor("DDDDDEFFXXX", "D", func(context.Context, iso20022.BIC, []byte) error { return nil }); err == nil {
		t.Fatal("a stopped mesh accepted a new actor, whose inbox nothing will ever read")
	}
	if _, ok := m.actors["DDDDDEFFXXX"]; ok {
		t.Error("the refused actor was registered anyway")
	}
}

// A Drain that hangs on a message nobody has picked up must say so. Reporting
// "(idle)" while an inbox holds work is the bare deadline this design says it
// avoids, at the moment naming something would help most.
func TestDrainNamesAnInboxNobodyIsReading(t *testing.T) {
	// Deliberately NOT started: no goroutine exists, so the message is certain
	// to sit unread rather than being picked up before the deadline.
	m := newTestMesh(t)
	if err := m.send("AAAADEFFXXX", "BBBBDEFFXXX", testEnvelope("AAAADEFFXXX", "BBBBDEFFXXX", "x")); err != nil {
		t.Fatalf("send: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := m.Drain(ctx)
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
	m := newTestMesh(t)
	boom := errors.New("boom")
	blocked := make(chan struct{})
	release := make(chan struct{})
	var n atomic.Int32
	// One actor, two messages, one goroutine: the first message's dead letter
	// is recorded before the second is even popped, so there is nothing here
	// for a deadline to race.
	m.actors["AAAADEFFXXX"].handle = func(ctx context.Context, from iso20022.BIC, raw []byte) error {
		if n.Add(1) == 1 {
			return boom
		}
		close(blocked)
		<-release
		return nil
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { close(release); _ = m.Stop(context.Background()) })

	for _, id := range []string{"1", "2"} {
		if err := m.send("CCCCDEFFXXX", "AAAADEFFXXX", testEnvelope("CCCCDEFFXXX", "AAAADEFFXXX", id)); err != nil {
			t.Fatalf("send %s: %v", id, err)
		}
	}
	<-blocked

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := m.Drain(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Drain = %v, want the deadline", err)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("Drain = %v; the dead letter it already had was left behind for some later Drain", err)
	}
}

// An actor with no behaviour yet REFUSES what it is sent. Tasks 10-13 replace
// these handlers; until they do, a message to one must not look like a message
// that was dealt with.
func TestAMessageToAnActorWithNoHandlerIsADeadLetter(t *testing.T) {
	m := newTestMesh(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	to := testConfig.ClearingHouseBIC
	if err := m.send("AAAADEFFXXX", to, testEnvelope("AAAADEFFXXX", to, "x")); err != nil {
		t.Fatalf("send: %v", err)
	}
	err := m.Drain(drainCtx(t))
	if err == nil {
		t.Fatal("Drain reported a clean mesh; the placeholder handler swallowed the message")
	}
	if !strings.Contains(err.Error(), string(to)) {
		t.Errorf("dead letter %q does not name the actor that produced it", err)
	}
}

// A mesh that has never sent anything is quiet, so Drain returns at once. The
// counter's channel therefore starts CLOSED: an open one, replaced on the first
// leave, would make the first Drain of a freshly started process block until
// something happened to be in flight.
func TestDrainOnAMeshThatHasSentNothingReturnsAtOnce(t *testing.T) {
	m := newTestMesh(t)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Drain(ctx); err != nil {
		t.Fatalf("Drain on an idle mesh = %v", err)
	}
}

// The two configured institutions get actors of their own, so that a message
// addressed to the clearing house or the central bank routes like any other.
func TestNewCreatesTheTwoInstitutions(t *testing.T) {
	m := newTestMesh(t)
	for _, bic := range []iso20022.BIC{testConfig.CentralBankBIC, testConfig.ClearingHouseBIC} {
		if _, ok := m.actors[bic]; !ok {
			t.Errorf("no actor for %s", bic)
		}
	}
}

func TestNewRefusesAConfigItCannotRoute(t *testing.T) {
	cases := map[string]Config{
		"no clearing house":  {CentralBankBIC: "CBSEDEFFXXX"},
		"no central bank":    {ClearingHouseBIC: "CSMXFRPPXXX"},
		"malformed BIC":      {CentralBankBIC: "cbse", ClearingHouseBIC: "CSMXFRPPXXX"},
		"one BIC, two roles": {CentralBankBIC: "CBSEDEFFXXX", ClearingHouseBIC: "CBSEDEFFXXX"},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := New(nil, cfg, slog.New(slog.DiscardHandler)); err == nil {
				t.Fatalf("New accepted %+v", cfg)
			}
		})
	}
}
