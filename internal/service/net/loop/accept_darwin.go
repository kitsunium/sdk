//go:build darwin

package loop

import "syscall"

// acceptConn accepts on lfd a connection that is close-on-exec and
// non-blocking. darwin has no accept4: close-on-exec is set under the fork
// lock, so a fork in between never inherits the connection, as the standard
// library does there.
func acceptConn(lfd int) (int, error) {
	syscall.ForkLock.RLock()
	nfd, _, err := syscall.Accept(lfd)
	//: marked before the lock is released.
	if err == nil {
		syscall.CloseOnExec(nfd)
	}
	syscall.ForkLock.RUnlock()
	//: refused: nothing to mark.
	if err != nil {
		//: the kernel's answer.
		return -1, err
	}
	//: the loop never blocks on a connection.
	if err := syscall.SetNonblock(nfd, true); err != nil {
		swallowErr(syscall.Close(nfd))
		//: the kernel's answer.
		return -1, err
	}
	//: the descriptor.
	return nfd, nil
}

// noSigPipe sets SO_NOSIGPIPE on an accepted socket: darwin has no
// MSG_NOSIGNAL, and a write to a peer that left must fail with EPIPE, never
// raise SIGPIPE. A socket that refuses it still serves: the Go runtime
// ignores SIGPIPE on any descriptor but standard output and error.
func noSigPipe(fd int) {
	swallowErr(syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_NOSIGPIPE, 1))
}
