//go:build goexperiment.simd && amd64

package swar

import (
	"math/bits"
	"simd/archsimd"
)

// vectorWidth is the bytes one AVX2 register covers.
const vectorWidth int = 32

// vectorMin is the shortest slice the vector forms take: below it the
// register setup costs more than the word loop it replaces, and a slice of
// one register or more can end on an overlapping load (see the note above avx2IndexByte2).
const vectorMin int = vectorWidth

// hasVector reports whether this CPU runs the vector forms: AVX2, checked
// once at program start. The SWAR forms are in the same binary, and take
// every call when it is false and every call below vectorMin when it is not.
var hasVector = archsimd.X86.AVX2()

// Every AVX2 form below is one loop over registers, the last of them the one
// at len(b)-vectorWidth: it overlaps the register before it when the length
// is no multiple of vectorWidth, over bytes that register already searched
// and found no match in, so the first match of the last register is the
// first of the slice, and no byte past the length is read. min holds the
// offset there. Each ends in ClearAVXUpperBits before it returns: the
// compiler does not emit the VZEROUPPER that keeps the SSE code which follows
// — the runtime's, the caller's — from paying for the dirty upper halves
// (archsimd's doc says it may one day, and then this call is redundant).

// avx2IndexByte2 is IndexByte2's AVX2 form, for len(b) at least vectorWidth.
func avx2IndexByte2(b []byte, c1, c2 byte) int {
	pat1, pat2 := archsimd.BroadcastUint8x32(c1), archsimd.BroadcastUint8x32(c2)
	last := len(b) - vectorWidth
	found := -1
	//: a register at a time, the last one at len(b)-vectorWidth.
	for off := 0; ; off += vectorWidth {
		off = min(off, last)
		x := archsimd.LoadUint8x32(b[off : off+vectorWidth])
		//: the first lane of the first register that holds c1 or c2.
		if mask := x.Equal(pat1).Or(x.Equal(pat2)).ToBits(); mask != 0 {
			found = off + bits.TrailingZeros32(mask)
			//: the match, found.
			break
		}
		//: the last register is searched; no byte of b is c1 or c2.
		if off == last {
			//: no match.
			break
		}
	}
	archsimd.ClearAVXUpperBits()
	return found
}

// avx2IndexLess is IndexLess's AVX2 form, for len(b) at least vectorWidth and
// n at least 1: x is below n exactly when min(x, n-1) is x.
func avx2IndexLess(b []byte, n byte) int {
	bound := archsimd.BroadcastUint8x32(n - 1)
	last := len(b) - vectorWidth
	found := -1
	//: a register at a time, the last one at len(b)-vectorWidth.
	for off := 0; ; off += vectorWidth {
		off = min(off, last)
		x := archsimd.LoadUint8x32(b[off : off+vectorWidth])
		//: the first lane of the first register that holds a byte below n.
		if mask := x.Min(bound).Equal(x).ToBits(); mask != 0 {
			found = off + bits.TrailingZeros32(mask)
			//: the match, found.
			break
		}
		//: the last register is searched; no byte of b is below n.
		if off == last {
			//: no match.
			break
		}
	}
	archsimd.ClearAVXUpperBits()
	return found
}

// avx2IndexSet is Set.Index's AVX2 form, for len(b) at least vectorWidth.
func avx2IndexSet(b []byte, s Set) int {
	pat1, pat2 := archsimd.BroadcastUint8x32(s.first), archsimd.BroadcastUint8x32(s.second)
	bound := archsimd.BroadcastUint8x32(s.below - 1)
	var zero archsimd.Int8x32
	last := len(b) - vectorWidth
	found := -1
	//: a register at a time, the last one at len(b)-vectorWidth.
	for off := 0; ; off += vectorWidth {
		off = min(off, last)
		x := archsimd.LoadUint8x32(b[off : off+vectorWidth])
		m := x.Equal(pat1).Or(x.Equal(pat2))
		//: a bound of zero holds no byte, and its n-1 would wrap to every byte.
		if s.below != 0 {
			m = m.Or(x.Min(bound).Equal(x))
		}
		//: the bytes from 0x80 up are the negative lanes.
		if s.upper {
			m = m.Or(zero.Greater(x.AsInt8x32()))
		}
		//: the first lane of the first register that holds a byte of the set.
		if mask := m.ToBits(); mask != 0 {
			found = off + bits.TrailingZeros32(mask)
			//: the match, found.
			break
		}
		//: the last register is searched; no byte of b is in the set.
		if off == last {
			//: no match.
			break
		}
	}
	archsimd.ClearAVXUpperBits()
	return found
}

// indexByte2 is IndexByte2's body: the AVX2 form from vectorMin bytes up on a
// CPU that has it; otherwise the word loop for a short slice and two scans of
// bytes.IndexByte for a long one.
func indexByte2(b []byte, c1, c2 byte) int {
	//: a long slice on an AVX2 CPU takes the vector form.
	if hasVector && len(b) >= vectorMin {
		//: the vector form.
		return avx2IndexByte2(b, c1, c2)
	}
	//: a long slice without AVX2 takes the standard library's assembly.
	if len(b) >= twoScanMin {
		//: two scans.
		return twoScanIndexByte2(b, c1, c2)
	}
	//: the word loop.
	return swarIndexByte2(b, c1, c2)
}

// indexLess is IndexLess's body. A bound of zero holds no byte and a bound
// above 128 has no SWAR form; the vector form serves the second, and the
// first never reaches it.
func indexLess(b []byte, n byte) int {
	//: a long slice on an AVX2 CPU takes the vector form, for any bound but zero.
	if hasVector && len(b) >= vectorMin && n != 0 {
		//: the vector form.
		return avx2IndexLess(b, n)
	}
	//: the SWAR form.
	return swarIndexLess(b, n)
}

// indexSet is Set.Index's body.
func indexSet(b []byte, s Set) int {
	//: a long slice on an AVX2 CPU takes the vector form, for any set.
	if hasVector && len(b) >= vectorMin {
		//: the vector form.
		return avx2IndexSet(b, s)
	}
	//: the SWAR form.
	return swarIndexSet(b, s)
}
