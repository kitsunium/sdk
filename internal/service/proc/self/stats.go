package self

import (
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"time"
)

// The runtime/metrics a snapshot reads. Each is documented in
// runtime/metrics; a name the running toolchain does not know reads as
// KindBad and leaves its field zero rather than failing the snapshot.
const (
	metricGoroutines   string = "/sched/goroutines:goroutines"
	metricHeapBytes    string = "/memory/classes/heap/objects:bytes"
	metricHeapGoal     string = "/gc/heap/goal:bytes"
	metricMemoryTotal  string = "/memory/classes/total:bytes"
	metricHeapObjects  string = "/gc/heap/objects:objects"
	metricTotalAlloc   string = "/gc/heap/allocs:bytes"
	metricGCCycles     string = "/gc/cycles/total:gc-cycles"
	metricGCPauses     string = "/sched/pauses/total/gc:seconds"
	metricSchedLatency string = "/sched/latencies:seconds"
	metricCPUTotal     string = "/cpu/classes/total:cpu-seconds"
	metricCPUIdle      string = "/cpu/classes/idle:cpu-seconds"
)

// maxDuration is the longest time.Duration, where a conversion saturates.
const maxDuration time.Duration = math.MaxInt64

// started is when this package was initialised: the process's start, to
// within the initialisation of the packages linked before it. A program that
// imports this package at all imports it at start-up, so the gap is the
// runtime's own bring-up — microseconds, not the lifetime of anything.
var started = time.Now()

// Count returns how many observations the distribution holds.
func (d DistributionValue) Count() uint64 {
	var total uint64
	//: every bucket's count.
	for _, count := range d.Counts {
		total += count
	}
	//: the sum.
	return total
}

// Quantile returns the q-th quantile of the distribution, q in [0, 1]: the
// UPPER bound of the bucket where the cumulative count reaches q of the total
// — an upper estimate, which is the honest reading of a bucketed histogram.
// q outside [0, 1] is clamped, NaN is read as 0, and an empty distribution is
// zero. A bucket whose upper bound is infinite answers with its lower bound.
func (d DistributionValue) Quantile(q float64) time.Duration {
	total := d.Count()
	//: no observation, no quantile.
	if total == 0 || len(d.Buckets) != len(d.Counts)+1 {
		//: zero.
		return 0
	}
	//: NaN compares false against everything, so it is resolved first.
	if math.IsNaN(q) {
		q = 0
	}
	//: the first observation at least, so q = 0 lands on the first bucket
	//: that holds one rather than on the first bucket.
	target := max(uint64(math.Ceil(min(max(q, 0), 1)*float64(total))), 1)
	var seen uint64
	//: walk the cumulative count to the bucket that reaches the target.
	for index, count := range d.Counts {
		seen += count
		//: not there yet.
		if seen < target {
			continue
		}
		//: the bucket's reading.
		return seconds(bucketBound(d.Buckets[index], d.Buckets[index+1]))
	}
	//: unreachable while the counts sum to total.
	return 0
}

// bucketBound returns the finite bound that stands for a bucket: its upper
// bound, or its lower one when the upper is infinite, or zero when neither is
// finite.
func bucketBound(lower, upper float64) float64 {
	switch {
	//: the ordinary bucket.
	case !math.IsInf(upper, 0):
		//: an upper estimate.
		return upper
	//: the last bucket, open above.
	case !math.IsInf(lower, 0):
		//: the most that can be said.
		return lower
	//: a bucket spanning everything says nothing.
	default:
		//: zero.
		return 0
	}
}

// seconds converts a finite, non-negative number of seconds to a duration,
// saturating rather than overflowing.
func seconds(value float64) time.Duration {
	nanos := value * float64(time.Second)
	//: a negative or unrepresentable bound is not a duration.
	if nanos <= 0 || math.IsNaN(nanos) {
		//: zero.
		return 0
	}
	//: past 2^63 ns the conversion would be implementation-defined.
	if nanos >= math.MaxInt64 {
		//: saturated.
		return maxDuration
	}
	//: in range.
	return time.Duration(nanos)
}

