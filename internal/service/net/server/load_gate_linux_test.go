//go:build linux

// The loads the network plan's merge gate measures (plan P8, tools/netgate):
// L1 and L2 here, L3 in load_l3_linux_internal_test.go. Each is fixed in this
// code, never by a flag, so two runs of the gate measure the same thing. The
// server runs in a child process — this test binary re-run on TestLoadChild —
// and the metric is ITS CPU, read through getrusage at the window's two ends,
// so the client's cost never dilutes the ratio between two engines.

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
// runs only in the child they start. It prints its address, then answers each
// line on stdin with its CPU, until stdin closes.
func TestLoadChild(t *testing.T) {
	spec := os.Getenv(loadChildEnv)
	if spec == "" || strings.HasPrefix(spec, "L3:") {
		t.Skip("the server half of BenchmarkLoadL1/L2: run by them, in a child")
	}
	load, engine, _ := strings.Cut(spec, ":")
	srv := server.New()
	g := srv.Group("netgate", server.Listen("tcp", "127.0.0.1:0"), loadGroupOption(engine))
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
	case load == "L2" && engine == "chain":
		g.HandleLoop(http1.HandleLoop(chainL2(t), http1.Parser{}))
	default:
		t.Fatalf("no engine %q on load %q", engine, load)
	}
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fmt.Printf("addr %s\n", srv.State().Listeners[0].Address)
	answerSamples(os.Stdin, os.Stdout)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// loadGroupOption is the engine's group option: a loop on the platform's best
// backend, or none for the goroutine engine.
func loadGroupOption(engine string) server.GroupOption {
	if engine == "goroutine" {
		return server.Name("netgate")
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

// handwrittenL2 is L2's engine A: the auth and the routing chainL2 does,
// written by hand in one handler. It allocates nothing.
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

// The routes of chainL2, by the identifier its handler switches on.
const (
	loadRouteItems = iota + 1
	loadRouteUsers
	loadRouteHealth
)

// loadVerifier is chainL2's: the one token L2 sends, and a subject.
type loadVerifier struct{}

// loadTokenBytes and loadSubject are what loadVerifier compares and sets.
var (
	loadTokenBytes = []byte(strings.TrimPrefix(loadToken, "Bearer "))
	loadSubject    = []byte("netgate")
)

func (loadVerifier) VerifyBearer(token []byte, auth *corenet.HTTP1AuthValue) bool {
	return bytes.Equal(token, loadTokenBytes) && auth.SetSubject(loadSubject)
}

// chainL2 is L2's engine B: the same auth and routing as handwrittenL2, built
// from the http1 chain's links — Routes, then Bearer with /health exempt —
// in front of a handler that answers by route.
func chainL2(t testing.TB) corenet.HTTP1Handler {
	routes, err := http1.Routes(
		http1.RouteValue{ID: loadRouteItems, Path: string(loadItemsRoute), Prefix: true},
		http1.RouteValue{ID: loadRouteUsers, Path: string(loadUsersRoute), Prefix: true},
		http1.RouteValue{ID: loadRouteHealth, Path: string(loadHealth)},
	)
	if err != nil {
		t.Fatal(err)
	}
	bearer, err := http1.Bearer(loadVerifier{}, loadRouteHealth)
	if err != nil {
		t.Fatal(err)
	}
	app := corenet.HTTP1HandlerFunc(func(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		switch req.Route {
		case loadRouteItems, loadRouteUsers:
			res.Status, res.Body = http.StatusOK, loadBody
		case loadRouteHealth:
			res.Status = http.StatusNoContent
		default:
			res.Status = http.StatusNotFound
		}
	})
	return corenet.Chain[corenet.HTTP1Handler](app, routes, bearer)
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
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	addr string
}

// startLoadChild re-runs this binary on TestLoadChild serving load on engine.
func startLoadChild(b testing.TB, load, engine string) *loadChild {
	b.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLoadChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), loadChildEnv+"="+load+":"+engine)
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
	addr, ok := strings.CutPrefix(strings.TrimSpace(line), "addr ")
	if err != nil || !ok {
		b.Fatalf("child %s:%s printed %q, %v, want its address", load, engine, line, err)
	}
	c.addr = addr
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

// BenchmarkLoadL1 is L1 on the goroutine engine and on the loop engine: the
// P6 gate's A and B, and the P5 gate's B.
func BenchmarkLoadL1(b *testing.B) {
	for _, engine := range []string{"goroutine", "loop"} {
		b.Run("engine="+engine, func(b *testing.B) { benchHTTPLoad(b, "L1", engine, loadL1Request) })
	}
}

// BenchmarkLoadL2 is L2 on the hand-written handler and on the http1 chain:
// the P2 gate's A and B.
func BenchmarkLoadL2(b *testing.B) {
	for _, engine := range []string{"handwritten", "chain"} {
		b.Run("engine="+engine, func(b *testing.B) { benchHTTPLoad(b, "L2", engine, loadL2Request) })
	}
}

// benchHTTPLoad serves load on engine in a child, opens loadConns kept-alive
// connections, and sends b.N requests over them, each connection one at a
// time. The window opens once every connection is up — setup and Start are
// outside it — and an error fails the bench, so no error path is counted.
func benchHTTPLoad(b *testing.B, load, engine string, req []byte) {
	child := startLoadChild(b, load, engine)
	conns := make([]stdnet.Conn, loadConns)
	for i := range conns {
		c, err := stdnet.DialTimeout("tcp", child.addr, 5*time.Second)
		if err != nil {
			b.Fatalf("dial %d: %v", i, err)
		}
		conns[i] = c
	}
	b.Cleanup(func() {
		for _, c := range conns {
			_ = c.Close()
		}
	})
	// One round trip on every connection, so each is accepted and served once
	// before the window.
	for _, c := range conns {
		if err := loadRoundTrip(c, bufio.NewReader(c), req); err != nil {
			b.Fatal(err)
		}
	}
	before := child.sample(b)
	b.ResetTimer()
	var next atomic.Int64
	var wg sync.WaitGroup
	errc := make(chan error, len(conns))
	for _, c := range conns {
		wg.Go(func() {
			br := bufio.NewReaderSize(c, 4096)
			for next.Add(1) <= int64(b.N) {
				if err := loadRoundTrip(c, br, req); err != nil {
					errc <- err
					return
				}
			}
		})
	}
	wg.Wait()
	b.StopTimer()
	after := child.sample(b)
	close(errc)
	if err := <-errc; err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(after[0]-before[0])/float64(b.N), "cpu-ns/req")
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

// TestLoadL1L2_wired serves each engine of L1 and L2 in its child for a few
// requests and reads its counters, so a harness that no longer serves its load
// fails the suite, not only the bench the suite never runs.
func TestLoadL1L2_wired(t *testing.T) {
	for _, c := range []struct {
		load, engine string
		req          []byte
	}{{"L1", "goroutine", loadL1Request}, {"L1", "loop", loadL1Request}, {"L2", "handwritten", loadL2Request}, {"L2", "chain", loadL2Request}} {
		t.Run(c.load+"/"+c.engine, func(t *testing.T) {
			child := startLoadChild(t, c.load, c.engine)
			conn, err := stdnet.DialTimeout("tcp", child.addr, 5*time.Second)
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
