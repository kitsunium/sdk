// Package entitlement — parsing and authenticating a published roster.
//
// The signature check lives HERE rather than with the value it produces: core
// declares what a roster IS, and verifying one is a mechanism with a
// cryptographic dependency and a freshness policy.
package entitlement

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

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
		return nil, fmt.Errorf("%w: window exceeds %s", coreent.ErrRosterStale, coreent.RosterLifetime)
	}
	//: A roster signed for the future is as suspect as an expired one: it
	//: would extend the replay window past what the vendor intended.
	if now.Before(decoded.IssuedAt) {
		//: Refuse a window that has not opened yet.
		return nil, fmt.Errorf("%w: issued in the future", coreent.ErrRosterStale)
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
	//: A malformed vendor key can only come from a broken build; refuse
	//: rather than let ed25519.Verify panic on a short slice.
	if len(vendor) != ed25519.PublicKeySize {
		//: Refuse rather than risk a panic inside ed25519.Verify.
		return nil, fmt.Errorf("%w: malformed vendor key", coreent.ErrRosterUnsigned)
	}
	//: Authenticate the bytes before decoding them: a forged roster must
	//: never reach the JSON parser, let alone the authorization decision.
	if !ed25519.Verify(vendor, raw, sig) {
		//: Unsigned or forged — the spoofing case.
		return nil, coreent.ErrRosterUnsigned
	}

	var decoded coreent.RosterValue
	//: Malformed JSON from an authenticated payload means a broken publisher.
	if unmarshalErr := json.Unmarshal(raw, &decoded); unmarshalErr != nil {
		//: Surface the decode failure rather than authorize on zero values.
		return nil, fmt.Errorf("decoding roster: %w", unmarshalErr)
	}
	//: Authentic, decoded, and entirely unjudged as to when.
	return &decoded, nil
}
