//go:build linux

// Package loop — the conformance suite: one suite, run on every backend the
// test binary was told to expect, with faults injected (ADR 0186
// §Consequences). The expectation is compiled in, by -ldflags -X under go
// test and x_defs under Bazel, and fails closed: a Linux binary given none
// fails, unless told "none".
package loop

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	stdnet "net"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// requiredBackends is the comma-separated list of backends this test binary
// must find and run, set at link time:
//
//	go test -ldflags=-X=github.com/kitsunium/sdk/internal/service/net/loop.requiredBackends=epoll,iouring
//
// "none" runs whatever is present and requires nothing.
var requiredBackends string

// idleClients is the memory test's client process, set by its file in the
// race-off build.
var idleClients func(addr string) int

// childEnv marks a re-executed child and names the case it runs.
const childEnv = "KITSUNIUM_LOOP_CHILD"

// backendCase is one backend the suite runs on.
type backendCase struct {
	name    string
	backend corenet.LoopBackend
}

// backendsPresent lists the backends this binary can run, in the order
// the suite runs them.
func backendsPresent() []backendCase {
	return []backendCase{{"iouring", corenet.LoopBackendIOURing}, {"epoll", corenet.LoopBackendEpoll}}
}

// TestMain runs a re-executed child's case when one is marked, and checks the
// compiled-in expectation otherwise.
func TestMain(m *testing.M) {
	if c := os.Getenv(childEnv); c != "" {
		os.Exit(runChild(c))
	}
	if addr := os.Getenv("KITSUNIUM_LOOP_IDLE_CLIENTS"); addr != "" && idleClients != nil {
		os.Exit(idleClients(addr))
	}
	os.Exit(m.Run())
}

// TestBackendsRequired fails closed when the binary was given no
// expectation, and names every expected backend that is not present.
func TestBackendsRequired(t *testing.T) {
	if requiredBackends == "" {
		t.Fatal("no backend expectation compiled in: build with -ldflags=-X=github.com/kitsunium/sdk/internal/service/net/loop.requiredBackends=<list|none>")
	}
	if requiredBackends == "none" {
		return
	}
	present := map[string]bool{}
	for _, b := range backendsPresent() {
		err := probeBackend(b.backend)
		if err == nil {
			present[b.name] = true
			continue
		}
		t.Logf("net/loop backend %s absent: %v %v", b.name, err, errs.FieldsOf(err))
	}
	for _, want := range strings.Split(requiredBackends, ",") {
		if !present[want] {
			t.Errorf("backend %q is required on this lane and not present", want)
		}
	}
	t.Logf("net/loop backends: required=%s present=%v kernel=%s", requiredBackends, present, kernelRelease())
}

// probeBackend starts and closes an empty group on b.
func probeBackend(b corenet.LoopBackend) error {
	g, err := Start(Config{Group: "probe", Value: corenet.LoopValue{Backend: b}, Handler: &lineH{}, Log: io.Discard})
	if err != nil {
		return err
	}
	return g.Close()
}

