package mesh

import (
	"sync"
	"testing"
)

// The queue is unbounded on purpose. A fixed buffer between two actors that
// message each other is a deadlock, and in this system they do: the CSM sends
// to a bank while that bank is sending to the CSM. This test is the pin.
func TestQueueNeverBlocksTheSender(t *testing.T) {
	q := newQueue()
	for i := 0; i < 10_000; i++ {
		q.push(item{from: "AURODEFFXXX", raw: []byte("x")})
	}
	if got := q.depth(); got != 10_000 {
		t.Fatalf("depth = %d, want 10000", got)
	}
}

func TestQueueDeliversInOrder(t *testing.T) {
	q := newQueue()
	for i := byte(0); i < 5; i++ {
		q.push(item{raw: []byte{i}})
	}
	q.close()
	for i := byte(0); i < 5; i++ {
		got, ok := q.pop()
		if !ok {
			t.Fatalf("pop %d: queue closed early", i)
		}
		if got.raw[0] != i {
			t.Fatalf("pop %d returned %d; the queue is not FIFO", i, got.raw[0])
		}
	}
	if _, ok := q.pop(); ok {
		t.Fatal("pop returned an item after the queue was drained and closed")
	}
}

func TestQueuePopBlocksUntilPushed(t *testing.T) {
	q := newQueue()
	var wg sync.WaitGroup
	wg.Add(1)
	got := make(chan byte, 1)
	go func() {
		defer wg.Done()
		it, ok := q.pop()
		if ok {
			got <- it.raw[0]
		}
	}()
	q.push(item{raw: []byte{7}})
	wg.Wait()
	if v := <-got; v != 7 {
		t.Fatalf("pop returned %d, want 7", v)
	}
}

// A push a stopped actor can never read must SAY so, rather than accept the
// message and drop it. The mesh increments its in-flight counter before it
// pushes, so a silently discarded item would be a message that is for ever in
// flight and a Drain that never returns — the loss the pop-before-close rule
// exists to prevent, one step earlier.
func TestQueueRefusesAPushAfterClose(t *testing.T) {
	q := newQueue()
	q.close()
	if q.push(item{raw: []byte{1}}) {
		t.Fatal("push reported success on a closed queue")
	}
	if got := q.depth(); got != 0 {
		t.Fatalf("depth = %d after a refused push, want 0", got)
	}
}
