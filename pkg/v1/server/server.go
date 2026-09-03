//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/server .

// Package server is the inbound half of the SDK's network domain (ADR 0029):
// one unified listener engine for TCP, Unix, TLS and mutual TLS, serving
// pluggable handlers grouped behind shared middlewares.
//
// # The whole thing
//
//	srv := server.New()
//	srv.Group("echo", server.Listen("tcp", ":8080")).
//		HandleFunc(func(ctx context.Context, c server.Conn) error {
//			_, err := io.Copy(c, c)
//			return err
//		})
//	err := srv.Serve(ctx)
//
// That is the design target, not a simplified excerpt. [Conn] embeds net.Conn,
// so a handler reads and writes a connection exactly as it would any socket and
// every io helper keeps working — io.Copy, bufio, encoding wrappers. Everything
// the domain adds is additive.
//
// # Groups
//
// A group is a set of listeners sharing one handler, one middleware chain and
// one policy. It exists so the same handler can answer on a TCP port and a Unix
// socket without wiring it twice:
//
//	srv.Group("api",
//		server.Listen("tcp", ":8443"),
//		server.Listen("unix", "/run/api.sock"),
//		server.TLS(identity),
//		server.IdleTimeout(30*time.Second),
//	).Use(mw).Handle(handler)
//
// [Group] returns the group rather than a (group, error) pair on purpose: a
// declaration mistake — a duplicate name, an unusable address, a missing
// handler — is recorded and reported by [Server.Start]. The declaration chain
// stays readable, and nothing is swallowed.
//
// # TLS and mutual TLS
//
// One option covers both. A [github.com/kitsunium/sdk/pkg/v1/tlsid.Identity]
// built with RequireClientCert turns the group's listeners into mutual-TLS
// listeners; the same identity type serves the outbound client, so the two
// cannot drift apart. The handshake runs on the connection's own goroutine, so
// a slow or hostile peer cannot stall the accept path for everyone else.
//
// # Lifecycle
//
// [Server.Start] returns once every listener is bound, so a nil error means the
// ports are open. [Server.Serve] starts, blocks until the context is cancelled,
// then drains. [Server.Shutdown] stops accepting and waits for in-flight work
// within a budget, severing what remains when the budget expires — a shutdown
// that never returns is worse than one that admits it gave up.
//
// [Server.State] reports the phase, the addresses actually bound, and whether
// any listener fell back from a requested optimisation. A silent degradation is
// indistinguishable from a working server, so it is surfaced rather than logged
// once at startup.
package server

import (
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	svcserver "github.com/kitsunium/sdk/internal/service/net/server"
	"github.com/kitsunium/sdk/pkg/v1/tlsid"
)

// Server owns a set of listener groups and their lifecycle.
type Server = svcserver.Server

// Group is a set of listeners sharing one handler, middleware chain and policy.
//
// Group.HandleHTTP mounts an http.Handler on the group's listeners: the group
// keeps its own limits, TLS identity and drain, and net/http only does the
// protocol.
type Group = svcserver.StreamGroup

// Conn is one accepted stream connection. It embeds net.Conn.
type Conn = corenet.Conn

// Handler serves one accepted stream connection.
type Handler = corenet.ConnHandler

// HandlerFunc adapts a plain function to Handler.
type HandlerFunc = corenet.ConnHandlerFunc

// Middleware decorates a handler with another of the same type.
type Middleware = corenet.Middleware[corenet.ConnHandler]

// State is a snapshot of the server's lifecycle and listeners.
type State = corenet.StateValue

// ListenerState reports one bound listener, including any fallback.
type ListenerState = corenet.ListenerStateValue

// Phase is where a server sits in its lifecycle.
type Phase = corenet.Phase

// The lifecycle phases, in the order a server passes through them. They are
// re-exported so a caller can compare State().Phase without importing the
// internal package, which Go's firewall forbids anyway.
// PhaseNew is a constructed server that has not bound anything yet.
const PhaseNew Phase = corenet.PhaseNew

// PhaseStarting is binding listeners; some may already be up.
const PhaseStarting Phase = corenet.PhaseStarting

// PhaseServing is bound and accepting.
const PhaseServing Phase = corenet.PhaseServing

// PhaseDraining has stopped accepting and is waiting for in-flight work.
const PhaseDraining Phase = corenet.PhaseDraining

// PhaseStopped has released every listener.
const PhaseStopped Phase = corenet.PhaseStopped

// Option configures a Server.
type Option = svcserver.Option

// GroupOption configures a Group.
type GroupOption = svcserver.GroupOption

// Sentinels returned by this package. Match with errors.Is or errs.HasCode.
var (
	// ListenFailed reports a listener that could not be bound.
	ListenFailed = corenet.ListenFailed
	// InvalidAddress reports an unusable listen address.
	InvalidAddress = corenet.InvalidAddress
	// UnsupportedNetwork reports a socket family the domain does not serve.
	UnsupportedNetwork = corenet.UnsupportedNetwork
	// AlreadyStarted reports a second Start on a running server.
	AlreadyStarted = corenet.AlreadyStarted
	// HandlerMissing reports a group declared without a handler.
	HandlerMissing = corenet.HandlerMissing
	// GroupDuplicate reports a second group declared under one name.
	GroupDuplicate = corenet.GroupDuplicate
	// DrainTimeout reports a shutdown whose budget expired with work in flight.
	DrainTimeout = corenet.DrainTimeout
	// ConnLimitReached reports a connection turned away by the group ceiling.
	ConnLimitReached = corenet.ConnLimitReached
)