// kernelRelease is uname -r.
func kernelRelease() string {
	var u syscall.Utsname
	if syscall.Uname(&u) != nil {
		return "unknown"
	}
	var b []byte
	for _, c := range u.Release {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

// eachBackend runs fn on every present backend.
func eachBackend(t *testing.T, fn func(t *testing.T, b backendCase)) {
	t.Helper()
	for _, b := range backendsPresent() {
		t.Run(b.name, func(t *testing.T) {
			if err := probeBackend(b.backend); err != nil {
				t.Skipf("%s not present: %v", b.name, err)
			}
			fn(t, b)
		})
	}
}

// lineH answers each newline-terminated line: "BIG n" with n bytes of a
// counting pattern, "CLOSE" by closing, anything else echoed. It consumes
// only whole lines, so a line split across reads exercises the spill.
type lineH struct {
	mu      sync.Mutex
	opens   int
	closes  []error
	refs    []corenet.LoopConnRef
	pending map[corenet.LoopConnRef][]byte
	// onData, when set, runs first on every OnData.
	onData func(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte)
	// batchEnd, when set, runs at every OnBatchEnd.
	batchEnd func(io corenet.LoopIO)
	// lastIO is the LoopIO of the last callback.
	lastIO corenet.LoopIO
}

func (h *lineH) OnOpen(io corenet.LoopIO, ref corenet.LoopConnRef) {
	h.mu.Lock()
	h.opens++
	h.refs = append(h.refs, ref)
	h.lastIO = io
	h.mu.Unlock()
}

func (h *lineH) OnData(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) int {
	if h.onData != nil {
		h.onData(io, ref, buf)
	}
	used := 0
	for {
		i := bytes.IndexByte(buf[used:], '\n')
		if i < 0 {
			return used
		}
		line := buf[used : used+i]
		switch {
		case bytes.HasPrefix(line, []byte("BIG ")):
			n, _ := strconv.Atoi(string(line[4:]))
			h.big(io, ref, n)
		case bytes.Equal(line, []byte("CLOSE")):
			_ = io.Close(ref)
		default:
			// Behind queued output, or refused, the echo joins the queue,
			// so the order holds.
			h.mu.Lock()
			queued := len(h.pending[ref]) > 0
			h.mu.Unlock()
			if queued || io.Write(ref, buf[used:used+i+1]) != nil {
				h.queue(ref, buf[used:used+i+1])
			}
		}
		used += i + 1
	}
}

// queue appends p to ref's queued output.
func (h *lineH) queue(ref corenet.LoopConnRef, p []byte) {
	h.mu.Lock()
	if h.pending == nil {
		h.pending = map[corenet.LoopConnRef][]byte{}
	}
	h.pending[ref] = append(h.pending[ref], p...)
	h.mu.Unlock()
}

// big queues n pattern bytes, writing what fits now and the rest on
// OnWritable.
func (h *lineH) big(io corenet.LoopIO, ref corenet.LoopConnRef, n int) {
	h.mu.Lock()
	if h.pending == nil {
		h.pending = map[corenet.LoopConnRef][]byte{}
	}
	h.pending[ref] = append(h.pending[ref], pattern(n)...)
	h.mu.Unlock()
	h.OnWritable(io, ref)
}

func (h *lineH) OnTimeout(corenet.LoopIO, corenet.LoopConnRef) {}

func (h *lineH) OnWritable(io corenet.LoopIO, ref corenet.LoopConnRef) {
	h.mu.Lock()
	defer h.mu.Unlock()
	rest := h.pending[ref]
	for len(rest) > 0 {
		n := min(len(rest), 64<<10)
		if err := io.Write(ref, rest[:n]); err != nil {
			break
		}
		rest = rest[n:]
	}
	if len(rest) == 0 {
		delete(h.pending, ref)
	} else {
		h.pending[ref] = rest
	}
}

func (h *lineH) OnBatchEnd(io corenet.LoopIO) {
	if h.batchEnd != nil {
		h.batchEnd(io)
	}
}

func (h *lineH) OnClose(_ corenet.LoopIO, ref corenet.LoopConnRef, err error) {
	h.mu.Lock()
	h.closes = append(h.closes, err)
	delete(h.pending, ref)
	h.mu.Unlock()
}

func (h *lineH) counts() (int, int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.opens, len(h.closes)
}

// pattern is n bytes counting 0 to 250, so a byte out of place shows.
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return b
}

// serve starts a group on b with h, listening on loopback, and returns its
// address. mod adjusts the Config first.
func serve(t *testing.T, b backendCase, h corenet.LoopHandler, mod func(*Config)) (*Group, string) {
	t.Helper()
	fd, addr := listenFD(t)
	cfg := Config{Group: t.Name(), Value: corenet.LoopValue{Backend: b.backend}, Handler: h, Listeners: []Listener{{FD: fd}}, Log: io.Discard}
	if mod != nil {
		mod(&cfg)
	}
	g, err := Start(cfg)
	if err != nil {
		t.Fatalf("Start: %v %v", err, errs.FieldsOf(err))
	}
	t.Cleanup(func() { _ = g.Close() })
	return g, addr
}

// listenFD binds a loopback listener and hands back a duplicated,
// non-blocking, close-on-exec descriptor of it, as server does.
func listenFD(t *testing.T) (int, string) {
	t.Helper()
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	if err := syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Listen(fd, 4096); err != nil {
		t.Fatal(err)
	}
	sa, err := syscall.Getsockname(fd)
	if err != nil {
		t.Fatal(err)
	}
	return fd, fmt.Sprintf("127.0.0.1:%d", sa.(*syscall.SockaddrInet4).Port)
}

// dial connects with a deadline on every operation.
func dial(t *testing.T, addr string) stdnet.Conn {
	t.Helper()
	c, err := stdnet.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// waitFor polls cond for up to five seconds.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestConformance_echoAndPartial pins the basic contract: lines split
// byte by byte across reads come back whole and in order.
func TestConformance_echoAndPartial(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		h := &lineH{}
		_, addr := serve(t, b, h, nil)
		c := dial(t, addr)
		msg := "hello world\nsecond line\n"
		for i := range len(msg) {
			if _, err := c.Write([]byte{msg[i]}); err != nil {
				t.Fatal(err)
			}
		}
		got := make([]byte, len(msg))
		if _, err := io.ReadFull(c, got); err != nil || string(got) != msg {
			t.Fatalf("echo = %q, %v", got, err)
		}
	})
}

