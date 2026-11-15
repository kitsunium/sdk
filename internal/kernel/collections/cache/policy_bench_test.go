package cache

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// zipfKeys is the key space the hit-ratio benchmarks draw from.
const zipfKeys int = 1_000_000

// hitWarmup is the warm-up of a hit-ratio cell, in capacities: the accesses
// run before the measurement starts.
const hitWarmup int = 2

// shiftEvery is how many accesses the shifting cell keeps one order of
// popularity: then every rank moves to the key a tenth of the space further.
const shiftEvery int = 10_000_000

// loopShare is the loop cell's cycle against the capacity, in fifths: a
// sequential cycle over six fifths of the capacity, which no policy that
// keeps the most recent keys ever hits.
const loopShare int = 6

// zipfTable draws ranks in [0, zipfKeys) with P(rank k) ∝ 1/(k+1)^s, in
// constant time: Vose's alias table, one bucket a rank, each bucket keeping
// its rank with the probability its threshold says (scaled to 2^32) and
// handing over to its alias otherwise. math/rand/v2's Zipf requires s > 1,
// and the exponents that matter for caches are around and below 1.
type zipfTable struct {
	threshold []uint32
	alias     []int32
}

// zipfTables caches one table per exponent: every policy and capacity draws
// from the same one.
var (
	zipfTablesMu sync.Mutex
	zipfTables   = map[float64]*zipfTable{}
)

// zipfTableFor returns the table of exponent s, built once.
func zipfTableFor(s float64) *zipfTable {
	zipfTablesMu.Lock()
	defer zipfTablesMu.Unlock()
	if t, ok := zipfTables[s]; ok {
		return t
	}
	t := newZipfTable(zipfKeys, s)
	zipfTables[s] = t
	return t
}

// newZipfTable builds the alias table of n ranks of exponent s.
func newZipfTable(n int, s float64) *zipfTable {
	weights := make([]float64, n)
	total := 0.0
	for k := range weights {
		weights[k] = math.Pow(float64(k+1), -s)
		total += weights[k]
	}
	t := &zipfTable{threshold: make([]uint32, n), alias: make([]int32, n)}
	small := make([]int32, 0, n)
	large := make([]int32, 0, n)
	for k, w := range weights {
		weights[k] = w * float64(n) / total
		if weights[k] < 1 {
			small = append(small, int32(k))
		} else {
			large = append(large, int32(k))
		}
	}
	for len(small) > 0 && len(large) > 0 {
		less := small[len(small)-1]
		small = small[:len(small)-1]
		more := large[len(large)-1]
		large = large[:len(large)-1]
		t.threshold[less] = uint32(weights[less] * (1 << 32))
		t.alias[less] = more
		weights[more] += weights[less] - 1
		if weights[more] < 1 {
			small = append(small, more)
		} else {
			large = append(large, more)
		}
	}
	//: what is left keeps its bucket: a rounding's remainder.
	for _, k := range append(large, small...) {
		t.threshold[k], t.alias[k] = math.MaxUint32, k
	}
	return t
}

// draw maps one 64-bit random word to a rank: its high half picks the bucket,
// its low half decides between the bucket and its alias.
func (t *zipfTable) draw(u uint64) int {
	bucket := int32((u >> 32) * uint64(len(t.alias)) >> 32)
	if uint32(u) < t.threshold[bucket] {
		return int(bucket)
	}
	return int(t.alias[bucket])
}

// hitWorkload is the shape of a hit-ratio cell's accesses.
type hitWorkload uint8

const (
	// workloadZipf draws every access from the Zipf law: the independent
	// reference model, under which keeping the C most popular keys is the
	// best any policy can do.
	workloadZipf hitWorkload = iota
	// workloadScan alternates Zipf phases (s = 1.0) of four capacities with
	// sweeps of two capacities over keys never seen before — a listing, a
	// backfill; only the Zipf accesses are counted.
	workloadScan
	// workloadLoop cycles over loopShare fifths of the capacity in order:
	// LRU's worst case, against the over-fitting of a policy to Zipf.
	workloadLoop
	// workloadShift draws from the Zipf law (s = 1.0) and moves every rank a
	// tenth of the key space further every shiftEvery accesses: popularity
	// that changes, against the over-fitting of a policy to a fixed order.
	workloadShift
)

// hitCell is one workload of the hit-ratio benchmark, at the capacities it
// runs at, in percent of zipfKeys.
type hitCell struct {
	name     string
	workload hitWorkload
	s        float64
	shares   []int
}

