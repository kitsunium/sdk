//go:build !linux && !darwin && !windows

package consensus

import (
	"net"
	"syscall"
	"time"
)

// errTimedOut is the error a read or write on a connection the kernel
// dropped at its own retransmission limit returns.
const errTimedOut = syscall.ETIMEDOUT

// boundUnacknowledged sets nothing on the BSDs and illumos, which have no
// per-connection bound on unacknowledged bytes (TCP_USER_TIMEOUT,
// TCP_RXT_CONNDROPTIME, TCP_MAXRT): a write that blocks still misses its
// deadline, and a stream the kernel only retransmits is closed by the
// sender's read-silence bound instead (TCPTransport.silent, #1116).
func boundUnacknowledged(net.Conn, time.Duration) error { return nil }
