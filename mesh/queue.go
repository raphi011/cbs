package mesh

import (
	"sync"

	"github.com/raphi011/cbs/iso20022"
)

// item is one message waiting in an actor's inbox: the bytes, and who sent
// them.
//
// The sender travels beside the bytes rather than inside them because an
// unparseable message hides its own header. Without this field an FF01 could
// be detected and not answered, which would make the one rejection every real
// receiver must be able to send the one this system could not.
type item struct {
	from iso20022.BIC
	raw  []byte
}

// queue is an actor's unbounded inbox.
//
// Unbounded, not a buffered channel. A fixed buffer between two actors that
// message each other is a deadlock, and in this system they do — the CSM sends
// to a bank while that bank is sending to the CSM. An unbounded queue means a
// send never blocks, so no cycle can wedge. TestQueueNeverBlocksTheSender and
// TestMutuallyMessagingActorsDoNotDeadlock are the two halves of that pin.
//
// The cost is that nothing applies backpressure: a runaway producer grows the
// slice until memory runs out. That is the right trade here, where the
// producers are a fixed set of actors driving a bounded number of payments,
// and it is recorded in the package doc as a property of this mesh rather than
// of a real one.
//
// depth is visible to the package because a queue you can see the length of is
// a thing a real CSM has and a chan []byte is not.
type queue struct {
	mu     sync.Mutex
	items  []item
	closed bool

	// signal carries at most one wake-up. A buffered channel of size 1 used
	// this way is the standard condition-variable-with-select shape: it lets
	// pop block on a channel, which sync.Cond cannot do.
	signal chan struct{}
}

func newQueue() *queue {
	return &queue{signal: make(chan struct{}, 1)}
}

// push appends an item and reports whether the queue accepted it. A closed
// queue accepts nothing.
//
// It returns a bool rather than swallowing the refusal because the mesh counts
// a message as in flight BEFORE it pushes: an item silently dropped here would
// be a message that never arrives and never completes, and a Drain that blocks
// for ever. See TestQueueRefusesAPushAfterClose and Mesh.send, which undoes its
// own accounting when this returns false.
func (q *queue) push(it item) bool {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return false
	}
	q.items = append(q.items, it)
	q.mu.Unlock()

	select {
	case q.signal <- struct{}{}:
	default:
	}
	return true
}

// pop blocks until an item is available, returning false once the queue is
// closed AND empty. Draining before reporting closed is deliberate: a Stop
// that discarded queued messages would make shutdown lose work silently.
func (q *queue) pop() (item, bool) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			it := q.items[0]
			q.items = q.items[1:]
			q.mu.Unlock()
			return it, true
		}
		closed := q.closed
		q.mu.Unlock()
		if closed {
			return item{}, false
		}
		<-q.signal
	}
}

func (q *queue) close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

func (q *queue) depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}
