//go:build linux && (amd64 || arm64 || riscv64 || loong64)

// Package loop — io_uring's provided buffers follow the demand: a ring hands
// the kernel bufsFirst at setup and more only when it runs short.
package loop

import (
	"fmt"
	"io"
	stdnet "net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// TestBufs_growUpToCount pins growBufs: bids handed out in order, at most the
// ring's count, none twice.
func TestBufs_growUpToCount(t *testing.T) {
	u, step, errno := openURing(probeEntries, 0)
	if errno != 0 {
		t.Skipf("io_uring %s: %v", step, errno)
	}
	defer u.close()
	if errno := u.setupBuffers(64, 64, 8); errno != 0 {
		t.Skipf("buffer ring: %v", errno)
	}
	if u.bufProvided != 8 || u.bufTail != 8 {
		t.Fatalf("after setup: %d provided, tail %d, want 8 and 8", u.bufProvided, u.bufTail)
	}
	if n := u.growBufs(100); n != 56 || u.bufProvided != 64 {
		t.Fatalf("growBufs(100) = %d, %d provided, want 56 and 64", n, u.bufProvided)
	}
	if n := u.growBufs(1); n != 0 || u.bufTail != 64 {
		t.Fatalf("growBufs past the count = %d, tail %d, want 0 and 64", n, u.bufTail)
	}
	seen := map[uint16]bool{}
	for i, e := range u.bufEntries() {
		bid := e.bid
		if i == 0 {
			bid = u.shadowBid
		}
		if seen[bid] {
			t.Fatalf("bid %d in the ring twice", bid)
		}
		seen[bid] = true
	}
}

// TestConformance_buffersFollowDemand holds a ring's connections idle, then
// lets burst of them send while the loop is held, so one batch receives more
// requests than the ring started with buffers. Idle, the ring keeps its first
// buffers; after the burst it provided more, every echo is exact, and it never
// passes its count.
func TestConformance_buffersFollowDemand(t *testing.T) {
	const idle, burst = 1000, 3000
	if lim := descriptorLimit(); lim < 2*(idle+burst)+256 {
		t.Skipf("RLIMIT_NOFILE %d is too low", lim)
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
		rr := uringOf(g).rings[0]
		provided := func() (n uint32) {
			onLoop(t, g, func() { n = rr.u.bufProvided })
			return n
		}
		conns := heard(t, addr, idle+burst)
		defer func() {
			for _, c := range conns {
				_ = c.Close()
			}
		}()
		if n := provided(); n != bufsFirst {
			t.Fatalf("%d connections heard one at a time: %d buffers provided, want %d", idle+burst, n, bufsFirst)
		}
		hold.Store(true)
		_, _ = conns[0].Write([]byte("hold\n"))
		<-held
		var wg sync.WaitGroup
		errc := make(chan error, burst)
		for i, c := range conns[idle:] {
			msg := fmt.Sprintf("burst %05d\n", i)
			if _, err := c.Write([]byte(msg)); err != nil {
				t.Fatal(err)
			}
			wg.Go(func() {
				got := make([]byte, len(msg))
				_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
				if _, err := io.ReadFull(c, got); err != nil || string(got) != msg {
					errc <- fmt.Errorf("%q, %v, want %q", got, err, msg)
				}
			})
		}
		time.Sleep(100 * time.Millisecond)
		close(release)
		wg.Wait()
		close(errc)
		if err := <-errc; err != nil {
			t.Fatalf("a burst echo: %v", err)
		}
		n := provided()
		t.Logf("after a burst of %d: %d buffers provided of %d, %d starved", burst, n, defaultBufs, g.State().RingStates[0].BufferStarved)
		if n <= bufsFirst || n > defaultBufs {
			t.Fatalf("after a burst of %d: %d buffers provided, want more than %d and at most %d", burst, n, bufsFirst, defaultBufs)
		}
	})
}

// heard opens n connections to addr one after the other, each sending one
// line and reading its echo.
func heard(t *testing.T, addr string, n int) []stdnet.Conn {
	t.Helper()
	conns := make([]stdnet.Conn, 0, n)
	for range n {
		c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		_ = c.SetDeadline(time.Now().Add(10 * time.Second))
		if _, err := c.Write([]byte("h\n")); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(c, make([]byte, 2)); err != nil {
			t.Fatal(err)
		}
	}
	return conns
}
