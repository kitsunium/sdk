package self

import (
	"math"
	"testing"
	"time"
)

// Test_DistributionValue_Quantile pins the reading of a bucketed histogram:
// the upper bound of the bucket where the cumulative count reaches the
// quantile, never a bucket that holds nothing, and a finite answer at the
// open ends.
func Test_DistributionValue_Quantile(t *testing.T) {
	t.Parallel()
	//: 10 observations: 5 in [0, 1ms), 4 in [1ms, 10ms), 1 in [10ms, +Inf).
	spread := DistributionValue{Counts: []uint64{0, 5, 4, 1}, Buckets: []float64{math.Inf(-1), 0, 0.001, 0.010, math.Inf(1)}}
	type tc struct {
		name string
		dist DistributionValue
		q    float64
		want time.Duration
	}
	tests := []tc{
		{"the median", spread, 0.5, time.Millisecond},
		{"the ninetieth percentile", spread, 0.9, 10 * time.Millisecond},
		{"the maximum lands in the open bucket, read at its lower bound", spread, 1, 10 * time.Millisecond},
		{"q = 0 is the first bucket that holds an observation", spread, 0, time.Millisecond},
		{"a negative q clamps to 0", spread, -3, time.Millisecond},
		{"a q above one clamps to one", spread, 7, 10 * time.Millisecond},
		{"NaN is read as 0", spread, math.NaN(), time.Millisecond},
		{"an empty distribution is zero", DistributionValue{}, 0.99, 0},
		{"mismatched shapes are zero", DistributionValue{Counts: []uint64{1}, Buckets: []float64{0}}, 0.5, 0},
		{
			"a bucket open at both ends says nothing",
			DistributionValue{Counts: []uint64{3}, Buckets: []float64{math.Inf(-1), math.Inf(1)}},
			0.5, 0,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := c.dist.Quantile(c.q); got != c.want {
			t.Errorf("Quantile(%v) = %v, want %v", c.q, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_seconds pins that a bound too large for a duration saturates rather
// than wrapping, and that a negative one reads as zero.
func Test_seconds(t *testing.T) {
	t.Parallel()
	if got := seconds(1e12); got != maxDuration {
		t.Errorf("seconds(1e12) = %v, want the longest duration", got)
	}
	if got := seconds(-1); got != 0 {
		t.Errorf("seconds(-1) = %v, want 0", got)
	}
	if got := seconds(0.25); got != 250*time.Millisecond {
		t.Errorf("seconds(0.25) = %v", got)
	}
}
