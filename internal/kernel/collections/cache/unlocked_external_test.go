package cache_test

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/collections/cache"
)

// evictionLog records what OnEvict saw, in order.
type evictionLog struct{ seen []string }

// record is an OnEvict observer appending key=val.
func (l *evictionLog) record(key string, val []byte) {
	l.seen = append(l.seen, key+"="+string(val))
}

// TestUnlockedMapBehavesAsCache is the conformance of UnlockedMap: every behavioural
// table of this package runs on Cache, which is an UnlockedMap under a mutex, and
// an UnlockedMap driven by the same operations must answer as that Cache answers.
// Each case drives both through one seeded sequence of Set, SetTTL, Fetch,
// Delete, Expire, Purge and clock moves, under every policy, and compares at
// every step what a caller can observe: the value and hit of every Fetch, the
// count Expire returns, Len, Stats, the keys Keys yields and every OnEvict.
func TestUnlockedMapBehavesAsCache(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		cfg  cache.Config[string, []byte]
	}
	tests := []tc{
		{name: "unbounded, no deadline", cfg: cache.Config[string, []byte]{}},
		{name: "a count bound", cfg: cache.Config[string, []byte]{MaxEntries: 16}},
		{name: "a weight bound", cfg: cache.Config[string, []byte]{MaxWeight: 120, WeightOf: byLength}},
		{name: "a default TTL and a count bound", cfg: cache.Config[string, []byte]{MaxEntries: 24, DefaultTTL: 3 * time.Second}},
		{name: "every option", cfg: cache.Config[string, []byte]{MaxEntries: 20, MaxWeight: 150, WeightOf: byLength, DefaultTTL: 5 * time.Second}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
			for seed := range uint64(4) {
				clk := &fakeClock{now: time.Unix(1_700_000_000, 0)}
				var lockedLog, unlockedLog evictionLog
				cfg := c.cfg
				cfg.Policy, cfg.Clock = policy, clk
				cfg.OnEvict = lockedLog.record
				locked := cache.NewCache(cfg)
				cfg.OnEvict = unlockedLog.record
				unlocked := cache.NewUnlockedMap(cfg)
				rng := rand.New(rand.NewPCG(seed, 1038))
				for step := range 3000 {
					key := "k" + strconv.Itoa(rng.IntN(40))
					val := []byte(strconv.Itoa(step) + string(make([]byte, rng.IntN(12))))
					switch op := rng.IntN(100); {
					case op < 30:
						locked.Set(key, val)
						unlocked.Set(key, val)
					case op < 45:
						ttl := time.Duration(rng.IntN(4000)-500) * time.Millisecond
						locked.SetTTL(key, val, ttl)
						unlocked.SetTTL(key, val, ttl)
					case op < 80:
						lv, lok := locked.Fetch(key)
						uv, uok := unlocked.Fetch(key)
						if lok != uok || string(lv) != string(uv) {
							t.Fatalf("seed %d step %d: Fetch(%q) = %q, %v on UnlockedMap, %q, %v on Cache", seed, step, key, uv, uok, lv, lok)
						}
					case op < 88:
						locked.Delete(key)
						unlocked.Delete(key)
					case op < 93:
						if l, u := locked.Expire(), unlocked.Expire(); l != u {
							t.Fatalf("seed %d step %d: Expire = %d on UnlockedMap, %d on Cache", seed, step, u, l)
						}
					case op < 94:
						locked.Purge()
						unlocked.Purge()
					default:
						clk.advance(time.Duration(rng.IntN(900)) * time.Millisecond)
					}
					assertSameState(t, locked, unlocked, &lockedLog, &unlockedLog)
				}
			}
		})
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// assertSameState fails when the two caches differ in anything a caller can
// read without moving them: Len, Stats, the keys and the evictions seen.
func assertSameState(t *testing.T, locked *cache.Cache[string, []byte], unlocked *cache.UnlockedMap[string, []byte], lockedLog, unlockedLog *evictionLog) {
	t.Helper()
	if l, u := locked.Len(), unlocked.Len(); l != u {
		t.Fatalf("Len = %d on UnlockedMap, %d on Cache", u, l)
	}
	if l, u := locked.Stats(), unlocked.Stats(); l != u {
		t.Fatalf("Stats = %+v on UnlockedMap, %+v on Cache", u, l)
	}
	lockedKeys := slices.Sorted(locked.Keys())
	if unlockedKeys := slices.Sorted(unlocked.Keys()); !slices.Equal(lockedKeys, unlockedKeys) {
		t.Fatalf("Keys = %v on UnlockedMap, %v on Cache", unlockedKeys, lockedKeys)
	}
	if !slices.Equal(lockedLog.seen, unlockedLog.seen) {
		t.Fatalf("OnEvict saw %v on UnlockedMap, %v on Cache", unlockedLog.seen, lockedLog.seen)
	}
}

