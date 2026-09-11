//go:build !race

// Package metrics — the allocation gate behind the labelled-lookup claim.
//
// Build constraint: the race detector adds its own bookkeeping allocations, so
// an allocation assertion is meaningless under `-race`. Race is ON by default
// in this repo (.bazelrc), which means this file compiles in exactly ONE lane —
// the race-off alloc lane. `//internal/service/metrics:metrics_test` is listed
// in tools/alloc-lane-targets.txt for that reason; without the entry this test
// would run nowhere at all, silently (CLAUDE.md rule 12).
//
// What it pins: resolving an EXISTING series costs zero allocations, labels or
// no labels. That is the whole reason the lookup path sorts into a stack array
// and indexes the map with string(scratch) instead of building a key string.
// Labels move the lookup from process start-up onto the request path — a value
// like status="503" is only known per observation — so an allocation here is
// an allocation per observation, and the claim is worth a gate.
package metrics

import (
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// allocRuns is how many iterations testing.AllocsPerRun averages over. Enough
// to amortise the first-call warm-up the helper already discards.
const allocRuns int = 200

// TestLookupIsAllocationFree pins zero allocations on every resolved-series
// fetch, across the label counts a caller realistically writes.
func TestLookupIsAllocationFree(t *testing.T) {
	type tc struct {
		name   string
		labels []coremetrics.LabelValue
	}
	tests := []tc{
		{name: "the dimensionless series"},
		{
			name:   "one label",
			labels: []coremetrics.LabelValue{{Key: "method", Value: "GET"}},
		},
		{
			//: declared out of key order, so the sort runs inside the
			//: measurement rather than being optimised away by luck.
			name: "three labels, unsorted",
			labels: []coremetrics.LabelValue{
				{Key: "status", Value: "200"},
				{Key: "method", Value: "GET"},
				{Key: "route", Value: "/v1/widgets"},
			},
		},
		{
			//: the stack scratch holds maxStackLabels; this fills it exactly,
			//: so a regression that shrinks the buffer shows up here.
			name:   "a full stack scratch",
			labels: manyLabels(maxStackLabels),
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := NewMeter()
		//: create the series first — this gate is about RESOLVING one, not
		//: about creating it, which allocates by construction.
		m.Counter("http_requests_total", c.labels...).Inc()

		got := testing.AllocsPerRun(allocRuns, func() {
			m.Counter("http_requests_total", c.labels...).Inc()
		})
		if got != 0 {
			t.Errorf("a resolved fetch allocates %v times per call, want 0", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestGaugeAndHistogramLookupAreAllocationFree pins the same budget on the two
// other instrument kinds, which share the lookup path but not its code.
func TestGaugeAndHistogramLookupAreAllocationFree(t *testing.T) {
	labels := []coremetrics.LabelValue{
		{Key: "status", Value: "200"},
		{Key: "method", Value: "GET"},
	}
	//: hoisted, deliberately. A bucket slice written as a literal inside the
	//: indirect call below cannot be proved non-escaping by the compiler, so
	//: it would allocate here and the test would be measuring its own
	//: harness. See BENCH.md — the same caveat applies to real call sites.
	buckets := []float64{1, 5}
	type tc struct {
		name  string
		fetch func(m coremetrics.Meter)
	}
	tests := []tc{
		{
			name:  "a gauge",
			fetch: func(m coremetrics.Meter) { m.Gauge("in_flight", labels...).Add(1) },
		},
		{
			name: "a histogram",
			fetch: func(m coremetrics.Meter) {
				m.Histogram("latency", buckets, labels...).Record(0.5)
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := NewMeter()
		//: warm the series into existence.
		c.fetch(m)

		if got := testing.AllocsPerRun(allocRuns, func() { c.fetch(m) }); got != 0 {
			t.Errorf("a resolved fetch allocates %v times per call, want 0", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			runCase(t, c)
		})
	}
}

// TestOverflowLookupIsAllocationFree pins the budget on the path a service
// takes AFTER its labels have blown up.
//
// This is the case that matters most: a process in overflow is by definition
// producing a never-before-seen label set on every observation, so it misses
// the read lock every time. If that path allocated, exceeding the cardinality
// bound would trade a memory leak for GC pressure — a different failure with
// the same cause, which is not a bound at all.
func TestOverflowLookupIsAllocationFree(t *testing.T) {
	m := NewMeterWithConfig(MeterConfig{MaxSeriesPerInstrument: 1})
	//: spend the one admitted slot, then force the overflow series to exist.
	m.Counter("http_requests_total").Inc()
	m.Counter("http_requests_total", coremetrics.LabelValue{Key: "id", Value: "0"}).Inc()

	//: every distinct label set from here on folds into that one series. The
	//: values are pre-rendered so the benchmark measures the meter, not
	//: strconv.
	ids := make([]string, allocRuns+2)
	for i := range ids {
		ids[i] = "id-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))
	}
	i := 0

	got := testing.AllocsPerRun(allocRuns, func() {
		i++
		m.Counter("http_requests_total", coremetrics.LabelValue{
			Key: "id", Value: ids[i%len(ids)],
		}).Inc()
	})
	if got != 0 {
		t.Errorf("an overflowing fetch allocates %v times per call, want 0", got)
	}

	//: and the fold really happened — one admitted series plus one overflow.
	series := m.Collect().Counters["http_requests_total"]
	if len(series) != 2 {
		t.Fatalf("%d series survived the overflow, want 2 (admitted + overflow)", len(series))
	}
}

// manyLabels builds n distinct labels with keys in descending order, so the
// sort has real work to do.
func manyLabels(n int) []coremetrics.LabelValue {
	out := make([]coremetrics.LabelValue, n)
	for i := range out {
		out[i] = coremetrics.LabelValue{
			Key:   "k" + string(rune('a'+(n-1-i))),
			Value: "v" + string(rune('a'+i)),
		}
	}
	return out
}
