package consensus

import "sync"

// fifo is a queue a producer never blocks on: the event loop pushes to the
// storage actor and the apply goroutine without ever waiting for them (D5b).
// What bounds it is upstream: proposals are refused with OVERLOADED past
// MaxPendingBytes before anything is appended.
type fifo[T any] struct {
	mu     sync.Mutex
	items  []T
	signal chan struct{}
}

func newFifo[T any]() *fifo[T] { return &fifo[T]{signal: make(chan struct{}, 1)} }

func (q *fifo[T]) push(x T) {
	q.mu.Lock()
	q.items = append(q.items, x)
	q.mu.Unlock()
	select {
	case q.signal <- struct{}{}:
	default:
	}
}

// take returns every queued item, oldest first.
func (q *fifo[T]) take(dst []T) []T {
	q.mu.Lock()
	dst = append(dst[:0], q.items...)
	clear(q.items)
	q.items = q.items[:0]
	q.mu.Unlock()
	return dst
}
