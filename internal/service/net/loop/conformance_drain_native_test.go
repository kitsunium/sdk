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

	// Each client, its end read, closes by a reset (resetClose): the drain's
	// linger ends on it rather than at drainLinger, and neither end keeps a
	// TIME_WAIT for the connection the server closed first.
	var wg sync.WaitGroup
	failures := make(chan string, queued+1)
	for i, c := range conns {
		wg.Go(func() {
			msg := echoThenEnd(c, "q\n")
			resetClose([]stdnet.Conn{c})
			if msg != "" {
				failures <- msg + " (queued #" + strconv.Itoa(i) + ")"
			}
		})
	}
	wg.Go(func() {
		msg := echoThenEnd(c0, "hold\n")
		resetClose([]stdnet.Conn{c0})
		if msg != "" {
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

// What TestConformance_drainTakesWhatTheQueueHolds queues: drainLoops loops
// share one listener, and drainArrivals connections arrive once the drain is
// asked for — more than io_uring took a ring a batch before #1385 (32, its
// acceptDepth) times the loops, within the listen backlog every platform
// grants. drainTakeBatches is how many batches the take may need: one to see
// the drain and cancel the accepts in flight, one for the cancels to come
// back, one to arm the take, one to reap it.
const (
	drainLoops       int = 3
	drainArrivals    int = min(1200, drainQueueCap)
	drainTakeBatches int = 4
)

// TestConformance_drainTakesWhatTheQueueHolds pins that the drain's take
// takes, at each batch, what the listen queue holds, up to queueTakeBatch a
// taker (ADR 0196 §D3, amended 2026-11-14): drainLoops loops of one ring share
// one TCP listener, each held at every batch end; the drain is asked for,
// drainArrivals connections complete their handshake into the queue, each
// with its line; then every loop runs drainTakeBatches batches. Every queued
// connection must be open by then, then served and ended cleanly.
//
// io_uring took acceptDepth a ring a batch: a dialer faster than that — one
// is enough on a loaded runner, its batches a millisecond longer — filled the
// listen queue to its backlog while the take fell behind, the take ran into
// its budget, and the close reset everything queued, connections that had
// waited half a second among them (#1385,
// TestDrain_arrivalsAcrossTheDrain/h2c/auto with three loops).
func TestConformance_drainTakesWhatTheQueueHolds(t *testing.T) {
	runCase := func(t *testing.T, b backendCase) {
		var gating atomic.Bool
		steps := make([]chan struct{}, drainLoops)
		for i := range steps {
			steps[i] = make(chan struct{}, 1)
		}
		held := make(chan int, drainLoops)
		h := &lineH{}
		h.batchEnd = func(lio corenet.LoopIO) {
			// Once gated, each loop holds at every batch end until stepped.
			lp, ok := lio.(*loop)
			if !ok || !gating.Load() {
				return
			}
			held <- lp.index
			<-steps[lp.index]
		}
		g, addr := serveOn(t, b, h, "tcp", drainLoops, 1)
		release := func() {
			gating.Store(false)
			for _, s := range steps {
				select {
				case s <- struct{}{}:
				default:
				}
			}
		}
		t.Cleanup(release)
		c0, err := stdnet.DialTimeout("tcp", addr, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		_ = c0.SetDeadline(time.Now().Add(20 * time.Second))
		t.Cleanup(func() { _ = c0.Close() })
		echo(t, c0, "warm\n")

		// park waits until want loops hold at a batch end; a loop that returned —
		// its listener handed back, its connections gone — holds no more.
		parked := map[int]bool{}
		park := func(want int) {
			deadline := time.After(2 * time.Second)
			for len(parked) < want {
				select {
				case i := <-held:
					parked[i] = true
				case <-deadline:
					return
				}
			}
		}
		gating.Store(true)
		for _, lp := range g.loops {
			if err := lp.Post(func(corenet.LoopIO, uintptr) {}, 0); err != nil {
				t.Fatalf("Post: %v", err)
			}
		}
		park(drainLoops)
		if len(parked) != drainLoops {
			t.Fatalf("%d of %d loops reached their batch end", len(parked), drainLoops)
		}
		g.ctl.drain()

		conns := make([]stdnet.Conn, 0, drainArrivals)
		for range drainArrivals {
			c, err := stdnet.DialTimeout("tcp", addr, 3*time.Second)
			if err != nil {
				t.Fatalf("dial into the queue: %v", err)
			}
			_ = c.SetDeadline(time.Now().Add(20 * time.Second))
			t.Cleanup(func() { _ = c.Close() })
			_, _ = c.Write([]byte("q\n"))
			conns = append(conns, c)
		}
		// Each step lets every parked loop run one batch, then waits for them to
		// hold again.
		for range drainTakeBatches {
			n := len(parked)
			for i := range parked {
				steps[i] <- struct{}{}
			}
			clear(parked)
			park(n)
		}
		opened, _ := h.counts()
		if want := 1 + len(conns); opened < want {
			t.Errorf("%d of %d connections open after %d batches of the drain: the take left %d in the listen queue", opened, want, drainTakeBatches, want-opened)
		}
		release()

		done := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done <- g.Shutdown(ctx)
		}()
		// Each client, its end read, closes by a reset (resetClose): the
		// drain's linger ends on it rather than at drainLinger, and neither
		// end keeps a TIME_WAIT for the connection the server closed first.
		var wg sync.WaitGroup
		failures := make(chan string, len(conns)+1)
		for i, c := range conns {
			wg.Go(func() {
				msg := echoThenEnd(c, "q\n")
				resetClose([]stdnet.Conn{c})
				if msg != "" {
					failures <- msg + " (queued #" + strconv.Itoa(i) + ")"
				}
			})
		}
		wg.Go(func() {
			msg := echoThenEnd(c0, "")
			resetClose([]stdnet.Conn{c0})
			if msg != "" {
				failures <- msg + " (the warm one)"
			}
		})
		wg.Wait()
		close(failures)
		var all []string
		for f := range failures {
			all = append(all, f)
		}
		if len(all) > 0 {
			t.Fatalf("%d of %d connections cut short: %v", len(all), len(conns), all[:min(len(all), 8)])
		}
		if err := <-done; err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}
	eachBackend(t, runCase)
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
