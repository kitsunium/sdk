package metrics

import (
	"math"
	"runtime/metrics"
	"slices"
	"strings"
	"sync"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
)

// The runtime/metrics keys the instruments read, one Read for all of them.
const (
	rtTotalBytes    string = "/memory/classes/total:bytes"
	rtReleasedBytes string = "/memory/classes/heap/released:bytes"
	rtMemLimit      string = "/gc/gomemlimit:bytes"
	rtAllocBytes    string = "/gc/heap/allocs:bytes"
	rtAllocObjects  string = "/gc/heap/allocs:objects"
	rtHeapGoal      string = "/gc/heap/goal:bytes"
	rtGoroutines    string = "/sched/goroutines:goroutines"
	rtGOMAXPROCS    string = "/sched/gomaxprocs:threads"
	rtGOGC          string = "/gc/gogc:percent"
	rtSchedLatency  string = "/sched/latencies:seconds"
	rtGCPauses      string = "/sched/pauses/total/gc:seconds"
)

// runtimeSeconds is the UCUM unit of the two runtime histograms.
const runtimeSeconds string = "s"

// The keys one Read samples, and the instruments derived from them.
var (
	// runtimeKeys is the Read's sample order; runtimeSamples.index maps it.
	runtimeKeys = []string{
		rtTotalBytes, rtReleasedBytes, rtMemLimit, rtAllocBytes, rtAllocObjects,
		rtHeapGoal, rtGoroutines, rtGOMAXPROCS, rtGOGC, rtSchedLatency, rtGCPauses,
	}
	// runtimeHistograms is the pair RuntimeInstruments registers on a meter
	// that is also an AsyncHistogramMeter (#868): the runtime's own buckets
	// carried over as the explicit bounds, with no sum — runtime/metrics
	// keeps none for a histogram.
	runtimeHistograms = []runtimeHistogram{
		{name: "go.schedule.duration", key: rtSchedLatency, description: "The time goroutines have spent in the scheduler in a runnable state before actually running."},
		{name: "go.gc.duration", key: rtGCPauses, description: "The time the world has been stopped for garbage collection, per pause."},
	}
	// runtimeInstruments is the set RuntimeInstruments registers.
	runtimeInstruments = []runtimeInstrument{
		{"go.memory.used", false, "By", "Memory used by the Go runtime.", func(s *runtimeSamples) (int64, bool) {
			return s.uint(rtTotalBytes) - s.uint(rtReleasedBytes), true
		}},
		{"go.memory.limit", false, "By", "Go runtime memory limit configured by the user, if a limit exists.", func(s *runtimeSamples) (int64, bool) {
			limit := s.uint(rtMemLimit)
			//: math.MaxInt64 is "no limit": there is nothing to report.
			return limit, limit != math.MaxInt64
		}},
		{"go.memory.allocated", true, "By", "Memory allocated to the heap by the application.", func(s *runtimeSamples) (int64, bool) {
			return s.uint(rtAllocBytes), true
		}},
		{"go.memory.allocations", true, "{allocation}", "Count of allocations to the heap by the application.", func(s *runtimeSamples) (int64, bool) {
			return s.uint(rtAllocObjects), true
		}},
		{"go.memory.gc.goal", false, "By", "Heap size target for the end of the GC cycle.", func(s *runtimeSamples) (int64, bool) {
			return s.uint(rtHeapGoal), true
		}},
		{"go.goroutine.count", false, "{goroutine}", "Count of live goroutines.", func(s *runtimeSamples) (int64, bool) {
			return s.uint(rtGoroutines), true
		}},
		{"go.processor.limit", false, "{thread}", "The number of OS threads that can execute user-level Go code simultaneously.", func(s *runtimeSamples) (int64, bool) {
			return s.uint(rtGOMAXPROCS), true
		}},
		{"go.config.gogc", false, "%", "Heap size target percentage configured by the user, otherwise 100.", func(s *runtimeSamples) (int64, bool) {
			return s.uint(rtGOGC), true
		}},
	}
)

// RuntimeConfig tunes RuntimeInstruments. The zero value registers the
// OpenTelemetry names.
type RuntimeConfig struct {
	// Underscore spells every name with '_' instead of '.' (go_memory_used),
	// for a meter a Prometheus scrape reads.
	Underscore bool
}

// runtimeInstrument is one instrument of the OpenTelemetry Go runtime
// conventions: its name, kind, unit and description, and how its value is
// derived from the Read.
type runtimeInstrument struct {
	// name is the OpenTelemetry name, dotted.
	name string
	// counter is true for a monotonic sum, false for an up-down one.
	counter bool
	// unit is the UCUM unit.
	unit string
	// description is the convention's.
	description string
	// value derives the instrument's value from the samples, and reports
	// false when there is nothing to observe (a limit that is not set).
	value func(s *runtimeSamples) (int64, bool)
}

