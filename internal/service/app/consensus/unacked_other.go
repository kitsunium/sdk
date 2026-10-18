//go:build !linux

package consensus

import (
	"net"
	"time"
)

// boundUnacknowledged sets nothing off Linux, which has no TCP_USER_TIMEOUT:
// a write that blocks still misses its deadline, a stream the kernel only
// retransmits is not seen.
func boundUnacknowledged(net.Conn, time.Duration) error { return nil }
