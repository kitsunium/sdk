package swar

import (
	"bytes"
	"strconv"
	"testing"
)

// The benchmarks hold each search to what a caller would write without this
// package: bytes.IndexByte where the standard library has the search, the
// byte loop where it has not. A function stays in the package only while it
// wins (BENCH.md has the figures and says where). Every search runs over the
// lengths a header line, a JSON string and a whole buffer have, with the
// match at the end: the scan that reads the most.

// benchLengths are the slice lengths: under a word, a word, a vector, a few
// vectors, and a buffer.
var benchLengths = []int{4, 8, 16, 32, 64, 256, 1024, 4096}

// benchSink keeps each search's result alive.
var benchSink int

// impl is one way to run a search.
type impl struct {
	name string
	fn   func(b []byte) int
}

// benchmark runs every impl whose function is non-nil over every length,
// one sub-benchmark per length and implementation, so a benchstat column
// split on /impl compares them over the same input. data builds the input of
// a given length: n-1 bytes no search accepts, then the byte it finds.
func benchmark(b *testing.B, last byte, impls []impl) {
	b.Helper()
	for _, n := range benchLengths {
		in := append(bytes.Repeat([]byte{'a'}, n-1), last)
		for _, im := range impls {
			//: a vector form needs a whole register.
			if im.fn == nil || (im.name == "vector" && n < vectorMin) {
				continue
			}
			b.Run("len="+strconv.Itoa(n)+"/impl="+im.name, func(b *testing.B) {
				b.SetBytes(int64(n))
				for range b.N {
					benchSink = im.fn(in)
				}
			})
		}
	}
}

func BenchmarkIndexByte2(b *testing.B) {
	benchmark(b, '\n', []impl{
		{"bytes.IndexAny", func(b []byte) int { return bytes.IndexAny(b, "\r\n") }},
		{"two-scan", func(b []byte) int { return twoScanIndexByte2(b, '\r', '\n') }},
		{"loop", func(b []byte) int { return loopIndex(b, func(c byte) bool { return c == '\r' || c == '\n' }) }},
		{"public", func(b []byte) int { return IndexByte2(b, '\r', '\n') }},
		{"swar", func(b []byte) int { return swarIndexByte2(b, '\r', '\n') }},
		{"vector", vectorByte2('\r', '\n')},
	})
}

func BenchmarkIndexLess(b *testing.B) {
	benchmark(b, 0x0A, []impl{
		{"loop", func(b []byte) int { return loopIndex(b, func(c byte) bool { return c < 0x20 }) }},
		{"public", func(b []byte) int { return IndexLess(b, 0x20) }},
		{"swar", func(b []byte) int { return swarIndexLess(b, 0x20) }},
		{"vector", vectorLess(0x20)},
	})
}

// escapeTable is the table a JSON encoder indexes per byte (the shape of
// encoding/json's safeSet, inverted): true for a byte that needs escaping.
var escapeTable = func() (t [256]bool) {
	for c := range 256 {
		t[c] = c < 0x20 || c == '"' || c == '\\' || c >= 0x80
	}
	return t
}()

func BenchmarkSetIndex(b *testing.B) {
	set := NewSet('"', '\\', 0x20, true)
	benchmark(b, '"', []impl{
		{"table-loop", func(b []byte) int {
			for i, c := range b {
				if escapeTable[c] {
					return i
				}
			}
			return -1
		}},
		{"predicate-loop", func(b []byte) int {
			return loopIndex(b, func(c byte) bool { return c == '"' || c == '\\' || c < 0x20 || c >= 0x80 })
		}},
		{"public", set.Index},
		{"swar", func(b []byte) int { return swarIndexSet(b, set) }},
		{"vector", vectorSet(set)},
	})
}

// loopIndex is the byte loop with its predicate in a function value: what
// bytes.IndexFunc does, without the rune decoding.
func loopIndex(b []byte, match func(byte) bool) int {
	for i, c := range b {
		if match(c) {
			return i
		}
	}
	return -1
}
