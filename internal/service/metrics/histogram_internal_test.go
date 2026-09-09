// Package metrics — the atomic bucketed histogram.
package metrics

import (
	"math"
	"slices"
	"sync"
	"testing"
	"time"
)

// Test_newHistogram pins the defensive copy and the sort.
//
// Both matter for the same reason: the bucket boundaries are the histogram's
// schema, and every consumer reads the counts positionally. Retaining the
// caller's slice would let them reorder it after the fact and silently relabel
// every bucket; accepting an unsorted one would put the counts in an order the
// Record loop cannot honour, so a value would land in the first bucket it did
// not exceed rather than the smallest.
func Test_newHistogram(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		buckets []float64
		want    []float64
	}
	tests := []tc{
		{"no buckets", nil, []float64{}},
		{"an empty slice", []float64{}, []float64{}},
		{"already sorted", []float64{1, 5, 10}, []float64{1, 5, 10}},
		{"unsorted input is sorted", []float64{10, 1, 5}, []float64{1, 5, 10}},
		{"negative boundaries sort too", []float64{1, -1, 0}, []float64{-1, 0, 1}},
		{"duplicate boundaries survive", []float64{1, 1, 2}, []float64{1, 1, 2}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		in := slices.Clone(c.buckets)

		h := newHistogram(in)

		if !slices.Equal(h.bounds, c.want) {
			t.Errorf("buckets = %v, want %v", h.bounds, c.want)
		}
		//: the counts slice carries one extra slot for the +Inf overflow, or a
		//: value above every boundary would have nowhere to go.
		if len(h.counts) != len(c.want)+1 {
			t.Errorf("counts has %d slots for %d buckets, want %d",
				len(h.counts), len(c.want), len(c.want)+1)
		}
		//: the caller's slice is untouched, so a later sort or append on their
		//: side cannot relabel our buckets.
		if !slices.Equal(in, c.buckets) {
			t.Errorf("newHistogram mutated the caller's slice into %v", in)
		}
		//: and it is a copy, not the same backing array.
		if len(in) > 0 && &h.bounds[0] == &in[0] {
			t.Error("newHistogram retained the caller's backing array")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_memHistogram_Record pins the bucket selection, which is the histogram's
// entire meaning.
//
// A value belongs to the FIRST bucket whose upper bound it does not exceed, and
// the boundaries are inclusive — a value exactly equal to a bound falls inside
// it, not above. Getting either wrong shifts every observation by one bucket,
// which no total or sum would reveal.
func Test_memHistogram_Record(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		buckets []float64
		values  []float64
		//: the expected per-bucket counts, including the trailing overflow.
		want []uint64
	}
	tests := []tc{
		{"nothing recorded", []float64{1, 5}, nil, []uint64{0, 0, 0}},
		{"below the first bound", []float64{1, 5}, []float64{0.5}, []uint64{1, 0, 0}},
		//: inclusive: exactly on a bound falls INSIDE it.
		{"exactly on the first bound", []float64{1, 5}, []float64{1}, []uint64{1, 0, 0}},
		{"between two bounds", []float64{1, 5}, []float64{3}, []uint64{0, 1, 0}},
		{"exactly on the last bound", []float64{1, 5}, []float64{5}, []uint64{0, 1, 0}},
		{"above every bound overflows", []float64{1, 5}, []float64{100}, []uint64{0, 0, 1}},
		{"a spread across every slot", []float64{1, 5}, []float64{0, 3, 100}, []uint64{1, 1, 1}},
		{"a negative value lands in the first bucket", []float64{1, 5}, []float64{-1}, []uint64{1, 0, 0}},
		//: with no boundaries at all every value is an overflow.
		{"no buckets at all", nil, []float64{1, 2, 3}, []uint64{3}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := newHistogram(c.buckets)
		var sum float64
		for _, v := range c.values {
			h.Record(v)
			sum += v
		}

		snap := h.snapshot(false)
		if !slices.Equal(snap.Counts, c.want) {
			t.Fatalf("counts = %v, want %v", snap.Counts, c.want)
		}
		//: the total counts every observation, whichever bucket took it.
		if snap.Count != uint64(len(c.values)) {
			t.Errorf("Count = %d, want %d", snap.Count, len(c.values))
		}
		//: the sum is what makes an average computable; it must include the
		//: overflow observations too.
		if math.Abs(snap.Sum-sum) > 1e-9 {
			t.Errorf("Sum = %v, want %v", snap.Sum, sum)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_memHistogram_RecordDuration pins the unit. Latency histograms are read in
// SECONDS by every dashboard and alert built on them, so recording nanoseconds
// would put every observation in the overflow bucket of a conventionally
// bucketed histogram — and the sum would be off by a factor of a billion.
func Test_memHistogram_RecordDuration(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		d    time.Duration
		want float64
	}
	tests := []tc{
		{"a whole second", time.Second, 1},
		{"a millisecond", time.Millisecond, 0.001},
		{"a microsecond", time.Microsecond, 0.000001},
		{"zero", 0, 0},
		{"a compound duration", 1500 * time.Millisecond, 1.5},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: boundaries in seconds, as a latency histogram is conventionally
		//: bucketed.
		h := newHistogram([]float64{0.01, 0.1, 1, 10})

		h.RecordDuration(c.d)

		snap := h.snapshot(false)
		if math.Abs(snap.Sum-c.want) > 1e-9 {
			t.Errorf("Sum = %v seconds, want %v", snap.Sum, c.want)
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

// Test_memHistogram_snapshot pins that the exported value shares nothing with
// the live histogram. A snapshot is handed to an exporter that may hold it while
// the process keeps recording, so an aliased buckets slice or counts array would
// let the reading change under the reader.
func Test_memHistogram_snapshot(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		buckets []float64
		before  []float64
		after   []float64
	}
	tests := []tc{
		{"no further observations", []float64{1, 5}, []float64{0.5}, nil},
		{"more observations after the snapshot", []float64{1, 5}, []float64{0.5}, []float64{3, 100}},
		{"a snapshot of an empty histogram", []float64{1, 5}, nil, []float64{1}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := newHistogram(c.buckets)
		for _, v := range c.before {
			h.Record(v)
		}

		snap := h.snapshot(false)
		countsBefore := slices.Clone(snap.Counts)
		bucketsBefore := slices.Clone(snap.Bounds)

		for _, v := range c.after {
			h.Record(v)
		}

		//: the snapshot is a point in time; later observations must not reach it.
		if !slices.Equal(snap.Counts, countsBefore) {
			t.Errorf("the snapshot's counts changed to %v, want %v", snap.Counts, countsBefore)
		}
		if snap.Count != uint64(len(c.before)) {
			t.Errorf("the snapshot's Count is %d, want %d", snap.Count, len(c.before))
		}
		//: and mutating the snapshot must not reach the histogram.
		if len(snap.Bounds) > 0 {
			snap.Bounds[0] = math.MaxFloat64
			if h.bounds[0] == math.MaxFloat64 {
				t.Error("the snapshot shares its buckets with the histogram")
			}
			copy(snap.Bounds, bucketsBefore)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_memHistogram_RecordConcurrent pins that no observation is lost under
// contention. A histogram is recorded from every request handler at once, and a
// lost update would understate the very tail a latency histogram exists to show.
func Test_memHistogram_RecordConcurrent(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		goroutines int
		each       int
	}
	tests := []tc{
		{"a few recorders", 4, 250},
		{"many recorders", 16, 250},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := newHistogram([]float64{1, 5, 10})

		//: Goroutine lifecycle: c.goroutines goroutines, each recording a fixed
		//: number of times and returning; the WaitGroup joins them all.
		var wg sync.WaitGroup
		for range c.goroutines {
			wg.Go(func() {
				for range c.each {
					h.Record(1)
				}
			})
		}
		wg.Wait()

		snap := h.snapshot(false)
		want := uint64(c.goroutines * c.each)
		if snap.Count != want {
			t.Errorf("Count = %d, want %d", snap.Count, want)
		}
		//: every observation of 1 lands in the first bucket, inclusive.
		if snap.Counts[0] != want {
			t.Errorf("the first bucket holds %d, want %d", snap.Counts[0], want)
		}
		//: and the CAS sum kept up with them.
		if math.Abs(snap.Sum-float64(want)) > 1e-9 {
			t.Errorf("Sum = %v, want %v", snap.Sum, float64(want))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
