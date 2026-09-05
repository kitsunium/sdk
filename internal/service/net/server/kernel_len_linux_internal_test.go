//go:build linux

// Package server — the width-correct kernel length assignment.
package server

import (
	"math"
	"syscall"
	"testing"
)

// kernelLen widens a kernel length field so a test can compare it whatever the
// architecture made it. Writing the conversion against the type parameter is
// what keeps it correct on both: uint64(x) on a concrete field is redundant on
// 64-bit and required on 32-bit, so neither concrete form compiles everywhere.
func kernelLen[T ~uint32 | ~uint64](v T) uint64 {
	//: the wide form is the only one that can hold either field.
	return uint64(v)
}

// Test_setKernelLen pins the conversion the batched datagram path depends on.
//
// syscall.Iovec.Len and syscall.Msghdr.Iovlen follow the C size_t: uint64 on
// 64-bit Linux and uint32 on 32-bit. setKernelLen exists so one call site is
// correct on both — but a helper that merely compiles everywhere proves nothing
// about what it writes. This exercises BOTH instantiations explicitly, so the
// narrow field is covered on the 64-bit host the suite actually runs on, where
// it is otherwise unreachable.
func Test_setKernelLen(t *testing.T) {
	t.Parallel()
	type tc struct {
		// name describes where the length comes from.
		name string
		// n is the length in bytes, as the wiring computes it.
		n int
	}
	tests := []tc{
		{name: "an empty buffer", n: 0},
		{name: "the iovec count wire writes", n: 1},
		{name: "the domain default datagram", n: defaultMaxPacketSize},
		{name: "the largest datagram UDP can carry", n: 65535},
		{name: "a megabyte read buffer", n: 1 << 20},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: the narrow field is what a 32-bit kernel reads; writing it here is
		//: what makes this test mean anything on a 64-bit host.
		var narrow uint32
		setKernelLen(&narrow, c.n)
		if kernelLen(narrow) != uint64(c.n) {
			t.Fatalf("setKernelLen into a uint32 field wrote %d for a length of %d — "+
				"a 32-bit kernel would read a truncated buffer length", narrow, c.n)
		}
		//: the wide field is what a 64-bit kernel reads.
		var wide uint64
		setKernelLen(&wide, c.n)
		if kernelLen(wide) != uint64(c.n) {
			t.Fatalf("setKernelLen into a uint64 field wrote %d for a length of %d", wide, c.n)
		}
		//: and the real field, whichever width this architecture selected.
		var slot syscall.Iovec
		setKernelLen(&slot.Len, c.n)
		if kernelLen(slot.Len) != uint64(c.n) {
			t.Fatalf("setKernelLen into syscall.Iovec.Len wrote %d for a length of %d",
				slot.Len, c.n)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_setKernelLen_Ceiling pins the boundary the narrow field imposes.
//
// On 32-bit Linux the length field is a uint32, so a length above 4 GiB - 1
// reaches the kernel truncated rather than refused. Every length this package
// wires is a datagram buffer or the constant 1, all far below that — but "far
// below" is worth asserting rather than assuming, because the ceiling is
// invisible on the 64-bit host the suite runs on.
func Test_setKernelLen_Ceiling(t *testing.T) {
	t.Parallel()
	const (
		// ceiling is the largest length a 32-bit kernel length field can hold.
		ceiling uint64 = math.MaxUint32
		// widestLength is the largest length this architecture can express at
		// all, since setKernelLen is handed an int.
		widestLength uint64 = math.MaxInt
	)
	type tc struct {
		// name describes where the length comes from.
		name string
		// n is the length this package actually hands the kernel.
		n int
	}
	tests := []tc{
		{name: "the iovec count", n: 1},
		{name: "the default datagram ceiling", n: defaultMaxPacketSize},
		{name: "the default batch size", n: defaultBatchSize},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: a wired length above the ceiling would be silently truncated.
		if uint64(c.n) > ceiling {
			t.Fatalf("%s is %d, above the 32-bit length ceiling of %d", c.name, c.n, ceiling)
		}
		//: on a 32-bit host int stops at 2^31-1, so no length can even reach the
		//: ceiling and there is nothing left to demonstrate.
		if widestLength <= ceiling {
			return
		}
		//: show the ceiling belongs to the FIELD rather than to the helper —
		//: which is precisely why the wired lengths above have to stay under it.
		over := ceiling + 1
		var narrow uint32
		setKernelLen(&narrow, int(over))
		if uint64(narrow) == over {
			t.Fatalf("a uint32 length field held %d, above its own ceiling of %d", over, ceiling)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
