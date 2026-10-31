//go:build linux

// A sparse keep-alive load: many connections open, each asked rarely, the
// whole at a fixed rate a loop serves in batches of one or two completions.
// It is the load a single-vCPU HTTP benchmark puts on the loop engine (ten
// thousand users, about a thousand requests a second), the one the grouped
// wait (ADR 0199, ADR 0202) and the per-request cost are measured against.
// The server runs in a child, as L1's and L2's (load_gate_linux_test.go), and
// the metrics are ITS CPU, its context switches and its loop's batches, each
// divided by the requests of the window, so the client's cost never counts.

package server_test

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	stdnet "net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// The sparse load, fixed in code so two runs measure the same thing.
const (
	// sparseConns is how many connections stay open.
	sparseConns = 10_000
	// sparseRate is the requests a second, over all connections.
	sparseRate = 1_000
	// sparseBody is the answer's size: a JSON object of a few fields.
	sparseBody = 160
	// sparseChildEnv names the child's grouped wait: "default", "off" or a
	// duration.
	sparseChildEnv = "KITSUNIUM_SPARSE_CHILD"
	// sparseCPUEnv, when set, is the CPU list the child is pinned to with
	// taskset, so the client never shares the server's CPU.
	sparseCPUEnv = "KITSUNIUM_SPARSE_CPU"
	// sparseProfileEnv, when set, is the file the child writes its CPU
	// profile to.
	sparseProfileEnv = "KITSUNIUM_SPARSE_PROFILE"
)

// sparseRequest is the request: a GET with a Bearer token, routed by prefix.
var sparseRequest = []byte("GET /api/v1/users/12345 HTTP/1.1\r\nHost: sparse\r\nAuthorization: " + loadToken + "\r\n\r\n")

// sparseAnswer is the body every request is answered.
var sparseAnswer = bytes.Repeat([]byte("s"), sparseBody)

