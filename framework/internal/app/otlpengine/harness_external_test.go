package otlpengine_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/otlpengine"
	"github.com/kitsunium/sdk/framework/internal/app/plug"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
)

// The numbers every case shares. An attempt, a lease and a settle are
// bounded far beyond anything a case advances, so none ends inside one; the
// driver advances the clock past every wait from floor to ceiling — the
// backoff, a Retry-After and the batch sink's delay among them —, but for
// the batch sink's export bound, exportBound, and never past an attempt's
// bound, a lease's or a settle's. A consumer's idle poll, below floor, is
// never advanced: a batch queued in the process wakes its consumer at once,
// and a clock moved by the polls would reach the export bound while a slow
// machine is still queueing the batch.
const (
	floor           time.Duration = time.Second
	exportBound     time.Duration = 30 * time.Second
	testSettle      time.Duration = time.Hour
	testSendTimeout time.Duration = 10 * time.Hour
	testMaxAge      time.Duration = 1000 * time.Hour
	testInterval    time.Duration = 100 * time.Hour
	ceiling         time.Duration = 10 * time.Minute
	// waitBudget turns a regression into a failure rather than a hang; it is
	// read on the wall clock, never slept on.
	waitBudget time.Duration = 30 * time.Second
)

// epoch anchors every clock, so an instant in a failure is readable.
var epoch = time.Date(2026, time.October, 22, 9, 0, 0, 0, time.UTC)

// testClock is a manual clock, as the framework hands the app's clock to the
// export, that reports every wait armed on it — a timer made or reset, an
// After —, so the driver advances it exactly when something waits.
type testClock struct {
	*clock.ManualClock
	armed chan time.Duration
}

// newTestClock is a testClock at epoch.
func newTestClock() *testClock {
	return &testClock{ManualClock: clock.NewManualClock(epoch), armed: make(chan time.Duration, 1<<16)}
}

// report says a wait of d was armed.
func (c *testClock) report(d time.Duration) {
	select {
	case c.armed <- d:
	default:
	}
}

// NewTimer arms a timer and reports it; its Reset reports too.
func (c *testClock) NewTimer(d time.Duration) plug.Timer {
	timer := &reportedTimer{Timer: c.ManualClock.NewTimer(d), c: c}
	c.report(d)
	return timer
}

// NewTicker is the manual clock's ticker.
func (c *testClock) NewTicker(d time.Duration) plug.Ticker { return c.ManualClock.NewTicker(d) }

// After arms a wait and reports it.
func (c *testClock) After(d time.Duration) <-chan time.Time {
	ch := c.ManualClock.After(d)
	c.report(d)
	return ch
}

// reportedTimer is a timer whose Reset is reported.
type reportedTimer struct {
	clock.Timer
	c *testClock
}

// Reset re-arms the timer and reports it.
func (t *reportedTimer) Reset(d time.Duration) bool {
	active := t.Timer.Reset(d)
	t.c.report(d)
	return active
}

// drive advances the clock past each wait from floor to ceiling as it is
// armed, until done holds; it fails the test once waitBudget passed on the
// wall clock. It never sleeps: between two waits it yields.
func (c *testClock) drive(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitBudget)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatalf("%s: not reached within %v", what, waitBudget)
		}
		select {
		case d := <-c.armed:
			if d >= floor && d < ceiling && d != exportBound {
				c.Advance(d)
			}
		default:
			runtime.Gosched()
		}
	}
}

// answer is one reply of the collector: a status and its Retry-After.
type answer struct {
	status     int
	retryAfter string
}

// post is one request the collector received: its path, its body, and the
// instant it arrived on the clock.
type post struct {
	path string
	body string
	at   time.Time
}

// collector is an OTLP/HTTP collector that answers from a script, the last
// answer repeating, and records every request.
type collector struct {
	*httptest.Server
	clock   *testClock
	mu      sync.Mutex
	answers []answer
	posts   []post
	headers []http.Header
}

// newCollector starts a collector answering answers in turn.
func newCollector(t *testing.T, clk *testClock, answers ...answer) *collector {
	t.Helper()
	c := &collector{clock: clk, answers: answers}
	c.Server = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.Close)
	return c
}

