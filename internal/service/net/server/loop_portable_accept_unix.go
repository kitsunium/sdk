//go:build unix && !linux

package server

import "syscall"

// acceptCloexec accepts on fd and sets close-on-exec under the fork lock, so
// a fork in between never inherits the connection: no accept4 everywhere.
func acceptCloexec(fd int) (int, error) {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	nfd, _, err := syscall.Accept(fd)
	//: refused: nothing to mark.
	if err != nil {
		//: the kernel's answer.
		return -1, err
	}
	syscall.CloseOnExec(nfd)
	//: the descriptor.
	return nfd, nil
}
