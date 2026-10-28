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
// connection.
func TestTakeWaits_outOfDescriptors(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EMFILE, syscall.ENFILE} {
		if !takeWaits(-int32(errno), 1) {
			t.Errorf("takeWaits(-%v, 1) = false, want true", errno)
		}
		if takeWaits(-int32(errno), 0) {
			t.Errorf("takeWaits(-%v, 0) = true, want false", errno)
		}
	}
	//: the other outcomes do not depend on the group's connections.
	if !takeWaits(-int32(syscall.ECANCELED), 0) {
		t.Error("takeWaits(-ECANCELED, 0) = false, want true")
	}
	if takeWaits(-int32(syscall.EBADF), 1) {
		t.Error("takeWaits(-EBADF, 1) = true, want false")
	}
}
