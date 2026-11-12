//go:build freebsd || openbsd || netbsd || dragonfly

package loop

import "syscall"

// acceptConn accepts on lfd a connection that is close-on-exec and
// non-blocking from its birth (accept4).
func acceptConn(lfd int) (int, error) {
	nfd, _, err := syscall.Accept4(lfd, syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC)
	//: the descriptor, or the kernel's answer.
	return nfd, err
}

// noSigPipe does nothing here: a SIGPIPE on a socket is the Go runtime's to
// ignore on every descriptor but standard output and error, and these BSDs'
// writes are refused with EPIPE either way.
func noSigPipe(_ int) {
	//: nothing to set.
}
