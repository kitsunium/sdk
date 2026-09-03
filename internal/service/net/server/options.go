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

// HandshakeTimeout bounds the TLS negotiation on a group's listeners.
//
// Unset does NOT mean unbounded here, unlike the other three: the handshake is
// the phase a peer can stall before any handler exists to notice, so it falls
// back to the domain's default rather than to no bound at all.
func HandshakeTimeout(d time.Duration) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.timeouts.Handshake = corenet.DurationValue(d)
	}
}

// ReadBufferSize sizes the per-connection scratch buffer handed to the handler.
func ReadBufferSize(n int) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.limits.ReadBufferSize = n
	}
}

// MaxPacketSize caps the datagram size a group accepts. A larger datagram is
// dropped and counted in State, never delivered as a truncated prefix.
func MaxPacketSize(n int) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.limits.MaxPacketSize = n
	}
}

// BatchSize sets how many datagrams one read attempts to collect. One disables
// batching explicitly; zero selects the platform default. Where the platform
// cannot batch, State reports the degradation rather than hiding it.
func BatchSize(n int) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.limits.BatchSize = n
	}
}

// Shards sets how many listeners to open on each of the group's addresses.
//
// Several listeners on one address is what SO_REUSEPORT buys: the kernel
// load-balances incoming connections across them, so N accept loops never
// contend on one accept queue. Zero selects one per core; one disables sharding.
// Where the platform or the socket family cannot shard, the count collapses to
// one and State reports why rather than staying silent.
func Shards(n int) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.limits.Shards = n
	}
}

// MaxConns caps how many connections the group serves at once.
//
// The budget is per group, not per socket: a group listening on a TCP port and
// a Unix socket, or sharded across several listeners, shares one ceiling —
// which is what an operator sizing a server actually means. Beyond it a
// connection is accepted and closed immediately with ConnLimitReached, because
// a refusal the peer can observe beats a timeout it cannot tell from a hang.
// Zero means no ceiling.
func MaxConns(n int) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.limits.MaxConns = n
	}
}

// Adopt takes over a socket inherited from a supervisor instead of binding one.
//
// Socket activation is what makes a zero-downtime restart possible: the
// supervisor holds the bound socket across the exec, so no connection is lost
// and no bind races. The name is the one the unit file publishes in
// LISTEN_FDNAMES. Repeat the option to adopt several sockets into one group.
//
// A named socket the supervisor did not pass is SocketAdoptFailed, never a
// silent fallback to binding: a service that quietly binds its own port has
// lost exactly the property activation exists to provide.
func Adopt(names ...string) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.adopt = append(g.adopt, names...)
	}
}
