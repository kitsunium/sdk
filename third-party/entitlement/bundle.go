// Package entitlement - the published bundle: roster and signature in ONE
// document, because two documents cannot be fetched atomically.
package entitlement

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// BundleValue is what a publication point actually serves: the roster's exact
// bytes and the vendor's signature over them, in a single object.
//
// It exists because the roster and its detached signature used to be two
// files, and two files cannot be fetched atomically. raw.githubusercontent.com
// caches each object independently with max-age=300, so for up to five minutes
// after every re-signature a client could receive a NEW signature over an OLD
// roster — measured on the live endpoints, not hypothesised. That mismatch is
// indistinguishable from forgery, so the client refused with the most alarming
// error the scheme has (ErrRosterUnsigned, the spoofing case) while nothing
// was wrong. With hourly signing that is roughly 8% of all wall-clock time.
//
// One object cannot desynchronise with itself. It also halves the cold-start
// cost, since there is now one round trip instead of two.
type BundleValue struct {
	// Payload is the roster document, base64. Carrying the bytes rather than
	// a nested object is what keeps the signature verifiable: it covers this
	// exact byte sequence, and re-serialising a decoded struct would not
	// reproduce it.
	Payload string `json:"payload"`
	// Signature is the vendor's detached ed25519 signature over the decoded
	// payload, base64.
	Signature string `json:"sig"`
}

// ParseBundle decodes a published bundle and authenticates the roster inside
// it. It is the only entry point production uses; ParseRoster remains for
// callers that already hold the two byte slices.
func ParseBundle(raw []byte, vendor ed25519.PublicKey, now time.Time) (roster *RosterValue, err error) {
	payload, signature, decodeErr := decodeBundle(raw)
	//: A document we cannot take apart carries nothing to authenticate.
	if decodeErr != nil {
		//: Propagate the publication or transport problem.
		return nil, decodeErr
	}
	//: Authentication and freshness are checked before any field is trusted.
	return ParseRoster(payload, signature, vendor, now)
}

// authenticateBundle decodes and AUTHENTICATES a bundle without applying the
// freshness window.
//
// It exists for one caller: the clock ratchet, which has to read the signing
// instant of a bundle that may well have expired — that is the ordinary state
// of a cached one — in order to compare it against the local clock. Every other
// path goes through ParseBundle and gets the window with it.
//
// The SIGNATURE is still required. A ratchet that advanced on unauthenticated
// bytes would let anyone who can write the cache file pin this machine's clock
// wherever they liked, which is a denial of service handed over for free.
func authenticateBundle(raw []byte, vendor ed25519.PublicKey) (roster *RosterValue, err error) {
	payload, signature, decodeErr := decodeBundle(raw)
	//: A document we cannot take apart carries nothing to authenticate.
	if decodeErr != nil {
		//: Propagate the publication or transport problem.
		return nil, decodeErr
	}
	//: Signature only; the window is the caller's business here.
	return authenticateRoster(payload, signature, vendor)
}

// decodeBundle splits a published bundle into the signed bytes and the
// signature over them, without judging either.
func decodeBundle(raw []byte) (payload, signature []byte, err error) {
	var bundle BundleValue
	//: A bundle we cannot decode tells us nothing; refuse rather than guess.
	if unmarshalErr := json.Unmarshal(raw, &bundle); unmarshalErr != nil {
		//: Report it as unreachable-shaped: the endpoint answered with
		//: something that is not a bundle at all, which is a publication or
		//: transport problem, not a forgery claim.
		return nil, nil, fmt.Errorf("%w: malformed bundle: %w", ErrRosterUnreachable, unmarshalErr)
	}

	//: A document that decoded as JSON but carries neither half is not a
	//: bundle at all — an unrelated file, a captive portal's JSON error page.
	//: Empty base64 decodes to empty bytes WITHOUT error, so without this
	//: check it would reach the signature verification and be reported as
	//: forged. "Someone is impersonating the vendor" is a very different
	//: thing to tell an operator than "this endpoint served junk".
	if bundle.Payload == "" || bundle.Signature == "" {
		//: Report a publication or transport problem, not an attack.
		return nil, nil, fmt.Errorf("%w: response is not a bundle", ErrRosterUnreachable)
	}

	decodedPayload, payloadErr := base64.StdEncoding.DecodeString(bundle.Payload)
	//: A payload that is not base64 cannot be the signed bytes.
	if payloadErr != nil {
		//: Same reasoning as above: this is a broken publisher, not a forger.
		return nil, nil, fmt.Errorf("%w: undecodable payload: %w", ErrRosterUnreachable, payloadErr)
	}
	decodedSignature, signatureErr := base64.StdEncoding.DecodeString(bundle.Signature)
	//: A signature that is not base64 cannot verify anything.
	if signatureErr != nil {
		//: Same reasoning as above.
		return nil, nil, fmt.Errorf("%w: undecodable signature: %w", ErrRosterUnreachable, signatureErr)
	}

	//: Both halves, still entirely untrusted.
	return decodedPayload, decodedSignature, nil
}
