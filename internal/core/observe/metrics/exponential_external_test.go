package metrics_test

import (
	"math"
	"slices"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
)

// TestExponentialIndexReproducesTheSpecificationValues pins MapToIndex on the
// values the OpenTelemetry specification and its reference SDKs test: the
// exact powers of two closing their bucket (upper bound inclusive), the
// values inside a bucket, and the extremes of the double range at the two
// scale limits.
func TestExponentialIndexReproducesTheSpecificationValues(t *testing.T) {
	t.Parallel()
	smallestSubnormal := math.SmallestNonzeroFloat64
	minNormal := 0x1p-1022
	cases := []struct {
		value float64
		scale int32
		want  int32
	}{
		// Scale 0: base 2, bucket k is (2^k, 2^(k+1)].
		{1, 0, -1},
		{2, 0, 0},
		{3, 0, 1},
		{4, 0, 1},
		{5, 0, 2},
		{0.75, 0, -1},
		{0.5, 0, -2},
		// Scale -1: base 4.
		{1, -1, -1},
		{2, -1, 0},
		{4, -1, 0},
		{5, -1, 1},
		{16, -1, 1},
		{17, -1, 2},
		// Scale 1: base √2.
		{1, 1, -1},
		{1.5, 1, 1},
		{2, 1, 1},
		{3, 1, 3},
		{4, 1, 3},
		// Scale 2: base 2^(1/4).
		{1.5, 2, 2},
		{2, 2, 3},
		// The extremes at the specification's limits.
		{math.MaxFloat64, 20, 1024<<20 - 1},
		{minNormal, 20, -1022<<20 - 1},
		{math.MaxFloat64, -10, 0},
		{1, -10, -1},
		{smallestSubnormal, -10, -2},
		{smallestSubnormal, 0, -1075},
	}
	for _, c := range cases {
		if got := coremetrics.ExponentialIndex(c.value, c.scale); got != c.want {
			t.Errorf("ExponentialIndex(%g, %d) = %d, want %d", c.value, c.scale, got, c.want)
		}
	}
}

// TestExponentialBoundsAgreeWithTheIndexAtEveryScale: for every scale from
// the floor to the ceiling, each bucket's lower bound maps to the bucket
// below and the next double to the bucket itself — exactly at every scale of
// zero or less, and for a power of two at any scale; within the logarithm's
// one-bucket error the specification admits elsewhere — and the indices are
// monotonic.
func TestExponentialBoundsAgreeWithTheIndexAtEveryScale(t *testing.T) {
	t.Parallel()
	for scale := coremetrics.ExponentialMinScale; scale <= coremetrics.ExponentialMaxScale; scale++ {
		var last int32 = math.MinInt32
		for _, index := range sampleIndices(scale) {
			bound := coremetrics.ExponentialLowerBound(index, scale)
			if bound <= 0 || math.IsInf(bound, 0) {
				continue
			}
			below := coremetrics.ExponentialIndex(bound, scale)
			above := coremetrics.ExponentialIndex(math.Nextafter(bound, math.Inf(1)), scale)
			if scale <= 0 && (below != index-1 || above != index) {
				t.Fatalf("scale %d index %d: bound %g maps to %d, the next double to %d", scale, index, bound, below, above)
			}
			//: above zero a power of two is answered from its exponent, exactly.
			if scale > 0 && index%(1<<scale) == 0 && below != index-1 {
				t.Fatalf("scale %d index %d: the power of two %g maps to %d", scale, index, bound, below)
			}
			if below < index-2 || below > index || above < index-1 || above > index+1 {
				t.Fatalf("scale %d index %d: bound %g maps to %d / %d, beyond the logarithm's ±1", scale, index, bound, below, above)
			}
			if below < last {
				t.Fatalf("scale %d: index %d maps below the previous bucket", scale, index)
			}
			last = below
		}
	}
}

// sampleIndices is a spread of bucket indices at scale: around zero, and
// near the largest and smallest finite double.
func sampleIndices(scale int32) []int32 {
	var out []int32
	top := coremetrics.ExponentialIndex(math.MaxFloat64, scale)
	bottom := coremetrics.ExponentialIndex(0x1p-1022, scale)
	for i := int32(-40); i <= 40; i++ {
		out = append(out, i)
	}
	for i := int32(0); i < 8; i++ {
		out = append(out, top-i, bottom+i+1)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
