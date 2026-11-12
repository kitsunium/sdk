//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — a secondary ring's completion and the primary's wait (#703).
package loop

import (
	"strings"
	"testing"
	"time"
)

// TestSecondaryWake_bounded is #703's detector: every connection on a
// secondary ring of a two-ring loop, 200 echo round trips, whose median must
// stay under half a tick. On a kernel that leaves the secondary's completion
// to the primary's timeout (x86 6.14 and 6.17 in #703's table) the median was
// one tick, 10 ms; fanTickFor bounds the wait there to fanWakeTick. The
// probe's verdict and the kernel are logged, so a lane names which kernel
// needed the bound.
func TestSecondaryWake_bounded(t *testing.T) {
	_, lat := secondaryRoundTrips(t, loopHooks{})
	n := len(lat)
	verdict := secondaryWakeVerdict()
	t.Logf("%s: secondary wake %s; secondary-only round trip p50/p90/max %v/%v/%v", kernelRelease(), verdict, lat[n/2], lat[n*9/10], lat[n-1])
	if verdict == wakeUnprobed {
		t.Fatal("a two-ring loop started without probing the secondary wake")
	}
	if lat[n/2] >= tickEvery/2 {
		t.Fatalf("secondary round trip p50 %v, at least half a tick (%v): the primary's wait is not woken (verdict %s)", lat[n/2], tickEvery/2, verdict)
	}
}

// TestSecondaryWake_tickFor pins the bound's scope: one ring keeps the tick,
// and a slow verdict bounds a loop with secondaries to fanWakeTick; with the
// verdict forced slow the secondary still serves.
func TestSecondaryWake_tickFor(t *testing.T) {
	slowFanWake()
	was := secondaryWakeVerdict()
	t.Cleanup(func() { secondaryWake.Store(was) })
	secondaryWake.Store(wakeSlow)
	if got := fanTickFor(1); got.nsec != int64(tickEvery) {
		t.Fatalf("one ring: %d ns, want the tick", got.nsec)
	}
	if got := fanTickFor(2); got.nsec != int64(fanWakeTick) {
		t.Fatalf("two rings, slow: %d ns, want %v", got.nsec, fanWakeTick)
	}
	g, lat := secondaryRoundTrips(t, loopHooks{})
	if lat[len(lat)/2] >= tickEvery/2 {
		t.Fatalf("forced slow: p50 %v", lat[len(lat)/2])
	}
	_ = g
	secondaryWake.Store(wakePrompt)
	if got := fanTickFor(2); got.nsec != int64(tickEvery) {
		t.Fatalf("two rings, prompt: %d ns, want the tick", got.nsec)
	}
}

// TestSecondaryWake_logged pins that the group's log line names the verdict.
func TestSecondaryWake_logged(t *testing.T) {
	var b strings.Builder
	g, _ := codeCase(t, func(cfg *Config) {
		cfg.Value.Rings = 2
		cfg.Log = &b
	})
	_ = g.Close()
	if !strings.Contains(b.String(), "secondary_wake="+secondaryWakeVerdict()) {
		t.Fatalf("log = %q", b.String())
	}
}

// TestSecondaryWake_probeControls runs the probe twice more: as the loop
// sets it up it must agree with its own first verdict — the process's may
// read slow past it, when a loop's waits found the wake slow (watchFan) —,
// and with the secondary's eventfd left unregistered — nothing wakes the
// primary — it must read slow, so a slow kernel cannot pass for a prompt one.
func TestSecondaryWake_probeControls(t *testing.T) {
	start := time.Now()
	if got := probeFanWake(false); got != wakeSlow {
		t.Fatalf("unregistered eventfd: %s, want slow", got)
	}
	if took := time.Since(start); took > 2*fanWakeBound {
		t.Fatalf("the probe took %v, past its bound", took)
	}
	slowFanWake()
	if got := probeFanWake(true); got != fanProbed {
		t.Fatalf("probe again: %s, first probe %s", got, fanProbed)
	}
}