// runtimeHistogram is one histogram of the runtime: its name, the
// runtime/metrics key it reads and its description; the unit is seconds.
// bounds and counts are the conversion's buffers, allocated on the first
// read and reused while the runtime's layout stays the same.
type runtimeHistogram struct {
	// name is the OpenTelemetry name, dotted.
	name string
	// key is the runtime/metrics histogram read.
	key string
	// description is the convention's.
	description string
	// bounds and counts hold the converted reading, reused across reads.
	bounds []float64
	counts []uint64
}

// runtimeSamples reads every key in one runtime/metrics.Read per collection:
// the first callback of a collection reads, the others use what it read. The
// meter runs each callback once per Collect, serialised, so counting the
// callbacks served marks where a collection begins.
type runtimeSamples struct {
	// mu guards samples and served against a meter collected concurrently by
	// two goroutines, which the meter serialises anyway.
	mu sync.Mutex
	// samples is the Read's buffer, allocated once.
	samples []metrics.Sample
	// index maps a key to its slot in samples.
	index map[string]int
	// served counts the callbacks of the current collection.
	served int
	// callbacks is how many callbacks one collection runs.
	callbacks int
}

// uint is the sample of key as an int64, 0 for a key this runtime lacks.
func (s *runtimeSamples) uint(key string) int64 {
	v := s.samples[s.index[key]].Value
	//: a key the runtime does not know reads KindBad: nothing to report.
	if v.Kind() != metrics.KindUint64 {
		return 0
	}
	return int64(min(v.Uint64(), math.MaxInt64))
}

// observe runs one callback's read: a fresh Read on the first callback of a
// collection, then the instrument's value.
func (s *runtimeSamples) observe(inst *runtimeInstrument) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serve()
	return inst.value(s)
}

// serve counts one callback of the collection in progress, reading every
// key on its first. The caller holds s.mu.
func (s *runtimeSamples) serve() {
	//: the first callback of this collection reads for all of them.
	if s.served == 0 {
		metrics.Read(s.samples)
	}
	s.served = (s.served + 1) % s.callbacks
}

// observeHistogram runs one histogram callback's read and converts the
// runtime's histogram into an explicit-bucket reading: the runtime's inner
// boundaries are the bounds, its -Inf and +Inf edges the implicit first and
// overflow buckets — a missing edge an empty bucket of its own. NoSum: the
// runtime keeps no total. It reports false for a key this runtime lacks.
func (s *runtimeSamples) observeHistogram(hist *runtimeHistogram) (point coremetrics.HistogramValue, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.serve()
	v := s.samples[s.index[hist.key]].Value
	//: a key the runtime does not know reads KindBad: nothing to report.
	if v.Kind() != metrics.KindFloat64Histogram {
		return point, false
	}
	raw := v.Float64Histogram()
	//: a histogram with no bucket has nothing to carry.
	if len(raw.Counts) == 0 || len(raw.Buckets) != len(raw.Counts)+1 {
		return point, false
	}
	hist.convert(raw.Buckets, raw.Counts)
	//: the converted reading; the meter copies what it keeps.
	return coremetrics.HistogramValue{Bounds: hist.bounds, Counts: hist.counts, NoSum: true}, true
}

// convert writes the runtime's buckets — edges[i] to edges[i+1] holding
// counts[i] — into h.bounds and h.counts, allocating only when the layout's
// size changed.
func (h *runtimeHistogram) convert(edges []float64, counts []uint64) {
	low, high := 0, len(edges)
	//: a -Inf first edge is the implicit first bucket's.
	if math.IsInf(edges[0], -1) {
		low = 1
	}
	//: a +Inf last edge is the implicit overflow's.
	if math.IsInf(edges[len(edges)-1], 1) {
		high = len(edges) - 1
	}
	h.bounds = resize(h.bounds, high-low)
	copy(h.bounds, edges[low:high])
	h.counts = resize(h.counts, len(h.bounds)+1)
	clear(h.counts)
	//: a finite first edge leaves the first bucket, below it, empty.
	copy(h.counts[1-low:], counts)
}

// resize returns buf of length n, reusing its array when it is that long.
func resize[T any](buf []T, n int) []T {
	//: the layout did not change: no allocation.
	if len(buf) == n {
		return buf
	}
	//: a new layout.
	return make([]T, n)
}