// New builds a server.
func New(opts ...Option) *Server {
	//: the service layer owns the wiring; this facade only forwards.
	return svcserver.New(opts...)
}

// Chain applies middlewares to a handler, outermost first.
//
// Chain(h, a, b, c) yields a(b(c(h))): the first middleware listed is the first
// to see a connection, matching how the list reads at the call site.
func Chain(h Handler, middlewares ...Middleware) Handler {
	//: the generic core helper serves both handler natures.
	return corenet.Chain(h, middlewares...)
}

// Listen adds an address to a group. Repeat it to bind several addresses to one
// handler — a TCP port and a Unix socket, for instance.
func Listen(network, addr string) GroupOption {
	//: forwarded unchanged.
	return svcserver.Listen(network, addr)
}

// TLS turns the group's listeners into TLS listeners, or mutual-TLS ones when
// the identity requires a client certificate.
func TLS(id tlsid.Identity) GroupOption {
	//: one option covers both, so the two cannot drift apart.
	return svcserver.TLS(id)
}

// ReadTimeout bounds one read from the peer.
func ReadTimeout(d time.Duration) GroupOption {
	//: forwarded unchanged.
	return svcserver.ReadTimeout(d)
}

// WriteTimeout bounds one write to the peer.
func WriteTimeout(d time.Duration) GroupOption {
	//: forwarded unchanged.
	return svcserver.WriteTimeout(d)
}

// IdleTimeout bounds how long a connection may sit with no traffic at all.
func IdleTimeout(d time.Duration) GroupOption {
	//: forwarded unchanged.
	return svcserver.IdleTimeout(d)
}

// ReadBufferSize sizes the per-connection scratch buffer handed to the handler
// by Conn.Buffer. It is recycled with the connection, which is what lets a
// handler read without allocating per connection.
func ReadBufferSize(n int) GroupOption {
	//: forwarded unchanged.
	return svcserver.ReadBufferSize(n)
}

// WithDrainTimeout bounds how long Shutdown waits for in-flight connections
// before severing them.
func WithDrainTimeout(d time.Duration) Option {
	//: forwarded unchanged.
	return svcserver.WithDrainTimeout(d)
}

// PacketGroup is a set of datagram sockets sharing one handler and chain.
type PacketGroup = svcserver.PacketGroup

// Packet is one received datagram.
type Packet = corenet.Packet

// PacketHandler serves one received datagram.
type PacketHandler = corenet.PacketHandler

// PacketHandlerFunc adapts a plain function to PacketHandler.
type PacketHandlerFunc = corenet.PacketHandlerFunc

// PacketMiddleware decorates a datagram handler.
type PacketMiddleware = corenet.Middleware[corenet.PacketHandler]

// MaxPacketSize caps the datagram size a group accepts.
//
// A larger datagram is dropped and counted in [State.OversizedPackets], never
// delivered. Truncating would hand the handler a prefix indistinguishable from
// a complete message, which is a correctness problem rather than a capacity
// one; a drop it can observe in State is the honest answer.
func MaxPacketSize(n int) GroupOption {
	//: forwarded unchanged.
	return svcserver.MaxPacketSize(n)
}

// BatchSize sets how many datagrams one read attempts to collect. One disables
// batching; zero selects the platform default. Where the platform cannot batch,
// State reports the degradation rather than hiding it.
func BatchSize(n int) GroupOption {
	//: forwarded unchanged.
	return svcserver.BatchSize(n)
}

// ChainPacket applies middlewares to a datagram handler, outermost first.
func ChainPacket(h PacketHandler, middlewares ...PacketMiddleware) PacketHandler {
	//: the same generic core helper serves both handler natures.
	return corenet.Chain(h, middlewares...)
}

// MaxConns caps how many connections a group serves at once.
//
// The budget is per group, not per socket: a group listening on a TCP port and
// a Unix socket, or sharded across several listeners, shares one ceiling —
// which is what an operator sizing a server actually means. Beyond it a
// connection is accepted and closed immediately with ConnLimitReached, because
// a refusal the peer can observe beats a timeout it cannot tell from a hang.
// Zero means no ceiling.
func MaxConns(n int) GroupOption {
	//: forwarded unchanged.
	return svcserver.MaxConns(n)
}

// Shards sets how many listeners to open on each of the group's addresses.
//
// Zero selects one per core; one disables sharding. Where the platform or the
// socket family cannot shard, the count collapses to one and State reports why.
func Shards(n int) GroupOption {
	//: forwarded unchanged.
	return svcserver.Shards(n)
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
	//: forwarded unchanged.
	return svcserver.Adopt(names...)
}
