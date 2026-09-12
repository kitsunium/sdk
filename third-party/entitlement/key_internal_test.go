package entitlement

import (
	"os"
	"testing"
)

// TestKeyConstants pins the two values the possession proof rests on. Both
// are security parameters rather than tuning knobs: loosening either silently
// weakens the scheme without breaking any behaviour a functional test would
// notice.
func TestKeyConstants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		got     int
		atLeast int
		reason  string
	}{
		{
			name:    "the challenge carries enough entropy",
			got:     nonceSize,
			atLeast: 32,
			reason:  "a short nonce makes precomputed signatures viable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.got < tt.atLeast {
				t.Errorf("%s: got %d, want at least %d (%s)", tt.name, tt.got, tt.atLeast, tt.reason)
			}
		})
	}

	t.Run("a private key may not be group or world readable", func(t *testing.T) {
		t.Parallel()

		//: Any bit outside the owner triad turns the secret machine-wide.
		if keyFileMode&^os.FileMode(0o600) != 0 {
			t.Errorf("keyFileMode = %#o, want no bits beyond owner read/write", keyFileMode)
		}
	})
}
