//go:build !windows

package ipc

import (
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
)

// TestNobodyListens pins which failed dials make a socket a leftover (issue
// #452): the kernel's ECONNREFUSED, or the file gone, and nothing else. A full
// accept queue (EAGAIN) and a dial out of time leave the owner possibly alive,
// so the socket is IN_USE rather than removed.
func TestNobodyListens(t *testing.T) {
	opError := func(errno error) error {
		return &net.OpError{Op: "dial", Net: "unix", Err: &os.SyscallError{Syscall: "connect", Err: errno}}
	}
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"a refused connection", opError(syscall.ECONNREFUSED), true},
		{"the file gone", opError(syscall.ENOENT), true},
		{"a full accept queue", opError(syscall.EAGAIN), false},
		{"a dial out of time", opError(os.ErrDeadlineExceeded), false},
		{"a permission refused", opError(syscall.EACCES), false},
		{"an error with no errno", errors.New("unknown"), false},
	} {
		if got := nobodyListens(c.err); got != c.want {
			t.Errorf("%s: nobodyListens = %v, want %v", c.name, got, c.want)
		}
	}
}