// ReadStats takes a snapshot of the running process. It reads the runtime's
// metrics once, the last collection's time once and the kernel's CPU
// accounting once, and never fails: a figure the platform cannot give is
// zero, and CPUEstimated says when CPUTime is an estimate.
func ReadStats() StatsValue {
	samples := []metrics.Sample{
		{Name: metricGoroutines},
		{Name: metricHeapBytes},
		{Name: metricHeapGoal},
		{Name: metricMemoryTotal},
		{Name: metricHeapObjects},
		{Name: metricTotalAlloc},
		{Name: metricGCCycles},
		{Name: metricGCPauses},
		{Name: metricSchedLatency},
	}
	metrics.Read(samples)
	now := time.Now()
	stats := StatsValue{
		At: now, Started: started, Uptime: now.Sub(started),
		PID: os.Getpid(), MaxProcs: runtime.GOMAXPROCS(0), CPUs: runtime.NumCPU(),
	}
	//: each sample fills the field it names; an unknown one fills nothing.
	for index := range samples {
		apply(&stats, &samples[index])
	}
	//: a toolchain without the goroutine metric still has the count.
	if stats.Goroutines == 0 {
		stats.Goroutines = runtime.NumGoroutine()
	}
	stats.LastGC = lastGC()
	stats.CPUTime, stats.CPUEstimated = cpuTime()
	//: the process, now.
	return stats
}

// apply copies one runtime sample into the field it names.
func apply(stats *StatsValue, sample *metrics.Sample) {
	switch sample.Value.Kind() {
	//: a counter or a gauge.
	case metrics.KindUint64:
		applyCount(stats, sample.Name, sample.Value.Uint64())
	//: one of the two distributions.
	case metrics.KindFloat64Histogram:
		applyDistribution(stats, sample.Name, sample.Value.Float64Histogram())
	//: KindBad — a name this toolchain does not export — or a kind this
	//: snapshot does not read.
	default:
	}
}

// applyCount copies a uint64 sample into its field.
func applyCount(stats *StatsValue, name string, value uint64) {
	switch name {
	//: the goroutine count fits an int on every platform Go supports it on.
	case metricGoroutines:
		stats.Goroutines = int(min(value, math.MaxInt32))
	case metricHeapBytes:
		stats.HeapBytes = value
	case metricHeapGoal:
		stats.HeapGoalBytes = value
	case metricMemoryTotal:
		stats.MemoryBytes = value
	case metricHeapObjects:
		stats.HeapObjects = value
	case metricTotalAlloc:
		stats.TotalAllocBytes = value
	case metricGCCycles:
		stats.GCCycles = value
	//: a count this snapshot does not read.
	default:
	}
}

// applyDistribution copies a histogram sample into its field. The runtime
// hands back fresh slices on every Read, so keeping them shares nothing.
func applyDistribution(stats *StatsValue, name string, histogram *metrics.Float64Histogram) {
	//: nothing to keep.
	if histogram == nil {
		return
	}
	distribution := DistributionValue{Counts: histogram.Counts, Buckets: histogram.Buckets}
	switch name {
	case metricGCPauses:
		stats.GCPauses = distribution
	case metricSchedLatency:
		stats.SchedLatencies = distribution
	//: a distribution this snapshot does not read.
	default:
	}
}

// lastGC returns when the most recent collection ended, or the zero time
// before the first one. runtime/metrics has no such figure; ReadGCStats
// copies it under the runtime's lock without stopping the world.
func lastGC() time.Time {
	var stats debug.GCStats
	debug.ReadGCStats(&stats)
	//: before the first collection the runtime reports the epoch-like zero.
	if stats.NumGC == 0 {
		//: none yet.
		return time.Time{}
	}
	//: the end of the last one.
	return stats.LastGC
}

// runtimeCPU is the Go runtime's estimate of the CPU time the process used:
// its total CPU capacity over the process's life minus its idle time. It is
// the fallback where the kernel's count is not at hand. The runtime takes
// these figures at a garbage collection's stop-the-world, so the estimate is
// as old as the last collection — measured: unchanged across 300 ms of
// spinning, and caught up by the next runtime.GC.
func runtimeCPU() time.Duration {
	samples := []metrics.Sample{{Name: metricCPUTotal}, {Name: metricCPUIdle}}
	metrics.Read(samples)
	//: a toolchain without either metric cannot estimate anything.
	if samples[0].Value.Kind() != metrics.KindFloat64 || samples[1].Value.Kind() != metrics.KindFloat64 {
		//: zero.
		return 0
	}
	//: never negative, whatever the two readings' rounding.
	return seconds(samples[0].Value.Float64() - samples[1].Value.Float64())
}
