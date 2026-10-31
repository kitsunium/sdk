//go:build linux

// Package loop — the stop's cost: a drain or a close of many idle
// connections takes a time linear in them, on every backend.
package loop

import (
	"context"
	"errors"
	"io"
	stdnet "net"
	"sync"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// stopConns is how many idle keep-alive connections the stop tests hold:
	// enough for a close that costs O(n) in the kernel per connection to show
	// (a cancel by descriptor walks every armed request, ADR 0200 §D2).
	stopConns int = 20000
	// stopBound is how long draining or closing stopConns connections may
	// take, the race detector on: linear, it takes a fraction of it.
	stopBound time.Duration = 2 * time.Second
	// stopDialers is how many goroutines open the connections.
	stopDialers int = 64
)

// TestConformance_drainBounded drains stopConns idle connections, each heard
// once, spread over three rings on io_uring: the drain closes them all within
// stopBound, and every peer reads the end.
func TestConformance_drainBounded(t *testing.T) {
	stopBounded(t, true)
}

// TestConformance_closeBounded closes the group under stopConns idle
// connections: Close returns within stopBound, and every peer reads the end.
func TestConformance_closeBounded(t *testing.T) {
	stopBounded(t, false)
}

// stopBounded holds stopConns heard connections, then drains (drain) or
// closes the group, and bounds how long that took.
func stopBounded(t *testing.T, drain bool) {
	if lim := descriptorLimit(); lim < uint64(2*stopConns+256) {
		t.Skipf("RLIMIT_NOFILE %d is too low", lim)
	}
	eachBackend(t, func(t *testing.T, b backendCase) {
		h := &lineH{}
		g, addr := serve(t, b, h, func(c *Config) {
			//: three rings on one listener, as a group past one table is.
			if b.backend == corenet.LoopBackendIOURing {
				c.Value.Loops, c.Value.Rings = 1, 3
			}
		})
		conns := heardConns(t, addr, stopConns)
		defer func() {
			for _, c := range conns {
				_ = c.Close()
			}
		}()
		waitFor(t, "every open", func() bool { return g.Live() == int64(stopConns) })
		from := time.Now()
		if drain {
			ctx, cancel := context.WithTimeout(context.Background(), 4*stopBound)
			defer cancel()
			if err := g.Shutdown(ctx); err != nil {
				t.Fatalf("Shutdown after %v: %v", time.Since(from), err)
			}
		} else {
			_ = g.Close()
		}
		took := time.Since(from)
		t.Logf("kernel %s: %s of %d connections took %v", kernelRelease(), map[bool]string{true: "drain", false: "close"}[drain], stopConns, took)
		if took > stopBound {
			t.Fatalf("stopping %d connections took %v, want under %v", stopConns, took, stopBound)
		}
		readEnds(t, conns)
	})
}

// heardConns opens n connections to addr, each sending one line and reading
// its echo, so the drain closes them at once.
func heardConns(t *testing.T, addr string, n int) []stdnet.Conn {
	t.Helper()
	conns := make([]stdnet.Conn, n)
	errc := make(chan error, stopDialers)
	var wg sync.WaitGroup
	for w := range stopDialers {
		wg.Go(func() {
			for i := w; i < n; i += stopDialers {
				c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
				if err != nil {
					errc <- err
					return
				}
				conns[i] = c
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				if _, err := c.Write([]byte("h\n")); err != nil {
					errc <- err
					return
				}
				if _, err := io.ReadFull(c, make([]byte, 2)); err != nil {
					errc <- err
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
		t.Fatalf("opening %d connections: %v", n, err)
	}
	return conns
}

// readEnds checks that every connection reads a clean end of the stream: a
// reset would be a drain's defect (ADR 0196).
func readEnds(t *testing.T, conns []stdnet.Conn) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for i, c := range conns {
		_ = c.SetReadDeadline(deadline)
		n, err := c.Read(make([]byte, 1))
		if n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("connection %d read %d bytes, %v, want a clean end of the stream", i, n, err)
		}
	}
}
