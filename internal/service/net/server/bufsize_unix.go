//go:build unix

package server

import (
	"errors"
	"syscall"
)

// setBuffers sets SO_RCVBUF and SO_SNDBUF on the descriptor, each if above zero.
func setBuffers(fd uintptr, receive, send int) error {
	//: the receive side.
	if receive > 0 {
		if err := bufferRefusal(syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, receive)); err != nil {
			return err
		}
	}
	//: the send side.
	if send > 0 {
		return bufferRefusal(syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, send))
	}
	return nil
}

// bufferRefusal is what a setsockopt's error means to a group: Linux clamps a
// size past its ceiling, the BSDs and darwin answer ENOBUFS and keep what the
// socket held, which is the same fact — the group gets less than it asked, and
// State reports what it holds — so it fails no bind. Any other error does.
func bufferRefusal(err error) error {
	//: past the platform's ceiling: reported by State, never a fault.
	if errors.Is(err, syscall.ENOBUFS) {
		return nil
	}
	return err
}

// readBuffers reads SO_RCVBUF and SO_SNDBUF from the descriptor; zero for one
// the kernel does not answer.
func readBuffers(fd uintptr) (receive, send int) {
	return bufferOption(fd, syscall.SO_RCVBUF), bufferOption(fd, syscall.SO_SNDBUF)
}

// bufferOption reads one socket option, zero when the kernel does not answer.
func bufferOption(fd uintptr, option int) int {
	value, err := syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, option)
	//: unanswered counts as unknown.
	if err != nil {
		return 0
	}
	return value
}
