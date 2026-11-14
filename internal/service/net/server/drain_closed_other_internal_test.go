//go:build !linux

// Package server — how TestDrain_requestHeardInTheDrainIsAnswered sees the
// listener close where no /proc names this process's sockets: by a refused
// dial (#1385).
package server

import (
	"errors"
	stdnet "net"
	"syscall"
	"testing"
)

// listenerCloseWatch returns a check that reports whether a dial to addr is
// refused: the kernel refuses a SYN only once no listener is left on the
// port. Each dial is bounded by drainProbeDial, so a SYN the kernel drops
// instead of refusing is abandoned and dialled again at the next check, and
// an accepted one is closed at once.
func listenerCloseWatch(t *testing.T, addr string) func() bool {
	t.Helper()
	return func() bool {
		p, err := stdnet.DialTimeout("tcp", addr, drainProbeDial)
		if err == nil {
			swallowErr(p.Close())
		}
		return errors.Is(err, syscall.ECONNREFUSED)
	}
}