// TestLoadSparseChild is the server half of BenchmarkSparseKeepAlive, and runs only
// in the child it starts: one loop group on the best backend — io_uring where
// the kernel has it —, GOMAXPROCS two and one loop as a single-vCPU deployment
// runs it, the http1 chain of L2. It prints its address, then answers each
// line on stdin with its CPU, context switches, batches and completions,
// until stdin closes.
func TestLoadSparseChild(t *testing.T) {
	spec := os.Getenv(sparseChildEnv)
	if spec == "" {
		t.Skip("the server half of BenchmarkSparseKeepAlive: run by it, in a child")
	}
	v := corenet.LoopValue{
		Backend: corenet.LoopBackendAuto, Loops: 1, SoftConns: 2 * sparseConns,
		Capabilities: []corenet.Capability{corenet.CapabilityURingDeferTaskrun, corenet.CapabilityURingNoSQArray, corenet.CapabilityURingRingFDs},
	}
	switch spec {
	case "default":
	case "off":
		v.BatchWait = corenet.DurationValue(-1)
	default:
		d, err := time.ParseDuration(spec)
		if err != nil {
			t.Fatal(err)
		}
		v.BatchWait = corenet.DurationValue(d)
	}
	srv := server.New()
	g := srv.Group("sparse", server.Listen("tcp", "127.0.0.1:0"), server.Loop(v), server.Plaintext())
	g.HandleLoop(http1.HandleLoop(sparseChain(t), http1.Parser{}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if path := os.Getenv(sparseProfileEnv); path != "" {
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		// a sample every 500 µs, so a short window still shows the batch.
		runtime.SetCPUProfileRate(2000)
		_ = pprof.StartCPUProfile(f)
		defer func() { pprof.StopCPUProfile(); _ = f.Close() }()
	}
	fmt.Println("addr " + srv.State().Listeners[0].Address)
	answerSparse(os.Stdin, os.Stdout, srv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

// sparseChain is L2's chain answering sparseAnswer.
func sparseChain(t testing.TB) corenet.HTTP1Handler {
	routes, err := http1.Routes(http1.RouteValue{ID: loadRouteUsers, Path: string(loadUsersRoute), Prefix: true})
	if err != nil {
		t.Fatal(err)
	}
	bearer, err := http1.Bearer(loadVerifier{})
	if err != nil {
		t.Fatal(err)
	}
	app := corenet.HTTP1HandlerFunc(func(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
		if req.Route != loadRouteUsers {
			res.Status = http.StatusNotFound
			return
		}
		res.Status, res.Body = http.StatusOK, sparseAnswer
	})
	return corenet.Chain[corenet.HTTP1Handler](app, routes, bearer)
}

// answerSparse answers each line read from in with the process's user and
// system CPU in nanoseconds, its context switches, the loop's batches and
// completions, and its busiest thread's time on CPU and times scheduled in.
func answerSparse(in io.Reader, out io.Writer, srv *server.Server) {
	r := bufio.NewReader(in)
	for {
		if _, err := r.ReadSlice('\n'); err != nil {
			return
		}
		var ru syscall.Rusage
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
		var batches, completions uint64
		for _, lp := range srv.State().Loops {
			for _, rs := range lp.RingStates {
				batches += rs.Batches
				completions += rs.Completions
			}
		}
		run, wakes := busiestThread()
		_, _ = fmt.Fprintf(out, "%d %d %d %d %d %d %d\n", ru.Utime.Nano(), ru.Stime.Nano(), ru.Nvcsw+ru.Nivcsw, batches, completions, run, wakes)
	}
}

// busiestThread is the time on CPU and the times scheduled in of the
// process's thread with the most time on CPU — the loop's, under load — from
// /proc/self/task/*/schedstat.
func busiestThread() (run, wakes uint64) {
	dirs, _ := os.ReadDir("/proc/self/task")
	for _, d := range dirs {
		b, err := os.ReadFile("/proc/self/task/" + d.Name() + "/schedstat")
		if err != nil {
			continue
		}
		f := strings.Fields(string(b))
		if len(f) < 3 {
			continue
		}
		r, _ := strconv.ParseUint(f[0], 10, 64)
		w, _ := strconv.ParseUint(f[2], 10, 64)
		if r > run {
			run, wakes = r, w
		}
	}
	return run, wakes
}

// startSparseChild re-runs this binary on TestLoadSparseChild with the
// grouped wait spec.
func startSparseChild(b testing.TB, spec string) *loadChild {
	b.Helper()
	args := []string{os.Args[0], "-test.run=^TestLoadSparseChild$", "-test.count=1"}
	if cpus := os.Getenv(sparseCPUEnv); cpus != "" {
		args = append([]string{"taskset", "-c", cpus}, args...)
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = append(os.Environ(), sparseChildEnv+"="+spec, "GOMAXPROCS=2")
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
		b.Fatalf("child %s printed %q, %v, want its address", spec, line, err)
	}
	c.addrs = []string{addr}
	return c
}

// sparsePool is the open connections not waiting for an answer.
type sparsePool struct {
	mu      sync.Mutex
	idle    []int
	conns   []stdnet.Conn
	readers []*bufio.Reader
	rnd     *rand.Rand
}

// dialSparse opens sparseConns connections to addr and serves one round trip
// on each, so every one is accepted and served before the window.
func dialSparse(b *testing.B, addr string) *sparsePool {
	p := &sparsePool{rnd: rand.New(rand.NewPCG(1, 2))}
	b.Cleanup(func() {
		for _, c := range p.conns {
			_ = c.Close()
		}
	})
	for i := range sparseConns {
		c, err := stdnet.DialTimeout("tcp", addr, 5*time.Second)
		if err != nil {
			b.Fatalf("dial %d: %v", i, err)
		}
		p.conns = append(p.conns, c)
		p.readers = append(p.readers, bufio.NewReaderSize(c, 1024))
		p.idle = append(p.idle, i)
		if err := sparseRoundTrip(c, p.readers[i]); err != nil {
			b.Fatal(err)
		}
	}
	return p
}

// take removes a connection at random from the idle ones.
func (p *sparsePool) take() (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.idle) == 0 {
		return 0, false
	}
	j := p.rnd.IntN(len(p.idle))
	i := p.idle[j]
	p.idle[j] = p.idle[len(p.idle)-1]
	p.idle = p.idle[:len(p.idle)-1]
	return i, true
}

// put gives a connection back.
func (p *sparsePool) put(i int) {
	p.mu.Lock()
	p.idle = append(p.idle, i)
	p.mu.Unlock()
}

// BenchmarkSparseKeepAlive sends b.N requests at sparseRate over sparseConns
// kept-alive connections, each to a connection chosen at random among those
// not waiting, and reports the child's CPU, context switches and loop batches
// a request, the completions a batch, and its loop thread's time on CPU and
// wakeups a request: with the grouped wait as Start resolves it (250 µs),
// off, and bounded at 1 ms and 4 ms.
//
// Its name keeps it out of the merge gate's BenchmarkLoad pattern: it is a
// measure, not a paired gate, and takes minutes a variant.
func BenchmarkSparseKeepAlive(b *testing.B) {
	for _, spec := range []string{"default", "off", "1ms", "4ms"} {
		b.Run("wait="+spec, func(b *testing.B) { benchSparse(b, spec) })
	}
}

// benchSparse is one sub-benchmark: one child, one window of b.N requests.
func benchSparse(b *testing.B, spec string) {
	child := startSparseChild(b, spec)
	p := dialSparse(b, child.addrs[0])
	errc := make(chan error, 1)
	var wg sync.WaitGroup
	b.ResetTimer()
	before := child.sample(b)
	start := time.Now()
	//: request k leaves at start + k/sparseRate, whatever the answers do.
	for k := range b.N {
		if d := time.Until(start.Add(time.Duration(k) * time.Second / sparseRate)); d > 0 {
			time.Sleep(d)
		}
		i, ok := p.take()
		if !ok {
			b.Fatal("every connection is waiting")
		}
		wg.Go(func() {
			if err := sparseRoundTrip(p.conns[i], p.readers[i]); err != nil {
				select {
				case errc <- err:
				default:
				}
			}
			p.put(i)
		})
	}
	wg.Wait()
	after := child.sample(b)
	b.StopTimer()
	select {
	case err := <-errc:
		b.Fatal(err)
	default:
	}
	n := float64(b.N)
	user, sys := float64(after[0]-before[0])/n, float64(after[1]-before[1])/n
	b.ReportMetric(user+sys, "cpu-ns/req")
	b.ReportMetric(user, "user-ns/req")
	b.ReportMetric(sys, "sys-ns/req")
	b.ReportMetric(float64(after[2]-before[2])/n, "switches/req")
	b.ReportMetric(float64(after[3]-before[3])/n, "batches/req")
	b.ReportMetric(float64(after[4]-before[4])/max(float64(after[3]-before[3]), 1), "completions/batch")
	b.ReportMetric(float64(after[5]-before[5])/n, "loop-ns/req")
	b.ReportMetric(float64(after[6]-before[6])/n, "loop-wakes/req")
}

// sparseRoundTrip writes the request on c and reads one 200 answer of
// sparseBody bytes.
func sparseRoundTrip(c stdnet.Conn, br *bufio.Reader) error {
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write(sparseRequest); err != nil {
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
	if length != sparseBody {
		return fmt.Errorf("Content-Length %d, want %d", length, sparseBody)
	}
	_, err = br.Discard(length)
	return err
}

// TestLoadSparse_wired serves a few requests through the sparse child and
// reads its counters, so a harness that no longer serves its load fails the
// suite, not only the bench the suite never runs.
func TestLoadSparse_wired(t *testing.T) {
	child := startSparseChild(t, "default")
	before := child.sample(t)
	conn, err := stdnet.DialTimeout("tcp", child.addrs[0], 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	br := bufio.NewReader(conn)
	for range 3 {
		if err := sparseRoundTrip(conn, br); err != nil {
			t.Fatal(err)
		}
	}
	after := child.sample(t)
	if len(after) != 7 || after[3] <= before[3] || after[4] <= before[4] {
		t.Fatalf("counters %v then %v: want seven, the batches and completions grown", before, after)
	}
}
