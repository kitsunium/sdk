// Package metrics — the atomic sum behind both counter kinds.
package metrics

import (
	"math"
	"sync"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/metrics"
)

// Test_memSum_Add pins monotonicity, which is the ONLY difference between a
// Counter and an UpDownCounter in this SDK — and, in the OTel data model, one
// boolean field on the metric rather than a second point type.
//
// A monotonic sum that could go down would break every consumer that computes a
// rate from two samples: a decrease reads as a counter reset, and the whole
// interval is then discarded or, worse, reported as an enormous spike. A
// non-monotonic one must accept exactly the deltas the monotonic one refuses.
func Test_memSum_Add(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		build  func() *memSum
		deltas []int64
		want   int64
	}
	tests := []tc{
		{"a single increment", newCounter, []int64{1}, 1},
		{"several increments", newCounter, []int64{1, 2, 3}, 6},
		{"a large increment", newCounter, []int64{1 << 40}, 1 << 40},
		//: the monotonicity guard: negatives and zero contribute nothing.
		{"a negative delta is ignored by a counter", newCounter, []int64{5, -3}, 5},
		{"a zero delta is ignored by a counter", newCounter, []int64{5, 0}, 5},
		{"only negatives leave a counter at zero", newCounter, []int64{-1, -2}, 0},
		{"nothing added at all", newCounter, nil, 0},
		//: the up-down counter is the same storage with the guard removed.
		{"an up-down counter accepts a negative", newUpDownCounter, []int64{5, -3}, 2},
		{"an up-down counter can go below zero", newUpDownCounter, []int64{-1, -2}, -3},
		{"an up-down counter accepts zero", newUpDownCounter, []int64{5, 0}, 5},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sum := c.build()
		for _, d := range c.deltas {
			sum.Add(d)
		}
		if got := sum.collect(false); got != c.want {
			t.Errorf("collect(false) = %d, want %d", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_memSum_Dec pins that Dec obeys the same monotonicity rule Add does.
//
// Dec is unreachable through the Counter interface, which does not declare it —
// that is what makes Counter and UpDownCounter structurally distinct. It is
// still reachable by a type assertion on the concrete instrument, so it routes
// through Add rather than touching the atomic directly, and a monotonic sum
// stays put.
func Test_memSum_Dec(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		build func() *memSum
		start int64
		want  int64
	}
	tests := []tc{
		{"a counter refuses to decrease", newCounter, 5, 5},
		{"an up-down counter decreases", newUpDownCounter, 5, 4},
		{"an up-down counter passes zero", newUpDownCounter, 0, -1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sum := c.build()
		sum.Add(c.start)
		sum.Dec()
		if got := sum.collect(false); got != c.want {
			t.Errorf("collect(false) = %d, want %d", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_memSum_Inc pins the one-step path, and that it is safe to call from
// every goroutine at once — a counter is the instrument most likely to be
// incremented from a request handler.
func Test_memSum_Inc(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		goroutines int
		each       int
	}
	tests := []tc{
		{"a single increment", 1, 1},
		{"a burst from one goroutine", 1, 1000},
		{"concurrent increments", 16, 250},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sum := newCounter()

		//: Goroutine lifecycle: c.goroutines goroutines, each incrementing a
		//: fixed number of times and returning; the WaitGroup joins them all
		//: before the assertion.
		var wg sync.WaitGroup
		for range c.goroutines {
			wg.Go(func() {
				for range c.each {
					sum.Inc()
				}
			})
		}
		wg.Wait()

		//: every increment must be accounted for — a lost update under
		//: contention is exactly what the atomic exists to prevent.
		want := int64(c.goroutines * c.each)
		if got := sum.collect(false); got != want {
			t.Errorf("collect(false) = %d, want %d", got, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_memSum_collect pins the difference between the two temporalities on the
// read path, which is where the whole concept becomes observable.
//
// Cumulative READS: two collections of an untouched sum report the same number
// twice, because a cumulative point covers everything since the start.
// Delta CONSUMES: the second collection reports zero, because the window it
// covers is empty. Getting this backwards is not a rounding error — a backend
// would double-count every observation forever.
func Test_memSum_collect(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		delta      bool
		firstAdd   int64
		secondAdd  int64
		wantFirst  int64
		wantSecond int64
	}
	tests := []tc{
		{
			name:      "cumulative repeats the running total",
			firstAdd:  3,
			secondAdd: 4,
			//: 3, then 3+4 — every collection covers the whole history.
			wantFirst: 3, wantSecond: 7,
		},
		{
			name:      "delta reports only the new window",
			delta:     true,
			firstAdd:  3,
			secondAdd: 4,
			//: 3, then 4 — the first window was consumed by reading it.
			wantFirst: 3, wantSecond: 4,
		},
		{
			name:     "a delta window with no observations is zero",
			delta:    true,
			firstAdd: 3,
			//: nothing happened in the second window.
			wantFirst: 3, wantSecond: 0,
		},
		{
			name:     "a cumulative window with no observations repeats",
			firstAdd: 3,
			//: the total did not change, so neither does the report.
			wantFirst: 3, wantSecond: 3,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sum := newCounter()
		sum.Add(c.firstAdd)
		if got := sum.collect(c.delta); got != c.wantFirst {
			t.Errorf("the first collect = %d, want %d", got, c.wantFirst)
		}
		sum.Add(c.secondAdd)
		if got := sum.collect(c.delta); got != c.wantSecond {
			t.Errorf("the second collect = %d, want %d", got, c.wantSecond)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_memSum_observe pins the asynchronous half of temporality.
//
// An observable callback reports an ABSOLUTE total (goroutines alive, bytes
// allocated since boot). Under cumulative temporality that IS the report; under
// delta it is not, and the meter has to difference successive observations
// itself. Each step of a case is one collection: the callback runs first and
// either reports a total or, where the step reads `unreported`, runs without
// mentioning the series at all; the reader follows.
//
// Two rules decide the delta answers, and each was a defect before it was a
// row here:
//
//   - A series the callback did not report saw nothing in that window, so it
//     reports ZERO — what an untouched synchronous series reports. The last
//     window used to be re-emitted instead, on every collection until the
//     series came back, and a backend summing deltas counted it each time.
//     previous survives the silence, so a series that returns is differenced
//     against its last reading rather than against zero.
//   - A MONOTONIC total that went down restarted behind the callback (a process
//     restart resets it), so the window is the new total: counted from zero
//     since the reset. It is never a negative delta on a sum whose metric says
//     Monotonic. An up-down total is a signed quantity and keeps its signed
//     window.
//
// The cumulative path is deliberately untouched by both: a silent callback
// leaves the last total standing, and a decrease is published as reported,
// because a cumulative reader decides for itself what a drop means.
//
// MUTATION-CHECKED, one mutation per rule, each restoring one defect and
// nothing else. Deleting collect's zero-reports gate, so every observed series
// is read with s.v.Load() whatever its callback did this collection, fails the
// three silence rows: `collection #2 = 5, want 0` on the first, and
// `collection #2 = 10, want 0` where the silence precedes a restart. Deleting
// observe's monotonic reset fails the two decrease rows on an observable
// counter, at `collection #2 = -7, want 3` and `collection #3 = -6, want 4`.
// Applying the reset to every sum instead of a monotonic one fails the two
// up-down rows, at `collection #2 = 3, want -7` and
// `collection #4 = 2, want -3`. The code before the fix failed the silence and
// decrease rows the same way.
func Test_memSum_observe(t *testing.T) {
	t.Parallel()
	//: a collection in which the callback did not report the series — a
	//: sentinel, since no row reports the most negative total there is.
	const unreported int64 = math.MinInt64
	type tc struct {
		name      string
		build     func() *memSum
		delta     bool
		absolutes []int64
		want      []int64
	}
	tests := []tc{
		{
			name:      "cumulative reports the absolute value",
			build:     newObservableCounter,
			absolutes: []int64{10, 15, 15, 40},
			want:      []int64{10, 15, 15, 40},
		},
		{
			//: the cumulative path is untouched by both delta rules.
			name:      "cumulative keeps the last total through a silence and publishes a decrease as reported",
			build:     newObservableCounter,
			absolutes: []int64{10, unreported, 4},
			want:      []int64{10, 10, 4},
		},
		{
			name:      "delta reports the difference",
			build:     newObservableCounter,
			delta:     true,
			absolutes: []int64{10, 15, 15, 40},
			//: the first window starts at zero, then 5, then nothing, then 25.
			want: []int64{10, 5, 0, 25},
		},
		{
			name:      "delta reports an empty window for a collection that did not report the series",
			build:     newObservableCounter,
			delta:     true,
			absolutes: []int64{5, unreported, 8},
			//: 5, then NOTHING rather than 5 again, then 8 - 5.
			want: []int64{5, 0, 3},
		},
		{
			name:      "delta reports an empty window for every collection of a long silence",
			build:     newObservableUpDownCounter,
			delta:     true,
			absolutes: []int64{5, unreported, unreported, 2},
			//: previous is still 5 when the series returns, so 2 - 5.
			want: []int64{5, 0, 0, -3},
		},
		{
			name:      "a delta monotonic total that went down restarted, so the window is the new total",
			build:     newObservableCounter,
			delta:     true,
			absolutes: []int64{10, 3, 7},
			//: 10, then the 3 counted since the restart, then 7 - 3.
			want: []int64{10, 3, 4},
		},
		{
			name:      "a delta monotonic total that returns lower after a silence restarted during it",
			build:     newObservableCounter,
			delta:     true,
			absolutes: []int64{10, unreported, 4},
			want:      []int64{10, 0, 4},
		},
		{
			//: an up-down total may legitimately fall, and says so.
			name:      "a delta up-down total that went down reports a signed window",
			build:     newObservableUpDownCounter,
			delta:     true,
			absolutes: []int64{10, 3},
			want:      []int64{10, -7},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sum := c.build()
		for i, absolute := range c.absolutes {
			//: the callback runs first, and may not mention the series.
			if absolute != unreported {
				sum.observe(absolute, c.delta)
			}
			//: then the collection reads it.
			if got := sum.collect(c.delta); got != c.want[i] {
				t.Errorf("collection #%d = %d, want %d", i+1, got, c.want[i])
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

// TestDeltaObservableThatStopsReportingReportsZero pins the silence rule end
// to end, through a real Collect, in the shape it takes in production: the
// callback keeps running and simply stops mentioning one attribute set — a
// shard that was drained, a pool that was closed.
//
// Three windows. {shard=a} reports 5, is absent from the second collection,
// then reports 8, and a delta reader must see 5, 0 and 3: the silent window is
// EMPTY, and the return is differenced against 5, the last reading, rather
// than against zero. {shard=b} reports throughout, so the silence of one series
// is not mistaken for the silence of the instrument.
//
// MUTATION-CHECKED with the same deleted zero-reports gate: both cases fail at
// `window #2: shard a = 5, want 0` — the second window re-emits the first,
// which is what the code before the fix did too, and it would have gone on
// re-emitting it on every collection until the shard came back.
func TestDeltaObservableThatStopsReportingReportsZero(t *testing.T) {
	t.Parallel()
	//: what the callback reports in each collection; a shard missing from a
	//: window is one the callback does not mention in it.
	windows := []map[string]int64{
		{"a": 5, "b": 1},
		{"b": 2},
		{"a": 8, "b": 2},
	}
	//: what a delta reader must see, series by series.
	want := []map[string]int64{
		{"a": 5, "b": 1},
		{"a": 0, "b": 1},
		{"a": 3, "b": 0},
	}
	type tc struct {
		name     string
		register func(m *memMeter, observe coremetrics.Int64Callback)
	}
	tests := []tc{
		{
			name: "an observable counter",
			register: func(m *memMeter, observe coremetrics.Int64Callback) {
				m.ObservableCounter("shard_items", observe)
			},
		},
		{
			name: "an observable up-down counter",
			register: func(m *memMeter, observe coremetrics.Int64Callback) {
				m.ObservableUpDownCounter("shard_items", observe)
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := newMemMeter(MeterConfig{Temporality: coremetrics.TemporalityDelta})
		window := 0
		c.register(m, func(observe coremetrics.ObserveInt64) {
			//: a fixed shard order, so the reports are deterministic.
			for _, shard := range []string{"a", "b"} {
				if value, ok := windows[window][shard]; ok {
					observe(value, coremetrics.String("shard", shard))
				}
			}
		})
		for window = range windows {
			got := make(map[string]int64, len(want[window]))
			for _, point := range m.Collect().Sums["shard_items"].Points {
				got[point.Attrs[0].Str()] = point.Value
			}
			for shard, value := range want[window] {
				if got[shard] != value {
					t.Errorf("window #%d: shard %s = %d, want %d", window+1, shard, got[shard], value)
				}
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
