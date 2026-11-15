package cache

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// policyTwinKeys is how many distinct keys the twins' workload names: more
// than the bound, so every policy evicts, admits, promotes and demotes.
const policyTwinKeys int = 96

// policyTwinSteps is how many operations each twin runs.
const policyTwinSteps int = 4000

// entryState is what a policy keeps of one entry: its key, the list it sits
// in, the counters each policy reads, and its deadline.
type entryState struct {
	key      string
	queue    queueID
	freq     uint8
	period   uint16
	weight   int32
	deadline int64
	timed    bool
}

// mapState is everything a policy decides by: each list in order, the
// counters, the sketch and the ghost's size.
type mapState struct {
	main, small, probation  []entryState
	hits, misses, evictions uint64
	blocks                  []sketchBlock
	door                    []uint64
	additions               int
	period                  uint16
	ghost                   int
}

// listState reads l from its head.
func listState(l *entryList[string, int]) []entryState {
	var out []entryState
	for ent := l.head; ent != nil; ent = ent.next {
		out = append(out, entryState{
			key: ent.key, queue: ent.queue, freq: ent.freq, period: ent.period,
			weight: ent.weight, deadline: ent.deadline, timed: ent.slot != 0,
		})
	}
	return out
}

// stateOf snapshots m.
func stateOf(m *UnlockedMap[string, int]) mapState {
	return mapState{
		main: listState(&m.main), small: listState(&m.small), probation: listState(&m.probation),
		hits: m.hits, misses: m.misses, evictions: m.evictions,
		blocks: slices.Clone(m.sketch.blocks), door: slices.Clone(m.sketch.door),
		additions: m.sketch.additions, period: m.sketch.period, ghost: m.ghost.size,
	}
}

// diffState names the first difference between two snapshots, "" when none.
func diffState(batched, single mapState) string {
	lists := []struct {
		name string
		a, b []entryState
	}{{"main", batched.main, single.main}, {"small", batched.small, single.small}, {"probation", batched.probation, single.probation}}
	for _, l := range lists {
		if !slices.Equal(l.a, l.b) {
			return fmt.Sprintf("%s list: batched %v, one by one %v", l.name, l.a, l.b)
		}
	}
	switch {
	case batched.hits != single.hits || batched.misses != single.misses || batched.evictions != single.evictions:
		return fmt.Sprintf("counters: batched %d/%d/%d, one by one %d/%d/%d",
			batched.hits, batched.misses, batched.evictions, single.hits, single.misses, single.evictions)
	case !slices.Equal(batched.blocks, single.blocks) || !slices.Equal(batched.door, single.door):
		return "the sketch's counters or doorkeeper"
	case batched.additions != single.additions || batched.period != single.period:
		return fmt.Sprintf("the sketch's period: batched %d/%d, one by one %d/%d",
			batched.additions, batched.period, single.additions, single.period)
	case batched.ghost != single.ghost:
		return fmt.Sprintf("the ghost: batched %d, one by one %d", batched.ghost, single.ghost)
	default:
		return ""
	}
}

// policyTwin is one map of a pair, its clock and what OnEvict saw.
type policyTwin struct {
	m       *UnlockedMap[string, int]
	clk     *clock.ManualClock
	evicted []string
}

// newPolicyTwin builds a bounded map under policy, weighed when weighed.
func newPolicyTwin(policy Policy, weighed bool) *policyTwin {
	twin := &policyTwin{clk: clock.NewManualClock(time.Unix(0, 0))}
	cfg := Config[string, int]{
		Clock: twin.clk, Policy: policy, MaxEntries: 32,
		OnEvict: func(key string, _ int) { twin.evicted = append(twin.evicted, key) },
	}
	//: a weigher: a value's weight is its last digit plus one.
	if weighed {
		cfg.MaxEntries, cfg.MaxWeight = 0, 96
		cfg.WeightOf = func(_ string, val int) int64 { return int64(val%10 + 1) }
	}
	twin.m = NewUnlockedMap(cfg)
	return twin
}

