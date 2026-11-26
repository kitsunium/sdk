package cache

import (
	"hash/maphash"
	"math"
	"strconv"
	"testing"
)

// sketchCell is one counter of the frequency sketch — its word and nibble —
// or one doorkeeper bit, its word and bit.
type sketchCell struct {
	word   *uint64
	nibble uint32
}

// sketchCells returns the four counters and the three doorkeeper bits the
// sketch counts key by.
func sketchCells[K comparable](s *frequencySketch, key K) [7]sketchCell {
	hash := uint32(maphash.Comparable(sketchSeed, key))
	block, mix := s.counters(hash)
	first, second, third := s.doorCells(hash)
	return [7]sketchCell{
		{&block[mix&1], mix >> 1 & 15},
		{&block[2|mix>>8&1], mix >> 9 & 15},
		{&block[4|mix>>16&1], mix >> 17 & 15},
		{&block[6|mix>>24&1], mix >> 25 & 15},
		{&s.door[first>>6], 16 + first&63},
		{&s.door[second>>6], 16 + second&63},
		{&s.door[third>>6], 16 + third&63},
	}
}

// disjointKeys returns n keys with prefix whose counters and doorkeeper bits
// in s share nothing, so the sketch counts each exactly: the seed is drawn
// per process, and a count-min sketch and a Bloom filter share by design.
func disjointKeys(t *testing.T, s *frequencySketch, prefix string, n int, taken map[sketchCell]bool) []string {
	t.Helper()
	keys := make([]string, 0, n)
	for i := 0; len(keys) < n; i++ {
		if i > 100_000 {
			t.Fatalf("no %d keys with disjoint counters", n)
		}
		key := prefix + strconv.Itoa(i)
		cells := sketchCells(s, key)
		free := true
		for _, cell := range cells {
			if taken[cell] {
				free = false
			}
		}
		if !free {
			continue
		}
		for _, cell := range cells {
			taken[cell] = true
		}
		keys = append(keys, key)
	}
	return keys
}