// TestConformance_bigResponse is the short-send and EAGAIN case: a response
// far larger than the socket's send buffer, read slowly, arrives byte-exact
// and in order, and so do the echoes queued behind it.
func TestConformance_bigResponse(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		h := &lineH{}
		_, addr := serve(t, b, h, func(c *Config) { c.Value.OutputCap = 256 << 10 })
		c := dial(t, addr)
		const n = 8 << 20
		if _, err := c.Write([]byte("BIG " + strconv.Itoa(n) + "\nafter\n")); err != nil {
			t.Fatal(err)
		}
		r := bufio.NewReaderSize(c, 1024)
		got := make([]byte, n)
		for off := 0; off < n; {
			k, err := r.Read(got[off:min(n, off+1+off%7919)])
			if err != nil {
				t.Fatal(err)
			}
			off += k
		}
		if !bytes.Equal(got, pattern(n)) {
			t.Fatal("the large response arrived altered or out of order")
		}
		line, err := r.ReadString('\n')
		if err != nil || line != "after\n" {
			t.Fatalf("the echo behind it = %q, %v", line, err)
		}
	})
}

// TestConformance_spillFull pins that a peer sending past SpillCap without a
// newline is closed with LoopSpillFull.
func TestConformance_spillFull(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		h := &lineH{}
		_, addr := serve(t, b, h, func(c *Config) { c.Value.SpillCap = 8 << 10 })
		c := dial(t, addr)
		_, _ = c.Write(bytes.Repeat([]byte{'x'}, 64<<10))
		waitFor(t, "the spill close", func() bool { _, n := h.counts(); return n == 1 })
		if !errors.Is(h.closes[0], corenet.LoopSpillFull) {
			t.Fatalf("OnClose err = %v", h.closes[0])
		}
	})
}

// TestConformance_closeTargetOnly opens two connections and closes slot 0
// while both are live: only the target closes, the other keeps echoing.
func TestConformance_closeTargetOnly(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		h := &lineH{}
		g, addr := serve(t, b, h, nil)
		c0 := dial(t, addr)
		c1 := dial(t, addr)
		waitFor(t, "two opens", func() bool { o, _ := h.counts(); return o == 2 })
		_, _ = c0.Write([]byte("CLOSE\n"))
		// The peer must see the end itself, promptly: a socket the kernel
		// keeps after the close would time out here instead.
		_ = c0.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, err := c0.Read(make([]byte, 1)); !errors.Is(err, io.EOF) && !errors.Is(err, syscall.ECONNRESET) {
			t.Fatalf("the closed connection's peer read %v, want the end of the stream", err)
		}
		_, _ = c1.Write([]byte("alive\n"))
		got := make([]byte, 6)
		_ = c1.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(c1, got); err != nil || string(got) != "alive\n" {
			t.Fatalf("the other connection = %q, %v\n%s", got, err, dumpGroup(g))
		}
	})
}

// TestConformance_slotReuseStaleWrite closes a connection, lets its slot be
// reused, and writes with the old reference: the write goes nowhere.
func TestConformance_slotReuseStaleWrite(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		h := &lineH{}
		var stale corenet.LoopConnRef
		var staleErr error
		var once sync.Once
		h.onData = func(io corenet.LoopIO, ref corenet.LoopConnRef, buf []byte) {
			h.mu.Lock()
			old := stale
			h.mu.Unlock()
			if bytes.HasPrefix(buf, []byte("probe")) && old.Gen != 0 {
				once.Do(func() {
					err := io.Write(old, []byte("LEAK\n"))
					h.mu.Lock()
					staleErr = err
					h.mu.Unlock()
				})
			}
		}
		_, addr := serve(t, b, h, func(c *Config) { c.tableCap = 1 })
		c0 := dial(t, addr)
		waitFor(t, "the first open", func() bool { o, _ := h.counts(); return o == 1 })
		h.mu.Lock()
		stale = h.refs[0]
		h.mu.Unlock()
		_, _ = c0.Write([]byte("CLOSE\n"))
		waitFor(t, "the first close", func() bool { _, n := h.counts(); return n == 1 })
		c1 := dial(t, addr)
		waitFor(t, "the reuse", func() bool { o, _ := h.counts(); return o == 2 })
		h.mu.Lock()
		reused := h.refs[1]
		h.mu.Unlock()
		if reused.Slot != stale.Slot || reused.Gen == stale.Gen {
			t.Fatalf("slot %d gen %d, then slot %d gen %d", stale.Slot, stale.Gen, reused.Slot, reused.Gen)
		}
		_, _ = c1.Write([]byte("probe\n"))
		got := make([]byte, 6)
		if _, err := io.ReadFull(c1, got); err != nil || string(got) != "probe\n" {
			t.Fatalf("got %q, %v: the stale write reached the new connection", got, err)
		}
		h.mu.Lock()
		got2 := staleErr
		h.mu.Unlock()
		if !errors.Is(got2, corenet.LoopStaleRef) {
			t.Fatalf("stale Write = %v", got2)
		}
	})
}

