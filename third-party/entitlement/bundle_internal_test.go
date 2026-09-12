package entitlement

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// TestBundleValueRoundTrip pins that the payload survives base64 unchanged.
//
// The signature covers the roster's EXACT bytes, so the bundle must carry
// those bytes rather than a nested object: re-serialising a decoded struct
// would reorder keys or alter spacing and invalidate a perfectly good
// signature. This test is what stops the payload ever becoming a nested
// object for readability.
func TestBundleValueRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		reason  string
	}{
		{name: "compact json survives", payload: `{"a":1,"b":2}`, reason: "the signed serialisation is compact"},
		{name: "spacing is preserved", payload: `{ "a" : 1 }`, reason: "a signature over spaced json must still verify"},
		{name: "key order is preserved", payload: `{"b":2,"a":1}`, reason: "re-serialising would sort these and break the signature"},
		{name: "an empty object survives", payload: `{}`, reason: "a roster with no subjects is legitimate"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			encoded := base64.StdEncoding.EncodeToString([]byte(tt.payload))
			raw, err := json.Marshal(BundleValue{Payload: encoded, Signature: "AAAA"})
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("marshalling bundle: %v", err)
			}

			var decoded BundleValue
			//: A failure here is an environment problem, not a test outcome.
			if unmarshalErr := json.Unmarshal(raw, &decoded); unmarshalErr != nil {
				t.Fatalf("unmarshalling bundle: %v", unmarshalErr)
			}
			payload, decodeErr := base64.StdEncoding.DecodeString(decoded.Payload)
			//: A failure here is an environment problem, not a test outcome.
			if decodeErr != nil {
				t.Fatalf("decoding payload: %v", decodeErr)
			}

			if string(payload) != tt.payload {
				t.Errorf("payload round-tripped to %q, want %q (%s)", payload, tt.payload, tt.reason)
			}
		})
	}
}

// TestParseBundleVerifiesBeforeDecoding pins the order of operations: the
// signature is checked against the raw payload BEFORE that payload reaches
// the JSON parser.
//
// A forged bundle must never get a decoder run on its contents — that is the
// property ParseRoster already documents, and routing through the bundle
// wrapper must not quietly lose it.
func TestParseBundleVerifiesBeforeDecoding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		reason  string
	}{
		{name: "unparseable json signed by nobody is refused as unsigned", payload: "not json at all", reason: "verification runs first, so the parser is never reached"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, _, err := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("generating vendor key: %v", err)
			}
			_, attackerPriv, err := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("generating attacker key: %v", err)
			}

			payload := []byte(tt.payload)
			raw, err := json.Marshal(BundleValue{
				Payload:   base64.StdEncoding.EncodeToString(payload),
				Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(attackerPriv, payload)),
			})
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("marshalling bundle: %v", err)
			}

			_, parseErr := ParseBundle(raw, vendorPub, time.Now())
			//: Unsigned, not "malformed json": the signature check refused it
			//: before the payload was ever parsed.
			if parseErr == nil {
				t.Fatalf("ParseBundle() = nil error, want a refusal (%s)", tt.reason)
			}
			if parseErr.Error() != ErrRosterUnsigned.Error() {
				t.Errorf("ParseBundle() error = %v, want %v (%s)", parseErr, ErrRosterUnsigned, tt.reason)
			}
		})
	}
}

