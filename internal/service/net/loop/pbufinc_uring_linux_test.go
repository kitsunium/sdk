//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — io_uring's provided buffers consumed incrementally
// (IOU_PBUF_RING_INC, ADR 0204): a buffer serves recv after recv while its
// completions carry IORING_CQE_F_BUF_MORE, each from where the last
// stopped, and goes back to the kernel only after the completion without it.
package loop

import (
	"bufio"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// bufUse is one recv completion's use of a provided buffer.
type bufUse struct {
	bid     uint16
	off, n  int
	bufMore bool
}

// TestURingPbufInc_bufMore serves short lines one at a time on a ring of
// four 256-byte buffers, filled with 0xAA and returned in a scrambled order:
// every echo is exact; a buffer serves several recvs, each starting where
// the previous one stopped, the earlier ones with BUF_MORE; and a buffer goes
// back to the kernel only after its completion without BUF_MORE, and serves
// again from its first byte.
func TestURingPbufInc_bufMore(t *testing.T) {
	const lines, size = 200, 256
	eachBackend(t, func(t *testing.T, b backendCase) {
		onlyIOURing(t, b)
		var mu sync.Mutex
		var uses []bufUse
		var faults []string
		// open is, per bid, whether the kernel may still hand it out.
		open := map[uint16]bool{}
		next := map[uint16]int{}
		hooks := loopHooks{bufs: 4, bufSize: size, fill: true, shuffle: true}
		hooks.bufSeen = func(bid uint16, off, n int, bufMore, _, returned bool) {
			mu.Lock()
			defer mu.Unlock()
			if returned {
				if open[bid] {
					faults = append(faults, fmt.Sprintf("bid %d returned while the kernel still had it (BUF_MORE)", bid))
				}
				next[bid] = 0
				return
			}
			uses = append(uses, bufUse{bid, off, n, bufMore})
			if off != next[bid] {
				faults = append(faults, fmt.Sprintf("bid %d: a completion at %d, the last stopped at %d", bid, off, next[bid]))
			}
			next[bid] = off + n
			open[bid] = bufMore
		}
		g, addr := serve(t, b, &lineH{}, func(c *Config) {
			c.Value.Loops, c.Value.Rings = 1, 1
			c.hooks = hooks
		})
		if st := capStatus(g, corenet.CapabilityURingPbufInc); st != statusOn {
			t.Skipf("uring-pbuf-inc %s on %s", st, kernelRelease())
		}
		c := dial(t, addr)
		_ = c.SetDeadline(time.Now().Add(20 * time.Second))
		r := bufio.NewReader(c)
		for i := range lines {
			line := fmt.Sprintf("line %03d of a few dozen bytes\n", i)
			if _, err := c.Write([]byte(line)); err != nil {
				t.Fatal(err)
			}
			got, err := r.ReadString('\n')
			if err != nil || got != line {
				t.Fatalf("line %d: %q, %v, want %q", i, got, err, line)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if len(faults) > 0 {
			t.Fatalf("%d faults, the first: %s", len(faults), faults[0])
		}
		shared, kept := 0, 0
		for _, u := range uses {
			if u.bufMore {
				kept++
			}
			if u.off > 0 {
				shared++
			}
		}
		t.Logf("%d completions: %d with BUF_MORE, %d past a buffer's first byte", len(uses), kept, shared)
		if kept == 0 || shared == 0 || shared < len(uses)/2 {
			t.Fatalf("%d completions, %d with BUF_MORE, %d past a first byte: the buffers were not shared", len(uses), kept, shared)
		}
	})
}

// TestURingPbufInc_refused plays a kernel that refuses IOU_PBUF_RING_INC
// (before 6.12): the group still starts on io_uring, its rings take a whole
// buffer a recv, the row reads absent with the refusal, and the suite's
// exchange is served.
func TestURingPbufInc_refused(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		onlyIOURing(t, b)
		g, addr := serve(t, b, &lineH{}, func(c *Config) {
			c.Value.Rings = 2
			c.hooks = loopHooks{failStage: "pbuf_inc"}
		})
		if g.State().Backend != corenet.LoopBackendIOURing {
			t.Fatalf("backend %s, want io_uring with the buffers consumed whole", g.State().Backend)
		}
		for _, rr := range uringOf(g).rings {
			if rr.u.bufInc {
				t.Fatal("a ring consumes its buffers incrementally after the kernel refused it")
			}
		}
		row := capRow(t, g.State(), corenet.CapabilityURingPbufInc)
		if row.Status != statusAbsent || !strings.Contains(row.Reason, "IOU_PBUF_RING_INC refused") {
			t.Fatalf("uring-pbuf-inc row %+v, want absent with the refusal", row)
		}
		for range 4 {
			echoBigClose(t, addr)
		}
	})
}

// consumesIncrementally reports whether a ring of g's first loop consumes
// its buffers incrementally.
func consumesIncrementally(g *Group) bool {
	for _, rr := range uringOf(g).rings {
		if rr.u.bufInc {
			return true
		}
	}
	return false
}

// capStatus is the status g's report gives c.
func capStatus(g *Group, c corenet.Capability) string {
	for _, row := range g.State().Capabilities {
		if row.Name == c {
			return row.Status
		}
	}
	return ""
}
