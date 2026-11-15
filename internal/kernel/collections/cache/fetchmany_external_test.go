package cache_test

import (
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/collections/cache"
)

// keysFrom returns the keys "k<from>" to "k<to-1>".
func keysFrom(from, to int) []string {
	keys := make([]string, 0, to-from)
	for i := from; i < to; i++ {
		keys = append(keys, "k"+strconv.Itoa(i))
	}
	return keys
}

// fetchManyFixture is a map holding k0 to k39 with no deadline, "expired",
// whose deadline the clock passes once advanced, and nothing under "deleted",
// which was stored and deleted.
func fetchManyFixture(clk *fakeClock, policy cache.Policy, onEvict func(string, int)) *cache.UnlockedMap[string, int] {
	m := cache.NewUnlockedMap(cache.Config[string, int]{Clock: clk, Policy: policy, OnEvict: onEvict})
	for i, key := range keysFrom(0, 40) {
		m.Set(key, i)
	}
	m.SetTTL("expired", -1, time.Second)
	m.Set("deleted", -2)
	m.Delete("deleted")
	//: the fixture, its one deadline still ahead.
	return m
}

// TestUnlockedMapFetchManyAnswersAsFetch is the batch read's conformance
// (#840): FetchMany appends one reference a key, in order, after whatever refs
// held, and each answers what a Fetch of its key answers on a twin map — the
// value of a hit, nil for a key never stored, deleted, or past its deadline —
// in batches shorter than one chunk of lookups and longer than two.
func TestUnlockedMapFetchManyAnswersAsFetch(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		keys []string
	}
	tests := []tc{
		{name: "an empty batch", keys: nil},
		{name: "hits and misses", keys: []string{"k1", "absent", "k2", "deleted", "expired", "k3"}},
		{name: "repeats", keys: []string{"k1", "k1", "absent", "absent", "expired", "expired"}},
		{name: "two chunks and a tail", keys: append(keysFrom(0, 36), "expired", "absent", "k5", "k35")},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
			clk := &fakeClock{now: time.Unix(1_700_000_000, 0)}
			batch, twin := fetchManyFixture(clk, policy, nil), fetchManyFixture(clk, policy, nil)
			clk.advance(2 * time.Second)
			held := 7
			refs := batch.FetchMany(c.keys, []*int{&held})
			if len(refs) != 1+len(c.keys) || refs[0] != &held {
				t.Fatalf("FetchMany returned %d references, want the one held and %d", len(refs), len(c.keys))
			}
			//: every position against the twin's Fetch of its key.
			for at, key := range c.keys {
				want, ok := twin.Fetch(key)
				got := refs[1+at]
				if (got != nil) != ok || (got != nil && *got != want) {
					t.Errorf("position %d (%q): reference %v, want what Fetch answers (%d, %v)", at, key, got, want, ok)
				}
			}
			if batch.Len() != twin.Len() {
				t.Errorf("Len = %d after the batch, %d after the Fetches", batch.Len(), twin.Len())
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

// TestUnlockedMapFetchManyReadsARepeatOnce pins the batch's read-once rule
// (#840): a key a batch names three times is one hit in Stats, and LRU moves
// it once, at its first position — so the key read after it stays ahead and
// outlives it — where three Fetches would count three hits and move it last.
// A key that misses counts a miss at every position, as Fetch counts it.
func TestUnlockedMapFetchManyReadsARepeatOnce(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		keys       []string
		hits, miss uint64
	}
	tests := []tc{
		{name: "a hit named three times", keys: []string{"a", "b", "a", "a"}, hits: 2},
		{name: "a miss named twice", keys: []string{"a", "x", "b", "x"}, hits: 2, miss: 2},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
			m := cache.NewUnlockedMap(cache.Config[string, int]{MaxEntries: 3, Policy: policy})
			m.Set("a", 1)
			m.Set("b", 2)
			m.Set("c", 3)
			before := m.Stats()
			refs := m.FetchMany(c.keys, nil)
			after := m.Stats()
			if hits, misses := after.Hits-before.Hits, after.Misses-before.Misses; hits != c.hits || misses != c.miss {
				t.Fatalf("the batch counted %d hits and %d misses, want %d and %d", hits, misses, c.hits, c.miss)
			}
			//: every position of one key holds the one reference.
			for at, key := range c.keys {
				if first := slices.Index(c.keys, key); refs[at] != refs[first] {
					t.Fatalf("positions %d and %d of %q hold two references", first, at, key)
				}
			}
			//: S3-FIFO moves nothing on a hit; LRU's order shows the one move.
			if policy != cache.PolicyLRU {
				return
			}
			m.Set("d", 4)
			m.Set("e", 5)
			if _, ok := m.Fetch("b"); !ok {
				t.Errorf("b, read after a, was evicted before it")
			}
			if _, ok := m.Fetch("a"); ok {
				t.Errorf("a outlived b: a repeat moved it again")
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

// TestUnlockedMapFetchManyReadsTheClockOnce is the batch's other saving
// (#840): every deadline of a batch is judged at one reading of the clock,
// however many chunks it takes, and none is taken while no entry carries a
// deadline — where Fetch reads it once a key with a deadline.
func TestUnlockedMapFetchManyReadsTheClockOnce(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		ttl   time.Duration
		keys  int
		reads int64
	}
	tests := []tc{
		{name: "sixteen keys with a deadline", ttl: time.Hour, keys: 16, reads: 1},
		{name: "forty keys with a deadline", ttl: time.Hour, keys: 40, reads: 1},
		{name: "no deadline", ttl: 0, keys: 40, reads: 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
			clk := &countingClock{now: time.Unix(1_700_000_000, 0)}
			m := cache.NewUnlockedMap(cache.Config[string, int]{Clock: clk, Policy: policy})
			keys := keysFrom(0, c.keys)
			for i, key := range keys {
				m.SetTTL(key, i, c.ttl)
			}
			clk.reads.Store(0)
			refs := m.FetchMany(keys, nil)
			if got := clk.reads.Load(); got != c.reads {
				t.Fatalf("a batch of %d keys read the clock %d times, want %d", c.keys, got, c.reads)
			}
			if slices.Contains(refs, nil) {
				t.Fatalf("a batch of live keys missed one")
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

// TestUnlockedMapFetchManyReapsWhatExpired pins the batch's lazy expiry: an
// entry past its deadline is reaped, as Fetch reaps it — once, however often
// the batch names it, counted as one eviction and handed to OnEvict once,
// after the batch —, and its later positions miss, a lookup made before the
// reap included.
func TestUnlockedMapFetchManyReapsWhatExpired(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		keys []string
	}
	tests := []tc{
		{name: "named once", keys: []string{"k1", "expired", "k2"}},
		{name: "named twice in one chunk", keys: []string{"expired", "k1", "expired"}},
		{name: "named in two chunks", keys: append(append([]string{"expired"}, keysFrom(0, 20)...), "expired")},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		forEachPolicy(t, func(t *testing.T, policy cache.Policy) {
			clk := &fakeClock{now: time.Unix(1_700_000_000, 0)}
			var evicted []string
			var m *cache.UnlockedMap[string, int]
			m = fetchManyFixture(clk, policy, func(key string, _ int) {
				//: re-entering is allowed: the map is consistent again.
				evicted = append(evicted, key+"@"+strconv.Itoa(m.Len()))
			})
			clk.advance(2 * time.Second)
			refs := m.FetchMany(c.keys, nil)
			for at, key := range c.keys {
				if key == "expired" && refs[at] != nil {
					t.Fatalf("position %d answered the expired entry", at)
				}
			}
			if want := []string{"expired@40"}; !slices.Equal(evicted, want) {
				t.Fatalf("OnEvict saw %v, want %v", evicted, want)
			}
			if got := m.Stats().Evictions; got != 1 {
				t.Fatalf("Evictions = %d, want 1", got)
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

// TestUnlockedMapFetchManyGrowsRefsOnlyWhenShort pins the append contract a
// zero-allocation batch rests on: refs with the room is written in place, and
// grows only when its capacity is short.
func TestUnlockedMapFetchManyGrowsRefsOnlyWhenShort(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		keys    int
		inPlace bool
	}
	tests := []tc{
		{name: "the room for every key", keys: 8, inPlace: true},
		{name: "the room for some", keys: 9, inPlace: false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		m := cache.NewUnlockedMap(cache.Config[string, int]{})
		keys := keysFrom(0, c.keys)
		for i, key := range keys {
			m.Set(key, i)
		}
		refs := make([]*int, 0, 8)
		got := m.FetchMany(keys, refs)
		if len(got) != c.keys {
			t.Fatalf("FetchMany returned %d references, want %d", len(got), c.keys)
		}
		if inPlace := &got[0] == &refs[:1][0]; inPlace != c.inPlace {
			t.Fatalf("written in place = %v, want %v", inPlace, c.inPlace)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