// TestUnlockedMapOnEvictMayReenter pins that an UnlockedMap hands an evicted pair to
// OnEvict only once the call that evicted it left the cache consistent: the
// observer may call back into the cache it observes, and finds the victim
// gone and the entry just written present.
func TestUnlockedMapOnEvictMayReenter(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		drive func(c *cache.UnlockedMap[string, int], clk *fakeClock)
		want  string
	}
	tests := []tc{
		{name: "an eviction under capacity pressure", want: "a", drive: func(c *cache.UnlockedMap[string, int], _ *fakeClock) {
			c.Set("a", 1)
			c.Set("b", 2)
		}},
		{name: "an entry reaped lazily on expiry", want: "a", drive: func(c *cache.UnlockedMap[string, int], clk *fakeClock) {
			c.SetTTL("a", 1, time.Second)
			clk.advance(time.Second)
			c.Fetch("a")
		}},
		{name: "an entry reaped by a sweep", want: "a", drive: func(c *cache.UnlockedMap[string, int], clk *fakeClock) {
			c.SetTTL("a", 1, time.Second)
			clk.advance(2 * time.Second)
			c.Expire()
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
			clk := &fakeClock{now: time.Unix(0, 0)}
			var seen []string
			var store *cache.UnlockedMap[string, int]
			store = cache.NewUnlockedMap(cache.Config[string, int]{MaxEntries: 1, Clock: clk, Policy: policy, OnEvict: func(key string, _ int) {
				//: the victim is gone, and the cache answers a call.
				if _, ok := store.Fetch(key); ok {
					t.Errorf("OnEvict(%q) found its victim still present", key)
				}
				store.Len()
				seen = append(seen, key)
			}})
			c.drive(store, clk)
			if !slices.Equal(seen, []string{c.want}) {
				t.Errorf("OnEvict saw %v, want [%s]", seen, c.want)
			}
		})
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestADeadlineSurvivesTheClockBeforeTheEpoch pins the deadline's arithmetic
// at its edges (#1038): a deadline is nanoseconds since the cache's first
// one, so a clock moved back before that instant, and a TTL of centuries,
// must keep the meaning a time.Time deadline had — expired once the clock
// reaches it, never sooner.
func TestADeadlineSurvivesTheClockBeforeTheEpoch(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// back moves the clock before the second write, from the epoch.
		back time.Duration
		ttl  time.Duration
		// after is how far past the second write the clock is read.
		after time.Duration
		want  bool
	}
	tests := []tc{
		{name: "a deadline before the epoch, not reached", back: -time.Hour, ttl: time.Minute, after: time.Minute - 1, want: true},
		{name: "a deadline before the epoch, reached", back: -time.Hour, ttl: time.Minute, after: time.Minute, want: false},
		{name: "a deadline exactly at the epoch, not reached", back: -time.Minute, ttl: time.Minute, after: time.Minute - 1, want: true},
		{name: "a deadline exactly at the epoch, reached", back: -time.Minute, ttl: time.Minute, after: time.Minute, want: false},
		{name: "a TTL past the int64 range holds", back: 0, ttl: time.Duration(1<<63 - 1), after: 100 * 365 * 24 * time.Hour, want: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
			clk := &fakeClock{now: time.Unix(1_700_000_000, 0)}
			store := cache.NewCache(cache.Config[string, int]{Clock: clk, Policy: policy})
			//: the first deadline fixes the epoch.
			store.SetTTL("epoch", 0, time.Hour)
			clk.advance(c.back)
			store.SetTTL("k", 1, c.ttl)
			clk.advance(c.after)
			if _, ok := store.Fetch("k"); ok != c.want {
				t.Errorf("Fetch hit = %v, want %v", ok, c.want)
			}
		})
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestUnlockedMapDeadlineIsARead pins Deadline (#1059): the instant a TTL'd
// entry expires, as the clock read it at the write — re-stamped by an
// overwrite, cleared by one without a TTL —, the zero Time for an entry
// without one, nothing for an absent key, and an entry past its deadline
// reported rather than reaped. No call moves a counter, Len or the order:
// a store that reads the deadline beside a Fetch must not count two hits.
func TestUnlockedMapDeadlineIsARead(t *testing.T) {
	t.Parallel()
	start := time.Unix(1_700_000_000, 0)
	type tc struct {
		name  string
		write func(c *cache.UnlockedMap[string, int], clk *fakeClock)
		// after moves the clock once the writes are done.
		after time.Duration
		want  time.Time
		ok    bool
	}
	tests := []tc{
		{name: "an absent key", write: func(*cache.UnlockedMap[string, int], *fakeClock) {}, ok: false},
		{name: "no deadline", write: func(c *cache.UnlockedMap[string, int], _ *fakeClock) { c.Set("k", 1) }, ok: true},
		{name: "a TTL", write: func(c *cache.UnlockedMap[string, int], _ *fakeClock) { c.SetTTL("k", 1, time.Minute) }, want: start.Add(time.Minute), ok: true},
		{name: "a TTL after the epoch", write: func(c *cache.UnlockedMap[string, int], clk *fakeClock) {
			c.SetTTL("epoch", 0, time.Hour)
			clk.advance(time.Second)
			c.SetTTL("k", 1, time.Minute)
		}, want: start.Add(time.Second + time.Minute), ok: true},
		{name: "an overwrite re-stamps it", write: func(c *cache.UnlockedMap[string, int], clk *fakeClock) {
			c.SetTTL("k", 1, time.Minute)
			clk.advance(time.Second)
			c.SetTTL("k", 2, time.Hour)
		}, want: start.Add(time.Second + time.Hour), ok: true},
		{name: "an overwrite without a TTL clears it", write: func(c *cache.UnlockedMap[string, int], _ *fakeClock) {
			c.SetTTL("k", 1, time.Minute)
			c.SetTTL("k", 2, 0)
		}, ok: true},
		{name: "a passed deadline is reported, not reaped", write: func(c *cache.UnlockedMap[string, int], _ *fakeClock) {
			c.SetTTL("k", 1, time.Minute)
		}, after: time.Hour, want: start.Add(time.Minute), ok: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
			clk := &fakeClock{now: start}
			store := cache.NewUnlockedMap(cache.Config[string, int]{Clock: clk, Policy: policy})
			c.write(store, clk)
			clk.advance(c.after)
			stats, held := store.Stats(), store.Len()
			got, ok := store.Deadline("k")
			if ok != c.ok || !got.Equal(c.want) {
				t.Fatalf("Deadline = %v, %v, want %v, %v", got, ok, c.want, c.ok)
			}
			if store.Stats() != stats || store.Len() != held {
				t.Errorf("Deadline moved the cache: Stats %+v -> %+v, Len %d -> %d", stats, store.Stats(), held, store.Len())
			}
		})
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
