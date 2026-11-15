//go:build linux

// Package server — the Backlog group option (#978) at the group's level, on
// every engine: the depth the kernel holds for the group's listener, what a
// held accept path lets connect, the host's somaxconn as the default, and the
// ADR 0196 drain through a small queue.
package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	stdnet "net"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// listenQueueOf reads a listening TCP socket's queue with TCP_INFO: on a
// listener the kernel answers its length (tcpi_unacked) and its maximum
// (tcpi_sacked), the backlog listen(2) applied, the clamp to somaxconn
// included.
func listenQueueOf(t *testing.T, ln stdnet.Listener) (queued, backlog uint32) {
	t.Helper()
	sc, ok := ln.(syscall.Conn)
	if !ok {
		t.Fatalf("%T has no descriptor", ln)
	}
	rc, err := sc.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var info syscall.TCPInfo
	var errno syscall.Errno
	if err := rc.Control(func(fd uintptr) {
		n := uint32(unsafe.Sizeof(info))
		_, _, errno = syscall.Syscall6(sysGetsockopt, fd, syscall.IPPROTO_TCP, syscall.TCP_INFO,
			uintptr(unsafe.Pointer(&info)), uintptr(unsafe.Pointer(&n)), 0)
	}); err != nil {
		t.Fatal(err)
	}
	if errno != 0 {
		t.Fatalf("TCP_INFO: %v", errno)
	}
	return info.Unacked, info.Sacked
}

// boundListeners returns the listeners the server bound, in bind order.
func boundListeners(srv *Server) []stdnet.Listener {
	srv.mu.RLock()
	defer srv.mu.RUnlock()
	out := make([]stdnet.Listener, 0, len(srv.listeners))
	for _, b := range srv.listeners {
		out = append(out, b.ln)
	}
	return out
}

// heldLoopH answers nothing; once a connection opened, every batch end holds
// on gate, which keeps the loop's thread — and on epoll its accepts — still.
type heldLoopH struct {
	opened atomic.Bool
	gate   chan struct{}
	held   chan struct{}
	once   sync.Once
}

func (h *heldLoopH) OnOpen(corenet.LoopIO, corenet.LoopConnRef) { h.opened.Store(true) }

func (h *heldLoopH) OnData(_ corenet.LoopIO, _ corenet.LoopConnRef, buf []byte) int { return len(buf) }

func (h *heldLoopH) OnTimeout(corenet.LoopIO, corenet.LoopConnRef)  {}
func (h *heldLoopH) OnWritable(corenet.LoopIO, corenet.LoopConnRef) {}

func (h *heldLoopH) OnBatchEnd(corenet.LoopIO) {
	//: nothing opened yet: the loop runs.
	if !h.opened.Load() {
		return
	}
	h.once.Do(func() { close(h.held) })
	<-h.gate
}

func (h *heldLoopH) OnClose(corenet.LoopIO, corenet.LoopConnRef, error) {}

// heldEngine is one engine under test: how to declare its group, and how it
// is held and released.
type heldEngine struct {
	name string
	// declare declares the group on srv and returns the hold, which blocks
	// until the accept path is held once c0 connected, and the release.
	declare func(t *testing.T, srv *Server) (hold func(t *testing.T, ln stdnet.Listener), release func())
}

