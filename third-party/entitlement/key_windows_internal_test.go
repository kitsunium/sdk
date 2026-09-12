//go:build windows

package entitlement

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCheckPrivateKeyMode pins the reason licence verification works on Windows
// at all: the mode carries no access-control meaning, so it is not asserted on.
//
// os.Stat does not report an ACL there. It synthesises a mode from the single
// read-only attribute — 0444 when set, 0666 otherwise — so a key written by
// `ktn-linter license create` with os.WriteFile(..., 0600) reads back as 0666.
// The POSIX guard `Perm()&^0600 != 0` is therefore true for EVERY file, and it
// refused every key the tool had itself just written: `license status` reported
// "is readable beyond its owner" and no Windows machine could ever verify.
//
// Both modes must be accepted. Asserting the enrolment mode alone would not
// catch a reintroduced POSIX test, because 0600 also fails that test here —
// only a case that would pass under the POSIX rule and one that would not
// together pin that the rule is gone rather than merely loosened.
func TestCheckPrivateKeyMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode os.FileMode
	}{
		{name: "the mode enrolment asks for", mode: 0o600},
		{name: "a mode the POSIX guard would refuse", mode: 0o666},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "key")
			if err := os.WriteFile(path, []byte("x"), tt.mode); err != nil {
				t.Fatalf("writing fixture: %v", err)
			}
			info, statErr := os.Stat(path)
			if statErr != nil {
				t.Fatalf("stat fixture: %v", statErr)
			}

			//: Refusing here is exactly the regression this test exists to
			//: catch: it would make every Windows machine unverifiable.
			if err := checkPrivateKeyMode(info, path); err != nil {
				t.Errorf("checkPrivateKeyMode(%#o) error = %v, want nil — the POSIX mode guard is back", tt.mode, err)
			}
		})
	}
}