// serve records the request and answers the next scripted reply.
func (c *collector) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	c.mu.Lock()
	c.posts = append(c.posts, post{path: r.URL.Path, body: string(body), at: c.clock.Now()})
	c.headers = append(c.headers, r.Header.Clone())
	a := answer{status: http.StatusOK}
	if len(c.answers) > 0 {
		a = c.answers[0]
		if len(c.answers) > 1 {
			c.answers = c.answers[1:]
		}
	}
	c.mu.Unlock()
	if a.retryAfter != "" {
		w.Header().Set("Retry-After", a.retryAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(a.status)
	_, _ = io.WriteString(w, "{}")
}

// script replaces the collector's answers.
func (c *collector) script(answers ...answer) {
	c.mu.Lock()
	c.answers = answers
	c.mu.Unlock()
}

// postsTo are the requests the collector received on path, in order.
func (c *collector) postsTo(path string) []post {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []post
	for _, p := range c.posts {
		if p.path == path {
			out = append(out, p)
		}
	}
	return out
}

// exportFixture is one export open over a collector, its loop running.
type exportFixture struct {
	ex   plug.OTLPExporter
	cfg  *plug.OTLPConfig
	stop context.CancelFunc
	done chan error
}

// config is the configuration every case starts from: the collector's
// endpoint, the clock, bounds no case reaches but where it says.
func config(clk *testClock, endpoint, dir string) *plug.OTLPConfig {
	return &plug.OTLPConfig{
		Clock: clk, Service: "shop", Version: "test", Endpoint: endpoint, Dir: dir,
		WhenFull: plug.OTLPDropOldest, MaxBatches: 100, MaxBytes: 16 << 20,
		MaxAge: testMaxAge, SendTimeout: testSendTimeout, Interval: testInterval,
		SettleTimeout: testSettle,
	}
}

// openExport opens the export cfg describes through the plugged driver and
// runs its loop until the case ends.
func openExport(t *testing.T, cfg *plug.OTLPConfig) *exportFixture {
	t.Helper()
	otlpengine.Enable()
	ex, err := plug.OTLP.Load().Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	fx := &exportFixture{ex: ex, cfg: cfg, stop: stop, done: make(chan error, 1)}
	go func() { fx.done <- ex.Run(ctx) }()
	t.Cleanup(func() { fx.close(t) })
	return fx
}

// close stops the loop and waits for it, once.
func (fx *exportFixture) close(t *testing.T) {
	t.Helper()
	if fx.stop == nil {
		return
	}
	fx.stop()
	fx.stop = nil
	// The stop's flush waits on the clock for at most its settle: drive it.
	clk, _ := fx.cfg.Clock.(*testClock)
	var err error
	finished := false
	clk.drive(t, "the export's stop", func() bool {
		select {
		case err = <-fx.done:
			finished = true
		default:
		}
		return finished
	})
	if err != nil {
		t.Errorf("Run: %v", err)
	}
}

// span ends one span named name into the export's sink, on the clock.
func (fx *exportFixture) span(t *testing.T, name string) {
	t.Helper()
	sink, ok := fx.ex.SpanSink().Sink.(trace.SpanSink)
	if !ok {
		t.Fatalf("SpanSink holds a %T, not a trace.SpanSink", fx.ex.SpanSink().Sink)
	}
	clk, _ := fx.cfg.Clock.(*testClock)
	tracer := trace.NewTracer(trace.TracerConfig{Sink: sink, Clock: clk.ManualClock})
	_, sp := tracer.Start(context.Background(), name, trace.SpanParams{Kind: trace.KindServer})
	sp.End()
}

// stats is the signal's stats.
func (fx *exportFixture) stats(signal string) plug.OTLPStatsValue {
	for _, st := range fx.ex.Stats() {
		if st.Signal == signal {
			return st
		}
	}
	return plug.OTLPStatsValue{}
}

// holds reports whether the posts carry name.
func holds(posts []post, name string) bool {
	for _, p := range posts {
		if strings.Contains(p.body, `"`+name+`"`) {
			return true
		}
	}
	return false
}

// diskRefused reports whether the SDK's durable queue refuses this GOOS by
// design (ADR 0018, ADR 0056, ADR 0144) — spelled out, as the SDK's own
// suites do, so a refusal on Linux, macOS or a BSD fails instead of skipping.
func diskRefused() bool {
	switch runtime.GOOS {
	case "linux", "android", "darwin", "ios", "freebsd", "openbsd", "netbsd", "dragonfly":
		return false
	}
	return true
}
