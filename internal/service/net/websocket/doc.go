// Package websocket — the closing handshake's payload (RFC 6455 §5.5.1): a
// two-byte status code and an optional UTF-8 reason, written and read here.
// The close code and the predicates that govern it — Sendable, Echoable — are
// the domain's (corenet.WSCloseCode); this is their wire form (ADR 0160 §4).
//
// Package websocket — the frame header and its wire form (RFC 6455 §5).
//
// Reading and writing frames is this package's mechanism, not the domain's
// contract (ADR 0160 §4): it moved here from internal/core/net, which keeps
// what a second implementation would share — the opcode, the close code, the
// message and the control-frame ceiling (corenet.WSMaxControlPayload).
//
// Package websocket — the RFC 6455 §4.2 opening handshake.
//
// Package websocket — the opening handshake's key exchange (RFC 6455 §4.1,
// §4.2.2): the check that a Sec-WebSocket-Key is a nonce, and the digest that
// answers it. The protocol's constants — the GUID, the version, the header
// names — are the domain's (corenet.WSGUID and its neighbours).
//
// Package websocket — the connection's functional options and their defaults.
//
// Package websocket — the UTF-8 rule a text message must satisfy (RFC 6455
// §8.1), checked on the bytes as they come off and go onto the wire. The
// message itself is the domain's value (corenet.WSMessageValue).
//
// Package websocket is the server side of RFC 6455: an HTTP request upgraded
// into a bidirectional, message-oriented connection over the same socket.
//
// It is written against net/http's own interfaces, not against this SDK's
// listener engine, so it works inside any http.Handler. Mounted on the SDK
// engine it additionally observes the server's drain signal, which is what
// stops a connection that by design never ends from being severed under its
// handler on every deployment.
//
// permessage-deflate and every other extension are deliberately NOT negotiated;
// the reserved frame bits an extension would use are refused as protocol
// errors. See the package CLAUDE.md.
package websocket