// TestConformance_tinyTable fills a table of 2 slots. The two served keep
// working. epoll accepts a third and closes it, counted; io_uring stops
// accepting at a full table, so the third waits in the listen queue and is
// served once a close makes room.
func TestConformance_tinyTable(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		h := &lineH{}
		g, addr := serve(t, b, h, func(c *Config) { c.tableCap = 2 })
		a, bb := dial(t, addr), dial(t, addr)
		waitFor(t, "two opens", func() bool { o, _ := h.counts(); return o == 2 })
		c := dial(t, addr)
		for _, x := range []stdnet.Conn{a, bb} {
			_, _ = x.Write([]byte("ok\n"))
			got := make([]byte, 3)
			if _, err := io.ReadFull(x, got); err != nil {
				t.Fatal(err)
			}
		}
		if b.backend == corenet.LoopBackendEpoll {
			if _, err := c.Read(make([]byte, 1)); err == nil {
				t.Fatal("a connection past the table was served")
			}
			if g.Rejected() == 0 {
				t.Fatal("the refused connection was not counted")
			}
			return
		}
		_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		_, _ = c.Write([]byte("third\n"))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("a connection past the table was served while it was full")
		}
		_ = a.Close()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		got := make([]byte, 6)
		if _, err := io.ReadFull(c, got); err != nil || string(got) != "third\n" {
			t.Fatalf("the third connection after a close = %q, %v", got, err)
		}
	})
}

// TestConformance_maxConns pins LimitsValue.MaxConns: accepted and closed.
func TestConformance_maxConns(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		h := &lineH{}
		g, addr := serve(t, b, h, func(c *Config) { c.MaxConns = 1 })
		dial(t, addr)
		waitFor(t, "one open", func() bool { o, _ := h.counts(); return o == 1 })
		c := dial(t, addr)
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("a connection past MaxConns was served")
		}
		waitFor(t, "the rejection", func() bool { return g.Rejected() == 1 })
	})
}

// TestConformance_idle pins IdleTimeout: an idle connection closes with the
// deadline error, an active one does not.
func TestConformance_idle(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		h := &lineH{}
		_, addr := serve(t, b, h, func(c *Config) { c.IdleTimeout = 100 * time.Millisecond })
		c := dial(t, addr)
		start := time.Now()
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("read data from an idle connection")
		}
		if d := time.Since(start); d < 90*time.Millisecond {
			t.Fatalf("closed after %v, before its idle bound", d)
		}
		waitFor(t, "OnClose", func() bool { _, n := h.counts(); return n == 1 })
		if !errors.Is(h.closes[0], os.ErrDeadlineExceeded) {
			t.Fatalf("OnClose err = %v", h.closes[0])
		}
	})
}

// TestConformance_postExactlyOnce posts from many goroutines while the loop
// serves traffic: every operation runs once, on the loop.
func TestConformance_postExactlyOnce(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		h := &lineH{}
		g, addr := serve(t, b, h, nil)
		c := dial(t, addr)
		waitFor(t, "an open", func() bool { o, _ := h.counts(); return o == 1 })
		const n = 20000
		var ran [n]int32
		var mu sync.Mutex
		op := func(_ corenet.LoopIO, arg uintptr) { mu.Lock(); ran[arg]++; mu.Unlock() }
		io := g.loops[0]
		var wg sync.WaitGroup
		for p := range 8 {
			wg.Go(func() {
				for i := p; i < n; i += 8 {
					for io.Post(op, uintptr(i)) != nil {
						time.Sleep(10 * time.Microsecond)
					}
				}
			})
		}
		for range 50 {
			_, _ = c.Write([]byte("x\n"))
			_, _ = c.Read(make([]byte, 2))
		}
		wg.Wait()
		waitFor(t, "every post", func() bool {
			mu.Lock()
			defer mu.Unlock()
			for i := range ran {
				if ran[i] != 1 {
					return false
				}
			}
			return true
		})
	})
}

