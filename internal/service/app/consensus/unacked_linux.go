//go:build linux

package consensus

import (
	"net"
	"syscall"
	"time"
)

// tcpUserTimeout is TCP_USER_TIMEOUT of linux/tcp.h, which package syscall
// does not name.
const tcpUserTimeout int = 0x12

// boundUnacknowledged makes the kernel abort c once bytes written on it
// stay unacknowledged for d (TCP_USER_TIMEOUT): the next write then fails,
// and the sender dials again. Without it, a link whose peer vanished and
// came back — a partition healed, an address reconnected — keeps
// accepting small writes into a stream the kernel retransmits under an
// exponential backoff, and they reach the peer only at the next
// retransmission, tens of seconds later.
func boundUnacknowledged(c net.Conn, d time.Duration) error {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return nil
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		return err
	}
	ms := int(d / time.Millisecond)
	var serr error
	if err := rc.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpUserTimeout, ms)
	}); err != nil {
		return err
	}
	return serr
}
