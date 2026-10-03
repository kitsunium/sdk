package id

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// steppedClock is a scripted Clock: each Now() consumes the next instant in
// the script, then pins to the last one. It lets a test drive the snowflake
// generator through a clock regression deterministically, without sleeping.
type steppedClock struct {
	mu     sync.Mutex
	script []int64 // epoch-relative milliseconds, consumed in order
	pos    int
}

// Now returns the next scripted instant, converting the epoch-relative
// millisecond back to an absolute time the generator will re-derive.
func (c *steppedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	//: past the end of the script, pin to the final value so a spinning
	//: caller observes a stable (not advancing) clock.
	idx := c.pos
	if idx >= len(c.script) {
		idx = len(c.script) - 1
	} else {
		c.pos++
	}
	//: rebuild the absolute instant the generator subtracts the epoch from.
	return time.UnixMilli(c.script[idx] + snowflakeEpoch)
}

// Since satisfies clock.Clock; the snowflake generator never calls it.
func (c *steppedClock) Since(t time.Time) time.Duration {
	//: derive from Now so the fake stays internally consistent.
	return c.Now().Sub(t)
}

// Test_snowflakeGen_tillNext pins the liveness contract of the
// same-millisecond overflow wait: tillNext holds the caller's g.mu for its
// whole duration, so a clock that regresses below the exhausted millisecond
// MUST surface ClockBackwards instead of spinning. Before the fix the loop
// only broke on `now > prev`, so a backward jump stalled every concurrent
// New() indefinitely.
func Test_snowflakeGen_tillNext(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		script   []int64
		prev     int64
		wantSent error // nil = expect success
		wantMS   int64
	}
	tests := []tc{
		//: the clock ticks forward — normal overflow wait, returns the new ms.
		{"advances past prev", []int64{100, 100, 101}, 100, nil, 101},
		//: the clock regresses below prev — unwaitable, must fail fast.
		{"regresses below prev", []int64{100, 99}, 100, ClockBackwards, 0},
		//: a regression on the very first read is caught immediately.
		{"regresses immediately", []int64{50}, 100, ClockBackwards, 0},
		//: the clock STALLS at prev forever — the spin budget must end the wait.
		{"stalls at prev", []int64{100}, 100, ClockStalled, 0},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		g := &snowflakeGen{clk: &steppedClock{script: tc.script}}
		//: the wait must terminate; a hang here fails the whole package by
		//: timeout, which is the observable form of the original defect.
		got, err := g.tillNext(tc.prev)
		//: contract: each misbehaviour maps to its OWN documented sentinel —
		//: asserting "some error" would let a regression swap one for the other.
		if tc.wantSent != nil {
			if !errors.Is(err, tc.wantSent) {
				t.Fatalf("%s: tillNext=(%d, %v), want %v", tc.name, got, err, tc.wantSent)
			}
			return
		}
		//: forward path returns the first millisecond strictly past prev.
		if err != nil {
			t.Fatalf("%s: tillNext err=%v, want nil", tc.name, err)
		}
		if got != tc.wantMS {
			t.Errorf("%s: tillNext=%d, want %d", tc.name, got, tc.wantMS)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { t.Parallel(); runCase(t, tc) })
	}
}

