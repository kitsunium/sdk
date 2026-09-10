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
// What it pins: resolving an EXISTING series costs zero allocations, attributes
// or no attributes. That is the whole reason the lookup path sorts into a stack
// array and indexes the map with string(scratch) instead of building a key
// string. Attributes move the lookup from process start-up onto the request
// path — a value like http.response.status_code=503 is only known per
// observation — so an allocation here is an allocation per observation, and the
// claim is worth a gate.
//
// It also pins, indirectly, that NewMeter and NewMeterWithConfig stay INLINABLE.
// Both are thin wrappers over newMemMeter for that reason: inlining is what
// carries the concrete meter type to the call site, and that is what lets the
// compiler prove a variadic attribute slice does not escape. Grow either
// constructor past the inlining budget and every case below regains one
// allocation per observation — measured, not theorised.
package metrics

import (
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// allocRuns is how many iterations testing.AllocsPerRun averages over. Enough
// to amortise the first-call warm-up the helper already discards.
const allocRuns int = 200

// TestLookupIsAllocationFree pins zero allocations on every resolved-series
// fetch, across the attribute counts and KINDS a caller realistically writes.
func TestLookupIsAllocationFree(t *testing.T) {
	type tc struct {
		name   string
		labels []coremetrics.AttrValue
	}
	tests := []tc{
		{name: "the dimensionless series"},
		{
			name:   "one attribute",
			labels: []coremetrics.AttrValue{coremetrics.String("method", "GET")},
		},
		{
			//: declared out of key order, so the sort runs inside the
			//: measurement rather than being optimised away by luck.
			name: "three attributes, unsorted",
			labels: []coremetrics.AttrValue{
				coremetrics.String("status", "200"),
				coremetrics.String("method", "GET"),
				coremetrics.String("route", "/v1/widgets"),
			},
		},
		{
			//: the three non-string kinds encode fixed-width bytes into the
			//: key buffer rather than a length-prefixed string, so they get
			//: their own case — a strconv call on this path would be an
			//: allocation per observation.
			name: "every attribute kind at once",
			labels: []coremetrics.AttrValue{
				coremetrics.Bool("cached", true),
				coremetrics.String("method", "GET"),
				coremetrics.Float64("ratio", 0.5),
				coremetrics.Int64("status", 503),
			},
		},
		{
			//: the stack scratch holds maxStackAttrs; this fills it exactly,
			//: so a regression that shrinks the buffer shows up here.
			name:   "a full stack scratch",
			labels: manyAttrs(maxStackAttrs),
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

// TestEveryInstrumentLookupIsAllocationFree pins the same budget on the three
// other synchronous instrument kinds, which share the lookup path but not its
// code — including the UpDownCounter, whose series live in the SAME store as a
// Counter's and are told apart by the instrument-kind byte that opens the key.
func TestEveryInstrumentLookupIsAllocationFree(t *testing.T) {
	labels := []coremetrics.AttrValue{
		coremetrics.String("status", "200"),
		coremetrics.String("method", "GET"),
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
			name: "an up-down counter",
			fetch: func(m coremetrics.Meter) {
				asFullMeter(m).UpDownCounter("queue_depth", labels...).Dec()
			},
		},
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
// takes AFTER its attributes have blown up.
//
// This is the case that matters most: a process in overflow is by definition
// producing a never-before-seen attribute set on every observation, so it
// misses the read lock every time. If that path allocated, exceeding the
// cardinality bound would trade a memory leak for GC pressure — a different
// failure with the same cause, which is not a bound at all.
func TestOverflowLookupIsAllocationFree(t *testing.T) {
	m := NewMeterWithConfig(MeterConfig{MaxSeriesPerInstrument: 1})
	//: spend the one admitted slot, then force the overflow series to exist.
	m.Counter("http_requests_total").Inc()
	m.Counter("http_requests_total", coremetrics.String("id", "0")).Inc()

	//: every distinct attribute set from here on folds into that one series. The
	//: values are pre-rendered so the benchmark measures the meter, not
	//: strconv.
	ids := make([]string, allocRuns+2)
	for i := range ids {
		ids[i] = "id-" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))
	}
	i := 0

	got := testing.AllocsPerRun(allocRuns, func() {
		i++
		m.Counter("http_requests_total", coremetrics.String("id", ids[i%len(ids)])).Inc()
	})
	if got != 0 {
		t.Errorf("an overflowing fetch allocates %v times per call, want 0", got)
	}

	//: and the fold really happened — one admitted series plus one overflow.
	series := m.Collect().Sums["http_requests_total"].Points
	if len(series) != 2 {
		t.Fatalf("%d series survived the overflow, want 2 (admitted + overflow)", len(series))
	}
}

// TestDescribedMeterLookupIsAllocationFree pins that ADR 0067 cost the
// observation path nothing.
//
// A description is wiring-time state, and the whole shape of the feature —
// Describe(name, …) on a sibling instead of a description parameter threaded
// through Counter — was chosen so that the fetch path never reads it. This is
// the executable version of that claim: the same fetch, on a meter that HAS a
// description map, still allocates zero. Threading the docstring through the
// instrument constructors would have put a second string on a variadic call the
// compiler has to prove non-escaping, which is exactly the proof this whole file
// exists to protect.
func TestDescribedMeterLookupIsAllocationFree(t *testing.T) {
	labels := []coremetrics.AttrValue{
		coremetrics.String("status", "200"),
		coremetrics.String("method", "GET"),
	}
	m := NewMeter()
	//: the map exists and is non-empty for the whole measurement.
	m.(coremetrics.Describer).Describe("http_requests_total", "Requests served")
	//: create the series first — this gate is about RESOLVING one.
	m.Counter("http_requests_total", labels...).Inc()

	got := testing.AllocsPerRun(allocRuns, func() {
		m.Counter("http_requests_total", labels...).Inc()
	})
	if got != 0 {
		t.Errorf("a fetch on a described meter allocates %v times per call, want 0", got)
	}
	//: and the description really is on the snapshot, so the gate is not
	//: passing by measuring a meter that quietly dropped it.
	if help := m.Collect().Sums["http_requests_total"].Description; help != "Requests served" {
		t.Fatalf("description = %q, want %q", help, "Requests served")
	}
}

// manyAttrs builds n distinct attributes with keys in descending order, so the
// sort has real work to do.
func manyAttrs(n int) []coremetrics.AttrValue {
	out := make([]coremetrics.AttrValue, n)
	for i := range out {
		out[i] = coremetrics.String(
			"k"+string(rune('a'+(n-1-i))),
			"v"+string(rune('a'+i)),
		)
	}
	return out
}

// asFullMeter reaches the sibling ports through the frozen Meter the table
// above is typed on.
func asFullMeter(m coremetrics.Meter) coremetrics.FullMeter {
	//: every in-tree Meter is a FullMeter; the widening is safe by ADR 0039.
	full, ok := m.(coremetrics.FullMeter)
	//: a Meter that is not one would be a foreign implementation.
	if !ok {
		//: nothing to measure.
		panic("the meter under test is not a FullMeter")
	}
	//: hand back the wide view.
	return full
}
