//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — a starved recv is re-armed only for the connection that
// starved, never for the next one its slot serves (#1471).
package loop

import (
	"slices"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestURing_starvedRearmsOnlyItsConnection queues a connection's recv as
// starved of buffers and re-arms the queue only once the slot has, or has
// not, been handed on. Until the queue's entry carried the connection's
// generation, a connection that finished while queued left its slot's
// number behind, and rearmStarved armed a second recv on the connection
// the slot served next, beside the one take armed: two recvs on one socket
// split its bytes between requests the loop handles independently. The
// queue is set aside while the slot changes hands, as an empty ring holds it
// back, and the slot is released and taken by the loop itself.
func TestURing_starvedRearmsOnlyItsConnection(t *testing.T) {
	b := backendCase{"iouring", corenet.LoopBackendIOURing}
	if err := probeBackend(b.backend); err != nil {
		t.Skipf("io_uring not present: %v", err)
	}
	type tc struct {
		name string
		// handOn finishes the starved connection and has its slot serve a
		// new one before the queue is re-armed.
		handOn bool
		// want is how many recvs the re-arm adds on the slot.
		want int32
	}
	cases := []tc{
		{name: "the connection still open is re-armed", handOn: false, want: 1},
		{name: "the slot handed to a new connection is not", handOn: true, want: 0},
	}
	runCase := func(t *testing.T, c tc) {
		g, addr := serve(t, b, &lineH{}, func(c *Config) { c.Value.Rings = 1 })
		rr := uringOf(g).rings[0]
		first := dial(t, addr)
		echo(t, first, "first\n")
		slot, gen := int32(-1), uint32(0)
		var queued int
		// restore puts back the queue as the first connection left it, set
		// aside at once: the batch's end would re-arm it.
		var restore func()
		onLoop(t, g, func() {
			// The only open slot: the first connection's.
			for i := range rr.r.conns {
				if rr.r.conns[i].state == stOpen {
					slot, gen = int32(i), rr.r.conns[i].gen
					break
				}
			}
			if slot < 0 {
				return
			}
			rr.recvStarved(slot)
			queued = len(rr.starved)
			held := slices.Clone(rr.starved)
			rr.starved = rr.starved[:0]
			restore = func() { rr.starved = append(rr.starved[:0], held...) }
		})
		if slot < 0 || queued != 1 {
			t.Fatalf("slot %d, %d entries queued: want one open slot, queued once", slot, queued)
		}
		if c.handOn {
			if err := first.Close(); err != nil {
				t.Fatal(err)
			}
			// The slot reads stFree from the moment its close is submitted,
			// but joins the free list only at the close's completion: a
			// connection accepted in between takes another slot. Wait for
			// the release itself.
			waitFor(t, "the first connection's slot back on the free list", func() bool {
				var free bool
				onLoop(t, g, func() { free = slices.Contains(rr.r.free, slot) })
				return free
			})
			second := dial(t, addr)
			echo(t, second, "second\n")
			var next uint32
			onLoop(t, g, func() { next = rr.r.conns[slot].gen })
			if next == gen {
				t.Fatalf("slot %d still at generation %d: the second connection took another slot", slot, gen)
			}
		}
		var armed int32
		onLoop(t, g, func() {
			// The queue as it stood when the first connection starved.
			restore()
			ops := rr.r.conns[slot].ops
			rr.rearmStarved()
			armed = rr.r.conns[slot].ops - ops
		})
		if armed != c.want {
			t.Fatalf("%d recvs armed on slot %d by the re-arm, want %d", armed, slot, c.want)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
