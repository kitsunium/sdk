// Package server — the functional options.
package server

import (
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// Option configures a Server.
type Option func(*Server)

// GroupOption configures a StreamGroup.
type GroupOption func(*StreamGroup)

// WithDrainTimeout bounds how long Shutdown waits for in-flight connections
// before closing them hard.
func WithDrainTimeout(d time.Duration) Option {
	//: the option is applied by the constructor, in declaration order.
	return func(s *Server) {
		s.drainTimeout = d
	}
}

// Listen adds an address to a group. It is variadic-friendly by repetition:
// calling it twice binds two addresses to the same handler, which is how one
// service reaches both a TCP port and a Unix socket.
func Listen(network, addr string) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.addrs = append(g.addrs, corenet.AddressValue{Network: network, Addr: addr})
	}
}

// TLS turns the group's listeners into TLS listeners, or mutual-TLS ones when
// the identity requires a client certificate. One option covers both, so the
// two cannot drift apart in configuration.
func TLS(id corenet.IdentityValue) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.identity = id
	}
}

// ReadTimeout bounds one read from the peer.
func ReadTimeout(d time.Duration) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.timeouts.Read = corenet.DurationValue(d)
	}
}

// WriteTimeout bounds one write to the peer.
func WriteTimeout(d time.Duration) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.timeouts.Write = corenet.DurationValue(d)
	}
}

// IdleTimeout bounds how long a connection may sit with no traffic.
func IdleTimeout(d time.Duration) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.timeouts.Idle = corenet.DurationValue(d)
	}
}

// ReadBufferSize sizes the per-connection scratch buffer handed to the handler.
func ReadBufferSize(n int) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.limits.ReadBufferSize = n
	}
}
