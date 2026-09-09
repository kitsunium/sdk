// Package metrics — the atomic sum behind both counter kinds.
package metrics

import (
	"sync"
	"testing"
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
// itself. An observed series is also never swapped to zero on collection —
// doing so would report the same delta twice, once as itself and once as its
// own negation.
func Test_memSum_observe(t *testing.T) {
	t.Parallel()
	type tc struct {
		name      string
		delta     bool
		absolutes []int64
		want      []int64
	}
	tests := []tc{
		{
			name:      "cumulative reports the absolute value",
			absolutes: []int64{10, 15, 15, 40},
			want:      []int64{10, 15, 15, 40},
		},
		{
			name:      "delta reports the difference",
			delta:     true,
			absolutes: []int64{10, 15, 15, 40},
			//: the first window starts at zero, then 5, then nothing, then 25.
			want: []int64{10, 5, 0, 25},
		},
		{
			//: an observable that resets (a process restart behind the
			//: callback) reports a negative window rather than a silent gap.
			//: Naming it here records what the SDK does not attempt to hide.
			name:      "delta reports a decreasing absolute as a negative window",
			delta:     true,
			absolutes: []int64{10, 4},
			want:      []int64{10, -6},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		sum := newObservableCounter()
		for i, absolute := range c.absolutes {
			sum.observe(absolute, c.delta)
			//: collect must not consume an observed series.
			if got := sum.collect(c.delta); got != c.want[i] {
				t.Errorf("collect #%d = %d, want %d", i+1, got, c.want[i])
			}
			//: and reading it twice must report the same thing, because the
			//: value was produced by the callback and not by the reader.
			if got := sum.collect(c.delta); got != c.want[i] {
				t.Errorf("a second collect #%d = %d, want %d", i+1, got, c.want[i])
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
