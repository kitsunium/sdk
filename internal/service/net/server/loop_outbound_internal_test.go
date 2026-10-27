// Package server — the loop's port to its workers (R3) through the http1
// adapter's pending answers, on every loop backend present: an answer filled
// off the loop's thread, pipelined answers kept in order behind it, a full
// queue answered 503 with the connection kept and counted, a connection that
// closes first, the holdback ceiling, the misuses, and the drain.
package server

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/http1"
)

// Test_jobRing pins the single-producer single-consumer ring: FIFO, refused
// when full, its size the next power of two.
func Test_jobRing(t *testing.T) {
	t.Parallel()
	r := newJobRing(3)
	if len(r.jobs) != 4 {
		t.Fatalf("size %d, want 4", len(r.jobs))
	}
	for i := range 4 {
		if !r.push(corenet.LoopJobValue{Arg: uint64(i)}) {
			t.Fatalf("push %d refused", i)
		}
	}
	if r.push(corenet.LoopJobValue{}) {
		t.Fatal("a full ring took a fifth job")
	}
	for i := range 4 {
		if j, ok := r.pop(); !ok || j.Arg != uint64(i) {
			t.Fatalf("pop %d: %+v %t", i, j, ok)
		}
	}
	if _, ok := r.pop(); ok {
		t.Fatal("an empty ring popped")
	}
}

// pendingApp answers "/fast" on the loop and "/slow" through its worker, which
// waits for gate when one is set; "/both" asks Pending with Defer.
type pendingApp struct {
	// gate, when not nil, holds every WorkHTTP1 until it is closed.
	gate chan struct{}
	// mu guards bodies.
	mu sync.Mutex
	// bodies are the worker's answers, kept alive until written.
	bodies map[uint64][]byte
	// next numbers the jobs.
	next uint64
	// worked counts the jobs the worker ran.
	worked int
	// started counts the jobs the worker took, before the gate.
	started atomic.Int64
	// deferred are the responses "/defer" deferred, by lane, completed at
	// EndBatch.
	deferred [2][]*corenet.HTTP1ResponseValue
}

func (a *pendingApp) ServeHTTP1(req *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	switch string(req.Path) {
	case "/slow":
		a.mu.Lock()
		a.next++
		res.Pending, res.Job = true, a.next
		a.mu.Unlock()
	case "/both":
		res.Pending, res.Defer = true, true
	case "/defer":
		res.Defer = true
		a.deferred[req.Lane] = append(a.deferred[req.Lane], res)
	default:
		res.Body = []byte("fast" + strings.TrimPrefix(string(req.Path), "/fast"))
	}
}

func (a *pendingApp) EndBatch(lane int) {
	for _, res := range a.deferred[lane] {
		res.Defer, res.Body = false, []byte("defer")
	}
	a.deferred[lane] = a.deferred[lane][:0]
}

func (a *pendingApp) WorkHTTP1(_ int, job uint64, res *corenet.HTTP1ResponseValue) {
	a.started.Add(1)
	if a.gate != nil {
		<-a.gate
	}
	body := []byte("slow-" + strconv.FormatUint(job, 10))
	a.mu.Lock()
	if a.bodies == nil {
		a.bodies = map[uint64][]byte{}
	}
	a.bodies[job] = body
	a.worked++
	a.mu.Unlock()
	res.Body = body
}

// startPending serves app on b with v's Outbound and OutputCap.
func startPending(t *testing.T, b corenet.LoopBackend, app corenet.HTTP1Handler, v corenet.LoopValue) (*Server, string) {
	t.Helper()
	v.Backend, v.Loops = b, 2
	srv := New()
	t.Cleanup(func() { swallowErr(srv.Close()) })
	srv.Group("loop", Listen("tcp", "127.0.0.1:0"), Loop(v)).HandleLoop(http1.HandleLoop(app, http1.Parser{}))
	if err := srv.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return srv, srv.State().Listeners[0].Address
}

// readAnswer reads one answer and returns its status, Retry-After and body.
func readAnswer(t *testing.T, r *bufio.Reader) (int, string, string) {
	t.Helper()
	resp, err := http.ReadResponse(r, nil)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("body: %v", err)
	}
	return resp.StatusCode, resp.Header.Get("Retry-After"), string(body)
}

// get is one GET of path.
func get(path string) string { return "GET " + path + " HTTP/1.1\r\nHost: h\r\n\r\n" }

// TestLoopOutbound_pending pins a pending answer: filled by the worker, off
// the loop's thread, written in its place, and every pipelined answer after
// it — answered on the loop, deferred to the batch's end, or pending — in
// request order on the same connection; Pending with Defer is a 500.
func TestLoopOutbound_pending(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			app := &pendingApp{}
			_, addr := startPending(t, b, app, corenet.LoopValue{})
			c := loopDial(t, addr)
			r := bufio.NewReader(c)
			if _, err := io.WriteString(c, get("/fast1")+get("/slow")+get("/fast2")+get("/defer")+get("/slow")+get("/fast3")); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"fast1", "slow-1", "fast2", "defer", "slow-2", "fast3"} {
				if status, _, body := readAnswer(t, r); status != 200 || body != want {
					t.Fatalf("got %d %q, want %q", status, body, want)
				}
			}
			//: Pending with Defer is a misuse: 500, and the connection closed.
			if _, err := io.WriteString(c, get("/both")); err != nil {
				t.Fatal(err)
			}
			if status, _, _ := readAnswer(t, r); status != 500 {
				t.Fatalf("Pending with Defer: %d", status)
			}
			if _, err := r.ReadByte(); err == nil {
				t.Fatal("the connection stayed open after a 500")
			}
		})
	}
}