// RuntimeInstruments registers the Go runtime's instruments on meter as
// observable instruments with the OpenTelemetry Go runtime semantic
// conventions' names, kinds and units (#868): go.memory.used,
// go.memory.limit (only while a limit is set), go.memory.allocated,
// go.memory.allocations, go.memory.gc.goal, go.goroutine.count,
// go.processor.limit and go.config.gogc; and, when meter is also an
// AsyncHistogramMeter, the two histograms go.schedule.duration
// (/sched/latencies:seconds) and go.gc.duration (/sched/pauses/total/gc:seconds),
// carried over without re-bucketing — the runtime's buckets are the explicit
// bounds — and marked NoSum, since the runtime keeps no total. Every value is
// read at Collect from ONE runtime/metrics.Read into samples allocated here,
// and the histograms' bounds and counts are converted into slices allocated
// once, so a collection allocates nothing for the readings after the first.
// The units and descriptions
// are bound when meter is a UnitDescriber and a Describer. Opt-in, with no
// attributes: the Resource carries the service. cfg.Underscore spells the
// names with '_' for a meter a Prometheus scrape reads, whose grammar has
// no '.'. Registering twice on one meter panics as any duplicate
// registration does.
func RuntimeInstruments(meter coremetrics.AsyncMeter, cfg RuntimeConfig) {
	samples := &runtimeSamples{
		samples:   make([]metrics.Sample, len(runtimeKeys)),
		index:     make(map[string]int, len(runtimeKeys)),
		callbacks: len(runtimeInstruments),
	}
	histograms, _ := meter.(coremetrics.AsyncHistogramMeter)
	//: the two histograms read in the same collection, off the same Read.
	if histograms != nil {
		samples.callbacks += len(runtimeHistograms)
	}
	for i, key := range runtimeKeys {
		samples.samples[i].Name = key
		samples.index[key] = i
	}
	notes := runtimeNotes{units: asUnits(meter), docs: asDocs(meter), underscore: cfg.Underscore}
	//: the eight sums.
	for i := range runtimeInstruments {
		inst := &runtimeInstruments[i]
		name := notes.name(inst.name)
		registerRuntime(meter, name, inst, samples)
		notes.annotate(name, inst.unit, inst.description)
	}
	//: the histograms only where the meter takes an observable histogram.
	if histograms != nil {
		registerRuntimeHistograms(histograms, samples, notes)
	}
}

// runtimeNotes spells a runtime instrument's name and binds its annotations
// where the meter keeps them.
type runtimeNotes struct {
	// units and docs are the meter's siblings, nil where it has none.
	units coremetrics.UnitDescriber
	docs  coremetrics.Describer
	// underscore spells the names with '_' (RuntimeConfig.Underscore).
	underscore bool
}

// asUnits is meter's UnitDescriber, nil when it has none.
func asUnits(meter coremetrics.AsyncMeter) coremetrics.UnitDescriber {
	units, _ := meter.(coremetrics.UnitDescriber)
	//: the sibling, or nil.
	return units
}

// asDocs is meter's Describer, nil when it has none.
func asDocs(meter coremetrics.AsyncMeter) coremetrics.Describer {
	docs, _ := meter.(coremetrics.Describer)
	//: the sibling, or nil.
	return docs
}

// name is the OpenTelemetry name, underscored for a Prometheus meter.
func (n runtimeNotes) name(dotted string) string {
	//: Prometheus's grammar has no '.'.
	if n.underscore {
		return strings.ReplaceAll(dotted, ".", "_")
	}
	//: the convention's spelling.
	return dotted
}

// annotate binds unit and description to name, where the meter keeps them.
func (n runtimeNotes) annotate(name, unit, description string) {
	//: the unit, on a UnitDescriber.
	if n.units != nil {
		n.units.Unit(name, unit)
	}
	//: the description, on a Describer.
	if n.docs != nil {
		n.docs.Describe(name, description)
	}
}

// registerRuntimeHistograms registers the runtime's two histograms, each
// with its own conversion buffers.
func registerRuntimeHistograms(meter coremetrics.AsyncHistogramMeter, samples *runtimeSamples, notes runtimeNotes) {
	//: this meter's own copy: the buffers are per registration.
	for _, hist := range slices.Clone(runtimeHistograms) {
		name := notes.name(hist.name)
		registerRuntimeHistogram(meter, name, &hist, samples)
		notes.annotate(name, runtimeSeconds, hist.description)
	}
}

// registerRuntimeHistogram registers one histogram's callback under name.
func registerRuntimeHistogram(meter coremetrics.AsyncHistogramMeter, name string, hist *runtimeHistogram, samples *runtimeSamples) {
	meter.ObservableHistogram(name, func(observe coremetrics.ObserveHistogram) {
		//: a reading only when this runtime has the key.
		if point, ok := samples.observeHistogram(hist); ok {
			observe(point)
		}
	})
}

// registerRuntime registers one instrument's callback under name.
func registerRuntime(meter coremetrics.AsyncMeter, name string, inst *runtimeInstrument, samples *runtimeSamples) {
	callback := func(observe coremetrics.ObserveInt64) {
		//: a value only when there is one (a limit that is set).
		if v, ok := samples.observe(inst); ok {
			observe(v)
		}
	}
	//: a monotonic sum, or an up-down one.
	if inst.counter {
		meter.ObservableCounter(name, callback)
		return
	}
	meter.ObservableUpDownCounter(name, callback)
}