// TestConformance_shutdownRace drains from another goroutine while clients
// talk, under the race detector: no fd leak, every connection closed.
func TestConformance_shutdownRace(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		before := countFDs(t)
		h := &lineH{}
		g, addr := serve(t, b, h, nil)
		var wg sync.WaitGroup
		for range 16 {
			wg.Go(func() {
				c, err := stdnet.DialTimeout("tcp", addr, time.Second)
				if err != nil {
					return
				}
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				for range 50 {
					if _, err := c.Write([]byte("ping\n")); err != nil {
						return
					}
					if _, err := c.Read(make([]byte, 5)); err != nil {
						return
					}
				}
			})
		}
		time.Sleep(20 * time.Millisecond)
		done := make(chan error, 1)
		go func() {
			ctx, cancel := contextWithTimeout(2 * time.Second)
			defer cancel()
			done <- g.Shutdown(ctx)
		}()
		wg.Wait()
		<-done
		o, cl := h.counts()
		if o != cl {
			t.Fatalf("%d opened, %d closed", o, cl)
		}
		waitFor(t, "every descriptor back", func() bool { return countFDs(t) <= before })
	})
}

// TestConformance_highWaterGC queues output to the high-water mark with sends
// in flight under GOGC=1 and forced collections: the bytes arrive exact, so
// nothing the kernel reads was moved or collected.
func TestConformance_highWaterGC(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		old := debug.SetGCPercent(1)
		defer debug.SetGCPercent(old)
		h := &lineH{}
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
					runtime.GC()
				}
			}
		}()
		_, addr := serve(t, b, h, func(c *Config) { c.Value.OutputCap = 64 << 10 })
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				c, err := stdnet.DialTimeout("tcp", addr, time.Second)
				if err != nil {
					t.Error(err)
					return
				}
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(20 * time.Second))
				const n = 2 << 20
				_, _ = c.Write([]byte("BIG " + strconv.Itoa(n) + "\n"))
				got := make([]byte, n)
				if _, err := io.ReadFull(c, got); err != nil {
					t.Error(err)
					return
				}
				if !bytes.Equal(got, pattern(n)) {
					t.Error("output altered under collection")
				}
			})
		}
		wg.Wait()
	})
}

// TestConformance_emfile serves from a re-executed child under
// RLIMIT_NOFILE=64 while this process, under its own limit, opens 80
// connections: the loop never crashes at the limit, pauses its accepts and
// says so — on EMFILE for epoll and io_uring on process descriptors, at its
// one ring's full table for io_uring on direct descriptors (no MaxConns
// derives a second ring here) — and once the connections close, a new one is
// served.
func TestConformance_emfile(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		child := startServeChild(t, b, 64, 0)
		var conns []stdnet.Conn
		served := 0
		for range 80 {
			c, err := stdnet.DialTimeout("tcp", child.addr, time.Second)
			if err != nil {
				continue
			}
			conns = append(conns, c)
			_ = c.SetDeadline(time.Now().Add(300 * time.Millisecond))
			_, _ = c.Write([]byte("x\n"))
			if _, err := io.ReadFull(c, make([]byte, 2)); err == nil {
				served++
			}
		}
		for _, c := range conns {
			_ = c.Close()
		}
		deadline := time.Now().Add(5 * time.Second)
		back := false
		for !back && time.Now().Before(deadline) {
			c, err := stdnet.DialTimeout("tcp", child.addr, time.Second)
			if err == nil {
				_ = c.SetDeadline(time.Now().Add(time.Second))
				_, _ = c.Write([]byte("back\n"))
				got := make([]byte, 5)
				_, err = io.ReadFull(c, got)
				back = err == nil && string(got) == "back\n"
				_ = c.Close()
			}
			if !back {
				time.Sleep(20 * time.Millisecond)
			}
		}
		report := child.stop(t)
		t.Logf("served %d of 80 under RLIMIT 64; child: %s", served, report)
		if !back {
			t.Fatal("never served again once the connections closed")
		}
		if !strings.Contains(report, "paused=true") {
			t.Fatal("the loop never paused its accept at the limit")
		}
	})
}

// serveChild is a re-executed child serving a loop group.
type serveChild struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *bufio.Reader
	addr  string
}

// startServeChild starts a child serving on b under RLIMIT_NOFILE=rlimit and
// MaxConns=maxConns, and reads the address it listens on.
func startServeChild(t *testing.T, b backendCase, rlimit, maxConns int) *serveChild {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=serve:%s:%d:%d", childEnv, b.name, rlimit, maxConns))
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(stdout)
	addr, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("child: %v", err)
	}
	return &serveChild{cmd: cmd, stdin: stdin, out: r, addr: strings.TrimSpace(addr)}
}

