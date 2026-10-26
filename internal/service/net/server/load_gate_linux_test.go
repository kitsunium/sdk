//go:build linux

// The loads the network plan's merge gate measures (plan P8, tools/netgate):
// L1 and L2 here, L3 in load_l3_linux_internal_test.go. Each is fixed in this
// code, never by a flag, so two runs of the gate measure the same thing. The
// server runs in a child process — this test binary re-run on TestLoadChild —
// and the metric is ITS CPU, read through getrusage at each window's two ends,
// so the client's cost never dilutes the ratio between two engines.
//
// The measure is paired: one child serves engine A and engine B side by side,
// and one round sends a quarter of the requests to each in the order A, B, B,
// A, so a drift of the runner during the round lands on both and its linear
// part cancels. The round reports B's CPU a request over A's — one ratio
// sample — and tools/netgate judges the interval of those ratios
// (BenchmarkLoad<load>/pair=<A>:<B>).

package server_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	stdnet "net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// The loads, as the plan fixes them.
const (
	// loadConns is L1's and L2's connection count, every one kept alive.
	loadConns = 1000
	// loadBodySize is the response body both loads answer.
	loadBodySize = 1024
	// loadToken is L2's Bearer token.
	loadToken = "Bearer netgate-l2-3f9c1a7e5b2d4c6a"
)

// loadL1Request is L1's request: a plaintext GET, kept alive.
var loadL1Request = []byte("GET /hello HTTP/1.1\r\nHost: netgate\r\n\r\n")

// loadL2Request is L2's: L1 with a Bearer token, on a route among several.
var loadL2Request = []byte("GET /api/v1/items/42 HTTP/1.1\r\nHost: netgate\r\nAuthorization: " + loadToken + "\r\n\r\n")

// loadBody is the 1 KiB answer.
var loadBody = bytes.Repeat([]byte("k"), loadBodySize)

// loadChildEnv names the load and engine a re-run child serves: "L1:loop".
const loadChildEnv = "KITSUNIUM_NETGATE_CHILD"

// TestLoadChild is the server half of BenchmarkLoadL1 and BenchmarkLoadL2, and
// runs only in the child they start: "L2:handwritten,chain" serves load on
// each engine named, one group each, side by side. It prints their addresses
// in that order, then answers each line on stdin with its CPU, until stdin
// closes.
func TestLoadChild(t *testing.T) {
	spec := os.Getenv(loadChildEnv)
	if spec == "" || strings.HasPrefix(spec, "L3:") {
		t.Skip("the server half of BenchmarkLoadL1/L2: run by them, in a child")
	}
	load, engines, _ := strings.Cut(spec, ":")
	srv := server.New()
	for i, engine := range strings.Split(engines, ",") {
		g := srv.Group("netgate-"+strconv.Itoa(i), server.Listen("tcp", "127.0.0.1:0"), loadGroupOption(engine))
		serveLoad(t, g, load, engine)
	}
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	line := "addr"
	for _, l := range srv.State().Listeners {
		line += " " + l.Address
	}
	fmt.Println(line)
	answerSamples(os.Stdin, os.Stdout)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// serveLoad declares engine's handler of load on g.
func serveLoad(t *testing.T, g *server.StreamGroup, load, engine string) {
	switch {
	case load == "L1" && engine == "goroutine":
		g.HandleHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(loadBodySize))
			_, _ = w.Write(loadBody)
		}))
	case load == "L1" && engine == "loop":
		g.HandleLoop(http1.HandleLoop(corenet.HTTP1HandlerFunc(func(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
			res.Status, res.Body = http.StatusOK, loadBody
		}), http1.Parser{}))
	case load == "L2" && engine == "handwritten":
		g.HandleLoop(http1.HandleLoop(corenet.HTTP1HandlerFunc(handwrittenL2), http1.Parser{}))
	default:
		t.Fatalf("no engine %q on load %q", engine, load)
	}
}

// loadGroupOption is the engine's group option: a loop on the platform's best
// backend, or none for the goroutine engine.
func loadGroupOption(engine string) server.GroupOption {
	if engine == "goroutine" {
		return server.Plaintext()
	}
	return server.Loop(corenet.LoopValue{Backend: corenet.LoopBackendAuto})
}

// loadItemsRoute and loadUsersRoute are L2's routes besides /health.
var (
	loadItemsRoute = []byte("/api/v1/items/")
	loadUsersRoute = []byte("/api/v1/users/")
	loadHealth     = []byte("/health")
	loadBearer     = []byte(loadToken)
)

// handwrittenL2 is L2's engine A: the auth and the routing a P2 chain will
// do, written by hand in one handler. It allocates nothing.
func handwrittenL2(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	if !bytes.Equal(req.Authorization, loadBearer) {
		res.Status = http.StatusUnauthorized
		return
	}
	switch t := req.Target; {
	case bytes.HasPrefix(t, loadItemsRoute) && len(t) > len(loadItemsRoute),
		bytes.HasPrefix(t, loadUsersRoute) && len(t) > len(loadUsersRoute):
		res.Status, res.Body = http.StatusOK, loadBody
	case bytes.Equal(t, loadHealth):
		res.Status = http.StatusNoContent
	default:
		res.Status = http.StatusNotFound
	}
}