// TestSecondaryWake_detectedInService is #703's detector where the start's
// probe does not see the slow wake — #1336's CI on 6.14: the probe's rig woke
// promptly, the loop's own secondaries did not, and every round trip took a
// tick. With the secondaries' eventfd left unregistered (fanSilent) nothing
// wakes the primary for their completions on any kernel, the verdict held
// prompt. The detector off, the median round trip is a tick at least half:
// the latency is made, and measured. The detector on, the loop finds its
// missed wakes, bounds its wait to fanWakeTick, the median falls under half
// a tick, and the process's verdict reads slow.
func TestSecondaryWake_detectedInService(t *testing.T) {
	slowFanWake()
	was := secondaryWakeVerdict()
	t.Cleanup(func() { secondaryWake.Store(was) })
	secondaryWake.Store(wakePrompt)
	g0, unwatched := secondaryRoundTrips(t, loopHooks{fanSilent: true, fanUnwatched: true})
	n := len(unwatched)
	t.Logf("%s: unheard secondaries, detector off: round trip p50/p90/max %v/%v/%v", kernelRelease(), unwatched[n/2], unwatched[n*9/10], unwatched[n-1])
	if unwatched[n/2] < tickEvery/2 {
		t.Fatalf("unheard secondaries, detector off: p50 %v under half a tick — the rig made no latency to detect", unwatched[n/2])
	}
	if v := secondaryWakeVerdict(); v != wakePrompt {
		t.Fatalf("verdict %s with the detector off, want prompt", v)
	}
	// The control's loop stops before the second starts: its thread, woken
	// every tick for the connection it still holds, would keep the second P
	// of a GOMAXPROCS=2 runner busy with the other loop's, leaving the
	// client's read to the netpoll sysmon runs every 10 ms — a round trip of
	// a tick the loop did not cause (#1338's CI VMs).
	_ = g0.Close()
	_, watched := secondaryRoundTrips(t, loopHooks{fanSilent: true})
	t.Logf("%s: unheard secondaries, detector on: round trip p50/p90/max %v/%v/%v", kernelRelease(), watched[n/2], watched[n*9/10], watched[n-1])
	if watched[n/2] >= tickEvery/2 {
		t.Fatalf("unheard secondaries, detector on: p50 %v, at least half a tick (%v): the loop never bounded its wait", watched[n/2], tickEvery/2)
	}
	if v := secondaryWakeVerdict(); v != wakeSlow {
		t.Fatalf("verdict %s once the loop found its wake slow, want slow", v)
	}
}

// primaryBatches is the batches of loop 0's primary ring: one a wait.
func primaryBatches(g *Group) uint64 {
	for _, r := range g.State().RingStates {
		if r.Loop == 0 && r.Ring == 0 {
			return r.Batches
		}
	}
	return 0
}

// TestSecondaryWake_idleKeepsTheTick pins the bound's cost: with the verdict
// slow, a two-ring loop that serves nothing waits the tick, not
// fanWakeTick — a loop at rest wakes no more often on a slow kernel than on
// another —, and only a secondary holding something the kernel may complete
// shortens the primary's wait (#703).
func TestSecondaryWake_idleKeepsTheTick(t *testing.T) {
	slowFanWake()
	was := secondaryWakeVerdict()
	t.Cleanup(func() { secondaryWake.Store(was) })
	secondaryWake.Store(wakeSlow)
	g, _ := codeCase(t, func(cfg *Config) {
		cfg.Value.Loops = 1
		cfg.Value.Rings = 2
	})
	time.Sleep(50 * time.Millisecond)
	const idle = 300 * time.Millisecond
	before := primaryBatches(g)
	time.Sleep(idle)
	ran := primaryBatches(g) - before
	t.Logf("an idle two-ring loop, verdict slow: %d batches in %v", ran, idle)
	// The tick allows idle/tickEvery waits, 30; fanWakeTick would allow 300.
	if limit := uint64(2 * idle / tickEvery); ran > limit {
		t.Fatalf("an idle two-ring loop, verdict slow, ran %d batches in %v, want at most %d: it waits %v, not the tick", ran, idle, limit, fanWakeTick)
	}
}

// TestSecondaryWake_plainWait pins which loop gets the shortened wait: a
// secondary with a slot held or an accept armed, never the primary's own,
// and the tick passes through unchanged.
func TestSecondaryWake_plainWait(t *testing.T) {
	fan := kernelTimespec{nsec: int64(fanWakeTick)}
	tick := kernelTimespec{nsec: int64(tickEvery)}
	primary := &uRing{r: &ring{conns: make([]connCtl, 1)}, armed: []int{1}}
	secondary := &uRing{r: &ring{conns: make([]connCtl, 1), free: []int32{0}}, armed: []int{0}}
	ul := &uLoop{rings: []*uRing{primary, secondary}}
	if got := ul.plainWait(fan); got != tick {
		t.Fatalf("a secondary holding nothing: %d ns, want the tick", got.nsec)
	}
	secondary.armed[0] = 1
	if got := ul.plainWait(fan); got != fan {
		t.Fatalf("a secondary with an accept armed: %d ns, want %v", got.nsec, fanWakeTick)
	}
	secondary.armed[0], secondary.r.free = 0, nil
	if got := ul.plainWait(fan); got != fan {
		t.Fatalf("a secondary holding a slot: %d ns, want %v", got.nsec, fanWakeTick)
	}
	if got := ul.plainWait(tick); got != tick {
		t.Fatalf("the tick: %d ns, want it unchanged", got.nsec)
	}
}
