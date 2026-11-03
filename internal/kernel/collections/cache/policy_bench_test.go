package cache

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// zipfKeys is the key space the hit-ratio benchmarks draw from.
const zipfKeys int = 1_000_000

// zipfTraceLen is how many accesses each trace holds; a benchmark that runs
// longer cycles through it.
const zipfTraceLen int = 2_000_000

// traces caches one trace per exponent: building one costs a second, and
// every policy and capacity reads the same accesses.
var (
	tracesMu sync.Mutex
	traces   = map[float64][]int{}
)

// zipfTrace returns zipfTraceLen ranks in [0, zipfKeys) drawn with
// P(rank k) ∝ 1/(k+1)^s. math/rand/v2's Zipf requires s > 1; the exponents
// that matter for caches are around and below 1, so the CDF is built here and
// inverted by binary search.
func zipfTrace(s float64) []int {
	tracesMu.Lock()
	defer tracesMu.Unlock()
	//: built once per exponent.
	if trace, ok := traces[s]; ok {
		return trace
	}
	cdf := make([]float64, zipfKeys)
	total := 0.0
	for k := range cdf {
		total += 1 / math.Pow(float64(k+1), s)
		cdf[k] = total
	}
	random := rand.New(rand.NewPCG(uint64(s*1000), 42))
	trace := make([]int, zipfTraceLen)
	for i := range trace {
		rank, _ := slices.BinarySearch(cdf, random.Float64()*total)
		trace[i] = min(rank, zipfKeys-1)
	}
	traces[s] = trace
	return trace
}

// benchPolicies names the policies the hit-ratio benchmarks compare.
var benchPolicies = []struct {
	name   string
	policy Policy
}{{"lru", PolicyLRU}, {"s3fifo", PolicyS3FIFO}}

// BenchmarkHitRatio reports, as hit/op, the share of accesses a cache of 1 % or
// 10 % of a million keys answers under a Zipf distribution, after a warm-up of
// twice its capacity: a read is a Fetch, a miss is followed by the Set a
// read-through cache makes. ns/op is the cost of one such access.
func BenchmarkHitRatio(b *testing.B) {
	for _, policy := range benchPolicies {
		for _, s := range []float64{0.8, 1.0, 1.2} {
			for _, share := range []int{1, 10} {
				name := fmt.Sprintf("%s/zipf-s%.1f/cap%d%%", policy.name, s, share)
				b.Run(name, func(b *testing.B) {
					trace := zipfTrace(s)
					capacity := zipfKeys * share / 100
					c := NewCache[int, struct{}](Config[int, struct{}]{MaxEntries: capacity, Policy: policy.policy})
					access := func(key int) bool {
						if _, ok := c.Fetch(key); ok {
							return true
						}
						c.Set(key, struct{}{})
						return false
					}
					warm := 2 * capacity
					for i := range warm {
						access(trace[i%zipfTraceLen])
					}
					b.ReportAllocs()
					b.ResetTimer()
					hits, ops := 0, 0
					for b.Loop() {
						if access(trace[(warm+ops)%zipfTraceLen]) {
							hits++
						}
						ops++
					}
					b.ReportMetric(float64(hits)/float64(ops), "hit/op")
				})
			}
		}
	}
}

// BenchmarkHitRatio_Scan is the scan-resistance case: Zipf phases (s = 1.0) of
// four times the capacity, each followed by a sequential sweep of twice the
// capacity over keys never seen before — a listing, a backfill. hit/op counts
// the Zipf accesses only (the sweep cannot hit by construction), so it says
// how much of the hot set each policy keeps through the sweeps.
func BenchmarkHitRatio_Scan(b *testing.B) {
	for _, policy := range benchPolicies {
		b.Run(policy.name+"/cap10%", func(b *testing.B) {
			trace := zipfTrace(1.0)
			capacity := zipfKeys / 10
			c := NewCache[int, struct{}](Config[int, struct{}]{MaxEntries: capacity, Policy: policy.policy})
			phase, sweep := 4*capacity, 2*capacity
			cycle := phase + sweep
			fresh := zipfKeys
			access := func(key int) bool {
				if _, ok := c.Fetch(key); ok {
					return true
				}
				c.Set(key, struct{}{})
				return false
			}
			b.ReportAllocs()
			b.ResetTimer()
			hits, zipfOps, ops := 0, 0, 0
			for b.Loop() {
				if ops%cycle < phase {
					if access(trace[zipfOps%zipfTraceLen]) {
						hits++
					}
					zipfOps++
				} else {
					access(fresh)
					fresh++
				}
				ops++
			}
			b.ReportMetric(float64(hits)/float64(max(zipfOps, 1)), "hit/op")
		})
	}
}

