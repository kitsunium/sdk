// Package server — the per-group connection ceiling.
package server

import (
	"context"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// connLimiter caps how many connections a group holds at once.
//
// It is a reject-mode channel semaphore: a free slot admits, a full one
// refuses at once, nothing queues. That is exactly the resilience bulkhead's
// shape, and the ceiling used to BE a bulkhead — until hijacking showed what a
// Runner cannot express. A Runner holds its slot for exactly one call, and a
// connection a handler takes over outlives the call that admitted it: every
// upgraded WebSocket gave its slot back the moment it upgraded, so MaxConns
// bounded nothing an upgrade reached. The slot is therefore held explicitly
// here — returned when the handler returns, or handed to the socket itself
// when the handler took the socket over, and returned by its Close.
type connLimiter struct {
	// slots is the semaphore; its capacity is the ceiling.
	slots chan struct{}
	// ceiling is the configured maximum, reported in the refusal.
	ceiling int
}

// newConnLimiter returns the group's ceiling, or nil when it has none.
func newConnLimiter(limits corenet.LimitsValue) *connLimiter {
	//: zero means "no ceiling", so the hot path skips the policy entirely.
	if limits.MaxConns <= 0 {
		//: no ceiling, so the hot path skips the policy entirely.
		return nil
	}
	//: the ceiling is carried alongside so a refusal can name the number it hit.
	return &connLimiter{
		slots:   make(chan struct{}, limits.MaxConns),
		ceiling: limits.MaxConns,
	}
}

// tryAcquire claims a slot without waiting, reporting whether one was free.
func (l *connLimiter) tryAcquire() bool {
	select {
	//: claimed; the caller now owes exactly one release.
	case l.slots <- struct{}{}:
		//: admitted.
		return true
	//: every slot is held — refuse now rather than queue a peer that cannot
	//: tell a queue from a hang.
	default:
		//: refused.
		return false
	}
}

// release returns one claimed slot.
func (l *connLimiter) release() {
	//: one receive per successful tryAcquire.
	<-l.slots
}

// releaseFor settles the slot c was admitted with once its handler is done:
// returned now, or — when a handler took c's socket over — handed to the
// socket, whose Close returns it.
//
// A hijacked connection stops being the engine's to close or to wait for (ADR
// 0047 §D9), but it is still a connection, and MaxConns counts connections.
// Its Close is the only event left that says it is over.
func (l *connLimiter) releaseFor(c corenet.Conn) {
	//: the handler took the socket over, and the group tracks closes, so the
	//: socket itself will say when the connection is over.
	if tracked := hijackedTracker(c); tracked != nil {
		tracked.holdSlot(l)
		//: the slot now belongs to the socket.
		return
	}
	//: the connection ends with its handler, and so does its slot.
	l.release()
}

// admit runs the handler under the group's ceiling, translating a rejection
// into the domain's own sentinel.
//
// A connection turned away here has already been accepted by the kernel, so it
// is answered by closing it immediately rather than by letting the accept queue
// overflow silently — a refusal the peer can observe beats a timeout it cannot
// distinguish from a hang.
func (s *Server) admit(ctx context.Context, limiter *connLimiter, c corenet.Conn, handler corenet.ConnHandler) error {
	//: no ceiling configured — call the handler directly, with no closure and
	//: no policy on the hot path.
	if limiter == nil {
		//: straight to the handler, with no closure on the hot path.
		return handler.ServeConn(ctx, c)
	}
	//: the ceiling rejected this connection.
	if !limiter.tryAcquire() {
		s.rejected.Add(1)
		//: report in the domain's own terms, naming the ceiling it hit.
		return errs.Wrap(corenet.ConnLimitReached, errs.WrapParams{},
			errs.Int("limit", limiter.ceiling))
	}
	//: deferred, so a handler that panics still settles its slot — a leaked
	//: slot wedges the group after MaxConns connections.
	defer limiter.releaseFor(c)
	//: whatever the handler itself produced.
	return handler.ServeConn(ctx, c)
}
