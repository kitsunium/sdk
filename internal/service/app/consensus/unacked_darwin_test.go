//go:build darwin

package consensus

import (
	"net"
	"syscall"
	"testing"
	"time"
)

// A sender link's connection carries the bound on unacknowledged bytes:
// the kernel drops it after WriteTimeout, rounded up to its seconds,
// instead of retransmitting into a vanished peer for minutes (#1116).
func TestBoundUnacknowledgedSetsTheConnDropTime(t *testing.T) {
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
	if err := boundUnacknowledged(c, 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	rc, err := c.(*net.TCPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var s int
	var gerr error
	if err := rc.Control(func(fd uintptr) {
		s, gerr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpRxtConnDropTime)
	}); err != nil || gerr != nil {
		t.Fatal(err, gerr)
	}
	if s != 2 {
		t.Fatalf("TCP_RXT_CONNDROPTIME %d s, want 2", s)
	}
}
