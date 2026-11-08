package notify

import (
	"net"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// dialNotify refuses: Windows' AF_UNIX has no SOCK_DGRAM, the only transport
// sd_notify(3) speaks, so a NOTIFY_SOCKET named here cannot be reached. The
// refusal is UnsupportedPlatform, the answer the capability matrix gives
// (ADR 0018), not the socket layer's fault text.
func dialNotify(_ *net.UnixAddr) (*net.UnixConn, error) {
	//: no datagram AF_UNIX socket on this kernel.
	return nil, coreproc.UnsupportedPlatform
}
