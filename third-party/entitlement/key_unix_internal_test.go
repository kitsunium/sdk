//go:build !windows

package entitlement

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	coreent "github.com/kitsunium/sdk/internal/core/entitlement"
)

// TestCheckPrivateKeyMode pins the POSIX half of the permission gate: mode bits
// ARE the access control here, so a key the whole machine can read is not a
// secret and the possession proof it backs is theatre.
//
// Build-tagged rather than shared with the Windows case because the assertion
// cannot be stated once. There os.Stat synthesises 0666/0444 from the
// read-only attribute, so every key — including the one enrolment just wrote at
// 0600 — would have to be accepted; key_windows_internal_test.go asserts that
// instead of leaving it unstated.
func TestCheckPrivateKeyMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mode    os.FileMode
		wantErr bool
		reason  string
	}{
		{name: "owner-only is what enrolment writes", mode: 0o600, wantErr: false},
		{name: "owner read-only is still owner-only", mode: 0o400, wantErr: false},
		{name: "group readable is refused", mode: 0o640, wantErr: true, reason: "anyone in the group can read the secret"},
		{name: "world readable is refused", mode: 0o644, wantErr: true, reason: "every account on the machine can read the secret"},
		{name: "world writable is refused", mode: 0o666, wantErr: true, reason: "anyone can replace the identity outright"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(path, []byte("x"), tt.mode); err != nil {
				t.Fatalf("writing fixture: %v", err)
			}
			//: os.WriteFile is subject to the process umask, so the mode is
			//: set explicitly afterwards rather than assumed.
			if err := os.Chmod(path, tt.mode); err != nil {
				t.Fatalf("setting fixture mode: %v", err)
			}
			info, statErr := os.Stat(path)
			if statErr != nil {
				t.Fatalf("stat fixture: %v", statErr)
			}

			err := checkPrivateKeyMode(info, path)
			if tt.wantErr {
				//: Refusing loudly beats authorizing on a machine-wide secret.
				if !errors.Is(err, coreent.ErrNoPossession) {
					t.Errorf("checkPrivateKeyMode(%#o) error = %v, want %v (%s)", tt.mode, err, coreent.ErrNoPossession, tt.reason)
				}
				return
			}
			if err != nil {
				t.Errorf("checkPrivateKeyMode(%#o) error = %v, want nil", tt.mode, err)
			}
		})
	}
}
