//go:build darwin

package consensus

import (
	"net"
	"syscall"
	"time"
)

// tcpRxtConnDropTime is TCP_RXT_CONNDROPTIME of xnu's bsd/netinet/tcp.h,
// which package syscall does not name.
const tcpRxtConnDropTime int = 0x80

// errTimedOut is the error a read or write on a connection the kernel
// dropped returns.
const errTimedOut = syscall.ETIMEDOUT

// unackedBound reports that boundUnacknowledged sets a kernel bound here
// (TCP_RXT_CONNDROPTIME), which Preflight reports as UserTimeout.
const unackedBound = true

// boundUnacknowledged makes the kernel drop c once its retransmissions of
// bytes written on it have gone unacknowledged for d, rounded up to the
// second it counts in (TCP_RXT_CONNDROPTIME, the contract Linux's
// TCP_USER_TIMEOUT states): the next write then fails, and the sender
// dials again. Without it, a link whose peer vanished keeps accepting
// small writes into a stream the kernel retransmits for minutes (#1116).
func boundUnacknowledged(c net.Conn, d time.Duration) error {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return nil
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := rc.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpRxtConnDropTime, unackedSeconds(d))
	}); err != nil {
		return err
	}
	return serr
}
