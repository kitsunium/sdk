//go:build linux

package server_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/service/net/server"
)

// releasedAfterClose holds that Close released addr: no listening socket of
// this process is left on it, read in the kernel's tables by inode
// (/proc/self/fd). A rebind would also answer for sockets that are not this
// process's to release — a copy of the descriptor table a concurrent fork
// holds until its exec, a port another process drew in between (#1386).
func releasedAfterClose(t *testing.T, addr string) {
	t.Helper()
	if n := server.OwnListenersOn(t, addr); n != 0 {
		t.Fatalf("%d listening sockets of this process still on %s after Close", n, addr)
	}
}
