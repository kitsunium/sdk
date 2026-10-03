package kit_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/framework/model"
)

// The daemon describes itself: its phases, its process, its HTTP server, and
// who wrote each of its loops.
func TestTheDaemonDescribesItself(t *testing.T) {
	app := startBench(t)
	for range 3 {
		call(t, app, "GET /search?q=x", noBody)
	}
	var g model.Graph
	call(t, app, "GET /_kit/api/graph", noBody).json(t, &g)
	rt := g.Runtime
	if rt == nil {
		t.Fatal("no runtime")
	}
	var phases []string
	for _, h := range rt.History {
		phases = append(phases, h.Phase)
		if h.At.IsZero() {
			t.Errorf("a phase change without its time: %+v", h)
		}
	}
	if !slices.Equal(phases, []string{model.PhaseStarting, model.PhaseServing}) {
		t.Errorf("history %v", phases)
	}

	p := rt.Process
	if p == nil || p.PID != os.Getpid() || p.Goroutines < 5 || p.HeapBytes == 0 || p.HeapObjects == 0 ||
		p.TotalAllocBytes < p.HeapBytes || p.MaxProcs < 1 || p.CPUs < 1 || p.UptimeMs <= 0 ||
		p.OS != runtime.GOOS || p.Arch != runtime.GOARCH || p.Go != runtime.Version() || p.At.IsZero() {
		t.Fatalf("process %+v", p)
	}
	if runtime.GOOS != "windows" && p.CPUSeconds <= 0 {
		t.Errorf("no CPU time: %+v", p)
	}
	// All the memory the runtime holds covers the heap; the next collection
	// starts above the live heap.
	if p.MemoryBytes < p.HeapBytes || p.HeapGoalBytes == 0 {
		t.Errorf("memory %d, heap %d, goal %d", p.MemoryBytes, p.HeapBytes, p.HeapGoalBytes)
	}
	runtime.GC()
	var polled model.Process
	if r := call(t, app, "GET /_kit/api/process", noBody); r.status != http.StatusOK {
		t.Fatalf("process route: %d", r.status)
	} else {
		r.json(t, &polled)
	}
	if polled.PID != p.PID || !polled.At.After(p.At) && !polled.At.Equal(p.At) {
		t.Errorf("polled %+v", polled)
	}
	// A collection ran just before the poll: the sample says when.
	if polled.LastGC == nil || polled.LastGC.Before(p.At.Add(-time.Minute)) || polled.GCCycles <= p.GCCycles {
		t.Errorf("after a collection: last GC %v, cycles %d → %d", polled.LastGC, p.GCCycles, polled.GCCycles)
	}

	h := rt.HTTP
	if h == nil || h.Library != "sdk/v1/net/server" || !strings.Contains(h.Loop, "accepts") || h.Addr != strings.TrimPrefix(app.URL(), "http://") {
		t.Fatalf("http %+v", h)
	}
	var chain []string
	for _, m := range h.Middleware {
		chain = append(chain, m.Kind)
	}
	if !slices.Equal(chain, []string{"observe", "recover", "csrf", "mux", "hostguard"}) {
		t.Errorf("middleware, outermost first: %v", chain)
	}
	if h.Timeouts["readHeader"] != "10s" || h.Timeouts["write"] != "1m0s" || h.Timeouts["idle"] != "2m0s" {
		t.Errorf("timeouts %v", h.Timeouts)
	}
	if h.Conns["total"] < 1 || h.Served < 3 || h.InFlight < 1 {
		t.Errorf("counters: conns %v, served %d, in flight %d (the graph's own request is)", h.Conns, h.Served, h.InFlight)
	}

	want := map[string][3]string{ // name → kind, provenance, library
		"http":                               {model.LoopHTTP, model.ProvenanceLibrary, "sdk/v1/net/server"},
		"scheduler":                          {model.LoopScheduler, model.ProvenanceLibrary, "sdk/v1/app/scheduler"},
		"audit/job/tally":                    {model.LoopJob, model.ProvenanceLibrary, "sdk/v1/app/scheduler"},
		"shop/workflow/lifecycle timers":     {model.LoopTimer, model.ProvenanceLibrary, "sdk/v1/app/statemachine"},
		"audit/subscription/record consumer": {model.LoopConsumer, model.ProvenanceLibrary, "sdk/v1/data/queue"},
	}
	for _, l := range rt.Loops {
		w, ok := want[l.Name]
		if !ok {
			continue
		}
		delete(want, l.Name)
		if l.Kind != w[0] || l.Provenance != w[1] || l.Library != w[2] || l.State == "" {
			t.Errorf("loop %s: %+v", l.Name, l)
		}
		if l.Name == "http" && l.Runs < 1 {
			t.Errorf("the accept loop counts connections: %+v", l)
		}
	}
	if len(want) > 0 {
		t.Errorf("loops missing: %v", want)
	}
}

