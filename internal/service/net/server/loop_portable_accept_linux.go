//go:build linux

package server

import "syscall"

// acceptCloexec accepts on fd with the close-on-exec flag set by the same
// call, so a fork in between never inherits the connection.
func acceptCloexec(fd int) (int, error) {
	nfd, _, err := syscall.Accept4(fd, syscall.SOCK_CLOEXEC)
	//: the descriptor, or the kernel's answer.
	return nfd, err
}
