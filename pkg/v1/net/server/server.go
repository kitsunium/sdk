//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/net/server .

// Package server — the two group options only an HTTP group reads: the header
// phase's own deadline and the header size cap.
//
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
// handler — is recorded and reported by [Server].Start. The declaration chain
// stays readable, and nothing is swallowed.
//
// # TLS and mutual TLS
//
// One option covers both. A [github.com/kitsunium/sdk/pkg/v1/net/tlsid.Identity]
// built with RequireClientCert turns the group's listeners into mutual-TLS
// listeners; the same identity type serves the outbound client, so the two
// cannot drift apart. The handshake runs on the connection's own goroutine, so
// a slow or hostile peer cannot stall the accept path for everyone else.
//
// # Lifecycle
//
// [Server].Start returns once every listener is bound, so a nil error means the
// ports are open. [Server].Serve starts, blocks until the context is cancelled,
// then drains. [Server].Shutdown stops accepting and waits for in-flight work
// within a budget, severing what remains when the budget expires — a shutdown
// that never returns is worse than one that admits it gave up.
//
// [Server].State reports the phase, the addresses actually bound, and whether
// any listener fell back from a requested optimisation. A silent degradation is
// indistinguishable from a working server, so it is surfaced rather than logged
// once at startup.
//
// An Accept that fails for any reason but its listener closing — the process
// out of file descriptors, typically — is retried after a wait, never at once:
// 5 ms, doubling, held at one second, started over by the next accepted
// connection, as net/http's own Serve does. Retrying at once would spin a core
// per listener for as long as the condition lasts. A shutdown during a wait
// ends it at once, and [State].AcceptBackoffs counts the waits, so a server
// that cannot accept says so.
//
// # Connections that never end
//
// A drain waits for in-flight work to finish, which assumes it eventually does.
// An event stream, a long poll, or anything built on a protocol upgrade breaks
// that assumption: the request is in flight forever by design, so the drain
// waits out its whole budget and then severs the socket under the handler.
//
// [DrainSignal] is the way out. A handler holding a connection open selects on
// it alongside its own work and returns when it closes, which turns a
// budget-length DRAIN_TIMEOUT into a clean drain:
//
//	select {
//	case <-server.DrainSignal(r.Context()):
//		return
//	case ev := <-events:
//		// …
//	}
//
// The request context is deliberately NOT cancelled to say this. Cancelling it
// would tell every handler to abandon the response it is halfway through, which
// is the opposite of what a graceful drain is for. The signal is additive: a
// handler that ignores it behaves exactly as it did before.
//
// [github.com/kitsunium/sdk/pkg/v1/net/sse] is the Server-Sent Events
// implementation built on it, and watches the signal for you.
//
// # Protocol upgrades
//
// [github.com/kitsunium/sdk/pkg/v1/net/websocket] is WebSocket (RFC 6455),
// server side. It hijacks the response, which means the socket stops being the
// engine's: it is neither closed nor waited for by a drain — the same carve-out
// http.Server.Shutdown documents for hijacked connections. The connection
// watches [DrainSignal] and closes itself with a 1001 "going away" instead, so
// a deployment ends it deliberately rather than by severing it.
//
// The same is true of anything else a handler hijacks: once the socket is taken
// over it is the handler's to close.
package server