// TestLoopOutbound_full pins the full queue: a pending answer the lane's
// queue cannot take is answered 503 with Retry-After at once, in its place,
// the connection kept, and the refusal counted in State.
func TestLoopOutbound_full(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			app := &pendingApp{gate: make(chan struct{})}
			srv, addr := startPending(t, b, app, corenet.LoopValue{Outbound: 1})
			c := loopDial(t, addr)
			r := bufio.NewReader(c)
			//: the first is taken by the worker, which waits on the gate; the
			//: second fills the queue of one; the third is refused.
			if _, err := io.WriteString(c, get("/slow")); err != nil {
				t.Fatal(err)
			}
			if !waitWorking(app) {
				t.Fatal("the worker never took the first job")
			}
			if _, err := io.WriteString(c, get("/slow")+get("/slow")+get("/fast")); err != nil {
				t.Fatal(err)
			}
			if !waitRefused(srv, 1) {
				t.Fatalf("refused %d, want 1", refused(srv))
			}
			close(app.gate)
			for i, want := range []struct {
				status int
				retry  string
				body   string
			}{{200, "", "slow-1"}, {200, "", "slow-2"}, {503, "1", ""}, {200, "", "fast"}} {
				status, retry, body := readAnswer(t, r)
				if status != want.status || retry != want.retry || body != want.body {
					t.Fatalf("answer %d: %d %q %q, want %+v", i, status, retry, body, want)
				}
			}
		})
	}
}

// waitWorking waits until the app's worker holds a job: its queue is empty
// again, the job out.
func waitWorking(app *pendingApp) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if app.started.Load() >= 1 {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// refused is the group's OutboundRefused.
func refused(srv *Server) uint64 {
	return srv.State().Loops[0].OutboundRefused
}

// waitRefused waits until the group counts n refusals.
func waitRefused(srv *Server, n uint64) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if refused(srv) >= n {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// TestLoopOutbound_closedFirst pins a connection that closes while its answer
// is out: the worker's job comes back, nothing is written, the slot is
// reused, and the next connections are served.
func TestLoopOutbound_closedFirst(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			app := &pendingApp{gate: make(chan struct{})}
			_, addr := startPending(t, b, app, corenet.LoopValue{})
			for range 8 {
				c := loopDial(t, addr)
				if _, err := io.WriteString(c, get("/slow")); err != nil {
					t.Fatal(err)
				}
				swallowErr(c.Close())
			}
			close(app.gate)
			for range 4 {
				c := loopDial(t, addr)
				r := bufio.NewReader(c)
				if _, err := io.WriteString(c, get("/slow")+get("/fast")); err != nil {
					t.Fatal(err)
				}
				if status, _, body := readAnswer(t, r); status != 200 || !strings.HasPrefix(body, "slow-") {
					t.Fatalf("after closed ones: %d %q", status, body)
				}
				if _, _, body := readAnswer(t, r); body != "fast" {
					t.Fatalf("after closed ones: %q", body)
				}
			}
		})
	}
}

// TestLoopOutbound_holdCap pins the holdback ceiling: a connection that
// pipelines past OutputCap behind a pending answer is closed.
func TestLoopOutbound_holdCap(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			app := &pendingApp{gate: make(chan struct{})}
			t.Cleanup(func() { close(app.gate) })
			_, addr := startPending(t, b, app, corenet.LoopValue{OutputCap: 4096})
			c := loopDial(t, addr)
			var sb strings.Builder
			sb.WriteString(get("/slow"))
			for range 200 {
				sb.WriteString(get("/fast"))
			}
			if _, err := io.WriteString(c, sb.String()); err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadAll(c); err != nil {
				t.Fatalf("read: %v", err)
			}
		})
	}
}

// pendingOnly marks every answer Pending with no worker to run it.
type pendingOnly struct{}

func (pendingOnly) ServeHTTP1(_ *corenet.HTTP1RequestValue, res *corenet.HTTP1ResponseValue) {
	res.Pending = true
}

// TestLoopOutbound_noWorker pins Pending from a handler that is no
// HTTP1Worker: 500, and the connection closed; no worker is started.
func TestLoopOutbound_noWorker(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			_, addr := startPending(t, b, pendingOnly{}, corenet.LoopValue{})
			c := loopDial(t, addr)
			r := bufio.NewReader(c)
			if _, err := io.WriteString(c, get("/x")); err != nil {
				t.Fatal(err)
			}
			if status, _, _ := readAnswer(t, r); status != 500 {
				t.Fatalf("Pending without a worker: %d", status)
			}
		})
	}
}

// TestLoopOutbound_drain pins Shutdown: an answer still out when the drain
// begins reaches its connection before the workers stop.
func TestLoopOutbound_drain(t *testing.T) {
	for _, b := range loopBackends() {
		t.Run(b.String(), func(t *testing.T) {
			app := &pendingApp{gate: make(chan struct{})}
			srv, addr := startPending(t, b, app, corenet.LoopValue{})
			c := loopDial(t, addr)
			r := bufio.NewReader(c)
			if _, err := io.WriteString(c, "GET /slow HTTP/1.1\r\nHost: h\r\nConnection: close\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			if !waitWorking(app) {
				t.Fatal("the worker never took the job")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			stopped := make(chan error, 1)
			go func() { stopped <- srv.Shutdown(ctx) }()
			time.Sleep(50 * time.Millisecond)
			close(app.gate)
			if status, _, body := readAnswer(t, r); status != 200 || body != "slow-1" {
				t.Fatalf("during the drain: %d %q", status, body)
			}
			if err := <-stopped; err != nil {
				t.Fatalf("Shutdown: %v", err)
			}
		})
	}
}