// heldEngines are the goroutine engine, held in its admission — Admit runs on
// the acceptor, under the Admission's mutex, which the test holds — and every
// loop backend the platform has, held at their batch end: io_uring, whose
// Backlog group runs DEFER_TASKRUN so an armed accept waits for the loop to
// reap, epoll, and the portable backend, whose acceptor a Backlog group's
// gate holds while no lane waits.
func heldEngines() []heldEngine {
	out := []heldEngine{{
		name: "goroutine",
		declare: func(t *testing.T, srv *Server) (func(*testing.T, stdnet.Listener), func()) {
			adm := NewAdmission(AdmissionConfig{PerAddress: 1 << 20})
			srv.Group("api", Listen("tcp", "127.0.0.1:0"), Plaintext(), Shards(1), Backlog(1), PreHandshake(adm)).
				Handle(corenet.ConnHandlerFunc(func(ctx context.Context, c corenet.Conn) error {
					_, _ = io.Copy(io.Discard, c)
					return nil
				}))
			adm.mu.Lock()
			var once sync.Once
			release := func() { once.Do(adm.mu.Unlock) }
			hold := func(t *testing.T, ln stdnet.Listener) {
				t.Helper()
				// c0 is taken off the queue by Accept, then waits in Admit: the
				// acceptor accepts nothing more.
				waitFor(t, func() bool { q, _ := listenQueueOf(t, ln); return q == 0 })
			}
			return hold, release
		},
	}}
	for _, b := range []corenet.LoopBackend{corenet.LoopBackendIOURing, corenet.LoopBackendEpoll, corenet.LoopBackendPortable} {
		out = append(out, heldEngine{
			name: b.String(),
			declare: func(t *testing.T, srv *Server) (func(*testing.T, stdnet.Listener), func()) {
				h := &heldLoopH{gate: make(chan struct{}), held: make(chan struct{})}
				srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Plaintext(), Backlog(1),
					Loop(corenet.LoopValue{Backend: b, Loops: 1, Rings: 1})).HandleLoop(h)
				var once sync.Once
				release := func() { once.Do(func() { close(h.gate) }) }
				hold := func(t *testing.T, _ stdnet.Listener) {
					t.Helper()
					select {
					case <-h.held:
					case <-time.After(5 * time.Second):
						t.Fatal("the loop never reached a batch end after the first open")
					}
				}
				return hold, release
			},
		})
	}
	return out
}

// TestBacklog_heldAcceptPathQueuesBacklogPlusOne pins #978's first criterion
// at the group's level, on every engine: a Backlog(1) group whose accept path
// is held completes at most two TCP connects — Linux queues backlog + 1
// established connections — and drops the SYN of the rest, which the
// dialers' 200 ms bound then ends: no engine takes a connection off the queue
// while it is held. State reports Backlog 1 and the kernel's own maximum,
// read back with TCP_INFO from the group's listener, is 1. io_uring holds it
// through DEFER_TASKRUN, which the group's Backlog opts it into: where the
// kernel lacks it, the row says so and the case is skipped.
func TestBacklog_heldAcceptPathQueuesBacklogPlusOne(t *testing.T) {
	for _, e := range heldEngines() {
		t.Run(e.name, func(t *testing.T) {
			srv := New()
			hold, release := e.declare(t, srv)
			if err := srv.Start(t.Context()); err != nil {
				release()
				if e.name == corenet.LoopBackendIOURing.String() {
					t.Skipf("io_uring refused on this host: %v", err)
				}
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(func() { swallowErr(srv.Close()) })
			t.Cleanup(release)
			if e.name == corenet.LoopBackendIOURing.String() {
				if status := capabilityStatus(srv, corenet.CapabilityURingDeferTaskrun); status != "on" {
					if status != "missing" {
						t.Fatalf("uring-defer-taskrun is %q on a Backlog group, want on, or missing on a kernel without it", status)
					}
					t.Skip("this kernel has no DEFER_TASKRUN: the row says so, and armed accepts complete while the loop is held")
				}
			}
			row := srv.State().Listeners[0]
			if row.Backlog != 1 || row.Degraded {
				t.Fatalf("row = %+v, want Backlog 1 and not degraded", row)
			}
			lns := boundListeners(srv)
			if len(lns) != 1 {
				t.Fatalf("%d listeners bound, want 1", len(lns))
			}
			if _, depth := listenQueueOf(t, lns[0]); depth != 1 {
				t.Fatalf("the kernel's backlog for the group's listener is %d, want 1", depth)
			}
			c0, err := stdnet.DialTimeout("tcp", row.Address, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c0.Close() })
			hold(t, lns[0])
			const dialers = 8
			got := connectsWithin(t, row.Address, dialers, 200*time.Millisecond)
			queued, _ := listenQueueOf(t, lns[0])
			conns := loopConns(srv)
			release()
			if got > 2 || got < 1 {
				t.Fatalf("%d of %d connects completed with the accept path held, want 1 to 2 (backlog + 1): %d in the kernel's queue, %d connections the engine holds",
					got, dialers, queued, conns)
			}
		})
	}
}

