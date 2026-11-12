//go:build windows

package consensus

import (
	"net"
	"syscall"
	"time"
)

// tcpMaxRT is TCP_MAXRT of the Windows SDK's ws2ipdef.h, which package
// syscall does not name.
const tcpMaxRT int = 5

// errTimedOut is WSAETIMEDOUT of winerror.h, the error a read or write on
// a connection the stack dropped returns; package syscall does not name it.
const errTimedOut syscall.Errno = 10060

// boundUnacknowledged makes the stack abort c once its retransmissions of
// bytes written on it have gone unacknowledged for d, rounded up to the
// second it counts in (TCP_MAXRT, the contract Linux's TCP_USER_TIMEOUT
// states): the next write then fails, and the sender dials again. Without
// it, a link whose peer vanished keeps accepting small writes into a
// stream the stack retransmits until its own limit (#1116).
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
		serr = syscall.SetsockoptInt(syscall.Handle(fd), syscall.IPPROTO_TCP, tcpMaxRT, unackedSeconds(d))
	}); err != nil {
		return err
	}
	return serr
}
