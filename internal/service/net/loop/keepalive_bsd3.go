//go:build freebsd || netbsd || dragonfly

package loop

import "syscall"

// keepAliveParams sets the idle time and the interval to secs and the probe
// count to keepAliveProbes, the standard library's own.
func keepAliveParams(fd, secs int) error {
	//: in order; the first refusal is the answer.
	return firstErr(
		syscall.SetsockoptInt(fd, syscall.IPPROTO_TCP, syscall.TCP_KEEPIDLE, secs),
		syscall.SetsockoptInt(fd, syscall.IPPROTO_TCP, syscall.TCP_KEEPINTVL, secs),
		syscall.SetsockoptInt(fd, syscall.IPPROTO_TCP, syscall.TCP_KEEPCNT, keepAliveProbes),
	)
}
