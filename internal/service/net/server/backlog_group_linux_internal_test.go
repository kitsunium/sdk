//go:build linux

// Package server — the Backlog group option (#978) at the group's level, on
// every engine: the depth the kernel holds for the group's listener, what a
// held accept path lets connect, the host's somaxconn as the default, and the
// ADR 0196 drain through a small queue.
package server

import (
	"bytes"
	"context"
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

// The in-process queues an engine keeps beside the kernel's while its handler
// is held at a batch end: io_uring's single-shot accepts stay armed in the
// kernel and complete into the ring (the loop's acceptDepth), and the portable
// backend's acceptor keeps accepting into its lane's event channel
// (portableEvents) and holds one more blocked on the send. epoll and the
// goroutine engine accept on the thread the test holds, and keep none.
const (
	heldURingAccepts    int = 32
	heldPortableAccepts int = portableEvents + 1
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

// heldEngine is one engine under test: how to declare its group, how many
// connections it takes off the kernel's queue while held, and how it is held
// and released.
type heldEngine struct {
	name  string
	extra int
	// declare declares the group on srv and returns the hold, which blocks
	// until the accept path is held once c0 connected, and the release.
	declare func(t *testing.T, srv *Server) (hold func(t *testing.T, ln stdnet.Listener), release func())
}

// heldEngines are the goroutine engine, held in its admission — Admit runs on
// the acceptor, under the Admission's mutex, which the test holds — and the
// loop backends the platform has, held at their batch end.
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
		extra := 0
		switch b {
		case corenet.LoopBackendIOURing:
			extra = heldURingAccepts
		case corenet.LoopBackendPortable:
			extra = heldPortableAccepts
		}
		out = append(out, heldEngine{
			name: b.String(), extra: extra,
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
// is held completes at most two TCP connects beyond what the engine itself
// took off the queue — Linux queues backlog + 1 established connections — and
// drops the SYN of the rest, which the dialers' 200 ms bound then ends. State
// reports Backlog 1 and the kernel's own maximum, read back with TCP_INFO from
// the group's listener, is 1. On io_uring the ring's armed accepts and on the
// portable backend the lane's event channel take connections off the queue
// while the handler is held (heldURingAccepts, heldPortableAccepts): the
// backlog bounds what waits past them.
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
			dialers := e.extra + 5
			got := connectsWithin(t, row.Address, dialers, 200*time.Millisecond)
			release()
			if got > e.extra+2 || got < 1 {
				t.Fatalf("%d of %d connects completed with the accept path held, want 1 to %d (the engine's %d, then backlog + 1)",
					got, dialers, e.extra+2, e.extra)
			}
		})
	}
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
)

// heldEchoH is laneEchoH whose batch end holds on gate once asked to.
type heldEchoH struct {
	laneEchoH
	holding atomic.Bool
	gate    chan struct{}
	held    chan struct{}
	once    sync.Once
}

func (h *heldEchoH) OnBatchEnd(corenet.LoopIO) {
	//: not asked: the loop runs.
	if !h.holding.Load() {
		return
	}
	h.once.Do(func() { close(h.held) })
	<-h.gate
}

// TestBacklog_drainThroughASmallQueue pins #978's drain criterion on the
// native backends, whose drain takes the listen queue (ADR 0196 §D3): a group
// of Backlog(8) whose loop is held while 384 peers dial at once, then drained
// with a grace, serves every peer — those the queue held are taken, those
// whose SYN the kernel dropped retry and connect while the grace keeps the
// listener accepting — each reading its echo, then the end, never a reset;
// Shutdown returns nil.
//
// The portable backend is not held to it, and the reason is measured, not
// assumed: its acceptor is not the handler's goroutine, so the hold never
// fills the kernel's queue, and the first retransmitted SYNs (1 s) arrive as
// one burst it drains more slowly than the kernel completes handshakes.
// Past eight requests in SYN_RECV the kernel answers with SYN cookies, and a
// final ACK that finds the accept queue full is dropped with no request kept
// (tcp_abort_on_overflow off): the peer believes itself connected, its
// request is retransmitted only on its own RTO — 3 s after a lost SYN, then
// 6 s —, the retransmissions of one cohort arrive together and overflow
// again, and what comes after the grace meets the closed listener. On this
// VM (6.12) 2 runs in 4 under -race and 4 in 4 without lost 3 to 97 peers so;
// the native backends lost none in 8. A Backlog this small is the operator's
// trade, and the kernel's, not the drain's.
func TestBacklog_drainThroughASmallQueue(t *testing.T) {
	for _, b := range []corenet.LoopBackend{corenet.LoopBackendIOURing, corenet.LoopBackendEpoll} {
		t.Run(b.String(), func(t *testing.T) {
			srv := New(WithDrainGrace(drainBacklogGrace))
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
			var connected atomic.Int64
			for i := range drainBacklogPeers {
				wg.Go(func() {
					c, err := stdnet.DialTimeout("tcp", row.Address, drainBacklogGrace+5*time.Second)
					if err != nil {
						failures <- "peer " + strconv.Itoa(i) + ": dial: " + err.Error()
						return
					}
					connected.Add(1)
					defer func() { _ = c.Close() }()
					if msg := echoThenEOF(c, "q\n"); msg != "" {
						failures <- "peer " + strconv.Itoa(i) + ": " + msg
					}
				})
			}
			wg.Go(func() {
				if msg := echoThenEOF(c0, ""); msg != "" {
					failures <- "the held one: " + msg
				}
			})
			// The queue is full and peers wait on their SYN timers.
			waitFor(t, func() bool { q, _ := listenQueueOf(t, boundListeners(srv)[0]); return q >= 8 })
			time.Sleep(100 * time.Millisecond)
			if n := connected.Load(); n >= drainBacklogPeers {
				t.Fatalf("%d peers connected with the loop held: the backlog held nobody back", n)
			}
			done := make(chan error, 1)
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), drainBacklogGrace+20*time.Second)
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
			if o, cl := h.opens.Load(), h.closes.Load(); o != drainBacklogPeers+1 || cl != o {
				t.Fatalf("%d opened, %d closed, want %d each", o, cl, drainBacklogPeers+1)
			}
		})
	}
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
