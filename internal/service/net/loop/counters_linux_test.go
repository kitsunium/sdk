//go:build linux

package loop

import (
	"bufio"
	"io"
	"strconv"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// ringSums is st's per-ring counters summed over every ring of every loop.
type ringSums struct {
	in, out, refused, spill, timeouts, idle uint64
	pending                                 int64
}

// sumRings adds up st's rings.
func sumRings(st corenet.LoopStateValue) ringSums {
	var s ringSums
	for _, r := range st.RingStates {
		s.in += r.BytesIn
		s.out += r.BytesOut
		s.refused += r.OutputRefused
		s.spill += r.SpillClosed
		s.timeouts += r.Timeouts
		s.idle += r.IdleClosed
		s.pending += r.PendingOutput
	}
	return s
}

// waitSums polls g's State until cond holds of its summed counters, and
// returns them; the counters are published at each batch's end.
func waitSums(t *testing.T, g *Group, what string, cond func(ringSums, corenet.LoopStateValue) bool) ringSums {
	t.Helper()
	var s ringSums
	waitFor(t, what, func() bool {
		st := g.State()
		s = sumRings(st)
		return cond(s, st)
	})
	return s
}

// TestState_bytes holds #975's first criterion: a group that served N requests
// of a bytes with answers of b bytes reports BytesIn == N*a and BytesOut == N*b
// over its rings, and PendingOutput 0 once every peer read its answers.
func TestState_bytes(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		g, addr := serve(t, b, &lineH{}, func(c *Config) { c.Value.Loops = 2 })
		const conns, each, line = 4, 25, "hello, loop\n"
		for range conns {
			c := dial(t, addr)
			r := bufio.NewReader(c)
			for range each {
				if _, err := c.Write([]byte(line)); err != nil {
					t.Fatal(err)
				}
				if got, err := r.ReadString('\n'); err != nil || got != line {
					t.Fatalf("echo = %q, %v", got, err)
				}
			}
		}
		const want uint64 = conns * each * uint64(len(line))
		s := waitSums(t, g, "every byte counted", func(s ringSums, _ corenet.LoopStateValue) bool {
			return s.in == want && s.out == want && s.pending == 0
		})
		if s.in != want || s.out != want || s.pending != 0 {
			t.Fatalf("in=%d out=%d pending=%d, want %d %d 0", s.in, s.out, s.pending, want, want)
		}
	})
}

// TestState_refusedAndSlab holds the output refusals and the slab's gauges: a
// 32 MiB answer past a 256 KiB output ceiling, more than the socket buffers hold, is refused with LoopOutputFull
// until the peer reads, every byte counted once it is read, the slab mapped,
// within its budget, and empty again once the connection is gone.
func TestState_refusedAndSlab(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		g, addr := serve(t, b, &lineH{}, func(c *Config) { c.Value.OutputCap = 256 << 10 })
		const n = 32 << 20
		c := dial(t, addr)
		if _, err := c.Write([]byte("BIG " + strconv.Itoa(n) + "\n")); err != nil {
			t.Fatal(err)
		}
		// Nothing is read yet: the output waits, a gauge, and the ceiling
		// refuses the rest.
		waitSums(t, g, "output waiting", func(s ringSums, st corenet.LoopStateValue) bool {
			return s.pending > 0 && s.refused > 0 && st.SlabInUse > 0
		})
		if _, err := io.CopyN(io.Discard, c, n); err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
		s := waitSums(t, g, "the answer sent", func(s ringSums, st corenet.LoopStateValue) bool {
			return s.out == uint64(n) && s.pending == 0 && st.SlabInUse == 0
		})
		st := g.State()
		if s.refused == 0 || st.SlabMapped <= 0 || st.SlabInUse > st.SlabBudget || st.SlabBudget <= 0 {
			t.Fatalf("refused=%d slab in use %d mapped %d budget %d", s.refused, st.SlabInUse, st.SlabMapped, st.SlabBudget)
		}
	})
}

// TestState_closedWithUnsent holds the gauge against a connection that goes
// with output unsent: its bytes are owed to nobody, and PendingOutput says so
// once the slot is released, the kernel's flight included.
func TestState_closedWithUnsent(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		h := &lineH{}
		g, addr := serve(t, b, h, func(c *Config) { c.Value.OutputCap = 256 << 10 })
		c := dial(t, addr)
		if _, err := c.Write([]byte("BIG " + strconv.Itoa(32<<20) + "\n")); err != nil {
			t.Fatal(err)
		}
		waitSums(t, g, "output waiting", func(s ringSums, _ corenet.LoopStateValue) bool { return s.pending > 0 })
		_ = c.Close()
		waitFor(t, "OnClose", func() bool { _, n := h.counts(); return n == 1 })
		s := waitSums(t, g, "nothing owed", func(s ringSums, st corenet.LoopStateValue) bool {
			return s.pending == 0 && st.SlabInUse == 0
		})
		if s.pending != 0 {
			t.Fatalf("pending = %d after the connection went", s.pending)
		}
	})
}

// TestState_closesAndTimeouts holds the counters of what the engine decides: a
// peer sending past SpillCap with no newline is SpillClosed, a silent one is
// IdleClosed under IdleTimeout, and a deadline the handler set is a Timeout.
func TestState_closesAndTimeouts(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		t.Run("spill", func(t *testing.T) {
			h := &lineH{}
			g, addr := serve(t, b, h, func(c *Config) { c.Value.SpillCap = 8 << 10 })
			c := dial(t, addr)
			_, _ = c.Write(make([]byte, 64<<10))
			waitFor(t, "the spill close", func() bool { _, n := h.counts(); return n == 1 })
			if s := waitSums(t, g, "SpillClosed", func(s ringSums, _ corenet.LoopStateValue) bool { return s.spill == 1 }); s.idle != 0 {
				t.Fatalf("a spill close counted as idle: %+v", s)
			}
		})
		t.Run("idle", func(t *testing.T) {
			h := &lineH{}
			g, addr := serve(t, b, h, func(c *Config) { c.IdleTimeout = 100 * time.Millisecond })
			dial(t, addr)
			waitFor(t, "the idle close", func() bool { _, n := h.counts(); return n == 1 })
			if s := waitSums(t, g, "IdleClosed", func(s ringSums, _ corenet.LoopStateValue) bool { return s.idle == 1 }); s.spill != 0 {
				t.Fatalf("an idle close counted as spill: %+v", s)
			}
		})
		t.Run("timeout", func(t *testing.T) {
			h := &lineH{}
			h.onData = func(io corenet.LoopIO, ref corenet.LoopConnRef, _ []byte) {
				_ = io.SetDeadline(ref, 30*time.Millisecond)
			}
			g, addr := serve(t, b, h, nil)
			c := dial(t, addr)
			_, _ = c.Write([]byte("arm\n"))
			waitSums(t, g, "a deadline fired", func(s ringSums, _ corenet.LoopStateValue) bool { return s.timeouts >= 1 })
		})
	})
}
