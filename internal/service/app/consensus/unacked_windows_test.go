//go:build windows

package consensus

import (
	"net"
	"syscall"
	"testing"
	"time"
)

// A sender link's connection carries the bound on unacknowledged bytes:
// the stack aborts it after WriteTimeout, rounded up to its seconds,
// instead of retransmitting into a vanished peer until its own limit
// (#1116).
func TestBoundUnacknowledgedSetsTheMaxRetransmissionTime(t *testing.T) {
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
		s, gerr = syscall.GetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_TCP, tcpMaxRT)
	}); err != nil || gerr != nil {
		t.Fatal(err, gerr)
	}
	if s != 2 {
		t.Fatalf("TCP_MAXRT %d s, want 2", s)
	}
}
