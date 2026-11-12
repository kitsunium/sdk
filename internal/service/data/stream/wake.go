package stream

import "sync"

// wakeSignal is a broadcast: every reader waiting on the channel wakes when it
// is closed, and a fresh channel is armed for the next wait. A channel closed
// and replaced, rather than one value sent, is what makes it a broadcast — a
// send wakes one receiver and is lost if nobody is receiving. It is
// internal/service/data/queue's wakeSignal without the due instant, which a
// stream has no use for: no record of a stream becomes readable on its own.
type wakeSignal struct {
	// ch is the channel handed out until the next fire.
	ch chan struct{}
	mu sync.Mutex
}

// newWakeSignal returns a signal with its first channel armed.
func newWakeSignal() *wakeSignal {
	//: never nil, so a reader can select on it without a nil check.
	return &wakeSignal{ch: make(chan struct{})}
}

// current returns the channel the next fire will close.
func (w *wakeSignal) current() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	//: the armed channel.
	return w.ch
}

// fire wakes every reader waiting on the current channel and arms the next.
func (w *wakeSignal) fire() {
	w.mu.Lock()
	defer w.mu.Unlock()
	//: the channel every current waiter holds is swapped out under the lock,
	//: so exactly one fire ever closes it.
	fired := w.ch
	w.ch = make(chan struct{})
	//: close is the broadcast; the replacement is what the next wait gets.
	close(fired)
}
