//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — a ring's provided buffers follow its table: tens of
// thousands of connections on one ring sending at once find a buffer each
// once the ring met the burst (ADR 0203 §D2).
package loop

import (
	"fmt"
	"io"
	stdnet "net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// scaleConns is how many connections TestConformance_noStarvationAtScale
	// holds on one ring when the host allows: the single-vCPU benchmark's
	// load per ring, where 8,192 buffers starved.
	scaleConns = 50000
	// scaleFloor is the fewest it runs with; under it the test skips.
	scaleFloor = 5000
	// scaleDialers is how many goroutines open the connections.
	scaleDialers = 64
	// scaleBursts is how many simultaneous bursts it sends: the first
	// grows the buffers, the others must find one each.
	scaleBursts = 3
)

// TestConformance_noStarvationAtScale holds up to scaleConns connections on
// one ring, the loop held at its batch end while every one of them sends a
// line, then released — one more connection's line holds it: every recv completes into the CQ while nothing is
// returned, so the burst needs a buffer a connection. The first burst grows
// the ring's buffers from bufsFirst; the second starves at most one recv in
// a thousand, the last none, every echo exact, the buffers provided at most the connections plus a step, and
// the loop within its budget.
func TestConformance_noStarvationAtScale(t *testing.T) {
	if testing.Short() {
		t.Skip("tens of thousands of connections")
	}
	n := scaleCount()
	if n < scaleFloor {
		t.Skipf("%d connections at most: RLIMIT_NOFILE or the local port range is too small", n)
	}
	eachBackend(t, func(t *testing.T, b backendCase) {
		onlyIOURing(t, b)
		var hold atomic.Bool
		release := make(chan struct{})
		held := make(chan struct{}, 1)
		h := &lineH{}
		h.batchEnd = func(corenet.LoopIO) {
			if hold.CompareAndSwap(true, false) {
				held <- struct{}{}
				<-release
			}
		}
		g, addr := serve(t, b, h, func(c *Config) { c.Value.Loops, c.Value.Rings = 1, 1 })
		ul := uringOf(g)
		rr := ul.rings[0]
		all := openScale(t, addr, n+1)
		defer func() {
			for _, c := range all {
				_ = c.Close()
			}
		}()
		holder, conns := all[n], all[:n]
		for burst := range scaleBursts {
			before := g.State().RingStates[0].BufferStarved
			release = make(chan struct{})
			hold.Store(true)
			_, _ = holder.Write([]byte("hold\n"))
			<-held
			for i, c := range conns {
				if _, err := fmt.Fprintf(c, "b%d %06d\n", burst, i); err != nil {
					t.Fatal(err)
				}
			}
			time.Sleep(200 * time.Millisecond)
			start := time.Now()
			close(release)
			readHold(t, holder)
			for i, c := range conns {
				want := fmt.Sprintf("b%d %06d\n", burst, i)
				got := make([]byte, len(want))
				_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
				if _, err := io.ReadFull(c, got); err != nil || string(got) != want {
					t.Fatalf("burst %d, connection %d: %q, %v, want %q", burst, i, got, err, want)
				}
			}
			starved := g.State().RingStates[0].BufferStarved - before
			var provided, count uint32
			var bytes uint64
			onLoop(t, g, func() { provided, count, bytes = rr.u.bufProvided, rr.u.bufCount, ul.bufBytes })
			t.Logf("burst %d of %d connections: every echo in %v, %d starved, %d buffers provided of %d, %d MiB", burst, n, time.Since(start).Round(time.Millisecond), starved, provided, count, bytes>>20)
			// The first burst grows the buffers to what it lacked, exactly:
			// the next may starve a few recvs where a group's last buffer
			// was the holder's, and grows a step; the last starves none.
			if burst > 0 && uint64(starved)*1000 > uint64(n) || burst == scaleBursts-1 && starved != 0 {
				t.Fatalf("burst %d of %d connections: %d recvs starved after the first burst", burst, n, starved)
			}
			if bytes > loopBufBudget || provided > uint32(n)+2*bufsStep+bufsFirst {
				t.Fatalf("burst %d: %d buffers provided, %d bytes, for %d connections", burst, provided, bytes, n)
			}
		}
	})
}

// scaleCount is how many connections this host lets one process hold on
// loopback: scaleConns, or fewer under RLIMIT_NOFILE (a client socket each,
// and a server one on process descriptors) or the local port range.
func scaleCount() int {
	n := min(scaleConns, int(descriptorLimit()/2)-256)
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return 0
	}
	var lo, hi int
	if _, err := fmt.Sscan(strings.TrimSpace(string(raw)), &lo, &hi); err != nil {
		return 0
	}
	return min(n, hi-lo-1000)
}

// openScale opens n connections to addr from scaleDialers goroutines, each
// heard once, and returns them with no deadline.
func openScale(t *testing.T, addr string, n int) []stdnet.Conn {
	t.Helper()
	conns := make([]stdnet.Conn, n)
	var next atomic.Int64
	var wg sync.WaitGroup
	errc := make(chan error, scaleDialers)
	for range scaleDialers {
		wg.Go(func() {
			buf := make([]byte, 2)
			for {
				i := int(next.Add(1) - 1)
				if i >= n {
					return
				}
				c, err := stdnet.DialTimeout("tcp", addr, 10*time.Second)
				if err == nil {
					conns[i] = c
					_ = c.SetDeadline(time.Now().Add(30 * time.Second))
					_, err = c.Write([]byte("h\n"))
				}
				if err == nil {
					_, err = io.ReadFull(c, buf)
				}
				if err == nil {
					err = c.SetDeadline(time.Time{})
				}
				if err != nil {
					errc <- fmt.Errorf("connection %d: %w", i, err)
					return
				}
			}
		})
	}
	wg.Wait()
	close(errc)
	if err := <-errc; err != nil {
		for _, c := range conns {
			if c != nil {
				_ = c.Close()
			}
		}
		t.Fatal(err)
	}
	return conns
}

// readHold reads the echo of the line that held the loop.
func readHold(t *testing.T, c stdnet.Conn) {
	t.Helper()
	got := make([]byte, len("hold\n"))
	_ = c.SetReadDeadline(time.Now().Add(30 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "hold\n" {
		t.Fatalf("the hold's echo: %q, %v", got, err)
	}
}
