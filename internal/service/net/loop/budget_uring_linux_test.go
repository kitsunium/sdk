//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — an io_uring batch bounded in time (LoopValue.BatchBudget,
// ADR 0206): a batch stops reaping once its budget ran out, the next one
// reaps what was left without waiting, and the answers of a long batch leave
// as soon as it ends.
package loop

import (
	"fmt"
	"io"
	stdnet "net"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// budgetConns is how many connections the budget's bursts use.
	budgetConns = 400
	// budgetWork is how long the test handler spins per line: what an
	// application's request costs, so a burst's batch lasts.
	budgetWork = 100 * time.Microsecond
)

// TestURingBudget_reapShape pins reapShape: no budget, or reaps already
// small, keep CQBatch reapRounds times; a budget reaps the same share in
// reaps of budgetChunk.
func TestURingBudget_reapShape(t *testing.T) {
	for _, c := range []struct {
		budget       time.Duration
		cqBatch      int
		chunk, round int
	}{
		{0, 512, 512, reapRounds},
		{time.Millisecond, 512, budgetChunk, reapRounds * 512 / budgetChunk},
		{time.Millisecond, 8, 8, reapRounds},
	} {
		chunk, rounds := reapShape(&sizing{batchBudget: c.budget, cqBatch: c.cqBatch})
		if chunk != c.chunk || rounds != c.round {
			t.Errorf("reapShape(budget %v, cqBatch %d) = %d, %d; want %d, %d", c.budget, c.cqBatch, chunk, rounds, c.chunk, c.round)
		}
	}
}

// spin busy-waits d: a handler's own work, which a sleep would not be.
func spin(d time.Duration) {
	for end := time.Now().Add(d); time.Now().Before(end); {
	}
}

