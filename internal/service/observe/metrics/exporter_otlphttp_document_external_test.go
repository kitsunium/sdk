package metrics_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	coremetrics "github.com/kitsunium/sdk/internal/core/observe/metrics"
	svcmetrics "github.com/kitsunium/sdk/internal/service/observe/metrics"
)

// TestOTLPHTTPExportDocumentPostsTheStoredBytes pins the sibling a
// store-and-forward queue posts through (ADR 0185): the exporter is a
// DocumentExporter, the document reaches the collector byte for byte, an
// empty one posts nothing, and a refusal carries the verdict ExportContext
// gives — the code, the retryable class and the collector's Retry-After.
func TestOTLPHTTPExportDocumentPostsTheStoredBytes(t *testing.T) {
	t.Parallel()
	exporter, captured := collectorStub(t, http.StatusOK, "", nil)
	document, ok := exporter.(coremetrics.DocumentExporter)
	if !ok {
		t.Fatal("the OTLP/HTTP exporter is not a DocumentExporter")
	}
	doc := mustEncode(t, gaugeSnapshot(nil, 2.5))
	if err := document.ExportDocument(context.Background(), []byte(doc)); err != nil {
		t.Fatalf("ExportDocument: %v", err)
	}
	if err := document.ExportDocument(context.Background(), nil); err != nil {
		t.Fatalf("an empty document: %v", err)
	}
	seen := captured.snapshot()
	if seen.body != doc || seen.seen != 1 || seen.path != svcmetrics.OTLPMetricsPath {
		t.Errorf("collector saw %d request(s) to %q with %q, want one with the stored document", seen.seen, seen.path, seen.body)
	}

	throttled, _ := collectorStub(t, http.StatusTooManyRequests, "", http.Header{"Retry-After": []string{"30"}})
	err := throttled.(coremetrics.DocumentExporter).ExportDocument(context.Background(), []byte(doc))
	if !errors.Is(err, coremetrics.OTLPExportUnavailable) || !svcmetrics.OTLPRetryable(err) {
		t.Fatalf("a 429 = %v, want the retryable OTLPExportUnavailable", err)
	}
	if !hasField(err, "retry_after_seconds", "30") {
		t.Errorf("the throttling hint must reach the caller: %v", err)
	}
	refused, _ := collectorStub(t, http.StatusBadRequest, "", nil)
	err = refused.(coremetrics.DocumentExporter).ExportDocument(context.Background(), []byte(doc))
	if !errors.Is(err, coremetrics.OTLPExportRejected) || svcmetrics.OTLPRetryable(err) {
		t.Errorf("a 400 = %v, want the permanent OTLPExportRejected", err)
	}
}