// answerSamples answers each line read from in with one line on out: the
// process's user+system CPU, all threads, in nanoseconds. It returns when in
// closes. Its allocations are not counted here: a process-wide count would
// count the runtime's too, and the 0-alloc budgets are held on their own
// stack by the alloc lane.
func answerSamples(in io.Reader, out io.Writer) {
	r := bufio.NewReader(in)
	line := make([]byte, 0, 32)
	for {
		if _, err := r.ReadSlice('\n'); err != nil {
			return
		}
		var ru syscall.Rusage
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
		line = strconv.AppendInt(line[:0], ru.Utime.Nano()+ru.Stime.Nano(), 10)
		_, _ = out.Write(append(line, '\n'))
	}
}

// loadChild is a running server child.
type loadChild struct {
	cmd   *exec.Cmd
	in    io.WriteCloser
	out   *bufio.Reader
	addrs []string
}

// startLoadChild re-runs this binary on TestLoadChild serving load on each of
// engines, side by side.
func startLoadChild(b testing.TB, load string, engines ...string) *loadChild {
	b.Helper()
	spec := load + ":" + strings.Join(engines, ",")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLoadChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), loadChildEnv+"="+spec)
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		b.Fatal(err)
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		b.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		b.Fatal(err)
	}
	c := &loadChild{cmd: cmd, in: in, out: bufio.NewReader(outPipe)}
	b.Cleanup(c.stop)
	line, err := c.out.ReadString('\n')
	addrs, ok := strings.CutPrefix(strings.TrimSpace(line), "addr ")
	c.addrs = strings.Fields(addrs)
	if err != nil || !ok || len(c.addrs) != len(engines) {
		b.Fatalf("child %s printed %q, %v, want one address an engine", spec, line, err)
	}
	return c
}

// sample asks the child for its counters.
func (c *loadChild) sample(b testing.TB) []uint64 {
	b.Helper()
	if _, err := c.in.Write([]byte("s\n")); err != nil {
		b.Fatal(err)
	}
	line, err := c.out.ReadString('\n')
	if err != nil {
		b.Fatalf("child sample: %v", err)
	}
	var vs []uint64
	for _, f := range strings.Fields(line) {
		v, perr := strconv.ParseUint(f, 10, 64)
		if perr != nil {
			b.Fatalf("child sample %q: %v", line, perr)
		}
		vs = append(vs, v)
	}
	return vs
}

// stop closes the child's stdin, which ends it, and waits.
func (c *loadChild) stop() {
	_ = c.in.Close()
	done := make(chan struct{})
	go func() { _ = c.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = c.cmd.Process.Kill()
		<-done
	}
}

// BenchmarkLoadL1 is L1 paired: the goroutine engine against the loop engine,
// the P6 gate's A and B, and the loop engine against itself, the
// calibration: the ratio two series of one engine give on this runner.
func BenchmarkLoadL1(b *testing.B) {
	for _, p := range [][2]string{{"goroutine", "loop"}, {"loop", "loop"}} {
		b.Run("pair="+p[0]+":"+p[1], func(b *testing.B) { benchPairedLoad(b, "L1", p[0], p[1], loadL1Request) })
	}
}

// BenchmarkLoadL2 is L2 paired: the hand-written handler against itself, the
// calibration of the P2 gate, whose B — the http1 chain — lands with P2.
func BenchmarkLoadL2(b *testing.B) {
	b.Run("pair=handwritten:handwritten", func(b *testing.B) {
		benchPairedLoad(b, "L2", "handwritten", "handwritten", loadL2Request)
	})
}

// pairedWindows is how many windows of each engine one round sends: A, B
// then B, A, repeated, so a drift of the runner during the round lands on
// both engines alike and its linear part cancels.
const pairedWindows = 8

// loadSide is one engine of a pair: its kept-alive connections and their
// readers.
type loadSide struct {
	conns   []stdnet.Conn
	readers []*bufio.Reader
}

// dialSide opens loadConns kept-alive connections to addr and serves one
// round trip on each, so every one is accepted and served before a window.
func dialSide(b *testing.B, addr string, req []byte) *loadSide {
	s := &loadSide{}
	for i := range loadConns {
		c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			b.Fatalf("dial %d: %v", i, err)
		}
		s.conns = append(s.conns, c)
		s.readers = append(s.readers, bufio.NewReaderSize(c, 4096))
	}
	b.Cleanup(func() {
		for _, c := range s.conns {
			_ = c.Close()
		}
	})
	for i, c := range s.conns {
		if err := loadRoundTrip(c, s.readers[i], req); err != nil {
			b.Fatal(err)
		}
	}
	return s
}