// hitCells are the cells every policy runs: the issue's Zipf grid, then the
// scan, the loop and the shifting popularity at a tenth of the keys (#834).
var hitCells = []hitCell{
	{"zipf-s0.8", workloadZipf, 0.8, []int{1, 10}},
	{"zipf-s1.0", workloadZipf, 1.0, []int{1, 10}},
	{"zipf-s1.2", workloadZipf, 1.2, []int{1, 10}},
	{"scan", workloadScan, 1.0, []int{10}},
	{"loop", workloadLoop, 0, []int{10}},
	{"shift", workloadShift, 1.0, []int{10}},
}

// hitTrace generates a cell's accesses on the fly from a PCG of fixed seed,
// so a cell is the same sequence on every run and for every policy: the hit
// ratio of a policy on it is a property of the policy, not of the run.
type hitTrace struct {
	cell     hitCell
	capacity int
	table    *zipfTable
	random   *rand.PCG
	at       int
	fresh    int
}

// newHitTrace starts cell's sequence for a cache of capacity entries.
func newHitTrace(cell hitCell, capacity int) *hitTrace {
	trace := &hitTrace{cell: cell, capacity: capacity, random: rand.NewPCG(uint64(cell.s*1000), 834), fresh: zipfKeys}
	if cell.workload != workloadLoop {
		trace.table = zipfTableFor(cell.s)
	}
	return trace
}

// next returns the next key and whether the hit ratio counts it.
func (t *hitTrace) next() (key int, counted bool) {
	at := t.at
	t.at++
	switch t.cell.workload {
	case workloadScan:
		phase, sweep := 4*t.capacity, 2*t.capacity
		if at%(phase+sweep) >= phase {
			t.fresh++
			return t.fresh - 1, false
		}
		return t.table.draw(t.random.Uint64()), true
	case workloadLoop:
		return at % (t.capacity * loopShare / 5), true
	case workloadShift:
		rank := t.table.draw(t.random.Uint64())
		return (rank + at/shiftEvery*(zipfKeys/10)) % zipfKeys, true
	default:
		return t.table.draw(t.random.Uint64()), true
	}
}

// benchPolicies names the policies the hit-ratio benchmarks compare.
var benchPolicies = []struct {
	name   string
	policy Policy
}{{"lru", PolicyLRU}, {"s3fifo", PolicyS3FIFO}, {"tinylfu", PolicyTinyLFU}}

// BenchmarkHitRatio reports, as hit/op, the share of a cell's counted
// accesses a cache of 1 % or 10 % of a million keys answers, after a warm-up
// of hitWarmup capacities: a read is a Fetch, a miss is followed by the Set a
// read-through cache makes. ns/op is the cost of one access. The protocol of
// #834 measures 100 million accesses a cell — -benchtime=100000000x — and the
// rows are named <policy>/<workload>/cap<share>%.
func BenchmarkHitRatio(b *testing.B) {
	for _, policy := range benchPolicies {
		for _, cell := range hitCells {
			for _, share := range cell.shares {
				name := fmt.Sprintf("%s/%s/cap%d%%", policy.name, cell.name, share)
				b.Run(name, func(b *testing.B) {
					capacity := zipfKeys * share / 100
					c := NewCache[int, struct{}](Config[int, struct{}]{MaxEntries: capacity, Policy: policy.policy})
					trace := newHitTrace(cell, capacity)
					access := func(key int) bool {
						if _, ok := c.Fetch(key); ok {
							return true
						}
						c.Set(key, struct{}{})
						return false
					}
					for range hitWarmup * capacity {
						key, _ := trace.next()
						access(key)
					}
					b.ReportAllocs()
					hits, ops := 0, 0
					for b.Loop() {
						key, counted := trace.next()
						hit := access(key)
						if counted {
							ops++
							if hit {
								hits++
							}
						}
					}
					b.ReportMetric(float64(hits)/float64(max(ops, 1)), "hit/op")
				})
			}
		}
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

// BenchmarkFetch_Hit_TinyLFU is Fetch_Hit under W-TinyLFU: a hit counts its
// key in the frequency sketch — four counters in one cache line, off the
// hash the entry keeps — and relinks the entry in its segment, as LRU does.
func BenchmarkFetch_Hit_TinyLFU(b *testing.B) {
	keys := benchKeys(1024)
	c := NewCache[string, int](Config[string, int]{MaxEntries: 2048, Policy: PolicyTinyLFU})
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

// BenchmarkSet_AtCapacity_TinyLFU is Set_AtCapacity under W-TinyLFU: each
// insert past the bound hashes the new key, counts it, and settles the
// window's oldest against main's victim by their counts.
func BenchmarkSet_AtCapacity_TinyLFU(b *testing.B) {
	keys := benchKeys(4096)
	c := NewCache[string, int](Config[string, int]{MaxEntries: 512, Policy: PolicyTinyLFU})
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