// stop closes the child's standard input and returns its report.
func (c *serveChild) stop(t *testing.T) string {
	t.Helper()
	_ = c.stdin.Close()
	report, _ := io.ReadAll(c.out)
	if err := c.cmd.Wait(); err != nil {
		t.Fatalf("child: %v: %s", err, report)
	}
	return strings.TrimSpace(string(report))
}

// runChild runs a re-executed case and returns its exit status.
func runChild(c string) int {
	name, spec, _ := strings.Cut(c, ":")
	if name == "serve" {
		return childServe(spec)
	}
	fmt.Println("unknown child case", c)
	return 2
}

// countFDs counts the process's open descriptors.
func countFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(ents)
}

// contextWithTimeout is context.WithTimeout from the background.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// TestConformance_foreignThread pins that, under loopdebug, a Write, Close or
// SetDeadline from a goroutine off the loop's thread is refused with
// LoopWrongThread and does nothing. Without the tag the check is not compiled.
func TestConformance_foreignThread(t *testing.T) {
	if !loopDebug {
		t.Skip("the thread check is compiled under the loopdebug build tag only")
	}
	eachBackend(t, func(t *testing.T, b backendCase) {
		h := &lineH{}
		_, addr := serve(t, b, h, nil)
		c := dial(t, addr)
		waitFor(t, "an open", func() bool { o, _ := h.counts(); return o == 1 })
		h.mu.Lock()
		io, ref := h.lastIO, h.refs[0]
		h.mu.Unlock()
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := io.Write(ref, []byte("foreign\n")); !errors.Is(err, corenet.LoopWrongThread) {
			t.Fatalf("foreign Write = %v", err)
		}
		if err := io.SetDeadline(ref, time.Second); !errors.Is(err, corenet.LoopWrongThread) {
			t.Fatalf("foreign SetDeadline = %v", err)
		}
		if err := io.Close(ref); !errors.Is(err, corenet.LoopWrongThread) {
			t.Fatalf("foreign Close = %v", err)
		}
		_, _ = c.Write([]byte("mine\n"))
		got := make([]byte, 5)
		if _, err := io2ReadFull(c, got); err != nil || string(got) != "mine\n" {
			t.Fatalf("got %q, %v: the foreign calls reached the connection", got, err)
		}
	})
}

// io2ReadFull is io.ReadFull, named apart from the LoopIO variables.
func io2ReadFull(r io.Reader, b []byte) (int, error) { return io.ReadFull(r, b) }

// TestConformance_overRLimit is io_uring's capacity past the descriptor limit
// (ADR 0186 §D5): a child serving under RLIMIT_NOFILE=128 with MaxConns 300
// derives three rings, and holds 300 connections the parent opens, the last
// ring's included, each echoing.
func TestConformance_overRLimit(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		if b.backend != corenet.LoopBackendIOURing {
			t.Skip("only io_uring holds connections past RLIMIT_NOFILE")
		}
		if !uringDirect(t) {
			t.Skip("this kernel keeps a closed direct descriptor's socket (before 6.13): io_uring serves on process descriptors, within RLIMIT_NOFILE")
		}
		child := startServeChild(t, b, 128, 300)
		var conns []stdnet.Conn
		for i := range 300 {
			c, err := stdnet.DialTimeout("tcp", child.addr, 3*time.Second)
			if err != nil {
				t.Fatalf("dial %d: %v", i, err)
			}
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			conns = append(conns, c)
		}
		for i, c := range conns {
			msg := fmt.Sprintf("c%03d\n", i)
			_, _ = c.Write([]byte(msg))
			got := make([]byte, len(msg))
			if _, err := io.ReadFull(c, got); err != nil || string(got) != msg {
				t.Fatalf("connection %d = %q, %v", i, got, err)
			}
		}
		report := child.stop(t)
		for _, c := range conns {
			_ = c.Close()
		}
		t.Logf("child: %s", report)
		if !strings.Contains(report, "rings=3") {
			t.Fatalf("child report: %s", report)
		}
	})
}

