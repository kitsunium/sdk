package swar

import (
	"bytes"
	"encoding/binary"
	"math/bits"
)

// The lane constants: a uint64 is read as eight byte lanes, lane 0 the lowest
// byte, which is the first of the eight bytes under binary.LittleEndian.
const (
	// ones is 0x01 in every lane: a byte multiplied by it fills the lanes.
	ones uint64 = 0x0101010101010101
	// low7 is the low seven bits of every lane.
	low7 uint64 = 0x7F7F7F7F7F7F7F7F
	// high is the high bit of every lane: where a lane test reports a hit.
	high uint64 = 0x8080808080808080
	// wordSize is the bytes one word covers.
	wordSize int = 8
	// twoScanMin is the length from which IndexByte2 prefers two scans of the
	// standard library's bytes.IndexByte to the word loop, where it has no
	// vector form: measured, the two cost the same near 64 bytes (BENCH.md).
	twoScanMin int = 64
	// exactBound is the largest bound a lane comparison is exact for.
	exactBound byte = 128
	// upperHalf is the first byte of the upper half of the alphabet: the
	// bytes whose high bit is set.
	upperHalf byte = 0x80
)

// zeroLanes returns 0x80 in each lane of v that is zero and 0 in every
// other lane. It is the EXACT form: (v&low7)+low7 cannot carry out of a lane
// (at most 0xFE), so no lane's answer depends on its neighbour's — unlike the
// classic (v-ones)&^v&high, which borrows across a lane and reports a false
// hit above a true one. Every set bit is therefore a real match, and the
// lowest is the first.
func zeroLanes(v uint64) uint64 {
	return ^(((v & low7) + low7) | v | low7)
}

// lessLanes returns 0x80 in each lane of v whose byte is below n and 0 in
// every other lane, where add is lessAdd(n). Exact for n up to 128: a lane
// below 0x80 is below n exactly when (lane + 0x80 - n) keeps its high bit
// clear, that sum is at most 0xFF so it never carries, and a lane with its
// high bit set is at least 0x80, never below n.
func lessLanes(v, add uint64) uint64 {
	return ^(((v & low7) + add) | v) & high
}

// lessAdd is the addend lessLanes reads for the bound n, n at most 128.
func lessAdd(n byte) uint64 {
	return uint64(upperHalf-n) * ones
}

// firstLane returns the index, within the word, of the lowest lane a mask of
// 0x80 flags marks. m is not zero.
func firstLane(m uint64) int {
	return bits.TrailingZeros64(m) >> 3
}

// load reads the eight bytes b[i:i+8], which the caller has checked to lie
// within len(b): the only way this package reads memory, so none of it reads
// past the length, whatever the capacity holds.
func load(b []byte, i int) uint64 {
	return binary.LittleEndian.Uint64(b[i : i+wordSize])
}

// IndexByte2 returns the index of the first byte of b equal to c1 or to c2,
// or -1 when there is none: the search for a line end, '\r' or '\n'. A slice of 64 bytes or more goes to two scans of bytes.IndexByte
// where the build has no AVX2 form.
func IndexByte2(b []byte, c1, c2 byte) int {
	return indexByte2(b, c1, c2)
}

// swarIndexByte2 is IndexByte2's SWAR form.
func swarIndexByte2(b []byte, c1, c2 byte) int {
	pat1, pat2 := uint64(c1)*ones, uint64(c2)*ones
	i := 0
	//: a word at a time while a whole word lies within the slice.
	for ; i+wordSize <= len(b); i += wordSize {
		v := load(b, i)
		//: the first lane of the first word that holds c1 or c2.
		if m := zeroLanes(v^pat1) | zeroLanes(v^pat2); m != 0 {
			//: the match, found.
			return i + firstLane(m)
		}
	}
	//: the last seven bytes, one at a time: nothing past len is read.
	for ; i < len(b); i++ {
		//: the match, found in the tail.
		if b[i] == c1 || b[i] == c2 {
			//: the match, found.
			return i
		}
	}
	//: no byte of b is c1 or c2.
	return -1
}

