//go:build unix && !darwin

package server_test

import "syscall"

// acceptsConnections reports whether the socket fd is listening, read from
// SO_ACCEPTCONN, which these kernels answer at the socket level.
func acceptsConnections(fd int) (bool, error) {
	accepting, err := syscall.GetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_ACCEPTCONN)
	return accepting != 0, err
}
