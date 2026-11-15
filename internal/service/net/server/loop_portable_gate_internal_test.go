// Package server — the portable backend's accept gate (#978): an acceptor of a
// Backlog group calls Accept only while a lane waits, the last lane to wake
// withdraws the Accept in progress, and the drain opens the gate for good.
package server

import (
	stdnet "net"
	"sync"
	"testing"
	"time"
)

// deadlineRecorder is a listener that records the deadlines set on it.
type deadlineRecorder struct {
	stdnet.Listener
	mu        sync.Mutex
	deadlines []time.Time
}

// SetDeadline records t.
func (d *deadlineRecorder) SetDeadline(t time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.deadlines = append(d.deadlines, t)
	return nil
}

// last is the deadline set last, and how many were.
func (d *deadlineRecorder) last() (time.Time, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.deadlines) == 0 {
		return time.Time{}, 0
	}
	return d.deadlines[len(d.deadlines)-1], len(d.deadlines)
}

// waited reports whether the gate's wait returned within d.
func waited(g *acceptGate, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		g.wait(true)
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		g.release()
		<-done
		return false
	}
}

// leaving runs leave on its own goroutine and returns what closes once it
// returned.
func leaving(g *acceptGate) <-chan struct{} {
	left := make(chan struct{})
	go func() {
		g.leave()
		close(left)
	}()
	return left
}

// returnsWithin reports whether ch closes within d.
func returnsWithin(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

// TestAcceptGate pins the gate's table: closed until a lane waits, open while
// one does; the last lane to wake withdraws a counted acceptor inside Accept
// with an expired deadline and waits until it is out — the netpoller would
// otherwise let a connection arriving before the acceptor runs win over the
// deadline —, released by done, by another lane waiting again, or by
// release; the next lane to wait clears the deadline; a listener a deadline
// does not reach is neither counted nor waited for; release opens the gate
// for good; a group with no Backlog has no gate, every call a no-op.
func TestAcceptGate(t *testing.T) {
	ln := &deadlineRecorder{}
	g := newAcceptGate([]stdnet.Listener{ln, &plainListener{}})
	if len(g.lns) != 1 || !g.reaches(ln) || g.reaches(&plainListener{}) {
		t.Fatalf("%d listeners a deadline reaches, want 1, the recorder's", len(g.lns))
	}
	if waited(newAcceptGate(nil), 50*time.Millisecond) {
		t.Fatal("an acceptor passed a gate no lane opened")
	}
	g.enter()
	if !waited(g, time.Second) {
		t.Fatal("an acceptor waited with a lane waiting")
	}
	if d, n := ln.last(); n != 1 || !d.IsZero() {
		t.Fatalf("deadline %v after %d sets, want the first lane's clear", d, n)
	}
	left := leaving(g)
	if returnsWithin(left, 100*time.Millisecond) {
		t.Fatal("the last lane left with a counted acceptor still inside Accept")
	}
	if d, _ := ln.last(); !d.Equal(gateExpired) {
		t.Fatalf("deadline %v once the last lane woke with an acceptor inside Accept, want the expired one", d)
	}
	g.done(true)
	if !returnsWithin(left, time.Second) {
		t.Fatal("the last lane still waits once the acceptor is out of Accept")
	}
	g.enter()
	g.leave()
	if d, n := ln.last(); n != 3 || !d.IsZero() {
		t.Fatalf("deadline %v after %d sets, want 3 ending clear: nothing withdrawn with no acceptor inside Accept", d, n)
	}
	// Two lanes: the second waiting again frees the first from its wait.
	g.enter()
	if !waited(g, time.Second) {
		t.Fatal("an acceptor waited with a lane waiting")
	}
	left = leaving(g)
	if returnsWithin(left, 100*time.Millisecond) {
		t.Fatal("the last lane left with a counted acceptor still inside Accept")
	}
	g.enter()
	if !returnsWithin(left, time.Second) {
		t.Fatal("a lane waiting again left the first one waiting on the acceptor")
	}
	g.done(true)
	g.leave()
	if !returnsWithin(leaving(newGateLeftOpen(g)), time.Second) {
		t.Fatal("release left a lane waiting on the acceptor")
	}
	g.done(true)
	g.release()
	if !waited(g, time.Second) {
		t.Fatal("an acceptor waited on a released gate")
	}
	g.done(true)
	if d, _ := ln.last(); !d.IsZero() {
		t.Fatalf("deadline %v on a released gate, want none", d)
	}
	var none *acceptGate
	none.enter()
	none.leave()
	none.wait(true)
	none.done(false)
	none.release()
	if none.reaches(ln) {
		t.Fatal("no gate reaches a listener")
	}
}

// newGateLeftOpen leaves g with one lane waiting on a counted acceptor, then
// releases it from another goroutine shortly after: the waiting lane must be
// let go.
func newGateLeftOpen(g *acceptGate) *acceptGate {
	g.enter()
	g.wait(true)
	go func() {
		time.Sleep(20 * time.Millisecond)
		g.release()
	}()
	return g
}

// plainListener is a listener with no SetDeadline, as a TLS layer leaves it.
type plainListener struct {
	stdnet.Listener
}
