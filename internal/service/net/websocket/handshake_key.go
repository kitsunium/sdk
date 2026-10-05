package websocket

import (
	"crypto/sha1" //nolint:gosec // RFC 6455 §1.3 names SHA-1 by value; the digest proves a handshake was understood, it protects nothing.
	"encoding/base64"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// AcceptKey computes the Sec-WebSocket-Accept value for a client's
// Sec-WebSocket-Key (RFC 6455 §4.2.2 step 5).
//
// The digest is SHA-1 by the RFC's own instruction. It is not a security
// primitive here and is not used as one: its job is to prove the server parsed
// the handshake rather than replaying it, so that a cache holding a stale 101
// cannot be mistaken for a live upgrade.
func AcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + corenet.WSGUID)) //nolint:gosec // see the doc comment: RFC-mandated, not a security claim.
	//: base64 of the raw digest, exactly as §4.2.2 specifies.
	return base64.StdEncoding.EncodeToString(sum[:])
}

// ValidateKey reports whether a Sec-WebSocket-Key is what RFC 6455 §4.1 says
// it is: base64 of exactly sixteen bytes.
//
// It is checked rather than merely echoed through the digest because a key of
// the wrong length is a request that was not produced by a WebSocket client at
// all — most often a scanner, sometimes a proxy replaying a fragment — and
// answering it with a 101 hands a socket to something that will never speak the
// protocol.
func ValidateKey(key string) error {
	//: an absent key is the commonest shape of "this is not a handshake".
	if key == "" {
		//: refuse.
		return errs.Wrap(corenet.WSHandshakeFailed, errs.WrapParams{},
			errs.String("header", corenet.WSKeyHeader),
			errs.String("why", "the header is missing"))
	}
	raw, derr := base64.StdEncoding.DecodeString(key)
	//: a key that is not base64 was not produced by §4.1's procedure.
	if derr != nil {
		//: refuse.
		return errs.Wrap(corenet.WSHandshakeFailed, errs.WrapParams{},
			errs.String("header", corenet.WSKeyHeader),
			errs.String("why", "the value is not base64"))
	}
	//: §4.1 fixes the nonce at sixteen bytes.
	if len(raw) != corenet.WSKeyLen {
		//: refuse.
		return errs.Wrap(corenet.WSHandshakeFailed, errs.WrapParams{},
			errs.String("header", corenet.WSKeyHeader),
			errs.Int("decoded_length", len(raw)),
			errs.String("why", "the key must decode to exactly sixteen bytes"))
	}
	//: a well-formed nonce.
	return nil
}
