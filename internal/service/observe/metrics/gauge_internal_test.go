// Package metrics — the atomic float64 gauge.
package metrics

import (
	"math"
	"sync"
	"testing"
)

// Test_memGauge_Set pins the bit-pattern round trip. The value is stored as
// IEEE-754 bits in an atomic.Uint64, so anything that survives that encoding has
// to come back exactly — including the values a naive numeric comparison would
// mishandle.
func Test_memGauge_Set(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    float64
	}
	tests := []tc{
		{"zero", 0},
		{"a positive value", 42.5},
		{"a negative value", -17.25},
		{"a very small value", math.SmallestNonzeroFloat64},
		{"the largest value", math.MaxFloat64},
		//: negative zero has a distinct bit pattern from positive zero.
		{"negative zero", math.Copysign(0, -1)},
		{"positive infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var g memGauge
		g.Set(c.v)

		got := g.load()
		//: compare bit patterns, so negative zero and the infinities are held
		//: to the same standard as any other value.
		if math.Float64bits(got) != math.Float64bits(c.v) {
			t.Errorf("load() = %v, want %v", got, c.v)
		}
		//: a later Set replaces rather than accumulates — that is what makes a
		//: gauge a gauge.
		g.Set(1)
		if g.load() != 1 {
			t.Errorf("a second Set did not replace the value: %v", g.load())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: NaN is round-tripped too, but only bit-for-bit: NaN != NaN by value.
	var g memGauge
	g.Set(math.NaN())
	if !math.IsNaN(g.load()) {
		t.Errorf("load() = %v after Set(NaN), want NaN", g.load())
	}
}

// Test_memGauge_Add pins the compare-and-swap retry loop. A gauge is adjusted
// from concurrent request handlers — in-flight counts, queue depths — so a lost
// update would leave a permanent drift that no later Set corrects.
func Test_memGauge_Add(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		start      float64
		goroutines int
		each       int
		delta      float64
	}
	tests := []tc{
		{"a single addition", 0, 1, 1, 1},
		{"repeated additions", 0, 1, 100, 0.5},
		{"a subtraction", 10, 1, 1, -3},
		{"concurrent additions", 0, 16, 100, 1},
		{"concurrent additions and subtractions", 1000, 8, 100, -1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var g memGauge
		g.Set(c.start)

		//: Goroutine lifecycle: c.goroutines goroutines, each adding a fixed
		//: number of times and returning; the WaitGroup joins them all.
		var wg sync.WaitGroup
		for range c.goroutines {
			wg.Go(func() {
				for range c.each {
					g.Add(c.delta)
				}
			})
		}
		wg.Wait()

		want := c.start + float64(c.goroutines*c.each)*c.delta
		//: floating-point accumulation, so allow a small epsilon — a LOST
		//: update would be off by a whole delta, far outside it.
		if math.Abs(g.load()-want) > 1e-9 {
			t.Errorf("load() = %v, want %v", g.load(), want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_memGauge_load pins the read used by Collect.
func Test_memGauge_load(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    float64
	}
	tests := []tc{
		{"a fresh gauge reads zero", 0},
		{"a set value", 3.5},
		{"a negative value", -1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		var g memGauge
		if c.v != 0 {
			g.Set(c.v)
		}
		first := g.load()
		if first != c.v {
			t.Fatalf("load() = %v, want %v", first, c.v)
		}
		//: repeated reads are stable; load has no side effects.
		if second := g.load(); second != first {
			t.Errorf("a second load() = %v, want %v", second, first)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
