//go:build !windows

package entitlement_test

import (
	"errors"
	"os"
	"testing"
	"time"

	entitlement "github.com/kitsunium/sdk/third-party/entitlement"
)

// TestService_VerifyRefusesALooseKey pins that the permission guard is reached
// and enforced through the full Verify path, not merely unit-tested on
// SignerFromFile in isolation.
//
// POSIX-only, in its own build-tagged file: os.Chmod cannot widen an ACL on
// Windows and os.Stat reports a synthesised mode there, so the fixture could
// not even set up the state this asserts. A shared case would have to be
// skipped on Windows, and this project forbids t.Skip — a test that does not
// run is not a test.
func TestService_VerifyRefusesALooseKey(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		mode os.FileMode
	}{
		{name: "world-readable", mode: 0o644},
		{name: "group-readable", mode: 0o640},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			fingerprint := enrol(t, dir, sampleUUID)
			//: A secret the whole machine can read is not a secret.
			if err := os.Chmod(entitlement.PrivateKeyPath(dir, sampleUUID), tt.mode); err != nil {
				t.Fatalf("loosening key mode: %v", err)
			}

			getter, vendor := publishRoster(t,
				map[string]entitlement.SubjectValue{sampleUUID: {Fingerprint: fingerprint}},
				now.Add(-time.Hour), now.Add(entitlement.RosterLifetime-time.Hour))

			_, err := entitlement.NewServiceWithGetter(getter, dir, vendor, &testProduct).Verify(now)
			//: The roster lists this subject and the fingerprint matches; only
			//: the mode makes it unusable, which is the point.
			if !errors.Is(err, entitlement.ErrNoPossession) {
				t.Errorf("Verify() error = %v, want %v", err, entitlement.ErrNoPossession)
			}
		})
	}
}
