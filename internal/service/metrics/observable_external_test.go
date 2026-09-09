// Package metrics_test — the asynchronous instruments and the temporality that
// decides what their reported numbers mean.
package metrics_test

import (
	"strconv"
	"sync"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
	svcmetrics "github.com/kitsunium/sdk/internal/service/metrics"
)

// TestObservableIsReadOncePerCollection pins the defining property of an
// asynchronous instrument: its value is produced by a callback at COLLECTION
// time, not written by the caller at observation time.
//
// A synchronous instrument reports what was recorded; an observable reports
// what is true now. That is the difference that makes it the right shape for a
// value that already exists somewhere — a runtime counter, a queue length —
// where instrumenting synchronously would mean finding every mutation site.
func TestObservableIsReadOncePerCollection(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeter()

	var calls int
	var reading int64
	m.ObservableCounter("allocated_total", func(observe coremetrics.ObserveInt64) {
		calls++
		observe(reading)
	})

	//: registration alone runs nothing.
	if calls != 0 {
		t.Errorf("the callback ran %d times before any collection", calls)
	}

	reading = 10
	if got := m.Collect().Sums["allocated_total"].Points[0].Value; got != 10 {
		t.Errorf("the first collection reads %d, want 10", got)
	}
	reading = 25
	if got := m.Collect().Sums["allocated_total"].Points[0].Value; got != 25 {
		t.Errorf("the second collection reads %d, want 25", got)
	}
	if calls != 2 {
		t.Errorf("the callback ran %d times over two collections, want 2", calls)
	}
}

// TestObservableCallbacksAccumulate pins the OTel API's own rule: registering a
// second callback under one name ADDS it rather than replacing it, so two
// packages can both contribute to one instrument.
func TestObservableCallbacksAccumulate(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeter()
	m.ObservableGauge("shards", func(observe coremetrics.ObserveFloat64) {
		observe(1, coremetrics.String("shard", "a"))
	})
	m.ObservableGauge("shards", func(observe coremetrics.ObserveFloat64) {
		observe(2, coremetrics.String("shard", "b"))
	})

	points := m.Collect().Gauges["shards"].Points
	if len(points) != 2 {
		t.Fatalf("%d series, want 2 — the second callback replaced the first", len(points))
	}
	if points[0].Value != 1 || points[1].Value != 2 {
		t.Errorf("the two series read %v and %v, want 1 and 2", points[0].Value, points[1].Value)
	}
}

// TestObservableWithNoCallbackIsNotRun pins the handling of a nil callback: it
// is dropped at registration rather than stored and called inside a scrape,
// where the panic would land far from the wiring that caused it. The NAME is
// still bound, so the kind conflict a later synchronous fetch would cause is
// still detected.
func TestObservableWithNoCallbackIsNotRun(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeter()
	m.ObservableCounter("allocated_total", nil)

	//: collecting must not panic, and must report nothing under that name.
	if points := m.Collect().Sums["allocated_total"].Points; len(points) != 0 {
		t.Errorf("a nil callback produced %d series", len(points))
	}
	//: but the name is taken, so a synchronous fetch of it still conflicts.
	defer func() {
		if recover() == nil {
			t.Error("a name bound by a nil-callback registration did not conflict")
		}
	}()
	m.Counter("allocated_total")
}

// TestObservableHonoursTheCardinalityBound pins that a callback cannot escape
// the bound a synchronous caller is held to. It is the more dangerous of the
// two, because a loop inside a callback can mint a thousand series in one
// collection with nothing between it and the map.
func TestObservableHonoursTheCardinalityBound(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeterWithConfig(svcmetrics.MeterConfig{MaxSeriesPerInstrument: 3})
	m.ObservableUpDownCounter("queue_depth", func(observe coremetrics.ObserveInt64) {
		for i := range 50 {
			observe(int64(i), coremetrics.String("id", strconv.Itoa(i)))
		}
	})

	points := m.Collect().Sums["queue_depth"].Points
	//: three admitted plus the one aggregated series outside the bound.
	if len(points) != 4 {
		t.Fatalf("%d series survived a bound of 3, want 4", len(points))
	}
	var sawOverflow bool
	for _, p := range points {
		for _, a := range p.Attrs {
			if a.Key == coremetrics.OverflowAttrKey {
				sawOverflow = true
			}
		}
	}
	if !sawOverflow {
		t.Error("the fold happened but no overflow series is visible")
	}
}

