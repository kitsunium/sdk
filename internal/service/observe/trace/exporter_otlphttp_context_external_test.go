package trace_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	coretrace "github.com/kitsunium/sdk/internal/core/observe/trace"
	svctrace "github.com/kitsunium/sdk/internal/service/observe/trace"
)

// : the emitter's constructor hands back the context-aware sibling, so a
// caller reaches ExportContext with no type assertion.
var _ func(coretrace.ExporterName, svctrace.OTLPHTTPConfig) (coretrace.ContextSpanExporter, error) = svctrace.NewOTLPHTTPExporter

// TestOTLPHTTPExportContextAbortsWhenTheContextEnds pins the bound the
// specification asks for: a collector that never answers holds the export
// only until the caller's context ends, and the verdict is the transient one,
// so a retry policy treats the timeout as retryable. No sleep: the handler
// says when the request has arrived, and holds it until the client goes.
func TestOTLPHTTPExportContextAbortsWhenTheContextEnds(t *testing.T) {
	arrived := make(chan struct{})
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		//: read the body, so the server watches the connection for the
		//: client going away, then say the request is here.
		_, _ = io.Copy(io.Discard, r.Body)
		close(arrived)
		//: never answer; the request ends when the client abandons it, or
		//: when the test is over.
		select {
		case <-r.Context().Done():
		case <-released:
		}
	}))
	defer server.Close()
	defer close(released)
	exporter, err := svctrace.NewOTLPHTTPExporter("otlphttp", svctrace.OTLPHTTPConfig{Endpoint: server.URL + svctrace.OTLPTracesPath})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	batch := fixtureSpans(t)
	result := make(chan error, 1)
	go func() { result <- exporter.ExportContext(ctx, batch) }()
	<-arrived
	cancel()
	exportErr := <-result
	if !errors.Is(exportErr, coretrace.OTLPExportUnavailable) {
		t.Fatalf("ExportContext = %v, want OTLPExportUnavailable", exportErr)
	}
	if !errors.Is(exportErr, context.Canceled) {
		t.Errorf("ExportContext = %v, want the context's end on the trail", exportErr)
	}
	if !svctrace.OTLPRetryable(exportErr) {
		t.Error("an export the caller's bound cut short is retryable")
	}
}

// TestOTLPHTTPExportContextRefusesADoneContextWithoutAPost pins that a context
// already done never reaches the collector.
func TestOTLPHTTPExportContextRefusesADoneContextWithoutAPost(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	exporter, err := svctrace.NewOTLPHTTPExporter("otlphttp", svctrace.OTLPHTTPConfig{Endpoint: server.URL + svctrace.OTLPTracesPath})
	if err != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if exportErr := exporter.ExportContext(ctx, fixtureSpans(t)); !errors.Is(exportErr, coretrace.OTLPExportUnavailable) {
		t.Fatalf("ExportContext = %v, want OTLPExportUnavailable", exportErr)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("the collector saw %d request(s), want none", got)
	}
}
