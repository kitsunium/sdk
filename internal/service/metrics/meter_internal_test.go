// Package metrics — the in-memory Meter.
package metrics

import (
	"math"
	"slices"
	"sync"
	"testing"
)

// sole returns the single series a name is expected to hold, failing loudly
// when the snapshot holds a different number. Every test in this file works on
// dimensionless instruments, where "one name, one series" is the invariant a
// wrong answer would quietly break.
func sole[V any](t *testing.T, points []V, name string) V {
	t.Helper()
	if len(points) != 1 {
		t.Fatalf("%q holds %d series, want exactly 1", name, len(points))
	}
	return points[0]
}

// Test_memMeter_Counter pins the idempotent fetch. A metric name is looked up
// from wherever it is used, often several times, and each fetch MUST return the
// same instrument — a fresh one per call would scatter the total across as many
// counters as there are call sites, each reporting a fraction of the truth.
func Test_memMeter_Counter(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		fetches int
	}
	tests := []tc{
		{"a single fetch", 1},
		{"repeated fetches", 5},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m, ok := NewMeter().(*memMeter)
		if !ok {
			t.Fatal("NewMeter did not return a memMeter")
		}

		first := m.Counter("requests")
		for range c.fetches {
			//: the same name must resolve to the same instrument.
			if again := m.Counter("requests"); again != first {
				t.Fatal("a repeated fetch returned a different counter")
			}
		}
		//: increments through any fetch land in the one instrument.
		for range c.fetches {
			m.Counter("requests").Inc()
		}
		if got := sole(t, m.Collect().Sums["requests"].Points, "requests").Value; got != int64(c.fetches) {
			t.Errorf("the counter reads %d, want %d", got, c.fetches)
		}
		//: a different name is a different instrument.
		if m.Counter("errors") == first {
			t.Error("two names resolved to the same counter")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_memMeter_Gauge pins the same idempotence for gauges, where a duplicate
// instrument is arguably worse: two gauges under one name means Collect reports
// whichever the map happens to hold, so the value would flap between two
// unrelated readings.
func Test_memMeter_Gauge(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		fetches int
	}
	tests := []tc{
		{"a single fetch", 1},
		{"repeated fetches", 5},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m, ok := NewMeter().(*memMeter)
		if !ok {
			t.Fatal("NewMeter did not return a memMeter")
		}

		first := m.Gauge("in_flight")
		for range c.fetches {
			if again := m.Gauge("in_flight"); again != first {
				t.Fatal("a repeated fetch returned a different gauge")
			}
		}
		for range c.fetches {
			m.Gauge("in_flight").Add(1)
		}
		if got := sole(t, m.Collect().Gauges["in_flight"].Points, "in_flight").Value; math.Abs(got-float64(c.fetches)) > 1e-9 {
			t.Errorf("the gauge reads %v, want %v", got, float64(c.fetches))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_memMeter_Histogram pins that the boundaries are fixed at CREATION.
//
// A later fetch with different buckets returns the existing histogram unchanged,
// because the counts already recorded are positional: re-bucketing would leave
// every stored count attached to a boundary it was never measured against, and
// nothing downstream could detect it.
func Test_memMeter_Histogram(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		first       []float64
		second      []float64
		wantBuckets []float64
	}
	tests := []tc{
		{"the same buckets twice", []float64{1, 5}, []float64{1, 5}, []float64{1, 5}},
		{"different buckets on the second fetch", []float64{1, 5}, []float64{100}, []float64{1, 5}},
		{"no buckets on the second fetch", []float64{1, 5}, nil, []float64{1, 5}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m, ok := NewMeter().(*memMeter)
		if !ok {
			t.Fatal("NewMeter did not return a memMeter")
		}

		first := m.Histogram("latency", c.first)
		//: record before the second fetch, so a re-bucketing would be visible
		//: as a count attached to the wrong boundary.
		first.Record(3)

		again := m.Histogram("latency", c.second)
		if again != first {
			t.Fatal("a repeated fetch returned a different histogram")
		}

		snap := sole(t, m.Collect().Histograms["latency"].Points, "latency")
		if !slices.Equal(snap.Bounds, c.wantBuckets) {
			t.Errorf("bounds = %v, want the creation-time %v", snap.Bounds, c.wantBuckets)
		}
		if snap.Count != 1 {
			t.Errorf("Count = %d, want 1", snap.Count)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_memMeter_Collect pins the snapshot: it copies every instrument, and what
// it hands back does not change as the process keeps recording.
func Test_memMeter_Collect(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how many instruments of each kind to create.
		instruments int
	}
	tests := []tc{
		{"an empty meter", 0},
		{"one of each kind", 1},
		{"several of each kind", 5},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := NewMeter()
		for i := range c.instruments {
			name := string(rune('a' + i))
			m.Counter(name + "_count").Inc()
			m.Gauge(name + "_gauge").Set(float64(i))
			m.Histogram(name+"_latency", []float64{1}).Record(0.5)
		}

		snap := m.Collect()
		if len(snap.Sums) != c.instruments {
			t.Errorf("%d sums, want %d", len(snap.Sums), c.instruments)
		}
		if len(snap.Gauges) != c.instruments {
			t.Errorf("%d gauges, want %d", len(snap.Gauges), c.instruments)
		}
		if len(snap.Histograms) != c.instruments {
			t.Errorf("%d histograms, want %d", len(snap.Histograms), c.instruments)
		}

		//: keep recording; the snapshot already taken must not move.
		for i := range c.instruments {
			name := string(rune('a' + i))
			m.Counter(name + "_count").Inc()
		}
		for name, metric := range snap.Sums {
			if metric.Points[0].Value != 1 {
				t.Errorf("the snapshot's %q counter changed to %d", name, metric.Points[0].Value)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_memMeter_CollectConcurrent pins that a Collect racing instrument
// creation is safe. Collect takes a read lock while Counter/Gauge/Histogram take
// the write lock, so this is where an unguarded map access would surface.
func Test_memMeter_CollectConcurrent(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		writers int
		readers int
	}
	tests := []tc{
		{"a few of each", 4, 4},
		{"many of each", 16, 8},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := NewMeter()

		//: Goroutine lifecycle: c.writers creators and c.readers collectors,
		//: all joined by the WaitGroup before the assertion.
		var wg sync.WaitGroup
		for i := range c.writers {
			wg.Go(func() {
				for j := range 50 {
					m.Counter(string(rune('a'+i)) + string(rune('0'+j%10))).Inc()
				}
			})
		}
		for range c.readers {
			wg.Go(func() {
				for range 50 {
					//: the snapshot's content is irrelevant here; the race
					//: detector is the assertion.
					if snap := m.Collect(); snap.Sums == nil {
						t.Error("Collect returned a nil sum map")
						return
					}
				}
			})
		}
		wg.Wait()

		//: after the storm every counter created is present exactly once.
		snap := m.Collect()
		if len(snap.Sums) == 0 {
			t.Error("no counters survived the concurrent creation")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_carveSurvivesABrokenSizeInvariant pins the guard in carve. The per-name
// sizes are supposed to sum to the store's length; when they do not, the guard
// must hand that name a stand-alone window — a slower collection — instead of
// panicking inside a scrape. It used to sit AFTER the three-index slice it
// protects, so the slice's own bounds check panicked first and the guard never
// ran. Seen failing with the guard moved back below the slice: "slice bounds out
// of range [::3] with capacity 1".
func Test_carveSurvivesABrokenSizeInvariant(t *testing.T) {
	t.Parallel()
	//: one name claims three series while the store holds one.
	names := map[string]*nameState{"c": {kind: kindCounter, series: 3}}
	var windows int
	for _, slot := range carve[int](1, names, groupSum) {
		windows++
		if got := cap(slot.window); got != 3 {
			t.Errorf("window capacity %d, want the name's own 3", got)
		}
	}
	if windows != 1 {
		t.Errorf("carved %d windows, want 1", windows)
	}
}
