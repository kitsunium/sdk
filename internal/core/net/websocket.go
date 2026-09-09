// Package net — the WebSocket opening handshake (RFC 6455 §4).
package net

import (
	"crypto/sha1" //nolint:gosec // RFC 6455 §1.3 names SHA-1 by value; the digest proves a handshake was understood, it protects nothing.
	"encoding/base64"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

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

// WSAcceptKey computes the Sec-WebSocket-Accept value for a client's
// Sec-WebSocket-Key (RFC 6455 §4.2.2 step 5).
//
// The digest is SHA-1 by the RFC's own instruction. It is not a security
// primitive here and is not used as one: its job is to prove the server parsed
// the handshake rather than replaying it, so that a cache holding a stale 101
// cannot be mistaken for a live upgrade.
func WSAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + WSGUID)) //nolint:gosec // see the doc comment: RFC-mandated, not a security claim.
	//: base64 of the raw digest, exactly as §4.2.2 specifies.
	return base64.StdEncoding.EncodeToString(sum[:])
}

// ValidateWSKey reports whether a Sec-WebSocket-Key is what RFC 6455 §4.1 says
// it is: base64 of exactly sixteen bytes.
//
// It is checked rather than merely echoed through the digest because a key of
// the wrong length is a request that was not produced by a WebSocket client at
// all — most often a scanner, sometimes a proxy replaying a fragment — and
// answering it with a 101 hands a socket to something that will never speak the
// protocol.
func ValidateWSKey(key string) error {
	//: an absent key is the commonest shape of "this is not a handshake".
	if key == "" {
		//: refuse.
		return errs.Wrap(WSHandshakeFailed, errs.WrapParams{},
			errs.String("header", WSKeyHeader),
			errs.String("why", "the header is missing"))
	}
	raw, derr := base64.StdEncoding.DecodeString(key)
	//: a key that is not base64 was not produced by §4.1's procedure.
	if derr != nil {
		//: refuse.
		return errs.Wrap(WSHandshakeFailed, errs.WrapParams{},
			errs.String("header", WSKeyHeader),
			errs.String("why", "the value is not base64"))
	}
	//: §4.1 fixes the nonce at sixteen bytes.
	if len(raw) != WSKeyLen {
		//: refuse.
		return errs.Wrap(WSHandshakeFailed, errs.WrapParams{},
			errs.String("header", WSKeyHeader),
			errs.Int("decoded_length", len(raw)),
			errs.String("why", "the key must decode to exactly sixteen bytes"))
	}
	//: a well-formed nonce.
	return nil
}
