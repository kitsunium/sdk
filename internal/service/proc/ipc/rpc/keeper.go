package rpc

import (
	"sync"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// keeper is a connection's timekeeper: one goroutine and one timer on the
// injected clock for every deadline the connection has — the frame being read,
// the frame being written, and each call's budget. It is how a connection
// stays at MaxInFlight + 2 goroutines: no call waits on a timer of its own.
type keeper struct {
	// clk is the injected clock every deadline is on.
	clk clock.Timed
	// frameExpired runs, on the keeper's goroutine, when a frame was not whole
	// in time; the connection closes.
	frameExpired func()
	// callExpired runs, on the keeper's goroutine, when a call's budget passed.
	callExpired func(id uint64)
	// wake tells the goroutine a deadline moved earlier.
	wake chan struct{}
	// stop ends the goroutine.
	stop chan struct{}
	// stopOnce closes stop once, whoever tears the connection down.
	stopOnce sync.Once
	// mu guards the deadlines below.
	mu sync.Mutex
	// readDue and writeDue are the frames' deadlines; the zero time is none.
	readDue, writeDue time.Time
	// calls are the calls' deadlines, by id.
	calls map[uint64]time.Time
}

// newKeeper returns a keeper whose goroutine run starts.
func newKeeper(clk clock.Timed, frameExpired func(), callExpired func(id uint64)) *keeper {
	return &keeper{
		clk: clk, frameExpired: frameExpired, callExpired: callExpired,
		wake: make(chan struct{}, 1), stop: make(chan struct{}),
		calls: make(map[uint64]time.Time),
	}
}

// run waits for the earliest deadline and expires what is due, until close.
func (k *keeper) run() {
	t := k.clk.NewTimer(time.Hour)
	t.Stop()
	defer t.Stop()
	for running := true; running; {
		running = k.step(t)
	}
}

// step arms t for the earliest deadline — none leaves it stopped — and waits
// for one event: false once close was called.
func (k *keeper) step(t clock.Timer) bool {
	//: the timer follows the earliest deadline.
	if due, ok := k.earliest(); ok {
		t.Reset(due.Sub(k.clk.Now()))
	}
	select {
	case _, open := <-k.stop:
		//: stop is only ever closed: nothing is sent on it.
		return open
	case _, open := <-k.wake:
		//: wake is never closed; a wake re-reads the deadlines.
		if open {
			t.Stop()
		}
	case _, fired := <-t.C():
		//: a timer's channel is never closed; a value is its firing.
		if fired {
			k.expire(k.clk.Now())
		}
	}
	return true
}

// close ends the goroutine; a second close changes nothing.
func (k *keeper) close() {
	k.stopOnce.Do(func() { close(k.stop) })
}

// earliest is the earliest deadline, and false when there is none.
func (k *keeper) earliest() (time.Time, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var due time.Time
	for _, d := range [...]time.Time{k.readDue, k.writeDue} {
		due = earlier(due, d)
	}
	for _, d := range k.calls {
		due = earlier(due, d)
	}
	return due, !due.IsZero()
}

// earlier is the earlier of a and b, the zero time standing for none.
func earlier(a, b time.Time) time.Time {
	//: b is none, or a is set and earlier.
	if b.IsZero() || (!a.IsZero() && a.Before(b)) {
		return a
	}
	return b
}

// expire runs the callbacks of every deadline at or before now; a timer that
// fired for a deadline since moved finds nothing.
func (k *keeper) expire(now time.Time) {
	k.mu.Lock()
	frame := due(k.readDue, now) || due(k.writeDue, now)
	var ids []uint64
	for id, d := range k.calls {
		//: past its budget.
		if due(d, now) {
			ids = append(ids, id)
			delete(k.calls, id)
		}
	}
	k.mu.Unlock()
	//: a frame not whole in time ends the connection.
	if frame {
		k.frameExpired()
	}
	for _, id := range ids {
		k.callExpired(id)
	}
}

// due reports whether the deadline d is set and not after now.
func due(d, now time.Time) bool {
	return !d.IsZero() && !d.After(now)
}

// armRead and armWrite set the frame's deadline FrameTimeout from now; the
// disarm twins clear it.
func (k *keeper) armRead(d time.Duration) {
	k.set(&k.readDue, k.clk.Now().Add(d))
}

// disarmRead clears the read frame's deadline.
func (k *keeper) disarmRead() {
	k.set(&k.readDue, time.Time{})
}

// armWrite sets the written frame's deadline d from now.
func (k *keeper) armWrite(d time.Duration) {
	k.set(&k.writeDue, k.clk.Now().Add(d))
}

// disarmWrite clears the written frame's deadline.
func (k *keeper) disarmWrite() {
	k.set(&k.writeDue, time.Time{})
}

// armCall sets call id's deadline d from now.
func (k *keeper) armCall(id uint64, d time.Duration) {
	k.mu.Lock()
	k.calls[id] = k.clk.Now().Add(d)
	k.mu.Unlock()
	k.poke()
}

// disarmCall clears call id's deadline.
func (k *keeper) disarmCall(id uint64) {
	k.mu.Lock()
	delete(k.calls, id)
	k.mu.Unlock()
}

// set writes one frame deadline and wakes the goroutine when it is set.
func (k *keeper) set(field *time.Time, d time.Time) {
	k.mu.Lock()
	*field = d
	k.mu.Unlock()
	//: a cleared deadline needs no wake: a timer left early finds nothing.
	if !d.IsZero() {
		k.poke()
	}
}

// poke wakes the goroutine without blocking: one pending wake is enough.
func (k *keeper) poke() {
	select {
	case k.wake <- struct{}{}:
	default:
	}
}
