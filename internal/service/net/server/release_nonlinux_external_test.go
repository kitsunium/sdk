//go:build unix && !linux

package server_test

import (
	"net/netip"
	"testing"
)

// serverListeners is the descriptors the server holds on addr, read once the
// server started and before Close.
func serverListeners(t *testing.T, addr string) []int {
	t.Helper()
	ap, err := netip.ParseAddrPort(addr)
	//: the server reported an address that is not one.
	if err != nil {
		t.Fatalf("server address %q: %v", addr, err)
	}
	fds := listeningFDsOn(ap)
	//: nothing held would make the check below prove nothing.
	if len(fds) == 0 {
		t.Fatalf("no listening socket of this process on %s while the server runs", addr)
	}
	return fds
}

// releasedAfterClose holds that Close released the sockets it held on addr:
// none of the descriptors read before is a listener on it any more. It does
// not bind again: a freed ephemeral port goes back to the kernel's pool, and
// a sibling test, another shard or another process may draw it before the
// bind, which then reads as a socket the server left (#1386).
func releasedAfterClose(t *testing.T, addr string, before []int) {
	t.Helper()
	ap, err := netip.ParseAddrPort(addr)
	//: the server reported an address that is not one.
	if err != nil {
		t.Fatalf("server address %q: %v", addr, err)
	}
	//: the server's own descriptors alone.
	if held := stillListening(before, ap); len(held) != 0 {
		t.Fatalf("descriptors %v of this process still listen on %s after Close", held, addr)
	}
}