// Test_snowflakeGen_New drives the sequence-overflow defect through New(): burn
// the 12-bit sequence inside one millisecond, then regress the clock so the
// overflow wait cannot complete.
//
// The restored sequence is the subtle half. Leaving it at 0 after a failed wait
// would let a retry inside the SAME millisecond hand out seq=1 — an identifier
// already issued — so the failure would turn a liveness problem into a
// correctness one.
func Test_snowflakeGen_New(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the millisecond the generator is pinned at while the sequence burns.
		pinned int64
		//: what the clock reports once the overflow wait begins.
		afterOverflow int64
		wantSentinel  error
	}
	tests := []tc{
		{"a clock that regresses during the wait", 500, 499, ClockBackwards},
		{"a clock that regresses far during the wait", 500, 0, ClockBackwards},
		//: a clock pinned at the exhausted millisecond is indistinguishable
		//: from a normal sub-millisecond remainder for a while, so the wait
		//: only gives up once the spin budget is exhausted.
		{"a clock stalled at the exhausted millisecond", 500, 500, ClockStalled},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g := &snowflakeGen{clk: &steppedClock{script: []int64{c.pinned}}, node: 1}

		//: burn the whole sequence space within the pinned millisecond.
		for i := int64(0); i <= maxSeq; i++ {
			if _, err := g.New(); err != nil {
				t.Fatalf("New #%d = %v, want nil while the sequence remains", i, err)
			}
		}

		//: the next call wraps the sequence and enters the overflow wait.
		g.clk = &steppedClock{script: []int64{c.afterOverflow}}
		_, err := g.New()

		//: each misbehaviour maps to its OWN sentinel; asserting "some error"
		//: would let a regression swap one for the other.
		if !errors.Is(err, c.wantSentinel) {
			t.Fatalf("New after overflow = %v, want %v", err, c.wantSentinel)
		}
		//: the sequence is restored to its pre-wrap value so a retry in this
		//: same millisecond re-enters the wait instead of reissuing seq=1.
		if g.seq != maxSeq {
			t.Errorf("seq = %d after the failed wait, want %d — a retry would reissue a used sequence",
				g.seq, maxSeq)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_snowflakeGen_Scheme pins the registry key. It is what a caller passes to
// id.New, so a drift here unregisters the generator from every consumer that
// asks for it by name.
func Test_snowflakeGen_Scheme(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		gen  *snowflakeGen
	}
	tests := []tc{
		{"the default-node generator", newSnowflake(defaultNode(), &steppedClock{script: []int64{1}})},
		{"an explicit-node generator", newSnowflake(7, &steppedClock{script: []int64{1}})},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := string(c.gen.Scheme()); got != "snowflake" {
			t.Errorf("Scheme() = %q, want snowflake", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_newSnowflake pins the node reduction. An out-of-range node is reduced
// into the 10-bit space rather than refused, which keeps a misconfigured
// deployment running — but it also means two nodes 1024 apart collide, so the
// reduction has to be exactly the documented mask and nothing looser.
func Test_newSnowflake(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		node int64
		want int64
	}
	tests := []tc{
		{"zero", 0, 0},
		{"a mid-range node", 7, 7},
		{"the highest in-range node", maxNode, maxNode},
		{"one past the space wraps to zero", maxNode + 1, 0},
		{"a large node reduces", 1<<40 + 5, 5},
		//: a negative node still lands inside the space rather than producing
		//: a negative shift into the packed identifier.
		{"a negative node", -1, maxNode},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g := newSnowflake(c.node, &steppedClock{script: []int64{1}})
		if g.node != c.want {
			t.Errorf("newSnowflake(%d).node = %d, want %d", c.node, g.node, c.want)
		}
		//: whatever the input, the node must fit the field it is packed into.
		if g.node < 0 || g.node > maxNode {
			t.Errorf("newSnowflake(%d).node = %d, outside the 10-bit space", c.node, g.node)
		}
		//: a fresh generator has issued nothing.
		if g.lastMS != 0 || g.seq != 0 {
			t.Errorf("a fresh generator starts at lastMS=%d seq=%d, want 0/0", g.lastMS, g.seq)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_defaultNode pins that the derived node is stable and in range.
//
// Stability is what makes it usable at all: the node id is the only thing
// keeping two processes on different hosts from issuing the same identifier in
// the same millisecond, so a value that changed between calls within one process
// would make the sequence counter meaningless.
func Test_defaultNode(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		calls int
	}
	tests := []tc{
		{"a single derivation", 1},
		{"ten derivations", 10},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		first := defaultNode()
		for range c.calls {
			//: the seed is hostname+pid, neither of which changes within a
			//: process, so the answer must not either.
			if got := defaultNode(); got != first {
				t.Fatalf("defaultNode() returned %d after %d", got, first)
			}
		}
		//: the value must fit the field it is packed into.
		if first < 0 || first > maxNode {
			t.Errorf("defaultNode() = %d, outside the 10-bit space", first)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
