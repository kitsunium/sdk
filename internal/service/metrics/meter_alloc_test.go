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
	"runtime"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// allocRuns is how many fetches each claim performs inside the measured window.
// Enough that a regression allocating on a doubling schedule has crossed
// several growth steps by the end.
const allocRuns int = 200

// allocRuntimeWarmup is how many times a measured closure is exercised on a
// THROWAWAY meter before the window opens, and it warms the Go runtime rather
// than this package.
//
// asFullMeter widens the frozen Meter port to FullMeter with a type assertion,
// which the runtime serves from a per-call-site cache it builds LAZILY and on
// purpose: runtime/iface.go gates the build behind `cheaprand()&1023 != 0`, so
// roughly one assertion in 1024 pays for the cache and buildTypeAssertCache
// allocates it. The result is one ~64-byte allocation landing at an
// unpredictable point about a thousand calls into the process, attributable to
// no line in this repository — measured, one stray in 1 of every 40 windows,
// in all three sub-cases, from the single call site inside asFullMeter.
//
// It is invisible to testing.AllocsPerRun, because 1 over 200 is 0 after the
// integer division — the same rounding that hides a real regression also hid
// this. Thirty thousand calls put the probability that the cache is still
// unbuilt at (1023/1024)^30000, about 2e-13; measured, 0 strays in 160 windows.
//
// It runs on a throwaway meter, and that is not tidiness. The cache is the
// runtime's, per call site and process-global, so any meter can pay for it —
// while the state this file polices is PER METER. Warming through the meter
// under test would push any accumulating regression far past its own growth
// steps, which is the same blindness the integer division produces, moved into
// the harness.
const allocRuntimeWarmup int = 30000

// mallocsOver reports the TOTAL heap allocations f performs across runs calls,
// rather than the per-call average testing.AllocsPerRun reports.
//
// The gap between those two is the whole cardinality story of this package. A
// meter's entire job is to hold state that GROWS — a series map, a name table,
// an overflow ledger, and, the moment anyone adds one, a slice: recently-seen
// keys, a sampled-exemplar ring, a per-name arrival log. Every one of those
// allocates on a doubling step and not on the observations in between, so the
// regression this file is meant to catch is amortised BY CONSTRUCTION rather
// than by accident. testing.AllocsPerRun ends in
// `float64(mallocs / uint64(runs))` — INTEGER division, documented in the
// stdlib as being there so a caller can write `== 1` instead of `< 2` — and so
// reports exactly 0.0 for any defect allocating less than once per call.
// Measured here: a growing lookup log on the fetch path allocates 7 times in
// 200 fetches and AllocsPerRun calls that 0.
//
// A total is not subject to that rounding. The bookkeeping mirrors
// AllocsPerRun's otherwise — pin GOMAXPROCS so no other P allocates into the
// count, warm up so lazily-initialised state is not attributed to the loop, and
// read the counter either side. There is deliberately NO runtime.GC(): an
// explicit collection returns before its sweep finishes, so the residual work
// allocates INSIDE the window; AllocsPerRun does not call it either, for the
// same reason.
//
// One caller-side obligation comes with counting totals: anything the RUNTIME
// initialises lazily now shows up too. See allocRuntimeWarmup.
func mallocsOver(runs int, f func()) uint64 {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	//: warm up so first-call initialisation is not counted as steady state.
	f()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		f()
	}
	runtime.ReadMemStats(&after)
	//: Mallocs is cumulative and monotonic, so the difference is the total.
	return after.Mallocs - before.Mallocs
}

// TestLookupIsAllocationFree pins zero allocations on every resolved-series
// fetch, across the attribute counts and KINDS a caller realistically writes.
//
// MUTATION-CHECKED, and the mutation is the one every metrics library
// eventually ships: a `lookupLog []int` field on memMeter with
// `s.meter.lookupLog = append(s.meter.lookupLog, len(key))` opening
// seriesStore.lookup — a per-observation record of which keys are being asked
// for, the natural first step towards cardinality diagnostics. All five cases
// fail at `200 resolved fetches performed 7 allocations, want 0`. Seven, not
// two hundred: the slice doubles, so it allocates on the growth steps and not
// on the fetches between them. testing.AllocsPerRun measured the same mutated
// fetch, three separate runs, and reported 0 every time. See mallocsOver.
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

		got := mallocsOver(allocRuns, func() {
			m.Counter("http_requests_total", c.labels...).Inc()
		})
		if got != 0 {
			t.Errorf("%d resolved fetches performed %d allocations, want 0", allocRuns, got)
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
//
// MUTATION-CHECKED with the same appending lookupLog: all three cases fail at
// `200 resolved fetches performed 7 allocations, want 0`, and
// testing.AllocsPerRun reported 0 for the same mutated path.
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
		//: the runtime's lazy assertion cache is spent on a meter nothing
		//: measures, so the one under test starts with its own state fresh.
		warm := NewMeter()
		for range allocRuntimeWarmup {
			c.fetch(warm)
		}
		m := NewMeter()
		//: widened ONCE, here: passing a FullMeter to a Meter parameter is an
		//: interface-to-interface conversion, and one inside the window would
		//: be a second lazily-cached call site charging the meter for the
		//: runtime's bookkeeping.
		var port coremetrics.Meter = m
		//: warm the series into existence.
		c.fetch(port)

		if got := mallocsOver(allocRuns, func() { c.fetch(port) }); got != 0 {
			t.Errorf("%d resolved fetches performed %d allocations, want 0", allocRuns, got)
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
//
// MUTATION-CHECKED with the same appending lookupLog: fails at
// `200 overflowing fetches performed 6 allocations, want 0` — six rather than
// the seven the other arms report, because this path enters the store once per
// observation rather than twice. testing.AllocsPerRun reported 0. And the
// arithmetic is the point: a bound that costs one allocation per observation
// once it is REACHED has replaced the leak with the GC pressure this test
// exists to refuse, and the old form of the assertion could not see it.
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

	got := mallocsOver(allocRuns, func() {
		i++
		m.Counter("http_requests_total", coremetrics.String("id", ids[i%len(ids)])).Inc()
	})
	if got != 0 {
		t.Errorf("%d overflowing fetches performed %d allocations, want 0", allocRuns, got)
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
//
// MUTATION-CHECKED with the same appending lookupLog: fails at
// `200 fetches on a described meter performed 7 allocations, want 0`, and
// testing.AllocsPerRun reported 0 for the same mutated path.
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

	got := mallocsOver(allocRuns, func() {
		m.Counter("http_requests_total", labels...).Inc()
	})
	if got != 0 {
		t.Errorf("%d fetches on a described meter performed %d allocations, want 0", allocRuns, got)
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
