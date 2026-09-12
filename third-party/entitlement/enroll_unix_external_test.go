//go:build !windows

package entitlement_test

import (
	"os"
	"path/filepath"
	"testing"

	entitlement "github.com/kitsunium/sdk/third-party/entitlement"
)

// TestGenerateKeyPairPosixModes pins what enrolment writes, and what it refuses
// to write into, on a filesystem where the mode bits mean something.
//
// POSIX-only, in its own build-tagged file, because the fixture itself cannot
// exist on Windows: os.Chmod there maps only the read-only attribute, which
// NTFS ignores for directories, and os.Stat reports a synthesised 0666/0777
// whatever os.WriteFile was asked for. A shared case would have to be skipped
// at runtime, and this project forbids t.Skip — a test that does not run is
// not a test.
func TestGenerateKeyPairPosixModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// dirMode is applied to a pre-created key directory; zero leaves the
		// directory absent so enrolment creates it itself.
		dirMode os.FileMode
		// wantErr is whether enrolment must refuse outright.
		wantErr bool
		// wantDirMode is the mode a directory enrolment created must carry.
		wantDirMode os.FileMode
		reason      string
	}{
		{
			name:        "a directory it creates is owner-only",
			wantDirMode: 0o700,
			reason:      "ssh refuses to use anything looser, so a key written into a 0755 directory is unusable by the very tool the convention exists for",
		},
		{
			name:    "a read-only directory is refused",
			dirMode: 0o500,
			wantErr: true,
			reason:  "half an identity is worse than none: the machine would hold a key it cannot pair, and every later failure would name the wrong cause",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sshDir := filepath.Join(t.TempDir(), ".ssh")
			//: Only the read-only case starts with a directory to restrict.
			if tt.dirMode != 0 {
				if err := os.MkdirAll(sshDir, 0o700); err != nil {
					t.Fatalf("creating fixture dir: %v", err)
				}
				//: Restore write permission before the temp dir is removed:
				//: t.TempDir's cleanup cannot delete a 0o500 directory's
				//: contents and reports that as a test failure rather than the
				//: fixture detail it is.
				t.Cleanup(func() {
					if chmodErr := os.Chmod(sshDir, 0o700); chmodErr != nil {
						t.Errorf("restoring fixture mode: %v", chmodErr)
					}
				})
				if err := os.Chmod(sshDir, tt.dirMode); err != nil {
					t.Fatalf("setting fixture mode: %v", err)
				}
			}

			_, err := testProduct.GenerateKeyPair(sshDir, sampleUUID)
			if tt.wantErr {
				//: root ignores the permission bits, so the write SUCCEEDS
				//: there and refusing would be the wrong assertion. What this
				//: case actually pins is that enrolment obeys the filesystem
				//: rather than working around it — which reads as "refused"
				//: for an ordinary user and "allowed" for root. Asserting the
				//: refusal unconditionally would fail every run in a root
				//: container, and t.Skip is not available: a test that does
				//: not run is not a test.
				if os.Geteuid() == 0 {
					if err != nil {
						t.Errorf("testProduct.GenerateKeyPair() error = %v, want nil — root is not bound by the mode", err)
					}
					return
				}
				if err == nil {
					t.Errorf("testProduct.GenerateKeyPair() succeeded into a read-only directory (%s)", tt.reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("testProduct.GenerateKeyPair() error = %v, want nil", err)
			}

			info, statErr := os.Stat(sshDir)
			if statErr != nil {
				t.Fatalf("stat key directory: %v", statErr)
			}
			if info.Mode().Perm() != tt.wantDirMode {
				t.Errorf("key directory mode = %#o, want %#o (%s)", info.Mode().Perm(), tt.wantDirMode, tt.reason)
			}

			keyInfo, keyErr := os.Stat(entitlement.PrivateKeyPath(sshDir, sampleUUID))
			if keyErr != nil {
				t.Fatalf("stat private key: %v", keyErr)
			}
			//: Anything looser makes the secret machine-wide, and
			//: SignerFromFile would rightly refuse it afterwards: enrolment
			//: must not produce a key its own verifier rejects.
			if keyInfo.Mode().Perm() != ownerOnly {
				t.Errorf("private key mode = %#o, want %#o", keyInfo.Mode().Perm(), ownerOnly)
			}
		})
	}
}
