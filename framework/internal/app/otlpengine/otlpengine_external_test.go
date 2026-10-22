package otlpengine_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/kitsunium/sdk/framework/internal/app/otlpengine"
	"github.com/kitsunium/sdk/framework/internal/app/plug"
)

// The OTLP/HTTP paths of the two signals.
const (
	tracesPath  = "/v1/traces"
	metricsPath = "/v1/metrics"
)

// A span that ends reaches the collector as an OTLP/JSON request on the
// trace signal's path, the endpoint's own path kept and its headers sent;
// the batch is counted sent and the queue is empty again.
func TestASpanReachesTheCollector(t *testing.T) {
	clk := newTestClock()
	col := newCollector(t, clk)
	cfg := config(clk, col.URL+"/", "")
	cfg.Headers = map[string]string{"Authorization": "Bearer token"}
	fx := openExport(t, cfg)
	fx.span(t, "checkout")
	clk.drive(t, "the span sent", func() bool { return fx.stats(plug.OTLPTraces).Sent == 1 })
	posts := col.postsTo(tracesPath)
	if len(posts) != 1 || !holds(posts, "checkout") || !strings.Contains(posts[0].body, `"shop"`) {
		t.Fatalf("the collector received %+v, want one trace request naming checkout and the service", posts)
	}
	col.mu.Lock()
	auth := col.headers[0].Get("Authorization")
	col.mu.Unlock()
	if auth != "Bearer token" {
		t.Errorf("the request's Authorization is %q", auth)
	}
	if st := fx.stats(plug.OTLPTraces); st.Pending != 0 || st.PendingBytes != 0 || st.Durable {
		t.Errorf("after the send: %+v", st)
	}
}

// A collector that is down is asked again on the backoff, the same batch,
// until it is back: the batch is sent once it answers, after two retries,
// each at least the backoff's first wait after the one before.
func TestACollectorDownThenBackGetsTheBatch(t *testing.T) {
	clk := newTestClock()
	unavailable := answer{status: http.StatusServiceUnavailable}
	col := newCollector(t, clk, unavailable, unavailable, answer{status: http.StatusOK})
	fx := openExport(t, config(clk, col.URL, ""))
	fx.span(t, "checkout")
	clk.drive(t, "the span sent after the collector came back", func() bool { return fx.stats(plug.OTLPTraces).Sent == 1 })
	posts := col.postsTo(tracesPath)
	if len(posts) != 3 || posts[0].body != posts[2].body {
		t.Fatalf("the collector received %d requests, want the same batch three times", len(posts))
	}
	for i := 1; i < len(posts); i++ {
		if gap := posts[i].at.Sub(posts[i-1].at); gap < 5*time.Second {
			t.Errorf("attempt %d came %v after the one before: the backoff starts at 5s", i+1, gap)
		}
	}
	if st := fx.stats(plug.OTLPTraces); st.Retries != 2 || st.DeadLettered != 0 || st.Dropped != 0 {
		t.Errorf("the stats: %+v", st)
	}
}

// A 429 whose Retry-After says forty-five seconds is waited in full: the next
// attempt comes no sooner, and the stats say the delay and when it was
// imposed.
func TestARetryAfterIsHonoured(t *testing.T) {
	clk := newTestClock()
	col := newCollector(t, clk, answer{status: http.StatusTooManyRequests, retryAfter: "45"}, answer{status: http.StatusOK})
	fx := openExport(t, config(clk, col.URL, ""))
	fx.span(t, "checkout")
	clk.drive(t, "the span sent after the Retry-After", func() bool { return fx.stats(plug.OTLPTraces).Sent == 1 })
	posts := col.postsTo(tracesPath)
	if len(posts) != 2 {
		t.Fatalf("the collector received %d requests, want 2", len(posts))
	}
	if gap := posts[1].at.Sub(posts[0].at); gap < 45*time.Second {
		t.Errorf("the second attempt came %v after the first: the collector asked for 45s", gap)
	}
	st := fx.stats(plug.OTLPTraces)
	if st.LastRetryAfter != 45*time.Second || st.LastRetryAfterAt.Before(posts[0].at) || st.LastRetryAfterAt.After(posts[1].at) {
		t.Errorf("the stats say the last Retry-After %v at %v, want 45s between %v and %v", st.LastRetryAfter, st.LastRetryAfterAt, posts[0].at, posts[1].at)
	}
}

// A 400 is the collector refusing the batch for good: it is not sent again,
// and the queue keeps it as a dead letter, counted.
func TestARejectedBatchIsDeadLettered(t *testing.T) {
	clk := newTestClock()
	col := newCollector(t, clk, answer{status: http.StatusBadRequest})
	fx := openExport(t, config(clk, col.URL, ""))
	fx.span(t, "checkout")
	clk.drive(t, "the batch dead-lettered", func() bool { return fx.stats(plug.OTLPTraces).DeadLettered == 1 })
	if n := len(col.postsTo(tracesPath)); n != 1 {
		t.Errorf("the collector received %d requests, want 1: a rejected batch is never sent again", n)
	}
	if st := fx.stats(plug.OTLPTraces); st.Pending != 0 || st.Sent != 0 || st.Retries != 0 {
		t.Errorf("the stats: %+v", st)
	}
}

