// Package metrics — the atomic monotonic counter.
package metrics

import (
	"sync"
	"testing"
)

// Test_memCounter_Add pins monotonicity. A counter that could go down would
// break every consumer that computes a rate from two samples: a decrease reads
// as a counter reset, and the whole interval is then discarded or, worse,
// reported as an enormous spike.
func Test_memCounter_Add(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		deltas []int64
		want   int64
	}
	tests := []tc{
		{"a single increment", []int64{1}, 1},
		{"several increments", []int64{1, 2, 3}, 6},
		{"a large increment", []int64{1 << 40}, 1 << 40},
		//: the monotonicity guard: negatives and zero contribute nothing.
		{"a negative delta is ignored", []int64{5, -3}, 5},
		{"a zero delta is ignored", []int64{5, 0}, 5},
		{"only negatives leave it at zero", []int64{-1, -2}, 0},
		{"nothing added at all", nil, 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var counter memCounter
		for _, d := range c.deltas {
			counter.Add(d)
		}
		if got := counter.load(); got != c.want {
			t.Errorf("load() = %d, want %d", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_memCounter_Inc pins the one-step path, and that it is safe to call from
// every goroutine at once — a counter is the instrument most likely to be
// incremented from a request handler.
func Test_memCounter_Inc(t *testing.T) {
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
		var counter memCounter

		//: Goroutine lifecycle: c.goroutines goroutines, each incrementing a
		//: fixed number of times and returning; the WaitGroup joins them all
		//: before the assertion.
		var wg sync.WaitGroup
		for range c.goroutines {
			wg.Go(func() {
				for range c.each {
					counter.Inc()
				}
			})
		}
		wg.Wait()

		//: every increment must be accounted for — a lost update under
		//: contention is exactly what the atomic exists to prevent.
		want := int64(c.goroutines * c.each)
		if got := counter.load(); got != want {
			t.Errorf("load() = %d, want %d", got, want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_memCounter_load pins the read used by Collect: it must observe every
// increment that has completed, without blocking the writers.
func Test_memCounter_load(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		steps int
	}
	tests := []tc{
		{"a fresh counter reads zero", 0},
		{"after one step", 1},
		{"after many steps", 5000},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var counter memCounter
		for range c.steps {
			counter.Inc()
		}
		//: repeated reads are stable; load has no side effects.
		first := counter.load()
		if first != int64(c.steps) {
			t.Fatalf("load() = %d, want %d", first, c.steps)
		}
		if second := counter.load(); second != first {
			t.Errorf("a second load() = %d, want %d", second, first)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
