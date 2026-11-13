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
	"errors"
	"fmt"
	"io"
	stdnet "net"
	"os"
	"os/exec"
	"path/filepath"
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

// requiredCaps is the comma-separated list of kernel capabilities this test
// binary must find, set at link time beside requiredBackends:
//
//	go test -ldflags='-X=github.com/kitsunium/sdk/internal/service/net/loop.requiredBackends=iouring -X=github.com/kitsunium/sdk/internal/service/net/loop.requiredCaps=uring-direct'
//
// "none" requires none. Like requiredBackends it fails closed: unset fails, a
// capability this kernel lacks fails, and a capability no probe of capProbes
// checks fails too — a lane may not require what nothing verifies. The
// net-kernel-gate workflow declares both per leg.
var requiredCaps string

// capProbes are the capabilities a lane may require, each with the probe that
// proves it on the running kernel. P3's capability reports add theirs here.
var capProbes = map[string]func() error{
	// uring-direct: io_uring's release check chose direct descriptors (a
	// kernel from 6.13, ADR 0186).
	"uring-direct": probeURingDirect,
	// uring-sock-getsockopt: the socket command read a peer's credentials on
	// the self-test's direct descriptor (6.7, #974); on or available, the
	// release check deciding whether the group uses it.
	"uring-sock-getsockopt": probeURingSockGetsockopt,
}

// errCapAbsent reports a capability whose probe ran and did not find it.
var errCapAbsent = errors.New("capability absent")

// probeURingDirect starts an io_uring group and reads its probe stage.
func probeURingDirect() error {
	g, err := Start(Config{Group: "probe", Value: corenet.LoopValue{Backend: corenet.LoopBackendIOURing}, Handler: &lineH{}, Log: io.Discard})
	if err != nil {
		return err
	}
	stage := g.State().ProbeStage
	if err := g.Close(); err != nil {
		return err
	}
	if !strings.HasSuffix(stage, "+direct") {
		return fmt.Errorf("%w: probe stage %q, want +direct", errCapAbsent, stage)
	}
	return nil
}

// probeURingSockGetsockopt starts an io_uring group on a Unix listener and
// reads its uring-sock-getsockopt row.
func probeURingSockGetsockopt() error {
	dir, err := os.MkdirTemp("", "p")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	fd, err := syscall.Socket(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	if err := syscall.Bind(fd, &syscall.SockaddrUnix{Name: filepath.Join(dir, "s")}); err != nil {
		_ = syscall.Close(fd)
		return err
	}
	if err := syscall.Listen(fd, 8); err != nil {
		_ = syscall.Close(fd)
		return err
	}
	g, err := Start(Config{Group: "probe", Value: corenet.LoopValue{Backend: corenet.LoopBackendIOURing}, Handler: &lineH{}, Listeners: []Listener{{FD: fd}}, Log: io.Discard})
	if err != nil {
		return err
	}
	var row corenet.CapabilityValue
	for _, r := range g.State().Capabilities {
		if r.Name == corenet.CapabilityURingSockGetsockopt {
			row = r
		}
	}
	if err := g.Close(); err != nil {
		return err
	}
	if row.Status != statusOn && row.Status != statusAvailable {
		return fmt.Errorf("%w: uring-sock-getsockopt %s: %s", errCapAbsent, row.Status, row.Reason)
	}
	return nil
}

// idleClients is the memory test's client process, set by its file in the
// race-off build.
var idleClients func(addr string) int

// childEnv marks a re-executed child and names the case it runs.
const childEnv = "KITSUNIUM_LOOP_CHILD"

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

// TestCapsRequired fails closed as TestBackendsRequired does: no expectation
// compiled in, a capability without a probe, and a capability the kernel
// lacks each fail, naming the kernel.
func TestCapsRequired(t *testing.T) {
	if requiredCaps == "" {
		t.Fatal("no capability expectation compiled in: build with -ldflags=-X=github.com/kitsunium/sdk/internal/service/net/loop.requiredCaps=<list|none>")
	}
	if requiredCaps == "none" {
		return
	}
	for _, want := range strings.Split(requiredCaps, ",") {
		probe, ok := capProbes[want]
		if !ok {
			t.Errorf("capability %q is required on this lane and no probe checks it", want)
			continue
		}
		if err := probe(); err != nil {
			t.Errorf("capability %q is required on this lane and absent on %s: %v %v", want, kernelRelease(), err, errs.FieldsOf(err))
		}
	}
	t.Logf("net/loop capabilities: required=%s kernel=%s", requiredCaps, kernelRelease())
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

// listenFD binds a loopback listener and hands back a duplicated,
// non-blocking, close-on-exec descriptor of it, as server does.
func listenFD(t testing.TB) (int, string) {
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
	if name == "drain" {
		return childDrain(spec)
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

// eachBufMode runs fn with io_uring's buffers consumed incrementally
// (IOU_PBUF_RING_INC) and one buffer a completion, as subtests "inc" and
// "plain"; fn calls requireMode once its group runs.
func eachBufMode(t *testing.T, fn func(t *testing.T, inc bool)) {
	t.Helper()
	for _, inc := range []bool{true, false} {
		name := "plain"
		if inc {
			name = "inc"
		}
		t.Run(name, func(t *testing.T) { fn(t, inc) })
	}
}

// bufMode switches incremental consumption off in c unless inc.
func bufMode(c *Config, inc bool) {
	if !inc {
		c.Disabled = append(c.Disabled, corenet.CapabilityURingPbufInc)
	}
}

// requireMode skips the "inc" case of a group whose kernel refused
// incremental consumption: it would run one buffer a completion under the
// other mode's name.
func requireMode(t *testing.T, g *Group, inc bool) {
	t.Helper()
	if !inc {
		return
	}
	for _, row := range g.State().Capabilities {
		if row.Name == corenet.CapabilityURingPbufInc && row.Status != statusOn {
			t.Skipf("uring-pbuf-inc %s on %s: %s", row.Status, kernelRelease(), row.Reason)
		}
	}
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
		eachBufMode(t, func(t *testing.T, inc bool) {
			h := &lineH{}
			g, addr := serve(t, b, h, func(c *Config) {
				c.hooks = loopHooks{bufs: 2, bufSize: 64, fill: true, shuffle: true}
				// Bundles where the kernel has them: a completion spans both
				// buffers in their scrambled ring order (ADR 0205).
				c.Value.Capabilities = []corenet.Capability{corenet.CapabilityURingRecvBundle}
				bufMode(c, inc)
			})
			requireMode(t, g, inc)
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
// close with uring-defer-taskrun opted into: IORING_SETUP_DEFER_TASKRUN on
// the primary ring, the class A-code option that must pass the suite before
// it may become the default (ADR 0189, P3a).
func TestConformance_deferTaskrun(t *testing.T) {
	eachBackend(t, func(t *testing.T, b backendCase) {
		onlyIOURing(t, b)
		h := &lineH{}
		_, addr := serve(t, b, h, func(c *Config) {
			c.Value.Capabilities = []corenet.Capability{corenet.CapabilityURingDeferTaskrun}
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
		defer resetClose(conns)
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
