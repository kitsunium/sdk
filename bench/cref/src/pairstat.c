// pairstat - ratio of two paired series with a bootstrap 95% interval.
//
// Reads `a b` pairs, one per line, on stdin (a is the baseline, b the
// candidate; both must be positive). Prints the geometric mean of b/a and the
// percentile bootstrap interval of it, over 10000 resamples of the pairs with
// a fixed seed, so the same input always prints the same interval.
//
// With 6 pairs there are few distinct resamples and a percentile interval
// under-covers: read it as "does b/a clearly differ from 1", not as a precise
// bound.
#include <math.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>

constexpr size_t MAX_PAIRS = 1024;
constexpr int RESAMPLES = 10000;

static uint64_t rng_state = 0x9e3779b97f4a7c15ull;

static uint64_t next_u64(void)
{
	// xorshift64*
	rng_state ^= rng_state >> 12;
	rng_state ^= rng_state << 25;
	rng_state ^= rng_state >> 27;
	return rng_state * 0x2545f4914f6cdd1dull;
}

static int cmp_double(const void *x, const void *y)
{
	double a = *(const double *)x, b = *(const double *)y;
	return (a > b) - (a < b);
}

int main(void)
{
	double lr[MAX_PAIRS];
	double sum_a = 0, sum_b = 0;
	size_t n = 0;
	double a, b;

	while (n < MAX_PAIRS && scanf("%lf %lf", &a, &b) == 2) {
		if (!(a > 0) || !(b > 0)) {
			fputs("pairstat: values must be positive\n", stderr);
			return 1;
		}
		lr[n++] = log(b / a);
		sum_a += a;
		sum_b += b;
	}
	if (n < 2) {
		fputs("pairstat: need at least 2 pairs\n", stderr);
		return 1;
	}

	double mean = 0;
	for (size_t i = 0; i < n; i++)
		mean += lr[i];
	mean /= (double)n;

	double *means = malloc(sizeof(double) * RESAMPLES);
	if (means == nullptr)
		return 1;
	for (int r = 0; r < RESAMPLES; r++) {
		double s = 0;
		for (size_t i = 0; i < n; i++)
			s += lr[next_u64() % n];
		means[r] = s / (double)n;
	}
	qsort(means, RESAMPLES, sizeof(double), cmp_double);
	double lo = means[(size_t)(0.025 * RESAMPLES)];
	double hi = means[(size_t)(0.975 * RESAMPLES)];

	printf("n=%zu a_mean=%.5g b_mean=%.5g ratio_b_over_a=%.4f ci95_lo=%.4f ci95_hi=%.4f excludes_1=%s\n", n,
	       sum_a / (double)n, sum_b / (double)n, exp(mean), exp(lo), exp(hi),
	       (exp(lo) > 1 || exp(hi) < 1) ? "yes" : "no");
	free(means);
	return 0;
}