// Test_decodeBundle pins that taking a document APART and judging it are two
// jobs, and that the first one never judges.
//
// Splitting them is what lets the clock ratchet read an EXPIRED cached bundle's
// signing instant — the ordinary state of a cached one — without the freshness
// check that every other caller still gets.
func Test_decodeBundle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// raw is the document offered.
		raw     string
		wantErr bool
		reason  string
	}{
		{name: "a well-formed bundle splits in two", raw: `{"payload":"aGk=","sig":"c2ln"}`, reason: "both halves come back untouched and unjudged"},
		{name: "junk is not a bundle", raw: `<html>captive portal</html>`, wantErr: true, reason: "a broken publisher, not a forger — the error says so"},
		{name: "a bundle missing a half is refused", raw: `{"payload":"aGk="}`, wantErr: true, reason: "empty base64 decodes to empty bytes WITHOUT error, so it would reach the signature check and be reported as forged"},
		{name: "an undecodable payload is refused", raw: `{"payload":"!!!","sig":"c2ln"}`, wantErr: true, reason: "not base64 means not the signed bytes"},
		{name: "an undecodable signature is refused", raw: `{"payload":"aGk=","sig":"!!!"}`, wantErr: true, reason: "not base64 means it verifies nothing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			payload, signature, err := decodeBundle([]byte(tt.raw))
			if tt.wantErr {
				//: Reported as a publication problem, never as an attack.
				if !errors.Is(err, ErrRosterUnreachable) {
					t.Errorf("decodeBundle() error = %v, want ErrRosterUnreachable (%s)", err, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeBundle() error = %v, want nil (%s)", err, tt.reason)
			}
			if len(payload) == 0 || len(signature) == 0 {
				t.Errorf("decodeBundle() = (%q, %q), want both halves (%s)", payload, signature, tt.reason)
			}
		})
	}
}

// Test_authenticateBundle pins that skipping the freshness window does NOT skip
// the signature.
//
// The ratchet is the only caller, and it exists to read a timestamp it cannot
// otherwise trust. A version of it that accepted unsigned bytes would let
// anybody who can write the cache file pin this machine's clock wherever they
// liked — a denial of service handed over through the very file the ratchet
// defends.
func Test_authenticateBundle(t *testing.T) {
	t.Parallel()

	issued := time.Now().Add(-48 * time.Hour).Truncate(time.Second)

	tests := []struct {
		name string
		// impostor signs with a key the verifier never saw.
		impostor bool
		wantErr  error
		reason   string
	}{
		{name: "a long-expired genuine bundle still yields its IssuedAt", reason: "an expired bundle is the ordinary state of a cached one, and it is still the vendor's statement about when it was signed"},
		{name: "a bundle signed by anyone else is refused", impostor: true, wantErr: ErrRosterUnsigned, reason: "the signature is the only thing that makes this instant unforgeable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, keyErr := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if keyErr != nil {
				t.Fatalf("generating vendor key: %v", keyErr)
			}
			signWith := vendorPriv
			//: The impostor row signs with a key the verifier never saw.
			if tt.impostor {
				_, other, otherErr := ed25519.GenerateKey(nil)
				//: A failure here is an environment problem, not a test outcome.
				if otherErr != nil {
					t.Fatalf("generating impostor key: %v", otherErr)
				}
				signWith = other
			}

			raw, marshalErr := json.Marshal(RosterValue{IssuedAt: issued, ExpiresAt: issued.Add(time.Hour)})
			//: A failure here is an environment problem, not a test outcome.
			if marshalErr != nil {
				t.Fatalf("marshalling roster: %v", marshalErr)
			}
			bundle, bundleErr := json.Marshal(BundleValue{
				Payload:   base64.StdEncoding.EncodeToString(raw),
				Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(signWith, raw)),
			})
			//: A failure here is an environment problem, not a test outcome.
			if bundleErr != nil {
				t.Fatalf("marshalling bundle: %v", bundleErr)
			}

			roster, err := authenticateBundle(bundle, vendorPub)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("authenticateBundle() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("authenticateBundle() error = %v, want nil (%s)", err, tt.reason)
			}
			//: The whole point: a window that closed two days ago is not a
			//: reason to forget when the vendor signed it.
			if !roster.IssuedAt.Equal(issued) {
				t.Errorf("authenticateBundle() IssuedAt = %s, want %s (%s)", roster.IssuedAt, issued, tt.reason)
			}
		})
	}
}