// fetchOneByOne reads keys as a run of Fetch calls would, except that a key a
// batch already found is not read again — the batch's read-once rule; a key
// it missed is read again, a miss counted each time, as FetchMany counts
// them — and returns the reference each position answers.
func fetchOneByOne(m *UnlockedMap[string, int], keys []string) []*int {
	out := make([]*int, len(keys))
	first := map[string]int{}
	for at, key := range keys {
		//: a hit already read: the same reference, nothing recorded.
		if earlier, seen := first[key]; seen {
			out[at] = out[earlier]
			continue
		}
		//: a hit's reference is the entry's own value.
		if _, ok := m.Fetch(key); ok {
			out[at] = &m.items[key].val
			first[key] = at
		}
	}
	return out
}

// TestFetchManyKeepsEachPolicysState is #840's equivalence under every
// policy: a run of FetchMany leaves a map exactly where the same keys read by
// Fetch, one by one, leave its twin — the lists and their order, S3-FIFO's
// hit counters, W-TinyLFU's sketch, doorkeeper marks, promotions out of
// probation and demotions out of protected, the counters and every OnEvict —
// with Sets, deadlines and clock moves between the batches, and batches
// shorter than one chunk of lookups, across chunks, with repeats.
func TestFetchManyKeepsEachPolicysState(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		policy  Policy
		weighed bool
		seed    uint64
	}
	tests := []tc{
		{name: "LRU", policy: PolicyLRU, seed: 1},
		{name: "S3-FIFO", policy: PolicyS3FIFO, seed: 2},
		{name: "W-TinyLFU", policy: PolicyTinyLFU, seed: 3},
		{name: "LRU, weighed", policy: PolicyLRU, weighed: true, seed: 4},
		{name: "S3-FIFO, weighed", policy: PolicyS3FIFO, weighed: true, seed: 5},
		{name: "W-TinyLFU, weighed", policy: PolicyTinyLFU, weighed: true, seed: 6},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		batched, single := newPolicyTwin(c.policy, c.weighed), newPolicyTwin(c.policy, c.weighed)
		rng := rand.New(rand.NewPCG(c.seed, 840))
		// hot is a skewed draw: a third of the keys take most of the reads,
		// so hits land in every segment and the sketch has favourites.
		key := func() string {
			//: a hot key or any key.
			if rng.IntN(3) > 0 {
				return "k" + strconv.Itoa(rng.IntN(policyTwinKeys/3))
			}
			return "k" + strconv.Itoa(rng.IntN(policyTwinKeys))
		}
		var refs []*int
		for step := range policyTwinSteps {
			switch op := rng.IntN(10); {
			//: a write, with a deadline one time in four.
			case op < 3:
				k, v := key(), rng.IntN(1000)
				ttl := time.Duration(0)
				if rng.IntN(4) == 0 {
					ttl = time.Duration(1+rng.IntN(5)) * time.Second
				}
				for _, twin := range []*policyTwin{batched, single} {
					twin.m.SetTTL(k, v, ttl)
				}
			//: the clock moves.
			case op == 3:
				d := time.Duration(rng.IntN(1500)) * time.Millisecond
				batched.clk.Advance(d)
				single.clk.Advance(d)
			//: a batch, of 1 to 40 keys.
			default:
				keys := make([]string, 1+rng.IntN(40))
				for at := range keys {
					keys[at] = key()
				}
				refs = batched.m.FetchMany(keys, refs[:0])
				want := fetchOneByOne(single.m, keys)
				for at := range keys {
					if (refs[at] == nil) != (want[at] == nil) || (refs[at] != nil && *refs[at] != *want[at]) {
						t.Fatalf("step %d: %q at %d answered differently", step, keys[at], at)
					}
				}
			}
			if diff := diffState(stateOf(batched.m), stateOf(single.m)); diff != "" {
				t.Fatalf("step %d: %s", step, diff)
			}
			if !slices.Equal(batched.evicted, single.evicted) {
				t.Fatalf("step %d: OnEvict saw %v batched, %v one by one", step, batched.evicted, single.evicted)
			}
		}
		if single.m.hits == 0 || single.m.evictions == 0 {
			t.Fatalf("the workload recorded %d hits and %d evictions: it proves nothing", single.m.hits, single.m.evictions)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