// loopConns is how many connections srv's loop groups hold — on the portable
// backend, those an acceptor handed a lane included, opened or not —, -1 for
// a server with none.
func loopConns(srv *Server) int64 {
	st := srv.State()
	if len(st.Loops) == 0 {
		return -1
	}
	var n int64
	for _, g := range st.Loops {
		for _, r := range g.RingStates {
			n += r.Conns
		}
	}
	return n
}

// capabilityStatus is the status State reports for c on srv's one loop group.
func capabilityStatus(srv *Server, c corenet.Capability) string {
	for _, g := range srv.State().Loops {
		for _, row := range g.Capabilities {
			if row.Name == c {
				return row.Status
			}
		}
	}
	return ""
}

// TestBacklog_defaultIsTheHostsSomaxconn pins Backlog(0) and the group
// without the option: State reports the host's somaxconn, the depth the
// standard library listens with (capped at 65535), and the kernel's own
// maximum for the listener is that value, on both engines.
func TestBacklog_defaultIsTheHostsSomaxconn(t *testing.T) {
	host := hostSomaxconn()
	if host <= 0 {
		t.Fatalf("somaxconn unreadable on Linux: %d", host)
	}
	want := min(host, maxListenerBacklog)
	for _, c := range []struct {
		name string
		opts []GroupOption
		loop bool
	}{
		{name: "goroutine/none"},
		{name: "goroutine/Backlog(0)", opts: []GroupOption{Backlog(0)}},
		{name: "loop/Backlog(0)", opts: []GroupOption{Backlog(0)}, loop: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := New()
			opts := append([]GroupOption{Listen("tcp", "127.0.0.1:0"), Plaintext()}, c.opts...)
			if c.loop {
				opts = append(opts, Loop(corenet.LoopValue{Backend: corenet.LoopBackendEpoll, Loops: 1}))
				srv.Group("g", opts...).HandleLoop(&heldLoopH{gate: make(chan struct{}), held: make(chan struct{})})
			} else {
				opts = append(opts, Shards(1))
				srv.Group("g", opts...).Handle(corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error { return nil }))
			}
			if err := srv.Start(t.Context()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(func() { swallowErr(srv.Close()) })
			row := srv.State().Listeners[0]
			if row.Backlog != want || row.Degraded {
				t.Fatalf("row = %+v, want Backlog %d (the host's somaxconn) and not degraded", row, want)
			}
			if _, depth := listenQueueOf(t, boundListeners(srv)[0]); int(depth) != want {
				t.Fatalf("the kernel's backlog is %d, want %d", depth, want)
			}
		})
	}
}

// TestBacklog_aboveSomaxconnIsDegradedInState pins #978's second criterion in
// State: a Backlog above the host's somaxconn reports the host's value, the
// listener Degraded, its reason naming somaxconn — the kernel's clamp said,
// never silent — on both engines.
func TestBacklog_aboveSomaxconnIsDegradedInState(t *testing.T) {
	host := hostSomaxconn()
	if host <= 0 || host >= maxListenerBacklog {
		t.Skipf("somaxconn is %d: no Backlog the option takes exceeds it", host)
	}
	asked := min(host+1000, maxListenerBacklog)
	for _, loop := range []bool{false, true} {
		t.Run("loop="+strconv.FormatBool(loop), func(t *testing.T) {
			srv := New()
			if loop {
				srv.Group("g", Listen("tcp", "127.0.0.1:0"), Plaintext(), Backlog(asked),
					Loop(corenet.LoopValue{Backend: corenet.LoopBackendEpoll, Loops: 1})).
					HandleLoop(&heldLoopH{gate: make(chan struct{}), held: make(chan struct{})})
			} else {
				srv.Group("g", Listen("tcp", "127.0.0.1:0"), Plaintext(), Shards(1), Backlog(asked)).
					Handle(corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error { return nil }))
			}
			if err := srv.Start(t.Context()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(func() { swallowErr(srv.Close()) })
			st := srv.State()
			row := st.Listeners[0]
			if row.Backlog != host || !row.Degraded || !bytes.Contains([]byte(row.DegradedReason), []byte("somaxconn")) {
				t.Fatalf("row = %+v, want Backlog %d, Degraded, a reason naming somaxconn", row, host)
			}
			if !st.Degraded() {
				t.Fatal("State.Degraded() is false with a listener below what it asked for")
			}
		})
	}
}

