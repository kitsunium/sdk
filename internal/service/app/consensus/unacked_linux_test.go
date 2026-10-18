//go:build linux

package consensus

import (
	"net"
	"syscall"
	"testing"
	"time"
)

// A sender link's connection carries the bound on unacknowledged bytes:
// the kernel aborts it after WriteTimeout instead of retransmitting into a
// vanished peer for minutes.
func TestBoundUnacknowledgedSetsTheUserTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := boundUnacknowledged(c, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	rc, err := c.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var ms int
	var gerr error
	if err := rc.Control(func(fd uintptr) {
		ms, gerr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpUserTimeout)
	}); err != nil || gerr != nil {
		t.Fatal(err, gerr)
	}
	if ms != 2000 {
		t.Fatalf("TCP_USER_TIMEOUT %d ms, want 2000", ms)
	}
}
