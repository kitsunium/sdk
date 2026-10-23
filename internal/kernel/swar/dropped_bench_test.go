package swar

import (
	"bytes"
	"testing"
)

// The one-byte search is not part of the package: bytes.IndexByte, an
// assembly loop over thirty-two bytes at a time, beats the word loop at every
// length that matters and ties it below sixteen bytes (BENCH.md has the
// figures). The benchmark stays, with its own copy of the loops it measured, so
// that the decision can be taken again on another CPU or another Go release
// instead of being taken on trust: go test -bench DroppedIndexByte.

// droppedSWARIndexByte is the SWAR form of the one-byte search that was dropped.
func droppedSWARIndexByte(b []byte, c byte) int {
	pat := uint64(c) * ones
	i := 0
	for ; i+wordSize <= len(b); i += wordSize {
		if m := zeroLanes(load(b, i) ^ pat); m != 0 {
			return i + firstLane(m)
		}
	}
	for ; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func BenchmarkDroppedIndexByte(b *testing.B) {
	benchmark(b, '\n', []impl{
		{"bytes", func(b []byte) int { return bytes.IndexByte(b, '\n') }},
		{"swar", func(b []byte) int { return droppedSWARIndexByte(b, '\n') }},
		{"vector", droppedVectorIndexByte('\n')},
	})
}