// The drain test's figures: 384 peers dialled at once against Backlog(8)
// while the loop is held, and a grace that covers three of a refused SYN's
// retransmissions (1 s, 3 s, 7 s after the first).
const (
	drainBacklogPeers = 384
	drainBacklogGrace = 8 * time.Second
	// drainBacklogStagger spaces the peers' dials. 384 goroutines dialing in
	// the same microsecond make one cohort: past the 8 the queue holds, the
	// kernel answers with SYN cookies and drops each final ACK the full queue
	// cannot take, so every such peer retransmits on the same timer, its
	// retransmissions landing in one softirq pass that no accept rate drains
	// 8 at a time — on loaded runners 51 and 80 of them were still waiting
	// when the grace ended and met the closed listener (linux/386 and 6.8 in
	// #1336's CI). The cohort is the goroutines' release, not a property of
	// the drain; spaced by a millisecond, every peer still dials against the
	// held loop and its queue of 8.
	drainBacklogStagger = time.Millisecond
)

// heldEchoH is laneEchoH whose batch end holds on gate once asked to, and
// which records every line it echoed.
type heldEchoH struct {
	laneEchoH
	holding atomic.Bool
	gate    chan struct{}
	held    chan struct{}
	once    sync.Once
	// lines are the lines echoed, a key each.
	lines sync.Map
}

// OnData echoes as laneEchoH does and records each line it consumed.
func (h *heldEchoH) OnData(lio corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	used := h.laneEchoH.OnData(lio, ref, buf)
	for _, line := range bytes.SplitAfter(buf[:used], []byte("\n")) {
		if len(line) > 0 {
			h.lines.Store(string(line), true)
		}
	}
	return used
}

// saw reports whether the handler echoed line.
func (h *heldEchoH) saw(line string) bool {
	_, ok := h.lines.Load(line)
	return ok
}

func (h *heldEchoH) OnBatchEnd(corenet.LoopIO) {
	//: not asked: the loop runs.
	if !h.holding.Load() {
		return
	}
	h.once.Do(func() { close(h.held) })
	<-h.gate
}

// TestBacklog_drainThroughASmallQueue pins #978's drain criterion on every
// loop backend, whose drain takes the listen queue (ADR 0196 §D3): a group
// of Backlog(8) whose loop is held while 384 peers dial, then drained with a
// grace, serves every one of them — each peer gets its echo, then the end,
// never a reset, the group opens and closes exactly as many connections, and
// Shutdown returns nil. The peers the full queue refused while the loop was
// held retry, and are served: as the drain begins the group's queue is
// widened to the host's somaxconn (widenBacklogs), so a peer's next SYN, or
// the final ACK and data a SYN cookie's peer sends again, is queued rather
// than dropped once more. Before that a cohort of retries overflowed the
// eight places whenever the loop was slow to take — under Bazel's parallel
// shards 38 of 385 peers had every retry of the 8 s grace dropped and met the
// closed listener (#1353) —; widened, nothing is lost under that load
// (six drains at once on two CPUs, BENCH.md edition 2026-11-15).
func TestBacklog_drainThroughASmallQueue(t *testing.T) {
	for _, b := range []corenet.LoopBackend{corenet.LoopBackendIOURing, corenet.LoopBackendEpoll, corenet.LoopBackendPortable} {
		t.Run(b.String(), func(t *testing.T) {
			// Side by side: each case waits out the same retransmission
			// timers, so three in a row would hold one Bazel shard 27 s.
			t.Parallel()
			served, lost := drainSmallQueue(t, b, drainBacklogGrace, drainBacklogStagger)
			t.Logf("%d peers served, %d never queued by the kernel and refused or reset at the listener's close", served, lost)
			if lost != 0 || served != drainBacklogPeers {
				t.Fatalf("%d of %d peers served, %d lost: every peer the queue refused must be served", served, drainBacklogPeers, lost)
			}
		})
	}
}

