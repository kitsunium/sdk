// cref.h - timing and CPU accounting shared by every bench program here.
//
// Each program prints one `key=value` line on stdout, which run.sh parses.
// CPU is read with getrusage(RUSAGE_SELF): user and system time are kept
// apart because a syscall-heavy path (echo) and a pure-compute path (HMAC,
// parse) move in different columns. RUSAGE_SELF covers every thread of the
// process, io-wq workers included. It does not see softirq work that the
// kernel charges to whichever task happened to be running; run.sh cross-checks
// that against /proc/stat.
#ifndef CREF_H
#define CREF_H

#include <stdint.h>
#include <stdio.h>
#include <sys/resource.h>
#include <time.h>

struct cref_cpu {
	double utime_us;
	double stime_us;
};

static inline uint64_t cref_now_ns(void)
{
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (uint64_t)ts.tv_sec * 1000000000u + (uint64_t)ts.tv_nsec;
}

static inline struct cref_cpu cref_cpu_now(void)
{
	struct rusage ru;
	getrusage(RUSAGE_SELF, &ru);
	return (struct cref_cpu){
		.utime_us = (double)ru.ru_utime.tv_sec * 1e6 + (double)ru.ru_utime.tv_usec,
		.stime_us = (double)ru.ru_stime.tv_sec * 1e6 + (double)ru.ru_stime.tv_usec,
	};
}

// cref_report prints the common result line of a per-operation bench.
static inline void cref_report(const char *name, uint64_t ops, uint64_t elapsed_ns,
			       struct cref_cpu c0, struct cref_cpu c1)
{
	double n = (double)ops;
	double u = (c1.utime_us - c0.utime_us) / n;
	double s = (c1.stime_us - c0.stime_us) / n;
	printf("bench=%s ops=%llu ns_per_op=%.3f utime_us_per_op=%.5f stime_us_per_op=%.5f "
	       "cpu_us_per_op=%.5f\n",
	       name, (unsigned long long)ops, (double)elapsed_ns / n, u, s, u + s);
}

#endif
