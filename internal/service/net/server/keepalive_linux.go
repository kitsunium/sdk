//go:build linux

package server

import (
	"strings"
	"syscall"
	"time"
)

// tcpUserTimeoutOption is TCP_USER_TIMEOUT (Linux 2.6.37, include/uapi/linux/tcp.h):
// absent from the standard library's syscall package.
const tcpUserTimeoutOption int = 0x12

// userTimeoutControl sets TCP_USER_TIMEOUT on a TCP socket about to be bound,
// which every socket it accepts inherits (ADR 0189); it runs from the Control
// hook, the same place as the socket's other options.
func userTimeoutControl(spec socketSpec, network string, rawConn syscall.RawConn) error {
	//: nothing asked, or a socket with no TCP.
	if spec.userTimeout <= 0 || !strings.HasPrefix(network, "tcp") {
		//: nothing to set.
		return nil
	}
	ms := spec.userTimeout.Milliseconds()
	//: a fraction of a millisecond must not round down to no timeout at all.
	if time.Duration(ms)*time.Millisecond < spec.userTimeout {
		ms++
	}
	var setErr error
	//: the descriptor, before bind.
	if err := rawConn.Control(func(fd uintptr) {
		setErr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, tcpUserTimeoutOption, int(ms))
	}); err != nil {
		//: the descriptor could not be reached.
		return err
	}
	//: the kernel's answer: a refusal fails the bind, as a buffer's does.
	return setErr
}
