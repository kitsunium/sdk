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

// WithDrainGrace sets how long Shutdown keeps accepting and serving once
// readiness has turned false, before it closes any listener: the time a load
// balancer or an orchestrator takes to stop routing to the server. Zero, the
// default, starts closing at once: a loop group still takes its listen queue
// before its listener closes. Serve adds it to the drain budget; a caller of
// Shutdown gives it within its ctx.
func WithDrainGrace(d time.Duration) Option {
	//: the option is applied by the constructor, in declaration order.
	return func(s *Server) {
		s.drainGrace = d
	}
}

// Loop configures a group's loop engine (ADR 0186 §D5): its backend, its loops
// and rings, and its per-connection and pressure bounds. It is judged at Start,
// once the group's handler is known: a group served by HandleLoop runs on it,
// and a group served by Handle, HandleFunc or HandleHTTP refuses it with
// LoopOptionUnsupported, whichever order the declarations came in. A group with
// no handler fails with HandlerMissing instead, and one given a second handler
// with HandlerAlreadySet. A datagram group refuses it with LoopOptionUnsupported
// too. A group served by HandleLoop without it runs on the zero LoopValue.
func Loop(v corenet.LoopValue) GroupOption {
	//: the option is applied by the constructor, in declaration order; it is
	//: judged at Start, once the group's handler is known.
	return func(g *StreamGroup) {
		g.loop = v
		g.loopSet = true
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
		g.kinds.add(corenet.OptionTLS)
	}
}

// ReadTimeout bounds one read from the peer. On an HTTP group it bounds each
// read of a request body, refreshed per read, the header phase unless
// ReadHeaderTimeout is set, and the wait for the next request unless
// IdleTimeout is.
func ReadTimeout(d time.Duration) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.timeouts.Read = corenet.DurationValue(d)
		g.kinds.add(corenet.OptionReadTimeout)
	}
}

// WriteTimeout bounds one write to the peer.
func WriteTimeout(d time.Duration) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.timeouts.Write = corenet.DurationValue(d)
		g.kinds.add(corenet.OptionWriteTimeout)
	}
}

// IdleTimeout bounds how long a connection may sit with no traffic.
func IdleTimeout(d time.Duration) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.timeouts.Idle = corenet.DurationValue(d)
		g.kinds.add(corenet.OptionIdleTimeout)
	}
}

// ReadHeaderTimeout bounds the HEADER phase of an HTTP request on its own: the
// request line and the header fields must arrive within d of the connection
// becoming ready for a request, however long the read timeout lets the body
// take.
//
// The header phase is the one a slowloris attack stalls — a few bytes a
// second, never finishing a line — and it is also the phase a legitimate
// client completes in milliseconds, so it deserves a far shorter bound than a
// read timeout sized for an upload. Unset (or non-positive) keeps what the
// group did before this option existed: the header phase is bounded by the
// read timeout. It has no effect on a group that does not serve HTTP.
func ReadHeaderTimeout(d time.Duration) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.http.readHeaderTimeout = d
		g.kinds.add(corenet.OptionReadHeaderTimeout)
	}
}

// MaxHeaderBytes caps how many bytes net/http reads for one request's line and
// header fields. A request over the cap is answered 431 Request Header Fields
// Too Large and never reaches the handler.
//
// Unset (or non-positive) keeps net/http's own default, 1 MiB
// (http.DefaultMaxHeaderBytes) — which was applying silently before this
// option existed. net/http reads a small fixed allowance past the cap before
// refusing, so the refusal happens a few kilobytes after n, not at the byte.
// It has no effect on a group that does not serve HTTP.
func MaxHeaderBytes(n int) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.http.maxHeaderBytes = n
		g.kinds.add(corenet.OptionMaxHeaderBytes)
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
		g.kinds.add(corenet.OptionHandshakeTimeout)
	}
}

// ReadBufferSize sizes the per-connection scratch buffer handed to the handler.
func ReadBufferSize(n int) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.limits.ReadBufferSize = n
		g.kinds.add(corenet.OptionReadBufferSize)
	}
}

