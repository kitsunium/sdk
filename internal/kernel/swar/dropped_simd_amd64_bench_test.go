//go:build goexperiment.simd && amd64

package swar

import (
	"math/bits"
	"simd/archsimd"
)

// droppedVectorIndexByte is the AVX2 form of the one-byte search that was
// dropped (dropped_bench_test.go), or nil on a CPU without AVX2.
func droppedVectorIndexByte(c byte) func([]byte) int {
	if !hasVector {
		return nil
	}
	return func(b []byte) int {
		pat := archsimd.BroadcastUint8x32(c)
		last := len(b) - vectorWidth
		found := -1
		for off := 0; ; off += vectorWidth {
			off = min(off, last)
			if mask := archsimd.LoadUint8x32(b[off : off+vectorWidth]).Equal(pat).ToBits(); mask != 0 {
				found = off + bits.TrailingZeros32(mask)
				break
			}
			if off == last {
				break
			}
		}
		archsimd.ClearAVXUpperBits()
		return found
	}
}
