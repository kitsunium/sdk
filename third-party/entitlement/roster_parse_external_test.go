package entitlement_test

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"testing"
	"time"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"

	svcent "github.com/kitsunium/sdk/internal/service/entitlement"
)

// vendorKey is a freshly generated signing pair standing in for the vendor's.
type vendorKey struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

// newVendorKey generates the pair a test signs its rosters with.
func newVendorKey(t *testing.T) vendorKey {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(nil)
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("generating vendor key: %v", err)
	}
	//: Return both halves so a test can sign and then verify.
	return vendorKey{pub: pub, priv: priv}
}

// signRoster marshals and signs a roster with the given key.
func signRoster(t *testing.T, k vendorKey, r coreent.RosterValue) (raw, sig []byte) {
	t.Helper()

	raw, err := json.Marshal(r)
	//: A failure here is an environment problem, not a test outcome.
	if err != nil {
		t.Fatalf("marshalling roster: %v", err)
	}
	//: Return the exact bytes that were signed, since verification is
	//: byte-exact.
	return raw, ed25519.Sign(k.priv, raw)
}

// TestParseRoster covers the admission policy: only a roster that is both
// authentic and inside its window may authorize anything.
func TestParseRoster(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		roster  coreent.RosterValue
		at      time.Time
		corrupt bool
		impost  bool
		badKey  bool
		wantErr error
	}{
		{
			name:   "authentic roster inside its window is accepted",
			roster: coreent.RosterValue{IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(coreent.RosterLifetime - time.Hour), Subjects: map[string]coreent.SubjectValue{"abc": {Fingerprint: "SHA256:x"}}},
			at:     now,
		},
		{
			name:    "a roster signed by anyone else is refused",
			roster:  coreent.RosterValue{IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(coreent.RosterLifetime - time.Hour)},
			at:      now,
			impost:  true,
			wantErr: coreent.ErrRosterUnsigned,
		},
		{
			name:    "tampering with the payload invalidates the signature",
			roster:  coreent.RosterValue{IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(coreent.RosterLifetime - time.Hour)},
			at:      now,
			corrupt: true,
			wantErr: coreent.ErrRosterUnsigned,
		},
		{
			name:    "a malformed vendor key cannot panic the verifier",
			roster:  coreent.RosterValue{IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(coreent.RosterLifetime - time.Hour)},
			at:      now,
			badKey:  true,
			wantErr: coreent.ErrRosterUnsigned,
		},
		{
			name:    "an expired roster is refused despite a good signature",
			roster:  coreent.RosterValue{IssuedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)},
			at:      now,
			wantErr: coreent.ErrRosterStale,
		},
		{
			//: The signature proves the vendor issued it, not that the
			//: vendor issued it correctly. A roster signed once with a
			//: distant expiry would authorise revoked subjects for as long
			//: as it says, so the client enforces the bound itself.
			name:    "a window wider than the agreed lifetime is refused",
			roster:  coreent.RosterValue{IssuedAt: now.Add(-time.Hour), ExpiresAt: now.Add(30 * 24 * time.Hour)},
			at:      now,
			wantErr: coreent.ErrRosterStale,
		},
		{
			name:   "a window exactly at the limit is accepted",
			roster: coreent.RosterValue{IssuedAt: now, ExpiresAt: now.Add(coreent.RosterLifetime)},
			at:     now,
		},
		{
			name:    "the instant past expiry is refused",
			roster:  coreent.RosterValue{IssuedAt: now.Add(-coreent.RosterLifetime), ExpiresAt: now.Add(-time.Nanosecond)},
			at:      now,
			wantErr: coreent.ErrRosterStale,
		},
		{
			name:    "a roster issued in the future is refused",
			roster:  coreent.RosterValue{IssuedAt: now.Add(time.Hour), ExpiresAt: now.Add(2 * coreent.RosterLifetime)},
			at:      now,
			wantErr: coreent.ErrRosterStale,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			key := newVendorKey(t)
			raw, sig := signRoster(t, key, tt.roster)
			verifier := key.pub
			//: A substituted endpoint serves bytes signed by someone else.
			if tt.impost {
				verifier = newVendorKey(t).pub
			}
			//: A truncated key must be refused, never dereferenced.
			if tt.badKey {
				verifier = ed25519.PublicKey{0x01}
			}
			//: Flipping a payload byte must break the signature.
			if tt.corrupt {
				raw[len(raw)-2] ^= 0xFF
			}

			got, err := svcent.ParseRoster(raw, sig, verifier, tt.at)
			//: Error cases assert the sentinel, not the wording.
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("ParseRoster() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRoster() error = %v, want nil", err)
			}
			if got == nil {
				t.Fatal("ParseRoster() returned no roster on the success path")
			}
		})
	}
}

// TestRosterValue_SubjectFor pins that absence means revocation rather than a
// lookup failure to be tolerated.
func TestRosterValue_SubjectFor(t *testing.T) {
	t.Parallel()

	expiry := time.Date(2027, 8, 23, 0, 0, 0, 0, time.UTC)
	roster := &coreent.RosterValue{Subjects: map[string]coreent.SubjectValue{
		"known":  {Fingerprint: "SHA256:aaa"},
		"termed": {Fingerprint: "SHA256:bbb", ExpiresAt: expiry},
		"blank":  {},
	}}

	tests := []struct {
		name    string
		uuid    string
		want    coreent.SubjectValue
		wantErr error
	}{
		{name: "known subject returns its fingerprint", uuid: "known", want: coreent.SubjectValue{Fingerprint: "SHA256:aaa"}},
		{name: "a subject with a term returns it too", uuid: "termed", want: coreent.SubjectValue{Fingerprint: "SHA256:bbb", ExpiresAt: expiry}},
		{name: "absent subject is revoked", uuid: "gone", wantErr: coreent.ErrRevoked},
		{name: "empty fingerprint is revoked, not accepted", uuid: "blank", wantErr: coreent.ErrRevoked},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := roster.SubjectFor(tt.uuid)
			//: Error cases assert the sentinel, not the wording.
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("SubjectFor(%q) error = %v, want %v", tt.uuid, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SubjectFor(%q) error = %v, want nil", tt.uuid, err)
			}
			if got != tt.want {
				t.Errorf("SubjectFor(%q) = %+v, want %+v", tt.uuid, got, tt.want)
			}
		})
	}
}
