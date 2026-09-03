// Package server — the bounded TLS handshake.
package server

import (
	"context"
	"crypto/tls"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// defaultHandshakeTimeout bounds the TLS negotiation when a group sets none.
//
// It has a default where Read, Write and Idle deliberately do not, because the
// handshake is the one phase a peer can stall before the application sees
// anything at all: no handler has run, no request exists, and nothing else in
// the stack has a reason to time it out. tls.NewListener does not handshake in
// Accept — the negotiation happens on the connection's own goroutine, which is
// the right design — so without a bound here a peer that opens a socket and
// says nothing pins a goroutine, an in-flight token, a live-socket registry
// entry and a slot under the group's connection ceiling, for free and forever.
const defaultHandshakeTimeout time.Duration = 10 * time.Second

// handshakeHandler completes the TLS negotiation under its own bound before
// handing the connection to the group's handler.
//
// It is applied once per group rather than per connection, and only to groups
// that actually carry a TLS identity, so a plaintext group pays nothing at all.
// It sits INSIDE the connection ceiling on purpose: negotiating first would let
// a flood of connections each pay for a full handshake before the ceiling had a
// chance to refuse them, which is the opposite of what the ceiling is for.
type handshakeHandler struct {
	// next is the group's own handler chain.
	next corenet.ConnHandler
	// timeouts carries the group's handshake bound.
	timeouts corenet.TimeoutsValue
}

// ServeConn implements corenet.ConnHandler.
func (h handshakeHandler) ServeConn(ctx context.Context, c corenet.Conn) error {
	//: a failed or stalled negotiation ends the connection here; the engine
	//: closes it on the way out, exactly as it would any handler error.
	if err := boundHandshake(ctx, c, h.timeouts); err != nil {
		//: the peer never completed the negotiation.
		return err
	}
	//: negotiated, so the handler sees a connection that is genuinely secured.
	return h.next.ServeConn(ctx, c)
}

// boundHandshake negotiates TLS under a deadline and then clears it.
func boundHandshake(ctx context.Context, c corenet.Conn, timeouts corenet.TimeoutsValue) error {
	tlsConn, ok := rawSocket(c).(*tls.Conn)
	//: a plaintext listener has no negotiation to bound.
	if !ok {
		//: nothing to do on a plaintext connection.
		return nil
	}
	budget := handshakeBudget(timeouts)
	swallowErr(tlsConn.SetDeadline(time.Now().Add(budget)))
	err := tlsConn.HandshakeContext(ctx)
	//: clear it again: this deadline belongs to the negotiation, and leaving it
	//: in place would silently bound the handler's first read by whatever was
	//: left of the handshake budget.
	swallowErr(tlsConn.SetDeadline(time.Time{}))
	//: whatever the negotiation produced.
	return err
}

// handshakeBudget returns how long the negotiation may take.
func handshakeBudget(timeouts corenet.TimeoutsValue) time.Duration {
	//: an explicit bound always wins.
	if configured := timeouts.Handshake.Duration(); configured > 0 {
		//: the group said how long it is willing to wait.
		return configured
	}
	//: unset must not mean unbounded here; this is the cheapest phase to stall.
	return defaultHandshakeTimeout
}