// Every loop's state is live: running while it runs, waiting after, stopped
// once the app is down.
func TestLoopStatesAreLive(t *testing.T) {
	app := startBench(t)
	stream := subscribe(t, app)
	it := create(t, app, "stateful", 1)
	awaitEvent(t, stream, "the consumer running", func(e model.Event) bool {
		return e.Type == model.EventLoop && e.Loop.Name == "audit/subscription/record consumer" && e.Loop.State == model.LoopRunning
	})
	ev := awaitEvent(t, stream, "the consumer waiting", func(e model.Event) bool {
		return e.Type == model.EventLoop && e.Loop.Name == "audit/subscription/record consumer" && e.Loop.State == model.LoopWaiting
	})
	if ev.Loop.LastWake != model.WakeTopic || ev.Loop.Runs < 1 {
		t.Errorf("after a delivery %+v", ev.Loop)
	}
	// A draft can make nothing due: its creation leaves the workflow's own
	// loop asleep. Published, the item is in a state a timer and a guard
	// leave: the write wakes the loop, which then waits for the next write or
	// the next transition due.
	if r := call(t, app, "POST /items/"+it.ID+"/publish", noBody); r.status != http.StatusOK {
		t.Fatalf("publish: %d %s", r.status, r.body)
	}
	awaitEvent(t, stream, "the workflow's loop woken by the write", func(e model.Event) bool {
		return e.Type == model.EventLoop && e.Loop.Name == "shop/workflow/lifecycle timers" && e.Loop.LastWake == model.WakeChange && e.Loop.State == model.LoopWaiting
	})
	// The scheduler waits for its one job, due in a minute.
	if l := loopOf(t, app, "scheduler"); l.State != model.LoopWaiting || l.NextRun == nil {
		t.Errorf("the scheduler %+v", l)
	}
}

// A signal is the reason of the drain it starts, in the daemon's history.
//
// Goroutine lifecycle: one goroutine runs the app and reports on a buffered
// channel; the signal ends the run, and the test waits for it.
func TestHistoryNamesTheSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no SIGTERM to send")
	}
	app := kit.NewApp("bench", Bench, Shop, Audit).With(kit.InMemory(), kit.Listen("127.0.0.1:0"), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard))
	done := make(chan error, 1)
	go func() { done <- app.Run(context.Background()) }()
	eventually(t, "the product serving", func() bool { return app.URL() != "" && app.Graph().Runtime != nil })
	self, selfErr := os.FindProcess(os.Getpid())
	if selfErr != nil {
		t.Fatal(selfErr)
	}
	if err := self.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SIGTERM did not drain the product")
	}
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	var got []string
	for _, h := range app.Graph().Runtime.History {
		got = append(got, h.Phase+":"+h.Reason)
	}
	want := []string{"starting:", "serving:", "draining:SIGTERM", "stopped:", "starting:", "serving:"}
	if !slices.Equal(got, want) {
		t.Fatalf("history %v, want %v", got, want)
	}
}

// A start that fails says why, in kit's words rather than the error's.
func TestHistoryNamesAFailedStart(t *testing.T) {
	busy, busyErr := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if busyErr != nil {
		t.Fatal(busyErr)
	}
	app := kit.NewApp("bench", Bench, Shop, Audit).With(kit.InMemory(), kit.Listen(busy.Addr().String()), kit.Env(kit.EnvDev), kit.Analyze(false), kit.Logs(io.Discard))
	if err := app.Start(t.Context()); err == nil {
		t.Fatal("started on a taken address")
	}
	busy.Close()
	if err := app.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer app.Stop(context.Background())
	var got []string
	for _, h := range app.Graph().Runtime.History {
		got = append(got, h.Phase+":"+h.Reason)
	}
	want := []string{"starting:", "failed:component http failed", "starting:", "serving:"}
	if !slices.Equal(got, want) {
		t.Fatalf("history %v, want %v", got, want)
	}
}
