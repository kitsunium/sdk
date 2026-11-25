package frozen_test

import (
	"math/rand/v2"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/collections/frozen"
)

// benchSizes are the key counts every benchmark runs at: a table in L1, one in
// L2, and one whose records only the last level cache and memory hold.
var benchSizes = []int{1_000, 100_000, 1_000_000}

// benchSink keeps the lookups' results alive.
var benchSink uint64

// benchSet is one size's keys, values and probes: the probes are the keys in a
// shuffled order and, for strings, in allocations of their own, as a caller's
// input would be — a probe sharing its bytes with the stored key would let a
// native map's comparison stop at the pointers.
type benchSet struct {
	ints, intValues []uint64
	intMisses       []uint64
	strs            []string
	strProbes       []string
	strMisses       []string
}

// newBenchSet draws n distinct uint64 keys, n distinct strings of 8 to 13
// bytes, and n absent keys of each.
func newBenchSet(n int) *benchSet {
	r := rand.New(rand.NewPCG(uint64(n), 7))
	s := &benchSet{}
	seenInt, seenStr := make(map[uint64]bool, 2*n), make(map[string]bool, 2*n)
	for len(s.ints) < n {
		if k := r.Uint64(); !seenInt[k] {
			seenInt[k] = true
			s.ints, s.intValues = append(s.ints, k), append(s.intValues, r.Uint64())
		}
	}
	for len(s.intMisses) < n {
		if k := r.Uint64(); !seenInt[k] {
			seenInt[k] = true
			s.intMisses = append(s.intMisses, k)
		}
	}
	str := func() string { return "k/" + strconv.FormatUint(r.Uint64()>>12, 36) }
	for len(s.strs) < n {
		if k := str(); !seenStr[k] {
			seenStr[k] = true
			s.strs = append(s.strs, k)
		}
	}
	for len(s.strMisses) < n {
		if k := str(); !seenStr[k] {
			seenStr[k] = true
			s.strMisses = append(s.strMisses, k)
		}
	}
	for _, k := range s.strs {
		s.strProbes = append(s.strProbes, strings.Clone(k))
	}
	r.Shuffle(n, func(i, j int) { s.strProbes[i], s.strProbes[j] = s.strProbes[j], s.strProbes[i] })
	return s
}

// lookups runs get over keys, round robin, as the benchmark's loop.
func lookups[K any](b *testing.B, get func(K) (uint64, bool), keys []K) {
	runtime.GC()
	var sum uint64
	for i := 0; b.Loop(); i++ {
		if v, ok := get(keys[i%len(keys)]); ok {
			sum += v | 1
		}
	}
	benchSink = sum
}

func BenchmarkGet(b *testing.B) {
	for _, n := range benchSizes {
		b.Run("n="+strconv.Itoa(n), func(b *testing.B) {
			s := newBenchSet(n)
			probes := append([]uint64(nil), s.ints...)
			rand.New(rand.NewPCG(1, 2)).Shuffle(n, func(i, j int) { probes[i], probes[j] = probes[j], probes[i] })
			strValues := make([]uint64, n)
			for i := range strValues {
				strValues[i] = uint64(i)
			}
			b.Run("key=uint64/impl=frozen", func(b *testing.B) {
				m, err := frozen.NewMap(s.ints, s.intValues)
				if err != nil {
					b.Fatal(err)
				}
				b.Run("probe=hit", func(b *testing.B) { lookups(b, m.Get, probes) })
				b.Run("probe=miss", func(b *testing.B) { lookups(b, m.Get, s.intMisses) })
			})
			b.Run("key=uint64/impl=native", func(b *testing.B) {
				m := make(map[uint64]uint64, n)
				for i, k := range s.ints {
					m[k] = s.intValues[i]
				}
				get := func(k uint64) (uint64, bool) { v, ok := m[k]; return v, ok }
				b.Run("probe=hit", func(b *testing.B) { lookups(b, get, probes) })
				b.Run("probe=miss", func(b *testing.B) { lookups(b, get, s.intMisses) })
			})
			b.Run("key=string/impl=frozen", func(b *testing.B) {
				m, err := frozen.NewStringMap(s.strs, strValues)
				if err != nil {
					b.Fatal(err)
				}
				b.Run("probe=hit", func(b *testing.B) { lookups(b, m.Get, s.strProbes) })
				b.Run("probe=miss", func(b *testing.B) { lookups(b, m.Get, s.strMisses) })
			})
			b.Run("key=string/impl=native", func(b *testing.B) {
				m := make(map[string]uint64, n)
				for i, k := range s.strs {
					m[k] = strValues[i]
				}
				get := func(k string) (uint64, bool) { v, ok := m[k]; return v, ok }
				b.Run("probe=hit", func(b *testing.B) { lookups(b, get, s.strProbes) })
				b.Run("probe=miss", func(b *testing.B) { lookups(b, get, s.strMisses) })
			})
		})
	}
}

func BenchmarkBuild(b *testing.B) {
	for _, n := range benchSizes {
		b.Run("n="+strconv.Itoa(n), func(b *testing.B) {
			s := newBenchSet(n)
			strValues := make([]uint64, n)
			b.Run("key=uint64/impl=frozen", func(b *testing.B) {
				for b.Loop() {
					if _, err := frozen.NewMap(s.ints, s.intValues); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("key=uint64/impl=native", func(b *testing.B) {
				for b.Loop() {
					m := make(map[uint64]uint64, n)
					for i, k := range s.ints {
						m[k] = s.intValues[i]
					}
				}
			})
			b.Run("key=string/impl=frozen", func(b *testing.B) {
				for b.Loop() {
					if _, err := frozen.NewStringMap(s.strs, strValues); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("key=string/impl=native", func(b *testing.B) {
				for b.Loop() {
					m := make(map[string]uint64, n)
					for i, k := range s.strs {
						m[k] = strValues[i]
					}
				}
			})
		})
	}
}

func BenchmarkLoad(b *testing.B) {
	for _, n := range benchSizes {
		b.Run("n="+strconv.Itoa(n), func(b *testing.B) {
			s := newBenchSet(n)
			m, err := frozen.NewStringMap(s.strs, make([]uint64, n))
			if err != nil {
				b.Fatal(err)
			}
			image, _ := m.MarshalBinary()
			for _, check := range []frozen.Check{frozen.CheckHeader, frozen.CheckAll} {
				b.Run("check="+[]string{"header", "all"}[check], func(b *testing.B) {
					b.SetBytes(int64(len(image)))
					for b.Loop() {
						if _, err := frozen.LoadStringMap[uint64](image, check); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}
