//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — busy-poll's io_uring half (#979): struct io_uring_napi
// frozen, and the window the kernel holds once registered.
package loop

import (
	"syscall"
	"testing"
	"unsafe"
)

// TestURingABI_napi freezes struct io_uring_napi and its register opcode
// against include/uapi/linux/io_uring.h (6.9; 6.13 turns pad[0] into
// opcode and the first half of resv into op_param, both zero here).
func TestURingABI_napi(t *testing.T) {
	var n uringNAPI
	if unsafe.Sizeof(n) != 16 || unsafe.Offsetof(n.prefer) != 4 || unsafe.Offsetof(n.resv) != 8 {
		t.Fatalf("struct io_uring_napi: size %d, prefer %d, resv %d; want 16, 4, 8", unsafe.Sizeof(n), unsafe.Offsetof(n.prefer), unsafe.Offsetof(n.resv))
	}
	if regNAPI != 27 {
		t.Fatalf("IORING_REGISTER_NAPI %d, want 27", regNAPI)
	}
}

// TestURing_registerNAPI registers a window on a throwaway ring, then another:
// the kernel hands the first back, which proves it read the struct where it
// is laid out.
func TestURing_registerNAPI(t *testing.T) {
	u, step, errno := openURing(probeEntries, 0)
	if errno != 0 {
		t.Skipf("io_uring %s: %v", step, errno)
	}
	defer u.close()
	if _, errno := u.registerNAPI(50, true); errno == syscall.EINVAL {
		t.Skipf("IORING_REGISTER_NAPI refused on %s: before 6.9", kernelRelease())
	} else if errno != 0 {
		t.Fatalf("register: %v", errno)
	}
	prev, errno := u.registerNAPI(7, false)
	if errno != 0 || prev.busyPollTo != 50 || prev.prefer != 1 {
		t.Fatalf("second register: %v, previous %+v, want 50 µs and prefer", errno, prev)
	}
}
