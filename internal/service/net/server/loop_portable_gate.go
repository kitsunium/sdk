package server

import (
	stdnet "net"
	"sync"
	"time"
)

// gateExpired is the deadline that withdraws an Accept in progress: in the
// past, so the netpoller wakes the acceptor at once with a timeout, no
// connection taken — Go checks a listener's deadline before accept(2).
var gateExpired = time.Unix(1, 0)

// deadlineListener is a listener whose Accept a deadline bounds: a TCP or
// Unix listener of the standard library; a TLS codec group's, whose layer
// hides SetDeadline, is not one.
type deadlineListener interface {
	stdnet.Listener
	// SetDeadline bounds Accept.
	SetDeadline(t time.Time) error
}

// acceptGate keeps a Backlog group's acceptors on the portable backend from
// taking a connection while no lane waits for work (#978). An acceptor runs
// beside the lanes: left alone it accepts into a lane's event channel while
// the lane is busy or held, up to the channel's 512 events and one more in
// hand — a queue past the one Backlog sizes, of connections nobody serves
// yet. With the gate, an acceptor calls Accept only while a lane waits in its
// select, and the last lane to wake withdraws the Accepts in progress with an
// expired deadline, and waits until they have returned: the netpoller lets a
// readiness that arrives after the deadline fired, but before the woken
// acceptor runs, win over it (runtime/netpoll.go: netpollunblock leaves the
// descriptor pdNil, the I/O sets pdReady, netpollblock returns true), and
// Accept then takes the connection — an acceptor starved of CPU took one a
// held lane never served. A connection an Accept returns before the lane's
// leave completes arrived while the lane waited; past it, a busy or held lane
// takes nothing, and the kernel's queue is the only one. A listener a deadline
// does not reach — a TLS codec group's — is not waited for, and keeps at most
// its acceptor's one in hand. The drain and Close open the gate for good
// (off), so an acceptor sees its listener close. Every wait is on one
// condition, changed, so no channel is ever closed twice.
type acceptGate struct {
	// mu guards every field below.
	mu sync.Mutex
	// changed is signalled on mu when a lane waits again, the last
	// withdrawable Accept returned, or the gate opened for good.
	changed sync.Cond
	// idle counts the lanes waiting in run's select.
	idle int
	// accepting counts the acceptors inside an Accept a deadline withdraws.
	accepting int
	// off is set once the group drains or stops: the gate stays open.
	off bool
	// lns are the acceptors' listeners a deadline reaches.
	lns []deadlineListener
}

// newAcceptGate is the gate of a group accepting on lns, closed until a lane
// waits.
func newAcceptGate(lns []stdnet.Listener) *acceptGate {
	g := &acceptGate{}
	g.changed.L = &g.mu
	//: the listeners a deadline reaches.
	for _, ln := range lns {
		//: one that takes a deadline.
		if d, ok := ln.(deadlineListener); ok {
			g.lns = append(g.lns, d)
		}
	}
	//: closed.
	return g
}

// enter records a lane about to wait for an event: the first one opens the
// gate, the deadline it set cleared, and releases a lane still waiting in
// leave for an acceptor it no longer needs kept out.
func (g *acceptGate) enter() {
	//: a group with no Backlog has no gate.
	if g == nil {
		//: nothing.
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.idle++
	//: the first waiting lane, on a gate still in use.
	if g.idle == 1 && !g.off {
		g.setDeadlines(time.Time{})
		g.changed.Broadcast()
	}
}

// leave records a lane that woke: the last one withdraws the Accepts in
// progress and waits until they returned — unless a lane waits again
// meanwhile, or the gate opens for good.
func (g *acceptGate) leave() {
	//: a group with no Backlog has no gate.
	if g == nil {
		//: nothing.
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.idle--
	//: a lane still waits, or the gate is open for good.
	if g.idle > 0 || g.off {
		//: the gate stays open.
		return
	}
	//: an acceptor inside an Accept a deadline reaches: withdrawn, and out.
	if g.accepting > 0 {
		g.setDeadlines(gateExpired)
	}
	//: until every such Accept returned.
	for g.accepting > 0 && g.idle == 0 && !g.off {
		g.changed.Wait()
	}
}

// reaches reports whether a deadline withdraws an Accept on ln.
func (g *acceptGate) reaches(ln stdnet.Listener) bool {
	_, ok := ln.(deadlineListener)
	//: a gate, and a listener that takes a deadline.
	return g != nil && ok
}

// wait blocks an acceptor until a lane waits or the gate is off, and counts
// it inside Accept when counted — its listener one a deadline reaches —;
// done ends that count.
func (g *acceptGate) wait(counted bool) {
	//: a group with no Backlog has no gate.
	if g == nil {
		//: nothing to wait for.
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	//: until a lane waits, or for good.
	for !g.off && g.idle == 0 {
		g.changed.Wait()
	}
	//: one a lane may have to wait out.
	if counted {
		g.accepting++
	}
}

// done records a counted acceptor out of Accept, waking a lane that waits
// for the last one.
func (g *acceptGate) done(counted bool) {
	//: not counted, or no gate.
	if !counted {
		//: nothing.
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.accepting--
	//: the last one out.
	if g.accepting == 0 {
		g.changed.Broadcast()
	}
}

// release opens the gate for good — the group drains or stops — its
// deadlines cleared, so every acceptor accepts until its listener closes.
func (g *acceptGate) release() {
	//: a group with no Backlog has no gate.
	if g == nil {
		//: nothing.
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	//: once.
	if g.off {
		//: already open.
		return
	}
	g.off = true
	g.setDeadlines(time.Time{})
	g.changed.Broadcast()
}

// setDeadlines sets every listener's deadline to t. The caller holds mu.
func (g *acceptGate) setDeadlines(t time.Time) {
	//: each one a deadline reaches; a listener closed meanwhile refuses it.
	for _, ln := range g.lns {
		swallowErr(ln.SetDeadline(t))
	}
}
