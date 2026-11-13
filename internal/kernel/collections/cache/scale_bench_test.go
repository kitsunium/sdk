package cache

import (
	"math/rand/v2"
	"runtime/debug"
	"sync"
	"testing"
	"time"
)

// scaleKeyCount is how many distinct keys the 10M rows insert before their
// cache is rebuilt, outside the timer.
const scaleKeyCount int = 10_000_000

// scaleHitCount is how many entries the 1M row reads from.
const scaleHitCount int = 1 << 20

// scaleKeys are built once per binary: ten million strings take seconds, and
// the bench function runs twice (b.N = 1, then the -benchtime).
var scaleKeys = sync.OnceValue(func() []string { return benchKeys(scaleKeyCount) })

// BenchmarkSet_NewKey_10M is Set_NewKey at scale (#1038): ten million distinct
// keys into one unbounded cache without a TTL, the map growing all the way and
// every node a fresh allocation the collector has to walk — where the node's
// size class shows, which a cache rebuilt every 4096 keys hides. It reports
// the collections a write costs. The rows of the bench A/B lane's scale mode
// (`-benchtime=10000000x`), about 1.5 GB: never on a shared machine.
func BenchmarkSet_NewKey_10M(b *testing.B) {
	benchSetNewKeyScale(b, 0)
}

// BenchmarkSet_NewKey_TTL_10M is the same with a TTL of an hour: every insert
// also pushes onto the expiry heap.
func BenchmarkSet_NewKey_TTL_10M(b *testing.B) {
	benchSetNewKeyScale(b, time.Hour)
}

// benchSetNewKeyScale inserts the scale keys in order, rebuilding the cache
// outside the timer once they are all in.
func benchSetNewKeyScale(b *testing.B, ttl time.Duration) {
	keys := scaleKeys()
	c := NewCache[string, int](Config[string, int]{DefaultTTL: ttl})
	var before, after debug.GCStats
	debug.ReadGCStats(&before)
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		//: every key is in: a fresh cache, so each write stays a new key.
		if i == len(keys) {
			b.StopTimer()
			c = NewCache[string, int](Config[string, int]{DefaultTTL: ttl})
			i = 0
			b.StartTimer()
		}
		c.Set(keys[i], i)
		i++
	}
	b.StopTimer()
	debug.ReadGCStats(&after)
	b.ReportMetric(float64(after.NumGC-before.NumGC)/float64(b.N), "gc-cycles/op")
	benchCache = c
}

// BenchmarkFetch_HitWithTTL_1M is Fetch_HitWithTTL over a million entries read
// in a uniform random order computed beforehand: most nodes are cold, so a hit
// pays the lines its node spans, which 1024 keys held in L1 never show.
func BenchmarkFetch_HitWithTTL_1M(b *testing.B) {
	keys := scaleKeys()[:scaleHitCount]
	c := NewCache[string, int](Config[string, int]{MaxEntries: 2 * scaleHitCount, DefaultTTL: time.Hour})
	for i, key := range keys {
		c.Set(key, i)
	}
	rng := rand.New(rand.NewPCG(1038, 1))
	order := make([]int32, 1<<24)
	for i := range order {
		order[i] = int32(rng.IntN(scaleHitCount))
	}
	b.ReportAllocs()
	b.ResetTimer()
	i := 0
	for b.Loop() {
		key := keys[order[i&(len(order)-1)]]
		v, ok := c.Fetch(key)
		if !ok {
			b.Fatalf("miss on seeded key %q", key)
		}
		benchInt = v
		i++
	}
}
