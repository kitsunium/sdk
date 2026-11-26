//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

// Package loop — the drain (plan P6, ADR 0196) on every native backend: the
// listen queue taken before the listener closes, and the connections it held
// served, on a TCP and on a Unix listener; io_uring and epoll on Linux, kqueue
// on darwin and the BSDs (ADR 0222).
package loop

import (
	"context"
	"errors"
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

// What TestConformance_drainReadsBeforeItJudgesSilence queues: drainUnreaped
// connections, each with its line, that one take accepts — fewer than
// queueTakeBatch and than the BSDs' listen backlog —, on a loop that reaps
// drainReap events a batch, so most of them wait batches for their read;
// drainTakeLimit bounds the batches the take may need, io_uring's reaping
// drainReap accepts and receives a batch.
const (
	drainUnreaped  int = min(24, drainQueueCap/2)
	drainReap      int = 4
	drainTakeLimit int = 4 * drainUnreaped
)

// TestConformance_drainReadsBeforeItJudgesSilence pins that the drain never
// takes for silent a connection whose request waits unread in its socket
// (#1541): one loop of one ring reaping drainReap events a batch, held at
// every batch end once the drain is asked for; drainUnreaped connections
// complete their handshake into the queue, each with its line; one batch
// takes them all, then the loop stays held past drainQuiet, as a loaded
// runner holds it between two batches, then goes on. Every one must read its
// echo, then the end.
//
// The drain judged a connection by the ticks since it last moved, and one
// taken at a batch's end had not been read yet: its readiness waited behind
// the events that batch could not reap (cqBatch), so the next batch begun a
// second later found it silent and closed it half-way — a FIN, the request
// unread, the peer reading EOF where its echo should be (the noble-6.8 lane:
// 91 of 1 200, the last of one loop's take past the 512 its next batch
// reaped).
func TestConformance_drainReadsBeforeItJudgesSilence(t *testing.T) {
	runCase := func(t *testing.T, b backendCase) {
		var gating atomic.Bool
		step := make(chan struct{})
		held := make(chan struct{}, 1)
		h := &lineH{}
		h.batchEnd = func(corenet.LoopIO) {
			//: once gated, the loop holds at every batch end until stepped.
			if gating.Load() {
				held <- struct{}{}
				<-step
			}
		}
		g, addr := serve(t, b, h, func(c *Config) { c.Value.Loops, c.Value.Rings, c.Value.CQBatch = 1, 1, drainReap })
		release := func() {
			//: let a loop held at its batch end go, and every batch after.
			if gating.Swap(false) {
				close(step)
			}
		}
		t.Cleanup(release)
		c0 := dial(t, addr)
		echo(t, c0, "warm\n")
		park := func() bool {
			//: held at a batch end, or never.
			select {
			case <-held:
				return true
			case <-time.After(5 * time.Second):
				return false
			}
		}
		gating.Store(true)
		//: a posted no-op wakes the loop to its batch end.
		if err := g.loops[0].Post(func(corenet.LoopIO, uintptr) {}, 0); err != nil {
			t.Fatalf("Post: %v", err)
		}
		//: parked before the drain.
		if !park() {
			t.Fatal("the loop never reached its batch end")
		}
		g.ctl.drain()
		conns := make([]stdnet.Conn, 0, drainUnreaped)
		//: every connection queued, its line sent, before the take.
		for range drainUnreaped {
			c := dial(t, addr)
			_, _ = c.Write([]byte("q\n"))
			conns = append(conns, c)
		}
		// The loop runs a batch at a time until the take holds every one:
		// epoll and kqueue take them in one, io_uring as its reaps allow.
		opened := 0
		//: drainTakeLimit batches at most.
		for range drainTakeLimit {
			step <- struct{}{}
			//: the batch ended.
			if !park() {
				t.Fatal("the loop never ended a batch of the take")
			}
			opened, _ = h.counts()
			//: the whole queue taken.
			if opened == 1+drainUnreaped {
				break
			}
		}
		//: the take left part of the queue.
		if want := 1 + drainUnreaped; opened != want {
			t.Fatalf("%d of %d connections open after %d batches of the take", opened, want, drainTakeLimit)
		}
		// The next batch begins past drainQuiet, as on a loaded runner.
		time.Sleep(drainQuiet + 250*time.Millisecond)
		release()

		done := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done <- g.Shutdown(ctx)
		}()
		var wg sync.WaitGroup
		failures := make(chan string, len(conns)+1)
		//: each client reads its echo, then the end.
		for i, c := range conns {
			wg.Go(func() {
				msg := echoThenEnd(c, "q\n")
				resetClose([]stdnet.Conn{c})
				//: cut short.
				if msg != "" {
					failures <- msg + " (queued #" + strconv.Itoa(i) + ")"
				}
			})
		}
		wg.Go(func() {
			msg := echoThenEnd(c0, "")
			resetClose([]stdnet.Conn{c0})
			//: cut short.
			if msg != "" {
				failures <- msg + " (the warm one)"
			}
		})
		wg.Wait()
		close(failures)
		var all []string
		//: every failure, in arrival order.
		for f := range failures {
			all = append(all, f)
		}
		//: one unread request closed as silence is the defect.
		if len(all) > 0 {
			t.Fatalf("%d of %d connections cut short: %v", len(all), len(conns)+1, all[:min(len(all), 8)])
		}
		//: the drain ended within its budget.
		if err := <-done; err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}
	eachBackend(t, runCase)
}

