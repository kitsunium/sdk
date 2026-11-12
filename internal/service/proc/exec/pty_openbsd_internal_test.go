//go:build openbsd

package exec

import (
	"testing"
	"unsafe"
)

// Test_ptmget_layout pins the hand-written ABI of #934's OpenBSD row: struct
// ptmget is 40 bytes, and PTMGET encodes that size, the read direction, group
// 't' and command 1, as _IOR does in sys/ioccom.h.
func Test_ptmget_layout(t *testing.T) {
	t.Parallel()
	const iocOut, iocParmMask = 0x40000000, 0x1fff
	size := uint(unsafe.Sizeof(ptmget{}))
	if size != 40 {
		t.Fatalf("sizeof(ptmget) = %d, want 40", size)
	}
	if want := iocOut | (size&iocParmMask)<<16 | uint('t')<<8 | 1; ptmGet != want {
		t.Fatalf("PTMGET = %#x, want %#x", ptmGet, want)
	}
	if got := cString([]byte{'/', 'd', 'e', 'v', 0, 'x'}); got != "/dev" {
		t.Fatalf("cString = %q, want /dev", got)
	}
	if got := cString([]byte("ttyp0")); got != "ttyp0" {
		t.Fatalf("cString(no NUL) = %q, want ttyp0", got)
	}
}