// window sends n requests over the side's connections, each connection one
// at a time, and returns the child's CPU it took.
func (s *loadSide) window(b *testing.B, child *loadChild, n int, req []byte) uint64 {
	before := child.sample(b)
	var next atomic.Int64
	var wg sync.WaitGroup
	errc := make(chan error, len(s.conns))
	for i, c := range s.conns {
		wg.Go(func() {
			for next.Add(1) <= int64(n) {
				if err := loadRoundTrip(c, s.readers[i], req); err != nil {
					errc <- err
					return
				}
			}
		})
	}
	wg.Wait()
	after := child.sample(b)
	close(errc)
	if err := <-errc; err != nil {
		b.Fatal(err)
	}
	return after[0] - before[0]
}

// benchPairedLoad serves load on engines a and b in one child, opens
// loadConns kept-alive connections to each, and sends b.N requests in
// 2×pairedWindows windows, in the order a, b, b, a, a, b, …. Setup and Start are
// outside the windows, and an error fails the bench, so no error path is
// counted. It reports each engine's CPU a request and their ratio, b over a:
// the round's one paired sample.
func benchPairedLoad(b *testing.B, load, engineA, engineB string, req []byte) {
	child := startLoadChild(b, load, engineA, engineB)
	sideA, sideB := dialSide(b, child.addrs[0], req), dialSide(b, child.addrs[1], req)
	per := max(b.N/(2*pairedWindows), 1)
	b.ResetTimer()
	var cpuA, cpuB uint64
	//: A, B, B, A, A, B, B, A, …: every window of B has one of A beside it.
	for w := range pairedWindows {
		first, second := sideA, sideB
		if w%2 == 1 {
			first, second = sideB, sideA
		}
		c1, c2 := first.window(b, child, per, req), second.window(b, child, per, req)
		if w%2 == 1 {
			c1, c2 = c2, c1
		}
		cpuA, cpuB = cpuA+c1, cpuB+c2
	}
	b.StopTimer()
	perA := float64(cpuA) / float64(pairedWindows*per)
	perB := float64(cpuB) / float64(pairedWindows*per)
	b.ReportMetric(perA, "a:cpu-ns/req")
	b.ReportMetric(perB, "b:cpu-ns/req")
	b.ReportMetric(perB/perA, "ratio:cpu-ns/req")
}

// loadRoundTrip writes req on c and reads one 200 response with a 1 KiB body.
func loadRoundTrip(c stdnet.Conn, br *bufio.Reader, req []byte) error {
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write(req); err != nil {
		return err
	}
	status, err := br.ReadSlice('\n')
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(status, []byte("HTTP/1.1 200 ")) {
		return fmt.Errorf("status line %q", status)
	}
	length := -1
	for {
		line, rerr := br.ReadSlice('\n')
		if rerr != nil {
			return rerr
		}
		if len(line) <= 2 {
			break
		}
		if v, ok := bytes.CutPrefix(line, []byte("Content-Length: ")); ok {
			length, _ = strconv.Atoi(string(bytes.TrimSpace(v)))
		}
	}
	if length != loadBodySize {
		return fmt.Errorf("Content-Length %d, want %d", length, loadBodySize)
	}
	_, err = br.Discard(length)
	return err
}

// TestLoadPaired_wired serves two engines side by side in one child, as a
// paired round does, and requires each address to answer its own engine's
// load and the child's CPU to move: the pair is wired, not only each engine.
func TestLoadPaired_wired(t *testing.T) {
	child := startLoadChild(t, "L1", "goroutine", "loop")
	before := child.sample(t)
	for _, addr := range child.addrs {
		conn, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		br := bufio.NewReader(conn)
		for range 3 {
			if err := loadRoundTrip(conn, br, loadL1Request); err != nil {
				t.Fatalf("%s: %v", addr, err)
			}
		}
		_ = conn.Close()
	}
	if after := child.sample(t); after[0] <= before[0] || child.addrs[0] == child.addrs[1] {
		t.Fatalf("CPU %v then %v on %v: want two addresses and the CPU grown", before, after, child.addrs)
	}
}

// TestLoadL1L2_wired serves each engine of L1 and L2 in its child for a few
// requests and reads its counters, so a harness that no longer serves its load
// fails the suite, not only the bench the suite never runs.
func TestLoadL1L2_wired(t *testing.T) {
	for _, c := range []struct {
		load, engine string
		req          []byte
	}{{"L1", "goroutine", loadL1Request}, {"L1", "loop", loadL1Request}, {"L2", "handwritten", loadL2Request}} {
		t.Run(c.load+"/"+c.engine, func(t *testing.T) {
			child := startLoadChild(t, c.load, c.engine)
			conn, err := stdnet.DialTimeout("tcp", child.addrs[0], 5*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			before := child.sample(t)
			br := bufio.NewReader(conn)
			for range 3 {
				if err := loadRoundTrip(conn, br, c.req); err != nil {
					t.Fatal(err)
				}
			}
			after := child.sample(t)
			if len(after) != 1 || after[0] <= before[0] {
				t.Fatalf("CPU %v then %v: want one counter, grown by three requests", before, after)
			}
		})
	}
}
