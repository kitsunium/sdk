//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/net/websocket .

// Package websocket is the server side of RFC 6455: an HTTP request upgraded
// into a bidirectional, message-oriented connection over the same socket.
//
// # The whole thing
//
//	func handler(w http.ResponseWriter, r *http.Request) {
//		conn, err := websocket.Upgrade(w, r)
//		if err != nil {
//			return // Upgrade has already written the HTTP refusal
//		}
//		defer conn.Close()
//
//		for {
//			msg, rerr := conn.Receive()
//			if rerr != nil {
//				return
//			}
//			if serr := conn.Send(msg); serr != nil {
//				return
//			}
//		}
//	}
//
// It is written against net/http's own interfaces, so it works in any
// http.Handler. Mounted on this SDK's engine — Group.HandleHTTP — it
// additionally observes the server's drain signal, which is what stops a
// connection that by design never ends from being severed under its handler on
// every deployment.
//
// # Messages, not frames
//
// [Conn].Receive returns whole messages. Fragmentation is the sender's private
// choice of chunk size, not a semantic boundary, so surfacing it would put an
// implementation detail of the peer's writer into every consumer. Control
// frames — Ping, Pong, Close — are answered underneath and never surface as
// messages: they are transport, not payload.
//
// The returned [Message].Data aliases the connection's reassembly buffer and is
// valid until the next [Conn].Receive. Keep it longer and you must copy it.
// That is what makes a steady-state read allocate nothing.
//
// # What is refused, and why refusing is the specification
//
// RFC 6455 is unusual in how much of it is stated as MUST-fail rather than
// should-tolerate, because a frame stream that keeps going after a
// disagreement is two endpoints reading different messages from the same bytes.
// This implementation refuses:
//
//   - an unmasked client frame (§5.1) — masking is what stops a hostile script
//     from steering a browser into emitting bytes a transparent proxy would
//     read as a second, attacker-chosen HTTP request;
//   - a set reserved bit or a reserved opcode (§5.2);
//   - a fragmented or over-125-byte control frame (§5.5);
//   - a length not in its minimal encoding (§5.2);
//   - a continuation with no message in progress, or a new data frame
//     interrupting one (§5.4);
//   - a text message that is not valid UTF-8 (§8.1), judged on the reassembled
//     message so a sequence straddling a fragment boundary stays valid;
//   - a close code that must never travel — 1004, 1005, 1006, 1015 and the
//     unallocated ranges (§7.4.2) — in either direction.
//
// # Bounds
//
// A frame announces its length in a 64-bit field the PEER writes.
// [MaxFrameSize] is checked against that announcement before a single byte is
// read or allocated, and [MaxMessageSize] against the accumulated total, since
// fragmentation lets a peer exceed any per-frame bound a thousand small frames
// at a time. Neither has an "unbounded" setting on purpose.
//
// # Extensions
//
// None are negotiated, permessage-deflate included. The server sends no
// Sec-WebSocket-Extensions header — which RFC 6455 §4.2.2 defines as the way to
// say "no extension is in use" — and the frame reader enforces the same answer
// by refusing any reserved bit an extension would have set. Compression is
// therefore a refusal the wire can verify, not a promise in a document.
//
// # Origin
//
// The browser's same-origin policy does not apply to WebSocket: any page can
// open a connection to this server, and the browser attaches the user's cookies
// to the handshake. By default an Origin header, when present, must name the
// request's own host and port — and, when this server terminates TLS itself,
// the https scheme as well, because an http:// page for the same host is
// exactly the downgrade the check exists to notice. A request with no Origin (a
// CLI, a service, a Go client) is allowed, because there is no ambient
// credential to abuse; the opaque "null" origin is refused.
//
// Behind a proxy that terminates TLS, the request reaches this server in
// plaintext whatever the browser used, so the default rule cannot see the
// scheme. It does not guess it either: a request carrying Forwarded or
// X-Forwarded-Proto on a connection this server did not terminate is refused,
// with a message naming what to configure. Only the PRESENCE of those headers
// is read, never their value — one a client writes can make the check stricter
// and never looser. [AllowOrigins] names the origins outright and replaces the
// default rule entirely; [AllowAnyOrigin] removes the check, by name.
//
// # One goroutine always reads
//
// Every connection needs exactly one goroutine looping on [Conn].Receive for
// its whole life — including a connection the handler only ever writes to.
// That loop is where the peer's Ping is answered (RFC 6455 §5.5.2), where its
// Close is replied to (§5.5.1), and the only place the heartbeat can see that
// the peer is alive. A handler that pushes runs it in a goroutine of its own
// and discards what it reads:
//
//	func push(w http.ResponseWriter, r *http.Request) {
//		conn, err := websocket.Upgrade(w, r)
//		if err != nil {
//			return
//		}
//		defer conn.Close()
//
//		go func() { // the reader: required even with nothing to read
//			for {
//				if _, rerr := conn.Receive(); rerr != nil {
//					return
//				}
//			}
//		}()
//
//		for {
//			select {
//			case <-conn.Done():
//				return
//			case event := <-events: // the application's own feed
//				if serr := conn.SendText(event); serr != nil {
//					return
//				}
//			}
//		}
//	}
//
// The reading goroutine needs no joining: the deferred Close ends the
// connection, and its Receive returns the terminal error.
//
// # Liveness and shutdown
//
// A peer that vanishes without closing leaves a socket that is perfectly
// readable and will simply never produce another byte. The heartbeat is the
// only thing that turns that silence into an ending: every [PingInterval] it
// sends a Ping, and one interval later it ends the connection if the handler
// has READ no frame since. It counts frames the handler has read, not frames
// that arrived — a Pong the peer sent on time is silence to it until
// [Conn].Receive reads it — so a connection nobody reads is ended within two
// intervals, about a minute at [DefaultPingInterval], however healthy the peer
// is. Zero is clamped to [DefaultPingInterval] rather than meaning "never", and
// "never" is spelled [WithoutPing].
//
// [Conn].Done closes when the peer sends Close, when the socket dies, when the
// heartbeat gives up, when the handler closes it, or when the server begins
// draining — in which case the connection sends a 1001 "going away" of its own
// accord. [Conn].Receive and [Conn].Send refuse from the same instant, so the
// reading loop and a pushing loop both terminate too.
//
// # Clients
//
// This package is the server half only. The SDK's outbound client returns a
// fully-read body by design and cannot dial an upgrade; see the package
// CLAUDE.md for why that is a separate decision rather than an omission.
package websocket
