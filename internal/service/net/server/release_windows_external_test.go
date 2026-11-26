//go:build windows

package server_test

import (
	stdnet "net"
	"testing"
)

// serverListeners is nil here: no descriptor table of this process is read.
func serverListeners(t *testing.T, addr string) []int {
	t.Helper()
	//: Windows answers a second bind on a held address with an error of its own.
	return nil
}

// releasedAfterClose holds that Close released addr: it binds again at once.
// No descriptor of this process can be read, and a bind on a held address is
// refused exclusively there (ADR 0193).
func releasedAfterClose(t *testing.T, addr string, _ []int) {
	t.Helper()
	again, err := stdnet.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("%s is still held after Close: %v", addr, err)
	}
	closeOrFail(t, again)
}