// Batches queued while the collector is down survive the process: a second
// export over the same directory finds them counted, and sends them once
// the collector is back. Where the SDK's durable queue refuses the platform,
// the export keeps its queues in memory instead, and says so; the case then
// skips, that fallback asserted.
func TestBatchesSurviveARestart(t *testing.T) {
	clk := newTestClock()
	col := newCollector(t, clk, answer{status: http.StatusServiceUnavailable})
	dir := t.TempDir()
	first := openExport(t, config(clk, col.URL, dir))
	if diskRefused() {
		if st := first.stats(plug.OTLPTraces); st.Durable {
			t.Fatalf("the export's queue is durable on %s, whose durable queue the SDK refuses", strings.ToLower(st.Signal))
		}
		t.Skip("the SDK's durable queue refuses this platform by design; the memory fallback is asserted above")
	}
	if st := first.stats(plug.OTLPTraces); !st.Durable {
		t.Fatalf("the export's queue over a directory is not durable: %+v", st)
	}
	first.span(t, "before-restart")
	clk.drive(t, "the batch tried once", func() bool { return len(col.postsTo(tracesPath)) >= 1 })
	first.close(t)
	if st := first.stats(plug.OTLPTraces); st.Pending == 0 {
		t.Fatalf("the first process holds nothing at its stop: %+v", st)
	}
	col.script(answer{status: http.StatusOK})
	second := openExport(t, config(clk, col.URL, dir))
	if st := second.stats(plug.OTLPTraces); st.Pending == 0 || st.PendingBytes == 0 {
		t.Fatalf("the second process found no backlog: %+v", st)
	}
	clk.drive(t, "the backlog sent by the second process", func() bool { return second.stats(plug.OTLPTraces).Sent >= 1 })
	if posts := col.postsTo(tracesPath); !holds(posts[len(posts)-1:], "before-restart") {
		t.Errorf("the second process sent %+v, want the span of the first", posts[len(posts)-1:])
	}
}

// At its stop the export flushes what it holds in memory into its queues: a
// span the batch sink had not exported yet, and one last snapshot of the
// metrics — the units of work the framework recorded, and the export's own
// counts. The collector is down until the stop, so the next process is the
// one that sends them.
func TestTheStopFlushesTheSpansAndTheMetrics(t *testing.T) {
	clk := newTestClock()
	col := newCollector(t, clk, answer{status: http.StatusServiceUnavailable})
	dir := t.TempDir()
	cfg := config(clk, col.URL, dir)
	fx := openExport(t, cfg)
	fx.ex.Record("shop/endpoint/checkout", "request", 120*time.Millisecond, false)
	fx.ex.Record("shop/endpoint/checkout", "request", 80*time.Millisecond, true)
	fx.span(t, "flushed")
	fx.close(t)
	if diskRefused() {
		t.Skip("the SDK's durable queue refuses this platform by design: a stop's flush lands in memory")
	}
	col.script(answer{status: http.StatusOK})
	next := openExport(t, config(clk, col.URL, dir))
	clk.drive(t, "the flushed batches sent", func() bool {
		return next.stats(plug.OTLPTraces).Sent >= 1 && next.stats(plug.OTLPMetrics).Sent >= 1
	})
	if !holds(col.postsTo(tracesPath), "flushed") {
		t.Error("the span the batch sink held at the stop was not sent")
	}
	metrics := col.postsTo(metricsPath)
	for _, name := range []string{"app.node.runs", "app.node.failures", "app.node.duration", "app.otlp.pending", "app.otlp.dead_lettered", "shop/endpoint/checkout"} {
		if !holds(metrics, name) {
			t.Errorf("the metrics sent hold no %s", name)
		}
	}
}

// The product's own sender replaces the network: each batch reaches it with
// its signal, and its failure is classified by the framework's verdict — a
// retryable one tried again, a permanent one dead-lettered.
func TestTheProductsSenderReplacesTheNetwork(t *testing.T) {
	clk := newTestClock()
	permanent := errors.New("refused for good")
	got := make(chan plug.OTLPBatchValue, 16)
	calls := 0
	cfg := config(clk, "", "")
	cfg.Send = func(_ context.Context, b plug.OTLPBatchValue) error {
		got <- b
		calls++
		if calls == 1 {
			return errors.New("try again")
		}
		return nil
	}
	cfg.Retryable = func(err error) bool { return !errors.Is(err, permanent) }
	fx := openExport(t, cfg)
	fx.span(t, "own")
	clk.drive(t, "the batch sent by the product", func() bool { return fx.stats(plug.OTLPTraces).Sent == 1 })
	first := <-got
	if first.Signal != plug.OTLPTraces || !strings.Contains(string(first.Document), `"own"`) {
		t.Errorf("the product's sender got %s %q", first.Signal, first.Document)
	}
	if st := fx.stats(plug.OTLPTraces); st.Retries != 1 {
		t.Errorf("the stats: %+v", st)
	}
	cfg.Send = func(context.Context, plug.OTLPBatchValue) error { return permanent }
	refused := openExport(t, cfg)
	refused.span(t, "refused")
	clk.drive(t, "the batch dead-lettered", func() bool { return refused.stats(plug.OTLPTraces).DeadLettered == 1 })
}

// An endpoint that is no collector's is refused at the open, and so is the
// export's queue where it cannot be made: nothing is left open.
func TestAnExportThatCannotOpenIsRefused(t *testing.T) {
	otlpengine.Enable()
	clk := newTestClock()
	if _, err := plug.OTLP.Load().Open(context.Background(), config(clk, "ftp://collector", "")); err == nil {
		t.Error("an ftp endpoint was accepted")
	}
	cfg := config(clk, "http://127.0.0.1:4318", "")
	cfg.MaxBatches = -1
	if _, err := plug.OTLP.Load().Open(context.Background(), cfg); err == nil {
		t.Error("a negative bound was accepted")
	}
}
