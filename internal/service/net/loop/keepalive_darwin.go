//go:build darwin

package loop

import "syscall"

// The keepalive interval and probe count of darwin's <netinet/tcp.h>, absent
// from package syscall, cited here as ADR 0018 asks. Its idle time is
// TCP_KEEPALIVE.
const (
	// tcpKeepIntvl is TCP_KEEPINTVL.
	tcpKeepIntvl int = 0x101
	// tcpKeepCnt is TCP_KEEPCNT.
	tcpKeepCnt int = 0x102
)

// keepAliveParams sets the idle time and the interval to secs and the probe
// count to keepAliveProbes, the standard library's own.
func keepAliveParams(fd, secs int) error {
	//: in order; the first refusal is the answer.
	return firstErr(
		syscall.SetsockoptInt(fd, syscall.IPPROTO_TCP, syscall.TCP_KEEPALIVE, secs),
		syscall.SetsockoptInt(fd, syscall.IPPROTO_TCP, tcpKeepIntvl, secs),
		syscall.SetsockoptInt(fd, syscall.IPPROTO_TCP, tcpKeepCnt, keepAliveProbes),
	)
}