// TestBacklog_drainWidensTheQueue pins widenBacklogs: once Shutdown begins,
// the listen queue of a Backlog(1) group's listener is the host's somaxconn
// for the grace, read back with TCP_INFO from the kernel, on the goroutine
// engine and a loop's; State still reports the depth the group asked for.
func TestBacklog_drainWidensTheQueue(t *testing.T) {
	want := min(hostSomaxconn(), maxListenerBacklog)
	if want <= 1 {
		t.Fatalf("somaxconn unreadable on Linux: %d", want)
	}
	for _, loop := range []bool{false, true} {
		t.Run("loop="+strconv.FormatBool(loop), func(t *testing.T) {
			srv := New(WithDrainGrace(2 * time.Second))
			if loop {
				srv.Group("g", Listen("tcp", "127.0.0.1:0"), Plaintext(), Backlog(1),
					Loop(corenet.LoopValue{Backend: corenet.LoopBackendEpoll, Loops: 1})).
					HandleLoop(&heldLoopH{gate: make(chan struct{}), held: make(chan struct{})})
			} else {
				srv.Group("g", Listen("tcp", "127.0.0.1:0"), Plaintext(), Shards(1), Backlog(1)).
					Handle(corenet.ConnHandlerFunc(func(context.Context, corenet.Conn) error { return nil }))
			}
			if err := srv.Start(t.Context()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(func() { swallowErr(srv.Close()) })
			ln := boundListeners(srv)[0]
			if _, depth := listenQueueOf(t, ln); depth != 1 {
				t.Fatalf("the kernel's backlog before the drain is %d, want 1", depth)
			}
			done := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				done <- srv.Shutdown(ctx)
			}()
			waitFor(t, func() bool { _, depth := listenQueueOf(t, ln); return int(depth) == want })
			if row := srv.State().Listeners[0]; row.Backlog != 1 {
				t.Fatalf("State reports Backlog %d during the drain, want the group's 1", row.Backlog)
			}
			if err := <-done; err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
		})
	}
}

// TestBacklog_drainLossIsTheKernels is the drain's limit, on purpose: the
// same drain with one cohort of peers and a grace of 300 ms, shorter than a
// refused SYN's first retransmission (1 s). A peer whose retry comes once the
// listener closed is lost — refused or reset, its line never read: the
// kernel kept nothing of it, and no queue, widened or not, can hold what is
// not sent yet —, and the drain still serves every connection it opened.
func TestBacklog_drainLossIsTheKernels(t *testing.T) {
	served, lost := drainSmallQueue(t, corenet.LoopBackendEpoll, 300*time.Millisecond, 0)
	t.Logf("%d peers served, %d never queued by the kernel", served, lost)
	if lost == 0 {
		t.Fatal("no peer was lost to a 300 ms grace: the cause #1353 names was not reproduced")
	}
}

