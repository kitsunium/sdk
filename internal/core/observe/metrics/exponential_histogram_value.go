package metrics

import coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"

// ExponentialBucketsValue is one range — positive or negative — of an
// exponential histogram point: Counts[i] counts the values whose bucket index
// is Offset+i, a bucket at index k holding the magnitudes in
// (base^k, base^(k+1)], base = 2^(2^-scale). Empty Counts is an empty range.
type ExponentialBucketsValue struct {
	// Offset is the bucket index of Counts[0].
	Offset int32
	// Counts holds one count per bucket, PER BUCKET (not cumulative).
	Counts []uint64
}

// ExponentialHistogramValue is one series of an exponential (base-2)
// histogram metric: OTLP's ExponentialHistogramDataPoint. Its buckets are not
// chosen by hand — the scale sets a relative error bound at every magnitude,
// and the histogram lowers the scale on its own when the range it holds would
// need more than its maximum number of buckets (ADR 0044, resolved).
type ExponentialHistogramValue struct {
	// Attrs is the series' attribute set, sorted by Key; nil for the
	// dimensionless series. See SumValue.Attrs for the aliasing rule.
	Attrs []coreotel.AttrValue
	// Scale sets the resolution: base = 2^(2^-Scale), so each bucket's upper
	// bound is base times its lower. ExponentialMinScale to
	// ExponentialMaxScale.
	Scale int32
	// ZeroCount counts the observations whose magnitude is at most
	// ZeroThreshold — zero itself, since this SDK's threshold is 0.
	ZeroCount uint64
	// ZeroThreshold is the magnitude ZeroCount covers; 0 in this SDK.
	ZeroThreshold float64
	// Positive holds the buckets of the positive observations.
	Positive ExponentialBucketsValue
	// Negative holds the buckets of the negative observations, by magnitude.
	Negative ExponentialBucketsValue
	// Sum is the total of every observed value in the window.
	Sum float64
	// Count is how many observations the window holds, ZeroCount included.
	Count uint64
	// Exemplars holds at most one exemplar: the last measurement of the
	// window recorded with RecordContext under a sampled span. Its buckets
	// move when the scale drops, so one per bucket would point at a bucket
	// that no longer exists.
	Exemplars []ExemplarValue
}
