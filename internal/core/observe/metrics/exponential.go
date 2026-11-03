package metrics

import "math"

const (
	// ExponentialMinScale is the lowest scale an exponential histogram falls to:
	// base 2^1024, three buckets cover every finite double. The OpenTelemetry
	// specification's floor.
	ExponentialMinScale int32 = -10
	// ExponentialMaxScale is the scale an exponential histogram starts at — base
	// 2^(2^-20), a relative error under 1e-6 — and the specification's ceiling.
	// The histogram only ever lowers it, when the range it holds needs more
	// buckets than its maximum.
	ExponentialMaxScale int32 = 20
	// DefaultExponentialMaxSize is the bucket count per range — positive and
	// negative each — an exponential histogram created with a maxSize of zero or
	// less holds: the OpenTelemetry SDKs' default, 160, which keeps a 1 µs to
	// 100 s latency range at scale 3, a relative error under 4.5 %.
	DefaultExponentialMaxSize int = 160
)

// The two facts of IEEE 754 binary64 the mapping rests on.
const (
	// frexpHalf is the fraction math.Frexp returns for an exact power of two.
	frexpHalf float64 = 0.5
	// maxNormalExponent is one past the largest double's base-2 exponent:
	// math.MaxFloat64 < 2^1024.
	maxNormalExponent int32 = 1024
)

// ExponentialIndex is the bucket index of magnitude at scale — the
// OpenTelemetry specification's MapToIndex: the k such that magnitude lies in
// (base^k, base^(k+1)], base = 2^(2^-scale), upper bound inclusive. At a scale
// of zero or less it reads the exponent math.Frexp returns, exact for every
// double, subnormals included; above zero it multiplies the natural logarithm
// by the scale factor, an exact power of two answered from the exponent. The
// caller passes a positive, finite magnitude and a scale from
// ExponentialMinScale to ExponentialMaxScale.
func ExponentialIndex(magnitude float64, scale int32) int32 {
	frac, exp := math.Frexp(magnitude)
	//: magnitude = frac·2^exp, frac in [0.5, 1): an exact power of two is
	//: 2^(exp-1), the INCLUSIVE upper bound of the bucket below it.
	if frac == frexpHalf {
		//: the bucket that power of two closes: 2^(exp-1) is the upper bound of
		//: index ((exp-1)·2^scale)-1 above zero, of index (exp-2)>>-scale at or
		//: below it.
		return powerOfTwoIndex(int32(exp-1), scale)
	}
	//: at or below zero the bucket is the base-2 exponent, coarsened.
	if scale <= 0 {
		//: an arithmetic shift floors, as the specification's does.
		return int32(exp-1) >> -scale
	}
	//: above zero, the logarithm: never an exact boundary, those were above.
	index := int32(math.Floor(math.Log(magnitude) * math.Ldexp(math.Log2E, int(scale))))
	//: the logarithm rounds math.MaxFloat64 onto 2^1024, a bound no double
	//: reaches: the last finite bucket is the specification's ceiling.
	return min(index, maxNormalExponent<<scale-1)
}

// ExponentialLowerBound is the exclusive lower bound of bucket index at scale,
// base^index — the specification's LowerBoundary, computed as 2^(index·2^-scale)
// split into an exponent and a fraction so that it neither overflows nor
// underflows inside the range a finite double can reach.
func ExponentialLowerBound(index, scale int32) float64 {
	//: at or below zero every bound is a power of two.
	if scale <= 0 {
		//: 2^(index·2^-scale), exact.
		return math.Ldexp(1, int(index)<<-scale)
	}
	//: index = whole·2^scale + part: 2^whole times 2^(part·2^-scale).
	whole := int(index) >> scale
	part := int(index) - whole<<scale
	//: the fraction is in [1, 2): no overflow, no underflow on the way.
	return math.Ldexp(math.Exp(float64(part)*math.Ldexp(math.Ln2, -int(scale))), whole)
}

// powerOfTwoIndex is the bucket index of the exact power of two 2^e at scale:
// the bucket whose INCLUSIVE upper bound it is.
func powerOfTwoIndex(e, scale int32) int32 {
	//: above zero every power of two is a bucket boundary.
	if scale > 0 {
		//: the index whose upper bound 2^e is.
		return e<<scale - 1
	}
	//: at or below zero, the specification's exponent method, corrected.
	return (e - 1) >> -scale
}
