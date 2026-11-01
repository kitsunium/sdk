//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — an io_uring loop's rings fill in order: a ring past the
// first accepts only once those before it are near their table's end, so a
// load one ring holds costs what one ring costs (ADR 0203 §D1).
package loop

import (
	"fmt"
	"io"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// fillBenchConns is how many keep-alive connections the fill benchmark
	// holds: well under one table, more than one accept wave.
	fillBenchConns = 3000
	// fillBenchWave is how many of them send one request each per iteration.
	fillBenchWave = 64
)

// TestURingFill_inOrder pins fillStep on three rings of a 128-entry table:
// only ring 0 accepts while it has room; ring 1 opens once ring 0's free
// entries fall to the margin and closes again only past twice it; ring 2
// opens only once ring 1 is full too; a ring passed over — drained by the
// watchdog — counts as full.
func TestURingFill_inOrder(t *testing.T) {
	const table = 128
	margin := fillMargin(table)
	lp := &loop{sz: sizing{tableSize: table}}
	ul := &uLoop{lp: lp}
	for i := range 3 {
		rr := &uRing{ul: ul}
		rr.r = &ring{index: i}
		ul.rings = append(ul.rings, rr)
	}
	use := func(i, n int) { ul.rings[i].r.conns = make([]connCtl, n) }
	step := func() {
		for _, rr := range ul.rings {
			rr.fillStep()
		}
	}
	want := func(what string, closed ...bool) {
		t.Helper()
		for i, rr := range ul.rings {
			if got := rr.fill&fillClosed != 0; got != closed[i] {
				t.Fatalf("%s: ring %d closed=%v, want %v", what, i, got, closed[i])
			}
		}
	}
	step()
	want("empty", false, true, true)
	use(0, table-margin-1)
	step()
	want("ring 0 one above the margin", false, true, true)
	use(0, table-margin)
	step()
	want("ring 0 at the margin", false, false, true)
	use(0, table-2*margin)
	step()
	want("ring 0 back at twice the margin", false, false, true)
	use(0, table-2*margin-1)
	step()
	want("ring 0 past twice the margin", false, true, true)
	use(0, table)
	use(1, table-margin)
	step()
	want("rings 0 and 1 full", false, false, false)
	use(0, 0)
	use(1, 0)
	ul.rings[0].r.draining = true
	step()
	want("ring 0 drained by the watchdog", false, false, true)
}

// TestConformance_ringsFillInOrder serves connections well under one table
// on a loop of three rings: every one of them is accepted by ring 0, and the
// two others hold nothing.
func TestConformance_ringsFillInOrder(t *testing.T) {
	const n = 300
	if lim := descriptorLimit(); lim < 2*n+256 {
		t.Skipf("RLIMIT_NOFILE %d is too low", lim)
	}
	eachBackend(t, func(t *testing.T, b backendCase) {
		onlyIOURing(t, b)
		g, addr := serve(t, b, &lineH{}, func(c *Config) { c.Value.Loops, c.Value.Rings = 1, 3 })
		conns := heard(t, addr, n)
		defer func() {
			for _, c := range conns {
				_ = c.Close()
			}
		}()
		st := g.State().RingStates
		if len(st) != 3 || st[0].Accepted != n || st[1].Accepted != 0 || st[2].Accepted != 0 {
			got := make([]uint64, len(st))
			for i, r := range st {
				got[i] = r.Accepted
			}
			t.Fatalf("accepted per ring %v, want [%d 0 0]", got, n)
		}
	})
}

// BenchmarkURingRings_fill measures one request's round trip, fillBenchWave
// at a time, over fillBenchConns keep-alive connections on a loop of one
// ring and of three: with the rings filled in order the two cost the same
// (ns/req), where connections spread over three rings paid each batch the
// secondaries' submits and the eventfd that wakes the primary.
func BenchmarkURingRings_fill(b *testing.B) {
	if err := probeBackend(corenet.LoopBackendIOURing); err != nil {
		b.Skipf("io_uring not present: %v", err)
	}
	if lim := descriptorLimit(); lim < 2*fillBenchConns+256 {
		b.Skipf("RLIMIT_NOFILE %d is too low", lim)
	}
	for _, rings := range []int{1, 3} {
		b.Run(fmt.Sprintf("rings=%d", rings), func(b *testing.B) {
			bc := backendCase{"iouring", corenet.LoopBackendIOURing}
			_, addr := serve(b, bc, &lineH{}, func(c *Config) { c.Value.Loops, c.Value.Rings = 1, rings })
			conns := heard(b, addr, fillBenchConns)
			defer func() {
				for _, c := range conns {
					_ = c.Close()
				}
			}()
			for _, c := range conns {
				_ = c.SetDeadline(time.Time{})
			}
			msg, got := []byte("r\n"), make([]byte, 2)
			at := 0
			for b.Loop() {
				wave := conns[at : at+fillBenchWave]
				at = (at + fillBenchWave) % (fillBenchConns - fillBenchWave)
				for _, c := range wave {
					if _, err := c.Write(msg); err != nil {
						b.Fatal(err)
					}
				}
				for _, c := range wave {
					if _, err := io.ReadFull(c, got); err != nil {
						b.Fatal(err)
					}
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*fillBenchWave), "ns/req")
		})
	}
}
