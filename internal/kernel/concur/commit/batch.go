package commit

import "sync"

// batch is one flush's worth of items, and the verdict every participant
// waits for.
//
// items is appended to under Group.mu until the batch is sealed, and read by
// the flush only after: the seal is the hand-off. err and panicked are
// written by the leading goroutine before done is closed, which is the
// happens-before edge every waiter reads them after.
type batch[T any] struct {
	items []T
	// full is closed when the batch reaches MaxItems, cutting the wait short.
	full chan struct{}
	// filled closes full once, however many callers find the batch full.
	filled sync.Once
	// after is the previous batch's done: flushes run one at a time, in order.
	after <-chan struct{}
	// done is closed once err and panicked are final.
	done chan struct{}
	// settled closes done once.
	settled  sync.Once
	err      error
	panicked any
}

// newBatch returns an empty batch that flushes after the batch whose done is
// after, nil for the first.
func newBatch[T any](after <-chan struct{}) *batch[T] {
	//: two channels, closed at most once each.
	return &batch[T]{full: make(chan struct{}), after: after, done: make(chan struct{})}
}

// markFull closes full once. Caller holds Group.mu.
func (b *batch[T]) markFull() {
	//: closing twice would panic.
	b.filled.Do(func() { close(b.full) })
}

// settle closes done once: err and panicked are final, every waiter may read
// them.
func (b *batch[T]) settle() {
	//: closing twice would panic.
	b.settled.Do(func() { close(b.done) })
}
