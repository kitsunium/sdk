// Package codec — the decompression-bomb guard's arithmetic, at the width the
// build actually gives it.
//
// The allocation half of the #215 regression lives in
// compressed_bomb_alloc_internal_test.go, which carries //go:build !race
// because a byte count measured under the race detector is the detector's
// number, not the guard's. This half has no such constraint: it is a pure
// function of two ints, it costs nothing, and the defect it pins is one the
// race lane can see as well as the alloc lane.
package codec

import "testing"

// Widths the ratio guard has to survive on a 32-bit build. maxExpansionRatio is
// 1000, so inLen*maxExpansionRatio leaves the int32 range once inLen passes
// math.MaxInt32/1000 — a little over two megabytes of COMPRESSED payload, which
// is an ordinary size rather than an adversarial one.
const (
	// bombOverflowInLen is a compressed size just past the 32-bit threshold.
	bombOverflowInLen int = 2_200_000
	// bombOverflowOutLen expands it by barely more than twice — three orders of
	// magnitude under the 1000x bound the guard actually enforces, so no
	// correct implementation may call this a bomb.
	bombOverflowOutLen int = 5_000_000
)

// TestIsBombSurvivesA32BitInt pins the ratio guard against integer overflow.
//
// `outLen > inLen*maxExpansionRatio` is evaluated in `int`. On a 64-bit build
// that is 64 bits wide and the product never overflows at these sizes; on a
// 32-bit build it is 32 bits, and 2 200 000 * 1000 wraps NEGATIVE. Every
// output is greater than a negative number, so the guard reports a bomb for a
// payload that expanded 2.3x — a false REJECTION of ordinary traffic, not a
// missed attack.
//
// This repository builds and tests linux/386, so the 32-bit path is shipped,
// not hypothetical. The assertion is written on isBomb directly because it is a
// pure function of two ints: reproducing it needs no 2 MB payload, only the two
// numbers. Run it with GOARCH=386 to see the failure this guards against.
func TestIsBombSurvivesA32BitInt(t *testing.T) {
	//: report the width so a run on either architecture says which it exercised.
	t.Logf("int is %d bits on this build", 32<<(^uint(0)>>63))
	//: the product is computed through a VARIABLE on purpose. As a constant
	//: expression the compiler rejects the overflow outright — which is how
	//: this very line first failed to build on GOARCH=386. isBomb overflows at
	//: RUNTIME instead, on values the compiler never sees, which is exactly why
	//: nothing caught it.
	inLen := bombOverflowInLen
	//: a 2.3x expansion is not a bomb by any reading of the ratio bound.
	if isBomb(inLen, bombOverflowOutLen) {
		//: name the arithmetic, since the numbers alone look innocuous.
		t.Fatalf("isBomb(%d, %d) reported a bomb for a %.1fx expansion: "+
			"inLen*maxExpansionRatio = %d overflowed int and went negative",
			inLen, bombOverflowOutLen,
			float64(bombOverflowOutLen)/float64(inLen),
			inLen*maxExpansionRatio)
	}
}
