//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd || solaris || aix

package server

import (
	stdnet "net"
	"syscall"
)

// setBacklog calls listen(2) again on ln's socket with n, and says why it
// could not: the empty string is success.
func setBacklog(ln stdnet.Listener, n int) string {
	sc, ok := ln.(syscall.Conn)
	//: a listener with no descriptor has no backlog to set.
	if !ok {
		return "the listener has no descriptor"
	}
	raw, err := sc.SyscallConn()
	//: unreachable descriptor.
	if err != nil {
		return err.Error()
	}
	var listenErr error
	//: the runtime holds the descriptor open for the call.
	if err := raw.Control(func(fd uintptr) { listenErr = syscall.Listen(int(fd), n) }); err != nil {
		return err.Error()
	}
	//: the kernel's answer.
	if listenErr != nil {
		return listenErr.Error()
	}
	return ""
}
