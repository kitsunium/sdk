package entitlement_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	entitlement "github.com/kitsunium/sdk/third-party/entitlement"
)

// TestParseBundleRejectsMismatchedHalves pins the failure the bundle format
// exists to make impossible.
//
// Published as two objects, the roster and its signature were cached
// independently by raw.githubusercontent.com with max-age=300 (measured on
// the live endpoints, not assumed). For up to five minutes after every
// re-signature a client could therefore receive a NEW signature over an OLD
// roster. That mismatch is cryptographically indistinguishable from forgery,
// so a correct client refused a correct roster with the scheme's most
// alarming error — and with hourly signing, that is roughly 8% of all
// wall-clock time.
//
// One document cannot desynchronise with itself. This test pins that the
// verification still catches a genuinely mismatched pair, since the format
// change removes the transport that produced it but not the need to detect
// it: a hostile endpoint can still assemble one by hand.
func TestParseBundleRejectsMismatchedHalves(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		reason  string
		wantErr error
	}{
		{
			name:    "a signature over different bytes is refused",
			reason:  "this is what a stale-cache pair looked like, and what a hand-assembled forgery looks like",
			wantErr: entitlement.ErrRosterUnsigned,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, err := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("generating vendor key: %v", err)
			}

			now := time.Now()
			//: Two rosters the vendor genuinely signed, one after the other —
			//: exactly what two cache generations held.
			older := mustMarshal(t, entitlement.RosterValue{IssuedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)})
			newer := mustMarshal(t, entitlement.RosterValue{IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour)})

			//: The old payload paired with the new signature: both halves are
			//: authentic, the combination is not.
			bundle := mustMarshal(t, entitlement.BundleValue{
				Payload:   base64.StdEncoding.EncodeToString(older),
				Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(vendorPriv, newer)),
			})

			_, parseErr := entitlement.ParseBundle(bundle, vendorPub, now)
			if !errors.Is(parseErr, tt.wantErr) {
				t.Errorf("ParseBundle() error = %v, want %v (%s)", parseErr, tt.wantErr, tt.reason)
			}
		})
	}
}

// TestParseBundleRejectsMalformedInput pins that a broken publisher reports
// as unreachable rather than as a forgery.
//
// The distinction is not cosmetic: "roster is unreachable" sends an operator
// to look at the network, while ErrRosterUnsigned claims someone is
// impersonating the vendor. A captive portal answering 200 with HTML must
// not raise a security alarm.
func TestParseBundleRejectsMalformedInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "not json at all", input: "<html>captive portal</html>"},
		{name: "json but not a bundle", input: `{"unrelated":true}`},
		{name: "payload is not base64", input: `{"payload":"!!!","sig":"AAAA"}`},
		{name: "signature is not base64", input: `{"payload":"e30=","sig":"!!!"}`},
		{name: "empty document", input: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, _, err := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("generating vendor key: %v", err)
			}

			_, parseErr := entitlement.ParseBundle([]byte(tt.input), vendorPub, time.Now())
			if !errors.Is(parseErr, entitlement.ErrRosterUnreachable) {
				t.Errorf("ParseBundle(%q) error = %v, want ErrRosterUnreachable", tt.input, parseErr)
			}
		})
	}
}

// TestParseBundle pins the happy path end to end:
// the roster inside a correctly signed bundle comes back intact.
func TestParseBundle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		floor string
	}{
		{name: "a bundle with no floor", floor: ""},
		{name: "a bundle carrying a mandatory-update floor", floor: "v1.5.14"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, err := ed25519.GenerateKey(nil)
			//: A failure here is an environment problem, not a test outcome.
			if err != nil {
				t.Fatalf("generating vendor key: %v", err)
			}

			now := time.Now()
			raw := mustMarshal(t, entitlement.RosterValue{
				IssuedAt:        now.Add(-time.Hour),
				ExpiresAt:       now.Add(time.Hour),
				Subjects:        map[string]entitlement.SubjectValue{sampleUUID: {Fingerprint: "SHA256:x"}},
				RequiredVersion: tt.floor,
			})
			bundle := mustMarshal(t, entitlement.BundleValue{
				Payload:   base64.StdEncoding.EncodeToString(raw),
				Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(vendorPriv, raw)),
			})

			roster, parseErr := entitlement.ParseBundle(bundle, vendorPub, now)
			if parseErr != nil {
				t.Fatalf("ParseBundle() error = %v, want nil", parseErr)
			}
			if roster.Subjects[sampleUUID].Fingerprint != "SHA256:x" {
				t.Errorf("ParseBundle() subjects = %v, want the published entry", roster.Subjects)
			}
			//: The floor must survive the round trip, or mandatory updates
			//: would silently stop being mandatory.
			if roster.RequiredVersion != tt.floor {
				t.Errorf("ParseBundle() floor = %q, want %q", roster.RequiredVersion, tt.floor)
			}
		})
	}
}

// mustMarshal marshals a value or fails the test.
func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()

	raw, err := json.Marshal(value)
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("marshalling %T: %v", value, err)
	}
	//: Return the encoded bytes.
	return raw
}