// burst holds g's loop at its batch end, has every connection send one line,
// releases the loop and returns each echo's delay from the release, sorted.
func burst(t testing.TB, hold *atomic.Bool, held, release chan struct{}, holder stdnet.Conn, conns []stdnet.Conn) []time.Duration {
	t.Helper()
	hold.Store(true)
	if _, err := holder.Write([]byte("hold\n")); err != nil {
		t.Fatal(err)
	}
	<-held
	for i, c := range conns {
		if _, err := fmt.Fprintf(c, "q %04d\n", i); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	start := time.Now()
	release <- struct{}{}
	got := make([]byte, len("hold\n"))
	if _, err := io.ReadFull(holder, got); err != nil {
		t.Fatal(err)
	}
	lat := make([]time.Duration, len(conns))
	var wg sync.WaitGroup
	for i, c := range conns {
		wg.Go(func() {
			want := fmt.Sprintf("q %04d\n", i)
			buf := make([]byte, len(want))
			_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
			if _, err := io.ReadFull(c, buf); err != nil || string(buf) != want {
				t.Errorf("connection %d: %q, %v, want %q", i, buf, err, want)
			}
			lat[i] = time.Since(start)
		})
	}
	wg.Wait()
	slices.Sort(lat)
	return lat
}

// budgetGroup serves lines that cost budgetWork each on a loop of rings
// rings with budget, the loop held at its batch end on demand.
func budgetGroup(t testing.TB, budget time.Duration, rings int) (*Group, *atomic.Bool, chan struct{}, chan struct{}, stdnet.Conn, []stdnet.Conn) {
	t.Helper()
	var hold atomic.Bool
	held, release := make(chan struct{}, 1), make(chan struct{})
	h := &lineH{}
	h.onData = func(corenet.LoopIO, corenet.LoopConnRef, []byte) { spin(budgetWork) }
	h.batchEnd = func(corenet.LoopIO) {
		if hold.CompareAndSwap(true, false) {
			held <- struct{}{}
			<-release
		}
	}
	bc := backendCase{"iouring", corenet.LoopBackendIOURing}
	g, addr := serve(t, bc, h, func(c *Config) {
		c.Value.Loops, c.Value.Rings = 1, rings
		c.Value.BatchBudget = corenet.DurationValue(budget)
		if rings > 1 {
			c.hooks.primaryIdle = true
		}
	})
	all := heard(t, addr, budgetConns+1)
	t.Cleanup(func() {
		for _, c := range all {
			_ = c.Close()
		}
	})
	for _, c := range all {
		_ = c.SetDeadline(time.Time{})
	}
	return g, &hold, held, release, all[budgetConns], all[:budgetConns]
}

// cutCount sums g's rings' batches the budget cut.
func cutCount(g *Group) uint64 {
	var n uint64
	for _, r := range g.State().RingStates {
		n += r.BatchesCut
	}
	return n
}

// TestURingBudget_cutsBatches serves a burst of budgetConns lines, each
// costing budgetWork, on one ring and on a secondary, with no budget and a
// 2 ms one: every echo is exact; with the budget batches are cut and
// counted, the first echoes come sooner than when the whole burst is one
// batch, and none waits for a tick for want of a wait.
func TestURingBudget_cutsBatches(t *testing.T) {
	if err := probeBackend(corenet.LoopBackendIOURing); err != nil {
		t.Skipf("io_uring not present: %v", err)
	}
	if lim := descriptorLimit(); lim < 2*budgetConns+256 {
		t.Skipf("RLIMIT_NOFILE %d is too low", lim)
	}
	for _, rings := range []int{1, 2} {
		t.Run(fmt.Sprintf("rings=%d", rings), func(t *testing.T) {
			first := map[time.Duration]time.Duration{}
			for _, budget := range []time.Duration{0, 2 * time.Millisecond} {
				g, hold, held, release, holder, conns := budgetGroup(t, budget, rings)
				lat := burst(t, hold, held, release, holder, conns)
				cut := cutCount(g)
				t.Logf("budget %v: first echo %v, median %v, last %v; %d batches cut", budget, lat[0], lat[len(lat)/2], lat[len(lat)-1], cut)
				first[budget] = lat[0]
				if budget == 0 && cut != 0 {
					t.Fatalf("no budget, yet %d batches cut", cut)
				}
				if budget > 0 && cut == 0 {
					t.Fatalf("a burst of %d lines of %v each under a %v budget, no batch cut", budgetConns, budgetWork, budget)
				}
			}
			if first[2*time.Millisecond] >= first[0] {
				t.Fatalf("first echo %v with a budget, %v without: the budget did not answer sooner", first[2*time.Millisecond], first[0])
			}
		})
	}
}

// BenchmarkURingBatchBudget measures a held burst of budgetConns lines of
// budgetWork each, with no budget and with 1, 2 and 5 ms: the delay from the
// release to the first, median and last echo (first-ms, p50-ms, last-ms).
// Under no burst a budget costs nothing more than a clock read a reap:
// BenchmarkURingRings_fill measures that load.
func BenchmarkURingBatchBudget(b *testing.B) {
	if err := probeBackend(corenet.LoopBackendIOURing); err != nil {
		b.Skipf("io_uring not present: %v", err)
	}
	if lim := descriptorLimit(); lim < 2*budgetConns+256 {
		b.Skipf("RLIMIT_NOFILE %d is too low", lim)
	}
	for _, budget := range []time.Duration{0, time.Millisecond, 2 * time.Millisecond, 5 * time.Millisecond} {
		b.Run(fmt.Sprintf("budget=%v", budget), func(b *testing.B) {
			_, hold, held, release, holder, conns := budgetGroup(b, budget, 1)
			var firstSum, midSum, lastSum time.Duration
			n := 0
			for b.Loop() {
				lat := burst(b, hold, held, release, holder, conns)
				firstSum += lat[0]
				midSum += lat[len(lat)/2]
				lastSum += lat[len(lat)-1]
				n++
			}
			ms := func(d time.Duration) float64 { return float64(d) / float64(n) / float64(time.Millisecond) }
			b.ReportMetric(ms(firstSum), "first-ms")
			b.ReportMetric(ms(midSum), "p50-ms")
			b.ReportMetric(ms(lastSum), "last-ms")
		})
	}
}
