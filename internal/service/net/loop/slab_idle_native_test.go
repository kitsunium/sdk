//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

// Package loop — a loop left with no connection still gives its slab back
// (#809), on every native backend.
package loop

import (
	"bytes"
	stdnet "net"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// slabIdleCap is the test's SpillCap and OutputCap: the slab's classes
	// run from 2 KiB to it, six of them.
	slabIdleCap = 64 << 10
	// slabIdleConns is how many clients the burst holds a spill for.
	slabIdleConns = 128
	// slabIdleLine is each client's unterminated line, held in a 64 KiB
	// chunk: the burst maps 8 MiB of that class alone.
	slabIdleLine = 60 << 10
	// slabIdleWithin is #809's bound: back to one arena a class within a
	// second of idle.
	slabIdleWithin = time.Second
	// slabIdleQuiet is how long a loop with nothing left to give back is
	// watched for a batch.
	slabIdleQuiet = 200 * time.Millisecond
)

// slabFloor is one arena a class for a slab whose largest chunk is maxChunk:
// what the shrink keeps.
func slabFloor(maxChunk int) int64 {
	floor := int64(0)
	for size := 1 << minClassShift; size <= maxChunk; size <<= 1 {
		floor += int64(max(arenaBytes, size))
	}
	return floor
}

// loopBatches sums every ring's batches.
func loopBatches(st corenet.LoopStateValue) uint64 {
	n := uint64(0)
	for _, r := range st.RingStates {
		n += r.Batches
	}
	return n
}

// TestSlab_idleLoopGivesBack pins #809's shrink on a loop every client left:
// a burst of spills maps the slab, the clients close, and with no event
// after the last close the slab's mapped bytes fall to one arena a class
// within a second. Then, nothing left to give back, an epoll or kqueue loop
// with no connection waits for an event again: no batch runs.
func TestSlab_idleLoopGivesBack(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		g, addr := serve(t, b, &lineH{}, func(c *Config) {
			c.Value.Loops = 1
			c.Value.SpillCap = slabIdleCap
			c.Value.OutputCap = slabIdleCap
		})
		line := bytes.Repeat([]byte{'x'}, slabIdleLine)
		conns := make([]stdnet.Conn, slabIdleConns)
		for i := range conns {
			conns[i] = dial(t, addr)
			if _, err := conns[i].Write(line); err != nil {
				t.Fatal(err)
			}
		}
		waitFor(t, "every spill held", func() bool { return g.State().SlabInUse >= slabIdleConns*slabIdleLine })
		floor := slabFloor(slabIdleCap)
		burst := g.State().SlabMapped
		if burst < 4*floor {
			t.Fatalf("the burst mapped %d, want at least %d for the test to mean anything", burst, 4*floor)
		}
		for _, c := range conns {
			_ = c.Close()
		}
		waitFor(t, "every close", func() bool { return g.Live() == 0 })
		idle := time.Now()
		for st := g.State(); st.SlabMapped > floor || st.SlabInUse != 0; st = g.State() {
			if time.Since(idle) > slabIdleWithin {
				t.Fatalf("%s after %v of idle: mapped %d (burst %d), in use %d; want at most %d, one arena a class (#809)",
					b.name, slabIdleWithin, st.SlabMapped, burst, st.SlabInUse, floor)
			}
			time.Sleep(5 * time.Millisecond)
		}
		// io_uring waits on the tick whatever it holds; epoll and kqueue
		// sleep until an event once nothing is timed nor left to give back.
		if b.backend == corenet.LoopBackendIOURing {
			return
		}
		before := loopBatches(g.State())
		time.Sleep(slabIdleQuiet)
		if after := loopBatches(g.State()); after != before {
			t.Fatalf("%s ran %d batches in %v with no connection and nothing to give back, want 0", b.name, after-before, slabIdleQuiet)
		}
	})
}
