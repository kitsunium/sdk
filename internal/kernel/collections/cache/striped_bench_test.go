package cache

import (
	"sync/atomic"
	"testing"
)

// benchStripes is the stripe count the parallel rows compare against one
// Cache: a power of two above the core counts measured.
const benchStripes int = 16

// BenchmarkStriped_Fetch_Parallel is Fetch_Parallel over a Striped(16): the
// readers of different stripes take different locks. Run it with -cpu=1,2,4,8
// beside Fetch_Parallel to read the scaling.
func BenchmarkStriped_Fetch_Parallel(b *testing.B) {
	keys := benchKeys(1024)
	s := NewStriped(Config[string, int]{MaxEntries: 2048}, benchStripes, nil)
	for i, k := range keys {
		s.Set(k, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	var offset atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		//: each goroutine starts at its own place in the key set, so they do
		//: not walk it in lockstep and meet on the same keys.
		i := int(offset.Add(131))
		for pb.Next() {
			if _, ok := s.Fetch(keys[i&1023]); !ok {
				b.Errorf("miss on seeded key")
				return
			}
			i++
		}
	})
}

// BenchmarkStriped_Fetch_Hit is the single-goroutine hit through a Striped:
// the delta against Fetch_Hit is the key's hash.
func BenchmarkStriped_Fetch_Hit(b *testing.B) {
	keys := benchKeys(1024)
	s := NewStriped(Config[string, int]{MaxEntries: 2048}, benchStripes, nil)
	for i, k := range keys {
		s.Set(k, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		v, ok := s.Fetch(keys[i&1023])
		if !ok {
			b.Fatalf("miss on seeded key %q", keys[i&1023])
		}
		benchInt = v
		i++
	}
}

// BenchmarkSet_Parallel overwrites seeded keys from every goroutine on one
// Cache: writers serialise on its one lock.
func BenchmarkSet_Parallel(b *testing.B) {
	keys := benchKeys(1024)
	c := NewCache[string, int](Config[string, int]{MaxEntries: 2048})
	for i, k := range keys {
		c.Set(k, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	var offset atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		//: each goroutine starts at its own place in the key set, so they do
		//: not walk it in lockstep and meet on the same keys.
		i := int(offset.Add(131))
		for pb.Next() {
			c.Set(keys[i&1023], i)
			i++
		}
	})
}

// BenchmarkStriped_Set_Parallel is Set_Parallel over a Striped(16).
func BenchmarkStriped_Set_Parallel(b *testing.B) {
	keys := benchKeys(1024)
	s := NewStriped(Config[string, int]{MaxEntries: 2048}, benchStripes, nil)
	for i, k := range keys {
		s.Set(k, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	var offset atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		//: each goroutine starts at its own place in the key set, so they do
		//: not walk it in lockstep and meet on the same keys.
		i := int(offset.Add(131))
		for pb.Next() {
			s.Set(keys[i&1023], i)
			i++
		}
	})
}
