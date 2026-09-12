package entitlement

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"testing"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// TestRosterLifetime pins the single dial of the whole scheme. It caps two
// things at once: how long a revoked subject keeps working offline, and how
// long a hostile endpoint can replay a genuine roster. Widening it weakens
// both without failing any functional test.
func TestRosterLifetime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		bound  time.Duration
		atMost bool
		reason string
	}{
		{
			name:   "long enough to survive a working day offline",
			bound:  8 * time.Hour,
			atMost: false,
			reason: "a daemon started in the morning must outlast a flight or an offline CI",
		},
		{
			name:   "short enough to bound revocation and replay",
			bound:  7 * 24 * time.Hour,
			atMost: true,
			reason: "a revoked subject must not keep working for a week",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			//: One table drives both a floor and a ceiling assertion.
			if tt.atMost && coreent.RosterLifetime > tt.bound {
				t.Errorf("coreent.RosterLifetime = %v, want at most %v (%s)", coreent.RosterLifetime, tt.bound, tt.reason)
			}
			if !tt.atMost && coreent.RosterLifetime < tt.bound {
				t.Errorf("coreent.RosterLifetime = %v, want at least %v (%s)", coreent.RosterLifetime, tt.bound, tt.reason)
			}
		})
	}
}

// Test_authenticateRoster pins that dropping the freshness window keeps every
// other check exactly where it was.
//
// It is split out of ParseRoster for the clock ratchet, which must read the
// signing instant of a document that has usually expired. What must NOT come
// with that is any weakening: the vendor key is still length-checked, the
// signature is still verified before a single field is decoded, and a payload
// that is not a roster is still refused.
func Test_authenticateRoster(t *testing.T) {
	t.Parallel()

	issued := time.Now().Add(-72 * time.Hour).Truncate(time.Second)

	tests := []struct {
		name string
		// shortKey hands in a vendor key of the wrong length.
		shortKey bool
		// impostor signs with a key the verifier never saw.
		impostor bool
		// notARoster replaces the signed payload with something else.
		notARoster bool
		wantErr    error
		reason     string
	}{
		{name: "a long-expired genuine roster authenticates", reason: "the window is the caller's business, not this function's"},
		{name: "a vendor key of the wrong length is refused", shortKey: true, wantErr: coreent.ErrRosterUnsigned, reason: "ed25519.Verify panics on a short slice; a broken build must not take the process down"},
		{name: "a payload signed by anyone else is refused", impostor: true, wantErr: coreent.ErrRosterUnsigned, reason: "the signature runs before the JSON parser, which is the whole ordering"},
		{name: "an authenticated payload that is not a roster is refused", notARoster: true, reason: "a broken publisher, surfaced rather than authorised on zero values"},
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

			raw, marshalErr := json.Marshal(coreent.RosterValue{IssuedAt: issued, ExpiresAt: issued.Add(time.Hour)})
			//: A failure here is an environment problem, not a test outcome.
			if marshalErr != nil {
				t.Fatalf("marshalling roster: %v", marshalErr)
			}
			//: A correctly SIGNED payload that is simply not JSON: the decode
			//: failure has to be reachable after authentication succeeds.
			if tt.notARoster {
				raw = []byte("not json at all")
			}

			key := vendorPub
			//: A key of the wrong length is a broken build, not a forgery.
			if tt.shortKey {
				key = vendorPub[:8]
			}

			roster, err := authenticateRoster(raw, ed25519.Sign(signWith, raw), key)
			//: The not-a-roster row carries no sentinel: a decode failure is
			//: a broken publisher and reports itself.
			if tt.wantErr == nil && tt.notARoster {
				if err == nil {
					t.Errorf("authenticateRoster() error = nil, want a decode failure (%s)", tt.reason)
				}
				return
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("authenticateRoster() error = %v, want %v (%s)", err, tt.wantErr, tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("authenticateRoster() error = %v, want nil (%s)", err, tt.reason)
			}
			//: The whole point: three days past its window, it still says when.
			if !roster.IssuedAt.Equal(issued) {
				t.Errorf("authenticateRoster() IssuedAt = %s, want %s (%s)", roster.IssuedAt, issued, tt.reason)
			}
		})
	}
}
