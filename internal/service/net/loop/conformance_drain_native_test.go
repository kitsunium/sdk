//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

// Package loop — the drain (plan P6, ADR 0196) on every native backend: the
// listen queue taken before the listener closes, and the connections it held
// served, on a TCP and on a Unix listener; io_uring and epoll on Linux, kqueue
// on darwin and the BSDs (ADR 0222).
package loop

import (
	"context"
	"io"
	stdnet "net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// drainQueued is how many connections wait in the listen queue as the drain
// begins: more than epoll or kqueue takes in one readiness (acceptBurst) and
// than
// io_uring keeps accepts in flight (acceptDepth), so the drain's own take is
// what reaches the last of them. drainQueuedMany passes what epoll's take
// takes in one batch (queueTakeBatch), so its take must go on at the next.
const (
	drainQueued     int = 3 * acceptBurst
	drainQueuedMany int = queueTakeBatch + 2*acceptBurst
)

// TestConformance_drainTakesQueue pins the drain's order on every backend:
// with the loop held, drainQueued connections complete their handshake and
// send one line into the listen queue, the drain is asked for, then the loop
// goes on. Every queued connection must read its echo, then the end — never a
// reset, never an end before the echo —, the listener must refuse once the
// drain is over, and Shutdown must return within its budget. Before plan P6
// the listener left the set unread and closed last, and a connection the
// drain found unread was closed at once: both reset the peer.
//
// It runs on a TCP listener, whose queue io_uring's take counts with
// TCP_INFO, and on a Unix one, which TCP_INFO does not answer and a poll
// does; with one loop of one ring, and with two loops of two rings, where
// every taker must hand the listener back before the last one closes it.
func TestConformance_drainTakesQueue(t *testing.T) {
	for _, network := range drainNetworks {
		for _, shape := range [][2]int{{1, 1}, {2, 2}} {
			if network == networkShards && shape[0] == 1 {
				shape[0] = 4
			}
			t.Run(network+"/loops="+strconv.Itoa(shape[0])+",rings="+strconv.Itoa(shape[1]), func(t *testing.T) {
				queued := drainQueued
				if network == "tcp" && shape[0] == 1 {
					queued = drainQueuedMany
				}
				queued = min(queued, drainQueueCap)
				eachBackend(t, func(t *testing.T, b backendCase) { drainTakesQueue(t, b, network, shape[0], shape[1], queued) })
			})
		}
	}
}

// drainTakesQueue is TestConformance_drainTakesQueue on b, over network,
// with loops loops of rings rings and queued connections in the queue.
func drainTakesQueue(t *testing.T, b backendCase, network string, loops, rings, queued int) {
	network, dialNet := network, strings.TrimSuffix(network, "+shards")
	gate := make(chan struct{})
	held := make(chan struct{}, loops)
	var holding atomic.Int32
	h := &lineH{}
	h.batchEnd = func(corenet.LoopIO) {
		// Once asked, each loop holds on the gate at its next batch end.
		if holding.Add(-1) >= 0 {
			held <- struct{}{}
			<-gate
		}
	}
	g, addr := serveOn(t, b, h, network, loops, rings)
	c0, err := stdnet.DialTimeout(dialNet, addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c0.SetDeadline(time.Now().Add(20 * time.Second))
	t.Cleanup(func() { _ = c0.Close() })
	echo(t, c0, "warm\n")
	holding.Store(int32(loops))
	_, _ = c0.Write([]byte("hold\n"))
	// A posted no-op wakes each loop, so each reaches a batch end and holds.
	for _, lp := range g.loops {
		if err := lp.Post(func(corenet.LoopIO, uintptr) {}, 0); err != nil {
			close(gate)
			t.Fatalf("Post: %v", err)
		}
	}
	for range loops {
		select {
		case <-held:
		case <-time.After(5 * time.Second):
			close(gate)
			t.Fatal("a loop never reached its batch end")
		}
	}

	conns := make([]stdnet.Conn, 0, queued)
	for range queued {
		c, err := stdnet.DialTimeout(dialNet, addr, 3*time.Second)
		if err != nil {
			close(gate)
			t.Fatalf("dial into the queue: %v", err)
		}
		_ = c.SetDeadline(time.Now().Add(20 * time.Second))
		t.Cleanup(func() { _ = c.Close() })
		_, _ = c.Write([]byte("q\n"))
		conns = append(conns, c)
	}
	// The drain is asked for while every loop is held, so each sees it as
	// it goes on; Shutdown asks again, which changes nothing.
	g.ctl.drain()
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		done <- g.Shutdown(ctx)
	}()
	close(gate)

	var wg sync.WaitGroup
	failures := make(chan string, queued+1)
	for i, c := range conns {
		wg.Go(func() {
			if msg := echoThenEnd(c, "q\n"); msg != "" {
				failures <- msg + " (queued #" + strconv.Itoa(i) + ")"
			}
		})
	}
	wg.Go(func() {
		if msg := echoThenEnd(c0, "hold\n"); msg != "" {
			failures <- msg + " (the held one)"
		}
	})
	wg.Wait()
	close(failures)
	var all []string
	for f := range failures {
		all = append(all, f)
	}
	if len(all) > 0 {
		t.Fatalf("%d of %d connections cut short: %v\n%s", len(all), queued+1, all[:min(len(all), 8)], dumpGroup(g))
	}
	if err := <-done; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if c, err := stdnet.DialTimeout(dialNet, addr, time.Second); err == nil {
		_ = c.Close()
		t.Fatal("the listener still accepts after the drain")
	}
	o, cl := h.counts()
	if o != queued+1 || cl != o {
		t.Fatalf("%d opened, %d closed, want %d each", o, cl, queued+1)
	}
}

// serveOn starts a group of loops loops of rings rings on b with h,
// listening on loopback TCP or on a Unix socket in a temporary directory, and
// returns its address.
func serveOn(t *testing.T, b backendCase, h corenet.LoopHandler, network string, loops, rings int) (*Group, string) {
	t.Helper()
	if network == networkShards {
		return serveShards(t, b, h, loops, rings)
	}
	if network == "tcp" {
		return serve(t, b, h, func(c *Config) { c.Value.Loops, c.Value.Rings = loops, rings })
	}
	path := shortSockPath(t)
	fd, err := streamSocket(syscall.AF_UNIX)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: path}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Listen(fd, 4096); err != nil {
		t.Fatal(err)
	}
	g, err := Start(Config{Group: t.Name(), Value: corenet.LoopValue{Backend: b.backend, Loops: loops, Rings: rings}, Handler: h, Listeners: []Listener{{FD: fd}}, Log: io.Discard})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = g.Close() })
	return g, path
}
