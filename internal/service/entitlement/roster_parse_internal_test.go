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
		{name: "an authenticated payload that is not a roster is refused", notARoster: true, wantErr: coreent.ErrRosterUnreachable, reason: "a broken publisher is the same class decodeBundle already reports: the endpoint served something that is not a roster, which is not a forgery claim and must still be classifiable"},
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
			//: Every refusal row names the sentinel it must carry, the
			//: not-a-roster one included: an error a caller cannot classify
			//: is one it cannot act on.
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

// Test_authenticateRoster_refusesADuplicateSubjectsBlock is the one of the four
// duplicate-name tests that has to carry a REAL signature.
//
// The payload is signed with the vendor key, so it passes ed25519.Verify: the
// refusal can only come from the name scan. A test on an unsigned payload would
// pass with or without the fix, for the wrong reason.
//
// The two blocks are the point. json.Unmarshal keeps the LAST, so the vendor
// signed one document and every reader that prefers the first sees the subject
// authorised while this one sees it gone. Both can prove the vendor signed what
// they hold, and they hold different rosters.
func Test_authenticateRoster_refusesADuplicateSubjectsBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		wantErr bool
		reason  string
	}{
		{
			name:    "one subjects block",
			payload: `{"iat":"2026-09-15T08:00:00Z","exp":"2026-09-16T08:00:00Z","subjects":{"u1":{"fp":"SHA256:x"}}}`,
			wantErr: false,
			reason:  "the ordinary shape must still authenticate",
		},
		{
			name:    "two subjects blocks, the second withdrawing the subject",
			payload: `{"iat":"2026-09-15T08:00:00Z","exp":"2026-09-16T08:00:00Z","subjects":{"u1":{"fp":"SHA256:x"}},"subjects":{}}`,
			wantErr: true,
			reason:  "authorised or revoked, decided by which member a parser keeps",
		},
		{
			name:    "a duplicated subject uuid",
			payload: `{"iat":"2026-09-15T08:00:00Z","exp":"2026-09-16T08:00:00Z","subjects":{"u1":{"fp":"SHA256:x"},"u1":{"fp":"SHA256:y"}}}`,
			wantErr: true,
			reason:  "two levels down, where a top-level scan would see nothing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vendorPub, vendorPriv, keyErr := ed25519.GenerateKey(nil)
			//: Without a key there is no signature to get past.
			if keyErr != nil {
				t.Fatalf("GenerateKey() error = %v", keyErr)
			}
			raw := []byte(tt.payload)
			//: Signed by the vendor, so ed25519.Verify passes and the refusal
			//: below can only be the name scan.
			roster, err := authenticateRoster(raw, ed25519.Sign(vendorPriv, raw), vendorPub)

			if tt.wantErr {
				//: Refused, and not by the signature.
				if err == nil {
					t.Fatalf("authenticateRoster() error = nil, want a refusal (%s)", tt.reason)
				}
				//: A refusal must not also hand back a roster to act on.
				if roster != nil {
					t.Errorf("authenticateRoster() returned a roster alongside its refusal (%s)", tt.reason)
				}
				return
			}
			//: The control row: a clean payload still authenticates, or this
			//: test would pass by refusing everything.
			if err != nil {
				t.Fatalf("authenticateRoster() error = %v, want nil (%s)", err, tt.reason)
			}
		})
	}
}
