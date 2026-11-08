//go:build !unix && !windows

package ipc

import (
	"net"

	coreipc "github.com/kitsunium/sdk/internal/core/proc/ipc"
)

// withFiles passes no files here: the connection is returned as it is, and
// asserting FileSender on it fails.
func withFiles(c *net.UnixConn, _ coreipc.PeerValue) net.Conn { return c }
