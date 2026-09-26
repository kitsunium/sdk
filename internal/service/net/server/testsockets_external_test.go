// Package server_test — the sockets the tests stand up, on every platform.
package server_test

import (
	"os"
	"testing"
)

// sunPathBytes is the room a socket path has in sockaddr_un's sun_path on
// macOS, the smallest of the kernels the suite runs on; Linux and Windows give
// it 108.
const sunPathBytes int = 104

// socketNameRoom is what socketDir keeps free after the directory: a separator
// and a short socket file name.
const socketNameRoom int = len("/socket.sock")

// socketDir returns a directory for the Unix sockets a test binds, removed when
// the test ends. Not t.TempDir(): that path carries the test's name, and on
// macOS $TMPDIR is already long — a socket path past the 104 bytes its sun_path
// holds fails to bind with EINVAL. A directory that leaves no room for a socket
// name fails here, naming itself, rather than as an EINVAL at the first bind.
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
	if len(dir)+socketNameRoom >= sunPathBytes {
		t.Fatalf("socket directory %q (%d bytes) leaves no room for a socket name within the %d bytes sun_path holds", dir, len(dir), sunPathBytes)
	}
	return dir
}
