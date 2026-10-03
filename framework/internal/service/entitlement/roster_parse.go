// Package entitlement — parsing and authenticating a published roster.
//
// The signature check lives HERE rather than with the value it produces: core
// declares what a roster IS, and verifying one is a mechanism with a
// cryptographic dependency and a freshness policy.
package entitlement

import (
	"crypto/ed25519"
	"encoding/json"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/pkg/v1/sign"

	coreent "github.com/kitsunium/sdk/framework/internal/core/entitlement"
)

// verifiedBy reports whether signature is key's ed25519 signature over message.
//
// It asks the crypto domain's signing facade (ADR 0158 §2) rather than calling
// crypto/ed25519 itself: one verifier for the vendor's roster, the self-update
// manifest and a Roughtime answer, and the one the rest of the SDK uses. The
// facade answers a key or a signature of the wrong length with false rather
// than a panic, and an error only for an algorithm it does not register —
// which Ed25519 always is, since importing the facade registers it. An error
// therefore reads as "not verified": nothing that failed to verify is believed.
func verifiedBy(key, message, signature []byte) bool {
	verified, verifyErr := sign.Verify(sign.Ed25519, key, message, signature)
	//: Verified, and by the scheme the facade was asked for.
	return verifyErr == nil && verified
}

// ParseRoster decodes and authenticates a roster. Signature verification runs
// before any field is trusted, and `now` is passed in rather than read from
// the clock so callers can test the boundary.
func ParseRoster(raw, sig []byte, vendor ed25519.PublicKey, now time.Time) (roster *coreent.RosterValue, err error) {
	authenticated, authErr := authenticateRoster(raw, sig, vendor)
	//: Unsigned, forged, or not a roster at all.
	if authErr != nil {
		//: Propagate the refusal.
		return nil, authErr
	}
	decoded := *authenticated

	//: A window wider than the agreed lifetime defeats the entire scheme: a
	//: roster signed once with a distant expiry would keep authorising
	//: revoked subjects for as long as it says. The signature proves the
	//: vendor issued it, not that the vendor issued it *correctly*, so the
	//: client enforces the bound itself.
	if decoded.ExpiresAt.Sub(decoded.IssuedAt) > coreent.RosterLifetime {
		//: Refuse an over-wide window whoever signed it.
		return nil, refuse(coreent.ErrRosterStale,
			errs.String("condition", "window wider than the agreed lifetime"),
			errs.String("lifetime", coreent.RosterLifetime.String()),
			errs.String("window", decoded.ExpiresAt.Sub(decoded.IssuedAt).String()))
	}
	//: A roster signed for the future is as suspect as an expired one: it
	//: would extend the replay window past what the vendor intended.
	if now.Before(decoded.IssuedAt) {
		//: Refuse a window that has not opened yet.
		return nil, refuse(coreent.ErrRosterStale,
			errs.String("condition", "issued in the future"),
			errs.String("issued_at", decoded.IssuedAt.UTC().Format(time.RFC3339)))
	}
	//: Past the window the signature no longer authorizes anything.
	if now.After(decoded.ExpiresAt) {
		//: Refuse a window that has closed.
		return nil, coreent.ErrRosterStale
	}

	//: Return the authenticated roster to the caller.
	return &decoded, nil
}

// authenticateRoster verifies the vendor signature and decodes the payload,
// WITHOUT applying the freshness window.
//
// Split out of ParseRoster for the clock ratchet, which must read the signing
// instant of a document that has usually expired — an expired roster is still
// the vendor's statement about when it was signed, and that statement is the
// whole point of the ratchet.
//
// Nothing here is a weaker check: the signature is verified before a single
// field is decoded, exactly as before. What is missing is only the judgement
// about NOW, which is what the caller with a clock to doubt cannot use.
func authenticateRoster(raw, sig []byte, vendor ed25519.PublicKey) (roster *coreent.RosterValue, err error) {
	//: A malformed vendor key can only come from a broken build; refuse it
	//: by name, with its length, rather than report every roster a forgery.
	if len(vendor) != ed25519.PublicKeySize {
		//: Refuse, naming the key's length.
		return nil, refuse(coreent.ErrRosterUnsigned,
			errs.String("condition", "the linked vendor key is not an ed25519 public key"),
			errs.Int("key_bytes", len(vendor)))
	}
	//: Authenticate the bytes before decoding them: a forged roster must
	//: never reach the JSON parser, let alone the authorization decision.
	if !verifiedBy(vendor, raw, sig) {
		//: Unsigned or forged — the spoofing case.
		return nil, coreent.ErrRosterUnsigned
	}

	//: After the signature and before the decode, so the order "authenticate
	//: before you read" is preserved word for word. The signature proves the
	//: vendor issued these bytes; it does not make two readers agree on what
	//: they SAY. A payload naming "subjects" twice is signed exactly once and
	//: read two ways — the second block silently replacing the first, which at
	//: a subject uuid is the difference between authorised and revoked.
	if member, duplicate := checkNoDuplicateNames(raw); duplicate {
		//: A publication fault, reported as such: only the vendor can produce
		//: this, so it is never a forgery claim.
		return nil, refuse(coreent.ErrRosterUnreachable,
			errs.String("stage", "decode_roster_payload"),
			errs.String("condition", "one member name declared twice, which two readers can read differently"),
			errs.String("member", member))
	}

	var decoded coreent.RosterValue
	//: Malformed JSON from an authenticated payload means a broken publisher.
	if unmarshalErr := json.Unmarshal(raw, &decoded); unmarshalErr != nil {
		//: Report it exactly as decodeBundle reports the same class one level
		//: up: the endpoint served something that is not a roster, which is a
		//: publication fault and not a forgery claim. It carried NO sentinel
		//: before, so a caller mapping refusals to exit codes and advice fell
		//: through to its default on the one input a vendor can produce by
		//: mistake.
		return nil, classify(coreent.ErrRosterUnreachable, unmarshalErr,
			errs.String("stage", "decode_roster_payload"))
	}
	//: Authentic, decoded, and entirely unjudged as to when.
	return &decoded, nil
}