// childServe serves under the RLIMIT_NOFILE and MaxConns its marker names
// until its standard input closes, then reports its rings.
func childServe(spec string) int {
	parts := strings.Split(spec, ":")
	var b corenet.LoopBackend
	for _, x := range backendsPresent() {
		if x.name == parts[0] {
			b = x.backend
		}
	}
	rlimit, _ := strconv.Atoi(parts[1])
	maxConns, _ := strconv.Atoi(parts[2])
	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &syscall.Rlimit{Cur: uint64(rlimit), Max: uint64(rlimit)}); err != nil {
		fmt.Println("setrlimit:", err)
		return 1
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return 1
	}
	_ = syscall.Bind(fd, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}})
	_ = syscall.Listen(fd, 1024)
	sa, _ := syscall.Getsockname(fd)
	h := &lineH{}
	g, err := Start(Config{Group: "child", Value: corenet.LoopValue{Backend: b}, MaxConns: maxConns, Handler: h, Listeners: []Listener{{FD: fd}}, Log: io.Discard})
	if err != nil {
		fmt.Println("start:", err)
		return 1
	}
	fmt.Printf("127.0.0.1:%d\n", sa.(*syscall.SockaddrInet4).Port)
	_, _ = io.Copy(io.Discard, os.Stdin)
	st := g.State()
	var accepted []uint64
	paused := false
	for _, r := range st.RingStates {
		accepted = append(accepted, r.Accepted)
		paused = paused || r.AcceptPauses > 0
	}
	fmt.Printf("rings=%d source=%s accepted=%v paused=%v rlimit=%d stage=%s\n", st.Rings, st.RingsSource, accepted, paused, st.RLimitNoFile, st.ProbeStage)
	_ = g.Close()
	return 0
}

// onlyIOURing skips a case that injects io_uring's own faults.
func onlyIOURing(t *testing.T, b backendCase) {
	t.Helper()
	if b.backend != corenet.LoopBackendIOURing {
		t.Skip("an io_uring fault")
	}
}

// TestConformance_bufferFaults runs io_uring on a two-entry provided-buffer
// ring of 64-byte buffers, recycled in a scrambled order and filled with 0xAA
// before each return: every recv runs out of buffers (ENOBUFS, re-armed once
// one comes back), and every byte echoed must be the byte sent, so a bid that
// did not match its buffer's content, or a buffer read after its return,
// shows as 0xAA or as another connection's bytes.
func TestConformance_bufferFaults(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		onlyIOURing(t, b)
		h := &lineH{}
		g, addr := serve(t, b, h, func(c *Config) {
			c.hooks = loopHooks{bufs: 2, bufSize: 64, fill: true, shuffle: true}
		})
		var wg sync.WaitGroup
		for k := range 8 {
			wg.Go(func() {
				c, err := stdnet.DialTimeout("tcp", addr, time.Second)
				if err != nil {
					t.Error(err)
					return
				}
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(20 * time.Second))
				r := bufio.NewReader(c)
				for i := range 100 {
					n := 1 + (i*37+k*101)%300
					line := bytes.Repeat([]byte{byte('a' + k)}, n)
					line = append(line, '\n')
					if _, err := c.Write(line); err != nil {
						t.Error(err)
						return
					}
					got, err := r.ReadBytes('\n')
					if err != nil || !bytes.Equal(got, line) {
						t.Errorf("client %d line %d: %d bytes back, %v", k, i, len(got), err)
						return
					}
				}
			})
		}
		wg.Wait()
		var starved uint64
		for _, r := range g.State().RingStates {
			starved += r.BufferStarved
		}
		t.Logf("ENOBUFS re-arms: %d", starved)
		if starved == 0 {
			t.Fatal("a two-entry buffer ring never ran out: the fault was not injected")
		}
	})
}

// TestConformance_probeStages fails each stage of the io_uring probe in turn:
// an explicit io_uring is refused with LoopBackendUnavailable naming the
// stage, and auto falls back to epoll, degraded, with the reason.
func TestConformance_probeStages(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		onlyIOURing(t, b)
		for _, stage := range []string{"setup", "probe", "self_test", "buf_ring", "sparse_files"} {
			t.Run(stage, func(t *testing.T) {
				fd, _ := listenFD(t)
				_, err := Start(Config{Group: "probe", Value: corenet.LoopValue{Backend: corenet.LoopBackendIOURing}, Handler: &lineH{}, Listeners: []Listener{{FD: fd}}, Log: io.Discard, hooks: loopHooks{failStage: stage}})
				if !errs.HasCode(err, corenet.CodeLoopBackendUnavailable) {
					t.Fatalf("explicit io_uring = %v", err)
				}
				fd2, addr := listenFD(t)
				g, err := Start(Config{Group: "probe", Value: corenet.LoopValue{}, Handler: &lineH{}, Listeners: []Listener{{FD: fd2}}, Log: io.Discard, hooks: loopHooks{failStage: stage}})
				if err != nil {
					t.Fatalf("auto: %v", err)
				}
				defer g.Close()
				st := g.State()
				if st.Backend != corenet.LoopBackendEpoll || !st.Degraded || !strings.Contains(st.DegradedReason, "io_uring") {
					t.Fatalf("auto fell back to %v degraded=%v %q", st.Backend, st.Degraded, st.DegradedReason)
				}
				c := dial(t, addr)
				_, _ = c.Write([]byte("fallback\n"))
				got := make([]byte, 9)
				if _, err := io.ReadFull(c, got); err != nil {
					t.Fatalf("the fallback does not serve: %v", err)
				}
			})
		}
	})
}

