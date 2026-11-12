//go:build linux

package loop

import (
	"syscall"
	"testing"
)

func init() {
	wakers = append(wakers, struct {
		name string
		mk   func(tb testing.TB) func()
	}{"wake=eventfd", func(tb testing.TB) func() {
		efd, _, errno := syscall.RawSyscall(syscall.SYS_EVENTFD2, 0, efdNonblock|efdCloexec, 0)
		if errno != 0 {
			tb.Skipf("eventfd: %v", errno)
		}
		fd := int(efd)
		tb.Cleanup(func() { _ = syscall.Close(fd) })
		one := [8]byte{1}
		drain := make([]byte, 8)
		return func() {
			// The drain side's read, so the counter never saturates.
			_, _ = syscall.Write(fd, one[:])
			_, _ = syscall.Read(fd, drain)
		}
	}})
}