// twoScanIndexByte2 is IndexByte2 as two assembly scans: the standard
// library's search for c1 over all of b, then for c2 over the part of b before
// the first c1. Past twoScanMin bytes it beats the word loop, which reads
// eight bytes where the assembly reads thirty-two.
func twoScanIndexByte2(b []byte, c1, c2 byte) int {
	i := bytes.IndexByte(b, c1)
	//: no c1: the first c2 is the answer.
	if i < 0 {
		//: the second scan, over all of b.
		return bytes.IndexByte(b, c2)
	}
	//: the first c2 before the first c1, if there is one.
	if j := bytes.IndexByte(b[:i], c2); j >= 0 {
		//: c2 comes first.
		return j
	}
	//: c1 comes first.
	return i
}

// swarIndexLess is IndexLess's SWAR form. A bound above 128 has no exact lane
// test, and takes the byte loop.
func swarIndexLess(b []byte, n byte) int {
	//: a bound past 128 has no lane test.
	if n > exactBound {
		//: one byte at a time.
		return scalarIndexLess(b, n)
	}
	add := lessAdd(n)
	i := 0
	//: a word at a time while a whole word lies within the slice.
	for ; i+wordSize <= len(b); i += wordSize {
		//: the first lane of the first word that holds a byte below n.
		if m := lessLanes(load(b, i), add); m != 0 {
			//: the match, found.
			return i + firstLane(m)
		}
	}
	//: the last seven bytes, one at a time: nothing past len is read.
	for ; i < len(b); i++ {
		//: the match, found in the tail.
		if b[i] < n {
			//: the match, found.
			return i
		}
	}
	//: no byte of b is below n.
	return -1
}

// scalarIndexLess is IndexLess for a bound above 128, which no lane test is
// exact for: one byte at a time.
func scalarIndexLess(b []byte, n byte) int {
	//: every byte, in order.
	for i, c := range b {
		//: the match, found.
		if c < n {
			//: the match, found.
			return i
		}
	}
	//: no byte of b is below n.
	return -1
}

// Set is a set of byte values: the two bytes it was built with, the bytes
// below its bound, and optionally the bytes from 0x80 up. [NewSet] computes
// what a search reads once, and [Set.Index] only reads it, so a Set is safe
// for any number of goroutines. The zero Set holds the byte 0x00 alone.
type Set struct {
	// a and b are the two bytes, each broadcast to every lane.
	a, b uint64
	// hi is high when the set holds the bytes from 0x80 up, 0 when not.
	hi uint64
	// first and second are the two bytes themselves.
	first, second byte
	// below is the bound: a byte under it is in the set.
	below byte
	// upper is whether the bytes from 0x80 up are in the set.
	upper bool
	// wide is whether below exceeds 128, which no lane test is exact for:
	// the scan then takes one byte at a time.
	wide bool
}

// NewSet returns the set of the bytes first and second, the bytes below
// below, and — when upper is true — the bytes from 0x80 up. Pass the same
// byte twice for a set of one, and a below of zero for none below.
//
// A below above 128 is accepted; [Set.Index] then reads byte by byte.
func NewSet(first, second, below byte, upper bool) Set {
	s := Set{
		a:      uint64(first) * ones,
		b:      uint64(second) * ones,
		first:  first,
		second: second,
		below:  below,
		upper:  upper,
		wide:   below > exactBound,
	}
	//: the bytes from 0x80 up are the lanes' own high bits.
	if upper {
		s.hi = high
	}
	//: the set, ready to scan with.
	return s
}

// Index returns the index of the first byte of b that is in the set, or -1
// when there is none. It reads nothing past len(b).
func (s Set) Index(b []byte) int {
	return indexSet(b, s)
}

// has reports whether the byte c is in the set.
func (s Set) has(c byte) bool {
	return c == s.first || c == s.second || c < s.below || (s.upper && c >= upperHalf)
}

// swarIndexSet is Set.Index's SWAR form.
func swarIndexSet(b []byte, s Set) int {
	add := lessAdd(s.below)
	i := 0
	//: a word at a time while a whole word lies within the slice; a bound past 128 skips the loop.
	for ; !s.wide && i+wordSize <= len(b); i += wordSize {
		v := load(b, i)
		m := zeroLanes(v^s.a) | zeroLanes(v^s.b) | lessLanes(v, add) | v&s.hi
		//: the first lane of the first word that holds a byte of the set.
		if m != 0 {
			//: the match, found.
			return i + firstLane(m)
		}
	}
	//: the tail, or the whole slice for a bound past 128, one byte at a time.
	for ; i < len(b); i++ {
		//: the match, found.
		if s.has(b[i]) {
			//: the match, found.
			return i
		}
	}
	//: no byte of b is in the set.
	return -1
}