// What TestConformance_drainClosesOnItsLastRead dials: drainBurst connections
// between two batches of the drain — on Linux more than io_uring keeps
// accepts in flight (acceptDepth, 32), so its take arms a round of its own,
// and within the listen backlog every platform grants —, drainSteps times at
// most.
const (
	drainBurst int = min(40, drainQueueCap/3)
	drainSteps int = 6
)

// TestConformance_drainClosesOnItsLastRead pins that a taker hands its
// listener back only on an empty read of the queue made as it hands it back,
// none of its accepts in flight (ADR 0196 §D3, amended 2026-11-15): one loop
// of one ring, held at every batch end once the drain is asked for; between
// two batches drainBurst connections complete their handshake into the queue,
// each with its line, until a dial is refused — the listener closed. Every
// connection whose dial succeeded must be served, then ended cleanly: one
// dialled between two batches was in the queue before the batch that closed
// the listener began, so that batch's last read saw it.
//
// io_uring read the queue right after submitting a round of accepts and,
// empty there, handed the listener back a batch later, once the round's
// completions came back, without reading it again: what arrived in between —
// the rest of the batch, the wait and the next reap, 70 to 230 ms on a loaded
// runner — met the close (#1385, TestDrain_arrivalsAcrossTheDrain/h2c/auto
// with three loops). epoll and kqueue read EAGAIN as they hand it back.
func TestConformance_drainClosesOnItsLastRead(t *testing.T) {
	runCase := func(t *testing.T, b backendCase) {
		var gating atomic.Bool
		step := make(chan struct{})
		held := make(chan struct{}, 1)
		h := &lineH{}
		h.batchEnd = func(corenet.LoopIO) {
			// Once gated, the loop holds at every batch end until stepped.
			if gating.Load() {
				held <- struct{}{}
				<-step
			}
		}
		g, addr := serveOn(t, b, h, "tcp", 1, 1)
		release := func() {
			// Let a loop held at its batch end go, and every batch after.
			if gating.Swap(false) {
				close(step)
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
		// park waits until the loop holds at a batch end, or returned.
		park := func() bool {
			select {
			case <-held:
				return true
			case <-g.ctl.done():
				return false
			case <-time.After(5 * time.Second):
				return false
			}
		}
		gating.Store(true)
		if err := g.loops[0].Post(func(corenet.LoopIO, uintptr) {}, 0); err != nil {
			t.Fatalf("Post: %v", err)
		}
		if !park() {
			t.Fatal("the loop never reached its batch end")
		}
		g.ctl.drain()

		var conns []stdnet.Conn
		refused := false
		// Each step dials a burst into the queue, then lets one batch run.
		for s := 0; s < drainSteps && !refused; s++ {
			for range drainBurst {
				c, err := stdnet.DialTimeout("tcp", addr, 3*time.Second)
				//: the listener closed: no connection of this step is in it.
				if err != nil {
					if !errors.Is(err, syscall.ECONNREFUSED) {
						t.Errorf("dial between two batches of the drain: %v", err)
					}
					refused = true
					break
				}
				_ = c.SetDeadline(time.Now().Add(20 * time.Second))
				t.Cleanup(func() { _ = c.Close() })
				_, _ = c.Write([]byte("q\n"))
				conns = append(conns, c)
			}
			step <- struct{}{}
			if !park() {
				break
			}
		}
		release()

		done := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			done <- g.Shutdown(ctx)
		}()
		var wg sync.WaitGroup
		failures := make(chan string, len(conns)+1)
		for i, c := range conns {
			wg.Go(func() {
				msg := echoThenEnd(c, "q\n")
				resetClose([]stdnet.Conn{c})
				if msg != "" {
					failures <- msg + " (dialled #" + strconv.Itoa(i) + ", step " + strconv.Itoa(i/drainBurst) + ")"
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
			t.Fatalf("%d of %d connections dialled during the drain cut short: %v", len(all), len(conns), all[:min(len(all), 8)])
		}
		if err := <-done; err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		if !refused {
			t.Fatalf("the listener still accepted after %d batches of the drain", drainSteps)
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
