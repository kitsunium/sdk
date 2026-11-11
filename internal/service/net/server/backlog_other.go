//go:build !(linux || darwin || dragonfly || freebsd || netbsd || openbsd || solaris || aix)

package server

import (
	stdnet "net"
	"runtime"
)

// setBacklog refuses: Windows' listen backlog is SOMAXCONN_HINT's, which the
// standard library sets and a second listen call does not move, and no other
// platform without the call has a socket to size.
func setBacklog(stdnet.Listener, int) string {
	return "the listen backlog is the platform's on " + runtime.GOOS
}
