// Package server_test — the sockets the tests stand up, on every platform.
package server_test

import (
	"os"
	"testing"
)

// socketDir returns a directory for the Unix sockets a test binds, removed when
// the test ends. Not t.TempDir(): that path carries the test's name, and on
// macOS $TMPDIR is already long — a socket path past the 104 bytes its sun_path
// holds (108 on Linux) fails to bind with EINVAL.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sock")
	if err != nil {
		t.Fatalf("socket directory: %v", err)
	}
	t.Cleanup(func() {
		if rerr := os.RemoveAll(dir); rerr != nil {
			t.Errorf("remove socket directory: %v", rerr)
		}
	})
	return dir
}
