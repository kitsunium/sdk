//go:build windows

package server

import "syscall"

// setBuffers sets SO_RCVBUF and SO_SNDBUF on the socket, each if above zero.
func setBuffers(fd uintptr, receive, send int) error {
	//: the receive side.
	if receive > 0 {
		if err := syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, receive); err != nil {
			return err
		}
	}
	//: the send side.
	if send > 0 {
		return syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, send)
	}
	return nil
}

// readBuffers reads SO_RCVBUF and SO_SNDBUF from the socket; zero for one
// the system does not answer.
func readBuffers(fd uintptr) (receive, send int) {
	return bufferOption(fd, syscall.SO_RCVBUF), bufferOption(fd, syscall.SO_SNDBUF)
}

// bufferOption reads one socket option, zero when the system does not answer.
func bufferOption(fd uintptr, option int) int {
	value, err := syscall.GetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, option)
	//: unanswered counts as unknown.
	if err != nil {
		return 0
	}
	return value
}
