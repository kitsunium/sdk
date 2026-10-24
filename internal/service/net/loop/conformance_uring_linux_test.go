//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — the io_uring close's conformance: the end a closed
// connection's peer reads, and the sweep that bounds the sockets a close
// leaves pinned before 6.13.
package loop

import (
	"errors"
	"fmt"
	"io"
	stdnet "net"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// eofWithin is how soon a closed connection's peer must read the end.
const eofWithin = 200 * time.Millisecond

// readsEOF reports how long c took to read the end of the stream, failing the
// test past eofWithin or on bytes.
func readsEOF(t *testing.T, c stdnet.Conn, from time.Time, what string) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := c.Read(make([]byte, 64))
	took := time.Since(from)
	if n != 0 || (!errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET)) {
		t.Fatalf("%s: the peer read %d bytes, %v, want the end of the stream", what, n, err)
	}
	if took > eofWithin {
		t.Fatalf("%s: the peer read the end after %v, want under %v", what, took, eofWithin)
	}
}

// TestConformance_directCloseEOF pins io_uring's close on direct descriptors
// on every kernel the suite meets, 6.8 and 6.12 included: a keep-alive's
// recv is armed first, the oldest request of the table, as an idle
// connection's is; before 6.13 it holds the put of every file closed after
// it. The loop's close shuts each victim down first, so its peer still reads
// the end within 200 ms, whether the server closes or the peer finished
// first. The release check must have chosen direct descriptors.
func TestConformance_directCloseEOF(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		onlyIOURing(t, b)
		h := &lineH{}
		g, addr := serve(t, b, h, func(c *Config) { c.Value.Loops, c.Value.Rings = 1, 1 })
		if st := g.State(); !strings.HasSuffix(st.ProbeStage, "+direct") {
			t.Fatalf("probe stage %q on %s: the release check did not choose direct descriptors", st.ProbeStage, kernelRelease())
		}
		keeper := dial(t, addr)
		waitFor(t, "the keeper's open", func() bool { o, _ := h.counts(); return o == 1 })
		opened := 1
		for round := range 3 {
			// The server closes.
			v := dial(t, addr)
			opened++
			waitFor(t, "the victim's open", func() bool { o, _ := h.counts(); return o == opened })
			from := time.Now()
			_, _ = v.Write([]byte("CLOSE\n"))
			readsEOF(t, v, from, fmt.Sprintf("round %d, server close", round))
			// The peer finishes first.
			p := dial(t, addr)
			opened++
			waitFor(t, "the peer's open", func() bool { o, _ := h.counts(); return o == opened })
			from = time.Now()
			if err := p.(*stdnet.TCPConn).CloseWrite(); err != nil {
				t.Fatal(err)
			}
			readsEOF(t, p, from, fmt.Sprintf("round %d, peer first", round))
		}
		// The keeper was never disturbed.
		_, _ = keeper.Write([]byte("alive\n"))
		got := make([]byte, 6)
		_ = keeper.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(keeper, got); err != nil || string(got) != "alive\n" {
			t.Fatalf("the keeper = %q, %v\n%s", got, err, dumpGroup(g))
		}
		t.Logf("kernel %s: stage %s, deferred put %v", kernelRelease(), g.State().ProbeStage, uringOf(g).rings[0].sweepOn)
	})
}

// uringOf is the first io_uring loop of g. Its rings, and whether they
// sweep, are set before Start returns.
func uringOf(g *Group) *uLoop {
	return g.ctl.(*uringRunner).loops[0]
}

// onLoop runs fn on g's first loop's thread and waits for it.
func onLoop(t *testing.T, g *Group, fn func()) {
	t.Helper()
	done := make(chan struct{})
	op := func(_ corenet.LoopIO, _ uintptr) { fn(); close(done) }
	if err := g.loops[0].post.post(op, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop did not run the post within 2 s")
	}
}

// tcpAlloc is /proc/net/sockstat's allocated TCP sockets.
func tcpAlloc(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile("/proc/net/sockstat")
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(string(b))
	for i := range f {
		if f[i] == "TCP:" {
			for j := i + 1; j+1 < len(f) && !strings.HasSuffix(f[j], ":"); j += 2 {
				if f[j] == "alloc" {
					n, _ := strconv.Atoi(f[j+1])
					return n
				}
			}
		}
	}
	t.Fatal("no TCP alloc in /proc/net/sockstat")
	return 0
}

// TestConformance_sweepReleases pins the sweep where a close's put is
// deferred (before 6.13): with a keep-alive's recv armed first, closed
// sockets stay allocated until a sweep cancels and re-arms the keeper's
// recv; after it they are put, and the keeper still echoes, nothing lost.
// Where the put is not deferred, no sweep runs and the closed sockets are
// put at once.
func TestConformance_sweepReleases(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		onlyIOURing(t, b)
		const victims = 256
		h := &lineH{}
		g, addr := serve(t, b, h, func(c *Config) {
			c.Value.Loops, c.Value.Rings = 1, 1
			c.hooks = loopHooks{sweepEvery: 600}
		})
		ul := uringOf(g)
		keeper := dial(t, addr)
		waitFor(t, "the keeper's open", func() bool { o, _ := h.counts(); return o == 1 })
		base := tcpAlloc(t)
		for i := range victims {
			c, err := stdnet.DialTimeout("tcp", addr, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = c.Write([]byte("CLOSE\n"))
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			if n, err := c.Read(make([]byte, 1)); n != 0 || err == nil {
				t.Fatalf("victim %d read %d, %v", i, n, err)
			}
			_ = c.Close()
		}
		waitFor(t, "every close", func() bool { _, cl := h.counts(); return cl == victims })
		time.Sleep(100 * time.Millisecond)
		pinned := tcpAlloc(t) - base
		var sweeps int
		onLoop(t, g, func() { sweeps = ul.rings[0].sweeps })
		t.Logf("kernel %s: deferred put %v, %d sockets allocated after %d closes, %d sweeps", kernelRelease(), ul.rings[0].sweepOn, pinned, victims, sweeps)
		if !ul.rings[0].sweepOn {
			if sweeps != 0 {
				t.Fatalf("%d sweeps where the put is not deferred", sweeps)
			}
			return
		}
		deadline := time.Now().Add(15 * time.Second)
		for sweeps == 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
			onLoop(t, g, func() { sweeps = ul.rings[0].sweeps })
		}
		if sweeps == 0 {
			t.Fatal("no sweep within 15 s")
		}
		time.Sleep(200 * time.Millisecond)
		left := tcpAlloc(t) - base
		t.Logf("after %d sweeps: %d sockets allocated", sweeps, left)
		if pinned-left < victims/2 {
			t.Fatalf("the sweep released %d of %d pinned sockets (%d before, %d after)", pinned-left, pinned, pinned, left)
		}
		_, _ = keeper.Write([]byte("alive\n"))
		got := make([]byte, 6)
		_ = keeper.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(keeper, got); err != nil || string(got) != "alive\n" {
			t.Fatalf("the keeper after the sweep = %q, %v\n%s", got, err, dumpGroup(g))
		}
	})
}
