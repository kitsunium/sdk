//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — a drain's lingering close keeps reading on io_uring
// whichever way its recv ended (#1385): out of buffers, or cancelled.
package loop

import (
	"bufio"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestURing_lingerReadsOnAfterStarveOrCancel holds the recv of a drain's
// lingering close armed on the paths that re-arm an open connection's: a
// recv starved of buffers, queued then re-armed as buffers come back, and a
// recv cancelled. recvBytes re-armed a lingering slot; these paths took it
// for closed, so a linger that met them stopped reading, and the close at
// its bound found input unread and reset the peer, which could lose the
// answers the linger waits on.
func TestURing_lingerReadsOnAfterStarveOrCancel(t *testing.T) {
	b := backendCase{"iouring", corenet.LoopBackendIOURing}
	if err := probeBackend(b.backend); err != nil {
		t.Skipf("io_uring not present: %v", err)
	}
	type tc struct {
		name string
		// end ends the slot's recv on the loop's thread as a completion
		// would.
		end func(rr *uRing, slot int32)
	}
	cases := []tc{
		{name: "starved, then re-armed", end: func(rr *uRing, slot int32) {
			rr.recvStarved(slot)
			rr.rearmStarved()
		}},
		{name: "cancelled", end: func(rr *uRing, slot int32) {
			rr.recvCancelled(slot, false)
		}},
	}
	runCase := func(t *testing.T, c tc) {
		g, addr := serve(t, b, &lineH{}, func(c *Config) { c.Value.Rings = 1 })
		conn := dial(t, addr)
		r := bufio.NewReader(conn)
		if _, err := conn.Write([]byte("hi\n")); err != nil {
			t.Fatal(err)
		}
		if line, err := r.ReadString('\n'); err != nil || line != "hi\n" {
			t.Fatalf("echo = %q, %v", line, err)
		}
		rr := uringOf(g).rings[0]
		var armed int32
		var failure string
		onLoop(t, g, func() {
			slot := int32(-1)
			for i := range rr.r.conns {
				if rr.r.conns[i].state == stOpen {
					slot = int32(i)
					break
				}
			}
			if slot < 0 {
				failure = "no open slot"
				return
			}
			cc := &rr.r.conns[slot]
			// A drain's close lingering, its recv just ended.
			cc.state, cc.reason, cc.linger = stClosing, rDrain, lingerHalf
			ops := cc.ops
			c.end(rr, slot)
			armed = cc.ops - ops
			// Back to an open connection, the extra recv its own, so the
			// group's close ends it as any other.
			cc.state, cc.reason, cc.linger = stOpen, rNone, lingerNone
		})
		if failure != "" {
			t.Fatal(failure)
		}
		if armed != 1 {
			t.Fatalf("%d recvs armed for the lingering slot, want 1: it would read no more", armed)
		}
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { runCase(t, c) })
	}
}
