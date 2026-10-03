// Package net — the WebSocket protocol's vocabulary (RFC 6455): the opening
// handshake's constants and the control-frame ceiling. Reading and writing the
// protocol — the handshake's key check and digest, the frame codec, the close
// payload, the UTF-8 check — is internal/service/net/websocket's (ADR 0160 §4).
package net

// WSGUID is the fixed string RFC 6455 §1.3 concatenates to Sec-WebSocket-Key
// before hashing. It is a constant of the protocol, not a secret and not a
// parameter: its only job is to make the accept value impossible to produce by
// echoing the request, so a cache or a naive proxy cannot fake an upgrade.
const WSGUID string = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WSVersion is the only protocol version this domain speaks (RFC 6455 §4.1).
// A request naming any other version is refused WITH this value advertised
// back, which is what lets a client renegotiate instead of guessing.
const WSVersion string = "13"

// WSKeyLen is how many bytes Sec-WebSocket-Key decodes to (RFC 6455 §4.1).
// A key of any other length is not a WebSocket handshake, whatever it decodes.
const WSKeyLen int = 16

// The handshake headers, spelled once so no caller mistypes one into a silent
// miss.
const (
	// WSKeyHeader carries the client's 16 random bytes, base64-encoded.
	WSKeyHeader string = "Sec-WebSocket-Key"
	// WSAcceptHeader carries the server's proof it understood the handshake.
	WSAcceptHeader string = "Sec-WebSocket-Accept"
	// WSVersionHeader carries the client's version, and the server's
	// counter-offer when it refuses one.
	WSVersionHeader string = "Sec-WebSocket-Version"
	// WSProtocolHeader carries the subprotocol list, then the single choice.
	WSProtocolHeader string = "Sec-WebSocket-Protocol"
	// WSExtensionsHeader carries the extension offer. This domain negotiates
	// none, so it is never echoed — see the package CLAUDE.md.
	WSExtensionsHeader string = "Sec-WebSocket-Extensions"
	// WSUpgradeToken is the token both Upgrade headers must carry.
	WSUpgradeToken string = "websocket"
)

// WSMaxControlPayload is the ceiling RFC 6455 §5.5 puts on a control frame's
// payload. It exists so an endpoint can answer a Ping without buffering: a
// control frame is answered inline, between the fragments of a message, so it
// must fit in a fixed buffer that is always available.
const WSMaxControlPayload int = 125
