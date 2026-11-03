package forward_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	coreforward "github.com/kitsunium/sdk/internal/core/app/forward"
	corelogger "github.com/kitsunium/sdk/internal/core/observe/logger"
	"github.com/kitsunium/sdk/internal/core/observe/logger/level"
	coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"
	svcforward "github.com/kitsunium/sdk/internal/service/app/forward"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
	"github.com/kitsunium/sdk/internal/service/observe/logger/writer/otlpjson"
)

// TestLogsWaitOutACollectorsRetryAfterOnce (#867 acceptance): a record
// written to the "otlpjson" writer reaches the forwarder through
// NewLogExporter as one OTLP/JSON document; the collector answers 429 with
// Retry-After: 90, the forwarder waits the 90 s on its clock, posts the same
// document again, once, and it is sent.
func TestLogsWaitOutACollectorsRetryAfterOnce(t *testing.T) {
	c := newCollector(t, http.StatusTooManyRequests, http.StatusOK)
	exporter, err := otlpjson.NewOTLPHTTPExporter(otlpjson.OTLPHTTPConfig{Endpoint: c.server.URL + otlpjson.OTLPLogsPath})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	clk := newTimerClock()
	broker, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Clock: clk, Policy: testPolicy})
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	fx := &fixture{clock: clk, broker: broker, sender: newSender()}
	fx.forwarder = mustForwarder(t, svcforward.Config{
		Broker: broker, Send: exporter.ExportDocument, Retryable: otlpjson.OTLPRetryable, Clock: clk,
		SendTimeout: testSendTimeout, MaxAge: testMaxAge, PollInterval: time.Hour,
	})
	durable, err := svcforward.NewLogExporter(fx.forwarder)
	if err != nil {
		t.Fatalf("NewLogExporter: %v", err)
	}
	sink, err := otlpjson.Writer.Open(otlpjson.Config{Exporter: durable})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rec := corelogger.RecordEvent{Time: epoch, Level: level.Warn, Message: "disk almost full"}
	if _, err := sink.Write(context.Background(), rec, []byte("line\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fx.awaitStats(t, "kept", func(s svcforward.StatsValue) bool { return s.Enqueued == 1 })
	want := string(otlpjson.EncodeOTLPJSON(coreotel.ResourceValue{}, coreotel.ScopeValue{}, []corelogger.RecordEvent{rec}))
	fx.run(t)
	if first := c.awaitBody(t); first != want {
		t.Fatalf("the collector got %s\nwant %s", first, want)
	}
	fx.clock.awaitTimer(t, 90*time.Second)
	fx.clock.Advance(90 * time.Second)
	if again := c.awaitBody(t); again != want {
		t.Fatalf("the retry posted %s", again)
	}
	stats := fx.awaitStats(t, "sent", func(s svcforward.StatsValue) bool { return s.Sent == 1 })
	if stats.Retries != 1 || stats.LastRetryAfter != 90*time.Second || stats.Pending != 0 {
		t.Errorf("stats = %+v, want one retry after the collector's 90 s", stats)
	}
	select {
	case body := <-c.bodies:
		t.Fatalf("the batch was posted a third time: %s", body)
	default:
	}
	if _, err := svcforward.NewLogExporter(nil); !errors.Is(err, coreforward.ForwardMisconfigured) {
		t.Errorf("NewLogExporter(nil) = %v, want ForwardMisconfigured", err)
	}
}
