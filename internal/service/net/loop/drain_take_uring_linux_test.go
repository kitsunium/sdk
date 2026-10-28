//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — io_uring's drain take out of descriptors: it waits for a
// close only while the group holds a connection whose close frees one.
package loop

import (
	"syscall"
	"testing"
)

// TestTakeWaits_outOfDescriptors pins io_uring's rule, the same as epoll's: a
// lack of descriptors keeps the take going only while the group holds a
// connection. The other outcomes do not depend on the group's connections.
func TestTakeWaits_outOfDescriptors(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes the case.
		name string
		// errno is the accept's failure, its completion's result negated.
		errno syscall.Errno
		// live is the connections the group still holds.
		live int64
		// want is whether the take goes on.
		want bool
	}
	tests := []tc{
		{name: "EMFILE with a connection left waits", errno: syscall.EMFILE, live: 1, want: true},
		{name: "EMFILE with none left ends the take", errno: syscall.EMFILE, live: 0, want: false},
		{name: "ENFILE with a connection left waits", errno: syscall.ENFILE, live: 1, want: true},
		{name: "ENFILE with none left ends the take", errno: syscall.ENFILE, live: 0, want: false},
		{name: "its own cancel waits", errno: syscall.ECANCELED, live: 0, want: true},
		{name: "a broken listener ends the take", errno: syscall.EBADF, live: 1, want: false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: whether the take goes on after this failure.
		if got := takeWaits(-int32(c.errno), c.live); got != c.want {
			t.Errorf("takeWaits(-%v, %d) = %v, want %v", c.errno, c.live, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