// TestDeltaCollectConsumesTheWindow pins the temporality contract end to end,
// across all three metric kinds that have one.
//
// Under cumulative a collection REPEATS the running total and the window's
// start never moves; under delta it CONSUMES what it reports and the start
// advances to the previous end. Getting this backwards is not a rounding error:
// a backend fed repeated cumulative values as if they were deltas double-counts
// every observation for as long as the process lives.
func TestDeltaCollectConsumesTheWindow(t *testing.T) {
	t.Parallel()
	type tc struct {
		name        string
		temporality coremetrics.Temporality
		wantSecond  int64
		wantCount   uint64
		startMoves  bool
	}
	tests := []tc{
		{
			name: "cumulative repeats", temporality: coremetrics.TemporalityCumulative,
			wantSecond: 3, wantCount: 2,
		},
		{
			name: "delta consumes", temporality: coremetrics.TemporalityDelta,
			wantSecond: 0, wantCount: 0, startMoves: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := svcmetrics.NewMeterWithConfig(svcmetrics.MeterConfig{Temporality: c.temporality})
		m.Counter("requests").Add(3)
		hist := m.Histogram("latency", []float64{1})
		hist.Record(0.5)
		hist.Record(2)

		first := m.Collect()
		if got := first.Sums["requests"].Points[0].Value; got != 3 {
			t.Errorf("the first sum reads %d, want 3", got)
		}
		if got := first.Histograms["latency"].Points[0].Count; got != 2 {
			t.Errorf("the first histogram counts %d, want 2", got)
		}
		//: the metric declares its own temporality; a reader never guesses.
		if got := first.Sums["requests"].Temporality; got != c.temporality {
			t.Errorf("the sum reports temporality %v, want %v", got, c.temporality)
		}
		if got := first.Histograms["latency"].Temporality; got != c.temporality {
			t.Errorf("the histogram reports temporality %v, want %v", got, c.temporality)
		}
		//: a gauge has NO temporality at all, which is the model rather than
		//: an omission — a sampled reading covers no window.
		m.Gauge("in_flight").Set(4)
		if got := m.Collect().Gauges["in_flight"].Points[0].Value; got != 4 {
			t.Errorf("the gauge reads %v, want 4", got)
		}

		second := m.Collect()
		if got := second.Sums["requests"].Points[0].Value; got != c.wantSecond {
			t.Errorf("the second sum reads %d, want %d", got, c.wantSecond)
		}
		if got := second.Histograms["latency"].Points[0].Count; got != c.wantCount {
			t.Errorf("the second histogram counts %d, want %d", got, c.wantCount)
		}
		//: a gauge is unaffected by either temporality — it keeps its reading.
		if got := second.Gauges["in_flight"].Points[0].Value; got != 4 {
			t.Errorf("the gauge lost its reading under %v: %v", c.temporality, got)
		}
		moved := !second.StartTime.Equal(first.StartTime)
		if moved != c.startMoves {
			t.Errorf("the window start moved = %v, want %v", moved, c.startMoves)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestDeltaHistogramConsumesEveryBucket pins that a delta collection consumes
// the whole distribution, not only its totals. A bucket left un-consumed would
// re-report its observations in every later window, and the ladder would climb
// forever while _count restarted.
func TestDeltaHistogramConsumesEveryBucket(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeterWithConfig(svcmetrics.MeterConfig{
		Temporality: coremetrics.TemporalityDelta,
	})
	hist := m.Histogram("latency", []float64{1, 5})
	hist.Record(0.5)
	hist.Record(2)
	hist.Record(9)

	first := m.Collect().Histograms["latency"].Points[0]
	if want := []uint64{1, 1, 1}; !equalCounts(first.Counts, want) {
		t.Fatalf("the first window's buckets are %v, want %v", first.Counts, want)
	}
	if first.Sum != 11.5 {
		t.Errorf("the first window sums %v, want 11.5", first.Sum)
	}

	second := m.Collect().Histograms["latency"].Points[0]
	if want := []uint64{0, 0, 0}; !equalCounts(second.Counts, want) {
		t.Errorf("the second window's buckets are %v, want %v", second.Counts, want)
	}
	if second.Sum != 0 {
		t.Errorf("the second window sums %v, want 0", second.Sum)
	}
	//: the BOUNDS are not a window and never move.
	if len(second.Bounds) != 2 {
		t.Errorf("the second window declares %v bounds, want the original two", second.Bounds)
	}
}

// equalCounts compares two bucket-count slices.
func equalCounts(got, want []uint64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestConcurrentCollectDoesNotSplitADeltaWindow pins why Collect is serialised
// against itself.
//
// Under delta, Collect is a MUTATION: it consumes the window it reports. Two
// concurrent collections would each carry away part of the observations with no
// way to notice — the totals would simply be wrong, split unpredictably between
// two callers. The mutex makes each collection whole; the assertion is that the
// windows SUM to what was recorded.
func TestConcurrentCollectDoesNotSplitADeltaWindow(t *testing.T) {
	t.Parallel()
	m := svcmetrics.NewMeterWithConfig(svcmetrics.MeterConfig{
		Temporality: coremetrics.TemporalityDelta,
	})
	const observations int64 = 1000
	for range observations {
		m.Counter("requests").Inc()
	}

	//: Goroutine lifecycle: eight collectors, each taking one snapshot and
	//: returning; the WaitGroup joins them before the assertion.
	var mu sync.Mutex
	var total int64
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			snap := m.Collect()
			mu.Lock()
			for _, p := range snap.Sums["requests"].Points {
				total += p.Value
			}
			mu.Unlock()
		})
	}
	wg.Wait()

	if total != observations {
		t.Errorf("the eight delta windows sum to %d, want %d — an observation was lost or double-counted",
			total, observations)
	}
}

// TestObservableAndSynchronousCannotShareAName pins the conflict that the
// shared sum store would otherwise hide.
//
// A Counter and an ObservableCounter produce the same point shape AND the same
// monotonicity, so nothing in the snapshot would distinguish them — but the two
// are collected differently under delta (one is swapped to zero, the other is
// differenced), so one name carrying both would report nonsense. The instrument
// kind opens the series key precisely so this reaches bindName instead of
// hitting the read lock.
func TestObservableAndSynchronousCannotShareAName(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		bind  func(m coremetrics.FullMeter)
		clash func(m coremetrics.FullMeter)
	}
	tests := []tc{
		{
			"a counter registered as an observable counter",
			func(m coremetrics.FullMeter) { m.Counter("x") },
			func(m coremetrics.FullMeter) {
				m.ObservableCounter("x", func(observe coremetrics.ObserveInt64) { observe(1) })
			},
		},
		{
			"an observable counter fetched as a counter",
			func(m coremetrics.FullMeter) {
				m.ObservableCounter("x", func(observe coremetrics.ObserveInt64) { observe(1) })
			},
			func(m coremetrics.FullMeter) { m.Counter("x") },
		},
		{
			"a gauge registered as an observable gauge",
			func(m coremetrics.FullMeter) { m.Gauge("x") },
			func(m coremetrics.FullMeter) {
				m.ObservableGauge("x", func(observe coremetrics.ObserveFloat64) { observe(1) })
			},
		},
		{
			"an observable counter registered as an observable up-down counter",
			func(m coremetrics.FullMeter) {
				m.ObservableCounter("x", func(observe coremetrics.ObserveInt64) { observe(1) })
			},
			func(m coremetrics.FullMeter) {
				m.ObservableUpDownCounter("x", func(observe coremetrics.ObserveInt64) { observe(1) })
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := svcmetrics.NewMeter()
		c.bind(m)
		defer func() {
			if recover() == nil {
				t.Error("a cross-kind name reuse did not panic")
			}
		}()
		c.clash(m)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
