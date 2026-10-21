package forward_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	svcforward "github.com/kitsunium/sdk/internal/service/app/forward"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
	svctrace "github.com/kitsunium/sdk/internal/service/observe/trace"
)

// collector is an OTLP/HTTP collector stub whose answers a case scripts:
// each request takes the next status, the last one repeating.
type collector struct {
	mu       sync.Mutex
	statuses []int
	bodies   chan string
	server   *httptest.Server
}

// newCollector starts a collector answering statuses in turn; a 429 carries
// Retry-After: 90.
func newCollector(t *testing.T, statuses ...int) *collector {
	t.Helper()
	c := &collector{statuses: statuses, bodies: make(chan string, 64)}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.bodies <- string(body)
		c.mu.Lock()
		status := c.statuses[0]
		if len(c.statuses) > 1 {
			c.statuses = c.statuses[1:]
		}
		c.mu.Unlock()
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "90")
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(c.server.Close)
	return c
}

// awaitBody returns the next document the collector received.
func (c *collector) awaitBody(t *testing.T) string {
	t.Helper()
	select {
	case body := <-c.bodies:
		return body
	case <-time.After(waitBudget):
		t.Fatal("the collector received nothing")
		return ""
	}
}

// spanBatch is one finished span.
func spanBatch(t *testing.T) coretrace.SpansValue {
	t.Helper()
	traceID, err := coretrace.ParseTraceID("4bf92f3577b34da6a3ce929d0e0e4736")
	if err != nil {
		t.Fatal(err)
	}
	spanID, err := coretrace.ParseSpanID("00f067aa0ba902b7")
	if err != nil {
		t.Fatal(err)
	}
	return coretrace.SpansValue{Spans: []coretrace.SpanValue{{
		Context:   coretrace.SpanContextValue{TraceID: traceID, SpanID: spanID, Flags: coretrace.FlagSampled},
		Name:      "charge",
		Kind:      coretrace.SpanKindInternal,
		StartTime: epoch,
		EndTime:   epoch.Add(time.Millisecond),
	}}}
}

// spanForwarder wires the trace signal as ADR 0185 describes: the OTLP/HTTP
// exporter's ExportDocument as the forwarder's Send, OTLPRetryable as its
// classification, and the producer adapter in front.
func spanForwarder(t *testing.T, endpoint string) (*fixture, coretrace.ContextSpanExporter) {
	t.Helper()
	exporter, err := svctrace.NewOTLPHTTPExporter("otlp", svctrace.OTLPHTTPConfig{Endpoint: endpoint + svctrace.OTLPTracesPath})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	document, ok := exporter.(coretrace.DocumentExporter)
	if !ok {
		t.Fatal("the OTLP/HTTP exporter is not a DocumentExporter")
	}
	clk := newTimerClock()
	broker, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Clock: clk, Policy: testPolicy})
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	fx := &fixture{clock: clk, broker: broker, sender: newSender()}
	fx.forwarder = mustForwarder(t, svcforward.Config{
		Broker: broker, Send: document.ExportDocument, Retryable: svctrace.OTLPRetryable, Clock: clk,
		SendTimeout: testSendTimeout, MaxAge: testMaxAge, PollInterval: time.Hour,
	})
	spans, err := svcforward.NewSpanExporter("otlp-durable", fx.forwarder)
	if err != nil {
		t.Fatalf("NewSpanExporter: %v", err)
	}
	return fx, spans
}

// mustForwarder builds a forwarder or fails the case.
func mustForwarder(t *testing.T, cfg svcforward.Config) *svcforward.Forwarder {
	t.Helper()
	forwarder, err := svcforward.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return forwarder
}

// TestSpansReachTheCollectorAsEncoded pins the trace path end to end: a
// batch exported through the adapter is kept, then posted by the OTLP/HTTP
// exporter byte for byte as EncodeOTLPJSON wrote it.
func TestSpansReachTheCollectorAsEncoded(t *testing.T) {
	c := newCollector(t, http.StatusOK)
	fx, spans := spanForwarder(t, c.server.URL)
	if spans.Name() != "otlp-durable" {
		t.Errorf("Name = %q", spans.Name())
	}
	batch := spanBatch(t)
	if err := spans.ExportContext(context.Background(), batch); err != nil {
		t.Fatalf("ExportContext: %v", err)
	}
	if err := spans.Export(coretrace.SpansValue{}); err != nil || fx.forwarder.Stats().Enqueued != 1 {
		t.Fatalf("an empty batch = %v, want nil and nothing kept", err)
	}
	fx.run(t)
	want, err := svctrace.EncodeOTLPJSON(batch)
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: %v", err)
	}
	if got := c.awaitBody(t); got != string(want) {
		t.Errorf("collector received %s, want %s", got, want)
	}
	fx.awaitStats(t, "sent", func(s svcforward.StatsValue) bool { return s.Sent == 1 })
}

// TestACollectorsRetryAfterAndRefusalAreHonoured pins the OTLP verdicts
// through the real exporter: a 429 with Retry-After: 90 is retried after
// exactly 90 s, a 400 is dead-lettered and never posted again.
func TestACollectorsRetryAfterAndRefusalAreHonoured(t *testing.T) {
	c := newCollector(t, http.StatusTooManyRequests, http.StatusBadRequest)
	fx, spans := spanForwarder(t, c.server.URL)
	if err := spans.Export(spanBatch(t)); err != nil {
		t.Fatalf("Export: %v", err)
	}
	fx.run(t)
	c.awaitBody(t)
	fx.clock.awaitTimer(t, 90*time.Second)
	fx.clock.Advance(90 * time.Second)
	c.awaitBody(t)
	stats := fx.awaitStats(t, "dead-lettered", func(s svcforward.StatsValue) bool { return s.DeadLettered == 1 })
	if stats.LastRetryAfter != 90*time.Second || stats.Retries != 1 || stats.Pending != 0 {
		t.Errorf("stats = %+v, want one retry after the collector's 90 s, then the dead letter", stats)
	}
}

// TestSnapshotsAreKeptAsEncoded pins the metrics adapter: a snapshot is
// encoded as EncodeOTLPJSON writes it and kept.
func TestSnapshotsAreKeptAsEncoded(t *testing.T) {
	fx := newFixture(t, svcforward.Config{})
	metrics, err := svcforward.NewMetricExporter("metrics-durable", fx.forwarder)
	if err != nil {
		t.Fatalf("NewMetricExporter: %v", err)
	}
	if metrics.Name() != "metrics-durable" {
		t.Errorf("Name = %q", metrics.Name())
	}
	snap := coremetrics.SnapshotValue{
		StartTime: epoch, Time: epoch.Add(time.Second),
		Gauges: map[string]coremetrics.GaugeMetricValue{"g": {Points: []coremetrics.GaugeValue{{Value: 2.5}}}},
	}
	if err := metrics.Export(snap); err != nil {
		t.Fatalf("Export: %v", err)
	}
	fx.run(t)
	want, err := svcmetrics.EncodeOTLPJSON(snap)
	if err != nil {
		t.Fatalf("EncodeOTLPJSON: %v", err)
	}
	if got := string(fx.sender.awaitCall(t)); got != string(want) {
		t.Errorf("sent %s, want %s", got, want)
	}
}
