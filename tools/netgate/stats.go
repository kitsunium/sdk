// Package main — the statistics: benchstat's median and its distribution-free
// confidence interval, from the samples alone.
package main

import (
	"errors"
	"math"
	"slices"
)

var (
	// errTooFewSamples reports a sample set smaller than minSamples.
	errTooFewSamples = errors.New("too few samples")
	// errNoInterval reports a sample set too small for any interval at the
	// level.
	errNoInterval = errors.New("no confidence interval at this level")
)

// summarize is benchstat's summary of a sample set (golang.org/x/perf
// benchmath, AssumeNothing): the median, and a distribution-free interval
// whose ends are order statistics — the k-th smallest and the k-th largest
// sample, k the largest for which the binomial(n, 1/2) tail below k is at most
// (1-level)/2. At n = 10 and 95 % that is the 2nd and the 9th of the sorted
// samples. It refuses fewer than minSamples samples.
func summarize(xs []float64, level float64) (summaryEntity, error) {
	n := len(xs)
	//: the plan's n is a floor, never an advice.
	if n < minSamples {
		//: a smaller set is refused rather than judged on a wider interval.
		return summaryEntity{N: n}, errTooFewSamples
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	k := orderStatistic(n, (1-level)/2)
	//: no order statistic bounds the median at this level.
	if k == 0 {
		//: a level this n cannot reach.
		return summaryEntity{N: n}, errNoInterval
	}
	//: the k-th smallest and the k-th largest.
	return summaryEntity{N: n, Lo: s[k-1], Median: median(s), Hi: s[n-k]}, nil
}

// median is the middle of a sorted set, the mean of the two middles for an
// even count.
func median(sorted []float64) float64 {
	n := len(sorted)
	//: an odd count has one middle.
	if n%2 == 1 {
		//: the middle sample.
		return sorted[n/2]
	}
	//: the two middles' mean.
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// orderStatistic is the largest k, at most n/2, whose binomial(n, 1/2) lower
// tail P(X <= k-1) is at most tail; 0 when even k = 1 exceeds it.
func orderStatistic(n int, tail float64) int {
	k := 0
	cdf := 0.0
	//: walk the tail up while it stays within the budget.
	for j := range n / 2 {
		cdf += binomialHalf(n, j)
		//: P(X <= j) past the budget: k = j is the last that held.
		if cdf > tail {
			//: the previous k.
			return k
		}
		k = j + 1
	}
	//: the tail never exceeded the budget up to the middle.
	return k
}

// binomialHalf is P(X = j) for X ~ binomial(n, 1/2), computed in log space so
// a large n neither underflows nor overflows.
func binomialHalf(n, j int) float64 {
	ln, _ := math.Lgamma(float64(n + 1))
	lj, _ := math.Lgamma(float64(j + 1))
	lnj, _ := math.Lgamma(float64(n - j + 1))
	//: n choose j over 2^n.
	return math.Exp(ln - lj - lnj - float64(n)*math.Ln2)
}
