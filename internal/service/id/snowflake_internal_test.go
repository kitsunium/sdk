package id

import (
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

// Test_tillNext_ClockRegressionDoesNotSpin pins the liveness contract of the
// same-millisecond overflow wait: tillNext holds the caller's g.mu for its
// whole duration, so a clock that regresses below the exhausted millisecond
// MUST surface ClockBackwards instead of spinning. Before the fix the loop
// only broke on `now > prev`, so a backward jump stalled every concurrent
// New() indefinitely.
func Test_tillNext_ClockRegressionDoesNotSpin(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		script  []int64
		prev    int64
		wantErr bool
		wantMS  int64
	}
	tests := []tc{
		//: the clock ticks forward — normal overflow wait, returns the new ms.
		{"advances past prev", []int64{100, 100, 101}, 100, false, 101},
		//: the clock regresses below prev — unwaitable, must fail fast.
		{"regresses below prev", []int64{100, 99}, 100, true, 0},
		//: a regression on the very first read is caught immediately.
		{"regresses immediately", []int64{50}, 100, true, 0},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		g := &snowflakeGen{clk: &steppedClock{script: tc.script}}
		//: the wait must terminate; a hang here fails the whole package by
		//: timeout, which is the observable form of the original defect.
		got, err := g.tillNext(tc.prev)
		//: contract: a backward clock yields ClockBackwards, never a spin.
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: tillNext returned (%d, nil), want ClockBackwards", tc.name, got)
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

// Test_New_SequenceOverflowClockRegression drives the defect through the
// public New() path: exhaust the 12-bit sequence inside one millisecond, then
// regress the clock so the overflow wait cannot complete. New must return
// ClockBackwards, and must NOT leave the sequence at 0 — a retry inside the
// same millisecond would then hand out seq=1, an id already issued.
func Test_New_SequenceOverflowClockRegression(t *testing.T) {
	t.Parallel()
	//: pin the clock at ms 500 for every read until the regression.
	clk := &steppedClock{script: []int64{500}}
	g := &snowflakeGen{clk: clk, node: 1}
	//: burn the whole sequence space within the pinned millisecond.
	for i := int64(0); i <= maxSeq; i++ {
		if _, err := g.New(); err != nil {
			t.Fatalf("New #%d err=%v, want nil while sequence remains", i, err)
		}
	}
	//: the next call wraps the sequence and enters the overflow wait; swap in
	//: a regressed clock so the wait can never be satisfied.
	g.clk = &steppedClock{script: []int64{499}}
	_, err := g.New()
	//: contract: fail fast rather than spin under the held mutex.
	if err == nil {
		t.Fatal("New after overflow with a regressed clock returned nil error, want ClockBackwards")
	}
	//: contract: the sequence is restored to its pre-wrap value so a retry in
	//: this same millisecond re-enters the wait instead of reissuing seq=1.
	if g.seq != maxSeq {
		t.Errorf("seq=%d after failed overflow wait, want %d (else a retry reissues a used sequence)", g.seq, maxSeq)
	}
}