// TestConformance_deferTaskrun runs the echo, the large response and the
// close under IORING_SETUP_DEFER_TASKRUN, the option that must pass the
// suite before it may become the default (ADR 0186 §Deferred).
func TestConformance_deferTaskrun(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		onlyIOURing(t, b)
		h := &lineH{}
		_, addr := serve(t, b, h, func(c *Config) {
			c.hooks = loopHooks{deferTaskrun: true}
			c.Value.OutputCap = 256 << 10
		})
		c := dial(t, addr)
		const n = 2 << 20
		_, _ = c.Write([]byte("hi\nBIG " + strconv.Itoa(n) + "\n"))
		r := bufio.NewReader(c)
		if line, err := r.ReadString('\n'); err != nil || line != "hi\n" {
			t.Fatalf("echo = %q, %v", line, err)
		}
		got := make([]byte, n)
		if _, err := io.ReadFull(r, got); err != nil || !bytes.Equal(got, pattern(n)) {
			t.Fatalf("large response: %v", err)
		}
		_, _ = c.Write([]byte("CLOSE\n"))
		if _, err := r.ReadByte(); err != io.EOF {
			t.Fatalf("after CLOSE = %v, want EOF", err)
		}
	})
}

// TestConformance_idleNoWatchdog holds 10 000 idle connections for longer
// than the watchdog's re-arms take: an armed recv on an empty socket is
// healthy, so the watchdog fires zero times and nothing falls back.
func TestConformance_idleNoWatchdog(t *testing.T) {
	if lim := descriptorLimit(); lim < 2*10000+256 {
		t.Skipf("RLIMIT_NOFILE %d is too low", lim)
	}
	eachBackend(t, func(t *testing.T, b backendCase) {
		onlyIOURing(t, b)
		h := &lineH{}
		g, addr := serve(t, b, h, nil)
		var conns []stdnet.Conn
		for range 10000 {
			c, err := stdnet.DialTimeout("tcp", addr, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			conns = append(conns, c)
		}
		defer func() {
			for _, c := range conns {
				_ = c.Close()
			}
		}()
		waitFor(t, "every open", func() bool { return g.Live() == 10000 })
		time.Sleep(time.Duration(watchdogFires+1) * time.Duration(watchdogTicks) * tickEvery)
		st := g.State()
		for _, r := range st.RingStates {
			if r.WatchdogFires != 0 || r.Draining {
				t.Fatalf("ring %d: %d watchdog fires, draining %v", r.Ring, r.WatchdogFires, r.Draining)
			}
		}
		if st.Degraded {
			t.Fatalf("degraded: %s", st.DegradedReason)
		}
	})
}

// dumpOut receives dumpGroup's snapshot, taken on the loop's own thread.
var dumpOut = make(chan string, 1)

// dumpGroup snapshots every slot of the group's first loop from its own
// thread, through Post, for a failure message.
func dumpGroup(g *Group) string {
	lp := g.loops[0]
	op := func(_ corenet.LoopIO, _ uintptr) {
		var b strings.Builder
		for _, r := range lp.rings {
			fmt.Fprintf(&b, "ring %d live=%d free=%v\n", r.index, r.live, r.free)
			for s := range r.conns {
				c := &r.conns[s]
				fmt.Fprintf(&b, "  slot %d gen=%d state=%d flags=%#x fd=%d ops=%d spill=%+v out=%+v flight=%+v\n",
					s, c.gen, c.state, c.flags, c.fd, c.ops, c.spill, c.out, c.flight)
			}
		}
		fmt.Fprintf(&b, "state: %+v", g.State())
		dumpOut <- b.String()
	}
	if err := lp.post.post(op, 0); err != nil {
		return "dump: " + err.Error()
	}
	select {
	case s := <-dumpOut:
		return s
	case <-time.After(2 * time.Second):
		return "dump: the loop did not run the post within 2 s"
	}
}

// uringDirect reports whether io_uring serves on direct descriptors here.
func uringDirect(t *testing.T) bool {
	t.Helper()
	g, err := Start(Config{Group: "mode", Value: corenet.LoopValue{Backend: corenet.LoopBackendIOURing}, Handler: &lineH{}, Log: io.Discard})
	if err != nil {
		t.Fatalf("io_uring: %v", err)
	}
	defer g.Close()
	return strings.HasSuffix(g.State().ProbeStage, "+direct")
}

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
