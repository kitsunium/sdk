//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — a drain's catch-up cut by the batch budget leaves its ring
// where reapRings' cut does: the next batch reaps it without waiting, starting
// there (ADR 0206, #1541).
package loop

import (
	"testing"
	"time"
)

// TestURingCatchUp_cutLeavesTheRingNext runs uLoop.catchUp on a draining loop
// whose batch budget is spent, the ring under test holding completions the
// last reap left: cut there, the loop records the ring for the next batch,
// whose await then only submits — a ring cut in catchUp would otherwise wait
// on an empty primary for its tick. A loop with no budget catches up whole
// and records nothing.
func TestURingCatchUp_cutLeavesTheRingNext(t *testing.T) {
	type tc struct {
		name   string
		rings  int
		on     int
		budget time.Duration
		cutAt  int
		behind bool
	}
	runCase := func(t *testing.T, c tc) {
		ul := &uLoop{lp: &loop{draining: true, sz: sizing{batchBudget: c.budget, cqBatch: 1}}}
		tick := kernelTimespec{nsec: int64(time.Second)}
		//: every ring on the test's locked thread, reaped whole by reapRings.
		for i := range c.rings {
			rr := &uRing{ul: ul, u: openWaitRing(t, setupBase)}
			rr.r = newRing(ul.lp, i, rr)
			rr.r.setBehind(false)
			ul.rings = append(ul.rings, rr)
		}
		u := ul.rings[c.on].u
		nops(t, u, 3)
		u.wait(&tick, 3)
		ul.catchUp()
		//: where the next batch starts.
		if ul.cutAt != c.cutAt {
			t.Fatalf("cutAt = %d, want %d", ul.cutAt, c.cutAt)
		}
		//: the ring's verdict.
		if got := ul.rings[c.on].r.behind(); got != c.behind {
			t.Fatalf("behind = %v, want %v", got, c.behind)
		}
		//: no cut: nothing left to reap.
		if c.cutAt == 0 {
			return
		}
		start := time.Now()
		plain := ul.await(&tick, 1, &tick)
		//: completions left: a submit, no wait.
		if took := time.Since(start); plain || took > waitSlack {
			t.Fatalf("await after a cut catch-up: plain %v after %v, want a submit at once", plain, took)
		}
	}
	for _, c := range []tc{
		{"the primary, cut", 1, 0, time.Nanosecond, 1, true},
		{"a secondary, cut, the primary empty", 2, 1, time.Nanosecond, 2, true},
		{"no budget, caught up", 2, 1, 0, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