// BenchmarkFetch_Hit_S3FIFO is Fetch_Hit under S3-FIFO: a hit sets a counter
// instead of relinking the entry.
func BenchmarkFetch_Hit_S3FIFO(b *testing.B) {
	keys := benchKeys(1024)
	c := NewCache[string, int](Config[string, int]{MaxEntries: 2048, Policy: PolicyS3FIFO})
	for i, k := range keys {
		c.Set(k, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		v, ok := c.Fetch(keys[i&1023])
		if !ok {
			b.Fatalf("miss on seeded key %q", keys[i&1023])
		}
		benchInt = v
		i++
	}
}

// BenchmarkSet_AtCapacity_S3FIFO is Set_AtCapacity under S3-FIFO: each insert
// past the bound walks probation and, for a key evicted without a hit,
// remembers its hash in the ghost.
func BenchmarkSet_AtCapacity_S3FIFO(b *testing.B) {
	keys := benchKeys(4096)
	c := NewCache[string, int](Config[string, int]{MaxEntries: 512, Policy: PolicyS3FIFO})
	for i, k := range keys[:512] {
		c.Set(k, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		c.Set(keys[i&4095], i)
		i++
	}
}

// BenchmarkSet_NewKey_WeightOf is Set_NewKey with a WeightOf: the delta is the
// weigher's call and the sums it feeds.
func BenchmarkSet_NewKey_WeightOf(b *testing.B) {
	keys := benchKeys(4096)
	weigh := func(key string, _ int) int64 { return int64(len(key)) }
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		if i&4095 == 0 {
			b.StopTimer()
			benchCache = NewCache[string, int](Config[string, int]{MaxWeight: 1 << 30, WeightOf: weigh})
			b.StartTimer()
		}
		benchCache.Set(keys[i&4095], i)
		i++
	}
}

// BenchmarkExpire_10kEntries_1pctExpired sweeps a cache of 10 000 entries that
// all carry a deadline, 100 of which have passed: the sweep pops those 100 off
// the expiry heap and never visits the other 9 900. The re-arming of the 100
// between sweeps is outside the timer.
func BenchmarkExpire_10kEntries_1pctExpired(b *testing.B) {
	keys := benchKeys(10_000)
	clk := clock.NewManualClock(time.Unix(0, 0))
	c := NewCache[string, int](Config[string, int]{Clock: clk})
	for i, k := range keys[100:] {
		c.SetTTL(k, i, 1000*time.Hour)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		b.StopTimer()
		for i, k := range keys[:100] {
			c.SetTTL(k, i, time.Second)
		}
		clk.Advance(2 * time.Second)
		b.StartTimer()
		if reaped := c.Expire(); reaped != 100 {
			b.Fatalf("Expire() = %d, want 100", reaped)
		}
	}
}

// BenchmarkExpire_NothingDue is a sweep with nothing to do on a cache whose
// entries carry no deadline: a lock round trip, no clock read.
func BenchmarkExpire_NothingDue(b *testing.B) {
	c := NewCache[string, int](Config[string, int]{MaxEntries: 1024})
	for i := range 1024 {
		c.Set(strconv.Itoa(i), i)
	}
	b.ReportAllocs()
	for b.Loop() {
		benchEvicted += c.Expire()
	}
}

// BenchmarkFetch_HitWithTTL_Coarse is Fetch_HitWithTTL with a
// clock.CoarseClock as the cache's clock (#814): the expiry check reads an
// atomic instead of the wall clock, and expiry is late by up to the
// resolution.
func BenchmarkFetch_HitWithTTL_Coarse(b *testing.B) {
	keys := benchKeys(1024)
	coarse := clock.NewCoarse(clock.System, time.Millisecond)
	defer coarse.Stop()
	c := NewCache[string, int](Config[string, int]{MaxEntries: 2048, DefaultTTL: time.Hour, Clock: coarse})
	for i, k := range keys {
		c.Set(k, i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		v, ok := c.Fetch(keys[i&1023])
		if !ok {
			b.Fatalf("miss on seeded key %q", keys[i&1023])
		}
		benchInt = v
		i++
	}
}