// drainSmallQueue runs TestBacklog_drainThroughASmallQueue's drain on b with
// grace, the peers' dials spaced by stagger, and returns how many peers were
// served and how many the kernel never queued. Any other failure fails t.
func drainSmallQueue(t *testing.T, b corenet.LoopBackend, grace, stagger time.Duration) (served, lost int64) {
	t.Helper()
	srv := New(WithDrainGrace(grace))
	h := &heldEchoH{gate: make(chan struct{}), held: make(chan struct{})}
	srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Plaintext(), Backlog(8),
		Loop(corenet.LoopValue{Backend: b, Loops: 1, Rings: 1})).HandleLoop(h)
	if err := srv.Start(t.Context()); err != nil {
		if b == corenet.LoopBackendIOURing {
			t.Skipf("io_uring refused on this host: %v", err)
		}
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { swallowErr(srv.Close()) })
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(h.gate) }) })
	row := srv.State().Listeners[0]
	if row.Backlog != 8 {
		t.Fatalf("row = %+v, want Backlog 8", row)
	}
	c0 := loopDial(t, row.Address)
	lineEcho(t, c0, "warm\n")
	h.holding.Store(true)
	lineWrite(c0, "hold\n")
	select {
	case <-h.held:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop never reached a batch end")
	}

	var wg sync.WaitGroup
	failures := make(chan string, drainBacklogPeers+1)
	var connected, dialing, servedN, lostN atomic.Int64
	var shutdownBegan atomic.Bool
	for i := range drainBacklogPeers {
		wg.Go(func() {
			//: one peer a millisecond: every one dials while the loop is
			//: held, and their retransmissions keep that spacing.
			time.Sleep(time.Duration(i) * stagger)
			dialing.Add(1)
			c, err := stdnet.DialTimeout("tcp", row.Address, grace+5*time.Second)
			if err != nil {
				//: a SYN the kernel dropped, retried once the listener closed.
				if errors.Is(err, syscall.ECONNREFUSED) && shutdownBegan.Load() {
					lostN.Add(1)
					return
				}
				failures <- "peer " + strconv.Itoa(i) + ": dial: " + err.Error()
				return
			}
			connected.Add(1)
			defer func() { _ = c.Close() }()
			line := "q" + strconv.Itoa(i) + "\n"
			msg, unqueued := echoOrUnqueued(c, line)
			switch {
			case msg == "":
				servedN.Add(1)
			case unqueued && !h.saw(line) && shutdownBegan.Load():
				lostN.Add(1)
			default:
				failures <- "peer " + strconv.Itoa(i) + ": " + msg
			}
		})
	}
	wg.Go(func() {
		if msg := echoThenEOF(c0, ""); msg != "" {
			failures <- "the held one: " + msg
		}
	})
	// The queue is full, every peer dialed, and those it could not
	// hold wait on their timers.
	waitFor(t, func() bool { q, _ := listenQueueOf(t, boundListeners(srv)[0]); return q >= 8 })
	waitFor(t, func() bool { return dialing.Load() == drainBacklogPeers })
	time.Sleep(100 * time.Millisecond)
	if n := connected.Load(); n >= drainBacklogPeers {
		t.Fatalf("%d peers connected with the loop held: the backlog held nobody back", n)
	}
	done := make(chan error, 1)
	shutdownBegan.Store(true)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), grace+20*time.Second)
		defer cancel()
		done <- srv.Shutdown(ctx)
	}()
	h.holding.Store(false)
	release.Do(func() { close(h.gate) })
	wg.Wait()
	close(failures)
	var all []string
	for f := range failures {
		all = append(all, f)
	}
	if len(all) > 0 {
		t.Fatalf("%d of %d peers not served: %v", len(all), drainBacklogPeers+1, all[:min(len(all), 8)])
	}
	if err := <-done; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if o, cl := h.opens.Load(), h.closes.Load(); o != servedN.Load()+1 || cl != o {
		t.Fatalf("%d opened, %d closed, want %d each: a connection the drain opened was not served", o, cl, servedN.Load()+1)
	}
	if servedN.Load() < 8 {
		t.Fatalf("%d peers served: the drain did not take even the queue's 8", servedN.Load())
	}
	return servedN.Load(), lostN.Load()
}

// echoOrUnqueued is echoThenEOF for a peer, and says whether its failure is
// the one a connection the kernel never queued meets: a reset, or an end,
// before a byte of the echo.
func echoOrUnqueued(c stdnet.Conn, line string) (string, bool) {
	_ = c.SetDeadline(time.Now().Add(drainBacklogGrace + 20*time.Second))
	if _, err := c.Write([]byte(line)); err != nil {
		return "write: " + err.Error(), errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
	}
	got := make([]byte, len(line))
	n, err := io.ReadFull(c, got)
	if err != nil || string(got) != line {
		return "echo " + strconv.Quote(string(got[:n])) + ": " + errString(err), n == 0 && (errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.EOF))
	}
	rest, err := io.ReadAll(c)
	if err != nil || len(rest) > 0 {
		return "end: " + errString(err) + " " + strconv.Quote(string(rest)), false
	}
	return "", false
}

// lineWrite writes line to c, its error left to the read that follows.
func lineWrite(c stdnet.Conn, line string) { _, _ = c.Write([]byte(line)) }

// echoThenEOF writes line (nothing when empty, its echo then already owed),
// reads its echo — "hold\n" for the held connection — and then the end, and
// says what went wrong: empty when nothing did.
func echoThenEOF(c stdnet.Conn, line string) string {
	_ = c.SetDeadline(time.Now().Add(drainBacklogGrace + 20*time.Second))
	want := line
	if line == "" {
		want = "hold\n"
	} else if _, err := c.Write([]byte(line)); err != nil {
		return "write: " + err.Error()
	}
	got := make([]byte, len(want))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != want {
		return "echo " + strconv.Quote(string(got)) + ": " + errString(err)
	}
	rest, err := io.ReadAll(c)
	if err != nil || len(rest) > 0 {
		return "end: " + errString(err) + " " + strconv.Quote(string(rest))
	}
	return ""
}