// Test_UnlockedMap_tinyLFUContest pins the admission contest on keys the
// sketch counts exactly: a full cache whose main holds keys used twice
// refuses a candidate used once or twice — it is evicted when it leaves the
// window — and admits one used three times, evicting main's oldest instead.
func Test_UnlockedMap_tinyLFUContest(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// uses is how many times the candidate is written or read.
		uses int
		// admitted is whether the candidate survives leaving the window.
		admitted bool
	}
	tests := []tc{
		{name: "a candidate used less than the victim is refused", uses: 1, admitted: false},
		{name: "a candidate used as often as the victim is refused", uses: 2, admitted: false},
		{name: "a candidate used more than the victim replaces it", uses: 3, admitted: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		store := NewUnlockedMap[string, int](Config[string, int]{MaxEntries: 10, Policy: PolicyTinyLFU})
		taken := map[sketchCell]bool{}
		resident := disjointKeys(t, &store.sketch, "main-", 10, taken)
		others := disjointKeys(t, &store.sketch, "other-", 2, taken)
		candidate, next := others[0], others[1]
		//: ten keys written and read once: every one used twice.
		for i, key := range resident {
			store.Set(key, i)
		}
		for _, key := range resident {
			if _, ok := store.Fetch(key); !ok {
				t.Fatalf("%s was evicted before the cache was full", key)
			}
		}
		store.Set(candidate, 0)
		for range c.uses - 1 {
			store.Fetch(candidate)
		}
		//: the next key pushes the candidate out of the window of one.
		store.Set(next, 0)
		hash := uint32(maphash.Comparable(sketchSeed, candidate))
		estimate := store.sketch.frequency(hash)
		//: the doorkeeper's use: its bits all set, none shared with another key.
		if store.sketch.doorHolds(store.sketch.doorCells(hash)) {
			estimate++
		}
		if estimate != uint64(c.uses) {
			t.Fatalf("the sketch estimates the candidate at %d uses, want %d", estimate, c.uses)
		}
		if _, ok := store.items[candidate]; ok != c.admitted {
			t.Errorf("candidate present = %v after %d uses, want %v", ok, c.uses, c.admitted)
		}
		if store.Len() != 10 {
			t.Errorf("Len = %d, want the bound, 10", store.Len())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_UnlockedMap_tinyLFUShares pins the segment bounds: the window a
// hundredth of the bound and at least one, protected four fifths of the rest,
// for each bound that applies, and none for a cache that never evicts.
func Test_UnlockedMap_tinyLFUShares(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		cfg    Config[string, int]
		want   tinyLFUShares
		words  int
		sample int
	}
	tests := []tc{
		{name: "a count bound", cfg: Config[string, int]{MaxEntries: 1000}, want: tinyLFUShares{window: 10, protected: 792}, words: 1024, sample: 10_000},
		{name: "a bound below a hundred", cfg: Config[string, int]{MaxEntries: 2}, want: tinyLFUShares{window: 1}, words: 8, sample: 20},
		{name: "a weight bound alone", cfg: Config[string, int]{MaxWeight: 500}, want: tinyLFUShares{windowWeight: 5, protectedWeight: 396}, words: 8, sample: 80},
		{name: "both bounds", cfg: Config[string, int]{MaxEntries: 300, MaxWeight: 9000}, want: tinyLFUShares{window: 3, protected: 237, windowWeight: 90, protectedWeight: 7128}, words: 512, sample: 3000},
		{name: "no bound", cfg: Config[string, int]{}, want: tinyLFUShares{}, words: 0, sample: 0},
		{name: "a weight bound near the int64's maximum", cfg: Config[string, int]{MaxWeight: math.MaxInt64}, want: tinyLFUShares{windowWeight: math.MaxInt64 / 100, protectedWeight: 7304910653188982439}, words: 8, sample: 80},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		cfg := c.cfg
		cfg.Policy = PolicyTinyLFU
		store := NewUnlockedMap(cfg)
		if store.shares != c.want {
			t.Errorf("shares = %+v, want %+v", store.shares, c.want)
		}
		if len(store.sketch.blocks)*blockWords != c.words || store.sketch.sampleSize != c.sample {
			t.Errorf("sketch = %d words aging every %d, want %d every %d", len(store.sketch.blocks)*blockWords, store.sketch.sampleSize, c.words, c.sample)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_UnlockedMap_tinyLFUSegments pins where W-TinyLFU keeps its entries: a
// new key in the window, the window's overflow in probation while main has
// room, a hit in probation into protected, and protected's oldest back to
// probation past its share — the lists' counts always the live count.
func Test_UnlockedMap_tinyLFUSegments(t *testing.T) {
	t.Parallel()
	store := NewUnlockedMap[string, int](Config[string, int]{MaxEntries: 10, Policy: PolicyTinyLFU})
	for i := range 10 {
		store.Set("k"+strconv.Itoa(i), i)
	}
	if store.small.count != 1 || store.probation.count != 9 || store.main.count != 0 {
		t.Fatalf("window %d, probation %d, protected %d after ten writes, want 1, 9, 0", store.small.count, store.probation.count, store.main.count)
	}
	if store.small.head.key != "k9" {
		t.Errorf("the window holds %s, want the newest, k9", store.small.head.key)
	}
	for i := range 9 {
		store.Fetch("k" + strconv.Itoa(i))
	}
	//: protected's share of ten is (10 - 1) × 4/5 = 7: two went back.
	if store.small.count != 1 || store.probation.count != 2 || store.main.count != 7 {
		t.Errorf("window %d, probation %d, protected %d after nine hits, want 1, 2, 7", store.small.count, store.probation.count, store.main.count)
	}
	if got := store.probation.head.key; got != "k1" {
		t.Errorf("probation's newest is %s, want k1, protected's oldest demoted last", got)
	}
	if store.small.count+store.probation.count+store.main.count != store.Len() {
		t.Errorf("the lists hold %d entries, Len says %d", store.small.count+store.probation.count+store.main.count, store.Len())
	}
}

// Test_UnlockedMap_tinyLFUStaleMark pins that a resident's mark of the
// aging period it went through the doorkeeper in is worth nothing once that
// period is over, however many periods have passed: the marks are narrow and
// the periods wrap, so a key left alone for a whole turn of them must not
// read as one that went through the doorkeeper in the current period — its
// estimate would hold a credit the doorkeeper emptied long ago, and its next
// use would count as a repeated one.
func Test_UnlockedMap_tinyLFUStaleMark(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// agings is how many aging periods pass while the key is left alone.
		agings int
	}
	tests := []tc{
		{name: "one period", agings: 1},
		{name: "a byte's worth of periods less one", agings: 255},
		{name: "a byte's worth of periods", agings: 256},
		{name: "a word's worth of periods less one", agings: 1<<16 - 1},
		{name: "a word's worth of periods", agings: 1 << 16},
		{name: "a word's worth of periods and one", agings: 1<<16 + 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		store := NewUnlockedMap[string, int](Config[string, int]{MaxEntries: 8, Policy: PolicyTinyLFU})
		keys := disjointKeys(t, &store.sketch, "key-", 2, map[sketchCell]bool{})
		resting, busy := keys[0], keys[1]
		store.Set(resting, 0)
		store.Set(busy, 0)
		held := store.items[resting]
		//: the busy key's two reads end one period each: the first counts and
		//: ends it, the second goes through the new doorkeeper.
		for range c.agings {
			before := store.sketch.period
			store.sketch.additions = store.sketch.sampleSize - 1
			store.Fetch(busy)
			if store.sketch.period == before {
				t.Fatalf("the period stayed %d: the read ended none", before)
			}
			store.Fetch(busy)
		}
		if got := store.sketch.estimateHeld(held.hash, held.period); got != 0 {
			t.Errorf("after %d periods the resting key is estimated at %d, want 0: its doorkeeper credit is long gone", c.agings, got)
		}
		store.Fetch(resting)
		if got := store.sketch.estimateHeld(held.hash, held.period); got != 1 {
			t.Errorf("after %d periods one use of the resting key estimates it at %d, want 1: it goes through the doorkeeper, no repeat", c.agings, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// weightedStore returns a W-TinyLFU cache bound at 100 in weight — protected
// holds 79 of it — whose entries weigh what their value says.
func weightedStore() *UnlockedMap[string, int] {
	return NewUnlockedMap[string, int](Config[string, int]{
		MaxWeight: 100,
		Policy:    PolicyTinyLFU,
		WeightOf:  func(_ string, v int) int64 { return int64(v) },
	})
}

// Test_UnlockedMap_tinyLFUHeavyCandidate pins that an entry heavier than
// protected's whole share, hit in probation, stays in probation — refreshed
// to its front — and demotes nothing: moving it would empty protected of the
// entries that fit, and then itself.
func Test_UnlockedMap_tinyLFUHeavyCandidate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// heavy is the weight of the entry hit second.
		heavy int
		// protectedKeys is what protected holds afterwards, newest first.
		protectedKeys []string
		// probationHead is probation's newest entry afterwards.
		probationHead string
	}
	tests := []tc{
		{name: "heavier than protected's share", heavy: 80, protectedKeys: []string{"a"}, probationHead: "b"},
		{name: "as heavy as protected's share", heavy: 79, protectedKeys: []string{"b"}, probationHead: "a"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		store := weightedStore()
		store.Set("a", 10)
		store.Set("b", c.heavy)
		store.Set("c", 1)
		store.Fetch("a")
		store.Fetch("b")
		var got []string
		for ent := store.main.head; ent != nil; ent = ent.next {
			got = append(got, ent.key)
		}
		if len(got) != len(c.protectedKeys) || (len(got) > 0 && got[0] != c.protectedKeys[0]) {
			t.Errorf("protected holds %v, want %v", got, c.protectedKeys)
		}
		if head := store.probation.head; head == nil || head.key != c.probationHead {
			t.Errorf("probation's newest is %v, want %s", head, c.probationHead)
		}
		if store.main.weight > store.shares.protectedWeight {
			t.Errorf("protected weighs %d over its share %d", store.main.weight, store.shares.protectedWeight)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_UnlockedMap_tinyLFUHeavierProtected pins that a Set which makes a
// protected entry heavier brings protected back within its share, its oldest
// going to probation, and that an unchanged weight moves nothing.
func Test_UnlockedMap_tinyLFUHeavierProtected(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		// weight is the weight the overwritten entry takes.
		weight int
		// protectedWeight is protected's weight afterwards.
		protectedWeight int64
		// probationCount is how many entries probation holds afterwards.
		probationCount int
	}
	tests := []tc{
		{name: "the same weight", weight: 40, protectedWeight: 79, probationCount: 0},
		{name: "a heavier entry", weight: 50, protectedWeight: 50, probationCount: 1},
		{name: "heavier than the whole share, the bound evicts the rest", weight: 90, protectedWeight: 0, probationCount: 1},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		store := weightedStore()
		store.Set("a", 40)
		store.Set("b", 39)
		store.Set("c", 1)
		store.Fetch("a")
		store.Fetch("b")
		if store.main.weight != 79 {
			t.Fatalf("protected weighs %d before the overwrite, want 79", store.main.weight)
		}
		store.Set("a", c.weight)
		if store.main.weight != c.protectedWeight || store.probation.count != c.probationCount {
			t.Errorf("protected weighs %d and probation holds %d, want %d and %d", store.main.weight, store.probation.count, c.protectedWeight, c.probationCount)
		}
		if store.small.count+store.probation.count+store.main.count != store.Len() {
			t.Errorf("the lists hold %d entries, Len says %d", store.small.count+store.probation.count+store.main.count, store.Len())
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
