//go:build goexperiment.simd && amd64

package swar

import "testing"

// The vector forms are called directly, so that the SWAR form and the
// dispatcher are held to the oracle beside them rather than instead of them.
// On a CPU without AVX2 the forms are not callable and the specs carry none.

// vectorByte2 is IndexByte2's vector form, or nil on a CPU without AVX2.
func vectorByte2(c1, c2 byte) func([]byte) int {
	if !hasVector {
		return nil
	}
	return func(b []byte) int { return avx2IndexByte2(b, c1, c2) }
}

// vectorLess is IndexLess's vector form, or nil where there is none: a CPU
// without AVX2, and a bound of zero, which the dispatcher keeps off it.
func vectorLess(n byte) func([]byte) int {
	if !hasVector || n == 0 {
		return nil
	}
	return func(b []byte) int { return avx2IndexLess(b, n) }
}

// vectorSet is Set.Index's vector form, or nil on a CPU without AVX2.
func vectorSet(s Set) func([]byte) int {
	if !hasVector {
		return nil
	}
	return func(b []byte) int { return avx2IndexSet(b, s) }
}

// TestVectorFormsRunHere says, in the log, whether this run exercised the
// vector forms: a lane that passes without them has proved the SWAR forms
// only, and the log must not let it read otherwise.
func TestVectorFormsRunHere(t *testing.T) {
	t.Logf("AVX2 vector forms exercised: %v", hasVector)
}
