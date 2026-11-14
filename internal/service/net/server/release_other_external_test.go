//go:build !linux

package server_test

import (
	stdnet "net"
	"testing"
)

// releasedAfterClose holds that Close released addr: it binds again at once.
// Off Linux no table names a socket's process, so the rebind is the proof;
// it holds while no test of this process forks beside the caller, which
// keeps its descriptors in the child until its exec (#1386).
func releasedAfterClose(t *testing.T, addr string) {
	t.Helper()
	again, err := stdnet.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("%s is still held after Close: %v", addr, err)
	}
	closeOrFail(t, again)
}