// MaxPacketSize caps the datagram size a group accepts. A larger datagram is
// dropped and counted in State, never delivered as a truncated prefix.
func MaxPacketSize(n int) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.limits.MaxPacketSize = n
		g.kinds.add(corenet.OptionMaxPacketSize)
	}
}

// BatchSize sets how many datagrams one read attempts to collect. One disables
// batching explicitly; zero selects the platform default. Where the platform
// cannot batch, State reports the degradation rather than hiding it.
func BatchSize(n int) GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.limits.BatchSize = n
		g.kinds.add(corenet.OptionBatchSize)
	}
}

// BatchReplies makes a datagram group's replies leave per read batch rather than
// one at a time (ADR 0192). Packet.Reply made while the batch is served then
// queues a copy of its datagram and reports it sent, len(b) and nil; the batch's
// replies leave together when it ends, and one that fails then is counted in
// State's Datagrams.ReplyFailures, since its handler has returned. On Linux a UDP
// socket sends them with one sendmmsg, a run of consecutive replies to one peer
// of one size as one UDP GSO send, split at 64 segments or the IP length, and
// one datagram a message once the device refused GSO (EIO); a zoned IPv6 peer
// is written at once, and a Unix datagram socket, a socket with no raw
// descriptor and every other platform send the queue one WriteTo a reply.
// A queue that is full — 128 replies or 256 KiB — sends what it holds early,
// before the batch ends; a reply larger than that room, or to a peer the sink
// cannot address, is written at once and returns the write's own error, never
// counted. Under the option Reply is valid only during the handler's call, as
// Data is:
// the Packet is the read loop's, and one kept and replied to while a later
// batch is served is queued with that batch. A reply made while no batch is
// served is written at once. Without the option, Reply writes at once and
// returns the write's own error. A stream group refuses it with
// LoopOptionUnsupported.
func BatchReplies() GroupOption {
	//: the option is applied by the constructor, in declaration order.
	return func(g *StreamGroup) {
		g.kinds.add(corenet.OptionBatchReplies)
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
		g.kinds.add(corenet.OptionShards)
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
		g.kinds.add(corenet.OptionMaxConns)
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
		g.kinds.add(corenet.OptionAdopt)
	}
}

// Plaintext declares that the group serves no TLS (ADR 0187). Every engine
// honours it. A group given both Plaintext and TLS, in either order, fails
// Start with PlaintextWithTLS. An HTTP group without TLS must give it, or
// fails Start with PlaintextRequired (ADR 0197).
func Plaintext() GroupOption {
	//: the option is applied by the constructor, in declaration order; the
	//: contradiction with TLS is judged at Start.
	return func(g *StreamGroup) {
		g.kinds.add(corenet.OptionPlaintext)
	}
}

// Name names the group in State, logs and errors (ADR 0187). On a group
// declared with an empty name it sets the name, which must then be unique like
// any other; on one declared with a name it must repeat it. An empty Name, or
// one that differs from a non-empty declared name, fails Start with
// GroupNameConflict.
func Name(name string) GroupOption {
	//: the option is applied by the constructor, in declaration order; the
	//: declaration reconciles it with the declared name.
	return func(g *StreamGroup) {
		g.optName = name
		g.kinds.add(corenet.OptionName)
	}
}

// Engine names the engine family the group runs on (ADR 0187): EngineGoroutine,
// one goroutine per connection, or EngineLoop, the loop engine; Loop then
// picks the loop's backend. The zero value, EngineAuto, lets the handler's
// type choose, which is what a group without the option does. An engine the
// group's handler cannot run on, or that another option contradicts —
// EngineGoroutine with Loop or HandleLoop, EngineLoop with Handle, HandleFunc
// or HandleHTTP or on a datagram group — fails Start with EngineUnsupported,
// and is never replaced by the other engine.
func Engine(e corenet.EngineValue) GroupOption {
	//: the option is applied by the constructor, in declaration order; it is
	//: judged at Start, once the group's handler is known.
	return func(g *StreamGroup) {
		g.engine = e
		g.kinds.add(corenet.OptionEngine)
	}
}
